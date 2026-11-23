package ps5

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"ps3mgr/internal/domain"
	"ps3mgr/internal/games"
	"ps3mgr/internal/transfers"
)

// ConvertFolder builds an image beside a discovered local game folder.
func (s *Service) ConvertFolder(ctx context.Context, wanted, format string) (string, error) {
	folders, format, err := s.conversionFolders(ctx, []string{wanted}, format)
	if err != nil {
		return "", err
	}
	return s.buildImage(ctx, folders[0], format)
}

// EnqueueConversions resolves the entire selection before submitting any jobs.
func (s *Service) EnqueueConversions(ctx context.Context, wanted []string, format string) ([]domain.Transfer, error) {
	folders, format, err := s.conversionFolders(ctx, wanted, format)
	if err != nil {
		return nil, err
	}
	return s.Conversions.Enqueue(folders, "", transfers.Options{OutputFormat: format})
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

func (s *Service) buildImage(ctx context.Context, folder domain.Game, format string) (string, error) {
	// A queued folder or its parent could have been moved or replaced since discovery.
	root, err := filepath.Abs(s.GameDir)
	if err != nil {
		return "", err
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(root, folder.LocalPath)
	if err != nil || !filepath.IsLocal(relative) {
		return "", fmt.Errorf("invalid PS5 folder location")
	}
	realFolder, err := filepath.EvalSymlinks(folder.LocalPath)
	if err != nil {
		return "", err
	}
	if realFolder != filepath.Join(realRoot, relative) {
		return "", fmt.Errorf("PS5 folder location changed after discovery")
	}
	info, err := os.Stat(filepath.Join(realFolder, "sce_sys", "param.json"))
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("missing PS5 game metadata")
	}
	output := folder.LocalPath + "." + format
	if err := requireNewImage(output); err != nil {
		return "", err
	}
	// One staging directory per output also serializes CLI and web writers.
	stage := filepath.Join(filepath.Dir(output), "."+folder.LibraryID+"-"+format)
	if err := os.Mkdir(stage, 0o700); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return "", fmt.Errorf("conversion already in progress or interrupted (staging directory %s)", stage)
		}
		return "", err
	}
	defer os.RemoveAll(stage)
	if err := filepath.WalkDir(folder.LocalPath, func(path string, entry fs.DirEntry, err error) error {
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
	staged := filepath.Join(stage, filepath.Base(output))
	cmd, err := imageBuilder(ctx, format, folder.LocalPath, staged)
	if err != nil {
		return "", err
	}
	cmd.Dir = stage
	var log builderLog
	cmd.Stdout, cmd.Stderr = &log, &log
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", fmt.Errorf("build %s: %w: %s", format, err, strings.TrimSpace(string(log)))
	}
	info, err = os.Lstat(staged)
	if err != nil {
		return "", fmt.Errorf("locate built image: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return "", fmt.Errorf("builder produced an empty or invalid %s image", format)
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := requireNewImage(output); err != nil {
		return "", err
	}
	if err := os.Rename(staged, output); err != nil {
		return "", err
	}
	s.mu.Lock()
	s.local = nil
	s.mu.Unlock()
	return filepath.Base(output), nil
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
