package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"

	"github.com/cindocode/trenchbot/internal/backtest"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg, err := backtest.LoadBacktestConfig()
	if err != nil {
		log.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	startDate, err := cfg.ParsedStartDate()
	if err != nil {
		log.Error("invalid start date", "error", err)
		os.Exit(1)
	}
	endDate, err := cfg.ParsedEndDate()
	if err != nil {
		log.Error("invalid end date", "error", err)
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

	// Query tokens from Postgres.
	log.Info("querying tokens", "start", cfg.StartDate, "end", cfg.EndDate)
	stored, err := store.QueryTokens(ctx, startDate, endDate)
	if err != nil {
		log.Error("failed to query tokens", "error", err)
		os.Exit(1)
	}
	if len(stored) == 0 {
		log.Error("no tokens found in date range — run the watcher first to collect data")
		os.Exit(1)
	}
	log.Info("loaded tokens", "count", len(stored))

	// Convert to synthetic tokens.
	tokens, simStart := backtest.ConvertToSyntheticTokens(stored)
	log.Info("converted tokens", "count", len(tokens), "sim_start", simStart)

	// Run backtest.
	engine := backtest.NewEngine(cfg, log)
	report := engine.Run(ctx, tokens, simStart)

	// Print report.
	fmt.Println(report.String())

	// Write report JSON.
	reportJSON, err := report.JSON()
	if err != nil {
		log.Error("failed to marshal report", "error", err)
		os.Exit(1)
	}
	if err := os.WriteFile("simulation-report.json", reportJSON, 0o644); err != nil {
		log.Error("failed to write report", "error", err)
		os.Exit(1)
	}
	log.Info("report written to simulation-report.json")
}
