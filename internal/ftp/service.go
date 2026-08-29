package ftp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
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
	User                  string
	Password              string
	Timeout               time.Duration
	RemoteRoot            string
	Port                  string
	DisableSELFDecryption bool
}

type Progress struct {
	File  string
	Key   string
	Delta int64
	Total int64
}

func (s *Service) connect(ctx context.Context, ip string) (*Client, error) {
	endpoint := ip
	if s.Port != "" {
		endpoint = net.JoinHostPort(ip, s.Port)
	}
	client, err := Dial(ctx, endpoint, s.User, s.Password, s.Timeout)
	if err != nil {
		return nil, err
	}
	if s.DisableSELFDecryption {
		if err := client.DisableSELFDecryption(ctx); err != nil {
			client.Close()
			return nil, fmt.Errorf("configure raw FTP transfers: %w", err)
		}
	}
	return client, nil
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

func (s *Service) Names(ctx context.Context, ip, remotePath string) ([]string, error) {
	client, err := s.connect(ctx, ip)
	if err != nil {
		return nil, err
	}
	defer client.Close()
	return client.Names(ctx, remotePath)
}

func (s *Service) UploadGame(ctx context.Context, ip string, game domain.Game, remoteRoot string, progress func(Progress)) error {
	if game.LocalPath == "" {
		return fmt.Errorf("game %q has no local path", game.Title)
	}
	info, err := os.Stat(game.LocalPath)
	if err != nil {
		return fmt.Errorf("access local game %q: %w", game.Title, err)
	}
	target := path.Join(remoteRoot, filepath.Base(game.LocalPath))
	if info.Mode().IsRegular() {
		var reported int64
		return s.uploadFile(ctx, ip, game.LocalPath, target, func(completed int64) {
			if completed <= reported {
				return
			}
			delta := completed - reported
			reported = completed
			if progress != nil {
				progress(Progress{File: filepath.Base(game.LocalPath), Delta: delta})
			}
		})
	}
	if !info.IsDir() {
		return fmt.Errorf("local game %q is neither a regular file nor directory", game.Title)
	}
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

// DownloadGame pulls a remote game into localRoot. Each file is staged as a
// .part file so an interrupted download cannot appear as a complete game.
func (s *Service) DownloadGame(ctx context.Context, ip string, game domain.Game, localRoot string, progress func(Progress)) error {
	if game.RemotePath == "" {
		return fmt.Errorf("game %q has no remote path", game.Title)
	}
	if err := os.MkdirAll(localRoot, 0o755); err != nil {
		return err
	}
	return s.downloadTree(ctx, ip, game.RemotePath, filepath.Join(localRoot, DownloadName(game)), progress)
}

func DownloadName(game domain.Game) string {
	name := strings.TrimSpace(game.Title)
	if game.ID != "" {
		if name == "" {
			name = game.ID
		} else {
			name = game.ID + " - " + name
		}
	}
	name = strings.Map(func(char rune) rune {
		switch {
		case char == '/' || char == '\\' || char == ':' || char == '*' || char == '?' || char == '"' || char == '<' || char == '>' || char == '|':
			return '-'
		case char < 32:
			return -1
		default:
			return char
		}
	}, name)
	name = strings.Trim(name, " .")
	if name == "" {
		name = filepath.Base(path.Clean(game.RemotePath))
	}
	if name == "" || name == "." || name == string(filepath.Separator) {
		return "Game"
	}
	return name
}

func (s *Service) DownloadFile(ctx context.Context, ip, remotePath, localPath string, progress func(Progress)) error {
	return s.downloadFile(ctx, ip, remotePath, localPath, progress)
}

func (s *Service) downloadTree(ctx context.Context, ip, remotePath, localPath string, progress func(Progress)) error {
	client, err := s.connect(ctx, ip)
	if err != nil {
		return err
	}
	names, listErr := client.Names(ctx, remotePath)
	client.Close()
	if listErr != nil {
		return s.downloadFile(ctx, ip, remotePath, localPath, progress)
	}
	if err := os.MkdirAll(localPath, 0o755); err != nil {
		return err
	}
	for _, name := range names {
		if name == "." || name == ".." || filepath.Base(name) != name {
			continue
		}
		if err := s.downloadTree(ctx, ip, path.Join(remotePath, name), filepath.Join(localPath, name), progress); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) downloadFile(ctx context.Context, ip, remotePath, localPath string, progress func(Progress)) error {
	var lastErr error
	var reported int64
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt) * time.Second):
			}
		}
		var attemptCompleted int64
		err := s.downloadFileAttempt(ctx, ip, remotePath, localPath, func(value Progress) {
			attemptCompleted += value.Delta
			value.Delta = 0
			if attemptCompleted > reported {
				value.Delta = attemptCompleted - reported
				reported = attemptCompleted
			}
			if progress != nil && (value.Delta > 0 || value.Total > 0) {
				progress(value)
			}
		})
		if err == nil {
			return nil
		}
		lastErr = err
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
	}
	return fmt.Errorf("download %s after retries: %w", filepath.Base(localPath), lastErr)
}

