package backtest

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/cindocode/trenchbot/internal/clock"
	"github.com/cindocode/trenchbot/internal/executor"
	"github.com/cindocode/trenchbot/internal/filter"
	"github.com/cindocode/trenchbot/internal/monitor"
	"github.com/cindocode/trenchbot/internal/risk"
	"github.com/cindocode/trenchbot/internal/simulation"
	"github.com/cindocode/trenchbot/internal/state"
	"github.com/cindocode/trenchbot/internal/testutil"
)

// Engine replays historical token data through the trading pipeline.
type Engine struct {
	cfg      BacktestConfig
	clk      *clock.SimClock
	store    *state.Store
	filt     *filter.Filter
	mon      *monitor.Monitor
	sizer    *risk.PositionSizer
	breaker  *risk.CircuitBreaker
	exec     *simulation.SimExecutor
	notifier *testutil.MockNotifier
	log      *slog.Logger
	report   *simulation.Report
}

// NewEngine creates a backtest engine with the given config.
func NewEngine(cfg BacktestConfig, log *slog.Logger) *Engine {
	clk := clock.NewSimClock(time.Time{}) // set to simStart in Run
	store := state.NewStore()
	store.SetPeakEquity(state.ChainSolana, cfg.StartingEquity)

	exec := simulation.NewSimExecutor(state.ChainSolana, clk, simulation.DefaultSimConfig())
	if cfg.GasCostPerTx > 0 {
		exec.SetGasCostPerTx(cfg.GasCostPerTx)
	}
	notifier := testutil.NewMockNotifier()

	// Initialize gas balance.
	if cfg.GasBudget > 0 {
		store.SetGasBalance(state.ChainSolana, cfg.GasBudget)
	}

	executors := map[state.Chain]executor.Executor{
		state.ChainSolana: exec,
	}

	breaker := risk.NewCircuitBreaker(risk.CircuitBreakerConfig{
		Chain:              state.ChainSolana,
		MaxDrawdownPct:     50,
		HeatFullPct:        15,
		ConsecutiveLossCap: 10,
		MaxSnipesPerHour:   10,
		StartingEquity:     cfg.StartingEquity,
	}, store, clk, log)

	sizer := risk.NewPositionSizer(store, 0.3)
	sizer.SetMaxPositions(5)
	sizer.SetGasReserve(cfg.GasCostPerTx * 10)
	filt := filter.New(cfg.MinScore, log)
	mon := monitor.New(store, executors, notifier, monitor.DefaultExitConfig(), clk, true, log)

	return &Engine{
		cfg:      cfg,
		clk:      clk,
		store:    store,
		filt:     filt,
		mon:      mon,
		sizer:    sizer,
		breaker:  breaker,
		exec:     exec,
		notifier: notifier,
		log:      log,
		report: &simulation.Report{
			DataSource:    "historical",
			ExitsByReason: make(map[string]int),
			ArchetypeResults: make(map[simulation.TokenArchetype]simulation.ArchetypeStats),
		},
	}
}

