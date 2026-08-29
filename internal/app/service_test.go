package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"ps3mgr/internal/config"
)

func TestReadinessReportsRequiredAndOptionalChecks(t *testing.T) {
	root := t.TempDir()
	missing := filepath.Join(root, "missing")
	service := New(config.Config{
		PS3GameDir:    root,
		PS2GameDir:    root,
		PS2SystemDir:  missing,
		PS2USBRoot:    missing,
		PS4GameDir:    root,
		PS5GameDir:    root,
		RemoteGameDir: "/dev_hdd0/GAMES",
		FTPTimeout:    time.Second,
		Workers:       1,
	})
	defer service.Close(context.Background())

	report := service.Readiness()
	if !report.Ready || report.Status != "ready_with_warnings" {
		t.Fatalf("readiness = %+v", report)
	}

	service.PS5.GameDir = missing
	report = service.Readiness()
	if report.Ready || report.Status != "not_ready" {
		t.Fatalf("missing required library readiness = %+v", report)
	}

	filePath := filepath.Join(root, "not-a-directory")
	if err := os.WriteFile(filePath, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	check := directoryCheck("fixture", filePath, true)
	if check.Status == "ok" || check.Detail == "" {
		t.Fatalf("file readiness check = %+v", check)
	}
	cacheCheck := writableDirectoryCheck("fixture_cache", filePath)
	if cacheCheck.Status != "warning" || cacheCheck.Detail == "" {
		t.Fatalf("cache readiness check = %+v", cacheCheck)
	}
}

type directDetector struct {
	detected bool
	count    int
	err      error
}

func (d directDetector) Detect(context.Context, string) (bool, int, error) {
	return d.detected, d.count, d.err
}

func TestAddConsoleValidatesDetectsAndStores(t *testing.T) {
	service := New(config.Config{})
	defer service.Close(context.Background())

	if _, err := service.AddConsole(context.Background(), "8.8.8.8"); err == nil {
		t.Fatal("expected public address rejection")
	}
	service.Scanner.Detector = directDetector{detected: false}
	if _, err := service.AddConsole(context.Background(), "192.168.1.20"); err == nil {
		t.Fatal("expected non-PS3 FTP rejection")
	}
	service.Scanner.Detector = directDetector{detected: true, count: 17}
	console, err := service.AddConsole(context.Background(), " 192.168.1.20 ")
	if err != nil {
		t.Fatal(err)
	}
	if console.GameCount != 17 || !console.Detected || len(service.Consoles()) != 1 {
		t.Fatalf("unexpected console: %+v", console)
	}
	service.Scanner.Detector = directDetector{err: errors.New("offline")}
	if _, err := service.AddConsole(context.Background(), "192.168.1.21"); err == nil {
		t.Fatal("expected connection error")
	}
}
