package ps4

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
)

// PendingTask identifies a Remote Package Installer task that has been
// registered on a console but not yet confirmed cleaned up (either by a
// successful install or an explicit cancel/unregister).
type PendingTask struct {
	ConsoleIP string `json:"console_ip"`
	TaskID    int    `json:"task_id"`
}

// TaskStore durably tracks in-flight RPI tasks so an orphaned registration
// survives a ps3mgr crash or restart and can be cleaned up on the next
// startup, instead of permanently colliding with future installs of the
// same content (Remote Package Installer error 0x80990015).
type TaskStore interface {
	Add(PendingTask) error
	Remove(PendingTask) error
	List() ([]PendingTask, error)
}

// FileTaskStore persists pending tasks as JSON on disk.
type FileTaskStore struct {
	path string
	mu   sync.Mutex
}

func NewFileTaskStore(path string) *FileTaskStore {
	return &FileTaskStore{path: path}
}

func (s *FileTaskStore) Add(task PendingTask) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tasks, err := s.readLocked()
	if err != nil {
		return err
	}
	for _, existing := range tasks {
		if existing == task {
			return nil
		}
	}
	return s.writeLocked(append(tasks, task))
}

func (s *FileTaskStore) Remove(task PendingTask) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tasks, err := s.readLocked()
	if err != nil {
		return err
	}
	kept := tasks[:0]
	for _, existing := range tasks {
		if existing != task {
			kept = append(kept, existing)
		}
	}
	return s.writeLocked(kept)
}

func (s *FileTaskStore) List() ([]PendingTask, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readLocked()
}

func (s *FileTaskStore) readLocked() ([]PendingTask, error) {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, nil
	}
	var tasks []PendingTask
	if err := json.Unmarshal(data, &tasks); err != nil {
		return nil, err
	}
	return tasks, nil
}

func (s *FileTaskStore) writeLocked(tasks []PendingTask) error {
	data, err := json.Marshal(tasks)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
