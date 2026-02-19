package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/cindocode/trenchbot/internal/capital"
	"github.com/cindocode/trenchbot/internal/clock"
	"github.com/cindocode/trenchbot/internal/config"
	"github.com/cindocode/trenchbot/internal/executor"
	"github.com/cindocode/trenchbot/internal/filter"
	"github.com/cindocode/trenchbot/internal/flow"
	"github.com/cindocode/trenchbot/internal/gas"
	"github.com/cindocode/trenchbot/internal/jito"
	"github.com/cindocode/trenchbot/internal/monitor"
	"github.com/cindocode/trenchbot/internal/notify"
	"github.com/cindocode/trenchbot/internal/reporter"
	"github.com/cindocode/trenchbot/internal/risk"
	"github.com/cindocode/trenchbot/internal/scanner"
	"github.com/cindocode/trenchbot/internal/state"
	"github.com/cindocode/trenchbot/internal/wallet"
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

	// Query actual wallet balance for circuit breaker starting equity.
	solBalance := 1.0 // fallback
	if bal, err := solClient.GetBalance(context.Background()); err == nil {
		solBalance = bal
		log.Info("solana wallet balance", "sol", solBalance)
	}
	// Live mode: verify sufficient balance before proceeding.
	if cfg.IsLive() {
		minRequired := cfg.SolanaSnipeAmount + cfg.MinGasReserveSOL
		if solBalance < minRequired {
			log.Error("insufficient balance for live mode",
				"balance", solBalance,
				"min_required", minRequired,
				"snipe_amount", cfg.SolanaSnipeAmount,
				"gas_reserve", cfg.MinGasReserveSOL,
			)
			os.Exit(1)
		}
	}

	// In shadow mode, use sweep reserve as starting equity so heat calculations
	// are realistic (wallet balance is 0 in shadow mode).
	if !cfg.IsLive() && solBalance < cfg.SweepReserveSOL {
		solBalance = cfg.SweepReserveSOL
		log.Info("shadow mode: using sweep reserve as starting equity", "sol", solBalance)
	}

	solCB := risk.NewCircuitBreaker(risk.CircuitBreakerConfig{
		Chain:              state.ChainSolana,
		MaxDrawdownPct:     cfg.TotalDrawdownLimitPct,
		HeatFullPct:        cfg.HeatFullPct,
		ConsecutiveLossCap: cfg.ConsecutiveLossPauseThresh,
		MaxSnipesPerHour:   cfg.MaxSnipesPerHour,
		StartingEquity:     solBalance,
	}, store, clk, log)

	// Restore circuit breaker state from snapshot (preserves halt/pause across restarts).
	if cbState := store.GetCBState(state.ChainSolana); cbState.Halted || cbState.ConsecutiveLosses > 0 || !cbState.PausedUntil.IsZero() {
		solCB.ImportState(cbState)
		log.Info("circuit breaker state restored from snapshot",
			"halted", cbState.Halted,
			"consecutive_losses", cbState.ConsecutiveLosses,
			"pause_cycles", cbState.PauseCycles,
		)
	}

	// Initialize gas balance from actual wallet balance.
	store.SetGasBalance(state.ChainSolana, solBalance)

	// Configure Helius API if key provided.
	if cfg.HeliusAPIKey != "" {
		solClient.SetHeliusKey(cfg.HeliusAPIKey)
		log.Info("helius API configured")
	}

	// Configure fallback RPCs.
	if cfg.SolanaRPCFallbackURLs != "" {
		urls := strings.Split(cfg.SolanaRPCFallbackURLs, ",")
		solClient.SetFallbackRPCs(urls)
		log.Info("solana fallback RPCs configured", "count", len(urls))
	}

	// Performance tracker (Phase 4: Kelly criterion).
	perfTracker := risk.NewPerformanceTracker(cfg.KellyWindowSize)

	sizer := risk.NewPositionSizer(store, cfg.SolanaSnipeAmount)
	sizer.SetMaxPositions(cfg.MaxPositionsTotal)
	sizer.SetHeatFunc(solCB.Heat)
	sizer.SetGasReserve(cfg.MinGasReserveSOL)
	sizer.SetMaxImpact(cfg.MaxTradeImpactPct)

	// Wire Kelly criterion if enabled.
	if cfg.KellyEnabled {
		sizer.SetPerformanceTracker(perfTracker)
		log.Info("kelly criterion sizing enabled", "window", cfg.KellyWindowSize)
	}

	// Enable dynamic position limits (scales with available capital).
	if cfg.DynamicPositionLimits {
		sizer.EnableDynamicLimits(cfg.PositionScaleFactor)
		log.Info("dynamic position limits enabled", "scale_factor", cfg.PositionScaleFactor)
	}

	// Reporter (works with nil reportStore).
	rep := reporter.New(reportStore, store, log)
	rep.SetCircuitBreaker(state.ChainSolana, solCB)

	tokenFilter := filter.New(cfg.MinScoreThreshold, log)
	// Heat no longer drives filter threshold — tokens cluster at exactly 55
	// and a 1-point increase blocks 100% of them. Heat still affects position
	// sizing via the position sizer.

	// Wire honeypot checker if enabled.
	if cfg.HoneypotCheckEnabled {
		tokenFilter.SetHoneypotChecker(filter.NewHoneypotChecker())
		log.Info("honeypot detection enabled via GoPlus API")
	}

	// Wire creator lookup if Postgres is available.
	if reportStore != nil {
		tokenFilter.SetCreatorLookup(reportStore)
	}

	// Wire holder distribution checker (Phase 2A).
	if cfg.HolderCheckEnabled && cfg.HeliusAPIKey != "" {
		tokenFilter.SetHolderChecker(&heliusHolderAdapter{client: solClient}, cfg.MaxTopHolderPct)
		log.Info("holder distribution checking enabled", "max_top_holder_pct", cfg.MaxTopHolderPct)
	}

	executors := make(map[state.Chain]executor.Executor)
	pumpExec := executor.NewPumpFunExecutor(cfg.PumpPortalTradeURL, solClient, cfg.SlippagePctSOL, log)

	// Wire dynamic slippage (Phase 3A).
	if cfg.DynamicSlippageEnabled {
		pumpExec.SetDynamicSlippage(true, solCB.Heat)
		log.Info("dynamic slippage enabled")
	}

	// Wire Helius priority fee estimates (Phase 3B).
	if cfg.HeliusAPIKey != "" {
		pumpExec.SetFeeEstimator(solClient.GetPriorityFeeEstimate)
		log.Info("helius priority fee estimates enabled")
	}

	// Wire Jito bundle submission (Phase 2A).
	if cfg.JitoEnabled {
		jitoClient := jito.NewClient(cfg.JitoBlockEngineURL, cfg.JitoTipLamports)
		pumpExec.SetJitoClient(jitoClient)
		log.Info("jito bundle submission enabled", "url", cfg.JitoBlockEngineURL, "tip_lamports", cfg.JitoTipLamports)
	}

	// Wire multi-wallet rotation (Phase 2B).
	if cfg.WalletRotationEnabled && cfg.SolanaPrivateKeys != "" {
		keys := strings.Split(cfg.SolanaPrivateKeys, ",")
		wp, err := wallet.NewPool(keys)
		if err != nil {
			log.Error("failed to init wallet pool", "err", err)
			os.Exit(1)
		}
		pumpExec.SetWalletPool(wp)
		log.Info("wallet rotation enabled", "wallets", wp.Count())
	}

	executors[state.ChainSolana] = pumpExec

	exitCfg := monitor.DefaultExitConfig()
	exitCfg.StopLossPct = cfg.StopLossPct
	exitCfg.Tranche1X = cfg.Tranche1X
	exitCfg.UniversalTrailingThreshold = cfg.UniversalTrailingThreshold
	exitCfg.UniversalTrailingStop = cfg.UniversalTrailingStop
	exitCfg.NoTradeTimeout = time.Duration(cfg.NoTradeTimeoutSec) * time.Second
	exitCfg.NoTradeMaxMult = cfg.NoTradeMaxMult
	exitCfg.PreGradExitProgress = cfg.PreGradExitProgress
	mon := monitor.New(store, executors, notifier, exitCfg, clk, !cfg.IsLive(), log)

	mon.SetCircuitBreakers(map[state.Chain]*risk.CircuitBreaker{
		state.ChainSolana: solCB,
	})
	mon.SetPerformanceTracker(perfTracker)
	mon.SetTradeRecorder(func(ctx context.Context, txHash string, chain state.Chain, tokenAddress, tokenSymbol string, sellPrice, amount, pnlPct, gasCost float64, reason string, shadow bool) {
		rep.RecordTrade(ctx, reporter.TradeRow{
			ID:           txHash,
			Chain:        string(chain),
			TokenAddress: tokenAddress,
			TokenSymbol:  tokenSymbol,
			Side:         "sell",
			Price:        sellPrice,
			Amount:       amount,
			PnLPct:       &pnlPct,
			ExitReason:   &reason,
			GasCost:      gasCost,
			Shadow:       shadow,
			CreatedAt:    clk.Now(),
		})
	})

	// Anti-bot intelligence (Phase 3).
	bundleDetector := filter.NewBundleDetector(cfg.BundleDetectionEnabled)
	reputationDB := filter.NewReputationDB()
	botDB := flow.NewBotDB(5)               // flag addresses seen in early buys on 5+ tokens
	var creatorMap sync.Map                   // tokenAddress -> creator address
	log.Info("anti-bot intelligence initialized",
		"bundle_detection", cfg.BundleDetectionEnabled,
	)

	// Survival model (Phase 6): offline-trained, runtime inference.
	var survivalModel *flow.SurvivalModel
	if cfg.SurvivalModelPath != "" {
		var err error
		survivalModel, err = flow.LoadSurvivalModel(cfg.SurvivalModelPath)
		if err != nil {
			log.Warn("failed to load survival model, using defaults", "err", err)
			survivalModel = flow.DefaultSurvivalModel()
		} else {
			log.Info("survival model loaded", "path", cfg.SurvivalModelPath, "features", len(survivalModel.Features))
		}
	}

	// Thompson sampling (Phase 7): multi-token allocation optimization.
	var thompsonSampler *flow.ThompsonSampler
	if cfg.ThompsonSamplingEnabled {
		thompsonSampler = flow.NewThompsonSampler()
		log.Info("thompson sampling enabled")
	}

	// Bot dump delayed entry (Phase 3D).
	var delayedEntry *flow.DelayedEntry
	if cfg.BotDumpDelayEnabled {
		delayedEntry = flow.NewDelayedEntry(time.Duration(cfg.BotDumpDelaySec) * time.Second)
		log.Info("bot dump delayed entry enabled", "delay_sec", cfg.BotDumpDelaySec)
	}

	tokenCh := make(chan scanner.NewToken, 100)
	tradeCh := make(chan scanner.TokenTrade, 256)
	var wg sync.WaitGroup

	pumpScanner := scanner.NewPumpFunScanner(cfg.PumpPortalWSURL, log)
	pumpScanner.SetTradeChannel(tradeCh)

	// Re-subscribe to trade feeds for open positions restored from snapshot.
	for _, pos := range store.AllOpenPositions() {
		if pos.Chain == state.ChainSolana {
			pumpScanner.SubscribeToken(pos.TokenAddress)
			log.Info("re-subscribed to trade feed for restored position",
				"token", pos.TokenSymbol,
				"id", pos.ID,
			)
		}
	}

	// Per-position CUSUM anomaly detectors for open positions.
	var positionAnomalyDetectors sync.Map // mint -> *flow.TokenAnomalyDetector

	// Unsubscribe from trade feed when positions close + update creator reputation.
	mon.SetOnPositionClose(func(chain state.Chain, tokenAddress string) {
		if chain == state.ChainSolana {
			pumpScanner.UnsubscribeToken(tokenAddress)
		}
		// Clean up CUSUM anomaly detector for this position.
		positionAnomalyDetectors.Delete(tokenAddress)
		// Update creator reputation from closed position.
		creatorVal, ok := creatorMap.LoadAndDelete(tokenAddress)
		if !ok {
			return
		}
		creator := creatorVal.(string)
		// Find the position by token address (including closed positions).
		pos, found := store.FindPositionByToken(tokenAddress)
		if !found {
			return
		}
		peakMult := 1.0
		if pos.EntryPrice > 0 {
			peakMult = pos.PeakPrice / pos.EntryPrice
		}
		isRug := peakMult < 0.3
		reputationDB.RecordOutcome(creator, peakMult, pos.HoldDuration, isRug)

		// Update Thompson sampler with outcome.
		if thompsonSampler != nil {
			obs := flow.ObservationResult{
				OFI:               pos.OFI,
				LiquidityVelocity: pos.LiquidityVelocity,
				BotBuyCount:       pos.BotBuyCount,
			}
			bucketKey := flow.BucketKey(obs, pos.FilterScore)
			won := peakMult > 1.5
			thompsonSampler.Update(bucketKey, won)
		}

		// Close observation in Postgres for survival model training.
		if reportStore != nil {
			pnlPct := (peakMult - 1.0) * 100
			if pos.PnL != 0 {
				pnlPct = pos.PnL
			}
			holdSec := pos.HoldDuration.Seconds()
			if err := reportStore.CloseObservation(ctx, pos.ID, holdSec, pnlPct, peakMult, pos.ExitReason); err != nil {
				log.Warn("failed to close observation", "err", err)
			}
		}
	})

	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := pumpScanner.Scan(ctx, tokenCh); err != nil && ctx.Err() == nil {
			log.Error("pumpfun scanner error", "err", err)
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		mon.Run(ctx)
	}()

	// Alpha monitor (Phase 6).
	if cfg.AlphaMonitorEnabled {
		origMinScore := cfg.MinScoreThreshold
		origMaxPositions := cfg.MaxPositionsTotal
		alphaMonitor := risk.NewAlphaMonitor(perfTracker, risk.AlphaMonitorConfig{}, log)
		alphaMonitor.SetOnDegrade(func() {
			sizer.SetMaxPositions(origMaxPositions / 2)
			log.Warn("alpha degradation: reduced positions")
		})
		alphaMonitor.SetOnRecover(func() {
			sizer.SetMaxPositions(origMaxPositions)
			log.Info("alpha recovered: restored positions")
		})
		_ = origMinScore

		wg.Add(1)
		go func() {
			defer wg.Done()
			alphaMonitor.Run(ctx.Done())
		}()
		log.Info("alpha monitor enabled")
	}

	// Pre-buy trade observers: tracks rich flow data per mint for observation window.
	var tradeObservers sync.Map // mint -> *flow.Observer

	// Semaphore to bound concurrent buy goroutines.
	workerSem := make(chan struct{}, cfg.MaxConcurrentBuys)

	// Bot dump delayed entry poller: re-evaluates tokens after bot dump wave.
	if delayedEntry != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ticker := time.NewTicker(5 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					for _, dt := range delayedEntry.Ready() {
						token, ok := dt.Original.(scanner.NewToken)
						if !ok {
							continue
						}
						log.Info("delayed entry: re-evaluating post-bot-dump",
							"token", token.Symbol,
							"mint", token.Address,
						)
						// Run a second observation window to check if organic buyers returned.
						observer := flow.NewObserver()
						tradeObservers.Store(token.Address, observer)
						pumpScanner.SubscribeToken(token.Address)

						time.Sleep(time.Duration(cfg.TradeObservationSecs) * time.Second)

						recheck := observer.Result()
						tradeObservers.Delete(token.Address)

						if recheck.OFI < cfg.MinOFIThreshold || recheck.TradeCount < cfg.MinTradesBeforeBuy {
							log.Debug("delayed entry: OFI still weak post-dump, skipping",
								"token", token.Symbol,
								"ofi", recheck.OFI,
								"trades", recheck.TradeCount,
							)
							pumpScanner.UnsubscribeToken(token.Address)
							continue
						}

						log.Info("delayed entry: organic activity recovered, proceeding to buy",
							"token", token.Symbol,
							"ofi", recheck.OFI,
							"trades", recheck.TradeCount,
						)
						// Re-enter the buy pipeline.
						go func(t scanner.NewToken) {
							workerSem <- struct{}{} // acquire
							defer func() { <-workerSem }()
							processToken(ctx, t, tokenFilter, sizer, executors, store, notifier, rep, reportStore,
								solCB, pumpScanner, &tradeObservers, perfTracker, bundleDetector, reputationDB, botDB, delayedEntry, &creatorMap, &positionAnomalyDetectors, survivalModel, thompsonSampler, cfg, log)
						}(token)
					}
				}
			}
		}()
	}

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
				// Feed trade into pre-buy observer (even for zero mcap trades).
				if v, ok := tradeObservers.Load(trade.Mint); ok {
					v.(*flow.Observer).RecordTrade(trade.TxType, trade.SolAmount, trade.MarketCapSol)
				}

				// Track sell pressure for open positions (Phase 5B).
				if trade.TxType == "sell" && trade.SolAmount >= 5.0 {
					mon.RecordSellPressure(ctx, trade.Mint, trade.SolAmount)
				}

				// CUSUM anomaly detection for open positions (Phase 3).
				if cfg.CUSUMEnabled {
					if v, ok := positionAnomalyDetectors.Load(trade.Mint); ok {
						detector := v.(*flow.TokenAnomalyDetector)
						sig := detector.Feed(trade.TxType, trade.SolAmount, time.Now())
						if sig.SellOnset {
							log.Info("CUSUM sell onset detected for open position",
								"mint", trade.Mint,
								"sol_amount", trade.SolAmount,
							)
							// Flag positions with CUSUM sell-onset and trigger immediate exit evaluation.
							for _, pos := range store.AllOpenPositions() {
								if pos.TokenAddress == trade.Mint {
									store.UpdatePosition(pos.ID, func(p *state.Position) {
										p.CUSUMSellOnset = true
									})
									mon.EvaluatePosition(ctx, pos.ID)
								}
							}
						}
					}
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

					// Trade-feed-driven exit evaluation (Phase 5A).
					mon.EvaluatePosition(ctx, pos.ID)
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
				// Note: do NOT reset pause cycles here. The escalating pause
				// (1h→2h→4h→8h) should persist until consecutive losses are
				// broken by a win. Resetting hourly undermines the protection.
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
				store.SetCBState(state.ChainSolana, solCB.ExportState())
				if err := store.SaveSnapshot(cfg.StateSnapshotPath); err != nil {
					log.Warn("failed to save state snapshot", "err", err)
				}
			}
		}
	}()

	// Capital capacity estimate at startup.
	maxExposure := float64(cfg.MaxPositionsTotal) * cfg.SolanaSnipeAmount
	gasNeeds := 200.0 * cfg.GasCostPerTxSOL * 2
	hourlyDeploy := float64(cfg.MaxSnipesPerHour) * cfg.SolanaSnipeAmount
	log.Info("capital capacity estimate",
		"max_concurrent_exposure_sol", fmt.Sprintf("%.2f", maxExposure),
		"hourly_deploy_capacity_sol", fmt.Sprintf("%.2f", hourlyDeploy),
		"gas_budget_sol", fmt.Sprintf("%.4f", gasNeeds),
		"reserve_needed_sol", cfg.SweepReserveSOL,
		"bank_address", cfg.BankAddress,
		"sweep_enabled", cfg.BankAddress != "",
	)

	// Capital refresh + gas refueler + capital sweep (every 30s).
	var refueler *gas.Refueler
	if cfg.GasRefuelEnabled {
		refueler = gas.NewRefueler(solClient, store, mon, gas.RefuelerConfig{
			Threshold:   cfg.GasRefuelThreshold,
			CooldownMin: cfg.GasRefuelCooldownMin,
			Shadow:      !cfg.IsLive(),
		}, log)
		log.Info("gas refueler enabled",
			"threshold", cfg.GasRefuelThreshold,
		)
	}

	sweeper := capital.NewSweeper(solClient, store, capital.SweeperConfig{
		BankAddress:   cfg.BankAddress,
		ReserveSOL:    cfg.SweepReserveSOL,
		IdleThreshold: cfg.SweepIdleThreshold,
		IdleMinutes:   cfg.SweepIdleMinutes,
		CooldownMin:   cfg.SweepCooldownMin,
		MinSweepSOL:   cfg.SweepMinSOL,
		Shadow:        !cfg.IsLive(),
	}, log)
	if sweeper != nil {
		log.Info("capital sweeper enabled",
			"bank_address", cfg.BankAddress,
			"reserve_sol", cfg.SweepReserveSOL,
			"idle_threshold", cfg.SweepIdleThreshold,
			"idle_minutes", cfg.SweepIdleMinutes,
		)
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		capitalTicker := time.NewTicker(30 * time.Second)
		defer capitalTicker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-capitalTicker.C:
				// Refresh wallet balance and update dynamic position limits.
				if bal, err := solClient.GetBalanceWithFallback(ctx); err == nil {
					store.SetGasBalance(state.ChainSolana, bal)

					if cfg.DynamicPositionLimits {
						openValue := 0.0
						for _, pos := range store.OpenPositions(state.ChainSolana) {
							openValue += pos.Amount
						}
						available := bal - cfg.MinGasReserveSOL - openValue
						sizer.UpdateCapital(state.ChainSolana, available)
						log.Debug("capital refresh",
							"chain", "solana",
							"balance", bal,
							"open_value", openValue,
							"available", available,
							"dynamic_max", sizer.DynamicMaxPerChain(state.ChainSolana),
						)
					}
				}

				// Gas refueler check.
				if refueler != nil {
					refueler.Check(ctx)
				}

				// Capital sweep check.
				if sweeper != nil {
					sweeper.Check(ctx)
				}
			}
		}
	}()

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
			store.SetCBState(state.ChainSolana, solCB.ExportState())
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
				processToken(ctx, t, tokenFilter, sizer, executors, store, notifier, rep, reportStore, solCB, pumpScanner, &tradeObservers, perfTracker, bundleDetector, reputationDB, botDB, delayedEntry, &creatorMap, &positionAnomalyDetectors, survivalModel, thompsonSampler, cfg, log)
			}(token)
		}
	}
}

