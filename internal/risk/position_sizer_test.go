package risk

import (
	"math"
	"testing"

	"github.com/cindocode/trenchbot/internal/state"
)

func almostEqual(a, b, epsilon float64) bool {
	return math.Abs(a-b) < epsilon
}

func TestSize_BaseCase(t *testing.T) {
	store := state.NewStore()
	ps := NewPositionSizer(store, 0.3, 0.05, 8)

	// score=80 → multiplier=1.0
	got := ps.Size(state.ChainSolana, 80)
	if !almostEqual(got, 0.3, 0.001) {
		t.Errorf("got %f, want 0.3", got)
	}
}

func TestSize_LowScore(t *testing.T) {
	store := state.NewStore()
	ps := NewPositionSizer(store, 0.3, 0.05, 8)

	// score=60 → multiplier=60/80=0.75
	got := ps.Size(state.ChainSolana, 60)
	if !almostEqual(got, 0.225, 0.001) {
		t.Errorf("got %f, want 0.225", got)
	}
}

func TestSize_MaxScore(t *testing.T) {
	store := state.NewStore()
	ps := NewPositionSizer(store, 0.3, 0.05, 8)

	// score=100 → multiplier=min(100/80, 1.25)=1.25
	got := ps.Size(state.ChainSolana, 100)
	if !almostEqual(got, 0.375, 0.001) {
		t.Errorf("got %f, want 0.375", got)
	}

	// score=120 → still capped at 1.25
	got2 := ps.Size(state.ChainSolana, 120)
	if !almostEqual(got2, 0.375, 0.001) {
		t.Errorf("got %f, want 0.375 (capped)", got2)
	}
}

func TestSize_DailyLossReduction(t *testing.T) {
	store := state.NewStore()
	store.SetPeakEquity(state.ChainSolana, 1200)
	store.UpdateDailyPnL(state.ChainSolana, -60) // 60/1200 = 5% > 4% (half of 8%)

	ps := NewPositionSizer(store, 0.3, 0.05, 8)
	got := ps.Size(state.ChainSolana, 80)

	// base 0.3 * 0.5 (loss reduction) * 1.0 (score) = 0.15
	if !almostEqual(got, 0.15, 0.001) {
		t.Errorf("got %f, want 0.15", got)
	}
}

func TestSize_BNBChain(t *testing.T) {
	store := state.NewStore()
	ps := NewPositionSizer(store, 0.3, 0.05, 8)

	got := ps.Size(state.ChainBNB, 80)
	if !almostEqual(got, 0.05, 0.001) {
		t.Errorf("got %f, want 0.05", got)
	}
}

func TestSize_GasReserveBlock(t *testing.T) {
	store := state.NewStore()
	ps := NewPositionSizer(store, 0.3, 0.05, 8)
	ps.SetGasReserves(0.005, 0.002)

	// No gas set — balance is 0, below reserve → should refuse to size.
	got := ps.Size(state.ChainSolana, 80)
	if got != 0 {
		t.Errorf("should refuse to size with no gas, got %f", got)
	}

	// Set gas above reserve → should size normally.
	store.SetGasBalance(state.ChainSolana, 0.25)
	got = ps.Size(state.ChainSolana, 80)
	if !almostEqual(got, 0.3, 0.001) {
		t.Errorf("got %f, want 0.3 with sufficient gas", got)
	}

	// Drain gas below reserve → should refuse.
	store.DeductGas(state.ChainSolana, 0.248)
	got = ps.Size(state.ChainSolana, 80)
	if got != 0 {
		t.Errorf("should refuse to size with low gas, got %f", got)
	}
}

func TestSize_UnknownChain(t *testing.T) {
	store := state.NewStore()
	ps := NewPositionSizer(store, 0.3, 0.05, 8)

	got := ps.Size("unknown", 80)
	if got != 0 {
		t.Errorf("got %f, want 0 for unknown chain", got)
	}
}
