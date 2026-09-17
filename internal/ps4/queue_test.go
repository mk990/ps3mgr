package ps4

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"ps3mgr/internal/domain"
	ps3ftp "ps3mgr/internal/ftp"
	"ps3mgr/internal/transfers"
)

type recordingEvents struct {
	mu     sync.Mutex
	events []string
}

func (r *recordingEvents) Publish(eventType string, _ any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, eventType)
}

func (r *recordingEvents) has(eventType string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, event := range r.events {
		if event == eventType {
			return true
		}
	}
	return false
}

// noopTaskControl supplies the task-control half of Installer for fakes that
// only exercise the install path.
type noopTaskControl struct{}

func (noopTaskControl) Pause(context.Context, string, int) error  { return nil }
func (noopTaskControl) Resume(context.Context, string, int) error { return nil }
func (noopTaskControl) FindTask(context.Context, string, string, int) (int, bool, error) {
	return 0, false, nil
}

type failingCancelInstaller struct{ noopTaskControl }

func (*failingCancelInstaller) Install(context.Context, string, []string) (int, error) {
	return 11, nil
}
func (*failingCancelInstaller) Progress(context.Context, string, int) (InstallProgress, error) {
	return InstallProgress{Transferred: 100, Total: 100, Complete: true}, nil
}
func (*failingCancelInstaller) IsInstalled(context.Context, string, string) (bool, error) {
	return false, nil
}
func (*failingCancelInstaller) Cancel(context.Context, string, int) error {
	return fmt.Errorf("connection refused")
}

type testProvider struct{}

func (testProvider) Register(pkg Package) ([]string, func(), error) {
	return []string{"http://manager/" + pkg.Title + ".pkg"}, func() {}, nil
}

type immediateInstaller struct {
	noopTaskControl
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
	queue := NewQueue(installer, testProvider{}, nil, nil)
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

// baseVerifyInstaller completes every install but reports failTitle as not
// installed, so a base game with that title ID fails its post-install
// verification while other titles succeed.
type baseVerifyInstaller struct {
	noopTaskControl
	failTitle string
}

func (*baseVerifyInstaller) Install(context.Context, string, []string) (int, error) { return 5, nil }
func (*baseVerifyInstaller) Progress(context.Context, string, int) (InstallProgress, error) {
	return InstallProgress{Transferred: 100, Total: 100, Complete: true}, nil
}
func (b *baseVerifyInstaller) IsInstalled(_ context.Context, _ string, titleID string) (bool, error) {
	return titleID != b.failTitle, nil
}
func (*baseVerifyInstaller) Cancel(context.Context, string, int) error { return nil }

func TestBaseGameFailureCancelsOnlyItsDependents(t *testing.T) {
	installer := &baseVerifyInstaller{failTitle: "CUSA00001"}
	queue := NewQueue(installer, testProvider{}, nil, nil)
	queue.pollEvery = time.Millisecond
	defer queue.Close(context.Background())
	items, err := queue.Enqueue([]Package{
		{Title: "A base", TitleID: "CUSA00001", Format: "pkg-game", Size: 100},
		{Title: "A patch", TitleID: "CUSA00001", Format: "pkg-patch", Size: 100},
		{Title: "A dlc", TitleID: "CUSA00001", Format: "pkg-dlc", Size: 100},
		{Title: "B base", TitleID: "CUSA00002", Format: "pkg-game", Size: 100},
	}, "192.168.1.4", false)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if last, _ := queue.Get(items[3].ID); last.State == StateCompleted {
			break
		}
		time.Sleep(time.Millisecond)
	}
	states := make(map[string]JobState)
	for _, job := range queue.List() {
		states[job.Package.Title] = job.State
	}
	want := map[string]JobState{
		"A base":  StateFailed,
		"A patch": StateCancelled,
		"A dlc":   StateCancelled,
		"B base":  StateCompleted,
	}
	for title, wantState := range want {
		if states[title] != wantState {
			t.Fatalf("%q state = %s, want %s (all: %+v)", title, states[title], wantState, states)
		}
	}
}

