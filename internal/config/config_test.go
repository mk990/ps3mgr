package config

import (
	"testing"
	"time"
)

func TestLoadDefaultsAndEnvironment(t *testing.T) {
	for _, name := range []string{"PS3MGR_GAME_DIR", "PS3MGR_PS3_GAME_DIR", "PS3MGR_PS2_GAME_DIR", "PS3MGR_PS2_SYSTEM_DIR", "PS3MGR_PS2_USB_MOUNT_ROOT", "PS3MGR_PS2_COVER_DOWNLOAD", "PS3MGR_PS4_GAME_DIR", "PS3MGR_PS4_REMOTE_GAME_DIR", "PS3MGR_PS4_RPI_PORT", "PS3MGR_PS4_PKG_LISTEN", "PS3MGR_PS4_ADVERTISE_URL", "PS3MGR_PS4_RPI_TIMEOUT", "PS3MGR_PS5_GAME_DIR", "PS3MGR_PS5_REMOTE_GAME_DIR", "PS3MGR_PS5_FTP_PORT", "PS3MGR_PS5_FTP_USER", "PS3MGR_PS5_FTP_PASSWORD", "PS3MGR_REMOTE_GAME_DIR", "PS3MGR_LISTEN", "PS3_FTP_USER", "PS3_FTP_PASSWORD", "PS3MGR_WORKERS", "PS3MGR_SCAN_TIMEOUT", "PS3MGR_FTP_TIMEOUT"} {
		t.Setenv(name, "")
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PS3GameDir != "." || cfg.Listen != "127.0.0.1:8080" || cfg.RemoteGameDir != "/dev_hdd0/GAMES" || cfg.PS4GameDir != "./ps4-games" || cfg.PS4RemoteGameDir != "/user/app" || cfg.PS4RPIPort != 12800 || cfg.PS4PKGListen != "0.0.0.0:8081" || cfg.PS4AdvertiseURL != "" || cfg.PS4RPITimeout != 15*time.Second || cfg.PS5GameDir != "./ps5-games" || cfg.PS5RemoteGameDir != "/data/etaHEN/games" || cfg.PS5FTPPort != 2121 || cfg.ScanTimeout != 500*time.Millisecond || !cfg.PS2CoverDownload {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
	t.Setenv("PS3MGR_GAME_DIR", "/legacy-must-be-ignored")
	t.Setenv("PS3MGR_PS3_GAME_DIR", "/games")
	t.Setenv("PS3MGR_PS2_GAME_DIR", "/ps2")
	t.Setenv("PS3MGR_PS2_SYSTEM_DIR", "/ps2/system")
	t.Setenv("PS3MGR_PS2_USB_MOUNT_ROOT", "/usb")
	t.Setenv("PS3MGR_PS2_COVER_DOWNLOAD", "false")
	t.Setenv("PS3MGR_PS4_GAME_DIR", "/ps4")
	t.Setenv("PS3MGR_PS4_RPI_PORT", "12801")
	t.Setenv("PS3MGR_PS4_PKG_LISTEN", "0.0.0.0:8181")
	t.Setenv("PS3MGR_PS4_ADVERTISE_URL", "http://192.168.1.20:8181")
	t.Setenv("PS3MGR_PS4_RPI_TIMEOUT", "3s")
	t.Setenv("PS3MGR_PS5_GAME_DIR", "/ps5")
	t.Setenv("PS3MGR_PS5_REMOTE_GAME_DIR", "/data/etaHEN/games-custom")
	t.Setenv("PS3MGR_PS5_FTP_PORT", "2211")
	t.Setenv("PS3MGR_PS5_FTP_USER", "ps5")
	t.Setenv("PS3MGR_WORKERS", "12")
	t.Setenv("PS3MGR_FTP_TIMEOUT", "1500ms")
	t.Setenv("PS3MGR_SCAN_TIMEOUT", "250ms")
	cfg, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PS3GameDir != "/games" || cfg.PS2GameDir != "/ps2" || cfg.PS2SystemDir != "/ps2/system" || cfg.PS2USBRoot != "/usb" || cfg.PS2CoverDownload || cfg.PS4GameDir != "/ps4" || cfg.PS4RPIPort != 12801 || cfg.PS4PKGListen != "0.0.0.0:8181" || cfg.PS4AdvertiseURL != "http://192.168.1.20:8181" || cfg.PS4RPITimeout != 3*time.Second || cfg.PS5GameDir != "/ps5" || cfg.PS5RemoteGameDir != "/data/etaHEN/games-custom" || cfg.PS5FTPPort != 2211 || cfg.PS5FTPUser != "ps5" || cfg.Workers != 12 || cfg.FTPTimeout != 1500*time.Millisecond || cfg.ScanTimeout != 250*time.Millisecond {
		t.Fatalf("environment was not applied: %+v", cfg)
	}
}

func TestLoadRejectsInvalidEnvironment(t *testing.T) {
	t.Setenv("PS3MGR_PS2_COVER_DOWNLOAD", "sometimes")
	if _, err := Load(); err == nil {
		t.Fatal("expected invalid cover download setting")
	}
	t.Setenv("PS3MGR_PS2_COVER_DOWNLOAD", "false")
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
	t.Setenv("PS3MGR_SCAN_TIMEOUT", "1s")
	t.Setenv("PS3MGR_PS5_FTP_PORT", "70000")
	if _, err := Load(); err == nil {
		t.Fatal("expected invalid PS5 FTP port error")
	}
	t.Setenv("PS3MGR_PS5_FTP_PORT", "2121")
	t.Setenv("PS3MGR_PS4_RPI_PORT", "0")
	if _, err := Load(); err == nil {
		t.Fatal("expected invalid PS4 RPI port error")
	}
}

func TestLegacyPS3GameDirectoryVariableIsIgnored(t *testing.T) {
	t.Setenv("PS3MGR_GAME_DIR", "/legacy")
	t.Setenv("PS3MGR_PS3_GAME_DIR", "")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PS3GameDir != "." {
		t.Fatalf("legacy PS3MGR_GAME_DIR was unexpectedly used: %q", cfg.PS3GameDir)
	}
}
