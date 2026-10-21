package main

import (
	"context"
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
	a := &httpapi.API{Pool: pool, Auth: auth.New(pool, cfg.AuthSecret, cfg.PublicURL, resend.New(cfg.ResendKey, cfg.EmailFrom)), Camps: camp.New(pool, cfg.AuthSecret), Origin: cfg.PublicURL}
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
