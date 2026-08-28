package config

import (
	"fmt"
	"os"
	"runtime"
	"strconv"
	"time"
)

type Config struct {
	PS3GameDir       string
	PS2GameDir       string
	PS2SystemDir     string
	PS2USBRoot       string
	PS2CoverDownload bool
	PS4GameDir       string
	PS4RemoteGameDir string
	PS4RPIPort       int
	PS4PKGListen     string
	PS4AdvertiseURL  string
	PS4RPITimeout    time.Duration
	PS5GameDir       string
	PS5RemoteGameDir string
	PS5FTPPort       int
	PS5FTPUser       string
	PS5FTPPassword   string
	RemoteGameDir    string
	Listen           string
	FTPUser          string
	FTPPassword      string
	Workers          int
	ScanTimeout      time.Duration
	FTPTimeout       time.Duration
}

func Load() (Config, error) {
	c := Config{
		PS3GameDir:       env("PS3MGR_PS3_GAME_DIR", "."),
		PS2GameDir:       env("PS3MGR_PS2_GAME_DIR", "./ps2-games"),
		PS2SystemDir:     env("PS3MGR_PS2_SYSTEM_DIR", "./ps2-system"),
		PS2USBRoot:       env("PS3MGR_PS2_USB_MOUNT_ROOT", "/mnt/usb"),
		PS2CoverDownload: true,
		PS4GameDir:       env("PS3MGR_PS4_GAME_DIR", "./ps4-games"),
		PS4RemoteGameDir: env("PS3MGR_PS4_REMOTE_GAME_DIR", "/data/games"),
		PS4RPIPort:       12800,
		PS4PKGListen:     env("PS3MGR_PS4_PKG_LISTEN", "0.0.0.0:8081"),
		PS4AdvertiseURL:  env("PS3MGR_PS4_ADVERTISE_URL", ""),
		PS4RPITimeout:    15 * time.Second,
		PS5GameDir:       env("PS3MGR_PS5_GAME_DIR", "./ps5-games"),
		PS5RemoteGameDir: env("PS3MGR_PS5_REMOTE_GAME_DIR", "/data/etaHEN/games"),
		PS5FTPPort:       2121,
		PS5FTPUser:       env("PS3MGR_PS5_FTP_USER", "anonymous"),
		PS5FTPPassword:   env("PS3MGR_PS5_FTP_PASSWORD", ""),
		RemoteGameDir:    env("PS3MGR_REMOTE_GAME_DIR", "/dev_hdd0/GAMES"),
		Listen:           env("PS3MGR_LISTEN", "127.0.0.1:8080"),
		FTPUser:          env("PS3_FTP_USER", "anonymous"),
		FTPPassword:      env("PS3_FTP_PASSWORD", ""),
		Workers:          min(32, max(4, runtime.NumCPU()*2)),
		ScanTimeout:      500 * time.Millisecond,
		FTPTimeout:       8 * time.Second,
	}
	if raw := os.Getenv("PS3MGR_PS2_COVER_DOWNLOAD"); raw != "" {
		enabled, err := strconv.ParseBool(raw)
		if err != nil {
			return Config{}, fmt.Errorf("PS3MGR_PS2_COVER_DOWNLOAD must be true or false")
		}
		c.PS2CoverDownload = enabled
	}
	if raw := os.Getenv("PS3MGR_WORKERS"); raw != "" {
		workers, err := strconv.Atoi(raw)
		if err != nil || workers < 1 || workers > 256 {
			return Config{}, fmt.Errorf("PS3MGR_WORKERS must be between 1 and 256")
		}
		c.Workers = workers
	}
	if raw := os.Getenv("PS3MGR_PS5_FTP_PORT"); raw != "" {
		port, err := strconv.Atoi(raw)
		if err != nil || port < 1 || port > 65535 {
			return Config{}, fmt.Errorf("PS3MGR_PS5_FTP_PORT must be between 1 and 65535")
		}
		c.PS5FTPPort = port
	}
	if raw := os.Getenv("PS3MGR_PS4_RPI_PORT"); raw != "" {
		port, err := strconv.Atoi(raw)
		if err != nil || port < 1 || port > 65535 {
			return Config{}, fmt.Errorf("PS3MGR_PS4_RPI_PORT must be between 1 and 65535")
		}
		c.PS4RPIPort = port
	}
	if raw := os.Getenv("PS3MGR_PS4_RPI_TIMEOUT"); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil || d <= 0 {
			return Config{}, fmt.Errorf("PS3MGR_PS4_RPI_TIMEOUT must be a positive duration")
		}
		c.PS4RPITimeout = d
	}
	if raw := os.Getenv("PS3MGR_FTP_TIMEOUT"); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil || d <= 0 {
			return Config{}, fmt.Errorf("PS3MGR_FTP_TIMEOUT must be a positive duration")
		}
		c.FTPTimeout = d
	}
	if raw := os.Getenv("PS3MGR_SCAN_TIMEOUT"); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil || d <= 0 {
			return Config{}, fmt.Errorf("PS3MGR_SCAN_TIMEOUT must be a positive duration")
		}
		c.ScanTimeout = d
	}
	return c, nil
}

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
