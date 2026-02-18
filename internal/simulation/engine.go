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
	SlippagePct       float64 // default 0.5
	GasSpikeEnabled   bool
	FrontRunMinPct    float64 // default 5
	FrontRunMaxPct    float64 // default 30
	PriceNoiseEnabled bool    // default true
	SellFailureRate   float64 // default 0.05 (5%)
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
		SlippagePct:       0.5,
		GasSpikeEnabled:   true,
		FrontRunMinPct:    5,
		FrontRunMaxPct:    30,
		PriceNoiseEnabled: true,
		SellFailureRate:   0.05,
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

	// Market shock persistence
	shockMultiplier float64 // starts at 1.0
	shockDecayRate  float64 // how fast shock recovers per tick

	// Slippage
	slippagePct float64

	// Gas spike simulation
	gasSpikeMultiplier float64 // 1.0 normally
	gasSpikeDecayTicks int     // ticks remaining for spike

	// Front-running / MEV
	frontRunMinPct float64
	frontRunMaxPct float64
	frontRunRng    *rand.Rand

	// Stochastic price noise (P5)
	volatility map[string]float64 // tokenAddr -> volatility coefficient
	noiseRng   *rand.Rand

	// Honeypot tokens (P3)
	honeypots map[string]bool

	// Sell-side RPC failures (P9)
	sellFailureRate  float64
	sellFailRng      *rand.Rand
	sellFailureCount int
}

func NewSimExecutor(chain state.Chain, clk *clock.SimClock, cfg SimConfig) *SimExecutor {
	return &SimExecutor{
		chain:              chain,
		clk:                clk,
		curves:             make(map[string][]PricePoint),
		entryTimes:         make(map[string]time.Time),
		gasCostPerTx:       0.000505, // default Solana gas
		shockMultiplier:    1.0,
		slippagePct:        cfg.SlippagePct,
		gasSpikeMultiplier: 1.0,
		frontRunMinPct:     cfg.FrontRunMinPct,
		frontRunMaxPct:     cfg.FrontRunMaxPct,
		frontRunRng:        rand.New(rand.NewSource(cfg.Seed + 777)),
		volatility:         make(map[string]float64),
		noiseRng:           rand.New(rand.NewSource(cfg.Seed + 555)),
		honeypots:          make(map[string]bool),
		sellFailureRate:    cfg.SellFailureRate,
		sellFailRng:        rand.New(rand.NewSource(cfg.Seed + 333)),
	}
}

// SetGasCostPerTx overrides the default per-transaction gas cost.
func (e *SimExecutor) SetGasCostPerTx(cost float64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.gasCostPerTx = cost
}

// ApplyShock multiplies the current shock multiplier by the given factor.
func (e *SimExecutor) ApplyShock(mult, decayRate float64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.shockMultiplier *= mult
	e.shockDecayRate = decayRate
}

// DecayShock gradually recovers the shock multiplier toward 1.0.
func (e *SimExecutor) DecayShock() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.shockMultiplier < 1.0 {
		e.shockMultiplier += e.shockDecayRate
		if e.shockMultiplier > 1.0 {
			e.shockMultiplier = 1.0
		}
	}
}

// ApplyGasSpike sets a gas cost multiplier for the given number of ticks.
func (e *SimExecutor) ApplyGasSpike(multiplier float64, durationTicks int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.gasSpikeMultiplier = multiplier
	e.gasSpikeDecayTicks = durationTicks
}

// DecayGasSpike decrements the gas spike duration and resets when expired.
func (e *SimExecutor) DecayGasSpike() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.gasSpikeDecayTicks > 0 {
		e.gasSpikeDecayTicks--
		if e.gasSpikeDecayTicks == 0 {
			e.gasSpikeMultiplier = 1.0
		}
	}
}

// SetVolatility sets the stochastic price noise coefficient for a token.
func (e *SimExecutor) SetVolatility(tokenAddr string, vol float64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.volatility[tokenAddr] = vol
}

// MarkHoneypot flags a token address as a honeypot (sells will revert).
func (e *SimExecutor) MarkHoneypot(addr string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.honeypots[addr] = true
}

