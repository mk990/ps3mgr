package ps2

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const defaultCoverBaseURL = "https://raw.githubusercontent.com/xlenore/ps2-covers/main/covers/default"

const defaultCoverMaxBytes int64 = 10 << 20

type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

type CoverCache struct {
	Client   HTTPDoer
	BaseURL  string
	Workers  int
	MaxBytes int64
}

type CoverFailure struct {
	GameID string `json:"game_id"`
	Reason string `json:"reason"`
}

type CoverStatus struct {
	Enabled   bool   `json:"enabled"`
	GameDir   string `json:"game_dir"`
	CacheDir  string `json:"cache_dir"`
	Available bool   `json:"available"`
	Writable  bool   `json:"writable"`
	Images    int    `json:"images"`
	Error     string `json:"error,omitempty"`
}

type coverJob struct {
	index int
	game  Game
}

type coverResult struct {
	index int
	path  string
	err   error
}

func NewCoverCache() *CoverCache {
	client := &http.Client{
		Timeout: 8 * time.Second,
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if len(via) > 0 && request.URL.Host != via[0].URL.Host {
				return fmt.Errorf("cover redirect to a different host is not allowed")
			}
			if len(via) >= 3 {
				return fmt.Errorf("too many cover redirects")
			}
			return nil
		},
	}
	return &CoverCache{Client: client, BaseURL: defaultCoverBaseURL, Workers: 6, MaxBytes: defaultCoverMaxBytes}
}

// Ensure prepares and verifies the cache below the configured PS2 library.
// The probe catches read-only or incorrectly owned Docker bind mounts before
// the first download, while leaving no diagnostic file behind.
func (c *CoverCache) Ensure(root string) (string, error) {
	cacheRoot := filepath.Join(root, "covers")
	info, err := os.Stat(root)
	if err != nil {
		return cacheRoot, fmt.Errorf("access PS2 game directory %q: %w", root, err)
	}
	if !info.IsDir() {
		return cacheRoot, fmt.Errorf("PS2 game path is not a directory: %s", root)
	}
	if err := os.MkdirAll(cacheRoot, 0o755); err != nil {
		return cacheRoot, fmt.Errorf("create cover cache %q: %w", cacheRoot, err)
	}
	probe, err := os.CreateTemp(cacheRoot, ".ps3mgr-write-test-*")
	if err != nil {
		return cacheRoot, fmt.Errorf("cover cache is not writable %q: %w", cacheRoot, err)
	}
	probePath := probe.Name()
	if closeErr := probe.Close(); closeErr != nil {
		_ = os.Remove(probePath)
		return cacheRoot, fmt.Errorf("verify cover cache %q: %w", cacheRoot, closeErr)
	}
	if err = os.Remove(probePath); err != nil {
		return cacheRoot, fmt.Errorf("clean cover cache probe %q: %w", probePath, err)
	}
	return cacheRoot, nil
}

func (c *CoverCache) Status(root string) CoverStatus {
	status := CoverStatus{Enabled: c != nil, GameDir: root, CacheDir: filepath.Join(root, "covers")}
	if c == nil {
		return status
	}
	cacheRoot, err := c.Ensure(root)
	status.CacheDir = cacheRoot
	if err != nil {
		status.Error = err.Error()
		return status
	}
	status.Available, status.Writable = true, true
	status.Images = len(indexCovers(cacheRoot))
	return status
}

// Populate downloads only covers that are missing from the local cache. The
// returned games always reference local files and same-origin API URLs.
func (c *CoverCache) Populate(ctx context.Context, root string, games []Game) (int, []CoverFailure) {
	if c == nil {
		return 0, nil
	}
	cacheRoot, err := c.Ensure(root)
	if err != nil {
		return 0, []CoverFailure{{Reason: err.Error()}}
	}
	if len(games) == 0 {
		return 0, nil
	}
	existing := indexCovers(cacheRoot)
	jobs := make([]coverJob, 0, len(games))
	for i := range games {
		if games[i].CoverPath != "" || games[i].ID == "unknown" {
			continue
		}
		if path := existing[coverKey(games[i].ID)]; path != "" {
			setGameCover(&games[i], path)
			continue
		}
		jobs = append(jobs, coverJob{index: i, game: games[i]})
	}
	if len(jobs) == 0 {
		return 0, nil
	}

	workers := c.Workers
	if workers < 1 {
		workers = 1
	}
	if workers > len(jobs) {
		workers = len(jobs)
	}
	jobChannel := make(chan coverJob)
	results := make(chan coverResult, len(jobs))
	for range workers {
		go func() {
			for job := range jobChannel {
				path, err := c.download(ctx, cacheRoot, job.game.ID)
				results <- coverResult{index: job.index, path: path, err: err}
			}
		}()
	}
	go func() {
		defer close(jobChannel)
		for _, job := range jobs {
			select {
			case jobChannel <- job:
			case <-ctx.Done():
				return
			}
		}
	}()

	downloaded := 0
	failures := make([]CoverFailure, 0)
	for range jobs {
		select {
		case result := <-results:
			if result.err != nil {
				failures = append(failures, CoverFailure{GameID: games[result.index].ID, Reason: result.err.Error()})
				continue
			}
			setGameCover(&games[result.index], result.path)
			downloaded++
		case <-ctx.Done():
			return downloaded, append(failures, CoverFailure{Reason: ctx.Err().Error()})
		}
	}
	return downloaded, failures
}

func (c *CoverCache) download(ctx context.Context, cacheRoot, gameID string) (string, error) {
	client := c.Client
	if client == nil {
		return "", fmt.Errorf("cover HTTP client is unavailable")
	}
	baseURL := strings.TrimRight(c.BaseURL, "/")
	if baseURL == "" {
		baseURL = defaultCoverBaseURL
	}
	requestURL := baseURL + "/" + url.PathEscape(gameID) + ".jpg"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("Accept", "image/jpeg,image/png")
	request.Header.Set("User-Agent", "ps3mgr-cover-cache")
	response, err := client.Do(request)
	if err != nil {
		return "", fmt.Errorf("download cover: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return "", fmt.Errorf("cover source returned %s", response.Status)
	}
	limit := c.MaxBytes
	if limit <= 0 {
		limit = defaultCoverMaxBytes
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return "", fmt.Errorf("read cover: %w", err)
	}
	if int64(len(data)) > limit {
		return "", fmt.Errorf("cover exceeds %d bytes", limit)
	}
	extension := ""
	switch http.DetectContentType(data) {
	case "image/jpeg":
		extension = ".jpg"
	case "image/png":
		extension = ".png"
	default:
		return "", fmt.Errorf("cover response is not a JPEG or PNG image")
	}
	destination := filepath.Join(cacheRoot, gameID+extension)
	temporary, err := os.CreateTemp(cacheRoot, "."+gameID+"-*.partial")
	if err != nil {
		return "", fmt.Errorf("create temporary cover: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err = temporary.Chmod(0o644); err == nil {
		_, err = temporary.Write(data)
	}
	if err == nil {
		err = temporary.Sync()
	}
	closeErr := temporary.Close()
	if err != nil {
		return "", fmt.Errorf("write cover cache: %w", err)
	}
	if closeErr != nil {
		return "", fmt.Errorf("close cover cache: %w", closeErr)
	}
	if err = os.Rename(temporaryPath, destination); err != nil {
		return "", fmt.Errorf("commit cover cache: %w", err)
	}
	return destination, nil
}

func setGameCover(game *Game, path string) {
	game.CoverPath = path
	game.CoverURL = "/api/ps2/games/" + game.PublicID + "/cover"
}
