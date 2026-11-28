// Package pkgicon reads the public icon0 entry in PlayStation packages.
package pkgicon

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/http"
)

var ErrNotFound = errors.New("package entry not found")

// Find returns a reader limited to the embedded image. PS5 FIH and LIH
// wrappers store offsets relative to the inner CNT header.
func Find(ctx context.Context, file io.ReaderAt, size int64) (*io.SectionReader, string, error) {
	icon, err := findEntry(ctx, file, size, 0x1200, 20<<20)
	if err != nil {
		return nil, "", err
	}
	var prefix [512]byte
	n, err := icon.ReadAt(prefix[:min(icon.Size(), 512)], 0)
	if err != nil {
		return nil, "", err
	}
	switch http.DetectContentType(prefix[:n]) {
	case "image/png":
		return icon, ".png", nil
	case "image/jpeg":
		return icon, ".jpg", nil
	case "image/webp":
		return icon, ".webp", nil
	default:
		return nil, "", fmt.Errorf("unsupported icon0 image type")
	}
}

// PS5Param reads the public param.json entry from a PS5 package.
func PS5Param(ctx context.Context, file io.ReaderAt, size int64) ([]byte, error) {
	entry, err := findEntry(ctx, file, size, 0x2000, 1<<20)
	if err != nil {
		return nil, err
	}
	return io.ReadAll(entry)
}

func findEntry(ctx context.Context, file io.ReaderAt, size int64, id uint32, limit int64) (*io.SectionReader, error) {
	var header [32]byte
	if _, err := file.ReadAt(header[:], 0); err != nil {
		return nil, ErrNotFound
	}
	var base int64
	switch string(header[:4]) {
	case "\x7fFIH", "\x7fLIH":
		offset, version := int64(0x58), uint16(3)
		if string(header[:4]) == "\x7fLIH" {
			offset, version = 0x30, 1
		}
		var pointer [8]byte
		if _, err := file.ReadAt(pointer[:], offset); err != nil || binary.LittleEndian.Uint16(header[6:]) != version {
			return nil, ErrNotFound
		}
		base = int64(binary.LittleEndian.Uint64(pointer[:]))
		if base < 0xa0 || base > size-32 {
			return nil, ErrNotFound
		}
		if _, err := file.ReadAt(header[:], base); err != nil {
			return nil, ErrNotFound
		}
	}
	if string(header[:4]) != "\x7fCNT" {
		return nil, ErrNotFound
	}
	count := int64(binary.BigEndian.Uint32(header[16:]))
	table := int64(binary.BigEndian.Uint32(header[24:]))
	if count > 1<<16 || table+count*32 > size-base {
		return nil, ErrNotFound
	}
	entries := bufio.NewReader(io.NewSectionReader(file, base+table, count*32))
	var entry [32]byte
	for i := int64(0); i < count; i++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if _, err := io.ReadFull(entries, entry[:]); err != nil {
			return nil, err
		}
		if binary.BigEndian.Uint32(entry[:]) != id {
			continue
		}
		offset := int64(binary.BigEndian.Uint32(entry[16:]))
		length := int64(binary.BigEndian.Uint32(entry[20:]))
		if length == 0 || length > limit || offset+length > size-base {
			return nil, fmt.Errorf("invalid package entry %#x", id)
		}
		return io.NewSectionReader(file, base+offset, length), nil
	}
	return nil, ErrNotFound
}
