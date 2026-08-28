package ps4

import (
	"context"
	"sync"
	"testing"
	"time"

	"ps3mgr/internal/domain"
	ps3ftp "ps3mgr/internal/ftp"
	"ps3mgr/internal/transfers"
)

type testProvider struct{}

func (testProvider) Register(pkg Package) ([]string, func(), error) {
	return []string{"http://manager/" + pkg.Title + ".pkg"}, func() {}, nil
}

type immediateInstaller struct {
	mu    sync.Mutex
	order []string
}

func (i *immediateInstaller) Install(_ context.Context, _ string, urls []string) (int, error) {
	i.mu.Lock()
	i.order = append(i.order, urls[0])
	task := len(i.order)
	i.mu.Unlock()
	return task, nil
}
func (*immediateInstaller) Progress(context.Context, string, int) (InstallProgress, error) {
	return InstallProgress{Transferred: 100, Total: 100, Complete: true}, nil
}
func (*immediateInstaller) IsInstalled(context.Context, string, string) (bool, error) {
	return true, nil
}
func (*immediateInstaller) Cancel(context.Context, string, int) error { return nil }

func TestQueueProcessesPackagesSequentially(t *testing.T) {
	installer := &immediateInstaller{}
	queue := NewQueue(installer, testProvider{}, nil)
	queue.pollEvery = time.Millisecond
	defer queue.Close(context.Background())
	items, err := queue.Enqueue([]Package{{Title: "first", Format: "pkg-patch", Size: 100}, {Title: "second", Format: "pkg-patch", Size: 100}}, "192.168.1.4", false)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		last, _ := queue.Get(items[1].ID)
		if last.State == StateCompleted {
			installer.mu.Lock()
			defer installer.mu.Unlock()
			if len(installer.order) != 2 || installer.order[0] != "http://manager/first.pkg" || installer.order[1] != "http://manager/second.pkg" {
				t.Fatalf("wrong processing order: %v", installer.order)
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("queue did not complete: %+v", queue.List())
}

type blockingInstaller struct{ started chan struct{} }

func (b blockingInstaller) Install(ctx context.Context, _ string, _ []string) (int, error) {
	b.started <- struct{}{}
	<-ctx.Done()
	return 0, ctx.Err()
}
func (blockingInstaller) Progress(context.Context, string, int) (InstallProgress, error) {
	return InstallProgress{}, nil
}
func (blockingInstaller) IsInstalled(context.Context, string, string) (bool, error) {
	return false, nil
}
func (blockingInstaller) Cancel(context.Context, string, int) error { return nil }

type ps3StartUploader struct{ started chan struct{} }

func (u ps3StartUploader) UploadGame(ctx context.Context, _ string, _ domain.Game, _ string, _ func(ps3ftp.Progress)) error {
	u.started <- struct{}{}
	<-ctx.Done()
	return ctx.Err()
}

func TestPS4QueueCannotBlockPS3Queue(t *testing.T) {
	ps4Started, ps3Started := make(chan struct{}, 1), make(chan struct{}, 1)
	ps4Queue := NewQueue(blockingInstaller{ps4Started}, testProvider{}, nil)
	ps3Queue := transfers.New(ps3StartUploader{ps3Started}, nil, "/dev_hdd0/GAMES")
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = ps4Queue.Close(ctx)
		_ = ps3Queue.Close(ctx)
	}()
	if _, err := ps4Queue.Enqueue([]Package{{Title: "slow PKG", Size: 1}}, "192.168.1.4", false); err != nil {
		t.Fatal(err)
	}
	if _, err := ps3Queue.Enqueue([]domain.Game{{Title: "PS3 FTP", Size: 1}}, "192.168.1.3", transfers.Options{}); err != nil {
		t.Fatal(err)
	}
	for name, started := range map[string]<-chan struct{}{"PS4": ps4Started, "PS3": ps3Started} {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatalf("%s queue was blocked by another platform", name)
		}
	}
}
