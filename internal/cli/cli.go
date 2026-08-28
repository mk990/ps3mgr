package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"ps3mgr/internal/app"
	"ps3mgr/internal/config"
	"ps3mgr/internal/domain"
	"ps3mgr/internal/games"
	"ps3mgr/internal/ps2"
	"ps3mgr/internal/ps4"
	ps3web "ps3mgr/internal/web"
)

// version is replaced at build time for tagged releases and container images.
var version = "dev"

type Runner struct {
	Out io.Writer
	Err io.Writer
}

func (r Runner) Run(ctx context.Context, args []string) int {
	if r.Out == nil {
		r.Out = os.Stdout
	}
	if r.Err == nil {
		r.Err = os.Stderr
	}
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		r.help()
		return 0
	}
	if args[0] == "version" || args[0] == "--version" {
		fmt.Fprintln(r.Out, "ps3mgr "+version)
		return 0
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(r.Err, "error:", err)
		return 2
	}
	application := app.New(cfg)
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = application.Close(closeCtx)
	}()

	var commandErr error
	switch args[0] {
	case "local-games":
		commandErr = r.localGames(ctx, application, args[1:])
	case "scan":
		commandErr = r.scan(ctx, application, args[1:])
	case "consoles":
		commandErr = r.consoles(application, args[1:])
	case "add-console":
		commandErr = r.addConsole(ctx, application, args[1:])
	case "games":
		commandErr = r.remoteGames(ctx, application, args[1:])
	case "compare":
		commandErr = r.compare(ctx, application, args[1:])
	case "install":
		commandErr = r.install(ctx, application, args[1:])
	case "serve":
		commandErr = r.serve(ctx, application, args[1:])
	case "ps2":
		commandErr = r.ps2(ctx, application, args[1:])
	case "ps4":
		commandErr = r.ps4(ctx, application, args[1:])
	case "ps5":
		commandErr = r.ps5(ctx, application, args[1:])
	default:
		fmt.Fprintf(r.Err, "unknown command %q\n\n", args[0])
		r.help()
		return 2
	}
	if commandErr != nil {
		if errors.Is(commandErr, flag.ErrHelp) {
			return 0
		}
		fmt.Fprintln(r.Err, "error:", commandErr)
		return 1
	}
	return 0
}

func (r Runner) help() {
	fmt.Fprint(r.Out, `PlayStation Manager — isolated PS2, PS3, PS4, and PS5 workflows

Usage:
  ps3mgr <command> [options]

Commands:
  ps2 <command>         Manage PS2 ISOs and OPL USB targets
  ps4 <command>         Install PS4 PKGs through Remote Package Installer
  ps5 <command>         Manage PS5 ShadowMountPlus games over FTP port 2121
  local-games           Scan and display the local game library
  scan <CIDR>           Discover PS3 consoles on a private local network
  consoles              List consoles found in the current process
  add-console --ip <IP> Verify a known PS3 address directly
  games --ip <IP>       List games installed on a PS3
  compare --ip <IP>     Compare the local library with a PS3
  install --ip <IP> <GAME...>
                         Install games sequentially
  serve                 Start the web panel (default 127.0.0.1:8080)
  version               Print the version

Use "ps3mgr <command> --help" for command-specific options.
`)
}

func (r Runner) ps2(ctx context.Context, application *app.Service, args []string) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprint(r.Out, `PlayStation 2 / Open PS2 Loader

Usage:
  ps3mgr ps2 local-games [--dir PATH] [--size] [--json]
  ps3mgr ps2 games       [--dir PATH] [--json]
  ps3mgr ps2 usb         [--json]
  ps3mgr ps2 compare     --usb USB_ID [--json]
  ps3mgr ps2 install     --usb USB_ID [--all] <GAME...>
  ps3mgr ps2 queue       [--json]
`)
		return nil
	}
	switch args[0] {
	case "local-games", "games":
		return r.ps2LocalGames(ctx, application, args[1:])
	case "usb":
		return r.ps2USB(application, args[1:])
	case "compare":
		return r.ps2Compare(ctx, application, args[1:])
	case "install":
		return r.ps2Install(ctx, application, args[1:])
	case "queue":
		return r.ps2Queue(application, args[1:])
	default:
		return fmt.Errorf("unknown PS2 command %q", args[0])
	}
}

