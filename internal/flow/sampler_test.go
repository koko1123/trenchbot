package flow

import (
	"testing"
)

func TestThompsonSampler_UnknownBucket(t *testing.T) {
	ts := NewThompsonSampler()
	score := ts.Score("unknown")
	if score < 0 || score > 1 {
		t.Errorf("expected score in [0, 1], got %g", score)
	}
}

func TestThompsonSampler_UpdateShiftsDistribution(t *testing.T) {
	ts := NewThompsonSampler()
	key := "ofi_hi:vel_hi:bot_no"

	// Record many wins.
	for i := 0; i < 50; i++ {
		ts.Update(key, true)
	}
	// Record a few losses.
	for i := 0; i < 5; i++ {
		ts.Update(key, false)
	}

	// Win rate should be high.
	wr := ts.WinRate(key)
	if wr < 0.7 {
		t.Errorf("expected win rate > 0.7 after 50W/5L, got %g", wr)
	}

	// Sampled scores should tend to be high (run multiple samples).
	highCount := 0
	for i := 0; i < 100; i++ {
		if ts.Score(key) > 0.5 {
			highCount++
		}
	}
	if highCount < 70 {
		t.Errorf("expected majority of samples > 0.5, got %d/100", highCount)
	}
}

func TestThompsonSampler_LosingBucket(t *testing.T) {
	ts := NewThompsonSampler()
	key := "ofi_lo:vel_lo:bot_yes"

	// Record many losses.
	for i := 0; i < 50; i++ {
		ts.Update(key, false)
	}
	for i := 0; i < 2; i++ {
		ts.Update(key, true)
	}

	wr := ts.WinRate(key)
	if wr > 0.15 {
		t.Errorf("expected win rate < 0.15 after 2W/50L, got %g", wr)
	}
}

func TestBucketKey_Discretization(t *testing.T) {
	tests := []struct {
		obs      ObservationResult
		expected string
	}{
		{
			obs:      ObservationResult{OFI: 0.1, LiquidityVelocity: 0.01, CurveProgress: 0.02},
			expected: "ofi_lo:vel_lo:curve_early",
		},
		{
			obs:      ObservationResult{OFI: 0.8, LiquidityVelocity: 0.5, CurveProgress: 0.03},
			expected: "ofi_toxic:vel_hi:curve_early",
		},
		{
			obs:      ObservationResult{OFI: 0.4, LiquidityVelocity: 0.2, CurveProgress: 0.08},
			expected: "ofi_sweet:vel_sweet:curve_mid",
		},
		{
			obs:      ObservationResult{OFI: 0.96, LiquidityVelocity: 0.25, CurveProgress: 0.01},
			expected: "ofi_hi:vel_sweet:curve_early",
		},
	}
	for _, tt := range tests {
		got := BucketKey(tt.obs, 70)
		if got != tt.expected {
			t.Errorf("BucketKey(%+v) = %q, want %q", tt.obs, got, tt.expected)
		}
	}
}

func TestBetaSample_InRange(t *testing.T) {
	for i := 0; i < 1000; i++ {
		s := betaSample(2, 3)
		if s < 0 || s > 1 {
			t.Fatalf("beta sample out of range: %g", s)
		}
	}
}
