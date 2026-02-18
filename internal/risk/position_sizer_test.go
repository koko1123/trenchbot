package risk

import (
	"fmt"
	"math"
	"testing"

	"github.com/cindocode/trenchbot/internal/state"
)

func almostEqual(a, b, epsilon float64) bool {
	return math.Abs(a-b) < epsilon
}

func TestSize_BaseCase(t *testing.T) {
	store := state.NewStore()
	ps := NewPositionSizer(store, 0.3)

	// score=80 → multiplier=1.0
	got := ps.Size(state.ChainSolana, 80)
	if !almostEqual(got, 0.3, 0.001) {
		t.Errorf("got %f, want 0.3", got)
	}
}

func TestSize_LowScore(t *testing.T) {
	store := state.NewStore()
	ps := NewPositionSizer(store, 0.3)

	// score=60 → multiplier=60/80=0.75
	got := ps.Size(state.ChainSolana, 60)
	if !almostEqual(got, 0.225, 0.001) {
		t.Errorf("got %f, want 0.225", got)
	}
}

func TestSize_MaxScore(t *testing.T) {
	store := state.NewStore()
	ps := NewPositionSizer(store, 0.3)

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

func TestSize_HeatReduction(t *testing.T) {
	store := state.NewStore()
	ps := NewPositionSizer(store, 0.3)

	// No heat → full size
	fullSize := ps.Size(state.ChainSolana, 80)
	if !almostEqual(fullSize, 0.3, 0.001) {
		t.Errorf("full size: got %f, want 0.3", fullSize)
	}

	// Heat = 0.5 → size *= (1.0 - 0.5*0.5) = 0.75 → 0.3 * 0.75 = 0.225
	ps.SetHeatFunc(func() float64 { return 0.5 })
	got := ps.Size(state.ChainSolana, 80)
	if !almostEqual(got, 0.225, 0.001) {
		t.Errorf("heat=0.5: got %f, want 0.225", got)
	}

	// Heat = 1.0 → size *= 0.5 → 0.3 * 0.5 = 0.15
	ps.SetHeatFunc(func() float64 { return 1.0 })
	got = ps.Size(state.ChainSolana, 80)
	if !almostEqual(got, 0.15, 0.001) {
		t.Errorf("heat=1.0: got %f, want 0.15", got)
	}
}

func TestSize_GasReserveBlock(t *testing.T) {
	store := state.NewStore()
	ps := NewPositionSizer(store, 0.3)
	ps.SetGasReserve(0.005)

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

func TestSize_ConcentrationScaling(t *testing.T) {
	store := state.NewStore()
	store.SetGasBalance(state.ChainSolana, 1.0)
	ps := NewPositionSizer(store, 0.3)
	ps.SetGasReserve(0.005)
	ps.SetMaxPositions(5)

	// 0 open positions → full size
	fullSize := ps.Size(state.ChainSolana, 80)

	// Add 3 open positions
	for i := 0; i < 3; i++ {
		store.AddPosition(&state.Position{
			ID:           fmt.Sprintf("p%d", i),
			Chain:        state.ChainSolana,
			TokenAddress: fmt.Sprintf("addr%d", i),
		})
	}

	// 3/5 open → 1.0 - 0.6*0.6 = 0.64x
	reducedSize := ps.Size(state.ChainSolana, 80)
	if reducedSize >= fullSize {
		t.Errorf("size should decrease with more positions: full=%.4f reduced=%.4f", fullSize, reducedSize)
	}

	expectedRatio := 0.64 // (1.0 - 3/5 * 0.6)
	actualRatio := reducedSize / fullSize
	if actualRatio < expectedRatio-0.05 || actualRatio > expectedRatio+0.05 {
		t.Errorf("concentration ratio wrong: got %.2f, want ~%.2f", actualRatio, expectedRatio)
	}
}

func TestSize_UnknownChain(t *testing.T) {
	store := state.NewStore()
	ps := NewPositionSizer(store, 0.3)

	got := ps.Size("unknown", 80)
	if got != 0 {
		t.Errorf("got %f, want 0 for unknown chain", got)
	}
}
