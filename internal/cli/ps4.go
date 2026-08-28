package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"ps3mgr/internal/app"
	"ps3mgr/internal/ps4"
)

func (r Runner) ps4(ctx context.Context, application *app.Service, args []string) error {
	if len(args) == 0 || hasArg(args, "-h") || hasArg(args, "--help") || args[0] == "help" {
		fmt.Fprint(r.Out, `PlayStation 4 / Remote Package Installer

Usage:
  ps3mgr ps4 local-games [--dir PATH] [--json]
  ps3mgr ps4 scan CIDR [--workers N] [--json]
  ps3mgr ps4 consoles [--json]
  ps3mgr ps4 add-console --ip IP [--json]
  ps3mgr ps4 compare --ip IP [--dir PATH] [--json]
  ps3mgr ps4 install --ip IP [--dir PATH] [--all] PACKAGE...
  ps3mgr ps4 queue [--json]

The PS4 must be running flatZ Remote Package Installer on port 12800.
PS3MGR_PS4_ADVERTISE_URL must be reachable by the PS4 for installs.
`)
		return nil
	}
	switch args[0] {
	case "local-games", "games":
		return r.ps4LocalGames(ctx, application, args[1:])
	case "scan":
		return r.ps4Scan(ctx, application, args[1:])
	case "consoles":
		return r.ps4Consoles(application, args[1:])
	case "add-console":
		return r.ps4AddConsole(ctx, application, args[1:])
	case "compare":
		return r.ps4Compare(ctx, application, args[1:])
	case "install":
		return r.ps4Install(ctx, application, args[1:])
	case "queue":
		return r.ps4Queue(application, args[1:])
	default:
		return fmt.Errorf("unknown PS4 command %q", args[0])
	}
}

func (r Runner) ps4LocalGames(ctx context.Context, application *app.Service, args []string) error {
	set := newFlagSet("ps4 local-games", r.Err)
	directory := set.String("dir", "", "PS4 PKG directory")
	asJSON := set.Bool("json", false, "print JSON")
	if err := set.Parse(args); err != nil {
		return err
	}
	items, err := application.PS4.LocalPackages(ctx, *directory)
	if err != nil {
		return err
	}
	if *asJSON {
		return r.printValue(items, true)
	}
	for _, pkg := range items {
		fmt.Fprintf(r.Out, "%-12s %-10s %10s  %s", fallback(pkg.TitleID, "UNKNOWN"), pkg.Format, humanBytes(pkg.Size), pkg.Title)
		if len(pkg.Parts) > 1 {
			fmt.Fprintf(r.Out, " (%d parts)", len(pkg.Parts))
		}
		fmt.Fprintln(r.Out)
	}
	if len(items) == 0 {
		fmt.Fprintln(r.Out, "No valid PS4 PKG files found.")
	}
	return nil
}

func (r Runner) ps4Scan(ctx context.Context, application *app.Service, args []string) error {
	set := newFlagSet("ps4 scan", r.Err)
	workers := set.Int("workers", 0, "parallel probes")
	asJSON := set.Bool("json", false, "print JSON")
	if err := set.Parse(args); err != nil {
		return err
	}
	if set.NArg() != 1 {
		return fmt.Errorf("one private CIDR is required")
	}
	items, err := application.PS4.Scan(ctx, set.Arg(0), *workers)
	if err != nil {
		return err
	}
	if *asJSON {
		return r.printValue(items, true)
	}
	for _, console := range items {
		fmt.Fprintf(r.Out, "%s:%d  PS4 Remote Package Installer detected\n", console.IP, console.APIPort)
	}
	if len(items) == 0 {
		fmt.Fprintf(r.Out, "No PS4 Remote Package Installer found on port %d.\n", application.PS4.RPI.Port)
	}
	return nil
}

func (r Runner) ps4Consoles(application *app.Service, args []string) error {
	set := newFlagSet("ps4 consoles", r.Err)
	asJSON := set.Bool("json", false, "print JSON")
	if err := set.Parse(args); err != nil {
		return err
	}
	items := application.PS4.Consoles()
	if *asJSON {
		return r.printValue(items, true)
	}
	for _, console := range items {
		fmt.Fprintf(r.Out, "%s:%d  Remote Package Installer\n", console.IP, console.APIPort)
	}
	if len(items) == 0 {
		fmt.Fprintln(r.Out, "No PS4 consoles discovered in this process.")
	}
	return nil
}

