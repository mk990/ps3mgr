package ftp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"ps3mgr/internal/domain"
	"ps3mgr/internal/games"
)

type Service struct {
	User       string
	Password   string
	Timeout    time.Duration
	RemoteRoot string
}

type Progress struct {
	File  string
	Delta int64
}

func (s *Service) connect(ctx context.Context, ip string) (*Client, error) {
	return Dial(ctx, ip, s.User, s.Password, s.Timeout)
}

func (s *Service) Detect(ctx context.Context, ip string) (bool, int, error) {
	client, err := s.connect(ctx, ip)
	if err != nil {
		return false, 0, err
	}
	defer client.Close()
	roots, err := client.Names(ctx, "/")
	if err != nil {
		return false, 0, fmt.Errorf("inspect FTP root: %w", err)
	}
	ps3 := looksLikePS3(roots)
	if !ps3 {
		return false, 0, nil
	}
	names, err := client.Names(ctx, s.RemoteRoot)
	if err != nil {
		return true, 0, nil
	}
	return true, len(names), nil
}

func (s *Service) RemoteGames(ctx context.Context, ip, remoteRoot string) ([]domain.Game, error) {
	client, err := s.connect(ctx, ip)
	if err != nil {
		return nil, err
	}
	defer client.Close()
	names, err := client.Names(ctx, remoteRoot)
	if err != nil {
		return nil, fmt.Errorf("list remote games: %w", err)
	}
	result := make([]domain.Game, 0, len(names))
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		game := domain.Game{Title: name, RemotePath: path.Join(remoteRoot, name), Installed: true, State: domain.StateInstalled}
		data, readErr := client.ReadFile(ctx, path.Join(game.RemotePath, "PS3_GAME/PARAM.SFO"), 2<<20)
		if readErr == nil {
			if metadata, parseErr := games.ParseSFO(data); parseErr == nil {
				if metadata["TITLE"] != "" {
					game.Title = metadata["TITLE"]
				}
				game.ID = metadata["TITLE_ID"]
				game.Version = metadata["APP_VER"]
			}
		}
		result = append(result, game)
	}
	sort.Slice(result, func(i, j int) bool { return strings.ToLower(result[i].Title) < strings.ToLower(result[j].Title) })
	return result, nil
}

func (s *Service) UploadGame(ctx context.Context, ip string, game domain.Game, remoteRoot string, progress func(Progress)) error {
	if game.LocalPath == "" {
		return fmt.Errorf("game %q has no local path", game.Title)
	}
	info, err := os.Stat(game.LocalPath)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("access local game %q: %w", game.Title, err)
	}
	target := path.Join(remoteRoot, filepath.Base(game.LocalPath))
	return filepath.WalkDir(game.LocalPath, func(localPath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		relative, err := filepath.Rel(game.LocalPath, localPath)
		if err != nil {
			return err
		}
		remotePath := target
		if relative != "." {
			remotePath = path.Join(target, filepath.ToSlash(relative))
		}
		if entry.IsDir() {
			client, err := s.connect(ctx, ip)
			if err != nil {
				return err
			}
			err = client.MakeDirAll(ctx, remotePath)
			client.Close()
			return err
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		var reported int64
		return s.uploadFile(ctx, ip, localPath, remotePath, func(completed int64) {
			if completed <= reported {
				return
			}
			delta := completed - reported
			reported = completed
			if progress != nil {
				progress(Progress{File: filepath.ToSlash(relative), Delta: delta})
			}
		})
	})
}

func (s *Service) uploadFile(ctx context.Context, ip, localPath, remotePath string, progress func(int64)) error {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt) * time.Second):
			}
		}
		client, err := s.connect(ctx, ip)
		if err != nil {
			lastErr = err
			continue
		}
		file, err := os.Open(localPath)
		if err != nil {
			client.Close()
			return err
		}
		info, err := file.Stat()
		if err != nil {
			file.Close()
			client.Close()
			return err
		}
		var offset int64
		if size, sizeErr := client.Size(ctx, remotePath); sizeErr == nil {
			if size == info.Size() {
				file.Close()
				client.Close()
				progress(size)
				return nil
			}
			if size > 0 && size < info.Size() {
				offset = size
				_, _ = file.Seek(offset, io.SeekStart)
				progress(offset)
			}
		}
		var uploaded int64
		err = client.Store(ctx, remotePath, file, offset, func(delta int64) {
			uploaded += delta
			progress(offset + uploaded)
		})
		if errors.Is(err, ErrResumeUnsupported) {
			// Some PS3 FTP servers implement SIZE but not REST. Restart only this file.
			client.Close()
			client, err = s.connect(ctx, ip)
			if err == nil {
				_, _ = file.Seek(0, io.SeekStart)
				uploaded = 0
				err = client.Store(ctx, remotePath, file, 0, func(delta int64) {
					uploaded += delta
					progress(uploaded)
				})
			}
		}
		file.Close()
		if client != nil {
			client.Close()
		}
		if err == nil {
			return nil
		}
		lastErr = err
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
	}
	return fmt.Errorf("upload %s after retries: %w", filepath.Base(localPath), lastErr)
}

func containsFold(values []string, wanted string) bool {
	for _, value := range values {
		if strings.EqualFold(value, wanted) {
			return true
		}
	}
	return false
}

func looksLikePS3(rootNames []string) bool {
	return containsFold(rootNames, "dev_hdd0") || containsFold(rootNames, "dev_flash")
}
