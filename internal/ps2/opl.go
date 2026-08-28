package ps2

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	ulPartSize      = int64(1 << 30)
	directCopyLimit = int64(4<<30) - 1
	copyBufferSize  = 512 << 10
)

type ProgressFunc func(Progress)

type OPLConverter interface {
	Convert(context.Context, Game, string, ProgressFunc) (OPLResult, error)
}

type OPLFilesystem interface {
	Prepare(context.Context, USBTarget) error
	InstallGame(context.Context, Game, USBTarget, ProgressFunc) (OPLResult, error)
}

type Filesystem struct {
	SystemDir       string
	DirectCopyLimit int64
	PartSize        int64
	mu              sync.Mutex
}

func (f *Filesystem) Convert(ctx context.Context, input Game, destination string, progress ProgressFunc) (OPLResult, error) {
	return f.InstallGame(ctx, input, USBTarget{ID: filepath.Base(destination), MountPath: destination, Available: true}, progress)
}

func (f *Filesystem) Prepare(ctx context.Context, target USBTarget) error {
	info, err := os.Stat(f.SystemDir)
	if err != nil {
		return fmt.Errorf("PS2 system directory %q: %w", f.SystemDir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("PS2 system directory is not a directory: %s", f.SystemDir)
	}
	entries, err := os.ReadDir(f.SystemDir)
	if err != nil {
		return fmt.Errorf("read PS2 system directory: %w", err)
	}
	if len(entries) == 0 {
		return fmt.Errorf("PS2 system directory contains no required files: %s", f.SystemDir)
	}
	for _, name := range []string{"DVD", "CD", "ART", "CFG", "VMC", "THM", "APPS"} {
		if err := os.MkdirAll(filepath.Join(target.MountPath, name), 0755); err != nil {
			return fmt.Errorf("prepare OPL directory %s: %w", name, err)
		}
	}
	systemDestination := filepath.Join(target.MountPath, "system")
	if err := os.MkdirAll(systemDestination, 0755); err != nil {
		return fmt.Errorf("prepare OPL system directory: %w", err)
	}
	return copyTree(ctx, f.SystemDir, systemDestination)
}

func (f *Filesystem) RequiredBytes() (int64, error) {
	var total int64
	err := filepath.WalkDir(f.SystemDir, func(_ string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("calculate PS2 system directory size %q: %w", f.SystemDir, err)
	}
	return total, nil
}

func (f *Filesystem) InstallGame(ctx context.Context, game Game, target USBTarget, progress ProgressFunc) (OPLResult, error) {
	if game.ID == "" || game.ID == "unknown" {
		return OPLResult{}, fmt.Errorf("cannot prepare OPL game %q: game ID is unknown", game.Title)
	}
	if err := f.Prepare(ctx, target); err != nil {
		return OPLResult{}, fmt.Errorf("unable to prepare OPL filesystem %q: %w", target.MountPath, err)
	}
	if f.useDirectCopy(game.Size) {
		return f.directCopy(ctx, game, target, progress)
	}
	return f.usbExtreme(ctx, game, target, progress)
}

func (f *Filesystem) directCopy(ctx context.Context, game Game, target USBTarget, progress ProgressFunc) (OPLResult, error) {
	name := safeTitle(game.Title)
	if len(name) > 32 {
		name = name[:32]
	}
	destination := filepath.Join(target.MountPath, "DVD", oplGameID(game.ID)+"."+name+".iso")
	if info, err := os.Stat(destination); err == nil {
		if info.Size() == game.Size {
			return OPLResult{Strategy: "direct-copy", Root: target.MountPath, Files: []string{destination}, ExpectedSizes: map[string]int64{destination: game.Size}, Bytes: game.Size}, nil
		}
		return OPLResult{}, fmt.Errorf("OPL destination already exists with a different size: %s", destination)
	} else if !os.IsNotExist(err) {
		return OPLResult{}, err
	}
	if err := copyWithProgress(ctx, game.ISOPath, destination, game.Size, StateWriting, progress); err != nil {
		return OPLResult{}, err
	}
	return OPLResult{Strategy: "direct-copy", Root: target.MountPath, Files: []string{destination}, ExpectedSizes: map[string]int64{destination: game.Size}, Bytes: game.Size}, nil
}

func (f *Filesystem) usbExtreme(ctx context.Context, game Game, target USBTarget, progress ProgressFunc) (OPLResult, error) {
	title := safeTitle(game.Title)
	if len(title) > 32 {
		title = title[:32]
	}
	serial := oplGameID(game.ID)
	configPath := filepath.Join(target.MountPath, "ul.cfg")
	if data, err := os.ReadFile(configPath); err == nil && strings.Contains(string(data), "ul."+serial) {
		return OPLResult{}, fmt.Errorf("PS2 game %s is already installed in ul.cfg", game.ID)
	} else if err != nil && !os.IsNotExist(err) {
		return OPLResult{}, fmt.Errorf("read ul.cfg: %w", err)
	}
	partSize := f.partSize()
	parts := int((game.Size + partSize - 1) / partSize)
	if parts > 255 {
		return OPLResult{}, fmt.Errorf("OPL image requires too many parts: %d", parts)
	}
	source, err := os.Open(game.ISOPath)
	if err != nil {
		return OPLResult{}, fmt.Errorf("open PS2 ISO: %w", err)
	}
	defer source.Close()
	prefix := fmt.Sprintf("ul.%08X.%s", oplCRC(title), serial)
	files := make([]string, 0, parts)
	created := make([]string, 0, parts)
	expected := make(map[string]int64, parts)
	var written int64
	started := time.Now()
	cleanup := func() {
		for _, path := range created {
			_ = os.Remove(path)
		}
		for _, path := range files {
			_ = os.Remove(path + ".partial")
		}
	}
	for part := 0; part < parts; part++ {
		final := filepath.Join(target.MountPath, fmt.Sprintf("%s.%02d", prefix, part))
		temp := final + ".partial"
		if _, statErr := os.Stat(final); statErr == nil {
			cleanup()
			return OPLResult{}, fmt.Errorf("OPL part already exists: %s", final)
		} else if !os.IsNotExist(statErr) {
			cleanup()
			return OPLResult{}, statErr
		}
		files = append(files, final)
		out, err := os.OpenFile(temp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0644)
		if err != nil {
			cleanup()
			return OPLResult{}, fmt.Errorf("create OPL part %d: %w", part, err)
		}
		remaining := min64(partSize, game.Size-written)
		expected[final] = remaining
		buffer := make([]byte, copyBufferSize)
		for remaining > 0 {
			select {
			case <-ctx.Done():
				out.Close()
				cleanup()
				return OPLResult{}, ctx.Err()
			default:
			}
			amount := int64(len(buffer))
			if amount > remaining {
				amount = remaining
			}
			n, readErr := io.ReadFull(source, buffer[:amount])
			if readErr != nil && !errors.Is(readErr, io.ErrUnexpectedEOF) {
				out.Close()
				cleanup()
				return OPLResult{}, fmt.Errorf("read ISO: %w", readErr)
			}
			if n == 0 {
				out.Close()
				cleanup()
				return OPLResult{}, io.ErrUnexpectedEOF
			}
			if _, err = out.Write(buffer[:n]); err != nil {
				out.Close()
				cleanup()
				return OPLResult{}, fmt.Errorf("write OPL part: %w", err)
			}
			written += int64(n)
			remaining -= int64(n)
			emitProgress(progress, StateConverting, filepath.Base(final), written, game.Size, started)
			emitProgress(progress, StateWriting, filepath.Base(final), written, game.Size, started)
		}
		if err = out.Sync(); err == nil {
			err = out.Close()
		} else {
			_ = out.Close()
		}
		if err != nil {
			cleanup()
			return OPLResult{}, fmt.Errorf("flush OPL part: %w", err)
		}
		if err = os.Rename(temp, final); err != nil {
			cleanup()
			return OPLResult{}, fmt.Errorf("commit OPL part: %w", err)
		}
		created = append(created, final)
	}
	f.mu.Lock()
	err = f.upsertULRecord(configPath, title, serial, parts)
	f.mu.Unlock()
	if err != nil {
		cleanup()
		return OPLResult{}, err
	}
	return OPLResult{Strategy: "usb-extreme", Root: target.MountPath, Files: files, ExpectedSizes: expected, ConfigFile: configPath, Bytes: written}, nil
}

func (f *Filesystem) upsertULRecord(path, title, serial string, parts int) error {
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read ul.cfg: %w", err)
	}
	if len(data)%64 != 0 {
		return fmt.Errorf("invalid ul.cfg size: %d", len(data))
	}
	image := "ul." + serial
	filtered := make([]byte, 0, len(data)+64)
	for offset := 0; offset < len(data); offset += 64 {
		record := data[offset : offset+64]
		oldName := strings.TrimRight(string(record[:32]), "\x00")
		oldImage := strings.TrimRight(string(record[32:47]), "\x00")
		if oldName == title || oldImage == image {
			continue
		}
		filtered = append(filtered, record...)
	}
	record := make([]byte, 64)
	copy(record[:32], title)
	copy(record[32:47], image)
	record[47] = byte(parts)
	record[48] = 0x14
	record[53] = 0x08
	filtered = append(filtered, record...)
	temp := path + ".partial"
	if err = os.WriteFile(temp, filtered, 0644); err != nil {
		return fmt.Errorf("write ul.cfg: %w", err)
	}
	if err = os.Rename(temp, path); err != nil {
		return fmt.Errorf("commit ul.cfg: %w", err)
	}
	return nil
}

func (f *Filesystem) Verify(_ context.Context, result OPLResult) error {
	if info, err := os.Stat(filepath.Join(result.Root, "DVD")); err != nil || !info.IsDir() {
		return fmt.Errorf("verify OPL layout: DVD directory is missing")
	}
	for _, path := range result.Files {
		info, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("verify OPL file %q: %w", path, err)
		}
		if expected := result.ExpectedSizes[path]; expected > 0 && info.Size() != expected {
			return fmt.Errorf("verify OPL file %q: expected %d bytes, found %d", path, expected, info.Size())
		}
	}
	if result.ConfigFile != "" {
		data, err := os.ReadFile(result.ConfigFile)
		if err != nil {
			return fmt.Errorf("verify ul.cfg: %w", err)
		}
		if len(data) == 0 || len(data)%64 != 0 {
			return fmt.Errorf("verify ul.cfg: invalid size %d", len(data))
		}
	}
	return nil
}

