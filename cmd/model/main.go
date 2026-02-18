package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	_ "github.com/lib/pq"

	"github.com/cindocode/trenchbot/internal/capital"
	"github.com/cindocode/trenchbot/internal/config"
)

func main() {
	shadow := flag.Bool("shadow", false, "compare against shadow run data from Postgres")
	hours := flag.Float64("hours", 2, "hours of shadow data to analyze")
	tokensPerDay := flag.Int("tokens-per-day", 10_000, "PumpFun tokens launched per day")
	filterRate := flag.Float64("filter-rate", 0.002, "estimated filter pass rate (0.002 = 0.2%)")
	winRate := flag.Float64("win-rate", 0.55, "estimated win rate")
	avgWin := flag.Float64("avg-win", 30, "average winning trade PnL%")
	avgLoss := flag.Float64("avg-loss", 25, "average losing trade PnL%")
	avgHold := flag.Float64("avg-hold", 15, "average hold time in minutes")
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	// Override defaults with CLI flags.
	inputs := capital.DefaultModelInputs(cfg)
	inputs.TokensPerDay = *tokensPerDay
	inputs.FilterPassRate = *filterRate
	inputs.WinRate = *winRate
	inputs.AvgWinPct = *avgWin
	inputs.AvgLossPct = *avgLoss
	inputs.AvgHoldMinutes = *avgHold

	if !*shadow {
		// Pure theoretical model.
		out := capital.ComputeModel(inputs)
		fmt.Print(out.String())
		return
	}

	// Shadow comparison mode — needs Postgres.
	dbURL := cfg.DatabaseURL
	if dbURL == "" {
		dbURL = os.Getenv("DATABASE_URL")
	}
	if dbURL == "" {
		log.Fatal("DATABASE_URL required for --shadow mode")
	}

	db, err := sql.Open("postgres", dbURL)
	if err != nil {
		log.Fatalf("connect to postgres: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	since := time.Now().Add(-time.Duration(*hours * float64(time.Hour)))

	// Token flow counters: query from token_observations as proxy.
	var tokensSeen, tokensPassed int
	err = db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM token_observations
		WHERE created_at >= $1
	`, since).Scan(&tokensPassed)
	if err != nil {
		log.Printf("warning: could not query token observations: %v", err)
	}
	// Estimate tokens seen from pass rate and count.
	// If we have a known filter rate, back-calculate; otherwise use default.
	if tokensPassed > 0 && *filterRate > 0 {
		tokensSeen = int(float64(tokensPassed) / *filterRate)
	}

	shadowSummary, err := capital.AnalyzeShadowRun(ctx, db, since, tokensSeen, tokensPassed)
	if err != nil {
		log.Fatalf("analyze shadow run: %v", err)
	}

	report := capital.CompareToModel(shadowSummary, cfg)
	fmt.Print(report.String())
}
