package transfers

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"ps3mgr/internal/domain"
	"ps3mgr/internal/filelock"
)

type conversionJournal struct {
	path string
	lock *os.File
	err  error
}

type conversionRecord struct {
	domain.Transfer
	Source string `json:"source"`
}

type conversionQueueState struct {
	Paused  bool               `json:"paused"`
	Jobs    []conversionRecord `json:"jobs"`
	Pending []string           `json:"pending"`
}

// RestoreConversions enables durable state for the server's conversion worker.
// The standalone CLI uses its own transient worker.
func (m *Manager) RestoreConversions(path string, recoverImage func(domain.Transfer) string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.journal != nil {
		return m.journal.err
	}
	if m.closed || m.convert == nil || len(m.order) != 0 {
		return fmt.Errorf("conversion persistence must be initialized before submitting jobs")
	}
	m.journal = &conversionJournal{path: path}
	if err := m.restoreConversionsLocked(recoverImage); err != nil {
		m.journal.err = fmt.Errorf("restore PS5 conversion queue %s: %w", path, err)
		return m.journal.err
	}
	m.cond.Broadcast()
	return nil
}

func (m *Manager) restoreConversionsLocked(recoverImage func(domain.Transfer) string) error {
	journal := m.journal
	if _, err := os.Lstat(journal.path); errors.Is(err, fs.ErrNotExist) {
		return nil // Browse-only deployments can use a read-only PS5 library.
	} else if err != nil {
		return err
	}
	if err := journal.acquire(); err != nil {
		return err
	}
	state, err := journal.read()
	if err != nil {
		journal.lock.Close()
		journal.lock = nil
		return err
	}
	m.paused = state.Paused
	for _, record := range state.Jobs {
		item := record.Transfer
		item.Game.LocalPath = filepath.Join(filepath.Dir(journal.path), record.Source)
		m.items[item.ID] = &item
		m.order = append(m.order, item.ID)
	}
	for _, id := range state.Pending {
		m.pending = append(m.pending, id)
	}
	changed := false
	for _, item := range m.items {
		if item.State != domain.QueueConverting {
			continue
		}
		changed = true
		now := time.Now()
		item.FinishedAt = &now
		if filename := recoverImage(*item); filename != "" {
			item.State, item.Filename, item.Percentage = domain.QueueCompleted, filename, 100
		} else {
			item.State = domain.QueueFailed
			item.Error = "conversion interrupted by server restart; retry to rebuild the image"
		}
	}
	if changed {
		if err := m.saveConversionQueueLocked(); err != nil {
			m.paused = true
			m.reportQueueSaveError(err)
		}
	}
	return nil
}

func (j *conversionJournal) acquire() error {
	lock, err := filelock.Open(j.path + ".lock")
	if err != nil {
		return fmt.Errorf("open conversion queue lock (check library permissions and other running servers): %w", err)
	}
	j.lock = lock
	return nil
}

func (j *conversionJournal) read() (conversionQueueState, error) {
	var state conversionQueueState
	info, err := os.Lstat(j.path)
	if err != nil {
		return state, err
	}
	if !info.Mode().IsRegular() {
		return state, fmt.Errorf("queue journal must be a regular file")
	}
	data, err := os.ReadFile(j.path)
	if err != nil {
		return state, err
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return state, err
	}
	ids := make(map[string]domain.QueueState, len(state.Jobs))
	waiting := 0
	for _, record := range state.Jobs {
		item := record.Transfer
		if item.ID == "" || ids[item.ID] != "" || !filepath.IsLocal(record.Source) || filepath.Clean(record.Source) == "." ||
			item.Platform != domain.PlatformPS5 || item.Direction != domain.TransferConversion {
			return state, fmt.Errorf("invalid conversion job in queue journal")
		}
		switch item.State {
		case domain.QueueWaiting:
			waiting++
		case domain.QueueConverting, domain.QueueCompleted, domain.QueueFailed, domain.QueueCancelled:
		default:
			return state, fmt.Errorf("invalid conversion job state %q", item.State)
		}
		ids[item.ID] = item.State
	}
	for _, id := range state.Pending {
		if ids[id] != domain.QueueWaiting {
			return state, fmt.Errorf("invalid pending job in queue journal")
		}
		delete(ids, id)
		waiting--
	}
	if waiting != 0 {
		return state, fmt.Errorf("waiting jobs missing from pending queue")
	}
	return state, nil
}

func (m *Manager) saveConversionQueueLocked() error {
	journal := m.journal
	if journal == nil {
		return nil
	}
	if journal.err != nil {
		return journal.err
	}
	if journal.lock == nil {
		if err := journal.acquire(); err != nil {
			return err
		}
		if _, err := os.Lstat(journal.path); !errors.Is(err, fs.ErrNotExist) {
			journal.lock.Close()
			journal.lock = nil
			if err != nil {
				return err
			}
			return fmt.Errorf("another server saved a conversion queue; restart this server to load it")
		}
	}
	state := conversionQueueState{Paused: m.paused}
	for _, id := range m.order {
		item := m.items[id]
		source, err := filepath.Rel(filepath.Dir(journal.path), item.Game.LocalPath)
		if err != nil || !filepath.IsLocal(source) || source == "." {
			return fmt.Errorf("conversion source is outside the queue library")
		}
		state.Jobs = append(state.Jobs, conversionRecord{Transfer: transferSnapshot(*item), Source: source})
	}
	for _, id := range m.pending {
		if item := m.items[id]; item != nil && item.State == domain.QueueWaiting {
			state.Pending = append(state.Pending, id)
		}
	}
	if err := journal.write(state); err != nil {
		return fmt.Errorf("save PS5 conversion queue %s: %w", journal.path, err)
	}
	return nil
}

func (j *conversionJournal) write(state conversionQueueState) error {
	file, err := os.CreateTemp(filepath.Dir(j.path), ".ps3mgr-conversion-queue-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	err = json.NewEncoder(file).Encode(state)
	if err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(file.Name(), j.path)
}
