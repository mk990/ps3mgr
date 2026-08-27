package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"ps3mgr/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit((cli.Runner{Out: os.Stdout, Err: os.Stderr}).Run(ctx, os.Args[1:]))
}
