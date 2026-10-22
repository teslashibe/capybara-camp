// camp-local is the desktop app's bundled JSON/stdio adapter. It opens provider
// stores read-only and does not use the shared Camp database or credentials.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/teslashibe/capybara-camp/server/internal/config"
	"github.com/teslashibe/capybara-camp/server/internal/localcli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	cfg, err := config.LoadLocal()
	if err == nil && len(os.Args) != 2 {
		err = fmt.Errorf("Choose a local helper action")
	}
	var result any
	if err == nil {
		app := localcli.New(cfg)
		if os.Args[1] == "codex-activity-watch" {
			err = app.WatchCodex(ctx, os.Stdin, os.Stdout)
			if err == nil {
				return
			}
		} else {
			result, err = app.Run(ctx, os.Args[1], os.Stdin)
		}
	}
	if err != nil {
		_ = json.NewEncoder(os.Stdout).Encode(map[string]string{"type": "error", "message": err.Error()})
		os.Exit(1)
	}
	if err = json.NewEncoder(os.Stdout).Encode(result); err != nil {
		os.Exit(1)
	}
}