type unverifiedInstaller struct {
	noopTaskControl
	mu        sync.Mutex
	cancelled []int
}

func (*unverifiedInstaller) Install(context.Context, string, []string) (int, error) { return 7, nil }
func (*unverifiedInstaller) Progress(context.Context, string, int) (InstallProgress, error) {
	return InstallProgress{Transferred: 100, Total: 100, Complete: true}, nil
}
func (*unverifiedInstaller) IsInstalled(context.Context, string, string) (bool, error) {
	return false, nil
}
func (u *unverifiedInstaller) Cancel(_ context.Context, _ string, taskID int) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.cancelled = append(u.cancelled, taskID)
	return nil
}

func TestQueueUnregistersTaskWhenPostInstallVerificationFails(t *testing.T) {
	installer := &unverifiedInstaller{}
	queue := NewQueue(installer, testProvider{}, nil, nil)
	queue.pollEvery = time.Millisecond
	defer queue.Close(context.Background())
	items, err := queue.Enqueue([]Package{{Title: "unverified", TitleID: "CUSA10416", Format: "pkg-game", Size: 100}}, "192.168.1.4", false)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		last, _ := queue.Get(items[0].ID)
		if last.State == StateFailed {
			installer.mu.Lock()
			defer installer.mu.Unlock()
			if len(installer.cancelled) != 1 || installer.cancelled[0] != 7 {
				t.Fatalf("expected leftover task 7 to be cancelled, got %v", installer.cancelled)
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("queue did not fail: %+v", queue.List())
}

type blockingInstaller struct {
	noopTaskControl
	started chan struct{}
}

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
	ps4Queue := NewQueue(blockingInstaller{started: ps4Started}, testProvider{}, nil, nil)
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

func TestQueuePublishesCleanupFailureAndKeepsPendingTask(t *testing.T) {
	events := &recordingEvents{}
	store := NewFileTaskStore(filepath.Join(t.TempDir(), "pending.json"))
	queue := NewQueue(&failingCancelInstaller{}, testProvider{}, events, store)
	queue.pollEvery = time.Millisecond
	defer queue.Close(context.Background())
	items, err := queue.Enqueue([]Package{{Title: "unverified", TitleID: "CUSA10416", Format: "pkg-game", Size: 100}}, "192.168.1.4", false)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		last, _ := queue.Get(items[0].ID)
		if last.State == StateFailed {
			if !events.has("ps4.task.cleanup_failed") {
				t.Fatalf("expected ps4.task.cleanup_failed event, got %v", events.events)
			}
			tasks, err := store.List()
			want := PendingTask{ConsoleIP: "192.168.1.4", TaskID: 11}
			if err != nil || len(tasks) != 1 || tasks[0] != want {
				t.Fatalf("expected orphaned task %v to stay tracked for retry, got %v, err %v", want, tasks, err)
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("queue did not fail: %+v", queue.List())
}

func TestQueueReconcileOrphanedTasksClearsPersistedEntries(t *testing.T) {
	store := NewFileTaskStore(filepath.Join(t.TempDir(), "pending.json"))
	orphan := PendingTask{ConsoleIP: "192.168.1.9", TaskID: 42}
	if err := store.Add(orphan); err != nil {
		t.Fatal(err)
	}

	installer := &unverifiedInstaller{} // Cancel succeeds and records the task ID.
	events := &recordingEvents{}
	queue := NewQueue(installer, testProvider{}, events, store)
	defer queue.Close(context.Background())

	queue.ReconcileOrphanedTasks(context.Background())

	installer.mu.Lock()
	cancelled := append([]int(nil), installer.cancelled...)
	installer.mu.Unlock()
	if len(cancelled) != 1 || cancelled[0] != orphan.TaskID {
		t.Fatalf("expected orphan task %d to be cancelled on startup, got %v", orphan.TaskID, cancelled)
	}
	if !events.has("ps4.task.orphan_cleared") {
		t.Fatalf("expected ps4.task.orphan_cleared event, got %v", events.events)
	}
	if tasks, err := store.List(); err != nil || len(tasks) != 0 {
		t.Fatalf("expected pending store to be empty after reconciliation, got %v, err %v", tasks, err)
	}
}

func TestQueueReconcileOrphanedTasksKeepsEntryWhenCancelFails(t *testing.T) {
	store := NewFileTaskStore(filepath.Join(t.TempDir(), "pending.json"))
	orphan := PendingTask{ConsoleIP: "192.168.1.9", TaskID: 42}
	if err := store.Add(orphan); err != nil {
		t.Fatal(err)
	}

	events := &recordingEvents{}
	queue := NewQueue(&failingCancelInstaller{}, testProvider{}, events, store)
	defer queue.Close(context.Background())

	queue.ReconcileOrphanedTasks(context.Background())

	if !events.has("ps4.task.orphan_cleanup_failed") {
		t.Fatalf("expected ps4.task.orphan_cleanup_failed event, got %v", events.events)
	}
	tasks, err := store.List()
	if err != nil || len(tasks) != 1 || tasks[0] != orphan {
		t.Fatalf("expected orphan entry to remain tracked for a later retry, got %v, err %v", tasks, err)
	}
}

// stallingInstaller keeps a job in DOWNLOADING so its task can be paused and
// resumed, and records every task-control call the queue makes.
type stallingInstaller struct {
	mu       sync.Mutex
	calls    []string
	existing int
}

func (s *stallingInstaller) record(call string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, call)
}
func (s *stallingInstaller) history() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls...)
}
func (s *stallingInstaller) Install(context.Context, string, []string) (int, error) {
	s.record("install")
	return 21, nil
}
func (*stallingInstaller) Progress(context.Context, string, int) (InstallProgress, error) {
	return InstallProgress{Transferred: 50, Total: 100}, nil
}
func (*stallingInstaller) IsInstalled(context.Context, string, string) (bool, error) {
	return true, nil
}
func (s *stallingInstaller) Cancel(context.Context, string, int) error {
	s.record("cancel")
	return nil
}
func (s *stallingInstaller) Pause(_ context.Context, _ string, taskID int) error {
	s.record(fmt.Sprintf("pause:%d", taskID))
	return nil
}
func (s *stallingInstaller) Resume(_ context.Context, _ string, taskID int) error {
	s.record(fmt.Sprintf("resume:%d", taskID))
	return nil
}
func (s *stallingInstaller) FindTask(_ context.Context, _ string, contentID string, subType int) (int, bool, error) {
	s.record(fmt.Sprintf("find:%s:%d", contentID, subType))
	if s.existing == 0 {
		return 0, false, nil
	}
	return s.existing, true, nil
}

func waitForState(t *testing.T, queue *Queue, id string, state JobState) Job {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if job, ok := queue.Get(id); ok && job.State == state {
			return job
		}
		time.Sleep(time.Millisecond)
	}
	job, _ := queue.Get(id)
	t.Fatalf("job never reached %s, last state %s", state, job.State)
	return Job{}
}