// Inter-buy cooldown: prevents burst trading by enforcing minimum time between buys.
var (
	lastBuyMu   sync.Mutex
	lastBuyTime time.Time
)

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
	pumpScanner *scanner.PumpFunScanner,
	tradeObservers *sync.Map,
	perfTracker *risk.PerformanceTracker,
	bundleDetector *filter.BundleDetector,
	reputationDB *filter.ReputationDB,
	botDB *flow.BotDB,
	delayedEntry *flow.DelayedEntry, // nil if disabled
	creatorMap *sync.Map, // tokenAddress -> creator for reputation tracking
	positionAnomalyDetectors *sync.Map, // mint -> *flow.TokenAnomalyDetector
	survivalModel *flow.SurvivalModel, // nil if disabled
	thompsonSampler *flow.ThompsonSampler, // nil if disabled
	cfg *config.Config,
	log *slog.Logger,
) {
	store.IncrTokensSeen()

	result := f.Evaluate(ctx, token)

	// Apply creator reputation score modifier.
	if token.Creator != "" {
		modifier := reputationDB.ScoreModifier(token.Creator)
		if modifier != 0 {
			result.Score += modifier
			tier := reputationDB.GetTier(token.Creator)
			log.Debug("creator reputation applied",
				"token", token.Symbol,
				"creator", token.Creator,
				"tier", tier,
				"modifier", modifier,
				"adjusted_score", result.Score,
			)
			// Re-check approval after score adjustment.
			result.Approved = result.Score >= cfg.MinScoreThreshold
		}
	}

	if !result.Approved {
		return
	}

	store.IncrTokensPassed()

	// Volume-aware pre-buy check with order flow analysis (Phase 1).
	var obsResult flow.ObservationResult
	if cfg.MinTradesBeforeBuy > 0 && token.Chain == state.ChainSolana {
		observer := flow.NewObserver()
		tradeObservers.Store(token.Address, observer)
		pumpScanner.SubscribeToken(token.Address)

		// Adjust observation duration based on creator reputation.
		observeDuration := time.Duration(cfg.TradeObservationSecs) * time.Second
		if token.Creator != "" {
			mult := reputationDB.ObservationMultiplier(token.Creator)
			observeDuration = time.Duration(float64(observeDuration) * mult)
		}

		// Adaptive observation: poll every 500ms and buy early if quality is high.
		if cfg.AdaptiveObservation {
			maxDuration := time.Duration(cfg.ObservationMaxSecs) * time.Second
			if maxDuration > observeDuration {
				observeDuration = maxDuration
			}
			stopper := flow.NewAdaptiveStopper(int(observeDuration.Seconds()))
			startTime := time.Now()
			ticker := time.NewTicker(500 * time.Millisecond)
			earlyBuy := false

		adaptiveLoop:
			for {
				select {
				case <-ctx.Done():
					ticker.Stop()
					tradeObservers.Delete(token.Address)
					pumpScanner.UnsubscribeToken(token.Address)
					return
				case <-ticker.C:
					elapsed := time.Since(startTime)
					obsResult = observer.Result()

					// Minimum trades gate: need at least MinTradesBeforeBuy.
					if obsResult.TradeCount >= cfg.MinTradesBeforeBuy {
						quality := flow.QualityScore(obsResult)
						if stopper.ShouldBuy(elapsed, quality) {
							earlyBuy = elapsed < observeDuration-500*time.Millisecond
							if earlyBuy {
								log.Debug("adaptive observation: early buy triggered",
									"token", token.Symbol,
									"elapsed", elapsed,
									"quality", quality,
									"threshold", stopper.Threshold(elapsed),
								)
							}
							ticker.Stop()
							break adaptiveLoop
						}
					}

					if elapsed >= observeDuration {
						ticker.Stop()
						break adaptiveLoop
					}
				}
			}
		} else {
			select {
			case <-time.After(observeDuration):
			case <-ctx.Done():
				tradeObservers.Delete(token.Address)
				pumpScanner.UnsubscribeToken(token.Address)
				return
			}
			obsResult = observer.Result()
		}

		tradeObservers.Delete(token.Address)

		// Basic volume check.
		if obsResult.TradeCount < cfg.MinTradesBeforeBuy {
			log.Debug("token failed volume check, skipping buy",
				"token", token.Symbol,
				"trades", obsResult.TradeCount,
				"required", cfg.MinTradesBeforeBuy,
			)
			pumpScanner.UnsubscribeToken(token.Address)
			return
		}

		// Order flow imbalance check (Phase 1A).
		if cfg.MinOFIThreshold > 0 && obsResult.OFI < cfg.MinOFIThreshold {
			log.Debug("token failed OFI check, skipping buy",
				"token", token.Symbol,
				"ofi", obsResult.OFI,
				"required", cfg.MinOFIThreshold,
			)
			pumpScanner.UnsubscribeToken(token.Address)
			return
		}

		// Price velocity check (Phase 1B).
		if cfg.MaxObservationGrowthPct > 0 && obsResult.GrowthRate*100 > cfg.MaxObservationGrowthPct {
			log.Debug("token growth too fast (pump-and-dump), skipping",
				"token", token.Symbol,
				"growth_pct", obsResult.GrowthRate*100,
				"max", cfg.MaxObservationGrowthPct,
			)
			pumpScanner.UnsubscribeToken(token.Address)
			return
		}

		// Anti-bot detection (Phase 1C).
		if cfg.MinTradeTimingCV > 0 && obsResult.IsBotLike {
			log.Debug("token has bot-like trade pattern, skipping",
				"token", token.Symbol,
				"timing_cv", obsResult.TimingCV,
				"threshold", cfg.MinTradeTimingCV,
			)
			pumpScanner.UnsubscribeToken(token.Address)
			return
		}

		// Bundle detection: check if creator is among early buyers (Phase 3A).
		if token.Creator != "" && obsResult.BotBuyCount > 0 {
			// Build early buyer list from bot buy addresses. For now, use creator match
			// check — the observation window captures trade events, not individual addresses.
			// In a full implementation, Observer would track buyer addresses.
			if bundleDetector.IsBundled(token.Creator, []string{}) {
				log.Debug("token flagged as bundled launch, skipping",
					"token", token.Symbol,
					"creator", token.Creator,
				)
				pumpScanner.UnsubscribeToken(token.Address)
				return
			}
		}

		// Bot dump delayed entry (Phase 3D): if bots are detected, delay buy.
		if delayedEntry != nil && obsResult.SuggestDelay {
			if delayedEntry.Schedule(token.Address, token) {
				log.Info("bot activity detected, scheduling delayed re-entry",
					"token", token.Symbol,
					"bot_buys", obsResult.BotBuyCount,
					"round_amounts", obsResult.RoundAmountCount,
					"delay_sec", cfg.BotDumpDelaySec,
				)
			}
			pumpScanner.UnsubscribeToken(token.Address)
			return
		}

		// Liquidity velocity check: strongest graduation predictor.
		if cfg.MinLiquidityVelocity > 0 && obsResult.LiquidityVelocity < cfg.MinLiquidityVelocity {
			log.Debug("token failed liquidity velocity check, skipping",
				"token", token.Symbol,
				"velocity", obsResult.LiquidityVelocity,
				"required", cfg.MinLiquidityVelocity,
			)
			pumpScanner.UnsubscribeToken(token.Address)
			return
		}

		// OFI acceleration check: fading buy pressure = likely top.
		if cfg.MaxOFIDeceleration != 0 && obsResult.OFIAcceleration < cfg.MaxOFIDeceleration {
			log.Debug("token OFI decelerating, skipping",
				"token", token.Symbol,
				"ofi_accel", obsResult.OFIAcceleration,
				"threshold", cfg.MaxOFIDeceleration,
			)
			pumpScanner.UnsubscribeToken(token.Address)
			return
		}

		// CUSUM anomaly detection: skip if sell onset detected during observation.
		if cfg.CUSUMEnabled && obsResult.AnomalyDetected {
			log.Debug("CUSUM anomaly detected during observation, skipping",
				"token", token.Symbol,
				"sell_onset", obsResult.AnomalySignal.SellOnset,
				"buy_collapse", obsResult.AnomalySignal.BuyCollapse,
				"size_shift", obsResult.AnomalySignal.SizeShift,
			)
			pumpScanner.UnsubscribeToken(token.Address)
			return
		}

		// Survival model: meta-filter combining all signals through trained lens.
		if survivalModel != nil {
			survScore := survivalModel.SurvivalScore(obsResult, result.Score)
			if survScore < -0.5 {
				log.Debug("token failed survival model check, skipping",
					"token", token.Symbol,
					"survival_score", survScore,
				)
				pumpScanner.UnsubscribeToken(token.Address)
				return
			}
		}

		// Thompson sampling: skip if sampled win rate for this token type is too low.
		if thompsonSampler != nil {
			bucketKey := flow.BucketKey(obsResult, result.Score)
			sampledScore := thompsonSampler.Score(bucketKey)
			if sampledScore < 0.15 {
				log.Debug("thompson sampling: token type has poor history, skipping",
					"token", token.Symbol,
					"bucket", bucketKey,
					"sampled_score", sampledScore,
					"win_rate", thompsonSampler.WinRate(bucketKey),
				)
				pumpScanner.UnsubscribeToken(token.Address)
				return
			}
		}

		log.Debug("token passed observation checks",
			"token", token.Symbol,
			"trades", obsResult.TradeCount,
			"ofi", obsResult.OFI,
			"ofi_accel", obsResult.OFIAcceleration,
			"velocity", obsResult.LiquidityVelocity,
			"entropy", obsResult.TradeEntropy,
			"curve_progress", obsResult.CurveProgress,
			"growth_pct", obsResult.GrowthRate*100,
			"timing_cv", obsResult.TimingCV,
			"bot_buys", obsResult.BotBuyCount,
			"bot_sniped", obsResult.BotSniped,
		)
	}

	if !solCB.CanSnipe() {
		log.Debug("snipe blocked by circuit breaker", "chain", token.Chain, "token", token.Symbol)
		if token.Chain == state.ChainSolana {
			pumpScanner.UnsubscribeToken(token.Address)
		}
		return
	}

	// Inter-buy cooldown: spread risk by enforcing minimum time between buys.
	if cfg.SnipeCooldownSec > 0 {
		lastBuyMu.Lock()
		elapsed := time.Since(lastBuyTime)
		lastBuyMu.Unlock()
		cooldown := time.Duration(cfg.SnipeCooldownSec) * time.Second
		if elapsed < cooldown {
			log.Debug("snipe cooldown active", "chain", token.Chain, "token", token.Symbol, "remaining", cooldown-elapsed)
			if token.Chain == state.ChainSolana {
				pumpScanner.UnsubscribeToken(token.Address)
			}
			return
		}
	}

	if !store.TryReserveSlot(token.Chain, sizer.DynamicMaxPerChain(token.Chain), sizer.DynamicMaxTotal()) {
		log.Debug("position limit reached", "chain", token.Chain)
		if token.Chain == state.ChainSolana {
			pumpScanner.UnsubscribeToken(token.Address)
		}
		return
	}

	// Extract mcap for dynamic slippage and liquidity-aware sizing.
	mcapSOL := 0.0
	if v, ok := token.Metadata["marketCapSol"]; ok {
		if mc, ok := v.(float64); ok {
			mcapSOL = mc
		}
	}

	size := sizer.SizeWithLiquidity(token.Chain, result.Score, mcapSOL)
	if size <= 0 {
		log.Debug("position size zero (liquidity too thin or gas low)",
			"token", token.Symbol,
			"mcap_sol", mcapSOL,
		)
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
		Score:        result.Score,
		McapSOL:      mcapSOL,
	})

	if buyResult.Error != nil {
		solCB.RecordError()
		store.ReleaseSlot(token.Chain)
		pumpScanner.UnsubscribeToken(token.Address)
		return
	}

	solCB.RecordSnipe()

	// Update inter-buy cooldown timestamp.
	lastBuyMu.Lock()
	lastBuyTime = time.Now()
	lastBuyMu.Unlock()

	// Deduct buy gas.
	store.DeductGas(token.Chain, buyResult.GasCost)

	posID := fmt.Sprintf("%s-%s-%d", token.Chain, executor.SafePrefix(token.Address, 8), time.Now().UnixMilli())
	store.AddPosition(&state.Position{
		ID:            posID,
		Chain:         token.Chain,
		TokenAddress:  token.Address,
		TokenSymbol:   token.Symbol,
		EntryPrice:    buyResult.Price,
		CurrentPrice:  buyResult.Price,
		PeakPrice:     buyResult.Price,
		Amount:        buyResult.Amount,
		TokenBalance:  buyResult.TokenAmount,
		EntryTime:     time.Now(),
		LastTradeTime: time.Now(),
		EntryGasCost:  buyResult.GasCost,
		FilterScore:   result.Score,
		SignalScores:  result.SignalBreakdown,
		OFI:           obsResult.OFI,
		ObsGrowthRate: obsResult.GrowthRate,
		ObsTimingCV:   obsResult.TimingCV,
		EntryHeat:         solCB.Heat(),
		BotBuyCount:       obsResult.BotBuyCount,
		LiquidityVelocity: obsResult.LiquidityVelocity,
		OFIAcceleration:   obsResult.OFIAcceleration,
		TradeEntropy:      obsResult.TradeEntropy,
		CurveProgress:     obsResult.CurveProgress,
		MaxTradeSize:      obsResult.MaxTradeSize,
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

	// Record observation features to Postgres for survival model training.
	if reportStore != nil {
		if err := reportStore.InsertObservation(ctx, reporter.TokenObservation{
			ID:                posID,
			TokenAddress:      token.Address,
			TokenSymbol:       token.Symbol,
			Chain:             string(token.Chain),
			Shadow:            shadow,
			LiquidityVelocity: obsResult.LiquidityVelocity,
			OFI:               obsResult.OFI,
			OFIAcceleration:   obsResult.OFIAcceleration,
			TradeEntropy:      obsResult.TradeEntropy,
			TimingCV:          obsResult.TimingCV,
			BotBuyCount:       obsResult.BotBuyCount,
			BuyCount:          obsResult.BuyCount,
			CurveProgress:     obsResult.CurveProgress,
			FilterScore:       result.Score,
			GrowthRate:        obsResult.GrowthRate,
			MaxTradeSize:      obsResult.MaxTradeSize,
			TradeCount:        obsResult.TradeCount,
			SellCount:         obsResult.SellCount,
			EntryHeat:         solCB.Heat(),
			EntryMcapSOL:      mcapSOL,
			EntrySizeSOL:      size,
		}); err != nil {
			log.Warn("failed to insert observation", "err", err)
		}
	}

	// Store creator for reputation tracking + future lookups.
	if token.Creator != "" {
		creatorMap.Store(token.Address, token.Creator)
	}
	if reportStore != nil && token.Creator != "" {
		if err := reportStore.UpsertTokenCreator(ctx, token.Address, token.Creator); err != nil {
			log.Warn("failed to upsert token creator", "err", err)
		}
	}

	notifier.Snipe(ctx, string(token.Chain), token.Symbol, token.Address, size, buyResult.Price, shadow)

	// Start CUSUM anomaly detector for this position.
	if cfg.CUSUMEnabled {
		positionAnomalyDetectors.Store(token.Address, flow.NewTokenAnomalyDetector())
	}

	// Subscribe to real-time trade events for this token's price updates.
	if token.Chain == state.ChainSolana {
		pumpScanner.SubscribeToken(token.Address)
	}

	// Run honeypot check asynchronously after the buy. If the token is flagged,
	// the position will be force-closed without blocking the pipeline.
	go f.CheckHoneypotAsync(ctx, token.Chain, token.Address, posID, store, notifier, log)
}

// heliusHolderAdapter wraps the Solana client to implement filter.HolderChecker.
type heliusHolderAdapter struct {
	client *solanaclient.Client
}

func (a *heliusHolderAdapter) GetTokenHolders(ctx context.Context, mint string) (filter.HolderDistribution, error) {
	dist, err := a.client.GetTokenHolders(ctx, mint)
	if err != nil {
		return filter.HolderDistribution{}, err
	}
	return filter.HolderDistribution{
		TotalHolders:  dist.TotalHolders,
		TopHolderPct:  dist.TopHolderPct,
		Top5HolderPct: dist.Top5HolderPct,
	}, nil
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
