package ps4

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type Installer interface {
	Install(context.Context, string, []string) (int, error)
	Progress(context.Context, string, int) (InstallProgress, error)
	IsInstalled(context.Context, string, string) (bool, error)
	Cancel(context.Context, string, int) error
	Pause(context.Context, string, int) error
	Resume(context.Context, string, int) error
	FindTask(context.Context, string, string, int) (int, bool, error)
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
	tasks       TaskStore
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

func NewQueue(installer Installer, provider PackageProvider, events Publisher, tasks TaskStore) *Queue {
	ctx, cancel := context.WithCancel(context.Background())
	// Remote Package Installer is known to crash under sustained request load
	// during a long or large install — its own troubleshooting guidance is to
	// keep progress polling at 2-3s rather than hammering it every second.
	q := &Queue{installer: installer, provider: provider, events: events, tasks: tasks, items: make(map[string]*Job), stopOnError: make(map[string]bool), rootCtx: ctx, rootStop: cancel, done: make(chan struct{}), pollEvery: 3 * time.Second}
	q.cond = sync.NewCond(&q.mu)
	go q.run()
	return q
}

// ReconcileOrphanedTasks cancels any RPI task left registered by a previous
// ps3mgr process (e.g. one that crashed or was restarted between a
// successful /api/install call and its cleanup). Left alone, an orphaned
// task permanently collides with future installs of the same content
// (Remote Package Installer error 0x80990015) until the console's Remote
// Package Installer app is relaunched by hand. Safe to call even when no
// TaskStore is configured.
func (q *Queue) ReconcileOrphanedTasks(ctx context.Context) {
	if q.tasks == nil {
		return
	}
	pending, err := q.tasks.List()
	if err != nil {
		q.publish("ps4.task.reconcile_failed", map[string]any{"platform": Platform, "error": err.Error()})
		return
	}
	for _, task := range pending {
		cancelCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := q.installer.Cancel(cancelCtx, task.ConsoleIP, task.TaskID)
		cancel()
		if err != nil {
			q.publish("ps4.task.orphan_cleanup_failed", map[string]any{"platform": Platform, "console_ip": task.ConsoleIP, "task_id": task.TaskID, "error": err.Error()})
			continue
		}
		_ = q.tasks.Remove(task)
		q.publish("ps4.task.orphan_cleared", map[string]any{"platform": Platform, "console_ip": task.ConsoleIP, "task_id": task.TaskID})
	}
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

// PauseJob suspends the install running on the console. The task keeps its
// registration and its transferred bytes, so ResumeJob continues the download
// instead of restarting it. Only the active job holds a console task; Pause
// covers the queue as a whole by holding back jobs that have not started.
func (q *Queue) PauseJob(id string) error { return q.setJobPaused(id, true) }

// ResumeJob continues an install previously suspended by PauseJob.
func (q *Queue) ResumeJob(id string) error { return q.setJobPaused(id, false) }

func (q *Queue) setJobPaused(id string, pause bool) error {
	from, to, verb := StateDownloading, StatePaused, "paused"
	if !pause {
		from, to, verb = StatePaused, StateDownloading, "resumed"
	}
	q.mu.Lock()
	item, ok := q.items[id]
	if !ok {
		q.mu.Unlock()
		return fmt.Errorf("PS4 job not found")
	}
	if item.State != from || q.activeID != id || item.TaskID <= 0 {
		state := item.State
		q.mu.Unlock()
		return fmt.Errorf("PS4 job in %s state cannot be %s", state, verb)
	}
	consoleIP, taskID := item.ConsoleIP, item.TaskID
	q.mu.Unlock()

	ctx, cancel := context.WithTimeout(q.rootCtx, 15*time.Second)
	defer cancel()
	var err error
	if pause {
		err = q.installer.Pause(ctx, consoleIP, taskID)
	} else {
		err = q.installer.Resume(ctx, consoleIP, taskID)
	}
	if err != nil {
		return fmt.Errorf("%s Remote Package Installer task: %w", strings.TrimSuffix(verb, "d"), err)
	}

	q.mu.Lock()
	item, ok = q.items[id]
	if !ok || item.State != from {
		// The job finished, failed, or was cancelled while the console request
		// was in flight, so its task state no longer decides anything.
		q.mu.Unlock()
		return nil
	}
	item.State = to
	snapshot := *item
	q.mu.Unlock()
	q.publish("ps4.job."+verb, snapshot)
	return nil
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
		switch {
		case err == nil || errors.Is(err, context.Canceled):
			// Success or a deliberate cancellation cancels nothing else.
		case q.stopOnError[queueID]:
			cancelled = q.cancelPendingLocked(queueID, "cancelled after an earlier PS4 job failed")
		case item.Package.Format == "pkg-game" && item.Package.TitleID != "":
			// A base game failed: its patch/DLC/license would be rejected by
			// Remote Package Installer because the base title is absent, so skip
			// only those. Other titles in the queue keep running.
			cancelled = q.cancelDependentsLocked(queueID, item.Package.TitleID, "cancelled because the base game install failed")
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
	taskID, reattached, err := q.registerTask(ctx, job, urls)
	if err != nil {
		return err
	}
	q.update(id, func(item *Job) { item.TaskID = taskID })
	if reattached {
		q.publish("ps4.install.reattached", q.snapshot(id))
	} else {
		q.publish("ps4.install.requested", q.snapshot(id))
	}
	q.setState(id, StateDownloading)

	// The RPI task stays registered on the console until explicitly
	// unregistered; any return path other than a verified success must clean
	// it up, or the next attempt for the same content ID fails immediately
	// with a "task already exists" error from the console's BGFT service
	// (Remote Package Installer error 0x80990015). The pending task is
	// persisted to tasks until cleanup is confirmed so an orphan left by a
	// crash or restart can still be cancelled on the next startup, see
	// ReconcileOrphanedTasks.
	pending := PendingTask{ConsoleIP: job.ConsoleIP, TaskID: taskID}
	if q.tasks != nil {
		if err := q.tasks.Add(pending); err != nil {
			q.publish("ps4.task.track_failed", map[string]any{"platform": Platform, "console_ip": job.ConsoleIP, "task_id": taskID, "error": err.Error()})
		}
	}
	succeeded := false
	defer func() {
		if succeeded {
			if q.tasks != nil {
				_ = q.tasks.Remove(pending)
			}
			return
		}
		cancelCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		cancelErr := q.installer.Cancel(cancelCtx, job.ConsoleIP, taskID)
		cancel()
		if cancelErr != nil {
			// The pending task entry is deliberately left in place so
			// ReconcileOrphanedTasks retries the cleanup on next startup.
			q.publish("ps4.task.cleanup_failed", map[string]any{"platform": Platform, "console_ip": job.ConsoleIP, "task_id": taskID, "error": cancelErr.Error()})
			return
		}
		if q.tasks != nil {
			_ = q.tasks.Remove(pending)
		}
	}()

	lastBytes, lastTime := int64(0), time.Now()
	ticker := time.NewTicker(q.pollEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			progress, err := q.pollProgress(ctx, job.ConsoleIP, taskID)
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
				succeeded = true
				return nil
			}
		}
	}
}

// registerTask attaches to the task the console already holds for this
// content instead of registering a second one. A task outlives the ps3mgr
// process that created it and its download keeps running on the console, so
// re-attaching after a restart preserves the bytes already transferred;
// registering again would only be rejected with a "task already exists" error
// (Remote Package Installer error 0x80990015).
func (q *Queue) registerTask(ctx context.Context, job Job, urls []string) (int, bool, error) {
	if contentID := job.Package.ContentID; contentID != "" {
		taskID, found, err := q.installer.FindTask(ctx, job.ConsoleIP, contentID, TaskSubTypeDefault)
		switch {
		case err != nil:
			// A failed lookup is not fatal. Registering a fresh task is the
			// normal path anyway, and it reports any real problem with the
			// console far more precisely than this probe can.
			q.publish("ps4.task.lookup_failed", map[string]any{"platform": Platform, "console_ip": job.ConsoleIP, "content_id": contentID, "error": err.Error()})
		case found:
			// Whatever registered the task may have left it paused. A resume
			// is rejected for a task that is already running, so the error is
			// advisory and progress polling reports the truth either way.
			resumeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			_ = q.installer.Resume(resumeCtx, job.ConsoleIP, taskID)
			cancel()
			return taskID, true, nil
		}
	}
	taskID, err := q.installer.Install(ctx, job.ConsoleIP, urls)
	if err != nil {
		return 0, false, fmt.Errorf("start Remote Package Installer task: %w", err)
	}
	return taskID, false, nil
}

// pollProgress reads install progress from the console's Remote Package
// Installer, retrying transient transport errors (the RPI HTTP service is
// known to drop connections under I/O load mid-install) before giving up.
func (q *Queue) pollProgress(ctx context.Context, ip string, taskID int) (InstallProgress, error) {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return InstallProgress{}, ctx.Err()
			case <-time.After(time.Duration(attempt) * time.Second):
			}
		}
		progress, err := q.installer.Progress(ctx, ip, taskID)
		if err == nil {
			return progress, nil
		}
		lastErr = err
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return InstallProgress{}, err
		}
	}
	return InstallProgress{}, lastErr
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
// cancelDependentsLocked cancels the still-waiting patch, DLC, and license jobs
// for titleID in the same queue after that title's base game failed. Jobs for
// other titles, and any job already running or finished, are left untouched.
func (q *Queue) cancelDependentsLocked(queueID, titleID, reason string) []Job {
	var cancelled []Job
	for _, item := range q.items {
		if item.QueueID != queueID || item.State != StateWaiting {
			continue
		}
		if !isDependentFormat(item.Package.Format) || !strings.EqualFold(item.Package.TitleID, titleID) {
			continue
		}
		item.State, item.Error = StateCancelled, reason
		now := time.Now()
		item.FinishedAt = &now
		cancelled = append(cancelled, *item)
	}
	return cancelled
}

// isDependentFormat reports whether a package requires its title's base game to
// be installed first, so it can be skipped when that base game fails.
func isDependentFormat(format string) bool {
	switch format {
	case "pkg-patch", "pkg-dlc", "pkg-license":
		return true
	default:
		return false
	}
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
	case StateValidating, StateServing, StateRequestingInstall, StateDownloading, StatePaused, StateVerifying:
		return true
	default:
		return false
	}
}
