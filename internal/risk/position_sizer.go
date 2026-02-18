package risk

import (
	"github.com/cindocode/trenchbot/internal/state"
)

type PositionSizer struct {
	store            *state.Store
	defaultSizeSol   float64
	defaultSizeBNB   float64
	minGasReserveSOL float64
	minGasReserveBNB float64
	maxPositions     int
	heatFn           func() float64
}

func NewPositionSizer(store *state.Store, defaultSol, defaultBNB float64) *PositionSizer {
	return &PositionSizer{
		store:          store,
		defaultSizeSol: defaultSol,
		defaultSizeBNB: defaultBNB,
	}
}

// SetGasReserves sets the minimum gas balance required before the sizer will
// return a non-zero position size.
func (ps *PositionSizer) SetGasReserves(solReserve, bnbReserve float64) {
	ps.minGasReserveSOL = solReserve
	ps.minGasReserveBNB = bnbReserve
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

	// Scale by score: 60 → 0.6x, 80 → 1.0x, 100 → 1.25x
	scoreMult := float64(score) / 80.0
	if scoreMult > 1.25 {
		scoreMult = 1.25
	}

	size := base * scoreMult

	// Concentration scaling: reduce size as open positions approach the limit.
	if ps.maxPositions > 0 {
		openCount := ps.store.OpenPositionCount(chain)
		concentrationMult := 1.0 - (float64(openCount)/float64(ps.maxPositions))*0.6
		if concentrationMult < 0.2 {
			concentrationMult = 0.2
		}
		size *= concentrationMult
	}

	return size
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
