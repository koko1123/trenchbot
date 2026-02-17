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

	solCB := risk.NewCircuitBreaker(risk.CircuitBreakerConfig{
		Chain:              state.ChainSolana,
		MaxDrawdownPct:     50,
		DailyLossLimitPct:  cfg.DailyLossLimitPct,
		ConsecutiveLossCap: cfg.ConsecutiveLossPauseThresh,
		MaxSnipesPerHour:   cfg.MaxSnipesPerHour,
		StartingEquity:     1200,
	}, store, clk, log)

	var bnbCB *risk.CircuitBreaker
	if bnbClient != nil {
		bnbCB = risk.NewCircuitBreaker(risk.CircuitBreakerConfig{
			Chain:              state.ChainBNB,
			MaxDrawdownPct:     50,
			DailyLossLimitPct:  cfg.DailyLossLimitPct,
			ConsecutiveLossCap: cfg.ConsecutiveLossPauseThresh,
			MaxSnipesPerHour:   cfg.MaxSnipesPerHour,
			StartingEquity:     800,
		}, store, clk, log)
	}

	// Initialize gas balances.
	store.SetGasBalance(state.ChainSolana, cfg.GasBudgetSOL)
	store.SetGasBalance(state.ChainBNB, cfg.GasBudgetBNB)

	sizer := risk.NewPositionSizer(store, cfg.SolanaSnipeAmount, cfg.BNBSnipeAmount, cfg.DailyLossLimitPct)
	sizer.SetGasReserves(cfg.MinGasReserveSOL, cfg.MinGasReserveBNB)
	tokenFilter := filter.New(cfg.MinScoreThreshold, log)

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
		fourScanner := scanner.NewFourMemeScanner(cfg.BitqueryAPIURL, cfg.BitqueryAPIKey, log)
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
				if solCB.IsHalted() {
					log.Warn("solana circuit breaker HALTED")
				}
				if bnbCB != nil && bnbCB.IsHalted() {
					log.Warn("bnb circuit breaker HALTED")
				}
			}
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		summaryTicker := time.NewTicker(1 * time.Hour)
		defer summaryTicker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-summaryTicker.C:
				positions := store.AllOpenPositions()
				msg := fmt.Sprintf("Hourly Summary: %d open positions", len(positions))
				notifier.Send(ctx, msg)
			}
		}
	}()

	log.Info("pipeline started, waiting for tokens...")
	for {
		select {
		case <-ctx.Done():
			log.Info("shutting down pipeline...")
			wg.Wait()
			log.Info("trenchbot stopped")
			return
		case token := <-tokenCh:
			go processToken(ctx, token, tokenFilter, sizer, executors, store, notifier, solCB, bnbCB, cfg, log)
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
	solCB *risk.CircuitBreaker,
	bnbCB *risk.CircuitBreaker,
	cfg *config.Config,
	log *slog.Logger,
) {
	result := f.Evaluate(token)
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

	if store.OpenPositionCount(token.Chain) >= cfg.MaxPositionsPerChain {
		log.Info("max positions per chain reached", "chain", token.Chain)
		return
	}
	if store.TotalOpenPositionCount() >= cfg.MaxPositionsTotal {
		log.Info("max total positions reached")
		return
	}

	size := sizer.Size(token.Chain, result.Score)
	if size <= 0 {
		return
	}

	exec, ok := executors[token.Chain]
	if !ok {
		log.Error("no executor for chain", "chain", token.Chain)
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
		return
	}

	cb.RecordSnipe()

	// Deduct buy gas.
	store.DeductGas(token.Chain, buyResult.GasCost)

	posID := fmt.Sprintf("%s-%s-%d", token.Chain, token.Address[:8], time.Now().UnixMilli())
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

	store.AddTrade(state.Trade{
		ID:           buyResult.TxHash,
		Chain:        token.Chain,
		TokenAddress: token.Address,
		TokenSymbol:  token.Symbol,
		Side:         "buy",
		Price:        buyResult.Price,
		Amount:       buyResult.Amount,
		Timestamp:    time.Now(),
		TxHash:       buyResult.TxHash,
		Shadow:       shadow,
	})

	notifier.Snipe(ctx, string(token.Chain), token.Symbol, token.Address, size, buyResult.Price, shadow)
}
