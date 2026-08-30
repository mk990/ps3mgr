package orbis

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Reader gives read access to a fake PKG and the filesystem inside it.
type Reader struct {
	file  *os.File
	info  *PkgInfo
	metas []metaEntry
	inner *pfsReader
}

// OpenPackage opens a PKG and unlocks its filesystem with the given passcode.
func OpenPackage(path, passcode string) (*Reader, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	reader, err := newReader(file, passcode)
	if err != nil {
		file.Close()
		return nil, err
	}
	return reader, nil
}

func newReader(file *os.File, passcode string) (*Reader, error) {
	head := make([]byte, 0x1000)
	if _, err := file.ReadAt(head, 0); err != nil {
		return nil, fmt.Errorf("read PKG header: %w", err)
	}
	info, err := parsePkgHeader(head)
	if err != nil {
		return nil, err
	}
	reader := &Reader{file: file, info: info}

	tableOffset := int64(binary.BigEndian.Uint32(head[0x18:]))
	count := int(info.EntryCount)
	if count <= 0 || count > 4096 {
		return nil, fmt.Errorf("PKG declares an implausible entry count: %d", count)
	}
	table := make([]byte, count*32)
	if _, err := file.ReadAt(table, tableOffset); err != nil {
		return nil, fmt.Errorf("read PKG entry table: %w", err)
	}
	for i := 0; i < count; i++ {
		reader.metas = append(reader.metas, parseMetaEntry(table[i*32:]))
	}

	if info.PfsImageSize == 0 {
		return reader, nil
	}
	ekpfs, err := computeKeys(info.ContentID, passcode, 1)
	if err != nil {
		return nil, err
	}
	outerSection := io.NewSectionReader(file, int64(info.PfsImageOffset), int64(info.PfsImageSize))
	outer, err := newPfsReader(outerSection, ekpfs)
	if err != nil {
		return nil, fmt.Errorf("read outer PFS (wrong passcode?): %w", err)
	}
	image := outer.uroot.Find("pfs_image.dat")
	if image == nil {
		return nil, fmt.Errorf("PKG does not contain pfs_image.dat")
	}
	source := image.Reader()
	if image.Compressed() {
		pfsc, err := newPfscReader(source)
		if err != nil {
			return nil, err
		}
		source = pfsc
	}
	inner, err := newPfsReader(source, nil)
	if err != nil {
		return nil, fmt.Errorf("read inner PFS: %w", err)
	}
	reader.inner = inner
	return reader, nil
}

// Close releases the underlying file.
func (r *Reader) Close() error { return r.file.Close() }

// Info returns the parsed header fields.
func (r *Reader) Info() PkgInfo { return *r.info }

// EntryData returns the raw bytes of an entry. Encrypted entries are not
// supported because they are not needed to rebuild a package.
func (r *Reader) EntryData(id uint32) ([]byte, error) {
	for _, meta := range r.metas {
		if meta.id != id {
			continue
		}
		if meta.encrypted() {
			return nil, fmt.Errorf("PKG entry %#x is encrypted", id)
		}
		data := make([]byte, meta.dataSize)
		if _, err := r.file.ReadAt(data, int64(meta.dataOffset)); err != nil {
			return nil, err
		}
		return data, nil
	}
	return nil, fmt.Errorf("PKG entry %#x is missing", id)
}

// ParamSFO returns the parsed param.sfo entry.
func (r *Reader) ParamSFO() (*ParamSFO, error) {
	data, err := r.EntryData(entryParamSFO)
	if err != nil {
		return nil, err
	}
	return ParseSFO(data)
}

// Root returns the root of the filesystem inside the package.
func (r *Reader) Root() *PfsNode {
	if r.inner == nil {
		return nil
	}
	return r.inner.uroot
}

// ExtractFiles writes the whole filesystem image to the given directory.
func (r *Reader) ExtractFiles(destination string) error {
	if r.inner == nil {
		return fmt.Errorf("PKG has no filesystem image")
	}
	return extractNode(r.inner.uroot, destination)
}

func extractNode(node *PfsNode, destination string) error {
	for _, child := range node.Children {
		if strings.Contains(child.Name, "..") || strings.ContainsAny(child.Name, `/\`) {
			return fmt.Errorf("refusing to extract suspicious path %q", child.Path)
		}
		target := filepath.Join(destination, child.Name)
		if child.IsDir {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			if err := extractNode(child, target); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(destination, 0o755); err != nil {
			return err
		}
		if err := writeNode(child, target); err != nil {
			return fmt.Errorf("extract %s: %w", child.Path, err)
		}
	}
	return nil
}

func writeNode(node *PfsNode, target string) error {
	out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer out.Close()
	source := node.Reader()
	size := node.Size
	if node.Compressed() {
		pfsc, err := newPfscReader(source)
		if err != nil {
			return err
		}
		source = pfsc
		size = node.CompSize
	}
	if _, err := io.Copy(out, io.NewSectionReader(source, 0, size)); err != nil {
		return err
	}
	return out.Close()
}

// EntryByName returns the bytes of an entry addressed by its file name, such as
// "param.sfo" or "icon0.png".
func (r *Reader) EntryByName(name string) ([]byte, error) {
	id, known := entryNameToID[name]
	if !known {
		return nil, fmt.Errorf("%q is not a known PKG entry", name)
	}
	return r.EntryData(id)
}

// HasEntry reports whether the package carries the named entry.
func (r *Reader) HasEntry(name string) bool {
	id, known := entryNameToID[name]
	if !known {
		return false
	}
	for _, meta := range r.metas {
		if meta.id == id {
			return true
		}
	}
	return false
}
