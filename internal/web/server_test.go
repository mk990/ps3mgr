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
	application := app.New(config.Config{PS3GameDir: root, RemoteGameDir: "/dev_hdd0/GAMES", FTPUser: "anonymous", FTPTimeout: time.Second, Workers: 1})
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = application.Close(ctx)
	}()
	handler := New(application).Handler()
	application.Scanner.Detector = webDetector{}
	for _, path := range []string{"/api/health", "/api/local-games", "/", "/ps2-games", "/ps2-usb", "/ps2-queue", "/ps3-games", "/ps3-consoles", "/ps3-scan", "/ps3-queue", "/ps4-games", "/ps4-consoles", "/ps4-scan", "/ps4-queue", "/ps5-games", "/ps5-consoles", "/ps5-scan", "/ps5-queue"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusOK {
			t.Fatalf("GET %s = %d", path, response.Code)
		}
		if csp := response.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "connect-src 'self'") || !strings.Contains(csp, "img-src 'self'") {
			t.Fatalf("GET %s has non-offline content policy %q", path, csp)
		}
	}
	compatibilityResponse := httptest.NewRecorder()
	handler.ServeHTTP(compatibilityResponse, httptest.NewRequest(http.MethodGet, "/ps2-usb", nil))
	if !strings.Contains(compatibilityResponse.Body.String(), "Docker-safe compatibility check") {
		t.Fatal("PS2 USB page is missing compatibility guidance")
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

func TestMutatingAPIsRejectCrossOriginBrowserRequests(t *testing.T) {
	application := app.New(config.Config{
		PS3GameDir:    t.TempDir(),
		PS2GameDir:    t.TempDir(),
		PS2SystemDir:  t.TempDir(),
		PS2USBRoot:    t.TempDir(),
		PS4GameDir:    t.TempDir(),
		PS5GameDir:    t.TempDir(),
		RemoteGameDir: "/dev_hdd0/GAMES",
		FTPTimeout:    time.Second,
		Workers:       1,
	})
	defer application.Close(context.Background())
	handler := New(application).Handler()

	request := httptest.NewRequest(http.MethodPost, "http://manager.local/api/scan", strings.NewReader(`{"cidr":"192.168.1.0/24"}`))
	request.Header.Set("Origin", "http://attacker.example")
	request.Header.Set("Content-Type", "text/plain")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("cross-origin POST status = %d, want %d", response.Code, http.StatusForbidden)
	}

	request = httptest.NewRequest(http.MethodPost, "http://manager.local/api/consoles", strings.NewReader(`{"ip":"127.0.0.1"}`))
	request.Header.Set("Origin", "http://manager.local")
	response = httptest.NewRecorder()
	application.Scanner.Detector = webDetector{}
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("same-origin POST status = %d body=%s", response.Code, response.Body.String())
	}
}

func TestReadinessEndpointUsesServiceUnavailableForMissingLibrary(t *testing.T) {
	root := t.TempDir()
	application := app.New(config.Config{
		PS3GameDir:    root,
		PS2GameDir:    root,
		PS2SystemDir:  root,
		PS2USBRoot:    root,
		PS4GameDir:    root,
		PS5GameDir:    filepath.Join(root, "missing"),
		RemoteGameDir: "/dev_hdd0/GAMES",
		FTPTimeout:    time.Second,
		Workers:       1,
	})
	defer application.Close(context.Background())

	response := httptest.NewRecorder()
	New(application).Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/ready", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("readiness status = %d body=%s", response.Code, response.Body.String())
	}
	for _, required := range []string{`"status":"not_ready"`, `"name":"ps5_library"`, `"required":true`} {
		if !strings.Contains(response.Body.String(), required) {
			t.Errorf("readiness response missing %s: %s", required, response.Body.String())
		}
	}
}