func TestQueuePausesAndResumesRunningJob(t *testing.T) {
	installer := &stallingInstaller{}
	events := &recordingEvents{}
	queue := NewQueue(installer, testProvider{}, events, nil)
	queue.pollEvery = time.Millisecond
	defer queue.Close(context.Background())
	items, err := queue.Enqueue([]Package{{Title: "stalled", Format: "pkg-patch", Size: 100}}, "192.168.1.4", false)
	if err != nil {
		t.Fatal(err)
	}
	id := items[0].ID
	waitForState(t, queue, id, StateDownloading)

	if err := queue.PauseJob(id); err != nil {
		t.Fatalf("pause: %v", err)
	}
	job, _ := queue.Get(id)
	if job.State != StatePaused {
		t.Fatalf("state = %s, want %s", job.State, StatePaused)
	}
	// A paused job still holds the console task, so the queue must not treat
	// it as finished and start the next job on top of it.
	if !isActive(StatePaused) {
		t.Fatal("a paused job must still count as active")
	}
	if err := queue.PauseJob(id); err == nil {
		t.Fatal("pausing an already paused job was accepted")
	}

	if err := queue.ResumeJob(id); err != nil {
		t.Fatalf("resume: %v", err)
	}
	waitForState(t, queue, id, StateDownloading)
	if err := queue.ResumeJob(id); err == nil {
		t.Fatal("resuming a running job was accepted")
	}

	calls := installer.history()
	if len(calls) < 3 || calls[0] != "install" || calls[1] != "pause:21" || calls[2] != "resume:21" {
		t.Fatalf("task control calls = %v", calls)
	}
	if !events.has("ps4.job.paused") || !events.has("ps4.job.resumed") {
		t.Fatalf("missing pause/resume events: %v", events.events)
	}
}

