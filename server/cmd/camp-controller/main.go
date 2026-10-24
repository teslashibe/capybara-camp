package main

import (
	"context"
	"encoding/base64"
	"errors"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/teslashibe/capybara-camp/server/internal/camp"
	"github.com/teslashibe/capybara-camp/server/internal/config"
	"github.com/teslashibe/capybara-camp/server/internal/database"
	"github.com/teslashibe/capybara-camp/server/internal/provisioner"
)

func main() {
	if err := run(); err != nil && !errors.Is(err, context.Canceled) {
		log.Print(err)
		os.Exit(1)
	}
}
func run() error {
	cfg, err := config.LoadController()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	pool, err := database.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err = database.Ready(ctx, pool); err != nil {
		return err
	}
	key, _ := base64.StdEncoding.Strict().DecodeString(cfg.ProviderKey)
	store, err := camp.NewWithProviderKey(pool, "controller-has-no-human-receipt-key", key)
	if err != nil {
		return err
	}
	kube, err := provisioner.NewKubeClient(cfg)
	if err != nil {
		return err
	}
	return provisioner.New(store, kube, cfg).Run(ctx, func(error) { log.Print("Runner reconciliation will retry") })
}
