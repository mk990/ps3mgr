package cli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"ps3mgr/internal/app"
	"ps3mgr/internal/domain"
	"ps3mgr/internal/ps2"
	"ps3mgr/internal/ps4"
	ps3web "ps3mgr/internal/web"
)

func (r Runner) serve(ctx context.Context, application *app.Service, args []string) error {
	set := newFlagSet("serve", r.Err)
	listen := set.String("listen", application.Config.Listen, "listen address")
	directory := set.String("dir", "", "local game directory")
	ps2Directory := set.String("ps2-dir", "", "local PS2 ISO directory")
	ps4Directory := set.String("ps4-dir", "", "local PS4 PKG directory")
	ps5Directory := set.String("ps5-dir", "", "local PS5 ShadowMountPlus game directory")
	if err := set.Parse(args); err != nil {
		return err
	}
	if *directory != "" {
		application.Config.PS3GameDir = *directory
	}
	if *ps2Directory != "" {
		application.PS2.GameDir = *ps2Directory
	}
	if *ps4Directory != "" {
		application.PS4.GameDir = *ps4Directory
		application.PS4.Content.SetRoot(*ps4Directory)
	}
	if *ps5Directory != "" {
		application.PS5.GameDir = *ps5Directory
	}
	logger := slog.New(slog.NewTextHandler(r.Out, &slog.HandlerOptions{Level: slog.LevelInfo}))
	logger.Info("[APP] starting PlayStation Manager", "listen", *listen, "ps2_game_dir", application.PS2.GameDir, "ps2_system_dir", application.PS2.SystemDir, "ps2_usb_root", application.Config.PS2USBRoot, "ps2_cover_download", application.Config.PS2CoverDownload, "ps2_cover_cache", filepath.Join(application.PS2.GameDir, "covers"), "ps3_game_dir", application.Config.PS3GameDir, "ps3_remote_dir", application.Config.RemoteGameDir, "ps4_game_dir", application.PS4.GameDir, "ps4_rpi_port", application.PS4.RPI.Port, "ps4_pkg_listen", application.PS4.Content.Listen, "ps4_advertise_url", fallback(application.PS4.Content.AdvertiseURL, "not configured"), "ps5_game_dir", application.PS5.GameDir, "ps5_remote_dir", application.PS5.RemoteDir, "ps5_ftp_port", application.PS5.FTP.Port)
	startPS4PackageServer(logger, application.PS4.Content)
	stopEventLogs := logServeEvents(ctx, logger, application)
	defer stopEventLogs()
	handler := ps3web.New(application).Handler()
	server := &http.Server{
		Addr: *listen, Handler: handler,
		ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 90 * time.Second,
	}
	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", *listen, err)
	}
	serverErrors := make(chan error, 1)
	go func() { serverErrors <- server.Serve(listener) }()
	logger.Info("[APP] web server ready", "url", "http://"+displayAddress(*listen), "health_url", "http://"+displayAddress(*listen)+"/api/health", "readiness_url", "http://"+displayAddress(*listen)+"/api/ready", "ps2_games_url", "http://"+displayAddress(*listen)+"/ps2-games", "ps3_games_url", "http://"+displayAddress(*listen)+"/ps3-games", "ps4_games_url", "http://"+displayAddress(*listen)+"/ps4-games", "ps5_games_url", "http://"+displayAddress(*listen)+"/ps5-games")

	startupCtx, cancelStartup := context.WithCancel(ctx)
	defer cancelStartup()
	go initializeServe(startupCtx, logger, application)

	select {
	case <-ctx.Done():
		logger.Info("[APP] shutdown requested")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		err := server.Shutdown(shutdownCtx)
		if err != nil {
			logger.Error("[APP] web server shutdown failed", "error", err)
		} else {
			logger.Info("[APP] web server stopped")
		}
		return err
	case err := <-serverErrors:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func initializeServe(ctx context.Context, logger *slog.Logger, application *app.Service) {
	started := time.Now()
	ps3Count := 0
	if items, err := application.LocalGames(ctx, ""); err != nil {
		if ctx.Err() != nil {
			return
		}
		logger.Error("[PS3] local library scan failed; panel remains available", "directory", application.Config.PS3GameDir, "error", err)
	} else {
		ps3Count = len(items)
		logger.Info("[PS3] local library loaded", "games", ps3Count, "directory", application.Config.PS3GameDir)
	}

	ps2Count := 0
	if _, statErr := os.Stat(application.PS2.GameDir); statErr == nil {
		coverStatus := application.PS2.CoverStatus()
		switch {
		case !coverStatus.Enabled:
			logger.Info("[PS2] cover downloads disabled", "cache", coverStatus.CacheDir)
		case coverStatus.Error != "":
			logger.Error("[PS2] cover cache unavailable; check Docker bind-mount ownership", "game_directory", coverStatus.GameDir, "cache", coverStatus.CacheDir, "error", coverStatus.Error)
		default:
			logger.Info("[PS2] cover cache ready", "cache", coverStatus.CacheDir, "cached_images", coverStatus.Images, "writable", coverStatus.Writable)
		}
		ps2Items, scanErr := application.PS2.LocalGames(ctx, "")
		if scanErr != nil {
			if ctx.Err() != nil {
				return
			}
			logger.Error("[PS2] local library scan failed; panel remains available", "directory", application.PS2.GameDir, "error", scanErr)
		} else {
			ps2Count = len(ps2Items)
			logger.Info("[PS2] local library loaded", "games", ps2Count, "directory", application.PS2.GameDir)
		}
	} else if os.IsNotExist(statErr) {
		logger.Warn("[PS2] local library is unavailable", "directory", application.PS2.GameDir, "hint", "set PS3MGR_PS2_GAME_DIR or use --ps2-dir")
	} else {
		logger.Error("[PS2] cannot access local library; panel remains available", "directory", application.PS2.GameDir, "error", statErr)
	}

	ps4Count := 0
	if _, statErr := os.Stat(application.PS4.GameDir); statErr == nil {
		coverStatus := application.PS4.CoverStatus()
		if coverStatus.Error != "" {
			logger.Error("[PS4] cover cache unavailable; check Docker bind-mount ownership", "game_directory", coverStatus.GameDir, "cache", coverStatus.CacheDir, "error", coverStatus.Error)
		} else {
			logger.Info("[PS4] cover cache ready", "cache", coverStatus.CacheDir, "cached_images", coverStatus.Images, "writable", coverStatus.Writable, "source", "embedded PKG icon0")
		}
		ps4Items, scanErr := application.PS4.LocalPackages(ctx, "")
		if scanErr != nil {
			if ctx.Err() != nil {
				return
			}
			logger.Error("[PS4] local PKG library scan failed; panel remains available", "directory", application.PS4.GameDir, "error", scanErr)
		} else {
			ps4Count = len(ps4Items)
			logger.Info("[PS4] local PKG library loaded", "packages", ps4Count, "directory", application.PS4.GameDir)
		}
	} else if os.IsNotExist(statErr) {
		logger.Warn("[PS4] local PKG library is unavailable", "directory", application.PS4.GameDir, "hint", "set PS3MGR_PS4_GAME_DIR or use --ps4-dir")
	} else {
		logger.Error("[PS4] cannot access local PKG library; panel remains available", "directory", application.PS4.GameDir, "error", statErr)
	}
	ps5Count := 0
	if _, statErr := os.Stat(application.PS5.GameDir); statErr == nil {
		ps5Items, scanErr := application.PS5.LocalGames(ctx, "")
		if scanErr != nil {
			if ctx.Err() != nil {
				return
			}
			logger.Error("[PS5] local library scan failed; panel remains available", "directory", application.PS5.GameDir, "error", scanErr)
		} else {
			ps5Count = len(ps5Items)
			logger.Info("[PS5] local library loaded", "games", ps5Count, "directory", application.PS5.GameDir, "formats", "folder,ffpfsc,exfat,ffpkg,ffpfs")
		}
	} else if os.IsNotExist(statErr) {
		logger.Warn("[PS5] local library is unavailable", "directory", application.PS5.GameDir, "hint", "set PS3MGR_PS5_GAME_DIR or use --ps5-dir")
	} else {
		logger.Error("[PS5] cannot access local library; panel remains available", "directory", application.PS5.GameDir, "error", statErr)
	}

	discovery, usbErr := application.PS2.USBDiscovery()
	if usbErr != nil {
		logger.Warn("[PS2] USB discovery failed", "root", application.Config.PS2USBRoot, "error", usbErr)
	} else {
		logger.Info("[PS2] USB discovery completed", "targets", len(discovery.Targets), "root", discovery.Root, "mode", discovery.Mode, "issues", len(discovery.Issues))
		for _, issue := range discovery.Issues {
			logger.Warn("[PS2] USB discovery issue", "path", issue.Path, "reason", issue.Reason)
		}
		for _, target := range discovery.Targets {
			logger.Info("[PS2] USB target available", "usb_id", target.ID, "mount", target.MountPath, "filesystem", target.Filesystem, "fat32_status", target.FAT32Status, "free_bytes", target.FreeBytes, "read_only", target.ReadOnly, "opl_ready", target.OPLReady)
		}
	}
	logger.Info("[APP] startup discovery completed", "duration", time.Since(started).Round(time.Millisecond), "ps2_games", ps2Count, "ps3_games", ps3Count, "ps4_packages", ps4Count, "ps5_games", ps5Count)
}

func startPS4PackageServer(logger *slog.Logger, content *ps4.ContentServer) {
	if startErr := content.Start(); startErr != nil {
		logger.Error("[PS4] package server failed to start; panel remains available", "listen", content.Listen, "error", startErr)
		return
	}
	logger.Info("[PS4] package server ready", "listen", content.Listen, "index_url", "http://"+displayAddress(content.Listen)+"/", "health_url", "http://"+displayAddress(content.Listen)+"/healthz")
	if advertiseErr := content.AdvertiseError(); advertiseErr != nil {
		logger.Warn("[PS4] package installs are not configured; package index remains available", "error", advertiseErr, "required_env", "PS3MGR_PS4_ADVERTISE_URL", "example", "http://192.168.1.20:8081")
		return
	}
	logger.Info("[PS4] package install URL configured", "advertise_url", content.AdvertiseURL)
}

func logServeEvents(ctx context.Context, logger *slog.Logger, application *app.Service) func() {
	stream, unsubscribe := application.Events.Subscribe(256)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-ctx.Done():
				return
			case event, ok := <-stream:
				if !ok {
					return
				}
				switch event.Type {
				case "ps2.covers.cached":
					logger.Info("[PS2] covers cached", "event", event.Payload)
				case "ps2.covers.failed":
					logger.Warn("[PS2] one or more covers could not be cached", "event", event.Payload)
				case "ps2.usb.connected", "ps2.usb.disconnected", "ps2.usb.prepared", "ps2.usb.skipped":
					logger.Info("[PS2] "+event.Type, "event", event.Payload)
				case "ps2.job.started":
					if job, ok := event.Payload.(ps2.Job); ok {
						logger.Info("[PS2] job started", "job_id", job.ID, "game", job.Game.Title, "game_id", job.Game.ID, "usb_id", job.USBID, "attempt", job.Attempts)
					}
				case "ps2.job.completed":
					if job, ok := event.Payload.(ps2.Job); ok {
						logger.Info("[PS2] job completed", "job_id", job.ID, "game", job.Game.Title, "usb_id", job.USBID, "bytes", job.Game.Size)
					}
				case "ps2.job.failed", "ps2.job.paused":
					if job, ok := event.Payload.(ps2.Job); ok {
						logger.Error("[PS2] "+event.Type, "job_id", job.ID, "game", job.Game.Title, "usb_id", job.USBID, "recoverable", job.Recoverable, "error", job.Error)
					}
				case "ps2.queue.completed":
					logger.Info("[PS2] queue completed", "summary", event.Payload)
				case "ps4.scan.started", "ps4.scan.completed":
					logger.Info("[PS4] "+event.Type, "event", event.Payload)
				case "ps4.covers.cached":
					logger.Info("[PS4] embedded covers cached", "event", event.Payload)
				case "ps4.covers.failed":
					logger.Warn("[PS4] one or more covers could not be cached", "event", event.Payload)
				case "ps4.console.connected", "ps4.scan.host_found":
					if console, ok := event.Payload.(domain.Console); ok {
						logger.Info("[PS4] console detected", "ip", console.IP, "api_port", console.APIPort)
					}
				case "ps4.job.started":
					if job, ok := event.Payload.(ps4.Job); ok {
						logger.Info("[PS4] install started", "job_id", job.ID, "package", job.Package.Title, "title_id", job.Package.TitleID, "parts", len(job.Package.Parts), "console_ip", job.ConsoleIP, "attempt", job.Attempts)
					}
				case "ps4.pkg.serving":
					if job, ok := event.Payload.(ps4.Job); ok {
						logger.Info("[PS4] package available to console", "job_id", job.ID, "package", job.Package.Title, "bytes", job.Package.Size, "parts", len(job.Package.Parts))
					}
				case "ps4.install.requested":
					if job, ok := event.Payload.(ps4.Job); ok {
						logger.Info("[PS4] Remote Package Installer task created", "job_id", job.ID, "task_id", job.TaskID, "console_ip", job.ConsoleIP)
					}
				case "ps4.job.completed":
					if job, ok := event.Payload.(ps4.Job); ok {
						logger.Info("[PS4] install completed", "job_id", job.ID, "package", job.Package.Title, "title_id", job.Package.TitleID, "console_ip", job.ConsoleIP, "bytes", job.TotalBytes)
					}
				case "ps4.job.failed":
					if job, ok := event.Payload.(ps4.Job); ok {
						logger.Error("[PS4] install failed", "job_id", job.ID, "package", job.Package.Title, "console_ip", job.ConsoleIP, "error", job.Error)
					}
				case "ps4.queue.completed":
					logger.Info("[PS4] queue completed", "summary", event.Payload)
				case "ps5.scan.started", "ps5.scan.completed":
					logger.Info("[PS5] "+event.Type, "event", event.Payload)
				case "ps5.console.connected", "ps5.scan.host_found":
					if console, ok := event.Payload.(domain.Console); ok {
						logger.Info("[PS5] console detected", "ip", console.IP, "ftp_port", console.FTPPort, "games", console.GameCount)
					}
				case "ps5.queue.item_started":
					if transfer, ok := event.Payload.(domain.Transfer); ok {
						logger.Info("[PS5] transfer started", "transfer_id", transfer.ID, "game", transfer.Game.Title, "format", transfer.Game.Format, "console_ip", transfer.ConsoleIP, "ftp_port", application.PS5.FTP.Port, "destination", application.PS5.RemoteDir, "attempt", transfer.Attempts)
					}
				case "ps5.queue.item_completed":
					if transfer, ok := event.Payload.(domain.Transfer); ok {
						logger.Info("[PS5] transfer completed", "transfer_id", transfer.ID, "game", transfer.Game.Title, "console_ip", transfer.ConsoleIP, "bytes", transfer.TotalBytes, "destination", application.PS5.RemoteDir)
					}
				case "ps5.queue.item_failed":
					if transfer, ok := event.Payload.(domain.Transfer); ok {
						logger.Error("[PS5] transfer failed", "transfer_id", transfer.ID, "game", transfer.Game.Title, "console_ip", transfer.ConsoleIP, "error", transfer.Error)
					}
				case "ps5.queue.completed":
					logger.Info("[PS5] queue completed", "summary", event.Payload)
				case "scan.started", "scan.completed":
					logger.Info("[PS3] "+event.Type, "event", event.Payload)
				case "console.connected":
					if console, ok := event.Payload.(domain.Console); ok {
						logger.Info("[PS3] console connected", "ip", console.IP, "games", console.GameCount)
					}
				case "queue.item_started":
					if transfer, ok := event.Payload.(domain.Transfer); ok {
						logger.Info("[PS3] transfer started", "transfer_id", transfer.ID, "game", transfer.Game.Title, "console_ip", transfer.ConsoleIP, "attempt", transfer.Attempts)
					}
				case "queue.item_completed":
					if transfer, ok := event.Payload.(domain.Transfer); ok {
						logger.Info("[PS3] transfer completed", "transfer_id", transfer.ID, "game", transfer.Game.Title, "console_ip", transfer.ConsoleIP, "bytes", transfer.TotalBytes)
					}
				case "queue.item_failed":
					if transfer, ok := event.Payload.(domain.Transfer); ok {
						logger.Error("[PS3] transfer failed", "transfer_id", transfer.ID, "game", transfer.Game.Title, "console_ip", transfer.ConsoleIP, "error", transfer.Error)
					}
				case "queue.completed":
					logger.Info("[PS3] queue completed", "summary", event.Payload)
				}
			}
		}
	}()
	return func() {
		unsubscribe()
		select {
		case <-done:
		case <-time.After(time.Second):
		}
	}
}
