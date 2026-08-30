package transfers

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"ps3mgr/internal/domain"
	ps3ftp "ps3mgr/internal/ftp"
)

type Uploader interface {
	UploadGame(ctx context.Context, ip string, game domain.Game, remoteRoot string, progress func(ps3ftp.Progress)) error
}

type Downloader interface {
	DownloadGame(ctx context.Context, ip string, game domain.Game, localRoot string, progress func(ps3ftp.Progress)) error
}

type Publisher interface {
	Publish(eventType string, payload any)
}

type Options struct {
	StopOnError bool
}

type Manager struct {
	mu          sync.Mutex
	cond        *sync.Cond
	uploader    Uploader
	downloader  Downloader
	localRoot   string
	events      Publisher
	remoteRoot  string
	items       map[string]*domain.Transfer
	order       []string
	pending     []string
	stopOnError map[string]bool
	paused      bool
	closed      bool
	activeID    string
	activeStop  context.CancelFunc
	rootCtx     context.Context
	rootStop    context.CancelFunc
	done        chan struct{}
	sequence    atomic.Uint64
	platform    domain.Platform
	eventPrefix string
	direction   domain.TransferDirection
}

func New(uploader Uploader, publisher Publisher, remoteRoot string) *Manager {
	return newManager(uploader, publisher, remoteRoot, "", "")
}

func NewPlatform(uploader Uploader, publisher Publisher, remoteRoot string, platform domain.Platform) *Manager {
	return newManager(uploader, publisher, remoteRoot, platform, string(platform))
}

func NewDownload(downloader Downloader, publisher Publisher, localRoot string, platform domain.Platform) *Manager {
	m := newManager(nil, publisher, "", platform, string(platform)+".pull")
	m.downloader = downloader
	m.localRoot = localRoot
	m.direction = domain.TransferDownload
	return m
}

func newManager(uploader Uploader, publisher Publisher, remoteRoot string, platform domain.Platform, eventPrefix string) *Manager {
	ctx, cancel := context.WithCancel(context.Background())
	m := &Manager{
		uploader: uploader, events: publisher, remoteRoot: remoteRoot,
		items: make(map[string]*domain.Transfer), stopOnError: make(map[string]bool),
		rootCtx: ctx, rootStop: cancel, done: make(chan struct{}),
		platform: platform, eventPrefix: eventPrefix, direction: domain.TransferUpload,
	}
	m.cond = sync.NewCond(&m.mu)
	go m.run()
	return m
}

func (m *Manager) Enqueue(games []domain.Game, consoleIP string, options Options) ([]domain.Transfer, error) {
	if len(games) == 0 {
		return nil, fmt.Errorf("at least one game is required")
	}
	now := time.Now()
	queueID := m.id("queue")
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, fmt.Errorf("transfer manager is shutting down")
	}
	m.stopOnError[queueID] = options.StopOnError
	created := make([]domain.Transfer, 0, len(games))
	for _, game := range games {
		item := &domain.Transfer{
			ID: m.id("transfer"), QueueID: queueID, ConsoleIP: consoleIP,
			Game: game, Platform: m.platform, Direction: m.direction, State: domain.QueueWaiting, TotalBytes: game.Size, CreatedAt: now,
		}
		m.items[item.ID] = item
		m.order = append(m.order, item.ID)
		m.pending = append(m.pending, item.ID)
		created = append(created, *item)
	}
	m.cond.Broadcast()
	m.publish("queue.created", map[string]any{"queue_id": queueID, "platform": m.platform, "items": created})
	return created, nil
}

func (m *Manager) List() []domain.Transfer {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]domain.Transfer, 0, len(m.order))
	for _, id := range m.order {
		if item := m.items[id]; item != nil {
			result = append(result, *item)
		}
	}
	return result
}

func (m *Manager) Get(id string) (domain.Transfer, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	item, ok := m.items[id]
	if !ok {
		return domain.Transfer{}, false
	}
	return *item, true
}

