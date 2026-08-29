package transfers

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"ps3mgr/internal/domain"
	ps3ftp "ps3mgr/internal/ftp"
)

type fakeUploader struct {
	mu        sync.Mutex
	active    int
	maxActive int
	order     []string
	fail      map[string]int
	wait      map[string]bool
}

func (u *fakeUploader) UploadGame(ctx context.Context, _ string, game domain.Game, _ string, progress func(ps3ftp.Progress)) error {
	u.mu.Lock()
	u.active++
	if u.active > u.maxActive {
		u.maxActive = u.active
	}
	u.order = append(u.order, game.Title)
	shouldFail := u.fail[game.Title] > 0
	if shouldFail {
		u.fail[game.Title]--
	}
	shouldWait := u.wait[game.Title]
	u.mu.Unlock()
	defer func() { u.mu.Lock(); u.active--; u.mu.Unlock() }()
	if shouldWait {
		<-ctx.Done()
		return ctx.Err()
	}
	progress(ps3ftp.Progress{File: "data.bin", Delta: game.Size})
	if shouldFail {
		return errors.New("simulated network failure")
	}
	time.Sleep(10 * time.Millisecond)
	return nil
}

type discardPublisher struct{}

func (discardPublisher) Publish(string, any) {}

type fakeDownloader struct{}

func (fakeDownloader) DownloadGame(_ context.Context, _ string, _ domain.Game, _ string, progress func(ps3ftp.Progress)) error {
	progress(ps3ftp.Progress{File: "app.pkg", Total: 1024})
	progress(ps3ftp.Progress{File: "app.pkg", Delta: 1024, Total: 1024})
	return nil
}

func TestDownloadManagerDiscoversRemoteTotalAndDirection(t *testing.T) {
	manager := NewDownload(fakeDownloader{}, discardPublisher{}, t.TempDir(), domain.PlatformPS4)
	defer closeManager(t, manager)
	created, err := manager.Enqueue([]domain.Game{{Title: "Remote game"}}, "127.0.0.1", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if created[0].Direction != domain.TransferDownload {
		t.Fatalf("created direction = %q", created[0].Direction)
	}
	waitState(t, manager, created[0].ID, domain.QueueCompleted)
	item, _ := manager.Get(created[0].ID)
	if item.TotalBytes != 1024 || item.BytesTransferred != 1024 || item.Percentage != 100 {
		t.Fatalf("completed pull progress = %+v", item)
	}
}

func TestManagerIsSequentialAndContinuesAfterFailure(t *testing.T) {
	uploader := &fakeUploader{fail: map[string]int{"first": 1}, wait: map[string]bool{}}
	manager := New(uploader, discardPublisher{}, "/games")
	defer closeManager(t, manager)
	created, err := manager.Enqueue([]domain.Game{{Title: "first", Size: 1}, {Title: "second", Size: 2}}, "127.0.0.1", Options{})
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, manager, created[1].ID, domain.QueueCompleted)
	first, _ := manager.Get(created[0].ID)
	if first.State != domain.QueueFailed {
		t.Fatalf("first state = %s", first.State)
	}
	uploader.mu.Lock()
	defer uploader.mu.Unlock()
	if uploader.maxActive != 1 || len(uploader.order) != 2 || uploader.order[0] != "first" || uploader.order[1] != "second" {
		t.Fatalf("transfers were not sequential: max=%d order=%v", uploader.maxActive, uploader.order)
	}
}

func TestManagerCancellationContinuesAndRetrySucceeds(t *testing.T) {
	uploader := &fakeUploader{fail: map[string]int{}, wait: map[string]bool{"blocked": true}}
	manager := New(uploader, discardPublisher{}, "/games")
	defer closeManager(t, manager)
	created, err := manager.Enqueue([]domain.Game{{Title: "blocked", Size: 1}, {Title: "next", Size: 1}}, "127.0.0.1", Options{})
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, manager, created[0].ID, domain.QueueTransferring)
	if err := manager.Cancel(created[0].ID); err != nil {
		t.Fatal(err)
	}
	waitState(t, manager, created[1].ID, domain.QueueCompleted)
	first, _ := manager.Get(created[0].ID)
	if first.State != domain.QueueCancelled {
		t.Fatalf("cancelled state = %s", first.State)
	}
	uploader.mu.Lock()
	uploader.wait["blocked"] = false
	uploader.mu.Unlock()
	if err := manager.Retry(created[0].ID); err != nil {
		t.Fatal(err)
	}
	waitState(t, manager, created[0].ID, domain.QueueCompleted)
}

func TestManagerStopOnErrorCancelsRemaining(t *testing.T) {
	uploader := &fakeUploader{fail: map[string]int{"bad": 1}, wait: map[string]bool{}}
	manager := New(uploader, discardPublisher{}, "/games")
	defer closeManager(t, manager)
	created, err := manager.Enqueue([]domain.Game{{Title: "bad"}, {Title: "never"}}, "127.0.0.1", Options{StopOnError: true})
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, manager, created[1].ID, domain.QueueCancelled)
}

func TestPS5AndPS3QueuesCannotBlockEachOther(t *testing.T) {
	for _, test := range []struct {
		name            string
		blockedPlatform domain.Platform
	}{
		{name: "slow PS5 does not block PS3", blockedPlatform: domain.PlatformPS5},
		{name: "slow PS3 does not block PS5", blockedPlatform: domain.PlatformPS3},
	} {
		t.Run(test.name, func(t *testing.T) {
			ps3Uploader := &fakeUploader{fail: map[string]int{}, wait: map[string]bool{}}
			ps5Uploader := &fakeUploader{fail: map[string]int{}, wait: map[string]bool{}}
			if test.blockedPlatform == domain.PlatformPS3 {
				ps3Uploader.wait["blocked"] = true
			} else {
				ps5Uploader.wait["blocked"] = true
			}
			ps3Queue := New(ps3Uploader, discardPublisher{}, "/dev_hdd0/GAMES")
			ps5Queue := NewPlatform(ps5Uploader, discardPublisher{}, "/data/etaHEN/games", domain.PlatformPS5)
			defer closeManager(t, ps3Queue)
			defer closeManager(t, ps5Queue)

			blockedQueue, freeQueue := ps5Queue, ps3Queue
			if test.blockedPlatform == domain.PlatformPS3 {
				blockedQueue, freeQueue = ps3Queue, ps5Queue
			}
			blocked, err := blockedQueue.Enqueue([]domain.Game{{Title: "blocked", Size: 1}}, "127.0.0.1", Options{})
			if err != nil {
				t.Fatal(err)
			}
			waitState(t, blockedQueue, blocked[0].ID, domain.QueueTransferring)
			free, err := freeQueue.Enqueue([]domain.Game{{Title: "free", Size: 1}}, "127.0.0.1", Options{})
			if err != nil {
				t.Fatal(err)
			}
			waitState(t, freeQueue, free[0].ID, domain.QueueCompleted)
			if err := blockedQueue.Cancel(blocked[0].ID); err != nil {
				t.Fatal(err)
			}
			waitState(t, blockedQueue, blocked[0].ID, domain.QueueCancelled)
		})
	}
}

func waitState(t *testing.T, manager *Manager, id string, wanted domain.QueueState) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		item, ok := manager.Get(id)
		if ok && item.State == wanted {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	item, _ := manager.Get(id)
	t.Fatalf("transfer %s did not reach %s; state %s", id, wanted, item.State)
}

func closeManager(t *testing.T, manager *Manager) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := manager.Close(ctx); err != nil {
		t.Fatal(err)
	}
}
