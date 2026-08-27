package config

import (
	"testing"
	"time"
)

func TestLoadDefaultsAndEnvironment(t *testing.T) {
	for _, name := range []string{"PS3MGR_GAME_DIR", "PS3MGR_REMOTE_GAME_DIR", "PS3MGR_LISTEN", "PS3_FTP_USER", "PS3_FTP_PASSWORD", "PS3MGR_WORKERS", "PS3MGR_SCAN_TIMEOUT", "PS3MGR_FTP_TIMEOUT"} {
		t.Setenv(name, "")
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GameDir != "." || cfg.Listen != "127.0.0.1:8080" || cfg.RemoteGameDir != "/dev_hdd0/GAMES" || cfg.ScanTimeout != 500*time.Millisecond {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
	t.Setenv("PS3MGR_GAME_DIR", "/games")
	t.Setenv("PS3MGR_WORKERS", "12")
	t.Setenv("PS3MGR_FTP_TIMEOUT", "1500ms")
	t.Setenv("PS3MGR_SCAN_TIMEOUT", "250ms")
	cfg, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GameDir != "/games" || cfg.Workers != 12 || cfg.FTPTimeout != 1500*time.Millisecond || cfg.ScanTimeout != 250*time.Millisecond {
		t.Fatalf("environment was not applied: %+v", cfg)
	}
}

func TestLoadRejectsInvalidEnvironment(t *testing.T) {
	t.Setenv("PS3MGR_WORKERS", "0")
	if _, err := Load(); err == nil {
		t.Fatal("expected invalid workers error")
	}
	t.Setenv("PS3MGR_WORKERS", "4")
	t.Setenv("PS3MGR_FTP_TIMEOUT", "never")
	if _, err := Load(); err == nil {
		t.Fatal("expected invalid timeout error")
	}
	t.Setenv("PS3MGR_FTP_TIMEOUT", "1s")
	t.Setenv("PS3MGR_SCAN_TIMEOUT", "0s")
	if _, err := Load(); err == nil {
		t.Fatal("expected invalid scan timeout error")
	}
}