func (s *Service) downloadFileAttempt(ctx context.Context, ip, remotePath, localPath string, progress func(Progress)) error {
	if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
		return err
	}
	part := localPath + ".part"
	file, err := os.OpenFile(part, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return err
	}
	offset := info.Size()
	expectedSize := int64(-1)
	client, err := s.connect(ctx, ip)
	if err == nil {
		if remoteSize, sizeErr := client.Size(ctx, remotePath); sizeErr == nil {
			expectedSize = remoteSize
			if progress != nil {
				progress(Progress{File: filepath.Base(localPath), Key: remotePath, Total: remoteSize})
			}
			switch {
			case offset == remoteSize && offset > 0:
				if progress != nil {
					progress(Progress{File: filepath.Base(localPath), Key: remotePath, Delta: offset, Total: expectedSize})
				}
				err = nil
				goto completed
			case offset > remoteSize:
				offset = 0
				if truncateErr := file.Truncate(0); truncateErr != nil {
					err = truncateErr
					goto completed
				}
			}
		}
		if _, seekErr := file.Seek(offset, io.SeekStart); seekErr != nil {
			err = seekErr
			goto completed
		}
		reportedOffset := false
		err = client.RetrieveFrom(ctx, remotePath, file, offset, func(delta int64) {
			if progress != nil {
				if !reportedOffset && offset > 0 {
					progress(Progress{File: filepath.Base(localPath), Key: remotePath, Delta: offset, Total: expectedSize})
				}
				progress(Progress{File: filepath.Base(localPath), Key: remotePath, Delta: delta, Total: expectedSize})
			}
			reportedOffset = true
		})
		if err == nil && !reportedOffset && offset > 0 && progress != nil {
			progress(Progress{File: filepath.Base(localPath), Key: remotePath, Delta: offset, Total: expectedSize})
		}
		if errors.Is(err, ErrResumeUnsupported) {
			client.Close()
			client = nil
			offset = 0
			if truncateErr := file.Truncate(0); truncateErr != nil {
				err = truncateErr
				goto completed
			}
			if _, seekErr := file.Seek(0, io.SeekStart); seekErr != nil {
				err = seekErr
				goto completed
			}
			client, err = s.connect(ctx, ip)
			if err == nil {
				err = client.Retrieve(ctx, remotePath, file, func(delta int64) {
					if progress != nil {
						progress(Progress{File: filepath.Base(localPath), Key: remotePath, Delta: delta, Total: expectedSize})
					}
				})
			}
		}
		if err == nil {
			if remoteSize, sizeErr := client.Size(ctx, remotePath); sizeErr == nil {
				expectedSize = remoteSize
			}
		}
	}

completed:
	closeErr := file.Close()
	if client != nil {
		client.Close()
	}
	if err != nil {
		return fmt.Errorf("download %s: %w", remotePath, err)
	}
	if closeErr != nil {
		return closeErr
	}
	if expectedSize >= 0 {
		info, statErr := os.Stat(part)
		if statErr != nil {
			return fmt.Errorf("verify downloaded file %s: %w", remotePath, statErr)
		}
		if err := verifyTransferSize("download", remotePath, info.Size(), expectedSize); err != nil {
			return err
		}
	}
	return os.Rename(part, localPath)
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
		if err == nil {
			if remoteSize, sizeErr := client.Size(ctx, remotePath); sizeErr == nil {
				err = verifyTransferSize("upload", remotePath, remoteSize, info.Size())
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

func verifyTransferSize(operation, remotePath string, actual, expected int64) error {
	if actual != expected {
		return fmt.Errorf("%s integrity check failed for %s: got %d bytes, expected %d", operation, remotePath, actual, expected)
	}
	return nil
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
