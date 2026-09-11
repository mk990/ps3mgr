package ps4

import (
	"path/filepath"
	"testing"
)

func TestFileTaskStoreAddRemoveList(t *testing.T) {
	store := NewFileTaskStore(filepath.Join(t.TempDir(), "pending.json"))

	if tasks, err := store.List(); err != nil || len(tasks) != 0 {
		t.Fatalf("expected empty store, got %v, err %v", tasks, err)
	}

	first := PendingTask{ConsoleIP: "192.168.1.4", TaskID: 7}
	second := PendingTask{ConsoleIP: "192.168.1.5", TaskID: 9}
	if err := store.Add(first); err != nil {
		t.Fatal(err)
	}
	if err := store.Add(second); err != nil {
		t.Fatal(err)
	}
	if err := store.Add(first); err != nil { // duplicate add must not duplicate the entry
		t.Fatal(err)
	}

	tasks, err := store.List()
	if err != nil || len(tasks) != 2 {
		t.Fatalf("expected 2 pending tasks, got %v, err %v", tasks, err)
	}

	if err := store.Remove(first); err != nil {
		t.Fatal(err)
	}
	tasks, err = store.List()
	if err != nil || len(tasks) != 1 || tasks[0] != second {
		t.Fatalf("expected only %v left, got %v, err %v", second, tasks, err)
	}
}

func TestFileTaskStorePersistsAcrossInstances(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "pending.json")
	task := PendingTask{ConsoleIP: "192.168.1.4", TaskID: 3}

	if err := NewFileTaskStore(path).Add(task); err != nil {
		t.Fatal(err)
	}

	tasks, err := NewFileTaskStore(path).List()
	if err != nil || len(tasks) != 1 || tasks[0] != task {
		t.Fatalf("expected persisted task %v, got %v, err %v", task, tasks, err)
	}
}