func (r Runner) ps2LocalGames(ctx context.Context, application *app.Service, args []string) error {
	set := newFlagSet("ps2 local-games", r.Err)
	directory := set.String("dir", "", "PS2 ISO directory (overrides PS3MGR_PS2_GAME_DIR)")
	sizeOnly := set.Bool("size", false, "print ISO size report")
	jsonOutput := set.Bool("json", false, "print JSON")
	_ = set.Bool("verbose", false, "verbose output")
	if err := set.Parse(args); err != nil {
		return err
	}
	items, err := application.PS2.LocalGames(ctx, *directory)
	if err != nil {
		return err
	}
	if *jsonOutput {
		return r.printValue(items, true)
	}
	var total int64
	if *sizeOnly {
		fmt.Fprintln(r.Out, "PS2 ISO Size Report")
		fmt.Fprintln(r.Out)
		fmt.Fprintf(r.Out, "%-40s %12s\n", "Game", "Size")
		fmt.Fprintln(r.Out, strings.Repeat("-", 53))
	}
	for _, game := range items {
		total += game.Size
		if *sizeOnly {
			fmt.Fprintf(r.Out, "%-40s %12s\n", game.Title, humanBytes(game.Size))
		} else {
			fmt.Fprintf(r.Out, "%-12s %10s  %s\n", strings.ToUpper(game.ID), humanBytes(game.Size), game.Title)
		}
	}
	if *sizeOnly {
		fmt.Fprintf(r.Out, "\nTotal: %s\n", humanBytes(total))
	} else {
		fmt.Fprintf(r.Out, "\n%d PS2 games\n", len(items))
	}
	return nil
}

func (r Runner) ps2USB(application *app.Service, args []string) error {
	set := newFlagSet("ps2 usb", r.Err)
	jsonOutput := set.Bool("json", false, "print JSON")
	_ = set.Bool("verbose", false, "verbose output")
	if err := set.Parse(args); err != nil {
		return err
	}
	items, err := application.PS2.USBTargets()
	if err != nil {
		return err
	}
	if *jsonOutput {
		return r.printValue(items, true)
	}
	for _, target := range items {
		status := "available"
		if target.ReadOnly {
			status = "read-only"
		}
		fmt.Fprintf(r.Out, "%-12s %-24s %10s free  %-10s  FAT32: %s\n", target.ID, target.MountPath, humanBytes(target.FreeBytes), fallback(target.Filesystem, "unknown"), target.FAT32Status+" ("+status+")")
	}
	if len(items) == 0 {
		fmt.Fprintln(r.Out, "No PS2 USB devices detected.")
	}
	return nil
}

func (r Runner) ps2Compare(ctx context.Context, application *app.Service, args []string) error {
	set := newFlagSet("ps2 compare", r.Err)
	usb := set.String("usb", "", "USB target ID (required)")
	directory := set.String("dir", "", "PS2 ISO directory")
	jsonOutput := set.Bool("json", false, "print JSON")
	_ = set.Bool("verbose", false, "verbose output")
	if err := set.Parse(args); err != nil {
		return err
	}
	if *usb == "" {
		return fmt.Errorf("--usb is required")
	}
	if _, err := application.PS2.LocalGames(ctx, *directory); err != nil {
		return err
	}
	if _, err := application.PS2.USBTargets(); err != nil {
		return err
	}
	items, err := application.PS2.Compare(*usb)
	if err != nil {
		return err
	}
	if *jsonOutput {
		return r.printValue(items, true)
	}
	for _, item := range items {
		status := "MISSING"
		if item.Installed {
			status = "INSTALLED"
		}
		fmt.Fprintf(r.Out, "%-10s %-12s %s\n", status, item.Game.ID, item.Game.Title)
	}
	return nil
}

