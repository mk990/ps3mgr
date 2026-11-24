package transfers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
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

type ConvertFunc func(ctx context.Context, game domain.Game, format string, phase func(domain.ConversionPhase) error) (filename string, err error)

type Publisher interface {
	Publish(eventType string, payload any)
}

type Options struct {
	StopOnError  bool
	OutputFormat string
}

type Manager struct {
	mu          sync.Mutex
	cond        *sync.Cond
	uploader    Uploader
	convert     ConvertFunc
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
	journal     *conversionJournal
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

func NewConversion(convert ConvertFunc, publisher Publisher, platform domain.Platform) *Manager {
	m := newManager(nil, publisher, "", platform, string(platform)+".conversion")
	m.convert = convert
	m.direction = domain.TransferConversion
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
	if m.convert != nil {
		for _, game := range games {
			if m.conversionPending(game, options.OutputFormat) {
				return nil, fmt.Errorf("%s is already queued for %s conversion", game.Title, options.OutputFormat)
			}
		}
	}
	m.stopOnError[queueID] = options.StopOnError
	oldOrderLen, oldPendingLen := len(m.order), len(m.pending)
	created := make([]domain.Transfer, 0, len(games))
	for _, game := range games {
		item := &domain.Transfer{
			OutputFormat: options.OutputFormat, ID: m.id("transfer"), QueueID: queueID, ConsoleIP: consoleIP,
			Game: game, Platform: m.platform, Direction: m.direction, State: domain.QueueWaiting, TotalBytes: game.Size, CreatedAt: now,
		}
		m.items[item.ID] = item
		m.order = append(m.order, item.ID)
		m.pending = append(m.pending, item.ID)
		created = append(created, *item)
	}
	if err := m.saveConversionQueueLocked(); err != nil {
		for _, item := range created {
			delete(m.items, item.ID)
		}
		delete(m.stopOnError, queueID)
		m.order = m.order[:oldOrderLen]
		m.pending = m.pending[:oldPendingLen]
		return nil, err
	}
	m.cond.Broadcast()
	m.publish("queue.created", map[string]any{"queue_id": queueID, "platform": m.platform, "items": created})
	return created, nil
}

// conversionPending is called with the queue lock held.
func (m *Manager) conversionPending(game domain.Game, format string) bool {
	for _, item := range m.items {
		if item.Game.LibraryID == game.LibraryID && item.OutputFormat == format &&
			(item.State == domain.QueueWaiting || item.State == domain.QueueStarting || item.State == domain.QueueConverting) {
			return true
		}
	}
	return false
}

func (m *Manager) List() []domain.Transfer {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]domain.Transfer, 0, len(m.order))
	for _, id := range m.order {
		if item := m.items[id]; item != nil {
			result = append(result, transferSnapshot(*item))
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
	return transferSnapshot(*item), true
}

func transferSnapshot(item domain.Transfer) domain.Transfer {
	if item.Direction == domain.TransferConversion && item.StartedAt != nil && item.FinishedAt == nil {
		item.ElapsedSeconds = max(0, int64(time.Since(*item.StartedAt).Seconds()))
	}
	return item
}

func (m *Manager) Cancel(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return fmt.Errorf("transfer manager is shutting down")
	}
	item, ok := m.items[id]
	if !ok {
		return fmt.Errorf("transfer not found")
	}
	switch item.State {
	case domain.QueueWaiting:
		previous := *item
		item.State = domain.QueueCancelled
		now := time.Now()
		item.FinishedAt = &now
		if err := m.saveConversionQueueLocked(); err != nil {
			*item = previous
			return err
		}
		m.publish("queue.item_cancelled", *item)
	case domain.QueueConverting, domain.QueueStarting, domain.QueueTransferring, domain.QueueVerifying:
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
	if m.closed {
		return fmt.Errorf("transfer manager is shutting down")
	}
	item, ok := m.items[id]
	if !ok {
		return fmt.Errorf("transfer not found")
	}
	if item.State != domain.QueueFailed && item.State != domain.QueueCancelled {
		return fmt.Errorf("only failed or cancelled transfers can be retried")
	}
	if m.convert != nil && m.conversionPending(item.Game, item.OutputFormat) {
		return fmt.Errorf("%s is already queued for %s conversion", item.Game.Title, item.OutputFormat)
	}
	previous := *item
	item.State = domain.QueueWaiting
	item.Error = ""
	item.BytesTransferred = 0
	item.Percentage = 0
	if m.convert != nil {
		item.StartedAt = nil
		item.ConversionPhase = ""
		item.ElapsedSeconds = 0
	}
	item.FinishedAt = nil
	// Keep the retry at the tail, including when the old slot is still queued.
	oldPending := m.pending
	if m.journal != nil {
		m.pending = slices.Clone(m.pending)
	}
	m.pending = slices.DeleteFunc(m.pending, func(pending string) bool { return pending == id })
	m.pending = append(m.pending, id)
	if err := m.saveConversionQueueLocked(); err != nil {
		*item = previous
		m.pending = oldPending
		return err
	}
	m.cond.Broadcast()
	m.publish("queue.item_retried", *item)
	return nil
}

func (m *Manager) Pause() error {
	return m.setPaused(true, "queue.paused")
}

func (m *Manager) Resume() error {
	return m.setPaused(false, "queue.resumed")
}

func (m *Manager) setPaused(paused bool, event string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return fmt.Errorf("transfer manager is shutting down")
	}
	previous := m.paused
	m.paused = paused
	if m.journal != nil && (len(m.items) != 0 || m.journal.lock != nil) {
		if err := m.saveConversionQueueLocked(); err != nil {
			m.paused = previous
			return err
		}
	}
	m.cond.Broadcast()
	m.publish(event, nil)
	return nil
}

