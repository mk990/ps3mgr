package app

import (
	"context"
	"errors"
	"testing"

	"ps3mgr/internal/config"
)

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