func copyWithProgress(ctx context.Context, source, destination string, total int64, stage State, progress ProgressFunc) error {
	in, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("open %q: %w", source, err)
	}
	defer in.Close()
	temp := destination + ".partial"
	out, err := os.OpenFile(temp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("create %q: %w", destination, err)
	}
	buffer := make([]byte, copyBufferSize)
	var written int64
	started := time.Now()
	for {
		select {
		case <-ctx.Done():
			out.Close()
			_ = os.Remove(temp)
			return ctx.Err()
		default:
		}
		n, readErr := in.Read(buffer)
		if n > 0 {
			if _, err = out.Write(buffer[:n]); err != nil {
				out.Close()
				_ = os.Remove(temp)
				return err
			}
			written += int64(n)
			emitProgress(progress, stage, filepath.Base(destination), written, total, started)
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			out.Close()
			_ = os.Remove(temp)
			return readErr
		}
	}
	if err = out.Sync(); err == nil {
		err = out.Close()
	} else {
		_ = out.Close()
	}
	if err != nil {
		_ = os.Remove(temp)
		return err
	}
	if err = os.Rename(temp, destination); err != nil {
		_ = os.Remove(temp)
		return err
	}
	return nil
}

func emitProgress(fn ProgressFunc, stage State, file string, bytes, total int64, started time.Time) {
	if fn == nil {
		return
	}
	elapsed := time.Since(started).Seconds()
	speed := int64(0)
	if elapsed > 0 {
		speed = int64(float64(bytes) / elapsed)
	}
	p := Progress{Stage: stage, CurrentFile: file, Bytes: bytes, Total: total, Speed: speed}
	if total > 0 {
		p.Percentage = float64(bytes) * 100 / float64(total)
		if speed > 0 {
			p.ETASeconds = (total - bytes) / speed
		}
	}
	fn(p)
}

