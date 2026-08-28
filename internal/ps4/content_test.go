package ps4

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

func TestContentServerUsesOpaqueURLsAndSupportsRanges(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "Game Part 0.pkg")
	data := []byte("0123456789")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	server := NewContentServer("127.0.0.1:0", "http://192.168.1.20:8081", root)
	server.server = &http.Server{}
	defer server.Close(context.Background())
	urls, cleanup, err := server.Register(Package{Parts: []PackagePart{{Name: filepath.Base(path), Path: path, Size: int64(len(data))}}})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	parsed, _ := url.Parse(urls[0])
	request := httptest.NewRequest(http.MethodGet, parsed.RequestURI(), nil)
	request.Header.Set("Range", "bytes=2-5")
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	response := recorder.Result()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusPartialContent || string(body) != "2345" {
		t.Fatalf("range response: status=%d body=%q", response.StatusCode, body)
	}
	cleanup()
	recorder = httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, parsed.RequestURI(), nil))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("expected token revocation, got %d", recorder.Code)
	}
}

func TestContentServerRejectsFilesOutsideLibrary(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.pkg")
	if err := os.WriteFile(outside, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	server := NewContentServer("127.0.0.1:0", "http://127.0.0.1:8081", root)
	server.server = &http.Server{}
	defer server.Close(context.Background())
	if _, _, err := server.Register(Package{Parts: []PackagePart{{Name: "outside.pkg", Path: outside, Size: 1}}}); err == nil {
		t.Fatal("expected outside-library rejection")
	}
}

func TestAdvertiseURLMustBeReachableByPS4(t *testing.T) {
	for _, value := range []string{"", "http://0.0.0.0:8081", "http://127.0.0.1:8081", "http://localhost:8081", "file:///packages", "http://192.168.1.20:8081/base"} {
		if err := validateAdvertiseURL(value); err == nil {
			t.Errorf("expected %q to be rejected", value)
		}
	}
	if err := validateAdvertiseURL("http://192.168.1.20:8081"); err != nil {
		t.Fatalf("reachable LAN URL rejected: %v", err)
	}
}
