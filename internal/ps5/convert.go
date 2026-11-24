package ps5

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"ps3mgr/internal/domain"
	"ps3mgr/internal/filelock"
	"ps3mgr/internal/games"
	"ps3mgr/internal/transfers"
)

// ConvertFolder builds an image beside a discovered local game folder.
func (s *Service) ConvertFolder(ctx context.Context, wanted, format string) (string, error) {
	folders, format, err := s.conversionFolders(ctx, []string{wanted}, format)
	if err != nil {
		return "", err
	}
	if err := s.checkConversion(ctx, folders[0], format, make(map[string]bool)); err != nil {
		return "", err
	}
	return s.buildImage(ctx, folders[0], format, nil)
}

// EnqueueConversions resolves the entire selection before submitting any jobs.
func (s *Service) EnqueueConversions(ctx context.Context, wanted []string, format string) ([]domain.Transfer, error) {
	folders, format, err := s.conversionFolders(ctx, wanted, format)
	if err != nil {
		return nil, err
	}
	probed := make(map[string]bool)
	for _, folder := range folders {
		if err := s.checkConversion(ctx, folder, format, probed); err != nil {
			return nil, err
		}
	}
	return s.Conversions.Enqueue(folders, "", transfers.Options{OutputFormat: format})
}

func (s *Service) RestoreConversions() error {
	root, err := filepath.Abs(s.GameDir)
	if err != nil {
		return err
	}
	return s.Conversions.RestoreConversions(filepath.Join(root, ".ps3mgr-conversion-queue.json"), s.recoverConversion)
}

func (s *Service) RetryConversion(ctx context.Context, id string) error {
	item, found := s.Conversions.Get(id)
	if found && (item.State == domain.QueueFailed || item.State == domain.QueueCancelled) {
		if err := s.checkConversion(ctx, item.Game, item.OutputFormat, make(map[string]bool)); err != nil {
			return err
		}
	}
	return s.Conversions.Retry(id)
}

func (s *Service) checkConversion(ctx context.Context, folder domain.Game, format string, probed map[string]bool) error {
	paths, err := s.conversionPaths(folder, format)
	if err != nil {
		return err
	}
	if err := requireNewImage(paths.output); err != nil {
		return err
	}
	if _, err := imageBuilder(ctx, format, paths.source, paths.staged); err != nil {
		return err
	}
	parent := filepath.Dir(paths.output)
	if !probed[parent] {
		probe, err := os.MkdirTemp(parent, ".ps3mgr-conversion-probe-*")
		if err != nil {
			return fmt.Errorf("cannot write conversion output in %s; mount the PS5 library read/write and check directory permissions: %w", parent, err)
		}
		if err := os.Remove(probe); err != nil {
			return fmt.Errorf("remove conversion write probe: %w", err)
		}
		probed[parent] = true
	}
	return nil
}

func (s *Service) conversionFolders(ctx context.Context, wanted []string, format string) ([]domain.Game, string, error) {
	format = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(format), "."))
	switch format {
	case "exfat", "ffpkg", "ffpfsc":
	default:
		return nil, "", fmt.Errorf("choose exfat, ffpkg, or ffpfsc")
	}
	if len(wanted) == 0 {
		return nil, "", fmt.Errorf("select at least one PS5 game folder")
	}
	root, err := filepath.Abs(s.GameDir)
	if err != nil {
		return nil, "", err
	}
	items, err := s.Library.Scan(ctx, root)
	if err != nil {
		return nil, "", err
	}
	selected := make([]domain.Game, 0, len(wanted))
	seen := make(map[string]bool)
	for _, id := range wanted {
		var folder domain.Game
		for _, game := range items {
			if game.Format != "folder" || id == "" {
				continue
			}
			if id != game.LibraryID && id != filepath.Base(game.LocalPath) && !strings.EqualFold(id, games.PublicID(game)) && !strings.EqualFold(id, game.Title) {
				continue
			}
			if folder.LocalPath != "" {
				return nil, "", fmt.Errorf("multiple folders match %q; select a library_id from ps5 local-games --json", id)
			}
			folder = game
		}
		if folder.LocalPath == "" {
			return nil, "", fmt.Errorf("local PS5 folder %q not found", id)
		}
		if !seen[folder.LibraryID] {
			selected = append(selected, folder)
			seen[folder.LibraryID] = true
		}
	}
	return selected, format, nil
}

type conversionPaths struct {
	source string
	output string
	stage  string
	staged string
}

func (s *Service) conversionPaths(folder domain.Game, format string) (conversionPaths, error) {
	var paths conversionPaths
	switch format {
	case "exfat", "ffpkg", "ffpfsc":
	default:
		return paths, fmt.Errorf("invalid PS5 image format %q", format)
	}
	// A queued folder or its parent could have been moved or replaced since discovery.
	root, err := filepath.Abs(s.GameDir)
	if err != nil {
		return paths, err
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return paths, err
	}
	relative, err := filepath.Rel(root, folder.LocalPath)
	if err != nil || !filepath.IsLocal(relative) || relative == "." {
		return paths, fmt.Errorf("invalid PS5 folder location")
	}
	realFolder, err := filepath.EvalSymlinks(folder.LocalPath)
	if err != nil {
		return paths, err
	}
	if realFolder != filepath.Join(realRoot, relative) {
		return paths, fmt.Errorf("PS5 folder location changed after discovery")
	}
	info, err := os.Stat(filepath.Join(realFolder, "sce_sys", "param.json"))
	if err != nil {
		return paths, err
	}
	if !info.Mode().IsRegular() {
		return paths, fmt.Errorf("missing PS5 game metadata")
	}
	output := realFolder + "." + format
	key := sha256.Sum256([]byte(filepath.Base(output)))
	stage := filepath.Join(filepath.Dir(output), fmt.Sprintf(".ps3mgr-conversion-%x", key[:16]))
	return conversionPaths{source: realFolder, output: output, stage: stage, staged: filepath.Join(stage, filepath.Base(output))}, nil
}