func (r Runner) ps4AddConsole(ctx context.Context, application *app.Service, args []string) error {
	set := newFlagSet("ps4 add-console", r.Err)
	ip := set.String("ip", "", "known PS4 IPv4 address")
	asJSON := set.Bool("json", false, "print JSON")
	if err := set.Parse(args); err != nil {
		return err
	}
	if *ip == "" {
		return fmt.Errorf("--ip is required")
	}
	console, err := application.PS4.AddConsole(ctx, *ip)
	if err != nil {
		return err
	}
	if *asJSON {
		return r.printValue(console, true)
	}
	fmt.Fprintf(r.Out, "%s:%d  PS4 Remote Package Installer detected\n", console.IP, console.APIPort)
	return nil
}

func (r Runner) ps4Compare(ctx context.Context, application *app.Service, args []string) error {
	set := newFlagSet("ps4 compare", r.Err)
	ip := set.String("ip", "", "PS4 IPv4 address")
	directory := set.String("dir", "", "PS4 PKG directory")
	asJSON := set.Bool("json", false, "print JSON")
	if err := set.Parse(args); err != nil {
		return err
	}
	if *ip == "" {
		return fmt.Errorf("--ip is required")
	}
	if _, err := application.PS4.LocalPackages(ctx, *directory); err != nil {
		return err
	}
	items, err := application.PS4.Compare(ctx, *ip)
	if err != nil {
		return err
	}
	if *asJSON {
		return r.printValue(items, true)
	}
	for _, pkg := range items {
		status := "MISSING"
		if pkg.Installed {
			status = "INSTALLED"
		}
		fmt.Fprintf(r.Out, "%-10s %-12s %-10s %s\n", status, fallback(pkg.TitleID, "UNKNOWN"), pkg.Format, pkg.Title)
	}
	return nil
}

func (r Runner) ps4Install(ctx context.Context, application *app.Service, args []string) error {
	set := newFlagSet("ps4 install", r.Err)
	ip := set.String("ip", "", "PS4 IPv4 address")
	directory := set.String("dir", "", "PS4 PKG directory")
	all := set.Bool("all", false, "install every local package")
	stopOnError := set.Bool("stop-on-error", false, "cancel remaining jobs after a failure")
	asJSON := set.Bool("json", false, "print queued jobs as JSON")
	if err := set.Parse(args); err != nil {
		return err
	}
	if *ip == "" {
		return fmt.Errorf("--ip is required")
	}
	packages, err := application.PS4.LocalPackages(ctx, *directory)
	if err != nil {
		return err
	}
	requested := set.Args()
	if !*all && len(requested) == 0 {
		return fmt.Errorf("at least one package or --all is required")
	}
	ids := make([]string, 0, len(packages))
	if *all {
		for _, pkg := range packages {
			ids = append(ids, pkg.ID)
		}
	} else {
		for _, wanted := range requested {
			found := false
			for _, pkg := range packages {
				if strings.EqualFold(wanted, pkg.Title) || strings.EqualFold(wanted, pkg.TitleID) || strings.EqualFold(wanted, pkg.ContentID) || strings.EqualFold(wanted, pkg.ID) {
					ids = append(ids, pkg.ID)
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("local PS4 package %q not found", wanted)
			}
		}
	}
	created, err := application.PS4.Enqueue(*ip, ids, *stopOnError)
	if err != nil {
		return err
	}
	if *asJSON {
		if err := r.printValue(created, true); err != nil {
			return err
		}
	} else {
		fmt.Fprintf(r.Out, "Queued %d PS4 package(s) for %s:%d. Packages are served at %s.\n", len(created), *ip, application.PS4.RPI.Port, application.PS4.Content.AdvertiseURL)
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
				_ = application.PS4.Queue.Cancel(id)
			}
			return ctx.Err()
		case <-ticker.C:
			done := true
			failed := false
			for _, item := range application.PS4.Queue.List() {
				if !wanted[item.ID] {
					continue
				}
				switch item.State {
				case ps4.StateCompleted:
				case ps4.StateFailed, ps4.StateCancelled:
					failed = true
				default:
					done = false
				}
			}
			if done {
				if failed {
					return fmt.Errorf("one or more PS4 installs failed; inspect ps3mgr ps4 queue")
				}
				return nil
			}
		}
	}
}

func (r Runner) ps4Queue(application *app.Service, args []string) error {
	set := newFlagSet("ps4 queue", r.Err)
	asJSON := set.Bool("json", false, "print JSON")
	if err := set.Parse(args); err != nil {
		return err
	}
	items := application.PS4.Queue.List()
	if *asJSON {
		return r.printValue(items, true)
	}
	for _, item := range items {
		fmt.Fprintf(r.Out, "%-22s %-20s %6.1f%%  %s\n", item.ID, item.State, item.Percentage, item.Package.Title)
		if item.Error != "" {
			fmt.Fprintf(r.Out, "  error: %s\n", item.Error)
		}
	}
	if len(items) == 0 {
		fmt.Fprintln(r.Out, "The PS4 queue is empty.")
	}
	return nil
}
