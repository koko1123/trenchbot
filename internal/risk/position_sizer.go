package risk

import (
	"math"
	"sync"

	"github.com/cindocode/trenchbot/internal/curve"
	"github.com/cindocode/trenchbot/internal/state"
)

type PositionSizer struct {
	store            *state.Store
	defaultSizeSol   float64
	minGasReserveSOL float64
	maxPositions     int
	heatFn           func() float64
	perfTracker      *PerformanceTracker
	maxImpactPct     float64 // max price impact per trade (e.g., 2.0 = 2%)

	// Dynamic capital management.
	mu              sync.RWMutex
	dynamicEnabled  bool
	scaleFactor     float64
	dynamicMaxSol   int // computed max positions for Solana
	dynamicMaxTotal int // computed total max
}

func NewPositionSizer(store *state.Store, defaultSol float64) *PositionSizer {
	return &PositionSizer{
		store:          store,
		defaultSizeSol: defaultSol,
	}
}

// SetGasReserve sets the minimum gas balance required before the sizer will
// return a non-zero position size.
func (ps *PositionSizer) SetGasReserve(solReserve float64) {
	ps.minGasReserveSOL = solReserve
}

// SetMaxPositions sets the maximum number of concurrent positions used for
// concentration scaling. When open positions approach this limit the sizer
// progressively reduces size.
func (ps *PositionSizer) SetMaxPositions(max int) {
	ps.maxPositions = max
}

// SetHeatFunc sets the function that returns the current heat level (0.0–1.0).
// Heat reduces position size: size *= (1.0 - heat * 0.5).
func (ps *PositionSizer) SetHeatFunc(fn func() float64) {
	ps.heatFn = fn
}

// SetPerformanceTracker enables Kelly criterion sizing based on rolling performance.
func (ps *PositionSizer) SetPerformanceTracker(pt *PerformanceTracker) {
	ps.perfTracker = pt
}

// SetMaxImpact sets the maximum price impact per trade as a percentage.
// E.g., 2.0 means the trade should not move the bonding curve by more than 2%.
// When set, Size() will cap the trade at curve.MaxEntrySOL(mcapSOL, maxImpactPct).
func (ps *PositionSizer) SetMaxImpact(pct float64) {
	ps.maxImpactPct = pct
}

// EnableDynamicLimits turns on capital-aware position limit scaling.
func (ps *PositionSizer) EnableDynamicLimits(scaleFactor float64) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	ps.dynamicEnabled = true
	ps.scaleFactor = scaleFactor
	if ps.scaleFactor <= 0 {
		ps.scaleFactor = 3.0
	}
}

// UpdateCapital recomputes dynamic position limits based on available capital.
// availableCapital = walletBalance - gasReserve - sumOfOpenPositionAmounts.
// Call this periodically (e.g., every 30s).
func (ps *PositionSizer) UpdateCapital(chain state.Chain, availableCapital float64) {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	if !ps.dynamicEnabled {
		return
	}

	baseSize := ps.baseSize(chain)
	if baseSize <= 0 || availableCapital <= 0 {
		ps.setDynamicMax(chain, 1)
		return
	}

	raw := math.Sqrt(availableCapital/baseSize) * ps.scaleFactor
	computed := int(raw)
	if computed < 1 {
		computed = 1
	}
	// Cap at static max.
	if ps.maxPositions > 0 && computed > ps.maxPositions {
		computed = ps.maxPositions
	}
	ps.setDynamicMax(chain, computed)
}

func (ps *PositionSizer) setDynamicMax(chain state.Chain, max int) {
	switch chain {
	case state.ChainSolana:
		ps.dynamicMaxSol = max
	}
	ps.dynamicMaxTotal = ps.dynamicMaxSol
	if ps.maxPositions > 0 && ps.dynamicMaxTotal > ps.maxPositions {
		ps.dynamicMaxTotal = ps.maxPositions
	}
}

