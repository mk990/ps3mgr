package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"ps3mgr/internal/app"
	"ps3mgr/internal/domain"
	"ps3mgr/internal/games"
)

func (r Runner) ps5(ctx context.Context, application *app.Service, args []string) error {
	if len(args) == 0 || hasArg(args, "-h") || hasArg(args, "--help") || args[0] == "help" {
		fmt.Fprint(r.Out, `PlayStation 5 / etaHEN ShadowMountPlus

Usage:
  ps3mgr ps5 local-games [--dir PATH] [--json]
  ps3mgr ps5 scan CIDR [--workers N] [--json]
  ps3mgr ps5 consoles [--json]
  ps3mgr ps5 add-console --ip IP [--json]
  ps3mgr ps5 games --ip IP [--json]
  ps3mgr ps5 compare --ip IP [--dir PATH] [--json]
  ps3mgr ps5 install --ip IP [--dir PATH] [--all] GAME...
  ps3mgr ps5 queue [--json]

The FTP destination is /data/etaHEN/games by default. The API never accepts
an arbitrary remote destination path.
`)
		return nil
	}
	switch args[0] {
	case "local-games":
		return r.ps5LocalGames(ctx, application, args[1:])
	case "scan":
		return r.ps5Scan(ctx, application, args[1:])
	case "consoles":
		return r.ps5Consoles(application, args[1:])
	case "add-console":
		return r.ps5AddConsole(ctx, application, args[1:])
	case "games":
		return r.ps5RemoteGames(ctx, application, args[1:])
	case "compare":
		return r.ps5Compare(ctx, application, args[1:])
	case "install":
		return r.ps5Install(ctx, application, args[1:])
	case "queue":
		return r.ps5Queue(application, args[1:])
	default:
		return fmt.Errorf("unknown PS5 command %q", args[0])
	}
}

func (r Runner) ps5LocalGames(ctx context.Context, application *app.Service, args []string) error {
	set := newFlagSet("ps5 local-games", r.Err)
	directory := set.String("dir", "", "PS5 game directory")
	asJSON := set.Bool("json", false, "print JSON")
	if err := set.Parse(args); err != nil {
		return err
	}
	items, err := application.PS5.LocalGames(ctx, *directory)
	if err != nil {
		return err
	}
	if *asJSON {
		return r.printValue(items, true)
	}
	printPS5Games(r, items)
	return nil
}

func (r Runner) ps5Scan(ctx context.Context, application *app.Service, args []string) error {
	set := newFlagSet("ps5 scan", r.Err)
	workers := set.Int("workers", 0, "parallel probes")
	asJSON := set.Bool("json", false, "print JSON")
	if err := set.Parse(args); err != nil {
		return err
	}
	if set.NArg() != 1 {
		return fmt.Errorf("one private CIDR is required")
	}
	items, err := application.PS5.Scan(ctx, set.Arg(0), *workers)
	if err != nil {
		return err
	}
	if *asJSON {
		return r.printValue(items, true)
	}
	for _, console := range items {
		fmt.Fprintf(r.Out, "%s:%d  PS5/etaHEN detected  %d games\n", console.IP, console.FTPPort, console.GameCount)
	}
	if len(items) == 0 {
		fmt.Fprintln(r.Out, "No PS5 etaHEN consoles found on FTP port 2121.")
	}
	return nil
}

func (r Runner) ps5Consoles(application *app.Service, args []string) error {
	set := newFlagSet("ps5 consoles", r.Err)
	asJSON := set.Bool("json", false, "print JSON")
	if err := set.Parse(args); err != nil {
		return err
	}
	items := application.PS5.Consoles()
	if *asJSON {
		return r.printValue(items, true)
	}
	for _, console := range items {
		fmt.Fprintf(r.Out, "%s:%d  %d ShadowMountPlus games\n", console.IP, console.FTPPort, console.GameCount)
	}
	if len(items) == 0 {
		fmt.Fprintln(r.Out, "No PS5 consoles discovered in this process.")
	}
	return nil
}

func (r Runner) ps5AddConsole(ctx context.Context, application *app.Service, args []string) error {
	set := newFlagSet("ps5 add-console", r.Err)
	ip := set.String("ip", "", "known PS5 IPv4 address")
	asJSON := set.Bool("json", false, "print JSON")
	if err := set.Parse(args); err != nil {
		return err
	}
	if *ip == "" {
		return fmt.Errorf("--ip is required")
	}
	console, err := application.PS5.AddConsole(ctx, *ip)
	if err != nil {
		return err
	}
	if *asJSON {
		return r.printValue(console, true)
	}
	fmt.Fprintf(r.Out, "%s:%d  PS5/etaHEN detected  %d games\n", console.IP, console.FTPPort, console.GameCount)
	return nil
}