// SellFailureCount returns the number of sell-side RPC failures encountered.
func (e *SimExecutor) SellFailureCount() int {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.sellFailureCount
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

	effectivePrice := e.currentPriceLocked(params.TokenAddress) * (1 + e.slippagePct/100)

	if e.frontRunMaxPct > 0 {
		frontRunPct := e.frontRunMinPct + e.frontRunRng.Float64()*(e.frontRunMaxPct-e.frontRunMinPct)
		effectivePrice *= (1 + frontRunPct/100)
	}

	return executor.BuyResult{
		Success: true,
		TxHash:  "sim-buy-" + params.TokenAddress,
		Price:   effectivePrice,
		Amount:  params.Amount,
		GasCost: e.gasCostPerTx * e.gasSpikeMultiplier,
	}
}

func (e *SimExecutor) Sell(_ context.Context, params executor.SellParams) executor.SellResult {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.sellCalls++

	// P9: Sell-side RPC failure
	if e.sellFailureRate > 0 && e.sellFailRng.Float64() < e.sellFailureRate {
		e.sellFailureCount++
		return executor.SellResult{
			Success: false,
			Error:   fmt.Errorf("RPC error: transaction not confirmed"),
			GasCost: e.gasCostPerTx * 0.5,
		}
	}

	// P3: Honeypot tokens always revert on sell
	if e.honeypots[params.TokenAddress] {
		return executor.SellResult{
			Success: false,
			Error:   fmt.Errorf("transaction reverted (honeypot)"),
			GasCost: e.gasCostPerTx,
		}
	}

	price := e.currentPriceLocked(params.TokenAddress)
	effectivePrice := price * (1 - e.slippagePct/100)
	return executor.SellResult{
		Success: true,
		TxHash:  "sim-sell-" + params.TokenAddress,
		Price:   effectivePrice,
		Amount:  params.AmountPct,
		GasCost: e.gasCostPerTx * e.gasSpikeMultiplier,
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
	price := InterpolatePrice(curve, offset) * e.shockMultiplier

	// P5: Stochastic price noise
	if vol, ok := e.volatility[tokenAddr]; ok && vol > 0 {
		noise := 1.0 + e.noiseRng.NormFloat64()*vol
		price *= noise
		if price < 0.001 {
			price = 0.001
		}
	}

	return price
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
	// Track current day for daily PnL reset
	lastDay int
	// Blocklist: tokens that hit stop-loss are not re-entered
	blocklist map[string]bool
	// Track position amounts by token address for SOL-denominated PnL
	positionAmounts map[string]float64
}

func NewEngine(cfg SimConfig, log *slog.Logger) *Engine {
	startTime := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	clk := clock.NewSimClock(startTime)
	store := state.NewStore()
	store.SetPeakEquity(cfg.Chain, cfg.StartingEquity)

	exec := NewSimExecutor(cfg.Chain, clk, cfg)
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
		HeatFullPct:        15,
		ConsecutiveLossCap: 10,
		MaxSnipesPerHour:   10,
		StartingEquity:     cfg.StartingEquity,
	}, store, clk, log)

	sizer := risk.NewPositionSizer(store, 0.3)
	sizer.SetMaxPositions(5)
	// Reserve enough gas for ~10 round-trips before refusing to size.
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
		rng:      rand.New(rand.NewSource(cfg.Seed + 999)), // offset seed for shock randomness
		report: &Report{
			Seed:              cfg.Seed,
			SimulatedDuration: cfg.SimulatedDuration,
			ExitsByReason:     make(map[string]int),
			ArchetypeResults:  make(map[TokenArchetype]ArchetypeStats),
		},
		archetypes:      make(map[string]TokenArchetype),
		lastDay:         startTime.YearDay(),
		blocklist:       make(map[string]bool),
		positionAmounts: make(map[string]float64),
	}
}

// shockOverride defines a scheduled shock injection at a specific tick.
type shockOverride struct {
	tick         int
	shockMult    float64
	decayRate    float64
	gasSpikeMult float64
}

func (e *Engine) Run(ctx context.Context) *Report {
	return e.runLoop(ctx, nil)
}

