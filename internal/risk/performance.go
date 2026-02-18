package risk

import (
	"math"
	"sync"
)

// TradeOutcome records the result of a closed trade for performance tracking.
type TradeOutcome struct {
	PnLPct     float64        // percentage gain/loss (-100 to +inf)
	Score      int            // filter score that approved this trade
	ExitReason string         // reason for exit (stop-loss, tranche-1, etc.)
	OFI        float64        // order flow imbalance at entry
	EntryHeat  float64        // heat level at entry (0.0-1.0)
	Signals    map[string]int // per-signal scores at entry
}

// PerformanceTracker maintains a rolling window of trade outcomes and computes
// Kelly criterion metrics. Thread-safe.
type PerformanceTracker struct {
	mu       sync.RWMutex
	trades   []TradeOutcome
	maxSize  int
	cursor   int  // next write position (circular buffer)
	filled   bool // true once buffer has been fully populated at least once
}

// NewPerformanceTracker creates a tracker with a rolling window of the given size.
func NewPerformanceTracker(windowSize int) *PerformanceTracker {
	if windowSize <= 0 {
		windowSize = 50
	}
	return &PerformanceTracker{
		trades:  make([]TradeOutcome, windowSize),
		maxSize: windowSize,
	}
}

// Record adds a trade outcome to the rolling window.
func (pt *PerformanceTracker) Record(outcome TradeOutcome) {
	pt.mu.Lock()
	defer pt.mu.Unlock()
	pt.trades[pt.cursor] = outcome
	pt.cursor++
	if pt.cursor >= pt.maxSize {
		pt.cursor = 0
		pt.filled = true
	}
}

// count returns the number of recorded trades.
func (pt *PerformanceTracker) count() int {
	if pt.filled {
		return pt.maxSize
	}
	return pt.cursor
}

// activeTrades returns the slice of recorded trades.
func (pt *PerformanceTracker) activeTrades() []TradeOutcome {
	n := pt.count()
	if pt.filled {
		return pt.trades
	}
	return pt.trades[:n]
}

// Stats returns the current performance metrics.
func (pt *PerformanceTracker) Stats() PerformanceStats {
	pt.mu.RLock()
	defer pt.mu.RUnlock()

	trades := pt.activeTrades()
	n := len(trades)
	if n == 0 {
		return PerformanceStats{}
	}

	var wins, losses int
	var totalWinPct, totalLossPct float64

	for _, t := range trades {
		if t.PnLPct >= 0 {
			wins++
			totalWinPct += t.PnLPct
		} else {
			losses++
			totalLossPct += math.Abs(t.PnLPct)
		}
	}

	stats := PerformanceStats{
		TradeCount: n,
		WinCount:   wins,
		LossCount:  losses,
	}

	if n > 0 {
		stats.WinRate = float64(wins) / float64(n)
	}
	if wins > 0 {
		stats.AvgWinPct = totalWinPct / float64(wins)
	}
	if losses > 0 {
		stats.AvgLossPct = totalLossPct / float64(losses)
	}

	// Half-Kelly fraction.
	if stats.AvgLossPct > 0 {
		rewardRatio := stats.AvgWinPct / stats.AvgLossPct
		kelly := stats.WinRate - (1-stats.WinRate)/rewardRatio
		stats.KellyFraction = kelly * 0.5
	}

	return stats
}

// ScoreBucketStats returns performance stats for a specific score range.
func (pt *PerformanceTracker) ScoreBucketStats(minScore, maxScore int) PerformanceStats {
	pt.mu.RLock()
	defer pt.mu.RUnlock()

	trades := pt.activeTrades()
	var filtered []TradeOutcome
	for _, t := range trades {
		if t.Score >= minScore && t.Score < maxScore {
			filtered = append(filtered, t)
		}
	}

	n := len(filtered)
	if n == 0 {
		return PerformanceStats{}
	}

	var wins, losses int
	var totalWinPct, totalLossPct float64

	for _, t := range filtered {
		if t.PnLPct >= 0 {
			wins++
			totalWinPct += t.PnLPct
		} else {
			losses++
			totalLossPct += math.Abs(t.PnLPct)
		}
	}

	stats := PerformanceStats{
		TradeCount: n,
		WinCount:   wins,
		LossCount:  losses,
	}

	if n > 0 {
		stats.WinRate = float64(wins) / float64(n)
	}
	if wins > 0 {
		stats.AvgWinPct = totalWinPct / float64(wins)
	}
	if losses > 0 {
		stats.AvgLossPct = totalLossPct / float64(losses)
	}

	if stats.AvgLossPct > 0 {
		rewardRatio := stats.AvgWinPct / stats.AvgLossPct
		kelly := stats.WinRate - (1-stats.WinRate)/rewardRatio
		stats.KellyFraction = kelly * 0.5
	}

	return stats
}

// PerformanceStats holds computed performance metrics.
type PerformanceStats struct {
	TradeCount    int
	WinCount      int
	LossCount     int
	WinRate       float64 // 0.0-1.0
	AvgWinPct     float64 // average winning trade PnL%
	AvgLossPct    float64 // average losing trade PnL% (positive number)
	KellyFraction float64 // half-Kelly optimal fraction
}