func (m *Manager) Cancel(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	item, ok := m.items[id]
	if !ok {
		return fmt.Errorf("transfer not found")
	}
	switch item.State {
	case domain.QueueWaiting:
		item.State = domain.QueueCancelled
		now := time.Now()
		item.FinishedAt = &now
		m.publish("queue.item_cancelled", *item)
	case domain.QueueStarting, domain.QueueTransferring, domain.QueueVerifying:
		if m.activeID == id && m.activeStop != nil {
			m.activeStop()
		}
	default:
		return fmt.Errorf("transfer in %s state cannot be cancelled", item.State)
	}
	return nil
}

func (m *Manager) Retry(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	item, ok := m.items[id]
	if !ok {
		return fmt.Errorf("transfer not found")
	}
	if item.State != domain.QueueFailed && item.State != domain.QueueCancelled {
		return fmt.Errorf("only failed or cancelled transfers can be retried")
	}
	item.State = domain.QueueWaiting
	item.Error = ""
	item.BytesTransferred = 0
	item.Percentage = 0
	item.FinishedAt = nil
	m.pending = append(m.pending, id)
	m.cond.Broadcast()
	m.publish("queue.item_retried", *item)
	return nil
}

func (m *Manager) Pause() {
	m.mu.Lock()
	m.paused = true
	m.mu.Unlock()
	m.publish("queue.paused", nil)
}

func (m *Manager) Resume() {
	m.mu.Lock()
	m.paused = false
	m.cond.Broadcast()
	m.mu.Unlock()
	m.publish("queue.resumed", nil)
}

func (m *Manager) ClearCompleted() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	kept := m.order[:0]
	removed := 0
	for _, id := range m.order {
		item := m.items[id]
		if item != nil && (item.State == domain.QueueCompleted || item.State == domain.QueueCancelled) {
			delete(m.items, id)
			removed++
			continue
		}
		kept = append(kept, id)
	}
	m.order = kept
	return removed
}

