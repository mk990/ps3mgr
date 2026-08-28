package ps2

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

type coverDoer func(*http.Request) (*http.Response, error)

func (do coverDoer) Do(request *http.Request) (*http.Response, error) { return do(request) }

func TestCoverCacheDownloadsMissingOnceAndReusesStaticFile(t *testing.T) {
	var requests atomic.Int32
	client := coverDoer(func(r *http.Request) (*http.Response, error) {
		requests.Add(1)
		if r.URL.Path != "/SCES-51719.jpg" {
			t.Errorf("request path = %q", r.URL.Path)
		}
		data := []byte{0xff, 0xd8, 0xff, 0xe0, 0x00, 0x10, 'J', 'F', 'I', 'F', 0x00}
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: io.NopCloser(bytes.NewReader(data)), Header: make(http.Header)}, nil
	})

	root := t.TempDir()
	cache := &CoverCache{Client: client, BaseURL: "https://covers.test", Workers: 2, MaxBytes: 1024}
	games := []Game{
		{ID: "SCES-51719", PublicID: "game-one"},
		{ID: "unknown", PublicID: "unknown-game"},
	}
	downloaded, failures := cache.Populate(context.Background(), root, games)
	if downloaded != 1 || len(failures) != 0 {
		t.Fatalf("downloaded=%d failures=%#v", downloaded, failures)
	}
	if requests.Load() != 1 {
		t.Fatalf("requests = %d, want 1", requests.Load())
	}
	if games[0].CoverPath != filepath.Join(root, "covers", "SCES-51719.jpg") || games[0].CoverURL != "/api/ps2/games/game-one/cover" {
		t.Fatalf("cached game = %#v", games[0])
	}
	if _, err := os.Stat(games[0].CoverPath); err != nil {
		t.Fatal(err)
	}
	if games[1].CoverPath != "" {
		t.Fatalf("unknown game unexpectedly has cover %q", games[1].CoverPath)
	}

	freshScan := []Game{{ID: "SCES-51719", PublicID: "game-one"}}
	downloaded, failures = cache.Populate(context.Background(), root, freshScan)
	if downloaded != 0 || len(failures) != 0 || requests.Load() != 1 {
		t.Fatalf("cache reuse downloaded=%d failures=%#v requests=%d", downloaded, failures, requests.Load())
	}
	if freshScan[0].CoverPath == "" || !strings.HasPrefix(freshScan[0].CoverURL, "/api/") {
		t.Fatalf("fresh scan did not reuse static cache: %#v", freshScan[0])
	}
}

func TestCoverCacheRejectsNonImageResponse(t *testing.T) {
	client := coverDoer(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: io.NopCloser(strings.NewReader("not an image")), Header: make(http.Header)}, nil
	})

	root := t.TempDir()
	cache := &CoverCache{Client: client, BaseURL: "https://covers.test", Workers: 1, MaxBytes: 1024}
	games := []Game{{ID: "SLUS-20312", PublicID: "game"}}
	downloaded, failures := cache.Populate(context.Background(), root, games)
	if downloaded != 0 || len(failures) != 1 || games[0].CoverPath != "" {
		t.Fatalf("downloaded=%d failures=%#v game=%#v", downloaded, failures, games[0])
	}
	entries, err := os.ReadDir(filepath.Join(root, "covers"))
	if err == nil && len(entries) != 0 {
		t.Fatalf("invalid response left cache files: %#v", entries)
	}
}
