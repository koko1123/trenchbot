package capital

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/cindocode/trenchbot/internal/state"
)

func newTestSweeper(reserve, idleThreshold, minSweep float64, idleMin int) *Sweeper {
	store := state.NewStore()
	return &Sweeper{
		store:         store,
		log:           slog.Default(),
		shadow:        true,
		bankAddress:   "BankAddr1111111111111111111111111111111111111",
		reserveSOL:    reserve,
		idleThreshold: idleThreshold,
		idleDuration:  time.Duration(idleMin) * time.Minute,
		cooldown:      10 * time.Minute,
		minSweep:      minSweep,
	}
}

func TestSweeper_NoExcess(t *testing.T) {
	s := newTestSweeper(10, 0.3, 0.5, 60)
	s.store.SetGasBalance(state.ChainSolana, 8.0) // below reserve

	s.Check(context.Background())

	if !s.excessSince.IsZero() {
		t.Error("idle timer should not start when balance < reserve")
	}
}

func TestSweeper_ExcessBelowThreshold(t *testing.T) {
	s := newTestSweeper(10, 0.3, 0.5, 60)
	s.store.SetGasBalance(state.ChainSolana, 12.0) // excess=2, threshold=3 (30% of 10)

	s.Check(context.Background())

	if !s.excessSince.IsZero() {
		t.Error("idle timer should not start when excess < reserve*threshold")
	}
}

func TestSweeper_ExcessStartsTimer(t *testing.T) {
	s := newTestSweeper(10, 0.3, 0.5, 60)
	s.store.SetGasBalance(state.ChainSolana, 20.0) // excess=10, well above 3

	s.Check(context.Background())

	if s.excessSince.IsZero() {
		t.Error("idle timer should have started")
	}
	if !s.lastSweep.IsZero() {
		t.Error("should not have swept yet (first check just starts timer)")
	}
}

func TestSweeper_TimerResetsOnDrawdown(t *testing.T) {
	s := newTestSweeper(10, 0.3, 0.5, 60)
	s.store.SetGasBalance(state.ChainSolana, 20.0)

	// Start timer.
	s.Check(context.Background())
	if s.excessSince.IsZero() {
		t.Fatal("timer should have started")
	}

	// Simulate drawdown — balance drops below threshold.
	s.store.SetGasBalance(state.ChainSolana, 11.0) // excess=1, below 3 threshold
	s.Check(context.Background())

	if !s.excessSince.IsZero() {
		t.Error("idle timer should have reset after drawdown")
	}
}

func TestSweeper_SweepsAfterIdlePeriod(t *testing.T) {
	s := newTestSweeper(10, 0.3, 0.5, 60)
	s.store.SetGasBalance(state.ChainSolana, 25.0) // excess=15

	// Start timer.
	s.Check(context.Background())

	// Fast-forward: pretend excess was detected 61 minutes ago.
	s.excessSince = time.Now().Add(-61 * time.Minute)

	// This should trigger a sweep.
	s.Check(context.Background())

	if s.lastSweep.IsZero() {
		t.Error("sweep should have executed after idle period")
	}
	if !s.excessSince.IsZero() {
		t.Error("idle timer should reset after sweep")
	}
}

func TestSweeper_RespectsCooldown(t *testing.T) {
	s := newTestSweeper(10, 0.3, 0.5, 60)
	s.store.SetGasBalance(state.ChainSolana, 25.0)

	// Simulate a recent sweep.
	s.lastSweep = time.Now().Add(-5 * time.Minute) // 5 min ago, cooldown is 10 min
	s.excessSince = time.Now().Add(-61 * time.Minute)

	prevSweep := s.lastSweep
	s.Check(context.Background())

	if s.lastSweep != prevSweep {
		t.Error("should not sweep during cooldown period")
	}
}

func TestSweeper_MinSweepAmount(t *testing.T) {
	s := newTestSweeper(10, 0.3, 0.5, 60)
	s.store.SetGasBalance(state.ChainSolana, 14.0) // excess=4, above threshold

	// Start timer.
	s.Check(context.Background())
	s.excessSince = time.Now().Add(-61 * time.Minute)

	// Now reduce balance so excess < minSweep.
	s.store.SetGasBalance(state.ChainSolana, 10.3) // excess=0.3, below minSweep of 0.5

	s.Check(context.Background())

	if !s.lastSweep.IsZero() {
		t.Error("should not sweep when amount < minSweep")
	}
	if !s.excessSince.IsZero() {
		t.Error("timer should reset when sweep amount < min")
	}
}

func TestNewSweeper_NilWhenNoBankAddress(t *testing.T) {
	store := state.NewStore()
	s := NewSweeper(nil, store, SweeperConfig{}, slog.Default())
	if s != nil {
		t.Error("should return nil when no bank address configured")
	}
}

func TestNewSweeper_DefaultValues(t *testing.T) {
	store := state.NewStore()
	s := NewSweeper(nil, store, SweeperConfig{
		BankAddress: "SomeAddress",
	}, slog.Default())

	if s == nil {
		t.Fatal("should not be nil with bank address set")
	}
	if s.reserveSOL != 10.0 {
		t.Errorf("default reserve should be 10, got %f", s.reserveSOL)
	}
	if s.idleThreshold != 0.3 {
		t.Errorf("default idle threshold should be 0.3, got %f", s.idleThreshold)
	}
	if s.idleDuration != 60*time.Minute {
		t.Errorf("default idle duration should be 60m, got %v", s.idleDuration)
	}
	if s.minSweep != 0.5 {
		t.Errorf("default min sweep should be 0.5, got %f", s.minSweep)
	}
}
