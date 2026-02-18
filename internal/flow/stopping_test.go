package flow

import (
	"testing"
	"time"
)

func TestAdaptiveStopper_EarlyBuyHighQuality(t *testing.T) {
	s := NewAdaptiveStopper(10)
	// Exceptional quality should trigger buy at T+1s.
	if !s.ShouldBuy(1*time.Second, 0.95) {
		t.Error("expected buy at T+1s with quality 0.95")
	}
}

func TestAdaptiveStopper_NoBuyEarlyLowQuality(t *testing.T) {
	s := NewAdaptiveStopper(10)
	// Low quality should NOT trigger at T+1s.
	if s.ShouldBuy(1*time.Second, 0.3) {
		t.Error("did not expect buy at T+1s with quality 0.3")
	}
}

func TestAdaptiveStopper_LateBuyMediumQuality(t *testing.T) {
	s := NewAdaptiveStopper(10)
	// Medium quality should trigger near end of window.
	if !s.ShouldBuy(9*time.Second, 0.3) {
		t.Error("expected buy at T+9s with quality 0.3")
	}
}

func TestAdaptiveStopper_ThresholdsDecreasing(t *testing.T) {
	s := NewAdaptiveStopper(10)
	prev := s.Threshold(0)
	for sec := 1; sec < 10; sec++ {
		curr := s.Threshold(time.Duration(sec) * time.Second)
		if curr > prev+0.001 {
			t.Errorf("threshold increased at second %d: %g -> %g", sec, prev, curr)
		}
		prev = curr
	}
}

func TestAdaptiveStopper_BoundsCheck(t *testing.T) {
	s := NewAdaptiveStopper(10)
	// Before window starts.
	thr := s.Threshold(-1 * time.Second)
	if thr < 0.2 || thr > 0.9 {
		t.Errorf("threshold out of bounds for negative time: %g", thr)
	}
	// After window ends.
	thr = s.Threshold(100 * time.Second)
	if thr < 0.2 || thr > 0.9 {
		t.Errorf("threshold out of bounds for past-window time: %g", thr)
	}
}

func TestQualityScore_PerfectSignals(t *testing.T) {
	obs := ObservationResult{
		OFI:               1.0,
		LiquidityVelocity: 0.5,
		TradeEntropy:       2.0,
		BotBuyCount:        0,
		OFIAcceleration:    0.5,
	}
	score := QualityScore(obs)
	if score < 0.9 {
		t.Errorf("expected quality > 0.9 for perfect signals, got %g", score)
	}
}

func TestQualityScore_BadSignals(t *testing.T) {
	obs := ObservationResult{
		OFI:               0,
		LiquidityVelocity: 0,
		TradeEntropy:       0,
		BotBuyCount:        5,
		OFIAcceleration:    -1.0,
	}
	score := QualityScore(obs)
	if score > 0.1 {
		t.Errorf("expected quality < 0.1 for bad signals, got %g", score)
	}
}

func TestQualityScore_Range(t *testing.T) {
	// Quality score should always be in [0, 1].
	testCases := []ObservationResult{
		{},
		{OFI: -1, LiquidityVelocity: -0.5, TradeEntropy: -1, BotBuyCount: 100, OFIAcceleration: -5},
		{OFI: 2, LiquidityVelocity: 10, TradeEntropy: 10, BotBuyCount: 0, OFIAcceleration: 5},
	}
	for i, tc := range testCases {
		score := QualityScore(tc)
		if score < 0 || score > 1 {
			t.Errorf("case %d: quality score %g out of [0, 1] range", i, score)
		}
	}
}