func (r Runner) ps2Install(ctx context.Context, application *app.Service, args []string) error {
	set := newFlagSet("ps2 install", r.Err)
	usb := set.String("usb", "", "USB target ID (required)")
	directory := set.String("dir", "", "PS2 ISO directory")
	all := set.Bool("all", false, "install all games missing from the USB")
	jsonOutput := set.Bool("json", false, "print JSON")
	_ = set.Bool("verbose", false, "verbose output")
	if err := set.Parse(args); err != nil {
		return err
	}
	if *usb == "" {
		return fmt.Errorf("--usb is required")
	}
	gamesList, err := application.PS2.LocalGames(ctx, *directory)
	if err != nil {
		return err
	}
	if _, err = application.PS2.USBTargets(); err != nil {
		return err
	}
	var selected []ps2.Game
	if *all {
		compared, err := application.PS2.Compare(*usb)
		if err != nil {
			return err
		}
		for _, item := range compared {
			if !item.Installed {
				selected = append(selected, item.Game)
			}
		}
	} else {
		for _, wanted := range set.Args() {
			found := false
			for _, game := range gamesList {
				if strings.EqualFold(wanted, game.Title) || strings.EqualFold(wanted, game.ID) || strings.EqualFold(wanted, game.ISOFilename) || wanted == game.PublicID {
					selected = append(selected, game)
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("PS2 game %q not found", wanted)
			}
		}
	}
	if len(selected) == 0 {
		return fmt.Errorf("no PS2 games selected")
	}
	ids := make([]string, len(selected))
	for i := range selected {
		ids[i] = selected[i].PublicID
	}
	jobs, err := application.PS2.Enqueue(*usb, ids)
	if err != nil {
		return err
	}
	if *jsonOutput {
		if err := r.printValue(jobs, true); err != nil {
			return err
		}
	} else {
		fmt.Fprintf(r.Out, "Queued %d PS2 game(s) for %s. OPL operations run one at a time.\n", len(jobs), *usb)
	}
	wanted := make(map[string]bool)
	for _, job := range jobs {
		wanted[job.ID] = true
	}
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			for id := range wanted {
				_ = application.PS2.Queue.Cancel(id)
			}
			return ctx.Err()
		case <-ticker.C:
			terminal := true
			failed := false
			for _, job := range application.PS2.Queue.List() {
				if !wanted[job.ID] {
					continue
				}
				if job.State == ps2.StateFailed {
					failed = true
				}
				if job.State != ps2.StateCompleted && job.State != ps2.StateFailed && job.State != ps2.StateCancelled {
					terminal = false
				}
			}
			if terminal {
				if failed {
					return fmt.Errorf("one or more PS2 installations failed")
				}
				if !*jsonOutput {
					fmt.Fprintln(r.Out, "PS2 queue completed.")
				}
				return nil
			}
		}
	}
}

func (r Runner) ps2Queue(application *app.Service, args []string) error {
	set := newFlagSet("ps2 queue", r.Err)
	jsonOutput := set.Bool("json", false, "print JSON")
	if err := set.Parse(args); err != nil {
		return err
	}
	items := application.PS2.Queue.List()
	if *jsonOutput {
		return r.printValue(items, true)
	}
	for _, job := range items {
		fmt.Fprintf(r.Out, "%-14s %-12s %s -> %s\n", job.State, job.Game.ID, job.Game.Title, job.USBID)
	}
	if len(items) == 0 {
		fmt.Fprintln(r.Out, "PS2 queue is empty.")
	}
	return nil
}

func (r Runner) localGames(ctx context.Context, application *app.Service, args []string) error {
	set := newFlagSet("local-games", r.Err)
	directory := set.String("dir", "", "local PS3 game directory (overrides PS3MGR_PS3_GAME_DIR)")
	jsonOutput := set.Bool("json", false, "print JSON")
	if err := set.Parse(args); err != nil {
		return err
	}
	items, err := application.LocalGames(ctx, *directory)
	if err != nil {
		return err
	}
	if *jsonOutput {
		return r.printValue(items, true)
	}
	for _, game := range items {
		fmt.Fprintf(r.Out, "%-12s  %10s  %s\n", fallback(game.ID, "UNKNOWN"), humanBytes(game.Size), game.Title)
	}
	fmt.Fprintf(r.Out, "\n%d games\n", len(items))
	return nil
}

func (r Runner) scan(ctx context.Context, application *app.Service, args []string) error {
	if hasArg(args, "-h") || hasArg(args, "--help") {
		fmt.Fprintln(r.Out, "Usage: ps3mgr scan <CIDR> [--workers N] [--json]")
		return nil
	}
	var cidr string
	workers := 0
	jsonOutput := false
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--json":
			jsonOutput = true
		case args[i] == "--workers" && i+1 < len(args):
			i++
			value, err := strconv.Atoi(args[i])
			if err != nil {
				return fmt.Errorf("invalid workers value")
			}
			workers = value
		case strings.HasPrefix(args[i], "--workers="):
			value, err := strconv.Atoi(strings.TrimPrefix(args[i], "--workers="))
			if err != nil {
				return fmt.Errorf("invalid workers value")
			}
			workers = value
		case strings.HasPrefix(args[i], "-"):
			return fmt.Errorf("unknown scan option %q", args[i])
		case cidr == "":
			cidr = args[i]
		default:
			return fmt.Errorf("unexpected argument %q", args[i])
		}
	}
	if cidr == "" {
		return fmt.Errorf("CIDR is required")
	}
	items, err := application.Scan(ctx, cidr, workers)
	if err != nil {
		return err
	}
	if jsonOutput {
		return r.printValue(items, true)
	}
	for _, console := range items {
		fmt.Fprintf(r.Out, "%s  PS3 detected  %d games\n", console.IP, console.GameCount)
	}
	if len(items) == 0 {
		fmt.Fprintln(r.Out, "No PS3 consoles found.")
	}
	return nil
}