func TestQueueRejectsPauseForJobWithoutConsoleTask(t *testing.T) {
	installer := &blockingInstaller{started: make(chan struct{}, 1)}
	queue := NewQueue(installer, testProvider{}, nil, nil)
	queue.pollEvery = time.Millisecond
	defer queue.Close(context.Background())
	items, err := queue.Enqueue([]Package{{Title: "first", Format: "pkg-patch", Size: 100}, {Title: "second", Format: "pkg-patch", Size: 100}}, "192.168.1.4", false)
	if err != nil {
		t.Fatal(err)
	}
	<-installer.started
	// The second job is still WAITING and owns no task on the console.
	if err := queue.PauseJob(items[1].ID); err == nil {
		t.Fatal("pausing a waiting job was accepted")
	}
	if err := queue.PauseJob("ps4-job-missing"); err == nil {
		t.Fatal("pausing an unknown job was accepted")
	}
}

// A task registered by an earlier process keeps downloading on the console, so
// the job must attach to it rather than register a colliding second task.
func TestQueueReattachesToExistingConsoleTask(t *testing.T) {
	installer := &stallingInstaller{existing: 88}
	events := &recordingEvents{}
	queue := NewQueue(installer, testProvider{}, events, nil)
	queue.pollEvery = time.Millisecond
	defer queue.Close(context.Background())
	items, err := queue.Enqueue([]Package{{Title: "orphaned", ContentID: "UP0001-CUSA12345_00-ABCDEFGHIJKLMNOP", Format: "pkg-patch", Size: 100}}, "192.168.1.4", false)
	if err != nil {
		t.Fatal(err)
	}
	job := waitForState(t, queue, items[0].ID, StateDownloading)
	if job.TaskID != 88 {
		t.Fatalf("task = %d, want the existing console task 88", job.TaskID)
	}
	calls := installer.history()
	if len(calls) < 2 || calls[0] != "find:UP0001-CUSA12345_00-ABCDEFGHIJKLMNOP:0" || calls[1] != "resume:88" {
		t.Fatalf("task control calls = %v", calls)
	}
	for _, call := range calls {
		if call == "install" {
			t.Fatalf("a second task was registered for content already installing: %v", calls)
		}
	}
	if !events.has("ps4.install.reattached") {
		t.Fatalf("missing re-attach event: %v", events.events)
	}
}

// A package with no content ID cannot be looked up, and a console that has no
// task for one that can must still take the normal registration path.
func TestQueueRegistersFreshTaskWhenNoConsoleTaskExists(t *testing.T) {
	installer := &stallingInstaller{}
	queue := NewQueue(installer, testProvider{}, nil, nil)
	queue.pollEvery = time.Millisecond
	defer queue.Close(context.Background())
	items, err := queue.Enqueue([]Package{{Title: "fresh", Format: "pkg-patch", Size: 100}}, "192.168.1.4", false)
	if err != nil {
		t.Fatal(err)
	}
	job := waitForState(t, queue, items[0].ID, StateDownloading)
	if job.TaskID != 21 {
		t.Fatalf("task = %d, want a newly registered task", job.TaskID)
	}
	if calls := installer.history(); len(calls) != 1 || calls[0] != "install" {
		t.Fatalf("task control calls = %v, want a bare install for a package with no content ID", calls)
	}
}
