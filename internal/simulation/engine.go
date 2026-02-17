package simulation

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand"
	"sync"
	"time"

	"github.com/cindocode/trenchbot/internal/clock"
	"github.com/cindocode/trenchbot/internal/executor"
	"github.com/cindocode/trenchbot/internal/filter"
	"github.com/cindocode/trenchbot/internal/monitor"
	"github.com/cindocode/trenchbot/internal/risk"
	"github.com/cindocode/trenchbot/internal/state"
	"github.com/cindocode/trenchbot/internal/testutil"
)

type SimConfig struct {
	Seed              int64
	SimulatedDuration time.Duration
	WallClockTimeout  time.Duration
	TokensPerHour     int
	StartingEquity    float64
	Chain             state.Chain
	MinScore          int
	GasBudget         float64 // gas balance in native token (e.g. 0.25 SOL)
	GasCostPerTx      float64 // gas cost per transaction (e.g. 0.000505 SOL)
}

func DefaultSimConfig() SimConfig {
	return SimConfig{
		Seed:              42,
		SimulatedDuration: 6 * time.Hour,
		WallClockTimeout:  5 * time.Minute,
		TokensPerHour:     60,
		StartingEquity:    1200,
		Chain:             state.ChainSolana,
		MinScore:          60,
		GasBudget:         0.25,
		GasCostPerTx:      0.000505,
	}
}

// SimExecutor tracks price curves and returns prices based on simulated time.
type SimExecutor struct {
	mu           sync.RWMutex
	chain        state.Chain
	clk          *clock.SimClock
	curves       map[string][]PricePoint
	entryTimes   map[string]time.Time
	buyCalls     int
	sellCalls    int
	gasCostPerTx float64
}

func NewSimExecutor(chain state.Chain, clk *clock.SimClock) *SimExecutor {
	return &SimExecutor{
		chain:        chain,
		clk:          clk,
		curves:       make(map[string][]PricePoint),
		entryTimes:   make(map[string]time.Time),
		gasCostPerTx: 0.000505, // default Solana gas
	}
}

// SetGasCostPerTx overrides the default per-transaction gas cost.
func (e *SimExecutor) SetGasCostPerTx(cost float64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.gasCostPerTx = cost
}

func (e *SimExecutor) Chain() state.Chain { return e.chain }

func (e *SimExecutor) RegisterCurve(tokenAddr string, curve []PricePoint) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.curves[tokenAddr] = curve
}

func (e *SimExecutor) Buy(_ context.Context, params executor.BuyParams) executor.BuyResult {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.buyCalls++
	e.entryTimes[params.TokenAddress] = e.clk.Now()
	return executor.BuyResult{
		Success: true,
		TxHash:  "sim-buy-" + params.TokenAddress,
		Price:   1.0,
		Amount:  params.Amount,
		GasCost: e.gasCostPerTx,
	}
}

func (e *SimExecutor) Sell(_ context.Context, params executor.SellParams) executor.SellResult {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.sellCalls++
	price := e.currentPriceLocked(params.TokenAddress)
	return executor.SellResult{
		Success: true,
		TxHash:  "sim-sell-" + params.TokenAddress,
		Price:   price,
		Amount:  params.AmountPct,
		GasCost: e.gasCostPerTx,
	}
}

func (e *SimExecutor) CurrentPrice(tokenAddr string) float64 {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.currentPriceLocked(tokenAddr)
}

func (e *SimExecutor) currentPriceLocked(tokenAddr string) float64 {
	curve, ok := e.curves[tokenAddr]
	if !ok {
		return 1.0
	}
	entryTime, ok := e.entryTimes[tokenAddr]
	if !ok {
		return 1.0
	}
	offset := e.clk.Now().Sub(entryTime)
	return InterpolatePrice(curve, offset)
}

type Engine struct {
	cfg      SimConfig
	clk      *clock.SimClock
	store    *state.Store
	filt     *filter.Filter
	mon      *monitor.Monitor
	sizer    *risk.PositionSizer
	breaker  *risk.CircuitBreaker
	exec     *SimExecutor
	notifier *testutil.MockNotifier
	log      *slog.Logger
	report   *Report
	rng      *rand.Rand

	// Track archetype per token address
	archetypes map[string]TokenArchetype
}

