package risk

import (
	"log/slog"
	"sync"
	"time"

	"github.com/cindocode/trenchbot/internal/clock"
	"github.com/cindocode/trenchbot/internal/state"
)

type CircuitBreaker struct {
	mu                  sync.RWMutex
	chain               state.Chain
	store               *state.Store
	clock               clock.Clock
	log                 *slog.Logger
	maxDrawdownPct      float64
	consecutiveLossCap  int
	maxSnipesPerHour    int
	consecutiveLosses   int
	pausedUntil         time.Time
	halted              bool
	snipeTimestamps     []time.Time
	errorTimestamps     []time.Time
	startingEquity      float64
	consecutivePauseCycles int
	heatFullPct            float64
}

type CircuitBreakerConfig struct {
	Chain              state.Chain
	MaxDrawdownPct     float64
	HeatFullPct        float64 // hourly loss % at which heat reaches 1.0 (default 15)
	ConsecutiveLossCap int
	MaxSnipesPerHour   int
	StartingEquity     float64
}

func NewCircuitBreaker(cfg CircuitBreakerConfig, store *state.Store, clk clock.Clock, log *slog.Logger) *CircuitBreaker {
	return &CircuitBreaker{
		chain:              cfg.Chain,
		store:              store,
		clock:              clk,
		log:                log,
		maxDrawdownPct:     cfg.MaxDrawdownPct,
		heatFullPct:        cfg.HeatFullPct,
		consecutiveLossCap: cfg.ConsecutiveLossCap,
		maxSnipesPerHour:   cfg.MaxSnipesPerHour,
		startingEquity:     cfg.StartingEquity,
	}
}

func (cb *CircuitBreaker) CanSnipe() bool {
	cb.mu.RLock()
	defer cb.mu.RUnlock()

	if cb.halted {
		return false
	}
	if cb.clock.Now().Before(cb.pausedUntil) {
		return false
	}
	return !cb.hourlyLimitReached()
}

func (cb *CircuitBreaker) RecordSnipe() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.snipeTimestamps = append(cb.snipeTimestamps, cb.clock.Now())

	// Prune entries older than 1 hour.
	cutoff := cb.clock.Now().Add(-1 * time.Hour)
	fresh := cb.snipeTimestamps[:0]
	for _, t := range cb.snipeTimestamps {
		if t.After(cutoff) {
			fresh = append(fresh, t)
		}
	}
	cb.snipeTimestamps = fresh
}

func (cb *CircuitBreaker) RecordLoss() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.consecutiveLosses++
	if cb.consecutiveLosses >= cb.consecutiveLossCap {
		cb.consecutivePauseCycles++
		// Escalate: 1h, 2h, 4h, max 8h.
		hours := 1 << min(cb.consecutivePauseCycles-1, 3)
		cb.pausedUntil = cb.clock.Now().Add(time.Duration(hours) * time.Hour)
		cb.log.Warn("circuit breaker: consecutive loss pause",
			"chain", cb.chain,
			"losses", cb.consecutiveLosses,
			"pause_hours", hours,
			"cycle", cb.consecutivePauseCycles,
			"paused_until", cb.pausedUntil,
		)
		cb.consecutiveLosses = 0
	}
}

func (cb *CircuitBreaker) RecordWin() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.consecutiveLosses = 0
}

func (cb *CircuitBreaker) RecordError() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	now := cb.clock.Now()
	cb.errorTimestamps = append(cb.errorTimestamps, now)

	// Prune entries older than 10 minutes.
	cutoff := now.Add(-10 * time.Minute)
	fresh := cb.errorTimestamps[:0]
	for _, t := range cb.errorTimestamps {
		if t.After(cutoff) {
			fresh = append(fresh, t)
		}
	}
	cb.errorTimestamps = fresh

	if len(fresh) > 5 {
		cb.pausedUntil = now.Add(10 * time.Minute)
		cb.log.Warn("circuit breaker: error rate pause",
			"chain", cb.chain,
			"errors_10m", len(fresh),
		)
	}
}

func (cb *CircuitBreaker) Check(currentEquity float64) {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	peak := cb.store.GetPeakEquity(cb.chain)
	if peak == 0 {
		peak = cb.startingEquity
	}

	drawdownPct := ((peak - currentEquity) / peak) * 100
	if drawdownPct >= cb.maxDrawdownPct {
		cb.halted = true
		cb.log.Error("circuit breaker: HALTED",
			"chain", cb.chain,
			"drawdown_pct", drawdownPct,
			"peak", peak,
			"current", currentEquity,
		)
	}

}

func (cb *CircuitBreaker) IsHalted() bool {
	cb.mu.RLock()
	defer cb.mu.RUnlock()
	return cb.halted
}

// Status returns a human-readable status string for the circuit breaker.
func (cb *CircuitBreaker) Status() string {
	cb.mu.RLock()
	defer cb.mu.RUnlock()

	if cb.halted {
		return "halted"
	}
	if cb.clock.Now().Before(cb.pausedUntil) {
		return "paused"
	}
	if cb.hourlyLimitReached() {
		return "rate-limited"
	}
	return "ok"
}

// ConsecutiveLosses returns the current consecutive loss count.
func (cb *CircuitBreaker) ConsecutiveLosses() int {
	cb.mu.RLock()
	defer cb.mu.RUnlock()
	return cb.consecutiveLosses
}

// ResetPauseCycles resets the escalating pause cycle counter.
func (cb *CircuitBreaker) ResetPauseCycles() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.consecutivePauseCycles = 0
}

// Heat returns the current heat level (0.0–1.0) based on hourly loss relative
// to heatFullPct. Heat drives dynamic filter tightening and position sizing.
func (cb *CircuitBreaker) Heat() float64 {
	pnl := cb.store.GetDailyPnL(cb.chain)
	if pnl >= 0 {
		return 0
	}
	if cb.startingEquity <= 0 || cb.heatFullPct <= 0 {
		return 0
	}
	lossPct := (-pnl / cb.startingEquity) * 100
	heat := lossPct / cb.heatFullPct
	if heat > 1.0 {
		heat = 1.0
	}
	return heat
}

func (cb *CircuitBreaker) hourlyLimitReached() bool {
	cutoff := cb.clock.Now().Add(-1 * time.Hour)
	recent := 0
	for _, t := range cb.snipeTimestamps {
		if t.After(cutoff) {
			recent++
		}
	}
	return recent >= cb.maxSnipesPerHour
}
