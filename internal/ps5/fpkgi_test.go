package ps5

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestFPKGiListsPS5PackagesWithCovers(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "pkgs"), 0o755); err != nil {
		t.Fatal(err)
	}
	header := make([]byte, 0x200)
	copy(header[0x40:], "EP9000-PPSA01234_00-ABCDEFGHIJKLMNOP")
	files := map[string][]byte{
		"pkgs/Astro Bot v1.02.pkg": header,
		"pkgs/Astro Bot v1.02.png": []byte("png"),
		"Folder Game.ffpkg":        []byte("image"),
		".hidden/secret.pkg":       []byte("x"),
	}
	for name, data := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	service := &Service{GameDir: root}
	base := "http://192.168.1.20:8081"
	handler := service.FPKGiHandler(func() (string, error) { return base, nil })

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/fpkgi/ps5/ps5.json", nil))
	var document struct {
		DATA map[string]map[string]any
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &document); err != nil {
		t.Fatalf("%v: %s", err, recorder.Body)
	}
	link := base + "/fpkgi/ps5/pkg/pkgs/Astro%20Bot%20v1.02.pkg"
	entry, ok := document.DATA[link]
	if len(document.DATA) != 1 || !ok {
		t.Fatalf("unexpected content list: %s", recorder.Body)
	}
	if entry["title_id"] != "PPSA01234" || entry["name"] != "Astro Bot v1.02" || entry["region"] != "EUR" || entry["version"] != "01.02" || entry["size"] != float64(0x200) || entry["cover_url"] != base+"/fpkgi/ps5/covers/pkgs/Astro%20Bot%20v1.02.png" {
		t.Fatalf("unexpected entry: %#v", entry)
	}

	for path, want := range map[string]int{
		"/fpkgi/ps5/pkg/pkgs/Astro%20Bot%20v1.02.pkg":    http.StatusOK,
		"/fpkgi/ps5/covers/pkgs/Astro%20Bot%20v1.02.png": http.StatusOK,
		"/fpkgi/ps5/pkg/Folder%20Game.ffpkg":             http.StatusNotFound,
		"/fpkgi/ps5/pkg/.hidden/secret.pkg":              http.StatusNotFound,
		"/fpkgi/ps5/pkg/..%2F..%2Fetc%2Fpasswd.pkg":      http.StatusNotFound,
	} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != want {
			t.Fatalf("%s: status %d, want %d", path, recorder.Code, want)
		}
	}

	failing := service.FPKGiHandler(func() (string, error) { return "", errors.New("not configured") })
	recorder = httptest.NewRecorder()
	failing.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/fpkgi/ps5/ps5.json", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 without an advertise URL, got %d", recorder.Code)
	}
}
