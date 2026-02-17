package risk

import (
	"math"

	"github.com/cindocode/trenchbot/internal/state"
)

type PositionSizer struct {
	store              *state.Store
	defaultSizeSol     float64
	defaultSizeBNB     float64
	dailyLossLimitPct  float64
	minGasReserveSOL   float64
	minGasReserveBNB   float64
}

func NewPositionSizer(store *state.Store, defaultSol, defaultBNB, dailyLossLimitPct float64) *PositionSizer {
	return &PositionSizer{
		store:             store,
		defaultSizeSol:    defaultSol,
		defaultSizeBNB:    defaultBNB,
		dailyLossLimitPct: dailyLossLimitPct,
	}
}

// SetGasReserves sets the minimum gas balance required before the sizer will
// return a non-zero position size.
func (ps *PositionSizer) SetGasReserves(solReserve, bnbReserve float64) {
	ps.minGasReserveSOL = solReserve
	ps.minGasReserveBNB = bnbReserve
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

	// Reduce size if approaching daily loss limit
	dailyPnL := ps.store.GetDailyPnL(chain)
	if dailyPnL < 0 {
		peak := ps.store.GetPeakEquity(chain)
		if peak > 0 {
			lossRatio := math.Abs(dailyPnL) / peak * 100
			if lossRatio > ps.dailyLossLimitPct/2 {
				base *= 0.5 // halve position size when past 50% of daily limit
			}
		}
	}

	// Scale by score: 60 → 0.6x, 80 → 1.0x, 100 → 1.25x
	scoreMult := float64(score) / 80.0
	if scoreMult > 1.25 {
		scoreMult = 1.25
	}

	return base * scoreMult
}

func (ps *PositionSizer) baseSize(chain state.Chain) float64 {
	switch chain {
	case state.ChainSolana:
		return ps.defaultSizeSol
	case state.ChainBNB:
		return ps.defaultSizeBNB
	default:
		return 0
	}
}

func (ps *PositionSizer) minGasReserve(chain state.Chain) float64 {
	switch chain {
	case state.ChainSolana:
		return ps.minGasReserveSOL
	case state.ChainBNB:
		return ps.minGasReserveBNB
	default:
		return 0
	}
}
