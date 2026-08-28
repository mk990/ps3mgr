package ps2

import (
	"context"
	"sync"
	"testing"
	"time"
)

type recordingProcessor struct {
	mu      sync.Mutex
	order   []string
	started chan string
	release chan struct{}
}

func (p *recordingProcessor) Process(ctx context.Context, game Game, _ string, stage func(State), progress ProgressFunc) error {
	stage(StateConverting)
	p.mu.Lock()
	p.order = append(p.order, game.Title)
	p.mu.Unlock()
	p.started <- game.Title
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-p.release:
		return nil
	}
}

func TestQueueProcessesOnePS2GameAtATime(t *testing.T) {
	processor := &recordingProcessor{started: make(chan string, 2), release: make(chan struct{}, 2)}
	queue := NewQueue(processor, nil)
	defer queue.Close(context.Background())
	_, err := queue.Enqueue([]Game{{Title: "A", Size: 1}, {Title: "B", Size: 1}}, "usb0")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-processor.started:
		if got != "A" {
			t.Fatalf("first = %s", got)
		}
	case <-time.After(time.Second):
		t.Fatal("first did not start")
	}
	select {
	case got := <-processor.started:
		t.Fatalf("second started concurrently: %s", got)
	case <-time.After(50 * time.Millisecond):
	}
	processor.release <- struct{}{}
	select {
	case got := <-processor.started:
		if got != "B" {
			t.Fatalf("second = %s", got)
		}
	case <-time.After(time.Second):
		t.Fatal("second did not start")
	}
	processor.release <- struct{}{}
}

func TestQueueCancellationAndRetry(t *testing.T) {
	processor := &recordingProcessor{started: make(chan string, 2), release: make(chan struct{}, 2)}
	queue := NewQueue(processor, nil)
	defer queue.Close(context.Background())
	jobs, _ := queue.Enqueue([]Game{{Title: "A", Size: 1}}, "usb0")
	<-processor.started
	if err := queue.Cancel(jobs[0].ID); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		job, _ := queue.Get(jobs[0].ID)
		if job.State == StateCancelled {
			if err := queue.Retry(job.ID); err != nil {
				t.Fatal(err)
			}
			select {
			case <-processor.started:
				processor.release <- struct{}{}
				return
			case <-time.After(time.Second):
				t.Fatal("retry did not start")
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("cancel did not finish")
}
