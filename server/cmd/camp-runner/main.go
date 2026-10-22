package main

import (
	"context"
	"errors"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/teslashibe/capybara-camp/server/internal/config"
	"github.com/teslashibe/capybara-camp/server/internal/runner"
)

func main() {
	if err := run(); err != nil && !errors.Is(err, context.Canceled) {
		log.Print(err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) > 1 {
		if len(os.Args) != 3 || os.Args[1] != "git-credential" {
			return errors.New("Unsupported runner operation")
		}
		return runner.GitCredential(os.Args[2], config.GitCredentialFile(), os.Stdin, os.Stdout)
	}
	cfg, err := config.LoadRunner()
	if err != nil {
		return err
	}
	w, err := runner.New(cfg, nil)
	if err != nil {
		return err
	}
	defer w.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return w.Run(ctx)
}
