package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
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
	// Shadow mode logs at DEBUG (verbose); live mode logs at INFO (actions only).
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg, err := config.Load()
	if err != nil {
		log.Error("failed to load config", "err", err)
		os.Exit(1)
	}

	mode := "SHADOW"
	if cfg.IsLive() {
		mode = "LIVE"
	} else {
		// In shadow mode, enable DEBUG to see per-token scoring and detection.
		log = slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))
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

	// Restore state from previous run.
	if err := store.LoadSnapshot(cfg.StateSnapshotPath); err != nil {
		log.Warn("failed to load state snapshot", "err", err)
	} else {
		log.Info("state snapshot loaded", "path", cfg.StateSnapshotPath)
	}

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

	// Initialize gas balances from actual wallet balance.
	store.SetGasBalance(state.ChainSolana, solBalance)
	store.SetGasBalance(state.ChainBNB, bnbBalance)

	sizer := risk.NewPositionSizer(store, cfg.SolanaSnipeAmount, cfg.BNBSnipeAmount, cfg.DailyLossLimitPct)
	sizer.SetMaxPositions(cfg.MaxPositionsTotal)
	sizer.SetGasReserves(cfg.MinGasReserveSOL, cfg.MinGasReserveBNB)

	// Reporter (works with nil reportStore).
	rep := reporter.New(reportStore, store, log)
	rep.SetCircuitBreaker(state.ChainSolana, solCB)
	if bnbCB != nil {
		rep.SetCircuitBreaker(state.ChainBNB, bnbCB)
	}

	tokenFilter := filter.New(cfg.MinScoreThreshold, log)

	// Wire honeypot checker if enabled.
	if cfg.HoneypotCheckEnabled {
		tokenFilter.SetHoneypotChecker(filter.NewHoneypotChecker())
		log.Info("honeypot detection enabled via GoPlus API")
	}

	// Wire creator lookup if Postgres is available.
	if reportStore != nil {
		tokenFilter.SetCreatorLookup(reportStore)
	}

	executors := make(map[state.Chain]executor.Executor)
	pumpExec := executor.NewPumpFunExecutor(cfg.PumpPortalTradeURL, solClient, cfg.SlippagePctSOL, log)
	executors[state.ChainSolana] = pumpExec

	if bnbClient != nil {
		fourExec, err := executor.NewFourMemeExecutor(bnbClient, cfg.FourMemeProxyContract, log)
		if err != nil {
			log.Warn("failed to init four.meme executor", "err", err)
		} else {
			executors[state.ChainBNB] = fourExec
		}
	}

	exitCfg := monitor.DefaultExitConfig()
	exitCfg.StopLossPct = cfg.StopLossPct
	exitCfg.Tranche1X = cfg.Tranche1X
	exitCfg.UniversalTrailingThreshold = cfg.UniversalTrailingThreshold
	exitCfg.UniversalTrailingStop = cfg.UniversalTrailingStop
	exitCfg.NoTradeTimeout = time.Duration(cfg.NoTradeTimeoutSec) * time.Second
	exitCfg.NoTradeMaxMult = cfg.NoTradeMaxMult
	mon := monitor.New(store, executors, notifier, exitCfg, clk, !cfg.IsLive(), log)

	cbs := map[state.Chain]*risk.CircuitBreaker{
		state.ChainSolana: solCB,
	}
	if bnbCB != nil {
		cbs[state.ChainBNB] = bnbCB
	}
	mon.SetCircuitBreakers(cbs)

	tokenCh := make(chan scanner.NewToken, 100)
	tradeCh := make(chan scanner.TokenTrade, 256)
	var wg sync.WaitGroup

	pumpScanner := scanner.NewPumpFunScanner(cfg.PumpPortalWSURL, log)
	pumpScanner.SetTradeChannel(tradeCh)

	// Unsubscribe from trade feed when positions close.
	mon.SetOnPositionClose(func(chain state.Chain, tokenAddress string) {
		if chain == state.ChainSolana {
			pumpScanner.UnsubscribeToken(tokenAddress)
		}
	})

	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := pumpScanner.Scan(ctx, tokenCh); err != nil && ctx.Err() == nil {
			log.Error("pumpfun scanner error", "err", err)
		}
	}()

	if bnbClient != nil && cfg.BitqueryAPIKey != "" {
		pollInterval := time.Duration(cfg.BitqueryPollIntervalSec) * time.Second
		fourScanner := scanner.NewFourMemeScanner(cfg.BitqueryAPIURL, cfg.BitqueryAPIKey, cfg.FourMemeProxyContract, pollInterval, log)
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := fourScanner.Scan(ctx, tokenCh); err != nil && ctx.Err() == nil {
				log.Error("four.meme scanner error", "err", err)
			}
		}()
	} else if bnbClient != nil {
		log.Info("four.meme scanner disabled (no BITQUERY_API_KEY)")
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		mon.Run(ctx)
	}()

	// Pre-buy trade counters: tracks trade volume per mint for observation window.
	var tradeCounters sync.Map // mint -> *int64

	// Trade event listener: receives real-time trade events from PumpPortal
	// WebSocket and updates position prices accordingly.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-ctx.Done():
				return
			case trade := <-tradeCh:
				// Increment pre-buy trade counter (even for zero mcap trades).
				if v, ok := tradeCounters.Load(trade.Mint); ok {
					atomic.AddInt64(v.(*int64), 1)
				}
				if trade.MarketCapSol <= 0 {
					continue
				}
				// Use SOL-denominated market cap as the price signal.
				// The absolute value doesn't matter — only the ratio
				// (current / entry) drives the multiplier.
				mcapSol := trade.MarketCapSol
				for _, pos := range store.AllOpenPositions() {
					if pos.TokenAddress != trade.Mint {
						continue
					}
					var multiplier float64
					tradeNow := time.Now()
					store.UpdatePosition(pos.ID, func(p *state.Position) {
						p.LastTradeTime = tradeNow
						if p.EntryPriceUSD <= 0 {
							p.EntryPriceUSD = mcapSol
						}
						if p.EntryPriceUSD > 0 {
							p.CurrentPrice = p.EntryPrice * (mcapSol / p.EntryPriceUSD)
							multiplier = p.CurrentPrice / p.EntryPrice
							if p.CurrentPrice > p.PeakPrice {
								p.PeakPrice = p.CurrentPrice
							}
						}
					})
					log.Debug("price updated from trade feed",
						"token", pos.TokenSymbol,
						"mcap_sol", mcapSol,
						"multiplier", multiplier,
					)
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

	// Hourly PnL reset: allows the bot to recover from bad streaks within the same day.
	wg.Add(1)
	go func() {
		defer wg.Done()
		resetTicker := time.NewTicker(1 * time.Hour)
		defer resetTicker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-resetTicker.C:
				store.ResetDailyPnL()
				solCB.ResetPauseCycles()
				if bnbCB != nil {
					bnbCB.ResetPauseCycles()
				}
				log.Info("hourly PnL reset")
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

	// Periodic state snapshot.
	wg.Add(1)
	go func() {
		defer wg.Done()
		snapTicker := time.NewTicker(30 * time.Second)
		defer snapTicker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-snapTicker.C:
				if err := store.SaveSnapshot(cfg.StateSnapshotPath); err != nil {
					log.Warn("failed to save state snapshot", "err", err)
				}
			}
		}
	}()

	// Semaphore to bound concurrent buy goroutines.
	workerSem := make(chan struct{}, cfg.MaxConcurrentBuys)
	// Duplicate token guard prevents processing the same token concurrently.
	var pendingTokens sync.Map

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
			if err := store.SaveSnapshot(cfg.StateSnapshotPath); err != nil {
				log.Warn("failed to save state on shutdown", "err", err)
			}
			log.Info("trenchbot stopped")
			return
		case token := <-tokenCh:
			if _, loaded := pendingTokens.LoadOrStore(token.Address, struct{}{}); loaded {
				continue
			}
			workerSem <- struct{}{} // acquire
			go func(t scanner.NewToken) {
				defer func() {
					<-workerSem // release
					pendingTokens.Delete(t.Address)
				}()
				processToken(ctx, t, tokenFilter, sizer, executors, store, notifier, rep, reportStore, solCB, bnbCB, pumpScanner, &tradeCounters, cfg, log)
			}(token)
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
	pumpScanner *scanner.PumpFunScanner,
	tradeCounters *sync.Map,
	cfg *config.Config,
	log *slog.Logger,
) {
	result := f.Evaluate(ctx, token)
	if !result.Approved {
		return
	}

	// Volume-aware pre-buy check: observe trade activity before committing capital.
	if cfg.MinTradesBeforeBuy > 0 && token.Chain == state.ChainSolana {
		var counter int64
		tradeCounters.Store(token.Address, &counter)
		pumpScanner.SubscribeToken(token.Address)

		observeDuration := time.Duration(cfg.TradeObservationSecs) * time.Second
		select {
		case <-time.After(observeDuration):
		case <-ctx.Done():
			tradeCounters.Delete(token.Address)
			pumpScanner.UnsubscribeToken(token.Address)
			return
		}

		trades := atomic.LoadInt64(&counter)
		tradeCounters.Delete(token.Address)

		if trades < int64(cfg.MinTradesBeforeBuy) {
			log.Debug("token failed volume check, skipping buy",
				"token", token.Symbol,
				"trades", trades,
				"required", cfg.MinTradesBeforeBuy,
			)
			pumpScanner.UnsubscribeToken(token.Address)
			return
		}
		log.Debug("token passed volume check",
			"token", token.Symbol,
			"trades", trades,
		)
	}

	var cb *risk.CircuitBreaker
	switch token.Chain {
	case state.ChainSolana:
		cb = solCB
	case state.ChainBNB:
		cb = bnbCB
	}
	if cb == nil || !cb.CanSnipe() {
		log.Debug("snipe blocked by circuit breaker", "chain", token.Chain, "token", token.Symbol)
		if token.Chain == state.ChainSolana {
			pumpScanner.UnsubscribeToken(token.Address)
		}
		return
	}

	if !store.TryReserveSlot(token.Chain, cfg.MaxPositionsPerChain, cfg.MaxPositionsTotal) {
		log.Debug("position limit reached", "chain", token.Chain)
		if token.Chain == state.ChainSolana {
			pumpScanner.UnsubscribeToken(token.Address)
		}
		return
	}

	size := sizer.Size(token.Chain, result.Score)
	if size <= 0 {
		store.ReleaseSlot(token.Chain)
		if token.Chain == state.ChainSolana {
			pumpScanner.UnsubscribeToken(token.Address)
		}
		return
	}

	exec, ok := executors[token.Chain]
	if !ok {
		log.Error("no executor for chain", "chain", token.Chain)
		store.ReleaseSlot(token.Chain)
		if token.Chain == state.ChainSolana {
			pumpScanner.UnsubscribeToken(token.Address)
		}
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
		if token.Chain == state.ChainSolana {
			pumpScanner.UnsubscribeToken(token.Address)
		}
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
		TokenBalance: buyResult.TokenAmount,
		EntryTime:     time.Now(),
		LastTradeTime: time.Now(),
		EntryGasCost:  buyResult.GasCost,
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

	// Subscribe to real-time trade events for this token's price updates.
	if token.Chain == state.ChainSolana {
		pumpScanner.SubscribeToken(token.Address)
	}

	// Run honeypot check asynchronously after the buy. If the token is flagged,
	// the position will be force-closed without blocking the pipeline.
	go f.CheckHoneypotAsync(ctx, token.Chain, token.Address, posID, store, notifier, log)
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
