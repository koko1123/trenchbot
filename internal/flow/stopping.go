package flow

import (
	"math"
	"time"
)

// AdaptiveStopper implements optimal stopping for the observation window.
// Instead of a fixed "wait N seconds then decide", it uses decreasing quality
// thresholds over time. Exceptional tokens get bought early (1-2s), weak tokens
// wait the full window or get rejected.
type AdaptiveStopper struct {
	windowSec  int
	thresholds []float64 // quality threshold at each second
}

// NewAdaptiveStopper creates a stopper with pre-computed thresholds based on
// backward induction logic. Early seconds require exceptional signals; later
// seconds accept lower quality.
func NewAdaptiveStopper(windowSec int) *AdaptiveStopper {
	if windowSec <= 0 {
		windowSec = 10
	}

	// Compute thresholds: exponential decay from 0.9 to 0.2 over the window.
	thresholds := make([]float64, windowSec)
	for i := range thresholds {
		// t goes from 0 (start) to 1 (end of window).
		t := float64(i) / float64(windowSec-1)
		// Exponential decay: 0.9 * exp(-1.5*t) but clamped to [0.2, 0.9].
		threshold := 0.9 * math.Exp(-1.5*t)
		if threshold < 0.2 {
			threshold = 0.2
		}
		thresholds[i] = threshold
	}

	return &AdaptiveStopper{
		windowSec:  windowSec,
		thresholds: thresholds,
	}
}

// ShouldBuy returns true if the current quality score exceeds the threshold
// for the given elapsed time in the observation window.
func (s *AdaptiveStopper) ShouldBuy(elapsed time.Duration, qualityScore float64) bool {
	sec := int(elapsed.Seconds())
	if sec < 0 {
		sec = 0
	}
	if sec >= len(s.thresholds) {
		sec = len(s.thresholds) - 1
	}
	return qualityScore >= s.thresholds[sec]
}

// Threshold returns the quality threshold at a given second (for logging/debugging).
func (s *AdaptiveStopper) Threshold(elapsed time.Duration) float64 {
	sec := int(elapsed.Seconds())
	if sec < 0 {
		sec = 0
	}
	if sec >= len(s.thresholds) {
		sec = len(s.thresholds) - 1
	}
	return s.thresholds[sec]
}

// QualityScore computes a normalized quality score from observation metrics.
// Range: [0, 1] where higher is better.
func QualityScore(obs ObservationResult) float64 {
	score := 0.0

	// OFI: 30% weight — strong buying pressure is the primary signal.
	score += clamp(obs.OFI, 0, 1) * 0.30

	// Liquidity velocity: 30% weight — strongest graduation predictor.
	score += clamp(obs.LiquidityVelocity/0.5, 0, 1) * 0.30

	// Trade entropy: 15% weight — diverse buyers is bullish.
	score += clamp(obs.TradeEntropy/2.0, 0, 1) * 0.15

	// Bot presence: 15% weight — fewer bots is better.
	botPenalty := clamp(float64(obs.BotBuyCount)/3.0, 0, 1)
	score += (1.0 - botPenalty) * 0.15

	// OFI acceleration: 10% weight — increasing pressure is bullish.
	score += clamp(obs.OFIAcceleration+0.5, 0, 1) * 0.10

	return score
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