// Run replays the given tokens through the pipeline and returns a report.
func (e *Engine) Run(ctx context.Context, tokens []simulation.SyntheticToken, simStart time.Time) *simulation.Report {
	wallStart := time.Now()

	e.clk.Set(simStart)
	e.report.TokensGenerated = len(tokens)

	// Register all price curves with the executor.
	for _, st := range tokens {
		e.exec.RegisterCurve(st.Token.Address, st.PriceCurve)
	}

	// Compute simulation duration from the data span.
	// Use the last token's emit time plus the longest price curve duration.
	if len(tokens) > 0 {
		lastEmit := tokens[len(tokens)-1].EmitTime
		maxCurveDuration := 60 * time.Minute // default fallback
		for _, st := range tokens {
			if len(st.PriceCurve) > 0 {
				d := st.PriceCurve[len(st.PriceCurve)-1].Offset
				if d > maxCurveDuration {
					maxCurveDuration = d
				}
			}
		}
		e.report.SimulatedDuration = lastEmit + maxCurveDuration
	}

	tokenIdx := 0
	tickDuration := 1 * time.Second
	totalTicks := int(e.report.SimulatedDuration / tickDuration)

	prevPausedState := false

	for tick := 0; tick < totalTicks; tick++ {
		if ctx.Err() != nil {
			break
		}

		e.clk.Advance(tickDuration)
		simElapsed := time.Duration(tick+1) * tickDuration

		// Emit tokens that should appear by now.
		for tokenIdx < len(tokens) && tokens[tokenIdx].EmitTime <= simElapsed {
			e.processToken(ctx, tokens[tokenIdx])
			tokenIdx++
		}

		// Update prices for open positions.
		for _, pos := range e.store.AllOpenPositions() {
			newPrice := e.exec.CurrentPrice(pos.TokenAddress)
			e.store.UpdatePosition(pos.ID, func(p *state.Position) {
				p.CurrentPrice = newPrice
			})
		}

		// Run monitor every 5 simulated seconds.
		if tick%5 == 0 {
			e.mon.CheckPositions(ctx)
		}

		// Track exits from notifier.
		e.collectExits()

		// Check circuit breaker periodically.
		if tick%10 == 0 {
			e.breaker.Check(e.calculateEquity())
			if e.breaker.IsHalted() {
				e.report.CircuitBreakerHalts = 1
			}
			paused := !e.breaker.CanSnipe() && !e.breaker.IsHalted()
			if paused && !prevPausedState {
				e.report.CircuitBreakerPauses++
			}
			prevPausedState = paused
		}

		// Track max drawdown.
		equity := e.calculateEquity()
		peak := e.store.GetPeakEquity(state.ChainSolana)
		if peak > 0 {
			dd := ((peak - equity) / peak) * 100
			if dd > e.report.MaxDrawdownPct {
				e.report.MaxDrawdownPct = dd
			}
		}
	}

	// Close remaining positions at their final price.
	for _, pos := range e.store.AllOpenPositions() {
		finalPrice := e.exec.CurrentPrice(pos.TokenAddress)
		pnlPct := ((finalPrice / pos.EntryPrice) - 1) * 100
		e.store.UpdatePosition(pos.ID, func(p *state.Position) {
			p.Closed = true
			p.PnL = pnlPct
			p.CurrentPrice = finalPrice
		})
		e.recordTradePnL(pnlPct)
		e.report.ExitsByReason["end-of-sim"]++
	}

	e.report.GasSpent = e.store.GetGasSpent(state.ChainSolana)
	e.report.GasRemaining = e.store.GetGasBalance(state.ChainSolana)
	totalTrades := e.report.WinCount + e.report.LossCount
	if totalTrades > 0 {
		e.report.GasPerTrade = e.report.GasSpent / float64(totalTrades)
	}

	e.report.WallClockElapsed = time.Since(wallStart)
	e.report.Finalize()

	return e.report
}

func (e *Engine) processToken(ctx context.Context, st simulation.SyntheticToken) {
	result := e.filt.Evaluate(ctx, st.Token)

	if !result.Approved {
		return
	}
	e.report.TokensFiltered++

	if !e.breaker.CanSnipe() {
		e.report.TokensBlocked++
		return
	}

	if e.store.OpenPositionCount(state.ChainSolana) >= 5 {
		e.report.TokensBlocked++
		return
	}
	if e.store.TotalOpenPositionCount() >= 8 {
		e.report.TokensBlocked++
		return
	}

	size := e.sizer.Size(state.ChainSolana, result.Score)
	if size <= 0 {
		return
	}

	buyResult := e.exec.Buy(ctx, executor.BuyParams{
		Chain:        state.ChainSolana,
		TokenAddress: st.Token.Address,
		TokenSymbol:  st.Token.Symbol,
		Amount:       size,
		Shadow:       true,
	})

	if buyResult.Error != nil {
		e.breaker.RecordError()
		return
	}

	e.breaker.RecordSnipe()
	e.report.TokensBought++

	// Deduct buy gas.
	e.store.DeductGas(state.ChainSolana, buyResult.GasCost)

	posID := fmt.Sprintf("bt-%s-%d", st.Token.Address, e.clk.Now().UnixMilli())
	e.store.AddPosition(&state.Position{
		ID:           posID,
		Chain:        state.ChainSolana,
		TokenAddress: st.Token.Address,
		TokenSymbol:  st.Token.Symbol,
		EntryPrice:   buyResult.Price,
		CurrentPrice: buyResult.Price,
		PeakPrice:    buyResult.Price,
		Amount:       size,
		EntryTime:    e.clk.Now(),
		EntryGasCost: buyResult.GasCost,
	})
}

func (e *Engine) collectExits() {
	exits := e.notifier.DrainExits()
	for _, exit := range exits {
		e.report.ExitsByReason[exit.Reason]++
		e.recordTradePnL(exit.PnLPct)
	}
}

func (e *Engine) recordTradePnL(pnlPct float64) {
	e.report.TotalPnLPct += pnlPct

	if pnlPct >= 0 {
		e.report.WinCount++
		e.breaker.RecordWin()
	} else {
		e.report.LossCount++
		e.breaker.RecordLoss()
	}

	if pnlPct > e.report.BestTrade {
		e.report.BestTrade = pnlPct
	}
	if pnlPct < e.report.WorstTrade {
		e.report.WorstTrade = pnlPct
	}
}

func (e *Engine) calculateEquity() float64 {
	equity := e.cfg.StartingEquity

	for _, pos := range e.store.AllOpenPositions() {
		if pos.EntryPrice > 0 {
			pnlMult := pos.CurrentPrice / pos.EntryPrice
			equity += pos.Amount * (pnlMult - 1)
		}
	}

	e.store.SetPeakEquity(state.ChainSolana, equity)
	return equity
}
