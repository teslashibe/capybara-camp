package main

import (
	"context"
	"log"
	"os"
	"time"

	"github.com/teslashibe/capybara-camp/server/internal/config"
	"github.com/teslashibe/capybara-camp/server/internal/database"
)

func main() {
	cfg, err := config.Load(true)
	if err != nil {
		log.Print(err)
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if database.Migrate(ctx, cfg.DatabaseURL) != nil {
		log.Print("Migration failed; inspect the schema before retrying")
		os.Exit(1)
	}
	log.Print("Camp schema ready")
}
