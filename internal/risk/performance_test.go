package risk

import (
	"math"
	"testing"
)

func TestPerformanceTracker_Empty(t *testing.T) {
	pt := NewPerformanceTracker(10)
	stats := pt.Stats()
	if stats.TradeCount != 0 {
		t.Errorf("expected 0 trades, got %d", stats.TradeCount)
	}
	if stats.KellyFraction != 0 {
		t.Errorf("expected 0 kelly, got %f", stats.KellyFraction)
	}
}

func TestPerformanceTracker_AllWins(t *testing.T) {
	pt := NewPerformanceTracker(10)
	for i := 0; i < 5; i++ {
		pt.Record(TradeOutcome{PnLPct: 50, Score: 70})
	}
	stats := pt.Stats()
	if stats.WinRate != 1.0 {
		t.Errorf("expected 100%% win rate, got %f", stats.WinRate)
	}
	if stats.AvgWinPct != 50.0 {
		t.Errorf("expected 50%% avg win, got %f", stats.AvgWinPct)
	}
}

func TestPerformanceTracker_MixedTrades(t *testing.T) {
	pt := NewPerformanceTracker(10)
	// 6 wins at +100%, 4 losses at -50%
	for i := 0; i < 6; i++ {
		pt.Record(TradeOutcome{PnLPct: 100, Score: 75})
	}
	for i := 0; i < 4; i++ {
		pt.Record(TradeOutcome{PnLPct: -50, Score: 60})
	}

	stats := pt.Stats()
	if stats.TradeCount != 10 {
		t.Errorf("expected 10 trades, got %d", stats.TradeCount)
	}
	if math.Abs(stats.WinRate-0.6) > 0.001 {
		t.Errorf("expected 0.6 win rate, got %f", stats.WinRate)
	}
	if stats.AvgWinPct != 100 {
		t.Errorf("expected 100%% avg win, got %f", stats.AvgWinPct)
	}
	if stats.AvgLossPct != 50 {
		t.Errorf("expected 50%% avg loss, got %f", stats.AvgLossPct)
	}

	// Kelly = 0.6 - 0.4/2.0 = 0.4, half-Kelly = 0.2
	expectedKelly := 0.2
	if math.Abs(stats.KellyFraction-expectedKelly) > 0.001 {
		t.Errorf("expected kelly %f, got %f", expectedKelly, stats.KellyFraction)
	}
}

func TestPerformanceTracker_CircularBuffer(t *testing.T) {
	pt := NewPerformanceTracker(5)
	// Fill with losses
	for i := 0; i < 5; i++ {
		pt.Record(TradeOutcome{PnLPct: -30, Score: 55})
	}
	// Overwrite with wins
	for i := 0; i < 5; i++ {
		pt.Record(TradeOutcome{PnLPct: 80, Score: 80})
	}

	stats := pt.Stats()
	if stats.WinRate != 1.0 {
		t.Errorf("expected 100%% win rate after overwrite, got %f", stats.WinRate)
	}
}

func TestPerformanceTracker_ScoreBucket(t *testing.T) {
	pt := NewPerformanceTracker(20)
	// Low-score trades: mostly losses
	for i := 0; i < 5; i++ {
		pt.Record(TradeOutcome{PnLPct: -40, Score: 58})
	}
	// High-score trades: mostly wins
	for i := 0; i < 5; i++ {
		pt.Record(TradeOutcome{PnLPct: 120, Score: 82})
	}

	low := pt.ScoreBucketStats(55, 65)
	if low.WinRate != 0.0 {
		t.Errorf("expected 0%% win rate for low scores, got %f", low.WinRate)
	}

	high := pt.ScoreBucketStats(75, 100)
	if high.WinRate != 1.0 {
		t.Errorf("expected 100%% win rate for high scores, got %f", high.WinRate)
	}
}

func TestPerformanceTracker_NegativeKelly(t *testing.T) {
	pt := NewPerformanceTracker(10)
	// 20% win rate, 1:1 ratio -> Kelly negative
	for i := 0; i < 2; i++ {
		pt.Record(TradeOutcome{PnLPct: 50, Score: 60})
	}
	for i := 0; i < 8; i++ {
		pt.Record(TradeOutcome{PnLPct: -50, Score: 60})
	}

	stats := pt.Stats()
	if stats.KellyFraction >= 0 {
		t.Errorf("expected negative kelly, got %f", stats.KellyFraction)
	}
}
