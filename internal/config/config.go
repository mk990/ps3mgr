package config

import (
	"fmt"
	"os"
	"runtime"
	"strconv"
	"time"
)

type Config struct {
	GameDir       string
	RemoteGameDir string
	Listen        string
	FTPUser       string
	FTPPassword   string
	Workers       int
	FTPTimeout    time.Duration
}

func Load() (Config, error) {
	c := Config{
		GameDir:       env("PS3MGR_GAME_DIR", "."),
		RemoteGameDir: env("PS3MGR_REMOTE_GAME_DIR", "/dev_hdd0/GAMES"),
		Listen:        env("PS3MGR_LISTEN", "127.0.0.1:8080"),
		FTPUser:       env("PS3_FTP_USER", "anonymous"),
		FTPPassword:   env("PS3_FTP_PASSWORD", ""),
		Workers:       min(32, max(4, runtime.NumCPU()*2)),
		FTPTimeout:    8 * time.Second,
	}
	if raw := os.Getenv("PS3MGR_WORKERS"); raw != "" {
		workers, err := strconv.Atoi(raw)
		if err != nil || workers < 1 || workers > 256 {
			return Config{}, fmt.Errorf("PS3MGR_WORKERS must be between 1 and 256")
		}
		c.Workers = workers
	}
	if raw := os.Getenv("PS3MGR_FTP_TIMEOUT"); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil || d <= 0 {
			return Config{}, fmt.Errorf("PS3MGR_FTP_TIMEOUT must be a positive duration")
		}
		c.FTPTimeout = d
	}
	return c, nil
}

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
