package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"ps3mgr/internal/app"
	"ps3mgr/internal/config"
	"ps3mgr/internal/domain"
	"ps3mgr/internal/games"
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
	case "pull":
		commandErr = r.pull(ctx, application, args[1:])
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
  pull --ip <IP> <GAME...>
                         Pull games from a PS3 into the local library
  serve                 Start the web panel (default 127.0.0.1:8080)
  version               Print the version

Use "ps3mgr <command> --help" for command-specific options.
`)
}

func (r Runner) pull(ctx context.Context, application *app.Service, args []string) error {
	set := newFlagSet("pull", r.Err)
	ip := set.String("ip", "", "PS3 IPv4 address")
	stopOnError := set.Bool("stop-on-error", false, "cancel remaining jobs after a failure")
	asJSON := set.Bool("json", false, "print queued transfers as JSON")
	if err := set.Parse(args); err != nil {
		return err
	}
	if *ip == "" || set.NArg() == 0 {
		return fmt.Errorf("--ip and at least one game are required")
	}
	items, err := application.EnqueuePull(ctx, *ip, set.Args(), *stopOnError)
	if err != nil {
		return err
	}
	if *asJSON {
		return r.printValue(items, true)
	}
	fmt.Fprintf(r.Out, "Queued %d PS3 pull(s) from %s into %s.\n", len(items), *ip, application.Config.PS3GameDir)
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
