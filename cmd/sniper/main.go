package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/cindocode/trenchbot/internal/clock"
	"github.com/cindocode/trenchbot/internal/config"
	"github.com/cindocode/trenchbot/internal/executor"
	"github.com/cindocode/trenchbot/internal/filter"
	"github.com/cindocode/trenchbot/internal/monitor"
	"github.com/cindocode/trenchbot/internal/notify"
	"github.com/cindocode/trenchbot/internal/reporter"
	"github.com/cindocode/trenchbot/internal/risk"
	"github.com/cindocode/trenchbot/internal/scanner"
	"github.com/cindocode/trenchbot/internal/state"
	bnbclient "github.com/cindocode/trenchbot/pkg/bnb"
	solanaclient "github.com/cindocode/trenchbot/pkg/solana"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg, err := config.Load()
	if err != nil {
		log.Error("failed to load config", "err", err)
		os.Exit(1)
	}

	mode := "SHADOW"
	if cfg.IsLive() {
		mode = "LIVE"
	}
	log.Info("trenchbot starting", "mode", mode)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		log.Info("shutdown signal received", "signal", sig)
		cancel()
	}()

	if err := notify.Init(cfg.SentryDSN, cfg.Mode); err != nil {
		log.Warn("sentry init failed, events will not be tracked", "err", err)
	}
	defer notify.Flush(2 * time.Second)

	clk := clock.RealClock{}
	store := state.NewStore()
	notifier := notify.New(cfg.SentryDSN, log)

	// Postgres reporter (optional).
	var reportStore *reporter.ReportStore
	if cfg.DatabaseURL != "" {
		reportStore, err = reporter.NewReportStore(cfg.DatabaseURL)
		if err != nil {
			log.Error("failed to connect to database", "err", err)
			os.Exit(1)
		}
		defer reportStore.Close()
		log.Info("connected to postgres for reporting")
	}

	solClient, err := solanaclient.NewClient(cfg.SolanaRPCURL, cfg.SolanaPrivateKey, log)
	if err != nil {
		log.Error("failed to init solana client", "err", err)
		os.Exit(1)
	}

	bnbClient, err := bnbclient.NewClient(cfg.BNBRPCURL, cfg.BNBPrivateKey, log)
	if err != nil {
		log.Warn("failed to init BNB client, disabling BNB chain", "err", err)
		bnbClient = nil
	}

	// Query actual wallet balance for circuit breaker starting equity.
	solBalance := 1.0 // fallback
	if bal, err := solClient.GetBalance(context.Background()); err == nil {
		solBalance = bal
		log.Info("solana wallet balance", "sol", solBalance)
	}

	var bnbBalance float64 = 1.0 // fallback
	if bnbClient != nil {
		if bal, err := bnbClient.GetBalanceBNB(context.Background()); err == nil {
			bnbBalance = bal
			log.Info("bnb wallet balance", "bnb", bnbBalance)
		}
	}

	solCB := risk.NewCircuitBreaker(risk.CircuitBreakerConfig{
		Chain:              state.ChainSolana,
		MaxDrawdownPct:     cfg.TotalDrawdownLimitPct,
		DailyLossLimitPct:  cfg.DailyLossLimitPct,
		ConsecutiveLossCap: cfg.ConsecutiveLossPauseThresh,
		MaxSnipesPerHour:   cfg.MaxSnipesPerHour,
		StartingEquity:     solBalance,
	}, store, clk, log)

	var bnbCB *risk.CircuitBreaker
	if bnbClient != nil {
		bnbCB = risk.NewCircuitBreaker(risk.CircuitBreakerConfig{
			Chain:              state.ChainBNB,
			MaxDrawdownPct:     cfg.TotalDrawdownLimitPct,
			DailyLossLimitPct:  cfg.DailyLossLimitPct,
			ConsecutiveLossCap: cfg.ConsecutiveLossPauseThresh,
			MaxSnipesPerHour:   cfg.MaxSnipesPerHour,
			StartingEquity:     bnbBalance,
		}, store, clk, log)
	}

	// Initialize gas balances.
	store.SetGasBalance(state.ChainSolana, cfg.GasBudgetSOL)
	store.SetGasBalance(state.ChainBNB, cfg.GasBudgetBNB)

	sizer := risk.NewPositionSizer(store, cfg.SolanaSnipeAmount, cfg.BNBSnipeAmount, cfg.DailyLossLimitPct)
	sizer.SetMaxPositions(5)
	sizer.SetGasReserves(cfg.MinGasReserveSOL, cfg.MinGasReserveBNB)
	tokenFilter := filter.New(cfg.MinScoreThreshold, log)

	// Wire creator lookup if Postgres is available.
	if reportStore != nil {
		tokenFilter.SetCreatorLookup(reportStore)
	}

	// Reporter (works with nil reportStore).
	rep := reporter.New(reportStore, store, log)
	rep.SetCircuitBreaker(state.ChainSolana, solCB)
	if bnbCB != nil {
		rep.SetCircuitBreaker(state.ChainBNB, bnbCB)
	}

	executors := make(map[state.Chain]executor.Executor)
	pumpExec := executor.NewPumpFunExecutor(cfg.PumpPortalTradeURL, solClient, log)
	executors[state.ChainSolana] = pumpExec

	if bnbClient != nil {
		fourExec, err := executor.NewFourMemeExecutor(bnbClient, cfg.FourMemeProxyContract, log)
		if err != nil {
			log.Warn("failed to init four.meme executor", "err", err)
		} else {
			executors[state.ChainBNB] = fourExec
		}
	}

	mon := monitor.New(store, executors, notifier, monitor.DefaultExitConfig(), clk, !cfg.IsLive(), log)

	cbs := map[state.Chain]*risk.CircuitBreaker{
		state.ChainSolana: solCB,
	}
	if bnbCB != nil {
		cbs[state.ChainBNB] = bnbCB
	}
	mon.SetCircuitBreakers(cbs)

	tokenCh := make(chan scanner.NewToken, 100)
	var wg sync.WaitGroup

	pumpScanner := scanner.NewPumpFunScanner(cfg.PumpPortalWSURL, log)
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := pumpScanner.Scan(ctx, tokenCh); err != nil && ctx.Err() == nil {
			log.Error("pumpfun scanner error", "err", err)
		}
	}()

	if bnbClient != nil {
		fourScanner := scanner.NewFourMemeScanner(cfg.BitqueryAPIURL, cfg.BitqueryAPIKey, cfg.FourMemeProxyContract, log)
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := fourScanner.Scan(ctx, tokenCh); err != nil && ctx.Err() == nil {
				log.Error("four.meme scanner error", "err", err)
			}
		}()
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		mon.Run(ctx)
	}()

	// Price update goroutine: polls current prices every 10 seconds.
	wg.Add(1)
	go func() {
		defer wg.Done()
		priceTicker := time.NewTicker(10 * time.Second)
		defer priceTicker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-priceTicker.C:
				for _, pos := range store.AllOpenPositions() {
					exec, ok := executors[pos.Chain]
					if !ok {
						continue
					}
					pf, ok := exec.(executor.PriceFeed)
					if !ok {
						continue
					}
					price, err := pf.CurrentPrice(ctx, pos.TokenAddress)
					if err != nil || price <= 0 {
						continue
					}
					store.UpdatePosition(pos.ID, func(p *state.Position) {
						p.CurrentPrice = price
					})
				}
			}
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		riskTicker := time.NewTicker(10 * time.Second)
		defer riskTicker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-riskTicker.C:
				// Compute equity and run circuit breaker checks (drawdown halt + daily loss limit).
				solEquity := calculateEquity(store, state.ChainSolana, solBalance)
				solCB.Check(solEquity)
				if solCB.IsHalted() {
					log.Warn("solana circuit breaker HALTED")
				}
				if bnbCB != nil {
					bnbEquity := calculateEquity(store, state.ChainBNB, bnbBalance)
					bnbCB.Check(bnbEquity)
					if bnbCB.IsHalted() {
						log.Warn("bnb circuit breaker HALTED")
					}
				}
			}
		}
	}()

	// Hourly reports → Postgres.
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(1 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				snap := rep.ComputeSnapshot(ctx, "hourly", time.Now().Add(-1*time.Hour), time.Now())
				rep.SaveReport(ctx, snap)
			}
		}
	}()

	// Daily reports at midnight UTC → Postgres.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			now := time.Now().UTC()
			next := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, time.UTC)
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Until(next)):
				dayStart := next.Add(-24 * time.Hour)
				snap := rep.ComputeSnapshot(ctx, "daily", dayStart, next)
				rep.SaveReport(ctx, snap)
			}
		}
	}()

	// Midnight daily-reset goroutine: resets daily PnL for circuit breaker.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			now := time.Now().UTC()
			next := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, time.UTC)
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Until(next)):
				store.ResetDailyPnL()
				solCB.ResetPauseCycles()
				if bnbCB != nil {
					bnbCB.ResetPauseCycles()
				}
				log.Info("daily PnL reset at midnight UTC")
			}
		}
	}()

	// Weekly reports at Monday midnight UTC → Postgres.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			now := time.Now().UTC()
			daysUntilMonday := (8 - int(now.Weekday())) % 7
			if daysUntilMonday == 0 {
				daysUntilMonday = 7
			}
			next := time.Date(now.Year(), now.Month(), now.Day()+daysUntilMonday, 0, 0, 0, 0, time.UTC)
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Until(next)):
				weekStart := next.Add(-7 * 24 * time.Hour)
				snap := rep.ComputeSnapshot(ctx, "weekly", weekStart, next)
				rep.SaveReport(ctx, snap)
			}
		}
	}()

	log.Info("pipeline started, waiting for tokens...")
	for {
		select {
		case <-ctx.Done():
			log.Info("shutting down pipeline...")

			// Shutdown report.
			now := time.Now()
			dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
			snap := rep.ComputeSnapshot(context.Background(), "shutdown", dayStart, now)
			rep.SaveReport(context.Background(), snap)

			wg.Wait()
			log.Info("trenchbot stopped")
			return
		case token := <-tokenCh:
			go processToken(ctx, token, tokenFilter, sizer, executors, store, notifier, rep, reportStore, solCB, bnbCB, cfg, log)
		}
	}
}