// RunWithShockAt runs the simulation with a forced market shock at the specified tick.
// shockMult is the price multiplier (e.g. 0.76 for -24%), decayRate controls recovery,
// and gasSpikeMult is the gas fee multiplier during the shock.
func (e *Engine) RunWithShockAt(ctx context.Context, shockTick int, shockMult, decayRate, gasSpikeMult float64) *Report {
	return e.runLoop(ctx, &shockOverride{
		tick:         shockTick,
		shockMult:    shockMult,
		decayRate:    decayRate,
		gasSpikeMult: gasSpikeMult,
	})
}

func (e *Engine) runLoop(ctx context.Context, override *shockOverride) *Report {
	wallStart := time.Now()
	ctx, cancel := context.WithTimeout(ctx, e.cfg.WallClockTimeout)
	defer cancel()

	gen := NewTokenGenerator(GeneratorConfig{
		Seed:              e.cfg.Seed,
		TokensPerHour:     e.cfg.TokensPerHour,
		SimulatedDuration: e.cfg.SimulatedDuration,
		Chain:             e.cfg.Chain,
		RugClusterProb:    0.03,
		RugClusterSize:    4,
		TimeOfDayEnabled:  true,
	})

	genResult := gen.GenerateWithResult()
	tokens := genResult.Tokens
	e.report.TokensGenerated = len(tokens)
	e.report.RugClusters = genResult.RugClusters

	// Track archetype counts and configure per-token simulation features
	archetypeCounts := make(map[TokenArchetype]int)
	for _, st := range tokens {
		archetypeCounts[st.Archetype]++
		e.archetypes[st.Token.Address] = st.Archetype
		e.exec.RegisterCurve(st.Token.Address, st.PriceCurve)

		// P5: Set per-token price noise volatility
		if e.cfg.PriceNoiseEnabled {
			if vol, ok := ArchetypeVolatility[st.Archetype]; ok {
				e.exec.SetVolatility(st.Token.Address, vol)
			}
		}

		// P3: Mark honeypot tokens
		if st.Archetype == ArchetypeHoneypot {
			e.exec.MarkHoneypot(st.Token.Address)
		}
	}
	for arch, count := range archetypeCounts {
		stats := e.report.ArchetypeResults[arch]
		stats.Generated = count
		e.report.ArchetypeResults[arch] = stats
	}
	e.report.HoneypotCount = archetypeCounts[ArchetypeHoneypot]

	tokenIdx := 0
	tickDuration := 1 * time.Second
	totalTicks := int(e.cfg.SimulatedDuration / tickDuration)

	prevPausedState := false
	overrideFired := false

	for tick := 0; tick < totalTicks; tick++ {
		if ctx.Err() != nil {
			break
		}

		e.clk.Advance(tickDuration)
		simElapsed := time.Duration(tick+1) * tickDuration

		// Decay shock and gas spike every tick
		e.exec.DecayShock()
		e.exec.DecayGasSpike()

		// Reset daily PnL at day boundaries
		currentDay := e.clk.Now().YearDay()
		if currentDay != e.lastDay {
			e.store.ResetDailyPnL()
			e.lastDay = currentDay
		}

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

		// Injected shock override (e.g. October 10 crash scenario)
		if override != nil && !overrideFired && tick == override.tick {
			e.exec.ApplyShock(override.shockMult, override.decayRate)
			e.report.MarketShocks++
			if override.gasSpikeMult > 1.0 {
				e.exec.ApplyGasSpike(override.gasSpikeMult, 300) // 5 min spike
				e.report.GasSpikeEvents++
			}
			overrideFired = true
			e.log.Warn("injected shock override",
				"tick", tick,
				"shock_mult", override.shockMult,
				"gas_spike_mult", override.gasSpikeMult,
			)
		}

		// Market-wide shocks: ~2 per simulated hour, random 20-50% dump across all open positions
		// Fires roughly every 30 simulated minutes (1800 ticks)
		if tick > 0 && tick%1800 == 0 && e.rng.Float64() < 0.7 {
			e.applyMarketShock()

			// Gas spike during congestion
			if e.cfg.GasSpikeEnabled {
				spikeMult := 10 + e.rng.Float64()*40 // 10x-50x gas spike
				e.exec.ApplyGasSpike(spikeMult, 60)   // lasts 60 ticks (1 min)
				e.report.GasSpikeEvents++
			}
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
		e.recordTradePnL(pos.TokenAddress, pnlPct, pos.Amount)
		e.report.ExitsByReason["end-of-sim"]++
	}

	e.report.GasSpent = e.store.GetGasSpent(e.cfg.Chain)
	e.report.GasRemaining = e.store.GetGasBalance(e.cfg.Chain)
	totalTrades := e.report.WinCount + e.report.LossCount
	if totalTrades > 0 {
		e.report.GasPerTrade = e.report.GasSpent / float64(totalTrades)
	}

	e.report.SellFailures = e.exec.SellFailureCount()

	e.report.WallClockElapsed = time.Since(wallStart)
	e.report.Finalize()

	return e.report
}

func (e *Engine) processToken(ctx context.Context, st SyntheticToken) {
	result := e.filt.Evaluate(ctx, st.Token)

	if !result.Approved {
		return
	}
	e.report.TokensFiltered++

	// Block re-entry for tokens that hit stop-loss
	if e.blocklist[st.Token.Address] {
		e.report.TokensBlocked++
		return
	}

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

	// Get base curve price before buy (for slippage/front-run cost tracking)
	basePrice := e.exec.CurrentPrice(st.Token.Address)

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

	// Track slippage cost: difference between effective buy price and base curve price
	slippageOnlyPrice := basePrice * (1 + e.cfg.SlippagePct/100)
	e.report.TotalSlippageCost += (slippageOnlyPrice - basePrice) * size

	// Track front-run cost: difference between final effective price and slippage-only price
	e.report.TotalFrontRunCost += (buyResult.Price - slippageOnlyPrice) * size

	// Track archetype buy
	arch := e.archetypes[st.Token.Address]
	stats := e.report.ArchetypeResults[arch]
	stats.Bought++
	e.report.ArchetypeResults[arch] = stats

	e.positionAmounts[st.Token.Address] = size

	posID := fmt.Sprintf("sim-%s-%d", st.Token.Address, e.clk.Now().UnixMilli())
	e.store.AddPosition(&state.Position{
		ID:           posID,
		Chain:        e.cfg.Chain,
		TokenAddress: st.Token.Address,
		TokenSymbol:  st.Token.Symbol,
		EntryPrice:   buyResult.Price,
		CurrentPrice: buyResult.Price,
		PeakPrice:    buyResult.Price,
		Amount:       size,
		EntryTime:    e.clk.Now(),
		EntryGasCost: buyResult.GasCost,
		FilterScore:  result.Score,
		SignalScores: result.SignalBreakdown,
	})
}

func (e *Engine) collectExits() {
	exits := e.notifier.DrainExits()
	for _, exit := range exits {
		e.report.ExitsByReason[exit.Reason]++
		amount := e.positionAmounts[exit.TokenAddress]
		e.recordTradePnL(exit.TokenAddress, exit.PnLPct, amount)
		if exit.Reason == "stop-loss" && exit.TokenAddress != "" {
			e.blocklist[exit.TokenAddress] = true
			e.report.ReEntryBlocked++
		}
	}
}

func (e *Engine) recordTradePnL(tokenAddr string, pnlPct float64, positionAmount float64) {
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

	// Track daily P&L in SOL terms for circuit breaker daily loss limit.
	// positionAmount is the SOL invested; pnlPct/100 gives the fractional return.
	solPnL := positionAmount * (pnlPct / 100)
	e.store.UpdateDailyPnL(e.cfg.Chain, solPnL)
}

// applyMarketShock simulates a correlated market dump by applying a persistent
// shock multiplier to the price curve. This models SOL flash crashes,
// market-wide panic sells, and correlation spikes that hit all memecoins at once.
func (e *Engine) applyMarketShock() {
	positions := e.store.AllOpenPositions()
	if len(positions) == 0 {
		return
	}

	shockMult := 0.5 + e.rng.Float64()*0.3 // multiply prices by 0.5-0.8
	e.report.MarketShocks++
	e.log.Warn("market shock", "multiplier", shockMult, "open_positions", len(positions))

	e.exec.ApplyShock(shockMult, 0.002)
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
