package ps4

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

type Installer interface {
	Install(context.Context, string, []string) (int, error)
	Progress(context.Context, string, int) (InstallProgress, error)
	IsInstalled(context.Context, string, string) (bool, error)
	Cancel(context.Context, string, int) error
}

type PackageProvider interface {
	Register(Package) ([]string, func(), error)
}

type Queue struct {
	mu          sync.Mutex
	cond        *sync.Cond
	installer   Installer
	provider    PackageProvider
	events      Publisher
	items       map[string]*Job
	order       []string
	pending     []string
	stopOnError map[string]bool
	activeID    string
	activeStop  context.CancelFunc
	paused      bool
	closed      bool
	rootCtx     context.Context
	rootStop    context.CancelFunc
	done        chan struct{}
	sequence    atomic.Uint64
	pollEvery   time.Duration
}

func NewQueue(installer Installer, provider PackageProvider, events Publisher) *Queue {
	ctx, cancel := context.WithCancel(context.Background())
	q := &Queue{installer: installer, provider: provider, events: events, items: make(map[string]*Job), stopOnError: make(map[string]bool), rootCtx: ctx, rootStop: cancel, done: make(chan struct{}), pollEvery: time.Second}
	q.cond = sync.NewCond(&q.mu)
	go q.run()
	return q
}

func (q *Queue) Enqueue(packages []Package, consoleIP string, stopOnError bool) ([]Job, error) {
	if len(packages) == 0 {
		return nil, fmt.Errorf("at least one PS4 package is required")
	}
	if consoleIP == "" {
		return nil, fmt.Errorf("console_id is required")
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return nil, fmt.Errorf("PS4 queue is shutting down")
	}
	queueID := q.id("ps4-queue")
	q.stopOnError[queueID] = stopOnError
	created := make([]Job, 0, len(packages))
	for _, pkg := range packages {
		job := &Job{ID: q.id("ps4-job"), QueueID: queueID, Platform: Platform, ConsoleIP: consoleIP, Package: pkg, State: StateWaiting, TotalBytes: pkg.Size, CreatedAt: time.Now()}
		q.items[job.ID] = job
		q.order = append(q.order, job.ID)
		q.pending = append(q.pending, job.ID)
		created = append(created, *job)
	}
	q.cond.Broadcast()
	q.publish("ps4.queue.created", map[string]any{"platform": Platform, "queue_id": queueID, "jobs": created})
	return created, nil
}

func (q *Queue) List() []Job {
	q.mu.Lock()
	defer q.mu.Unlock()
	result := make([]Job, 0, len(q.order))
	for _, id := range q.order {
		if item := q.items[id]; item != nil {
			result = append(result, *item)
		}
	}
	return result
}

func (q *Queue) Get(id string) (Job, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	item, ok := q.items[id]
	if !ok {
		return Job{}, false
	}
	return *item, true
}

func (q *Queue) Cancel(id string) error {
	q.mu.Lock()
	item, ok := q.items[id]
	if !ok {
		q.mu.Unlock()
		return fmt.Errorf("PS4 job not found")
	}
	if item.State == StateWaiting {
		item.State = StateCancelled
		now := time.Now()
		item.FinishedAt = &now
		snapshot, queueID := *item, item.QueueID
		done := q.queueDoneLocked(queueID)
		q.mu.Unlock()
		q.publish("ps4.job.cancelled", snapshot)
		if done {
			q.publishQueueCompleted(queueID)
		}
		return nil
	}
	if isActive(item.State) && q.activeID == id && q.activeStop != nil {
		q.activeStop()
		q.mu.Unlock()
		return nil
	}
	state := item.State
	q.mu.Unlock()
	return fmt.Errorf("PS4 job in %s state cannot be cancelled", state)
}

func (q *Queue) Retry(id string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	item, ok := q.items[id]
	if !ok {
		return fmt.Errorf("PS4 job not found")
	}
	if item.State != StateFailed && item.State != StateCancelled {
		return fmt.Errorf("only failed or cancelled PS4 jobs can be retried")
	}
	item.State, item.Error, item.TaskID = StateWaiting, "", 0
	item.BytesTransferred, item.Percentage, item.Speed, item.ETASeconds = 0, 0, 0, 0
	item.StartedAt, item.FinishedAt = nil, nil
	q.pending = append(q.pending, id)
	q.cond.Broadcast()
	q.publish("ps4.job.retried", *item)
	return nil
}

