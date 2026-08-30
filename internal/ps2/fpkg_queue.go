package ps2

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

type FPKGQueue struct {
	mu         sync.Mutex
	cond       *sync.Cond
	builder    FPKGBuilder
	events     Publisher
	items      map[string]*FPKGJob
	order      []string
	pending    []string
	activeID   string
	activeStop context.CancelFunc
	closed     bool
	rootCtx    context.Context
	rootStop   context.CancelFunc
	done       chan struct{}
	sequence   atomic.Uint64
}

func NewFPKGQueue(builder FPKGBuilder, events Publisher) *FPKGQueue {
	ctx, cancel := context.WithCancel(context.Background())
	queue := &FPKGQueue{builder: builder, events: events, items: make(map[string]*FPKGJob), rootCtx: ctx, rootStop: cancel, done: make(chan struct{})}
	queue.cond = sync.NewCond(&queue.mu)
	go queue.run()
	return queue
}

func (q *FPKGQueue) Status() FPKGStatus { return q.builder.Status() }

func (q *FPKGQueue) Enqueue(games []Game) ([]FPKGJob, error) {
	if len(games) == 0 {
		return nil, fmt.Errorf("at least one PS2 game is required")
	}
	if status := q.Status(); !status.Ready {
		return nil, fmt.Errorf("PS2 FPKG converter is not ready: %s", status.Message)
	}
	for _, game := range games {
		if game.ID == "" || game.ID == "unknown" {
			return nil, fmt.Errorf("PS2 game %q has an unknown serial and cannot be converted", game.Title)
		}
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return nil, fmt.Errorf("PS2 FPKG queue is shutting down")
	}
	created := make([]FPKGJob, 0, len(games))
	for _, game := range games {
		job := &FPKGJob{ID: q.id(), Game: game, State: FPKGWaiting, Progress: FPKGProgress{Stage: FPKGWaiting}, CreatedAt: time.Now()}
		q.items[job.ID] = job
		q.order = append(q.order, job.ID)
		q.pending = append(q.pending, job.ID)
		created = append(created, *job)
	}
	q.cond.Broadcast()
	q.publish("ps2.fpkg.queue.created", map[string]any{"platform": Platform, "jobs": created})
	return created, nil
}

func (q *FPKGQueue) List() []FPKGJob {
	q.mu.Lock()
	defer q.mu.Unlock()
	items := make([]FPKGJob, 0, len(q.order))
	for _, id := range q.order {
		if item := q.items[id]; item != nil {
			items = append(items, *item)
		}
	}
	return items
}

func (q *FPKGQueue) Get(id string) (FPKGJob, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	item, ok := q.items[id]
	if !ok {
		return FPKGJob{}, false
	}
	return *item, true
}

func (q *FPKGQueue) Cancel(id string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	item, ok := q.items[id]
	if !ok {
		return fmt.Errorf("PS2 FPKG job not found")
	}
	switch item.State {
	case FPKGWaiting:
		now := time.Now()
		item.State, item.Progress.Stage, item.FinishedAt = FPKGCancelled, FPKGCancelled, &now
		q.publish("ps2.fpkg.job.cancelled", *item)
	case FPKGExtracting, FPKGImporting, FPKGPatching, FPKGBuilding, FPKGVerifying:
		if q.activeID == id && q.activeStop != nil {
			q.activeStop()
		}
	default:
		return fmt.Errorf("PS2 FPKG job in %s state cannot be cancelled", item.State)
	}
	return nil
}

func (q *FPKGQueue) Retry(id string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	item, ok := q.items[id]
	if !ok {
		return fmt.Errorf("PS2 FPKG job not found")
	}
	if item.State != FPKGFailed && item.State != FPKGCancelled {
		return fmt.Errorf("only failed or cancelled PS2 FPKG jobs can be retried")
	}
	item.State = FPKGWaiting
	item.Progress = FPKGProgress{Stage: FPKGWaiting}
	item.Error, item.OutputPath, item.FinishedAt = "", "", nil
	q.pending = append(q.pending, id)
	q.cond.Broadcast()
	q.publish("ps2.fpkg.job.retried", *item)
	return nil
}

func (q *FPKGQueue) Close(ctx context.Context) error {
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

func (q *FPKGQueue) run() {
	defer close(q.done)
	for {
		q.mu.Lock()
		for !q.closed && len(q.pending) == 0 {
			q.cond.Wait()
		}
		if q.closed {
			q.mu.Unlock()
			return
		}
		id := q.pending[0]
		q.pending = q.pending[1:]
		item := q.items[id]
		if item == nil || item.State != FPKGWaiting {
			q.mu.Unlock()
			continue
		}
		ctx, cancel := context.WithCancel(q.rootCtx)
		now := time.Now()
		item.StartedAt, item.Attempts = &now, item.Attempts+1
		q.activeID, q.activeStop = id, cancel
		game := item.Game
		q.mu.Unlock()
		q.publish("ps2.fpkg.job.started", q.snapshot(id))
		output, err := q.builder.Build(ctx, game, func(progress FPKGProgress) {
			q.mu.Lock()
			if current := q.items[id]; current != nil {
				current.State, current.Progress = progress.Stage, progress
			}
			q.mu.Unlock()
			q.publish("ps2.fpkg.job.progress", q.snapshot(id))
		})
		cancel()
		q.finish(id, output, err)
		q.mu.Lock()
		q.activeID, q.activeStop = "", nil
		q.mu.Unlock()
	}
}

func (q *FPKGQueue) finish(id, output string, err error) {
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
			item.State, item.Error = FPKGCancelled, "PS2 FPKG conversion cancelled"
		} else {
			item.State, item.Error = FPKGFailed, err.Error()
		}
		item.Progress.Stage = item.State
		snapshot := *item
		q.mu.Unlock()
		if snapshot.State == FPKGCancelled {
			q.publish("ps2.fpkg.job.cancelled", snapshot)
		} else {
			q.publish("ps2.fpkg.job.failed", snapshot)
		}
		return
	}
	item.State, item.Progress.Stage, item.Progress.Percentage = FPKGCompleted, FPKGCompleted, 100
	item.OutputPath = output
	snapshot := *item
	q.mu.Unlock()
	q.publish("ps2.fpkg.job.completed", snapshot)
}

func (q *FPKGQueue) snapshot(id string) FPKGJob {
	q.mu.Lock()
	defer q.mu.Unlock()
	if item := q.items[id]; item != nil {
		return *item
	}
	return FPKGJob{}
}

func (q *FPKGQueue) publish(event string, payload any) {
	if q.events != nil {
		q.events.Publish(event, payload)
	}
}

func (q *FPKGQueue) id() string {
	return fmt.Sprintf("ps2-fpkg-%d-%d", time.Now().UnixNano(), q.sequence.Add(1))
}