func processToken(
	ctx context.Context,
	token scanner.NewToken,
	f *filter.Filter,
	sizer *risk.PositionSizer,
	executors map[state.Chain]executor.Executor,
	store *state.Store,
	notifier notify.Notifier,
	rep *reporter.Reporter,
	reportStore *reporter.ReportStore,
	solCB *risk.CircuitBreaker,
	bnbCB *risk.CircuitBreaker,
	cfg *config.Config,
	log *slog.Logger,
) {
	result := f.Evaluate(ctx, token)
	if !result.Approved {
		return
	}

	var cb *risk.CircuitBreaker
	switch token.Chain {
	case state.ChainSolana:
		cb = solCB
	case state.ChainBNB:
		cb = bnbCB
	}
	if cb == nil || !cb.CanSnipe() {
		log.Info("snipe blocked by circuit breaker", "chain", token.Chain, "token", token.Symbol)
		return
	}

	if !store.TryReserveSlot(token.Chain, cfg.MaxPositionsPerChain, cfg.MaxPositionsTotal) {
		log.Info("position limit reached", "chain", token.Chain)
		return
	}

	size := sizer.Size(token.Chain, result.Score)
	if size <= 0 {
		store.ReleaseSlot(token.Chain)
		return
	}

	exec, ok := executors[token.Chain]
	if !ok {
		log.Error("no executor for chain", "chain", token.Chain)
		store.ReleaseSlot(token.Chain)
		return
	}

	shadow := !cfg.IsLive()
	buyResult := exec.Buy(ctx, executor.BuyParams{
		Chain:        token.Chain,
		TokenAddress: token.Address,
		TokenSymbol:  token.Symbol,
		Amount:       size,
		Shadow:       shadow,
	})

	if buyResult.Error != nil {
		cb.RecordError()
		store.ReleaseSlot(token.Chain)
		return
	}

	cb.RecordSnipe()

	// Deduct buy gas.
	store.DeductGas(token.Chain, buyResult.GasCost)

	posID := fmt.Sprintf("%s-%s-%d", token.Chain, executor.SafePrefix(token.Address, 8), time.Now().UnixMilli())
	store.AddPosition(&state.Position{
		ID:           posID,
		Chain:        token.Chain,
		TokenAddress: token.Address,
		TokenSymbol:  token.Symbol,
		EntryPrice:   buyResult.Price,
		CurrentPrice: buyResult.Price,
		PeakPrice:    buyResult.Price,
		Amount:       buyResult.Amount,
		EntryTime:    time.Now(),
		EntryGasCost: buyResult.GasCost,
	})
	store.ConsumeSlot(token.Chain)

	now := time.Now()
	store.AddTrade(state.Trade{
		ID:           buyResult.TxHash,
		Chain:        token.Chain,
		TokenAddress: token.Address,
		TokenSymbol:  token.Symbol,
		Side:         "buy",
		Price:        buyResult.Price,
		Amount:       buyResult.Amount,
		Timestamp:    now,
		TxHash:       buyResult.TxHash,
		Shadow:       shadow,
	})

	// Record trade to Postgres.
	rep.RecordTrade(ctx, reporter.TradeRow{
		ID:           buyResult.TxHash,
		Chain:        string(token.Chain),
		TokenAddress: token.Address,
		TokenSymbol:  token.Symbol,
		Side:         "buy",
		Price:        buyResult.Price,
		Amount:       buyResult.Amount,
		GasCost:      buyResult.GasCost,
		Shadow:       shadow,
		CreatedAt:    now,
	})

	// Store creator for future lookups.
	if reportStore != nil && token.Creator != "" {
		if err := reportStore.UpsertTokenCreator(ctx, token.Address, token.Creator); err != nil {
			log.Warn("failed to upsert token creator", "err", err)
		}
	}

	notifier.Snipe(ctx, string(token.Chain), token.Symbol, token.Address, size, buyResult.Price, shadow)
}

// calculateEquity computes the current equity for a chain based on open positions.
func calculateEquity(store *state.Store, chain state.Chain, startingEquity float64) float64 {
	equity := startingEquity
	for _, pos := range store.OpenPositions(chain) {
		if pos.EntryPrice > 0 {
			pnlMult := pos.CurrentPrice / pos.EntryPrice
			equity += pos.Amount * (pnlMult - 1)
		}
	}
	store.SetPeakEquity(chain, equity)
	return equity
}
