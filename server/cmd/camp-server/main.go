package main

import (
	"context"
	"encoding/base64"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/teslashibe/capybara-camp/server/internal/auth"
	"github.com/teslashibe/capybara-camp/server/internal/camp"
	"github.com/teslashibe/capybara-camp/server/internal/config"
	"github.com/teslashibe/capybara-camp/server/internal/database"
	"github.com/teslashibe/capybara-camp/server/internal/gitprovider"
	"github.com/teslashibe/capybara-camp/server/internal/httpapi"
	"github.com/teslashibe/magiclink-auth-go/resend"
)

func main() {
	if err := run(); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}
func run() error {
	cfg, err := config.Load(false)
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	startup, stop := context.WithTimeout(ctx, 10*time.Second)
	pool, err := database.Open(startup, cfg.DatabaseURL)
	if err == nil {
		err = database.Ready(startup, pool)
	}
	stop()
	if pool != nil {
		defer pool.Close()
	}
	if err != nil {
		return err
	}
	camps := camp.New(pool, cfg.AuthSecret)
	if cfg.ProviderEncryptionKey != "" {
		key, _ := base64.StdEncoding.Strict().DecodeString(cfg.ProviderEncryptionKey)
		camps, err = camp.NewWithProviderKey(pool, cfg.AuthSecret, key)
		if err != nil {
			return err
		}
	}
	a := &httpapi.API{Pool: pool, Auth: auth.New(pool, cfg.AuthSecret, cfg.PublicURL, resend.New(cfg.ResendKey, cfg.EmailFrom)), Camps: camps, Origin: cfg.PublicURL}
	gitDone := make(chan struct{})
	if cfg.GitHub.Enabled() {
		a.GitWebhookSecret = cfg.GitHub.WebhookSecret
		provider, e := gitprovider.New(cfg.GitHub, cfg.PublicURL, nil)
		if e != nil {
			return e
		}
		key, _ := base64.StdEncoding.Strict().DecodeString(cfg.GitHub.EncryptionKey)
		a.Git, e = camp.NewGitStore(camps, provider, key)
		if e != nil {
			return e
		}
		go func() {
			defer close(gitDone)
			tick := time.NewTicker(30 * time.Second)
			defer tick.Stop()
			for {
				work, stop := context.WithTimeout(ctx, 15*time.Second)
				if err := a.Git.Reap(work); err != nil && ctx.Err() == nil {
					log.Print("Git authorization cleanup will retry")
				}
				stop()
				select {
				case <-ctx.Done():
					return
				case <-tick.C:
				}
			}
		}()
	} else {
		close(gitDone)
	}
	defer func() { cancel(); <-gitDone }()
	maintenanceDone := make(chan struct{})
	go func() {
		defer close(maintenanceDone)
		tick := time.NewTicker(10 * time.Second)
		defer tick.Stop()
		for {
			work, stop := context.WithTimeout(ctx, 5*time.Second)
			if err := a.Camps.ReapRequests(work); err != nil && ctx.Err() == nil {
				log.Print("Request recovery will retry")
			}
			if err := a.Camps.ReapProviderLogins(work); err != nil && ctx.Err() == nil {
				log.Print("Provider login expiry will retry")
			}
			stop()
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
			}
		}
	}()
	defer func() { cancel(); <-maintenanceDone }()
	srv := &http.Server{Addr: cfg.Address, Handler: a.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10, BaseContext: func(net.Listener) context.Context { return ctx }}
	done := make(chan error, 1)
	go func() { done <- srv.ListenAndServe() }()
	select {
	case err = <-done:
		if err == http.ErrServerClosed {
			return nil
		}
		return err
	case <-ctx.Done():
	}
	shutdown, finish := context.WithTimeout(context.Background(), 10*time.Second)
	defer finish()
	return srv.Shutdown(shutdown)
}