func NewEngine(cfg SimConfig, log *slog.Logger) *Engine {
	startTime := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	clk := clock.NewSimClock(startTime)
	store := state.NewStore()
	store.SetPeakEquity(cfg.Chain, cfg.StartingEquity)

	exec := NewSimExecutor(cfg.Chain, clk)
	if cfg.GasCostPerTx > 0 {
		exec.SetGasCostPerTx(cfg.GasCostPerTx)
	}
	notifier := testutil.NewMockNotifier()

	// Initialize gas balance.
	if cfg.GasBudget > 0 {
		store.SetGasBalance(cfg.Chain, cfg.GasBudget)
	}

	executors := map[state.Chain]executor.Executor{
		cfg.Chain: exec,
	}

	breaker := risk.NewCircuitBreaker(risk.CircuitBreakerConfig{
		Chain:              cfg.Chain,
		MaxDrawdownPct:     50,
		DailyLossLimitPct:  8,
		ConsecutiveLossCap: 10,
		MaxSnipesPerHour:   10,
		StartingEquity:     cfg.StartingEquity,
	}, store, clk, log)

	sizer := risk.NewPositionSizer(store, 0.3, 0.05, 8)
	// Reserve enough gas for ~10 round-trips before refusing to size.
	sizer.SetGasReserves(cfg.GasCostPerTx*10, 0)
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
		rng:      rand.New(rand.NewSource(cfg.Seed + 999)), // offset seed for shock randomness
		report: &Report{
			Seed:              cfg.Seed,
			SimulatedDuration: cfg.SimulatedDuration,
			ExitsByReason:     make(map[string]int),
			ArchetypeResults:  make(map[TokenArchetype]ArchetypeStats),
		},
		archetypes: make(map[string]TokenArchetype),
	}
}

func (e *Engine) Run(ctx context.Context) *Report {
	wallStart := time.Now()
	ctx, cancel := context.WithTimeout(ctx, e.cfg.WallClockTimeout)
	defer cancel()

	gen := NewTokenGenerator(GeneratorConfig{
		Seed:              e.cfg.Seed,
		TokensPerHour:     e.cfg.TokensPerHour,
		SimulatedDuration: e.cfg.SimulatedDuration,
		Chain:             e.cfg.Chain,
	})

	tokens := gen.Generate()
	e.report.TokensGenerated = len(tokens)

	// Track archetype counts
	archetypeCounts := make(map[TokenArchetype]int)
	for _, st := range tokens {
		archetypeCounts[st.Archetype]++
		e.archetypes[st.Token.Address] = st.Archetype
		e.exec.RegisterCurve(st.Token.Address, st.PriceCurve)
	}
	for arch, count := range archetypeCounts {
		stats := e.report.ArchetypeResults[arch]
		stats.Generated = count
		e.report.ArchetypeResults[arch] = stats
	}

	tokenIdx := 0
	tickDuration := 1 * time.Second
	totalTicks := int(e.cfg.SimulatedDuration / tickDuration)

	prevPausedState := false

	for tick := 0; tick < totalTicks; tick++ {
		if ctx.Err() != nil {
			break
		}

		e.clk.Advance(tickDuration)
		simElapsed := time.Duration(tick+1) * tickDuration

		// Emit tokens
		for tokenIdx < len(tokens) && tokens[tokenIdx].EmitTime <= simElapsed {
			e.processToken(ctx, tokens[tokenIdx])
			tokenIdx++
		}

		// Update prices for open positions
		for _, pos := range e.store.AllOpenPositions() {
			newPrice := e.exec.CurrentPrice(pos.TokenAddress)
			e.store.UpdatePosition(pos.ID, func(p *state.Position) {
				p.CurrentPrice = newPrice
			})
		}

		// Run monitor every 5 simulated seconds
		if tick%5 == 0 {
			e.mon.CheckPositions(ctx)
		}

		// Track exits from notifier
		e.collectExits()

		// Check circuit breaker periodically
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

		// Market-wide shocks: ~2 per simulated hour, random 20-50% dump across all open positions
		// Fires roughly every 30 simulated minutes (1800 ticks)
		if tick > 0 && tick%1800 == 0 && e.rng.Float64() < 0.7 {
			e.applyMarketShock()
		}

		// Track max drawdown
		equity := e.calculateEquity()
		peak := e.store.GetPeakEquity(e.cfg.Chain)
		if peak > 0 {
			dd := ((peak - equity) / peak) * 100
			if dd > e.report.MaxDrawdownPct {
				e.report.MaxDrawdownPct = dd
			}
		}
	}

	// Close remaining positions at their final price
	for _, pos := range e.store.AllOpenPositions() {
		finalPrice := e.exec.CurrentPrice(pos.TokenAddress)
		pnlPct := ((finalPrice / pos.EntryPrice) - 1) * 100
		e.store.UpdatePosition(pos.ID, func(p *state.Position) {
			p.Closed = true
			p.PnL = pnlPct
			p.CurrentPrice = finalPrice
		})
		e.recordTradePnL(pos.TokenAddress, pnlPct)
		e.report.ExitsByReason["end-of-sim"]++
	}

	e.report.GasSpent = e.store.GetGasSpent(e.cfg.Chain)
	e.report.GasRemaining = e.store.GetGasBalance(e.cfg.Chain)
	totalTrades := e.report.WinCount + e.report.LossCount
	if totalTrades > 0 {
		e.report.GasPerTrade = e.report.GasSpent / float64(totalTrades)
	}

	e.report.WallClockElapsed = time.Since(wallStart)
	e.report.Finalize()

	return e.report
}

