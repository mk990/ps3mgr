package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"ps3mgr/internal/app"
	"ps3mgr/internal/ps2"
)

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
  ps3mgr ps2 fpkg        [--all] <GAME...>
  ps3mgr ps2 fpkg-status [--json]
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
	case "fpkg":
		return r.ps2FPKG(ctx, application, args[1:])
	case "fpkg-status":
		return r.ps2FPKGStatus(application, args[1:])
	default:
		return fmt.Errorf("unknown PS2 command %q", args[0])
	}
}

func (r Runner) ps2FPKGStatus(application *app.Service, args []string) error {
	set := newFlagSet("ps2 fpkg-status", r.Err)
	jsonOutput := set.Bool("json", false, "print JSON")
	if err := set.Parse(args); err != nil {
		return err
	}
	status := application.PS2.FPKG.Status()
	if *jsonOutput {
		return r.printValue(status, true)
	}
	fmt.Fprintf(r.Out, "PS2 FPKG converter ready: %t\n", status.Ready)
	fmt.Fprintf(r.Out, "Converter: %s\nEmulator: %s\nOutput: %s\n", status.Converter, status.Emulator, status.OutputDir)
	if status.Message != "" {
		fmt.Fprintf(r.Out, "Problem: %s\n", status.Message)
	}
	return nil
}

func (r Runner) ps2FPKG(ctx context.Context, application *app.Service, args []string) error {
	set := newFlagSet("ps2 fpkg", r.Err)
	directory := set.String("dir", "", "PS2 ISO directory")
	all := set.Bool("all", false, "convert every PS2 game with a known serial")
	jsonOutput := set.Bool("json", false, "print JSON")
	if err := set.Parse(args); err != nil {
		return err
	}
	gamesList, err := application.PS2.LocalGames(ctx, *directory)
	if err != nil {
		return err
	}
	selected := make([]ps2.Game, 0)
	if *all {
		for _, game := range gamesList {
			if game.ID != "unknown" {
				selected = append(selected, game)
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
		return fmt.Errorf("no PS2 games selected for FPKG conversion")
	}
	ids := make([]string, len(selected))
	for i := range selected {
		ids[i] = selected[i].PublicID
	}
	jobs, err := application.PS2.EnqueueFPKG(ids)
	if err != nil {
		return err
	}
	if *jsonOutput {
		if err := r.printValue(jobs, true); err != nil {
			return err
		}
	} else {
		fmt.Fprintf(r.Out, "Queued %d PS2 game(s) for PS4 FPKG conversion.\n", len(jobs))
	}
	wanted := make(map[string]bool, len(jobs))
	for _, job := range jobs {
		wanted[job.ID] = true
	}
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			for id := range wanted {
				_ = application.PS2.FPKG.Cancel(id)
			}
			return ctx.Err()
		case <-ticker.C:
			terminal, failed := true, false
			for _, job := range application.PS2.FPKG.List() {
				if !wanted[job.ID] {
					continue
				}
				if job.State == ps2.FPKGFailed {
					failed = true
				}
				if job.State != ps2.FPKGCompleted && job.State != ps2.FPKGFailed && job.State != ps2.FPKGCancelled {
					terminal = false
				}
			}
			if terminal {
				if failed {
					return fmt.Errorf("one or more PS2 FPKG conversions failed")
				}
				if !*jsonOutput {
					for _, job := range application.PS2.FPKG.List() {
						if wanted[job.ID] && job.OutputPath != "" {
							fmt.Fprintf(r.Out, "Created %s\n", job.OutputPath)
						}
					}
				}
				return nil
			}
		}
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
