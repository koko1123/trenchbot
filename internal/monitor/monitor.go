package monitor

import (
	"context"
	"log/slog"
	"time"

	"github.com/cindocode/trenchbot/internal/clock"
	"github.com/cindocode/trenchbot/internal/executor"
	"github.com/cindocode/trenchbot/internal/notify"
	"github.com/cindocode/trenchbot/internal/state"
)

type ExitConfig struct {
	Tranche1Pct   float64 // sell this % at Tranche1X
	Tranche1X     float64 // first exit multiplier (e.g. 2x)
	Tranche2Pct   float64 // sell this % at Tranche2X
	Tranche2X     float64 // second exit multiplier (e.g. 5x)
	TrailingStop  float64 // % drop from peak to exit remaining (e.g. 40%)
	StopLossPct   float64 // hard stop-loss from entry (e.g. 50%)
	StaleMinutes  int     // auto-exit if no buys for this many minutes
}

func DefaultExitConfig() ExitConfig {
	return ExitConfig{
		Tranche1Pct:  25,
		Tranche1X:    2.0,
		Tranche2Pct:  50,
		Tranche2X:    5.0,
		TrailingStop: 40,
		StopLossPct:  50,
		StaleMinutes: 30,
	}
}

type Monitor struct {
	store      *state.Store
	executors  map[state.Chain]executor.Executor
	notifier   notify.Notifier
	exitCfg    ExitConfig
	clock      clock.Clock
	log        *slog.Logger
	shadow     bool
}

func New(store *state.Store, executors map[state.Chain]executor.Executor, notifier notify.Notifier, exitCfg ExitConfig, clk clock.Clock, shadow bool, log *slog.Logger) *Monitor {
	return &Monitor{
		store:     store,
		executors: executors,
		notifier:  notifier,
		exitCfg:   exitCfg,
		clock:     clk,
		shadow:    shadow,
		log:       log,
	}
}

func (m *Monitor) Run(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.CheckPositions(ctx)
		}
	}
}

func (m *Monitor) CheckPositions(ctx context.Context) {
	positions := m.store.AllOpenPositions()
	for _, pos := range positions {
		m.evaluateExit(ctx, pos)
	}
}

func (m *Monitor) evaluateExit(ctx context.Context, pos *state.Position) {
	if pos.CurrentPrice <= 0 || pos.EntryPrice <= 0 {
		return
	}

	multiplier := pos.CurrentPrice / pos.EntryPrice
	dropFromPeak := 0.0
	if pos.PeakPrice > 0 {
		dropFromPeak = ((pos.PeakPrice - pos.CurrentPrice) / pos.PeakPrice) * 100
	}

	// Update peak price
	if pos.CurrentPrice > pos.PeakPrice {
		m.store.UpdatePosition(pos.ID, func(p *state.Position) {
			p.PeakPrice = pos.CurrentPrice
		})
	}

	// Hard stop-loss
	if multiplier <= (1 - m.exitCfg.StopLossPct/100) {
		m.executeSell(ctx, pos, 100-pos.SoldPct, "stop-loss")
		return
	}

	// Tranche 1: sell 25% at 2x
	if multiplier >= m.exitCfg.Tranche1X && pos.SoldPct < m.exitCfg.Tranche1Pct {
		m.executeSell(ctx, pos, m.exitCfg.Tranche1Pct, "tranche-1")
		return
	}

	// Tranche 2: sell 50% at 5x
	if multiplier >= m.exitCfg.Tranche2X && pos.SoldPct < m.exitCfg.Tranche1Pct+m.exitCfg.Tranche2Pct {
		m.executeSell(ctx, pos, m.exitCfg.Tranche2Pct, "tranche-2")
		return
	}

	// Trailing stop on remaining position
	if pos.SoldPct >= m.exitCfg.Tranche1Pct+m.exitCfg.Tranche2Pct {
		if dropFromPeak >= m.exitCfg.TrailingStop {
			m.executeSell(ctx, pos, 100-pos.SoldPct, "trailing-stop")
			return
		}
	}

	// Stale position exit
	if m.exitCfg.StaleMinutes > 0 {
		staleThreshold := time.Duration(m.exitCfg.StaleMinutes) * time.Minute
		if m.clock.Since(pos.EntryTime) > staleThreshold && multiplier < 1.5 {
			m.executeSell(ctx, pos, 100-pos.SoldPct, "stale-position")
			return
		}
	}
}

func (m *Monitor) executeSell(ctx context.Context, pos *state.Position, sellPct float64, reason string) {
	exec, ok := m.executors[pos.Chain]
	if !ok {
		m.log.Error("no executor for chain", "chain", pos.Chain)
		return
	}

	result := exec.Sell(ctx, executor.SellParams{
		Chain:        pos.Chain,
		TokenAddress: pos.TokenAddress,
		TokenSymbol:  pos.TokenSymbol,
		AmountPct:    sellPct,
		Shadow:       m.shadow,
	})

	if result.Error != nil {
		m.log.Error("sell failed",
			"token", pos.TokenSymbol,
			"reason", reason,
			"err", result.Error,
		)
		return
	}

	pnlPct := ((pos.CurrentPrice / pos.EntryPrice) - 1) * 100

	m.store.UpdatePosition(pos.ID, func(p *state.Position) {
		p.SoldPct += sellPct
		if p.SoldPct >= 100 {
			p.Closed = true
			p.PnL = pnlPct
		}
	})

	m.log.Info("SELL",
		"chain", pos.Chain,
		"token", pos.TokenSymbol,
		"sell_pct", sellPct,
		"reason", reason,
		"pnl_pct", pnlPct,
		"tx", result.TxHash,
	)

	m.notifier.Exit(ctx, string(pos.Chain), pos.TokenSymbol, pnlPct, reason)
}
