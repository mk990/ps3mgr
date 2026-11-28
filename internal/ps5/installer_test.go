package ps5

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"ps3mgr/internal/domain"
	"ps3mgr/internal/ftp"
)

func TestPackageUploadUsesDPIv2(t *testing.T) {
	root := t.TempDir()
	const name = "Package #1.PKG"
	data := bytes.Repeat([]byte("package bytes"), 8000)
	if err := os.WriteFile(filepath.Join(root, name), data, 0o600); err != nil {
		t.Fatal(err)
	}
	games, err := (Library{}).Scan(context.Background(), root)
	if err != nil || len(games) != 1 {
		t.Fatalf("scan = %+v, %v", games, err)
	}
	for _, tc := range []struct {
		name    string
		status  int
		reply   string
		wantErr bool
	}{
		{"accepted", 200, "SUCCESS: PKG installation started from file: " + name, false},
		{"rejected", 200, "FAILED: Install failed with error 123", true},
		{"unavailable", 503, "Installer unavailable", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/upload" || r.ContentLength <= int64(len(data)) {
					t.Errorf("unexpected request: %s %s length %d", r.Method, r.URL.Path, r.ContentLength)
				}
				file, header, err := r.FormFile("file")
				if err != nil {
					t.Error(err)
					return
				}
				defer file.Close()
				got, err := io.ReadAll(file)
				if err != nil || header.Filename != name || !bytes.Equal(got, data) {
					t.Errorf("unexpected upload: %q, %d bytes, %v", header.Filename, len(got), err)
				}
				w.WriteHeader(tc.status)
				io.WriteString(w, tc.reply)
			}))
			defer server.Close()
			host, port, err := net.SplitHostPort(server.Listener.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			var sent atomic.Int64
			err = (consoleUploader{dpiPort: port}).UploadGame(context.Background(), host, games[0], "/games", func(p ftp.Progress) {
				sent.Add(p.Delta)
			})
			if (err != nil) != tc.wantErr || (err != nil && !strings.Contains(err.Error(), tc.reply)) {
				t.Fatalf("installer error = %v", err)
			}
			if sent.Load() != int64(len(data)) {
				t.Fatalf("reported %d package bytes", sent.Load())
			}
		})
	}
}

func TestCancelledPackageUpload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "game.pkg")
	if err := os.WriteFile(path, []byte("package"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := (consoleUploader{dpiPort: "12800"}).UploadGame(ctx, "127.0.0.1", domain.Game{LocalPath: path, Format: "pkg"}, "/games", nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled upload = %v", err)
	}
}
