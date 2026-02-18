package monitor

import (
	"context"
	"log/slog"
	"time"

	"github.com/cindocode/trenchbot/internal/clock"
	"github.com/cindocode/trenchbot/internal/executor"
	"github.com/cindocode/trenchbot/internal/notify"
	"github.com/cindocode/trenchbot/internal/risk"
	"github.com/cindocode/trenchbot/internal/state"
)

type ExitConfig struct {
	Tranche1Pct            float64 // sell this % at Tranche1X
	Tranche1X              float64 // first exit multiplier (e.g. 2x)
	Tranche2Pct            float64 // sell this % at Tranche2X
	Tranche2X              float64 // second exit multiplier (e.g. 5x)
	TrailingStop           float64 // % drop from peak to exit remaining (e.g. 40%)
	StopLossPct            float64 // hard stop-loss from entry (e.g. 50%)
	StaleMinutes           int     // auto-exit if no buys for this many minutes
	StaleMinutes2          int     // max hold time for sub-tranche1 positions, 0 = disabled
	EarlyTrailingThreshold   float64 // multiplier above which early trailing activates (e.g. 3.0x)
	EarlyTrailingStop        float64 // % drop from peak to trigger early trailing exit (e.g. 30%)
	StaleMultiplierThreshold float64 // positions above this multiplier are not considered stale (default 1.5)
	UniversalTrailingThreshold float64       // peak multiplier above which universal trailing activates (e.g. 1.15)
	UniversalTrailingStop      float64       // % drop from peak to exit any position (e.g. 20%)
	NoTradeTimeout             time.Duration // exit if no trade events for this long (e.g. 2m)
	NoTradeMaxMult             float64       // only exit dead tokens below this multiplier (e.g. 1.1)
}

func DefaultExitConfig() ExitConfig {
	return ExitConfig{
		Tranche1Pct:              25,
		Tranche1X:                1.5,
		Tranche2Pct:              50,
		Tranche2X:                5.0,
		TrailingStop:             40,
		StopLossPct:              30,
		StaleMinutes:             30,
		StaleMinutes2:            60,
		EarlyTrailingThreshold:   3.0,
		EarlyTrailingStop:        30,
		StaleMultiplierThreshold: 1.5,
		UniversalTrailingThreshold: 1.15,
		UniversalTrailingStop:      20,
		NoTradeTimeout:             2 * time.Minute,
		NoTradeMaxMult:             1.1,
	}
}