func TestJSONAndOriginValidationFailurePaths(t *testing.T) {
	root := t.TempDir()
	application := app.New(config.Config{
		PS3GameDir: root, PS2GameDir: root, PS2SystemDir: root, PS2USBRoot: root,
		PS4GameDir: root, PS5GameDir: root, RemoteGameDir: "/dev_hdd0/GAMES",
		FTPTimeout: time.Second, Workers: 1,
	})
	defer application.Close(context.Background())
	handler := New(application).Handler()

	tests := []struct {
		name   string
		body   string
		origin string
		want   int
	}{
		{name: "malformed JSON", body: `{`, want: http.StatusBadRequest},
		{name: "unknown field", body: `{"cidr":"192.168.1.0/24","extra":true}`, want: http.StatusBadRequest},
		{name: "multiple objects", body: `{"cidr":"192.168.1.0/24"}{}`, want: http.StatusBadRequest},
		{name: "opaque origin", body: `{"cidr":"192.168.1.0/24"}`, origin: "null", want: http.StatusForbidden},
		{name: "non-HTTP origin", body: `{"cidr":"192.168.1.0/24"}`, origin: "file://manager.local", want: http.StatusForbidden},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "http://manager.local/api/scan", strings.NewReader(test.body))
			if test.origin != "" {
				request.Header.Set("Origin", test.origin)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.want {
				t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func TestPS2APIsUseDiscoveredTargetIDs(t *testing.T) {
	ps3Root, ps2Root, systemRoot, usbRoot := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Join(ps3Root, "Game"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ps2Root, "SCES_517.19.Game.iso"), []byte("fixture"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(systemRoot, "OPL.ELF"), []byte("fixture"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(usbRoot, "usb0"), 0755); err != nil {
		t.Fatal(err)
	}
	application := app.New(config.Config{PS3GameDir: ps3Root, PS2GameDir: ps2Root, PS2SystemDir: systemRoot, PS2USBRoot: usbRoot, RemoteGameDir: "/dev_hdd0/GAMES", FTPTimeout: time.Second, Workers: 1})
	defer application.Close(context.Background())
	handler := New(application).Handler()
	for _, path := range []string{"/api/ps2/games", "/api/ps2/usb", "/api/ps2/usb/status"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusOK {
			t.Fatalf("GET %s = %d: %s", path, response.Code, response.Body.String())
		}
	}
	coverResponse := httptest.NewRecorder()
	handler.ServeHTTP(coverResponse, httptest.NewRequest(http.MethodGet, "/api/ps2/covers/status", nil))
	if coverResponse.Code != http.StatusOK || !strings.Contains(coverResponse.Body.String(), filepath.Join(ps2Root, "covers")) {
		t.Fatalf("cover status = %d: %s", coverResponse.Code, coverResponse.Body.String())
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/ps2/usb/usb0/prepare", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("prepare USB status = %d: %s", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/ps2/queue", strings.NewReader(`{"usb_id":"usb0","game_ids":[],"destination":"/etc"}`))
	request.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("arbitrary destination status = %d", response.Code)
	}
}

func TestEmptyCollectionAPIsReturnArrays(t *testing.T) {
	ps3Root, ps2Root, ps4Root, ps5Root := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	application := app.New(config.Config{
		PS3GameDir:    ps3Root,
		PS2GameDir:    ps2Root,
		PS2SystemDir:  t.TempDir(),
		PS2USBRoot:    t.TempDir(),
		PS4GameDir:    ps4Root,
		PS5GameDir:    ps5Root,
		RemoteGameDir: "/dev_hdd0/GAMES",
		FTPTimeout:    time.Second,
		Workers:       1,
	})
	defer application.Close(context.Background())
	handler := New(application).Handler()

	for _, path := range []string{
		"/api/local-games",
		"/api/ps2/games",
		"/api/consoles",
		"/api/queue",
		"/api/ps2/queue",
		"/api/ps2/fpkg/queue",
		"/api/ps4/games",
		"/api/ps4/consoles",
		"/api/ps4/queue",
		"/api/ps5/games",
		"/api/ps5/consoles",
		"/api/ps5/queue",
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusOK {
			t.Fatalf("GET %s = %d: %s", path, response.Code, response.Body.String())
		}
		if got := strings.TrimSpace(response.Body.String()); got != "[]" {
			t.Errorf("GET %s returned %q, want []", path, got)
		}
	}
	coverStatus := httptest.NewRecorder()
	handler.ServeHTTP(coverStatus, httptest.NewRequest(http.MethodGet, "/api/ps4/covers/status", nil))
	if coverStatus.Code != http.StatusOK || !strings.Contains(coverStatus.Body.String(), filepath.Join(ps4Root, "covers")) {
		t.Fatalf("PS4 cover status = %d: %s", coverStatus.Code, coverStatus.Body.String())
	}
	fpkgStatus := httptest.NewRecorder()
	handler.ServeHTTP(fpkgStatus, httptest.NewRequest(http.MethodGet, "/api/ps2/fpkg/status", nil))
	if fpkgStatus.Code != http.StatusOK || !strings.Contains(fpkgStatus.Body.String(), `"ready":false`) || !strings.Contains(fpkgStatus.Body.String(), "emulator FPKG") {
		t.Fatalf("PS2 FPKG status = %d: %s", fpkgStatus.Code, fpkgStatus.Body.String())
	}
}

func TestGameCardsUseWholeCardSelection(t *testing.T) {
	script, err := assets.ReadFile("webui/app.js")
	if err != nil {
		t.Fatal(err)
	}
	content := string(script)
	if strings.Contains(content, `type="checkbox"`) {
		t.Fatal("game cards still contain checkbox inputs")
	}
	for _, required := range []string{
		`role="button"`,
		`tabindex="0"`,
		`aria-pressed=`,
		`bindGameCards('.ps2-game-card'`,
		`bindGameCards('.ps3-game-card'`,
		`bindGameCards('.ps4-game-card'`,
		`bindGameCards('.ps5-game-card'`,
		`event.key==='Enter'||event.key===' '`,
	} {
		if !strings.Contains(content, required) {
			t.Errorf("game-card selection is missing %q", required)
		}
	}

	stylesheet, err := assets.ReadFile("webui/styles.css")
	if err != nil {
		t.Fatal(err)
	}
	css := string(stylesheet)
	for _, required := range []string{
		"grid-template-columns: repeat(2, minmax(0, 1fr))",
		"touch-action: manipulation",
		".game-card:focus-visible",
		"@media (min-width: 640px)",
		"@media (min-width: 901px)",
	} {
		if !strings.Contains(css, required) {
			t.Errorf("mobile-first game-card CSS is missing %q", required)
		}
	}
}

func TestConsolePullsAppearInPlatformQueues(t *testing.T) {
	script, err := assets.ReadFile("webui/app.js")
	if err != nil {
		t.Fatal(err)
	}
	content := string(script)
	for _, required := range []string{
		"/api/ps4/pull-queue",
		"item.direction==='download'",
		"transferAmount(item)",
		".pull.${suffix}",
		"showView(queueView)",
	} {
		if !strings.Contains(content, required) {
			t.Errorf("pull progress UI is missing %q", required)
		}
	}
}

func TestDesktopSidebarKeepsPS5NavigationReachable(t *testing.T) {
	markup, err := assets.ReadFile("webui/index.html")
	if err != nil {
		t.Fatal(err)
	}
	for _, view := range []string{"ps5games", "ps5consoles", "ps5scan", "ps5queue"} {
		if !strings.Contains(string(markup), `data-view="`+view+`"`) {
			t.Errorf("sidebar is missing PS5 view %q", view)
		}
	}

	stylesheet, err := assets.ReadFile("webui/styles.css")
	if err != nil {
		t.Fatal(err)
	}
	css := string(stylesheet)
	for _, required := range []string{
		"min-height: 0",
		"flex: 1 1 auto",
		"overflow-y: auto",
		"overscroll-behavior: contain",
	} {
		if !strings.Contains(css, required) {
			t.Errorf("desktop sidebar scrolling CSS is missing %q", required)
		}
	}
}

func TestEmbeddedUIHasNoExternalAssetURLs(t *testing.T) {
	for _, name := range []string{"webui/index.html", "webui/app.js", "webui/styles.css"} {
		data, err := assets.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		content := strings.ToLower(string(data))
		for _, external := range []string{"http://", "https://", `src="//`, `href="//`, "url(//"} {
			if strings.Contains(content, external) {
				t.Errorf("%s contains external asset reference %q", name, external)
			}
		}
	}
}