// DynamicMaxPerChain returns the current per-chain position limit.
// If dynamic limits are disabled, returns the static config value.
func (ps *PositionSizer) DynamicMaxPerChain(chain state.Chain) int {
	ps.mu.RLock()
	defer ps.mu.RUnlock()

	if !ps.dynamicEnabled {
		return ps.maxPositions
	}

	if chain == state.ChainSolana && ps.dynamicMaxSol > 0 {
		return ps.dynamicMaxSol
	}
	return ps.maxPositions
}

// DynamicMaxTotal returns the current total position limit.
func (ps *PositionSizer) DynamicMaxTotal() int {
	ps.mu.RLock()
	defer ps.mu.RUnlock()

	if !ps.dynamicEnabled || ps.dynamicMaxTotal <= 0 {
		return ps.maxPositions
	}
	return ps.dynamicMaxTotal
}

// SizeWithLiquidity computes position size with a liquidity-aware cap.
// mcapSOL is the current market cap in SOL; if > 0 and maxImpactPct is set,
// the returned size is capped so the trade doesn't move the bonding curve
// by more than maxImpactPct.
func (ps *PositionSizer) SizeWithLiquidity(chain state.Chain, score int, mcapSOL float64) float64 {
	size := ps.Size(chain, score)
	if size <= 0 || ps.maxImpactPct <= 0 || mcapSOL <= 0 {
		return size
	}

	maxEntry := curve.MaxEntrySOL(mcapSOL, ps.maxImpactPct)
	if maxEntry > 0 && size > maxEntry {
		size = maxEntry
	}

	// Floor: don't go below 0.01 SOL — not worth the gas.
	if size < 0.01 {
		return 0
	}
	return size
}

func (ps *PositionSizer) Size(chain state.Chain, score int) float64 {
	// Refuse to size if gas is too low for a round-trip (buy + sell).
	minReserve := ps.minGasReserve(chain)
	if minReserve > 0 {
		gasBalance := ps.store.GetGasBalance(chain)
		if gasBalance < minReserve {
			return 0
		}
	}

	base := ps.baseSize(chain)

	// Heat-based size reduction: shrink positions as losses mount.
	if ps.heatFn != nil {
		heat := ps.heatFn()
		if heat > 0 {
			base *= (1.0 - heat*0.5) // range [1.0, 0.5]
		}
	}

	// Kelly criterion sizing: if enabled and we have enough data,
	// use half-Kelly fraction instead of heuristic score-based scaling.
	if ps.perfTracker != nil {
		stats := ps.perfTracker.Stats()
		if stats.TradeCount >= 10 { // need minimum data
			kellyFraction := stats.KellyFraction
			if kellyFraction < 0.1 {
				kellyFraction = 0.1 // floor at 10% of base
			}
			if kellyFraction > 1.25 {
				kellyFraction = 1.25 // cap
			}
			size := base * kellyFraction
			size = ps.applyConcentration(chain, size)
			return size
		}
	}

	// Fallback: heuristic score-based scaling.
	// Scale by score: 60 → 0.6x, 80 → 1.0x, 100 → 1.25x
	scoreMult := float64(score) / 80.0
	if scoreMult > 1.25 {
		scoreMult = 1.25
	}

	size := base * scoreMult
	size = ps.applyConcentration(chain, size)
	return size
}

// applyConcentration reduces size as open positions approach the limit.
func (ps *PositionSizer) applyConcentration(chain state.Chain, size float64) float64 {
	effectiveMax := ps.DynamicMaxPerChain(chain)
	if effectiveMax > 0 {
		openCount := ps.store.OpenPositionCount(chain)
		concentrationMult := 1.0 - (float64(openCount)/float64(effectiveMax))*0.6
		if concentrationMult < 0.2 {
			concentrationMult = 0.2
		}
		size *= concentrationMult
	}
	return size
}

func (ps *PositionSizer) baseSize(chain state.Chain) float64 {
	if chain == state.ChainSolana {
		return ps.defaultSizeSol
	}
	return 0
}

func (ps *PositionSizer) minGasReserve(chain state.Chain) float64 {
	if chain == state.ChainSolana {
		return ps.minGasReserveSOL
	}
	return 0
}
