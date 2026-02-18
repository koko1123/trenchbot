package curve

import (
	"math"
	"testing"
)

func TestSpotPrice(t *testing.T) {
	price := SpotPrice(InitVirtualSOL, InitVirtualTokens)
	expected := InitVirtualSOL / InitVirtualTokens
	if math.Abs(price-expected) > 1e-15 {
		t.Errorf("initial spot price = %g, want %g", price, expected)
	}
}

func TestSpotPriceZeroTokens(t *testing.T) {
	if SpotPrice(30, 0) != 0 {
		t.Error("expected 0 for zero tokens")
	}
}

func TestPriceImpact(t *testing.T) {
	// 1 SOL buy into 30 SOL reserves = 3.33% impact.
	impact := PriceImpact(1.0, 30.0)
	expected := 1.0 / 30.0
	if math.Abs(impact-expected) > 1e-10 {
		t.Errorf("impact = %g, want %g", impact, expected)
	}
}

func TestPriceImpactScalesDown(t *testing.T) {
	// Same buy at higher reserves = lower impact.
	impactLow := PriceImpact(0.5, 30)
	impactHigh := PriceImpact(0.5, 100)
	if impactHigh >= impactLow {
		t.Errorf("higher reserves should give lower impact: %g >= %g", impactHigh, impactLow)
	}
}

func TestTokensReceived(t *testing.T) {
	// Buy 1 SOL at initial reserves.
	tokens := TokensReceived(1.0, InitVirtualTokens, InitVirtualSOL)
	// Formula: vT * buySOL / (vSOL + buySOL)
	expected := InitVirtualTokens * 1.0 / (InitVirtualSOL + 1.0)
	if math.Abs(tokens-expected) > 1 {
		t.Errorf("tokens = %g, want %g", tokens, expected)
	}
}

func TestSOLReceived(t *testing.T) {
	// Sell some tokens back at initial reserves.
	tokens := 10_000_000.0
	sol := SOLReceived(tokens, InitVirtualTokens, InitVirtualSOL)
	if sol <= 0 || sol >= InitVirtualSOL {
		t.Errorf("SOL received = %g, should be positive and less than reserves", sol)
	}
}

func TestConstantProductInvariant(t *testing.T) {
	// After a buy, vSOL * vTokens should still equal K.
	buySOL := 5.0
	newVSOL := InitVirtualSOL + buySOL
	tokensOut := TokensReceived(buySOL, InitVirtualTokens, InitVirtualSOL)
	newVTokens := InitVirtualTokens - tokensOut

	product := newVSOL * newVTokens
	if math.Abs(product-K) > 1 { // allow rounding
		t.Errorf("invariant broken: %g * %g = %g, want %g", newVSOL, newVTokens, product, K)
	}
}

func TestProgress(t *testing.T) {
	tests := []struct {
		name     string
		mcapSOL  float64
		wantMin  float64
		wantMax  float64
	}{
		{"zero mcap", 0, 0, 0},
		{"very early", 1, 0, 0.15},
		{"mid curve", 50, 0.05, 0.3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := Progress(tt.mcapSOL)
			if p < tt.wantMin || p > tt.wantMax {
				t.Errorf("Progress(%g) = %g, want [%g, %g]", tt.mcapSOL, p, tt.wantMin, tt.wantMax)
			}
		})
	}
}

func TestProgressClamped(t *testing.T) {
	// Very high mcap should not exceed 1.0.
	p := Progress(100000)
	if p > 1.0 {
		t.Errorf("progress should be capped at 1.0, got %g", p)
	}
}

func TestEstimateVirtualSOL(t *testing.T) {
	// At zero mcap, should return initial reserves.
	vs := EstimateVirtualSOL(0)
	if vs != InitVirtualSOL {
		t.Errorf("at zero mcap, vSOL = %g, want %g", vs, InitVirtualSOL)
	}

	// At positive mcap, should be above initial.
	vs2 := EstimateVirtualSOL(50)
	if vs2 <= InitVirtualSOL {
		t.Errorf("at mcap=50, vSOL should be > initial: %g", vs2)
	}
}

func TestMaxDumpImpact(t *testing.T) {
	// Top holder with 20% at mcap 30.
	impact := MaxDumpImpact(20, 30)
	if impact <= 0 {
		t.Error("expected positive dump impact")
	}
	if impact >= 1 {
		t.Errorf("dump impact unreasonably high: %g", impact)
	}
}

func TestMaxDumpImpactZero(t *testing.T) {
	if MaxDumpImpact(0, 30) != 0 {
		t.Error("zero holder % should give zero impact")
	}
	if MaxDumpImpact(20, 0) != 0 {
		t.Error("zero mcap should give zero impact")
	}
}

func TestOptimalSlippage(t *testing.T) {
	tests := []struct {
		name    string
		buySOL  float64
		mcapSOL float64
		wantMin int
		wantMax int
	}{
		{"small buy early curve", 0.3, 5, 3, 30},
		{"small buy mid curve", 0.3, 50, 3, 10},
		{"large buy early", 2.0, 5, 5, 49},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := OptimalSlippage(tt.buySOL, tt.mcapSOL)
			if s < tt.wantMin || s > tt.wantMax {
				t.Errorf("OptimalSlippage(%g, %g) = %d, want [%d, %d]", tt.buySOL, tt.mcapSOL, s, tt.wantMin, tt.wantMax)
			}
		})
	}
}

func TestOptimalSlippageBounds(t *testing.T) {
	// Should never go below 3 or above 49.
	s := OptimalSlippage(0.001, 1000)
	if s < 3 {
		t.Errorf("slippage below floor: %d", s)
	}
	s = OptimalSlippage(100, 1)
	if s > 49 {
		t.Errorf("slippage above cap: %d", s)
	}
}

func TestMaxEntrySOL(t *testing.T) {
	// At genesis (vSOL=30), 2% impact → max 0.6 SOL.
	got := MaxEntrySOL(5.0, 2.0)
	if math.Abs(got-0.6) > 0.01 {
		t.Errorf("genesis 2%%: got %g, want ~0.6", got)
	}

	// At mcap 50 (vSOL≈40.1), 2% impact → max ~0.802 SOL.
	got = MaxEntrySOL(50.0, 2.0)
	if got < 0.7 || got > 0.9 {
		t.Errorf("mcap50 2%%: got %g, want ~0.8", got)
	}

	// Deeper pool allows more.
	small := MaxEntrySOL(10.0, 2.0)
	large := MaxEntrySOL(200.0, 2.0)
	if large <= small {
		t.Errorf("deeper pool should allow more: small=%g large=%g", small, large)
	}

	// Zero mcap → vSOL=30 → max = 30 * 2/100 = 0.6
	got = MaxEntrySOL(0, 2.0)
	if math.Abs(got-0.6) > 0.01 {
		t.Errorf("zero mcap: got %g, want ~0.6", got)
	}

	// Zero impact → 0
	got = MaxEntrySOL(50.0, 0)
	if got != 0 {
		t.Errorf("zero impact: got %g, want 0", got)
	}
}