func (m *Manager) ClearCompleted() int {
	removed, _ := m.ClearCompletedChecked()
	return removed
}

func (m *Manager) ClearCompletedChecked() (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return 0, fmt.Errorf("transfer manager is shutting down")
	}
	kept := make([]string, 0, len(m.order))
	var removed []string
	for _, id := range m.order {
		item := m.items[id]
		if item != nil && (item.State == domain.QueueCompleted || item.State == domain.QueueCancelled) {
			removed = append(removed, id)
			continue
		}
		kept = append(kept, id)
	}
	if len(removed) == 0 {
		return 0, nil
	}
	previous := m.order
	m.order = kept
	if err := m.saveConversionQueueLocked(); err != nil {
		m.order = previous
		return 0, err
	}
	for _, id := range removed {
		delete(m.items, id)
	}
	return len(removed), nil
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
	defer func() {
		m.mu.Lock()
		if m.journal != nil && m.journal.lock != nil {
			m.journal.lock.Close()
			m.journal.lock = nil
		}
		m.mu.Unlock()
		close(m.done)
	}()
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
		oldPending := m.pending
		m.pending = m.pending[1:]
		item := m.items[id]
		if item == nil || item.State != domain.QueueWaiting {
			m.mu.Unlock()
			continue
		}
		previous := *item
		item.State = domain.QueueStarting
		if m.convert != nil {
			item.State = domain.QueueConverting
			item.ConversionPhase = domain.ConversionPreparing
		}
		item.Attempts++
		now := time.Now()
		item.StartedAt = &now
		if err := m.saveConversionQueueLocked(); err != nil {
			*item = previous
			m.pending = oldPending
			m.paused = true
			m.mu.Unlock()
			m.reportQueueSaveError(err)
			continue
		}
		ctx, cancel := context.WithCancel(m.rootCtx)
		m.activeID, m.activeStop = id, cancel
		m.mu.Unlock()

		m.publish("queue.started", map[string]any{"queue_id": item.QueueID, "platform": m.platform})
		m.publish("queue.item_started", m.snapshot(id))
		m.process(ctx, item)
		cancel()

		m.mu.Lock()
		m.activeID, m.activeStop = "", nil
		queueFinished := m.queueFinishedLocked(item.QueueID)
		m.mu.Unlock()
		if queueFinished {
			m.publish("queue.completed", map[string]any{"queue_id": item.QueueID, "platform": m.platform})
		}
	}
}

func (m *Manager) process(ctx context.Context, item *domain.Transfer) {
	var filename string
	var err error
	if m.convert != nil {
		filename, err = m.convert(ctx, item.Game, item.OutputFormat, func(phase domain.ConversionPhase) error {
			return m.conversionPhase(item.ID, phase)
		})
	} else {
		err = m.transfer(ctx, item)
	}
	m.mu.Lock()
	now := time.Now()
	if m.convert != nil {
		item.ElapsedSeconds = max(0, int64(now.Sub(*item.StartedAt).Seconds()))
	}
	item.FinishedAt = &now
	event := "queue.item_completed"
	switch {
	case err == nil:
		item.State = domain.QueueCompleted
		item.Filename, item.Percentage = filename, 100
	case errors.Is(err, context.Canceled) && !(m.convert != nil && m.closed):
		item.State = domain.QueueCancelled
		item.Error = "transfer cancelled"
		if m.convert != nil {
			item.Error = "conversion cancelled"
		}
		event = "queue.item_cancelled"
	default:
		item.State = domain.QueueFailed
		item.Error = err.Error()
		if m.convert != nil && m.closed && errors.Is(err, context.Canceled) {
			item.Error = "conversion interrupted by server shutdown; retry to rebuild the image"
		}
		if m.stopOnError[item.QueueID] {
			m.cancelWaitingQueueLocked(item.QueueID)
		}
		event = "queue.item_failed"
	}
	saveErr := m.saveConversionQueueLocked()
	snapshot := transferSnapshot(*item)
	m.mu.Unlock()
	if saveErr != nil {
		m.reportQueueSaveError(saveErr)
	}
	m.publish(event, snapshot)
}

func (m *Manager) conversionPhase(id string, phase domain.ConversionPhase) error {
	m.mu.Lock()
	item := m.items[id]
	previous := item.ConversionPhase
	item.ConversionPhase = phase
	if phase == domain.ConversionFinalizing {
		if err := m.saveConversionQueueLocked(); err != nil {
			item.ConversionPhase = previous
			m.mu.Unlock()
			return err
		}
	}
	snapshot := transferSnapshot(*item)
	m.mu.Unlock()
	m.publish("queue.progress", snapshot)
	return nil
}

func (m *Manager) reportQueueSaveError(err error) {
	slog.Error("PS5 conversion queue could not be saved", "error", err)
	m.publish("queue.store_failed", map[string]string{"error": err.Error()})
}

func (m *Manager) transfer(ctx context.Context, item *domain.Transfer) error {
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
	if err == nil {
		m.update(item.ID, func(value *domain.Transfer) {
			value.State, value.Percentage = domain.QueueVerifying, 100
			if value.TotalBytes <= 0 {
				value.TotalBytes = value.BytesTransferred
			} else {
				value.BytesTransferred = value.TotalBytes
			}
		})
	}
	return err
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
		return transferSnapshot(*item)
	}
	return domain.Transfer{}
}

func (m *Manager) queueFinishedLocked(queueID string) bool {
	for _, item := range m.items {
		if item.QueueID == queueID && (item.State == domain.QueueWaiting || item.State == domain.QueueStarting || item.State == domain.QueueTransferring || item.State == domain.QueueVerifying || item.State == domain.QueueConverting) {
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
