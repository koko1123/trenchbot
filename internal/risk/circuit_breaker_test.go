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
		HeatFullPct:        15,
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

func TestHeat_NoLoss(t *testing.T) {
	clk := clock.NewSimClock(time.Now())
	cb, _ := newTestBreaker(clk)

	heat := cb.Heat()
	if heat != 0 {
		t.Errorf("heat should be 0 with no losses, got %f", heat)
	}
}

func TestHeat_PartialLoss(t *testing.T) {
	clk := clock.NewSimClock(time.Now())
	cb, store := newTestBreaker(clk)

	// 7.5% hourly loss on 1200 equity → heat = 7.5/15 = 0.5
	store.UpdateDailyPnL(state.ChainSolana, -90)
	heat := cb.Heat()
	if !almostEqual(heat, 0.5, 0.01) {
		t.Errorf("heat should be ~0.5, got %f", heat)
	}
}

func TestHeat_FullLoss(t *testing.T) {
	clk := clock.NewSimClock(time.Now())
	cb, store := newTestBreaker(clk)

	// 15% hourly loss on 1200 equity → heat = 15/15 = 1.0
	store.UpdateDailyPnL(state.ChainSolana, -180)
	heat := cb.Heat()
	if !almostEqual(heat, 1.0, 0.01) {
		t.Errorf("heat should be 1.0, got %f", heat)
	}
}

func TestHeat_CappedAtOne(t *testing.T) {
	clk := clock.NewSimClock(time.Now())
	cb, store := newTestBreaker(clk)

	// 25% hourly loss → heat should still be capped at 1.0
	store.UpdateDailyPnL(state.ChainSolana, -300)
	heat := cb.Heat()
	if heat != 1.0 {
		t.Errorf("heat should be capped at 1.0, got %f", heat)
	}
}

func TestHeat_WinningReducesHeat(t *testing.T) {
	clk := clock.NewSimClock(time.Now())
	cb, store := newTestBreaker(clk)

	// Start with losses → heat > 0
	store.UpdateDailyPnL(state.ChainSolana, -90) // heat = 0.5
	heat1 := cb.Heat()

	// Winning trade reduces loss
	store.UpdateDailyPnL(state.ChainSolana, 60) // net = -30, heat = (30/1200*100)/15 ≈ 0.167
	heat2 := cb.Heat()

	if heat2 >= heat1 {
		t.Errorf("heat should decrease after winning: before=%f after=%f", heat1, heat2)
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
