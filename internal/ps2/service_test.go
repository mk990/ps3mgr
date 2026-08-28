package ps2

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestServiceEndToEndISOToDiscoveredUSBQueue(t *testing.T) {
	gamesRoot, systemRoot, usbRoot := t.TempDir(), t.TempDir(), t.TempDir()
	targetRoot := filepath.Join(usbRoot, "usb0")
	if err := os.Mkdir(targetRoot, 0755); err != nil {
		t.Fatal(err)
	}
	payload := []byte("authorized PS2 ISO fixture")
	if err := os.WriteFile(filepath.Join(gamesRoot, "SCES_517.19.Gran Turismo 4.iso"), payload, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(systemRoot, "OPNPS2LD.ELF"), []byte("fixture"), 0644); err != nil {
		t.Fatal(err)
	}
	service := NewService(gamesRoot, systemRoot, usbRoot, nil)
	defer service.Close(context.Background())
	games, err := service.LocalGames(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(games) != 1 {
		t.Fatalf("games = %d", len(games))
	}
	targets, err := service.USBTargets()
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 || targets[0].ID != "usb0" {
		t.Fatalf("targets = %#v", targets)
	}
	if targets[0].Filesystem == "" || targets[0].FAT32Status == "" {
		t.Fatalf("compatibility metadata missing: %#v", targets[0])
	}
	prepared, err := service.PrepareUSB(context.Background(), "usb0")
	if err != nil {
		t.Fatal(err)
	}
	if !prepared.OPLReady {
		t.Fatalf("prepared target = %#v", prepared)
	}
	jobs, err := service.Enqueue("usb0", []string{games[0].PublicID})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		job, _ := service.Queue.Get(jobs[0].ID)
		if job.State == StateCompleted {
			installed := filepath.Join(targetRoot, "DVD", "SCES_517.19.Gran Turismo 4.iso")
			data, readErr := os.ReadFile(installed)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if string(data) != string(payload) {
				t.Fatalf("installed data = %q", data)
			}
			return
		}
		if job.State == StateFailed {
			t.Fatalf("job failed: %s", job.Error)
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("PS2 install did not complete")
}
