package ps2_test

import (
	"context"
	"testing"
	"time"

	"ps3mgr/internal/domain"
	ps3ftp "ps3mgr/internal/ftp"
	"ps3mgr/internal/ps2"
	"ps3mgr/internal/transfers"
)

type ps2Blocker struct {
	started chan struct{}
	release chan struct{}
}

func (p ps2Blocker) Process(ctx context.Context, _ ps2.Game, _ string, _ func(ps2.State), _ ps2.ProgressFunc) error {
	p.started <- struct{}{}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-p.release:
		return nil
	}
}

type ps3Blocker struct {
	started chan struct{}
	release chan struct{}
}

func (p ps3Blocker) UploadGame(ctx context.Context, _ string, _ domain.Game, _ string, _ func(ps3ftp.Progress)) error {
	p.started <- struct{}{}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-p.release:
		return nil
	}
}

type noEvents struct{}

func (noEvents) Publish(string, any) {}

func TestPS2AndPS3QueuesStartIndependently(t *testing.T) {
	ps2Started, ps3Started := make(chan struct{}, 1), make(chan struct{}, 1)
	ps2Release, ps3Release := make(chan struct{}), make(chan struct{})
	ps2Queue := ps2.NewQueue(ps2Blocker{ps2Started, ps2Release}, noEvents{})
	ps3Queue := transfers.New(ps3Blocker{ps3Started, ps3Release}, noEvents{}, "/dev_hdd0/GAMES")
	defer func() {
		close(ps2Release)
		close(ps3Release)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = ps2Queue.Close(ctx)
		_ = ps3Queue.Close(ctx)
	}()
	if _, err := ps2Queue.Enqueue([]ps2.Game{{Title: "slow conversion", Size: 1}}, "usb0"); err != nil {
		t.Fatal(err)
	}
	if _, err := ps3Queue.Enqueue([]domain.Game{{Title: "FTP transfer", Size: 1}}, "192.168.1.2", transfers.Options{}); err != nil {
		t.Fatal(err)
	}
	for name, ch := range map[string]<-chan struct{}{"PS2": ps2Started, "PS3": ps3Started} {
		select {
		case <-ch:
		case <-time.After(time.Second):
			t.Fatalf("%s queue was blocked by the other platform", name)
		}
	}
}