func (m *Manager) Close(ctx context.Context) error {
	m.mu.Lock()
	if !m.closed {
		m.closed = true
		m.rootStop()
		if m.activeStop != nil {
			m.activeStop()
		}
		m.cond.Broadcast()
	}
	m.mu.Unlock()
	select {
	case <-m.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *Manager) run() {
	defer close(m.done)
	for {
		m.mu.Lock()
		for !m.closed && (m.paused || len(m.pending) == 0) {
			m.cond.Wait()
		}
		if m.closed {
			m.mu.Unlock()
			return
		}
		id := m.pending[0]
		m.pending = m.pending[1:]
		item := m.items[id]
		if item == nil || item.State != domain.QueueWaiting {
			m.mu.Unlock()
			continue
		}
		ctx, cancel := context.WithCancel(m.rootCtx)
		m.activeID, m.activeStop = id, cancel
		item.State = domain.QueueStarting
		item.Attempts++
		now := time.Now()
		item.StartedAt = &now
		m.mu.Unlock()

		m.publish("queue.started", map[string]any{"queue_id": item.QueueID, "platform": m.platform})
		m.publish("queue.item_started", m.snapshot(id))
		m.process(ctx, item)
		cancel()

		m.mu.Lock()
		m.activeID, m.activeStop = "", nil
		failedQueue := item.State == domain.QueueFailed && m.stopOnError[item.QueueID]
		if failedQueue {
			m.cancelWaitingQueueLocked(item.QueueID)
		}
		queueFinished := m.queueFinishedLocked(item.QueueID)
		m.mu.Unlock()
		if queueFinished {
			m.publish("queue.completed", map[string]any{"queue_id": item.QueueID, "platform": m.platform})
		}
	}
}

func (m *Manager) process(ctx context.Context, item *domain.Transfer) {
	m.update(item.ID, func(value *domain.Transfer) { value.State = domain.QueueTransferring })
	started := time.Now()
	var lastProgressEvent time.Time
	dynamicTotal := item.TotalBytes <= 0
	fileTotals := make(map[string]int64)
	progressFn := func(progress ps3ftp.Progress) {
		m.mu.Lock()
		value := m.items[item.ID]
		if value == nil {
			m.mu.Unlock()
			return
		}
		value.CurrentFile = progress.File
		if dynamicTotal && progress.Total > 0 {
			key := progress.Key
			if key == "" {
				key = progress.File
			}
			previous := fileTotals[key]
			fileTotals[key] = progress.Total
			value.TotalBytes += progress.Total - previous
		}
		value.BytesTransferred += progress.Delta
		if value.TotalBytes > 0 && value.BytesTransferred > value.TotalBytes {
			value.TotalBytes = value.BytesTransferred
		}
		elapsed := time.Since(started)
		value.ElapsedSeconds = int64(elapsed.Seconds())
		if elapsed > 0 {
			value.Speed = int64(float64(value.BytesTransferred) / elapsed.Seconds())
		}
		if value.TotalBytes > 0 {
			value.Percentage = float64(value.BytesTransferred) * 100 / float64(value.TotalBytes)
			if value.Speed > 0 {
				value.ETASeconds = (value.TotalBytes - value.BytesTransferred) / value.Speed
			}
		}
		snapshot := *value
		m.mu.Unlock()
		if time.Since(lastProgressEvent) >= 250*time.Millisecond || snapshot.BytesTransferred == snapshot.TotalBytes {
			lastProgressEvent = time.Now()
			m.publish("queue.progress", snapshot)
		}
	}
	var err error
	if m.downloader != nil {
		err = m.downloader.DownloadGame(ctx, item.ConsoleIP, item.Game, m.localRoot, progressFn)
	} else {
		err = m.uploader.UploadGame(ctx, item.ConsoleIP, item.Game, m.remoteRoot, progressFn)
	}
	if err != nil {
		m.mu.Lock()
		value := m.items[item.ID]
		now := time.Now()
		value.FinishedAt = &now
		if errors.Is(err, context.Canceled) {
			value.State = domain.QueueCancelled
			value.Error = "transfer cancelled"
		} else {
			value.State = domain.QueueFailed
			value.Error = err.Error()
		}
		snapshot := *value
		m.mu.Unlock()
		if snapshot.State == domain.QueueCancelled {
			m.publish("queue.item_cancelled", snapshot)
		} else {
			m.publish("queue.item_failed", snapshot)
		}
		return
	}
	m.update(item.ID, func(value *domain.Transfer) {
		value.State = domain.QueueVerifying
		value.Percentage = 100
		if value.TotalBytes <= 0 {
			value.TotalBytes = value.BytesTransferred
		} else {
			value.BytesTransferred = value.TotalBytes
		}
	})
	// FTP size checks happen per file during resumable transfers; reaching this point verifies every file completed.
	m.update(item.ID, func(value *domain.Transfer) {
		now := time.Now()
		value.State = domain.QueueCompleted
		value.FinishedAt = &now
	})
	m.publish("queue.item_completed", m.snapshot(item.ID))
}

func (m *Manager) update(id string, fn func(*domain.Transfer)) {
	m.mu.Lock()
	if item := m.items[id]; item != nil {
		fn(item)
	}
	m.mu.Unlock()
}

func (m *Manager) snapshot(id string) domain.Transfer {
	m.mu.Lock()
	defer m.mu.Unlock()
	if item := m.items[id]; item != nil {
		return *item
	}
	return domain.Transfer{}
}

func (m *Manager) queueFinishedLocked(queueID string) bool {
	for _, item := range m.items {
		if item.QueueID == queueID && (item.State == domain.QueueWaiting || item.State == domain.QueueStarting || item.State == domain.QueueTransferring || item.State == domain.QueueVerifying) {
			return false
		}
	}
	return true
}

func (m *Manager) cancelWaitingQueueLocked(queueID string) {
	for _, item := range m.items {
		if item.QueueID == queueID && item.State == domain.QueueWaiting {
			item.State = domain.QueueCancelled
			item.Error = "queue stopped after an earlier failure"
			now := time.Now()
			item.FinishedAt = &now
		}
	}
}

func (m *Manager) id(prefix string) string {
	return fmt.Sprintf("%s-%d-%d", prefix, time.Now().UnixNano(), m.sequence.Add(1))
}

func (m *Manager) publish(eventType string, payload any) {
	if m.events == nil {
		return
	}
	if m.eventPrefix != "" {
		eventType = m.eventPrefix + "." + eventType
	}
	m.events.Publish(eventType, payload)
}
