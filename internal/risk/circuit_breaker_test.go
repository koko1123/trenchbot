package risk

import (
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/cindocode/trenchbot/internal/clock"
	"github.com/cindocode/trenchbot/internal/state"
)

var testLog = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

func newTestBreaker(clk *clock.SimClock) (*CircuitBreaker, *state.Store) {
	store := state.NewStore()
	store.SetPeakEquity(state.ChainSolana, 1200)
	cb := NewCircuitBreaker(CircuitBreakerConfig{
		Chain:              state.ChainSolana,
		MaxDrawdownPct:     50,
		DailyLossLimitPct:  8,
		ConsecutiveLossCap: 10,
		MaxSnipesPerHour:   10,
		StartingEquity:     1200,
	}, store, clk, testLog)
	return cb, store
}

func TestCanSnipe_Fresh(t *testing.T) {
	clk := clock.NewSimClock(time.Now())
	cb, _ := newTestBreaker(clk)
	if !cb.CanSnipe() {
		t.Error("fresh breaker should allow sniping")
	}
}

func TestCanSnipe_Halted(t *testing.T) {
	clk := clock.NewSimClock(time.Now())
	cb, _ := newTestBreaker(clk)
	cb.Check(500) // 1200 → 500 = 58% drawdown > 50%
	if cb.CanSnipe() {
		t.Error("halted breaker should not allow sniping")
	}
	if !cb.IsHalted() {
		t.Error("should be halted")
	}
}

func TestConsecutiveLossPause(t *testing.T) {
	clk := clock.NewSimClock(time.Now())
	cb, _ := newTestBreaker(clk)

	for i := 0; i < 10; i++ {
		cb.RecordLoss()
	}
	if cb.CanSnipe() {
		t.Error("should be paused after 10 consecutive losses")
	}

	// Advance past pause
	clk.Advance(61 * time.Minute)
	if !cb.CanSnipe() {
		t.Error("should be unpaused after 1 hour")
	}
}

func TestWinResetsLosses(t *testing.T) {
	clk := clock.NewSimClock(time.Now())
	cb, _ := newTestBreaker(clk)

	for i := 0; i < 9; i++ {
		cb.RecordLoss()
	}
	cb.RecordWin() // reset at 9

	cb.RecordLoss() // only 1 now
	if !cb.CanSnipe() {
		t.Error("win should have reset loss counter")
	}
}

func TestErrorRatePause(t *testing.T) {
	clk := clock.NewSimClock(time.Now())
	cb, _ := newTestBreaker(clk)

	for i := 0; i < 6; i++ {
		cb.RecordError()
	}
	if cb.CanSnipe() {
		t.Error("should be paused after 6 errors in 10 minutes")
	}

	clk.Advance(11 * time.Minute)
	if !cb.CanSnipe() {
		t.Error("should be unpaused after 10 minute pause expires")
	}
}

func TestOldErrorsExpire(t *testing.T) {
	clk := clock.NewSimClock(time.Now())
	cb, _ := newTestBreaker(clk)

	// 5 errors (not enough to trigger)
	for i := 0; i < 5; i++ {
		cb.RecordError()
	}
	if !cb.CanSnipe() {
		t.Error("5 errors should not trigger pause")
	}

	// Advance 11 min so those expire, then add 3 new errors
	clk.Advance(11 * time.Minute)
	for i := 0; i < 3; i++ {
		cb.RecordError()
	}
	if !cb.CanSnipe() {
		t.Error("old errors should have expired, only 3 recent")
	}
}

func TestDrawdownHalt_Boundary(t *testing.T) {
	clk := clock.NewSimClock(time.Now())
	cb, _ := newTestBreaker(clk)

	// 49% drawdown — should not halt
	cb.Check(612) // (1200-612)/1200 = 49%
	if cb.IsHalted() {
		t.Error("49% drawdown should not halt")
	}

	// Exactly 50% drawdown — should halt
	cb.Check(600) // (1200-600)/1200 = 50%
	if !cb.IsHalted() {
		t.Error("50% drawdown should halt")
	}
}

func TestDailyLossLimitPause(t *testing.T) {
	start := time.Date(2025, 6, 1, 14, 0, 0, 0, time.UTC) // 2pm UTC
	clk := clock.NewSimClock(start)
	cb, store := newTestBreaker(clk)

	// Simulate daily loss of 8% of starting equity (1200 * 0.08 = 96)
	store.UpdateDailyPnL(state.ChainSolana, -96)
	cb.Check(1104) // equity dropped but not enough for drawdown halt

	if cb.CanSnipe() {
		t.Error("should be paused after daily loss limit hit")
	}

	// Advance to just before 1 hour — still paused
	clk.Advance(59 * time.Minute)
	if cb.CanSnipe() {
		t.Error("should still be paused before 1 hour")
	}

	// Advance past 1 hour
	clk.Advance(2 * time.Minute)
	if !cb.CanSnipe() {
		t.Error("should be unpaused after 1 hour")
	}
}

func TestDailyLossLimitNotTriggered(t *testing.T) {
	clk := clock.NewSimClock(time.Now())
	cb, store := newTestBreaker(clk)

	// 7% daily loss — below 8% limit
	store.UpdateDailyPnL(state.ChainSolana, -84)
	cb.Check(1116)

	if !cb.CanSnipe() {
		t.Error("7% daily loss should not trigger pause")
	}
}

func TestHourlyRateLimit(t *testing.T) {
	clk := clock.NewSimClock(time.Now())
	cb, _ := newTestBreaker(clk)

	for i := 0; i < 10; i++ {
		cb.RecordSnipe()
	}
	if cb.CanSnipe() {
		t.Error("should be blocked after 10 snipes in 1 hour")
	}

	// Advance past the window
	clk.Advance(61 * time.Minute)
	if !cb.CanSnipe() {
		t.Error("should be unblocked after hourly window slides")
	}
}