type Monitor struct {
	store           *state.Store
	executors       map[state.Chain]executor.Executor
	notifier        notify.Notifier
	exitCfg         ExitConfig
	clock           clock.Clock
	log             *slog.Logger
	shadow          bool
	circuitBreakers map[state.Chain]*risk.CircuitBreaker
	onPositionClose func(chain state.Chain, tokenAddress string)
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

func (m *Monitor) SetCircuitBreakers(cbs map[state.Chain]*risk.CircuitBreaker) {
	m.circuitBreakers = cbs
}

func (m *Monitor) SetOnPositionClose(fn func(chain state.Chain, tokenAddress string)) {
	m.onPositionClose = fn
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
	if pos.CurrentPrice <= 0 {
		return
	}
	entryPrice := pos.EntryPrice
	if entryPrice <= 0 {
		entryPrice = pos.CurrentPrice // treat as break-even so stops still fire
	}
	multiplier := pos.CurrentPrice / entryPrice
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

	// Early trailing stop: after tranche-1 but before tranche-2, if peak was high enough
	if m.exitCfg.EarlyTrailingThreshold > 0 &&
		pos.SoldPct >= m.exitCfg.Tranche1Pct &&
		pos.SoldPct < m.exitCfg.Tranche1Pct+m.exitCfg.Tranche2Pct {
		peakMult := pos.PeakPrice / entryPrice
		if peakMult >= m.exitCfg.EarlyTrailingThreshold && dropFromPeak >= m.exitCfg.EarlyTrailingStop {
			m.executeSell(ctx, pos, 100-pos.SoldPct, "early-trailing-stop")
			return
		}
	}

	// Trailing stop on remaining position (after both tranches completed)
	if pos.SoldPct >= m.exitCfg.Tranche1Pct+m.exitCfg.Tranche2Pct {
		if dropFromPeak >= m.exitCfg.TrailingStop {
			m.executeSell(ctx, pos, 100-pos.SoldPct, "trailing-stop")
			return
		}
	}

	// Universal trailing stop: protect gains on any position once it's been up.
	if m.exitCfg.UniversalTrailingThreshold > 0 {
		peakMult := pos.PeakPrice / entryPrice
		if peakMult >= m.exitCfg.UniversalTrailingThreshold && dropFromPeak >= m.exitCfg.UniversalTrailingStop {
			m.executeSell(ctx, pos, 100-pos.SoldPct, "universal-trailing-stop")
			return
		}
	}

	// No-trade-activity exit: dead tokens with no price feed activity.
	if m.exitCfg.NoTradeTimeout > 0 && !pos.LastTradeTime.IsZero() {
		if m.clock.Since(pos.LastTradeTime) > m.exitCfg.NoTradeTimeout && multiplier < m.exitCfg.NoTradeMaxMult {
			m.executeSell(ctx, pos, 100-pos.SoldPct, "no-trade-activity")
			return
		}
	}

	// Stale position exit
	if m.exitCfg.StaleMinutes > 0 {
		staleThreshold := time.Duration(m.exitCfg.StaleMinutes) * time.Minute
		if m.clock.Since(pos.EntryTime) > staleThreshold && multiplier < m.exitCfg.StaleMultiplierThreshold {
			m.executeSell(ctx, pos, 100-pos.SoldPct, "stale-position")
			return
		}
	}

	// Extended stale exit: positions below tranche-1 for too long are force-exited.
	if m.exitCfg.StaleMinutes2 > 0 {
		staleThreshold2 := time.Duration(m.exitCfg.StaleMinutes2) * time.Minute
		if m.clock.Since(pos.EntryTime) > staleThreshold2 && multiplier < m.exitCfg.Tranche1X {
			m.executeSell(ctx, pos, 100-pos.SoldPct, "stale-max-hold")
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
		TokenBalance: pos.TokenBalance,
		Shadow:       m.shadow,
	})

	if result.Error != nil {
		// Capture failures inside the closure to avoid data race.
		var failures int
		m.store.UpdatePosition(pos.ID, func(p *state.Position) {
			p.SellFailures++
			failures = p.SellFailures
		})
		m.log.Error("sell failed",
			"token", pos.TokenSymbol,
			"reason", reason,
			"err", result.Error,
			"failures", failures,
		)
		// Force-close after 5 consecutive failures (likely honeypot).
		const maxSellFailures = 5
		if failures >= maxSellFailures {
			// Last-ditch attempt with maximum slippage before giving up.
			lastResult := exec.Sell(ctx, executor.SellParams{
				Chain:        pos.Chain,
				TokenAddress: pos.TokenAddress,
				TokenSymbol:  pos.TokenSymbol,
				AmountPct:    100 - pos.SoldPct,
				TokenBalance: pos.TokenBalance,
				MaxSlippage:  49,
				Shadow:       m.shadow,
			})
			if lastResult.Error == nil {
				m.log.Info("high-slippage sell succeeded",
					"token", pos.TokenSymbol,
					"tx", lastResult.TxHash,
				)
				m.store.DeductGas(pos.Chain, lastResult.GasCost)
				m.store.UpdatePosition(pos.ID, func(p *state.Position) {
					p.Closed = true
					p.SoldPct = 100
					p.TokenBalance = 0
					p.SellFailures = 0
					p.PnL = -90.0 // assume near-total loss at max slippage
				})
				m.notifier.Exit(ctx, string(pos.Chain), pos.TokenSymbol, pos.TokenAddress, -90.0, "max-slippage-sell")
				m.firePositionClose(pos.Chain, pos.TokenAddress)
				return
			}

			m.log.Error("force-closing position after max sell failures",
				"token", pos.TokenSymbol,
				"failures", failures,
			)
			m.store.UpdatePosition(pos.ID, func(p *state.Position) {
				p.Closed = true
				p.PnL = -100.0
			})
			m.notifier.Exit(ctx, string(pos.Chain), pos.TokenSymbol, pos.TokenAddress, -100.0, "force-close-honeypot")
			m.firePositionClose(pos.Chain, pos.TokenAddress)
		}
		return
	}

	// Deduct sell gas from the gas balance.
	m.store.DeductGas(pos.Chain, result.GasCost)

	// Compute P&L adjusted for gas costs (entry gas + exit gas as % of position).
	effectiveEntry := pos.EntryPrice
	if effectiveEntry <= 0 {
		effectiveEntry = pos.CurrentPrice
	}
	// Use actual sell price when available, fall back to current price.
	sellPrice := pos.CurrentPrice
	if result.Price > 0 {
		sellPrice = result.Price
	}
	rawPnlPct := ((sellPrice / effectiveEntry) - 1) * 100
	totalGas := pos.EntryGasCost + result.GasCost
	gasPctOfPosition := 0.0
	if pos.Amount > 0 {
		gasPctOfPosition = (totalGas / pos.Amount) * 100
	}
	pnlPct := rawPnlPct - gasPctOfPosition

	// Record partial PnL proportional to the fraction being sold.
	partialFraction := sellPct / 100.0
	solPnL := pos.Amount * partialFraction * (rawPnlPct / 100.0)
	m.store.UpdateDailyPnL(pos.Chain, solPnL)

	// Notify circuit breaker of win/loss.
	if cb, ok := m.circuitBreakers[pos.Chain]; ok {
		if pnlPct >= 0 {
			cb.RecordWin()
		} else {
			cb.RecordLoss()
		}
	}

	m.store.UpdatePosition(pos.ID, func(p *state.Position) {
		p.SoldPct += sellPct
		if p.SoldPct > 100 {
			p.SoldPct = 100
		}
		// Reduce token balance proportionally.
		soldFraction := sellPct / 100.0
		p.TokenBalance -= p.TokenBalance * soldFraction
		p.ExitGasCost += result.GasCost
		p.SellFailures = 0
		if p.SoldPct >= 100 {
			p.Closed = true
			p.TokenBalance = 0
			p.PnL = pnlPct
		}
	})

	m.log.Info("SELL",
		"chain", pos.Chain,
		"token", pos.TokenSymbol,
		"sell_pct", sellPct,
		"reason", reason,
		"pnl_pct", pnlPct,
		"gas_cost", totalGas,
		"tx", result.TxHash,
	)

	m.notifier.Exit(ctx, string(pos.Chain), pos.TokenSymbol, pos.TokenAddress, pnlPct, reason)

	if pos.SoldPct+sellPct >= 100 {
		m.firePositionClose(pos.Chain, pos.TokenAddress)
	}
}

func (m *Monitor) firePositionClose(chain state.Chain, tokenAddress string) {
	if m.onPositionClose != nil {
		m.onPositionClose(chain, tokenAddress)
	}
}