func (r Runner) consoles(application *app.Service, args []string) error {
	set := newFlagSet("consoles", r.Err)
	jsonOutput := set.Bool("json", false, "print JSON")
	if err := set.Parse(args); err != nil {
		return err
	}
	items := application.Consoles()
	if *jsonOutput {
		return r.printValue(items, true)
	}
	for _, console := range items {
		fmt.Fprintf(r.Out, "%s  %d games\n", console.IP, console.GameCount)
	}
	if len(items) == 0 {
		fmt.Fprintln(r.Out, "No consoles discovered in this process.")
	}
	return nil
}

func (r Runner) addConsole(ctx context.Context, application *app.Service, args []string) error {
	set := newFlagSet("add-console", r.Err)
	ip := set.String("ip", "", "known PS3 IPv4 address (required)")
	jsonOutput := set.Bool("json", false, "print JSON")
	if err := set.Parse(args); err != nil {
		return err
	}
	if *ip == "" {
		return fmt.Errorf("--ip is required")
	}
	console, err := application.AddConsole(ctx, *ip)
	if err != nil {
		return err
	}
	if *jsonOutput {
		return r.printValue(console, true)
	}
	fmt.Fprintf(r.Out, "%s  PS3 detected  %d games\n", console.IP, console.GameCount)
	return nil
}

func (r Runner) remoteGames(ctx context.Context, application *app.Service, args []string) error {
	set := newFlagSet("games", r.Err)
	ip := set.String("ip", "", "PS3 IPv4 address (required)")
	remoteDir := set.String("remote-dir", "", "remote games directory")
	jsonOutput := set.Bool("json", false, "print JSON")
	if err := set.Parse(args); err != nil {
		return err
	}
	if *ip == "" {
		return fmt.Errorf("--ip is required")
	}
	items, err := application.RemoteGames(ctx, *ip, *remoteDir)
	if err != nil {
		return err
	}
	if *jsonOutput {
		return r.printValue(items, true)
	}
	printGames(r.Out, items)
	return nil
}

func (r Runner) compare(ctx context.Context, application *app.Service, args []string) error {
	set := newFlagSet("compare", r.Err)
	ip := set.String("ip", "", "PS3 IPv4 address (required)")
	directory := set.String("dir", "", "local game directory")
	jsonOutput := set.Bool("json", false, "print JSON")
	if err := set.Parse(args); err != nil {
		return err
	}
	if *ip == "" {
		return fmt.Errorf("--ip is required")
	}
	if _, err := application.LocalGames(ctx, *directory); err != nil {
		return err
	}
	items, err := application.Compare(ctx, *ip)
	if err != nil {
		return err
	}
	if *jsonOutput {
		return r.printValue(items, true)
	}
	for _, game := range items {
		status := "MISSING"
		if game.Installed {
			status = "INSTALLED"
		}
		fmt.Fprintf(r.Out, "%-10s  %-12s  %s\n", status, fallback(game.ID, "UNKNOWN"), game.Title)
	}
	return nil
}

