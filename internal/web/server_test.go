package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ps3mgr/internal/app"
	"ps3mgr/internal/config"
)

type webDetector struct{}

func (webDetector) Detect(context.Context, string) (bool, int, error) { return true, 4, nil }

func TestHealthLibraryAndValidation(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "Game"), 0o755); err != nil {
		t.Fatal(err)
	}
	application := app.New(config.Config{GameDir: root, RemoteGameDir: "/dev_hdd0/GAMES", FTPUser: "anonymous", FTPTimeout: time.Second, Workers: 1})
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = application.Close(ctx)
	}()
	handler := New(application).Handler()
	application.Scanner.Detector = webDetector{}
	for _, path := range []string{"/api/health", "/api/local-games", "/"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusOK {
			t.Fatalf("GET %s = %d", path, response.Code)
		}
	}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/scan", strings.NewReader(`{"cidr":"8.8.8.0/24"}`))
	request.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("public scan status = %d", response.Code)
	}
	response = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost, "/api/queue", strings.NewReader(`{"console_id":"not-an-ip","game_ids":["game"]}`))
	request.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid queue status = %d", response.Code)
	}
	response = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost, "/api/consoles", strings.NewReader(`{"ip":"127.0.0.1"}`))
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("direct console status = %d body=%s", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost, "/api/consoles/127.0.0.2/rescan", nil)
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("unknown rescan status = %d", response.Code)
	}
}