func (s *Service) recoverConversion(item domain.Transfer) string {
	paths, err := s.conversionPaths(item.Game, item.OutputFormat)
	if err != nil {
		return ""
	}
	lock, err := filelock.Open(paths.stage + ".lock")
	if err != nil {
		return "" // A builder may still be running after the server exited.
	}
	defer lock.Close()
	_, stagedErr := os.Lstat(paths.staged)
	info, outputErr := os.Lstat(paths.output)
	_ = os.RemoveAll(paths.stage) // If cleanup fails, a retry reports the error.
	if item.ConversionPhase == domain.ConversionFinalizing && errors.Is(stagedErr, fs.ErrNotExist) &&
		outputErr == nil && info.Mode().IsRegular() && info.Size() > 0 {
		return filepath.Base(paths.output)
	}
	return ""
}

func (s *Service) buildImage(ctx context.Context, folder domain.Game, format string, phase func(domain.ConversionPhase) error) (string, error) {
	paths, err := s.conversionPaths(folder, format)
	if err != nil {
		return "", err
	}
	if err := requireNewImage(paths.output); err != nil {
		return "", err
	}
	cmd, err := imageBuilder(ctx, format, paths.source, paths.staged)
	if err != nil {
		return "", err
	}
	lock, err := filelock.Open(paths.stage + ".lock")
	if err != nil {
		return "", fmt.Errorf("cannot lock conversion output; check for a running conversion or a read-only destination: %w", err)
	}
	defer lock.Close()
	if err := requireNewImage(paths.output); err != nil {
		return "", err
	}
	if err := os.RemoveAll(paths.stage); err != nil {
		return "", fmt.Errorf("clean up interrupted conversion: %w", err)
	}
	if err := os.Mkdir(paths.stage, 0o700); err != nil {
		return "", err
	}
	defer os.RemoveAll(paths.stage)
	if err := filepath.WalkDir(paths.source, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && !entry.Type().IsRegular() {
			return fmt.Errorf("cannot include special file or symlink %q in the image", path)
		}
		return ctx.Err()
	}); err != nil {
		return "", err
	}
	if err := filelock.Inherit(cmd, lock); err != nil {
		return "", err
	}
	if phase != nil {
		if err := phase(domain.ConversionBuilding); err != nil {
			return "", err
		}
	}
	cmd.Dir = paths.stage
	var log builderLog
	cmd.Stdout, cmd.Stderr = &log, &log
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", fmt.Errorf("build %s: %w: %s", format, err, strings.TrimSpace(string(log)))
	}
	info, err := os.Lstat(paths.staged)
	if err != nil {
		return "", fmt.Errorf("locate built image: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return "", fmt.Errorf("builder produced an empty or invalid %s image", format)
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := requireNewImage(paths.output); err != nil {
		return "", err
	}
	if phase != nil {
		if err := phase(domain.ConversionFinalizing); err != nil {
			return "", err
		}
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := os.Rename(paths.staged, paths.output); err != nil {
		return "", err
	}
	s.mu.Lock()
	s.local = nil
	s.mu.Unlock()
	return filepath.Base(paths.output), nil
}

func requireNewImage(path string) error {
	_, err := os.Lstat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err == nil:
		return fmt.Errorf("image already exists: %s", filepath.Base(path))
	default:
		return err
	}
}

func imageBuilder(ctx context.Context, format, source, output string) (*exec.Cmd, error) {
	if format == "ffpkg" {
		if tool, err := exec.LookPath("makefs"); err == nil {
			// BSD newfs expresses UFS disk addresses in 512-byte units,
			// including on 4K-sector devices. makefs uses -S for that unit.
			return exec.CommandContext(ctx, tool, "-t", "ffs", "-B", "little", "-S", "512", "-M", "16m", "-b", "2%", "-o", "version=2,bsize=65536,fsize=65536,minfree=0", output, source), nil
		}
		if tool, err := exec.LookPath("ufs2tool"); err == nil {
			return exec.CommandContext(ctx, tool, "newfs", "-D", source, output), nil
		}
		return nil, fmt.Errorf("FFPKG conversion requires makefs or UFS2Tool on PATH")
	}
	tool, err := exec.LookPath("mkpfs")
	if err != nil {
		return nil, fmt.Errorf("%s conversion requires MkPFS 1.1.0 or later on PATH", format)
	}
	if format == "exfat" {
		return exec.CommandContext(ctx, tool, "pack", "exfat", "--cluster-size", "65536", "--no-progress", source, output), nil
	}
	return exec.CommandContext(ctx, tool, "pack", "folder", "--version", "PS5", "--inode-bits", "32", "--verify", "--no-ampr-index", source, output), nil
}

// Keep the final diagnostic without buffering progress for a multi-hour build.
type builderLog []byte

func (l *builderLog) Write(p []byte) (int, error) {
	const limit = 4096
	n := len(p)
	if n >= limit {
		*l = append((*l)[:0], p[n-limit:]...)
	} else {
		start := max(0, len(*l)+n-limit)
		*l = append((*l)[start:], p...)
	}
	return n, nil
}
