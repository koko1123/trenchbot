package risk

import (
	"log/slog"
	"sync"
	"time"
)

// AlphaMonitor detects strategy degradation by tracking rolling win rates
// and automatically adapting filter/sizing parameters.
type AlphaMonitor struct {
	mu              sync.RWMutex
	tracker         *PerformanceTracker
	log             *slog.Logger
	hourlyWinRates  []float64 // rolling hourly win rates
	degradedHours   int       // consecutive hours below threshold
	recoveredHours  int       // consecutive hours above recovery threshold
	adapted         bool      // true if parameters have been tightened

	// Thresholds
	degradeWinRate float64 // win rate below which we consider degraded (default 0.30)
	recoverWinRate float64 // win rate above which we consider recovered (default 0.40)
	degradeHours   int     // consecutive hours below threshold to trigger (default 2)
	recoverHours   int     // consecutive hours above threshold to recover (default 2)

	// Callbacks for adaptation
	onDegrade func() // called when alpha degradation detected
	onRecover func() // called when alpha recovers
}

// AlphaMonitorConfig holds configuration for the alpha monitor.
type AlphaMonitorConfig struct {
	DegradeWinRate float64 // default 0.30
	RecoverWinRate float64 // default 0.40
	DegradeHours   int     // default 2
	RecoverHours   int     // default 2
}

// NewAlphaMonitor creates a new alpha monitor backed by the given performance tracker.
func NewAlphaMonitor(tracker *PerformanceTracker, cfg AlphaMonitorConfig, log *slog.Logger) *AlphaMonitor {
	if cfg.DegradeWinRate <= 0 {
		cfg.DegradeWinRate = 0.30
	}
	if cfg.RecoverWinRate <= 0 {
		cfg.RecoverWinRate = 0.40
	}
	if cfg.DegradeHours <= 0 {
		cfg.DegradeHours = 2
	}
	if cfg.RecoverHours <= 0 {
		cfg.RecoverHours = 2
	}
	return &AlphaMonitor{
		tracker:        tracker,
		log:            log,
		degradeWinRate: cfg.DegradeWinRate,
		recoverWinRate: cfg.RecoverWinRate,
		degradeHours:   cfg.DegradeHours,
		recoverHours:   cfg.RecoverHours,
	}
}

// SetOnDegrade sets the callback for when alpha degradation is detected.
func (am *AlphaMonitor) SetOnDegrade(fn func()) {
	am.mu.Lock()
	defer am.mu.Unlock()
	am.onDegrade = fn
}

// SetOnRecover sets the callback for when alpha recovers.
func (am *AlphaMonitor) SetOnRecover(fn func()) {
	am.mu.Lock()
	defer am.mu.Unlock()
	am.onRecover = fn
}

// Check evaluates the current performance and triggers adaptation if needed.
// Call this hourly.
func (am *AlphaMonitor) Check() {
	stats := am.tracker.Stats()
	if stats.TradeCount < 5 {
		return // not enough data
	}

	am.mu.Lock()
	defer am.mu.Unlock()

	winRate := stats.WinRate
	am.hourlyWinRates = append(am.hourlyWinRates, winRate)
	// Keep only last 24 hours.
	if len(am.hourlyWinRates) > 24 {
		am.hourlyWinRates = am.hourlyWinRates[len(am.hourlyWinRates)-24:]
	}

	if winRate < am.degradeWinRate {
		am.degradedHours++
		am.recoveredHours = 0
	} else if winRate >= am.recoverWinRate {
		am.recoveredHours++
		am.degradedHours = 0
	} else {
		// Between thresholds — no change to counters.
		am.degradedHours = 0
		am.recoveredHours = 0
	}

	if !am.adapted && am.degradedHours >= am.degradeHours {
		am.adapted = true
		am.log.Warn("alpha degradation detected, tightening parameters",
			"win_rate", winRate,
			"degraded_hours", am.degradedHours,
		)
		if am.onDegrade != nil {
			am.onDegrade()
		}
	}

	if am.adapted && am.recoveredHours >= am.recoverHours {
		am.adapted = false
		am.log.Info("alpha recovered, restoring parameters",
			"win_rate", winRate,
			"recovered_hours", am.recoveredHours,
		)
		if am.onRecover != nil {
			am.onRecover()
		}
	}
}

// IsAdapted returns true if the monitor has detected degradation and tightened parameters.
func (am *AlphaMonitor) IsAdapted() bool {
	am.mu.RLock()
	defer am.mu.RUnlock()
	return am.adapted
}

// HourlyWinRates returns the rolling hourly win rates for reporting.
func (am *AlphaMonitor) HourlyWinRates() []float64 {
	am.mu.RLock()
	defer am.mu.RUnlock()
	result := make([]float64, len(am.hourlyWinRates))
	copy(result, am.hourlyWinRates)
	return result
}

// Run starts the alpha monitor's hourly check loop.
func (am *AlphaMonitor) Run(done <-chan struct{}) {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			am.Check()
		}
	}
}
