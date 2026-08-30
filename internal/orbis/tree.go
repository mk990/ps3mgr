package orbis

import (
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// Tree is the filesystem that goes into a package image.
type Tree struct {
	root *fsDir
	ctx  context.Context
}

// NewTree returns an empty image tree.
func NewTree() *Tree { return &Tree{root: &fsDir{}, ctx: context.Background()} }

// WithContext makes long file copies abort when the context is cancelled.
func (t *Tree) WithContext(ctx context.Context) *Tree {
	if ctx != nil {
		t.ctx = ctx
	}
	return t
}

// DirTree builds an image tree from a directory on disk. File contents are
// streamed from disk when the package is written, so large files are never
// held in memory.
func DirTree(source string) (*Tree, error) {
	tree := NewTree()
	entries, err := os.ReadDir(source)
	if err != nil {
		return nil, err
	}
	if err := tree.addDir(tree.root, source, entries); err != nil {
		return nil, err
	}
	return tree, nil
}

func (t *Tree) addDir(parent *fsDir, source string, entries []os.DirEntry) error {
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		full := filepath.Join(source, entry.Name())
		if entry.IsDir() {
			child := &fsDir{name: entry.Name(), parent: parent}
			parent.dirs = append(parent.dirs, child)
			children, err := os.ReadDir(full)
			if err != nil {
				return err
			}
			if err := t.addDir(child, full, children); err != nil {
				return err
			}
			continue
		}
		if !entry.Type().IsRegular() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		parent.files = append(parent.files, t.diskFile(entry.Name(), parent, full, info.Size()))
	}
	return nil
}

func (t *Tree) diskFile(name string, parent *fsDir, source string, size int64) *fsFile {
	return &fsFile{
		name:   name,
		parent: parent,
		size:   size,
		write: func(w io.Writer) error {
			file, err := os.Open(source)
			if err != nil {
				return err
			}
			defer file.Close()
			reader := &contextReader{ctx: t.ctx, source: io.LimitReader(file, size)}
			written, err := io.CopyBuffer(w, reader, make([]byte, 1<<20))
			if err != nil {
				return err
			}
			if written != size {
				return fmt.Errorf("%s changed size while packaging", source)
			}
			return nil
		},
	}
}

// mkdirAll returns the directory at the given image path, creating it if needed.
func (t *Tree) mkdirAll(dirPath string) *fsDir {
	current := t.root
	for _, part := range strings.Split(dirPath, "/") {
		if part == "" || part == "." {
			continue
		}
		var next *fsDir
		for _, child := range current.dirs {
			if child.name == part {
				next = child
				break
			}
		}
		if next == nil {
			next = &fsDir{name: part, parent: current}
			current.dirs = append(current.dirs, next)
		}
		current = next
	}
	return current
}

func (t *Tree) put(target string, file *fsFile) {
	dir := t.mkdirAll(path.Dir(target))
	file.name = path.Base(target)
	file.parent = dir
	for i, existing := range dir.files {
		if existing.name == file.name {
			dir.files[i] = file
			return
		}
	}
	dir.files = append(dir.files, file)
}

// AddFile adds or replaces a file whose contents are streamed from disk.
func (t *Tree) AddFile(target, source string) error {
	info, err := os.Stat(source)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", source)
	}
	t.put(target, t.diskFile(path.Base(target), nil, source, info.Size()))
	return nil
}

// AddData adds or replaces a file with in-memory contents.
func (t *Tree) AddData(target string, data []byte) {
	t.put(target, &fsFile{
		size:  int64(len(data)),
		write: func(w io.Writer) error { _, err := w.Write(data); return err },
	})
}

// ReadFile returns the contents of a small file in the image.
func (t *Tree) ReadFile(target string) ([]byte, error) {
	file := t.root.getFile(target)
	if file == nil {
		return nil, fmt.Errorf("%s is not in the image", target)
	}
	return collectFile(file)
}

// contextReader aborts a copy when its context is cancelled.
type contextReader struct {
	ctx    context.Context
	source io.Reader
}

func (c *contextReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.source.Read(p)
}