func (q *Queue) Pause() { q.mu.Lock(); q.paused = true; q.mu.Unlock() }
func (q *Queue) Resume() {
	q.mu.Lock()
	q.paused = false
	q.cond.Broadcast()
	q.mu.Unlock()
}

func (q *Queue) ClearCompleted() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	removed := 0
	kept := q.order[:0]
	for _, id := range q.order {
		item := q.items[id]
		if item != nil && (item.State == StateCompleted || item.State == StateCancelled) {
			delete(q.items, id)
			removed++
			continue
		}
		kept = append(kept, id)
	}
	q.order = kept
	return removed
}

func (q *Queue) Close(ctx context.Context) error {
	q.mu.Lock()
	if !q.closed {
		q.closed = true
		q.rootStop()
		if q.activeStop != nil {
			q.activeStop()
		}
		q.cond.Broadcast()
	}
	q.mu.Unlock()
	select {
	case <-q.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (q *Queue) run() {
	defer close(q.done)
	for {
		q.mu.Lock()
		for !q.closed && (q.paused || len(q.pending) == 0) {
			q.cond.Wait()
		}
		if q.closed {
			q.mu.Unlock()
			return
		}
		id := q.pending[0]
		q.pending = q.pending[1:]
		item := q.items[id]
		if item == nil || item.State != StateWaiting {
			q.mu.Unlock()
			continue
		}
		ctx, cancel := context.WithCancel(q.rootCtx)
		q.activeID, q.activeStop = id, cancel
		now := time.Now()
		item.StartedAt = &now
		item.Attempts++
		q.mu.Unlock()

		q.publish("ps4.queue.started", map[string]any{"platform": Platform, "queue_id": item.QueueID})
		q.publish("ps4.job.started", q.snapshot(id))
		err := q.process(ctx, id)
		cancel()
		q.finish(id, err)

		q.mu.Lock()
		q.activeID, q.activeStop = "", nil
		queueID := item.QueueID
		var cancelled []Job
		if err != nil && q.stopOnError[queueID] && !errors.Is(err, context.Canceled) {
			cancelled = q.cancelPendingLocked(queueID, "cancelled after an earlier PS4 job failed")
		}
		done := q.queueDoneLocked(queueID)
		q.mu.Unlock()
		for _, job := range cancelled {
			q.publish("ps4.job.cancelled", job)
		}
		if done {
			q.publishQueueCompleted(queueID)
		}
	}
}

func (q *Queue) process(ctx context.Context, id string) error {
	job := q.snapshot(id)
	q.setState(id, StateValidating)
	urls, cleanup, err := q.provider.Register(job.Package)
	if err != nil {
		return err
	}
	defer cleanup()
	q.setState(id, StateServing)
	q.publish("ps4.pkg.serving", q.snapshot(id))
	q.setState(id, StateRequestingInstall)
	taskID, err := q.installer.Install(ctx, job.ConsoleIP, urls)
	if err != nil {
		return fmt.Errorf("start Remote Package Installer task: %w", err)
	}
	q.update(id, func(item *Job) { item.TaskID = taskID })
	q.publish("ps4.install.requested", q.snapshot(id))
	q.setState(id, StateDownloading)

	lastBytes, lastTime := int64(0), time.Now()
	ticker := time.NewTicker(q.pollEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			cancelCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = q.installer.Cancel(cancelCtx, job.ConsoleIP, taskID)
			cancel()
			return ctx.Err()
		case <-ticker.C:
			progress, err := q.installer.Progress(ctx, job.ConsoleIP, taskID)
			if err != nil {
				return fmt.Errorf("read PS4 install progress: %w", err)
			}
			now := time.Now()
			total := progress.Total
			if total <= 0 {
				total = job.Package.Size
			}
			speed := int64(0)
			if elapsed := now.Sub(lastTime).Seconds(); elapsed > 0 && progress.Transferred >= lastBytes {
				speed = int64(float64(progress.Transferred-lastBytes) / elapsed)
			}
			percentage, eta := float64(0), int64(0)
			if total > 0 {
				percentage = float64(progress.Transferred) * 100 / float64(total)
				if percentage > 100 {
					percentage = 100
				}
				if speed > 0 && progress.Transferred < total {
					eta = (total - progress.Transferred) / speed
				}
			}
			q.update(id, func(item *Job) {
				item.BytesTransferred, item.TotalBytes, item.Percentage = progress.Transferred, total, percentage
				item.Speed, item.ETASeconds, item.CurrentFile = speed, eta, progress.CurrentFile
			})
			q.publish("ps4.download.progress", q.snapshot(id))
			lastBytes, lastTime = progress.Transferred, now
			if progress.Complete {
				q.setState(id, StateVerifying)
				if job.Package.Format == "pkg-game" && job.Package.TitleID != "" {
					installed, err := q.installer.IsInstalled(ctx, job.ConsoleIP, job.Package.TitleID)
					if err != nil {
						return fmt.Errorf("verify installed PS4 package: %w", err)
					}
					if !installed {
						return fmt.Errorf("Remote Package Installer finished but %s is not installed", job.Package.TitleID)
					}
				}
				return nil
			}
		}
	}
}

func (q *Queue) finish(id string, err error) {
	q.mu.Lock()
	item := q.items[id]
	if item == nil {
		q.mu.Unlock()
		return
	}
	now := time.Now()
	item.FinishedAt = &now
	if err != nil {
		if errors.Is(err, context.Canceled) {
			item.State, item.Error = StateCancelled, "PS4 job cancelled"
		} else {
			item.State, item.Error = StateFailed, err.Error()
		}
	} else {
		item.State, item.Percentage = StateCompleted, 100
		item.BytesTransferred = item.TotalBytes
	}
	snapshot := *item
	q.mu.Unlock()
	if snapshot.State == StateCompleted {
		q.publish("ps4.job.completed", snapshot)
	} else if snapshot.State == StateCancelled {
		q.publish("ps4.job.cancelled", snapshot)
	} else {
		q.publish("ps4.job.failed", snapshot)
	}
}

func (q *Queue) setState(id string, state JobState) {
	q.update(id, func(item *Job) { item.State = state })
}
func (q *Queue) update(id string, fn func(*Job)) {
	q.mu.Lock()
	if item := q.items[id]; item != nil {
		fn(item)
	}
	q.mu.Unlock()
}
func (q *Queue) snapshot(id string) Job {
	q.mu.Lock()
	defer q.mu.Unlock()
	if item := q.items[id]; item != nil {
		return *item
	}
	return Job{}
}
func (q *Queue) queueDoneLocked(queueID string) bool {
	for _, item := range q.items {
		if item.QueueID == queueID && (item.State == StateWaiting || isActive(item.State)) {
			return false
		}
	}
	return true
}
func (q *Queue) cancelPendingLocked(queueID, reason string) []Job {
	var cancelled []Job
	for _, item := range q.items {
		if item.QueueID == queueID && item.State == StateWaiting {
			item.State, item.Error = StateCancelled, reason
			now := time.Now()
			item.FinishedAt = &now
			cancelled = append(cancelled, *item)
		}
	}
	return cancelled
}
func (q *Queue) publishQueueCompleted(queueID string) {
	completed, failed, cancelled := 0, 0, 0
	for _, item := range q.List() {
		if item.QueueID != queueID {
			continue
		}
		switch item.State {
		case StateCompleted:
			completed++
		case StateFailed:
			failed++
		case StateCancelled:
			cancelled++
		}
	}
	q.publish("ps4.queue.completed", map[string]any{"platform": Platform, "queue_id": queueID, "completed": completed, "failed": failed, "cancelled": cancelled})
}
func (q *Queue) publish(event string, payload any) {
	if q.events != nil {
		q.events.Publish(event, payload)
	}
}
func (q *Queue) id(prefix string) string {
	return fmt.Sprintf("%s-%d-%d", prefix, time.Now().UnixNano(), q.sequence.Add(1))
}
func isActive(state JobState) bool {
	switch state {
	case StateValidating, StateServing, StateRequestingInstall, StateDownloading, StateVerifying:
		return true
	default:
		return false
	}
}