func (r Runner) install(ctx context.Context, application *app.Service, args []string) error {
	if hasArg(args, "-h") || hasArg(args, "--help") {
		fmt.Fprintln(r.Out, "Usage: ps3mgr install --ip <IP> [--dir PATH] [--stop-on-error] <GAME...>")
		return nil
	}
	var ip, directory string
	stopOnError := false
	var requested []string
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--ip" && i+1 < len(args):
			i++
			ip = args[i]
		case strings.HasPrefix(args[i], "--ip="):
			ip = strings.TrimPrefix(args[i], "--ip=")
		case args[i] == "--dir" && i+1 < len(args):
			i++
			directory = args[i]
		case strings.HasPrefix(args[i], "--dir="):
			directory = strings.TrimPrefix(args[i], "--dir=")
		case args[i] == "--stop-on-error":
			stopOnError = true
		case strings.HasPrefix(args[i], "-"):
			return fmt.Errorf("unknown install option %q", args[i])
		default:
			requested = append(requested, args[i])
		}
	}
	if ip == "" || len(requested) == 0 {
		return fmt.Errorf("--ip and at least one game are required")
	}
	local, err := application.LocalGames(ctx, directory)
	if err != nil {
		return err
	}
	ids := make([]string, 0, len(requested))
	for _, wanted := range requested {
		found := false
		for _, game := range local {
			if strings.EqualFold(wanted, game.Title) || strings.EqualFold(wanted, game.ID) || strings.EqualFold(wanted, games.PublicID(game)) {
				ids = append(ids, games.PublicID(game))
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("local game %q not found", wanted)
		}
	}
	eventStream, unsubscribe := application.Events.Subscribe(128)
	defer unsubscribe()
	created, err := application.Enqueue(ip, ids, stopOnError)
	if err != nil {
		return err
	}
	fmt.Fprintf(r.Out, "Queued %d game(s) for %s. Transfers run one at a time.\n", len(created), ip)
	wantedItems := make(map[string]bool)
	for _, item := range created {
		wantedItems[item.ID] = true
	}
	for {
		select {
		case <-ctx.Done():
			for id := range wantedItems {
				_ = application.Transfers.Cancel(id)
			}
			return ctx.Err()
		case event := <-eventStream:
			item, ok := event.Payload.(domain.Transfer)
			if !ok || !wantedItems[item.ID] {
				continue
			}
			switch event.Type {
			case "queue.item_started":
				fmt.Fprintf(r.Out, "Starting %s\n", item.Game.Title)
			case "queue.progress":
				fmt.Fprintf(r.Out, "\r%-30s %6.2f%%  %s/s", item.Game.Title, item.Percentage, humanBytes(item.Speed))
			case "queue.item_completed":
				fmt.Fprintf(r.Out, "\r%-30s complete                      \n", item.Game.Title)
			case "queue.item_failed", "queue.item_cancelled":
				fmt.Fprintf(r.Out, "\r%-30s %s: %s\n", item.Game.Title, strings.ToLower(string(item.State)), item.Error)
			}
			if allTerminal(application.Transfers.List(), wantedItems) {
				for _, value := range application.Transfers.List() {
					if wantedItems[value.ID] && value.State == domain.QueueFailed {
						return fmt.Errorf("one or more transfers failed")
					}
				}
				return nil
			}
		}
	}
}

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
	logger.Info("[APP] web server ready", "url", "http://"+displayAddress(*listen), "health_url", "http://"+displayAddress(*listen)+"/api/health", "ps2_games_url", "http://"+displayAddress(*listen)+"/ps2-games", "ps3_games_url", "http://"+displayAddress(*listen)+"/ps3-games", "ps4_games_url", "http://"+displayAddress(*listen)+"/ps4-games", "ps5_games_url", "http://"+displayAddress(*listen)+"/ps5-games")

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
	if application.PS4.Content.AdvertiseURL == "" {
		logger.Warn("[PS4] package server is not configured; browsing remains available", "required_env", "PS3MGR_PS4_ADVERTISE_URL", "example", "http://192.168.1.20:8081")
	} else if startErr := application.PS4.Content.Start(); startErr != nil {
		logger.Error("[PS4] package server failed to start; panel remains available", "listen", application.PS4.Content.Listen, "error", startErr)
	} else {
		logger.Info("[PS4] package server ready", "listen", application.PS4.Content.Listen, "advertise_url", application.PS4.Content.AdvertiseURL)
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

func newFlagSet(name string, output io.Writer) *flag.FlagSet {
	set := flag.NewFlagSet(name, flag.ContinueOnError)
	set.SetOutput(output)
	return set
}

func (r Runner) printValue(value any, asJSON bool) error {
	if !asJSON {
		return nil
	}
	encoder := json.NewEncoder(r.Out)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func printGames(output io.Writer, items []domain.Game) {
	for _, game := range items {
		fmt.Fprintf(output, "%-12s  %s\n", fallback(game.ID, "UNKNOWN"), game.Title)
	}
	fmt.Fprintf(output, "\n%d games\n", len(items))
}

func allTerminal(items []domain.Transfer, wanted map[string]bool) bool {
	for _, item := range items {
		if wanted[item.ID] && item.State != domain.QueueCompleted && item.State != domain.QueueFailed && item.State != domain.QueueCancelled {
			return false
		}
	}
	return true
}

func humanBytes(value int64) string {
	if value < 1024 {
		return fmt.Sprintf("%d B", value)
	}
	units := []string{"KB", "MB", "GB", "TB"}
	size := float64(value)
	for _, unit := range units {
		size /= 1024
		if size < 1024 || unit == "TB" {
			return fmt.Sprintf("%.1f %s", size, unit)
		}
	}
	return fmt.Sprintf("%d B", value)
}

func displayAddress(address string) string {
	if strings.HasPrefix(address, ":") {
		return "127.0.0.1" + address
	}
	return address
}

func fallback(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func hasArg(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
