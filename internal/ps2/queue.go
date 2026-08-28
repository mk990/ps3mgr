package ps2

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

type JobProcessor interface {
	Process(context.Context, Game, string, func(State), ProgressFunc) error
}

type Queue struct {
	mu         sync.Mutex
	cond       *sync.Cond
	processor  JobProcessor
	events     Publisher
	items      map[string]*Job
	order      []string
	pending    []string
	activeID   string
	activeStop context.CancelFunc
	closed     bool
	paused     bool
	rootCtx    context.Context
	rootStop   context.CancelFunc
	done       chan struct{}
	sequence   atomic.Uint64
}

func NewQueue(processor JobProcessor, events Publisher) *Queue {
	ctx, cancel := context.WithCancel(context.Background())
	q := &Queue{processor: processor, events: events, items: make(map[string]*Job), rootCtx: ctx, rootStop: cancel, done: make(chan struct{})}
	q.cond = sync.NewCond(&q.mu)
	go q.run()
	return q
}

func (q *Queue) Enqueue(games []Game, usbID string) ([]Job, error) {
	if len(games) == 0 {
		return nil, fmt.Errorf("at least one PS2 game is required")
	}
	if usbID == "" {
		return nil, fmt.Errorf("usb_id is required")
	}
	now := time.Now()
	queueID := q.id("ps2-queue")
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return nil, fmt.Errorf("PS2 queue is shutting down")
	}
	created := make([]Job, 0, len(games))
	for _, game := range games {
		job := &Job{ID: q.id("ps2-job"), QueueID: queueID, Platform: Platform, Game: game, USBID: usbID, State: StateWaiting, Progress: Progress{Stage: StateWaiting, Total: game.Size}, CreatedAt: now}
		q.items[job.ID] = job
		q.order = append(q.order, job.ID)
		q.pending = append(q.pending, job.ID)
		created = append(created, *job)
	}
	q.cond.Broadcast()
	q.publish("ps2.queue.created", map[string]any{"platform": Platform, "queue_id": queueID, "jobs": created})
	return created, nil
}

func (q *Queue) List() []Job {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]Job, 0, len(q.order))
	for _, id := range q.order {
		if item := q.items[id]; item != nil {
			out = append(out, *item)
		}
	}
	return out
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
	defer q.mu.Unlock()
	item, ok := q.items[id]
	if !ok {
		return fmt.Errorf("PS2 job not found")
	}
	switch item.State {
	case StateWaiting, StatePaused:
		wasPaused := item.State == StatePaused
		item.State = StateCancelled
		item.Progress.Stage = StateCancelled
		now := time.Now()
		item.FinishedAt = &now
		if wasPaused {
			q.paused = false
			q.cond.Broadcast()
		}
		q.publish("ps2.job.cancelled", *item)
	case StatePreparing, StateConverting, StateWriting, StateVerifying:
		if q.activeID == id && q.activeStop != nil {
			q.activeStop()
		}
	default:
		return fmt.Errorf("PS2 job in %s state cannot be cancelled", item.State)
	}
	return nil
}

func (q *Queue) Retry(id string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	item, ok := q.items[id]
	if !ok {
		return fmt.Errorf("PS2 job not found")
	}
	if item.State != StateFailed && item.State != StateCancelled && item.State != StatePaused {
		return fmt.Errorf("only failed, paused, or cancelled PS2 jobs can be retried")
	}
	wasPaused := item.State == StatePaused
	item.State = StateWaiting
	item.Progress = Progress{Stage: StateWaiting, Total: item.Game.Size}
	item.Error = ""
	item.Recoverable = false
	item.FinishedAt = nil
	q.paused = false
	if wasPaused {
		q.pending = append([]string{id}, q.pending...)
	} else {
		q.pending = append(q.pending, id)
	}
	q.cond.Broadcast()
	q.publish("ps2.job.retried", *item)
	return nil
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
		q.activeID = id
		q.activeStop = cancel
		now := time.Now()
		item.StartedAt = &now
		item.Attempts++
		q.mu.Unlock()
		q.publish("ps2.queue.started", map[string]any{"platform": Platform, "queue_id": item.QueueID})
		q.publish("ps2.job.started", q.snapshot(id))
		lastProgress := make(map[State]time.Time)
		err := q.processor.Process(ctx, item.Game, item.USBID, func(stage State) { q.update(id, func(job *Job) { job.State = stage; job.Progress.Stage = stage }) }, func(progress Progress) {
			q.update(id, func(job *Job) { job.State = progress.Stage; job.Progress = progress })
			if !lastProgress[progress.Stage].IsZero() && time.Since(lastProgress[progress.Stage]) < 250*time.Millisecond && progress.Bytes != progress.Total {
				return
			}
			lastProgress[progress.Stage] = time.Now()
			event := "ps2.write.progress"
			if progress.Stage == StateConverting {
				event = "ps2.conversion.progress"
			}
			q.publish(event, q.snapshot(id))
		})
		cancel()
		q.finish(id, err)
		q.mu.Lock()
		q.activeID = ""
		q.activeStop = nil
		queueDone := q.queueDoneLocked(item.QueueID)
		q.mu.Unlock()
		if queueDone {
			q.publishQueueCompleted(item.QueueID)
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
			item.State = StateCancelled
			item.Error = "PS2 job cancelled"
		} else if errors.Is(err, ErrUSBUnavailable) {
			item.State = StatePaused
			item.Error = err.Error()
			item.Recoverable = true
			item.FinishedAt = nil
			q.paused = true
		} else {
			item.State = StateFailed
			item.Error = err.Error()
			item.Recoverable = true
		}
		item.Progress.Stage = item.State
		snapshot := *item
		q.mu.Unlock()
		if snapshot.State == StateCancelled {
			q.publish("ps2.job.cancelled", snapshot)
		} else if snapshot.State == StatePaused {
			q.publish("ps2.job.paused", snapshot)
		} else {
			q.publish("ps2.job.failed", snapshot)
		}
		return
	}
	item.State = StateCompleted
	item.Progress.Stage = StateCompleted
	item.Progress.Percentage = 100
	item.Progress.Bytes = item.Game.Size
	item.Progress.Total = item.Game.Size
	snapshot := *item
	q.mu.Unlock()
	q.publish("ps2.job.completed", snapshot)
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
		if item.QueueID == queueID && (item.State == StateWaiting || item.State == StatePaused || item.State == StatePreparing || item.State == StateConverting || item.State == StateWriting || item.State == StateVerifying) {
			return false
		}
	}
	return true
}
func (q *Queue) publishQueueCompleted(queueID string) {
	completed, failed := 0, 0
	for _, item := range q.List() {
		if item.QueueID != queueID {
			continue
		}
		if item.State == StateCompleted {
			completed++
		}
		if item.State == StateFailed {
			failed++
		}
	}
	q.publish("ps2.queue.completed", map[string]any{"platform": Platform, "queue_id": queueID, "completed": completed, "failed": failed})
}
func (q *Queue) publish(event string, payload any) {
	if q.events != nil {
		q.events.Publish(event, payload)
	}
}
func (q *Queue) id(prefix string) string {
	return fmt.Sprintf("%s-%d-%d", prefix, time.Now().UnixNano(), q.sequence.Add(1))
}