func (r Runner) ps5RemoteGames(ctx context.Context, application *app.Service, args []string) error {
	set := newFlagSet("ps5 games", r.Err)
	ip := set.String("ip", "", "PS5 IPv4 address")
	asJSON := set.Bool("json", false, "print JSON")
	if err := set.Parse(args); err != nil {
		return err
	}
	if *ip == "" {
		return fmt.Errorf("--ip is required")
	}
	items, err := application.PS5.RemoteGames(ctx, *ip)
	if err != nil {
		return err
	}
	if *asJSON {
		return r.printValue(items, true)
	}
	printPS5Games(r, items)
	return nil
}

func (r Runner) ps5Compare(ctx context.Context, application *app.Service, args []string) error {
	set := newFlagSet("ps5 compare", r.Err)
	ip := set.String("ip", "", "PS5 IPv4 address")
	directory := set.String("dir", "", "PS5 game directory")
	asJSON := set.Bool("json", false, "print JSON")
	if err := set.Parse(args); err != nil {
		return err
	}
	if *ip == "" {
		return fmt.Errorf("--ip is required")
	}
	if _, err := application.PS5.LocalGames(ctx, *directory); err != nil {
		return err
	}
	items, err := application.PS5.Compare(ctx, *ip)
	if err != nil {
		return err
	}
	if *asJSON {
		return r.printValue(items, true)
	}
	for _, game := range items {
		status := "MISSING"
		if game.Installed {
			status = "INSTALLED"
		}
		fmt.Fprintf(r.Out, "%-10s %-12s %-8s %s\n", status, fallback(game.ID, "UNKNOWN"), game.Format, game.Title)
	}
	return nil
}

func (r Runner) ps5Install(ctx context.Context, application *app.Service, args []string) error {
	set := newFlagSet("ps5 install", r.Err)
	ip := set.String("ip", "", "PS5 IPv4 address")
	directory := set.String("dir", "", "PS5 game directory")
	all := set.Bool("all", false, "install every local game")
	stopOnError := set.Bool("stop-on-error", false, "cancel remaining jobs after a failure")
	asJSON := set.Bool("json", false, "print queued jobs as JSON")
	if err := set.Parse(args); err != nil {
		return err
	}
	if *ip == "" {
		return fmt.Errorf("--ip is required")
	}
	local, err := application.PS5.LocalGames(ctx, *directory)
	if err != nil {
		return err
	}
	requested := set.Args()
	if !*all && len(requested) == 0 {
		return fmt.Errorf("at least one game or --all is required")
	}
	ids := make([]string, 0, len(local))
	if *all {
		for _, game := range local {
			ids = append(ids, games.PublicID(game))
		}
	} else {
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
				return fmt.Errorf("local PS5 game %q not found", wanted)
			}
		}
	}
	created, err := application.PS5.Enqueue(*ip, ids, *stopOnError)
	if err != nil {
		return err
	}
	if *asJSON {
		if err := r.printValue(created, true); err != nil {
			return err
		}
	} else {
		fmt.Fprintf(r.Out, "Queued %d PS5 game(s) for %s:%d -> %s.\n", len(created), *ip, application.PS5.FTP.Port, application.PS5.RemoteDir)
	}
	wanted := make(map[string]bool)
	for _, item := range created {
		wanted[item.ID] = true
	}
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			for id := range wanted {
				_ = application.PS5.Transfers.Cancel(id)
			}
			return ctx.Err()
		case <-ticker.C:
			items := application.PS5.Transfers.List()
			if !allTerminal(items, wanted) {
				continue
			}
			for _, item := range items {
				if wanted[item.ID] && item.State == domain.QueueFailed {
					return fmt.Errorf("one or more PS5 transfers failed")
				}
			}
			if !*asJSON {
				fmt.Fprintln(r.Out, "PS5 queue completed.")
			}
			return nil
		}
	}
}

func (r Runner) ps5Queue(application *app.Service, args []string) error {
	set := newFlagSet("ps5 queue", r.Err)
	asJSON := set.Bool("json", false, "print JSON")
	if err := set.Parse(args); err != nil {
		return err
	}
	items := application.PS5.Transfers.List()
	if *asJSON {
		return r.printValue(items, true)
	}
	for _, item := range items {
		fmt.Fprintf(r.Out, "%-14s %-12s %6.2f%% %s -> %s:%d\n", item.State, fallback(item.Game.ID, "UNKNOWN"), item.Percentage, item.Game.Title, item.ConsoleIP, application.PS5.FTP.Port)
	}
	if len(items) == 0 {
		fmt.Fprintln(r.Out, "PS5 queue is empty.")
	}
	return nil
}

func printPS5Games(r Runner, items []domain.Game) {
	for _, game := range items {
		fmt.Fprintf(r.Out, "%-12s %-8s %10s %s\n", fallback(game.ID, "UNKNOWN"), game.Format, humanBytes(game.Size), game.Title)
	}
	fmt.Fprintf(r.Out, "\n%d PS5 games\n", len(items))
}
