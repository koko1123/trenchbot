package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"

	"github.com/cindocode/trenchbot/internal/backtest"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg, err := backtest.LoadWatcherConfig()
	if err != nil {
		log.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	store, err := backtest.NewStore(cfg.DatabaseURL)
	if err != nil {
		log.Error("failed to connect to database", "error", err)
		os.Exit(1)
	}
	defer store.Close()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	if err := store.CreateTables(ctx); err != nil {
		log.Error("failed to create tables", "error", err)
		os.Exit(1)
	}

	client := backtest.NewGeckoClient(log)

	if err := backtest.RunWatcher(ctx, cfg, store, client, log); err != nil {
		log.Error("watcher error", "error", err)
		os.Exit(1)
	}
}
