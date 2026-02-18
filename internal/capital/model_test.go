package capital

import (
	"math"
	"testing"
)

func defaultInputs() ModelInputs {
	return ModelInputs{
		BaseSnipeSOL:     0.3,
		MaxPositions:     15,
		MaxSnipesPerHour: 10,
		MaxImpactPct:     2.0,
		StopLossPct:      30,
		Tranche1X:        1.5,
		GasCostPerTx:     0.000505,
		SweepReserveSOL:  10,
		FilterPassRate:   0.002,
		AvgHoldMinutes:   15,
		WinRate:          0.55,
		AvgWinPct:        30,
		AvgLossPct:       25,
		TokensPerDay:     10_000,
	}
}

func TestComputeModel_Throughput(t *testing.T) {
	out := ComputeModel(defaultInputs())

	// 15 positions × 60/15 min = 60 turnover/hour, capped at 10 snipes/hour.
	if out.MaxBuysPerHour != 10 {
		t.Errorf("expected MaxBuysPerHour=10, got %.0f", out.MaxBuysPerHour)
	}
	if out.MaxBuysPerDay != 240 {
		t.Errorf("expected MaxBuysPerDay=240, got %.0f", out.MaxBuysPerDay)
	}

	// 10000 × 0.002 = 20, which is less than 240 max.
	if out.RealisticBuysPerDay != 20 {
		t.Errorf("expected RealisticBuysPerDay=20, got %.0f", out.RealisticBuysPerDay)
	}
}

func TestComputeModel_TradeSize(t *testing.T) {
	out := ComputeModel(defaultInputs())

	// Fresh token vSOL=30, 2% impact = 0.6 SOL cap. Base is 0.3, so 0.3 wins.
	if math.Abs(out.AvgTradeSize-0.3) > 0.001 {
		t.Errorf("expected AvgTradeSize=0.3, got %.4f", out.AvgTradeSize)
	}
}

func TestComputeModel_TradeSizeCappedByImpact(t *testing.T) {
	in := defaultInputs()
	in.BaseSnipeSOL = 1.0 // larger than impact cap
	out := ComputeModel(in)

	// 30 × 2% = 0.6 SOL cap.
	if math.Abs(out.AvgTradeSize-0.6) > 0.001 {
		t.Errorf("expected AvgTradeSize capped at 0.6, got %.4f", out.AvgTradeSize)
	}
}

func TestComputeModel_PositiveEV(t *testing.T) {
	out := ComputeModel(defaultInputs())

	// With 55% WR, +30% avg win, -25% avg loss: EV should be positive.
	if out.GrossEVPerTrade <= 0 {
		t.Errorf("expected positive gross EV, got %.4f", out.GrossEVPerTrade)
	}
	if out.NetEVPerTrade <= 0 {
		t.Errorf("expected positive net EV, got %.4f", out.NetEVPerTrade)
	}
	if out.DailyNetProfit <= 0 {
		t.Errorf("expected positive daily net profit, got %.4f", out.DailyNetProfit)
	}
}

func TestComputeModel_NegativeEdge(t *testing.T) {
	in := defaultInputs()
	in.WinRate = 0.3   // 30% win rate
	in.AvgWinPct = 20  // small wins
	in.AvgLossPct = 30 // large losses
	out := ComputeModel(in)

	if out.NetEVPerTrade >= 0 {
		t.Errorf("expected negative net EV with bad rates, got %.4f", out.NetEVPerTrade)
	}
	if out.RuinProbability < 50 {
		t.Errorf("expected high ruin probability with negative edge, got %.2f%%", out.RuinProbability)
	}
}

func TestComputeModel_ZeroBuys(t *testing.T) {
	in := defaultInputs()
	in.FilterPassRate = 0 // nothing passes
	out := ComputeModel(in)

	if out.RealisticBuysPerDay != 0 {
		t.Errorf("expected 0 buys/day, got %.0f", out.RealisticBuysPerDay)
	}
	if out.DailyNetProfit != 0 {
		t.Errorf("expected 0 daily profit, got %.4f", out.DailyNetProfit)
	}
}

func TestComputeModel_MonthlyProjection(t *testing.T) {
	out := ComputeModel(defaultInputs())

	expected := out.DailyNetProfit * 30
	if math.Abs(out.MonthlyNetProfit-expected) > 0.0001 {
		t.Errorf("monthly != daily×30: got %.4f, expected %.4f", out.MonthlyNetProfit, expected)
	}
}

func TestComputeModel_KellyPositive(t *testing.T) {
	out := ComputeModel(defaultInputs())

	// With positive edge, Kelly should be positive.
	if out.HalfKellyFraction <= 0 {
		t.Errorf("expected positive half-Kelly, got %.4f", out.HalfKellyFraction)
	}
}

func TestComputeModel_StringNotEmpty(t *testing.T) {
	out := ComputeModel(defaultInputs())
	s := out.String()
	if len(s) < 100 {
		t.Errorf("expected substantial output string, got %d chars", len(s))
	}
}

func TestComputeModel_RateLimitBottleneck(t *testing.T) {
	in := defaultInputs()
	in.FilterPassRate = 0.1 // 10% pass rate → 1000 candidates/day
	in.MaxSnipesPerHour = 5 // tight rate limit
	out := ComputeModel(in)

	// Should be capped by rate limit: 5/hr × 24 = 120.
	if out.RealisticBuysPerDay != 120 {
		t.Errorf("expected rate-limited at 120/day, got %.0f", out.RealisticBuysPerDay)
	}
}