func copyTree(ctx context.Context, source, destination string) error {
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if relative == "." {
			return nil
		}
		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0755)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		return copyWithProgress(ctx, path, target, info.Size(), StatePreparing, nil)
	})
}

var unsafeTitle = regexp.MustCompile(`[\\/:*?"<>|\x00-\x1f]+`)

func safeTitle(value string) string {
	value = strings.TrimSpace(unsafeTitle.ReplaceAllString(value, "_"))
	var ascii strings.Builder
	for _, char := range value {
		if char >= 32 && char <= 126 {
			ascii.WriteRune(char)
		} else {
			ascii.WriteByte('_')
		}
	}
	value = ascii.String()
	value = strings.Trim(value, ". ")
	if value == "" {
		return "Unknown Game"
	}
	return value
}
func oplGameID(id string) string {
	if match := gameIDPattern.FindStringSubmatch(id); match != nil {
		return strings.ToUpper(match[1]) + "_" + match[2] + "." + match[3]
	}
	return strings.ReplaceAll(id, "-", "_")
}
func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func (f *Filesystem) useDirectCopy(size int64) bool {
	limit := f.DirectCopyLimit
	if limit <= 0 {
		limit = directCopyLimit
	}
	return size <= limit
}
func (f *Filesystem) partSize() int64 {
	if f.PartSize > 0 {
		return f.PartSize
	}
	return ulPartSize
}

// oplCRC reproduces iso2opl's non-reflected polynomial table and includes the trailing NUL byte.
func oplCRC(value string) uint32 {
	var table [256]uint32
	var crc uint32
	for i := 0; i < 256; i++ {
		crc = uint32(i) << 24
		for bit := 0; bit < 8; bit++ {
			if crc&0x80000000 != 0 {
				crc <<= 1
			} else {
				crc = (crc << 1) ^ 0x04C11DB7
			}
		}
		table[255-i] = crc
	}
	for _, b := range append([]byte(value), 0) {
		crc = table[byte(crc>>24)^b] ^ (crc << 8)
	}
	return crc
}
