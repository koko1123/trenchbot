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
	dailyLossLimitPct   float64
	consecutiveLossCap  int
	maxSnipesPerHour    int
	consecutiveLosses   int
	pausedUntil         time.Time
	halted              bool
	snipeTimestamps     []time.Time
	errorTimestamps     []time.Time
	startingEquity      float64
}

type CircuitBreakerConfig struct {
	Chain               state.Chain
	MaxDrawdownPct      float64
	DailyLossLimitPct   float64
	ConsecutiveLossCap  int
	MaxSnipesPerHour    int
	StartingEquity      float64
}

func NewCircuitBreaker(cfg CircuitBreakerConfig, store *state.Store, clk clock.Clock, log *slog.Logger) *CircuitBreaker {
	return &CircuitBreaker{
		chain:              cfg.Chain,
		store:              store,
		clock:              clk,
		log:                log,
		maxDrawdownPct:     cfg.MaxDrawdownPct,
		dailyLossLimitPct:  cfg.DailyLossLimitPct,
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
}

func (cb *CircuitBreaker) RecordLoss() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.consecutiveLosses++
	if cb.consecutiveLosses >= cb.consecutiveLossCap {
		cb.pausedUntil = cb.clock.Now().Add(1 * time.Hour)
		cb.log.Warn("circuit breaker: consecutive loss pause",
			"chain", cb.chain,
			"losses", cb.consecutiveLosses,
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

	cutoff := now.Add(-10 * time.Minute)
	recentErrors := 0
	for _, t := range cb.errorTimestamps {
		if t.After(cutoff) {
			recentErrors++
		}
	}
	if recentErrors > 5 {
		cb.pausedUntil = now.Add(10 * time.Minute)
		cb.log.Warn("circuit breaker: error rate pause",
			"chain", cb.chain,
			"errors_10m", recentErrors,
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