func (e *Engine) processToken(ctx context.Context, st SyntheticToken) {
	result := e.filt.Evaluate(st.Token)

	if !result.Approved {
		return
	}
	e.report.TokensFiltered++

	if !e.breaker.CanSnipe() {
		e.report.TokensBlocked++
		return
	}

	if e.store.OpenPositionCount(e.cfg.Chain) >= 5 {
		e.report.TokensBlocked++
		return
	}
	if e.store.TotalOpenPositionCount() >= 8 {
		e.report.TokensBlocked++
		return
	}

	size := e.sizer.Size(e.cfg.Chain, result.Score)
	if size <= 0 {
		return
	}

	buyResult := e.exec.Buy(ctx, executor.BuyParams{
		Chain:        e.cfg.Chain,
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
	e.store.DeductGas(e.cfg.Chain, buyResult.GasCost)

	// Track archetype buy
	arch := e.archetypes[st.Token.Address]
	stats := e.report.ArchetypeResults[arch]
	stats.Bought++
	e.report.ArchetypeResults[arch] = stats

	posID := fmt.Sprintf("sim-%s-%d", st.Token.Address, e.clk.Now().UnixMilli())
	e.store.AddPosition(&state.Position{
		ID:           posID,
		Chain:        e.cfg.Chain,
		TokenAddress: st.Token.Address,
		TokenSymbol:  st.Token.Symbol,
		EntryPrice:   1.0,
		CurrentPrice: 1.0,
		PeakPrice:    1.0,
		Amount:       size,
		EntryTime:    e.clk.Now(),
		EntryGasCost: buyResult.GasCost,
	})
}

func (e *Engine) collectExits() {
	exits := e.notifier.DrainExits()
	for _, exit := range exits {
		e.report.ExitsByReason[exit.Reason]++
		e.recordTradePnL("", exit.PnLPct)
	}
}

func (e *Engine) recordTradePnL(tokenAddr string, pnlPct float64) {
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

// applyMarketShock simulates a correlated market dump — all open positions
// take a 20-50% haircut on current price. This models SOL flash crashes,
// market-wide panic sells, and correlation spikes that hit all memecoins at once.
func (e *Engine) applyMarketShock() {
	positions := e.store.AllOpenPositions()
	if len(positions) == 0 {
		return
	}

	shockMult := 0.5 + e.rng.Float64()*0.3 // multiply prices by 0.5-0.8
	e.report.MarketShocks++
	e.log.Warn("market shock", "multiplier", shockMult, "open_positions", len(positions))

	for _, pos := range positions {
		newPrice := pos.CurrentPrice * shockMult
		e.store.UpdatePosition(pos.ID, func(p *state.Position) {
			p.CurrentPrice = newPrice
		})
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

	e.store.SetPeakEquity(e.cfg.Chain, equity)
	return equity
}
