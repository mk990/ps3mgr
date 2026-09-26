package ps4

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFPKGiContentListsServeSinglePackagesByCategory(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"Games", "Updates", "Split"} {
		if err := os.Mkdir(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	game := "UP0001-CUSA12345_00-ABCDEFGHIJKLMNOP"
	writePKGFixture(t, filepath.Join(root, "Games", "My Game "+game+".pkg"), game, 0x1a, 0x200)
	writePKGFixture(t, filepath.Join(root, "Updates", "My Game "+game+"-V0105.pkg"), game, 0x1e, 0x200)
	writePKGFixture(t, filepath.Join(root, "Split", "Big_0.pkg"), "EP0002-CUSA54321_00-ABCDEFGHIJKLMNOP", 0x1a, 0x100)
	writePKGFixture(t, filepath.Join(root, "Split", "Big_1.pkg"), "EP0002-CUSA54321_00-ABCDEFGHIJKLMNOP", 0x1a, 0x100)
	service := NewService(root, "", "127.0.0.1:0", "http://192.168.1.20:8081", 0, 1, 0, 0, nil)
	defer service.Queue.Close(context.Background())

	recorder := httptest.NewRecorder()
	service.Content.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/fpkgi/ps4/games.json", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status %d: %s", recorder.Code, recorder.Body)
	}
	var document struct {
		DATA map[string]map[string]any
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	if len(document.DATA) != 1 {
		t.Fatalf("expected only the single-file game, got %s", recorder.Body)
	}
	for link, entry := range document.DATA {
		if link != "http://192.168.1.20:8081/ps4-library/Games/My%20Game%20"+game+".pkg" {
			t.Fatalf("unexpected link %s", link)
		}
		if entry["title_id"] != "CUSA12345" || entry["region"] != "USA" || entry["size"] != float64(0x200) {
			t.Fatalf("unexpected entry %#v", entry)
		}
		// The link must resolve on the same package server.
		download := httptest.NewRecorder()
		service.Content.Handler().ServeHTTP(download, httptest.NewRequest(http.MethodHead, link[len("http://192.168.1.20:8081"):], nil))
		if download.Code != http.StatusOK {
			t.Fatalf("package link not served: %d", download.Code)
		}
	}

	// The panel's export path also resolves on the package server.
	for _, alias := range []string{"/api/ps4/fpkgi/games.json", "/api/ps4/fpkgi/games", "/ps4/fpkgi/games.json"} {
		aliased := httptest.NewRecorder()
		service.Content.Handler().ServeHTTP(aliased, httptest.NewRequest(http.MethodGet, alias, nil))
		if aliased.Code != http.StatusOK || aliased.Body.String() != recorder.Body.String() {
			t.Fatalf("%s: status %d: %s", alias, aliased.Code, aliased.Body)
		}
	}

	// A wrong guess gets the right URLs in the 404 body.
	missing := httptest.NewRecorder()
	service.Content.Handler().ServeHTTP(missing, httptest.NewRequest(http.MethodGet, "/fpkgi/games.json", nil))
	if missing.Code != http.StatusNotFound || !strings.Contains(missing.Body.String(), "/fpkgi/ps4/games.json") {
		t.Fatalf("unhelpful 404: %d %s", missing.Code, missing.Body)
	}

	items, skipped, err := service.FPKGiItems(context.Background(), "updates")
	if err != nil || len(items) != 1 || items[0].Version != "01.05" || skipped != 0 {
		t.Fatalf("updates: items=%+v skipped=%d err=%v", items, skipped, err)
	}
	if _, skipped, _ = service.FPKGiItems(context.Background(), "all"); skipped != 1 {
		t.Fatalf("expected the split package to be skipped once, got %d", skipped)
	}
	if _, _, err = service.FPKGiItems(context.Background(), "themes"); err == nil {
		t.Fatal("expected unknown category error")
	}
}

func TestFPKGiRequiresAdvertiseURL(t *testing.T) {
	service := NewService(t.TempDir(), "", "127.0.0.1:0", "", 0, 1, 0, 0, nil)
	defer service.Queue.Close(context.Background())
	recorder := httptest.NewRecorder()
	service.Content.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/fpkgi/ps4/games.json", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 without an advertise URL, got %d", recorder.Code)
	}
}
