package flow

import (
	"math"
	"testing"
	"time"
)

// recordTradeAt is a test helper that records a trade with a specific timestamp.
func recordTradeAt(o *Observer, txType string, solAmount, mcapSol float64, ts time.Time) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.trades = append(o.trades, trade{
		txType:    txType,
		solAmount: solAmount,
		mcapSol:   mcapSol,
		ts:        ts,
	})
}

func TestOFI_AllBuys(t *testing.T) {
	o := NewObserver()
	o.RecordTrade("buy", 1.0, 100)
	o.RecordTrade("buy", 2.0, 110)
	o.RecordTrade("buy", 0.5, 120)

	res := o.Result()

	if res.OFI != 1.0 {
		t.Errorf("expected OFI = 1.0 for all buys, got %f", res.OFI)
	}
	if res.BuyCount != 3 {
		t.Errorf("expected BuyCount = 3, got %d", res.BuyCount)
	}
	if res.SellCount != 0 {
		t.Errorf("expected SellCount = 0, got %d", res.SellCount)
	}
	assertClose(t, "BuyVolSOL", res.BuyVolSOL, 3.5)
	assertClose(t, "SellVolSOL", res.SellVolSOL, 0)
}

func TestOFI_AllSells(t *testing.T) {
	o := NewObserver()
	o.RecordTrade("sell", 1.0, 100)
	o.RecordTrade("sell", 2.0, 90)

	res := o.Result()

	if res.OFI != -1.0 {
		t.Errorf("expected OFI = -1.0 for all sells, got %f", res.OFI)
	}
}

func TestOFI_Mixed(t *testing.T) {
	o := NewObserver()
	o.RecordTrade("buy", 3.0, 100)
	o.RecordTrade("sell", 1.0, 95)

	res := o.Result()

	// OFI = (3 - 1) / (3 + 1) = 0.5
	assertClose(t, "OFI", res.OFI, 0.5)
}

func TestGrowthRate(t *testing.T) {
	o := NewObserver()
	o.RecordTrade("buy", 1.0, 100)
	o.RecordTrade("buy", 1.0, 150)

	res := o.Result()

	// GrowthRate = (150 - 100) / 100 = 0.5
	assertClose(t, "GrowthRate", res.GrowthRate, 0.5)
	assertClose(t, "StartMcapSOL", res.StartMcapSOL, 100)
	assertClose(t, "EndMcapSOL", res.EndMcapSOL, 150)
}

func TestGrowthRate_ZeroStart(t *testing.T) {
	o := NewObserver()
	o.RecordTrade("buy", 1.0, 0)
	o.RecordTrade("buy", 1.0, 50)

	res := o.Result()

	if res.GrowthRate != 0 {
		t.Errorf("expected GrowthRate = 0 when start mcap is 0, got %f", res.GrowthRate)
	}
}

func TestBotDetection_RegularIntervals(t *testing.T) {
	o := NewObserver()
	base := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)

	// Perfectly regular 1-second intervals: CV = 0.
	for i := 0; i < 5; i++ {
		recordTradeAt(o, "buy", 1.0, 100, base.Add(time.Duration(i)*time.Second))
	}

	res := o.Result()

	if res.TimingCV >= 0.3 {
		t.Errorf("expected TimingCV < 0.3 for regular intervals, got %f", res.TimingCV)
	}
	if !res.IsBotLike {
		t.Error("expected IsBotLike = true for regular intervals")
	}
}

func TestBotDetection_IrregularIntervals(t *testing.T) {
	o := NewObserver()
	base := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)

	// Irregular intervals: 100ms, 2s, 50ms, 5s, 200ms.
	offsets := []time.Duration{
		0,
		100 * time.Millisecond,
		2100 * time.Millisecond,
		2150 * time.Millisecond,
		7150 * time.Millisecond,
		7350 * time.Millisecond,
	}
	for _, off := range offsets {
		recordTradeAt(o, "buy", 1.0, 100, base.Add(off))
	}

	res := o.Result()

	if res.TimingCV < 0.3 {
		t.Errorf("expected TimingCV >= 0.3 for irregular intervals, got %f", res.TimingCV)
	}
	if res.IsBotLike {
		t.Error("expected IsBotLike = false for irregular intervals")
	}
}

func TestNoTrades(t *testing.T) {
	o := NewObserver()
	res := o.Result()

	if res.TradeCount != 0 {
		t.Errorf("expected TradeCount = 0, got %d", res.TradeCount)
	}
	if res.OFI != 0 {
		t.Errorf("expected OFI = 0, got %f", res.OFI)
	}
	if res.GrowthRate != 0 {
		t.Errorf("expected GrowthRate = 0, got %f", res.GrowthRate)
	}
	if res.TimingCV != 0 {
		t.Errorf("expected TimingCV = 0, got %f", res.TimingCV)
	}
	if res.IsBotLike {
		t.Error("expected IsBotLike = false for no trades")
	}
}

func TestSingleTrade(t *testing.T) {
	o := NewObserver()
	o.RecordTrade("buy", 1.0, 100)

	res := o.Result()

	if res.TradeCount != 1 {
		t.Errorf("expected TradeCount = 1, got %d", res.TradeCount)
	}
	if res.TimingCV != 0 {
		t.Errorf("expected TimingCV = 0 for single trade, got %f", res.TimingCV)
	}
	if res.IsBotLike {
		t.Error("expected IsBotLike = false for single trade")
	}
	if res.GrowthRate != 0 {
		t.Errorf("expected GrowthRate = 0 for single trade (start == end), got %f", res.GrowthRate)
	}
}

func TestLiquidityVelocity(t *testing.T) {
	o := NewObserver()
	o.RecordTrade("buy", 2.0, 100)
	o.RecordTrade("buy", 3.0, 110)
	o.RecordTrade("sell", 1.0, 105)

	res := o.Result()

	// netSOL = 2 + 3 - 1 = 4, trades = 3, velocity = 4/3 ≈ 1.333
	expected := 4.0 / 3.0
	if math.Abs(res.LiquidityVelocity-expected) > 0.01 {
		t.Errorf("LiquidityVelocity = %g, want ~%g", res.LiquidityVelocity, expected)
	}
}

func TestOFIAcceleration(t *testing.T) {
	o := NewObserver()
	base := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)

	// First half: balanced (OFI ≈ 0)
	recordTradeAt(o, "buy", 1.0, 100, base)
	recordTradeAt(o, "sell", 1.0, 98, base.Add(1*time.Second))
	// Second half: strong buys (OFI ≈ 1.0)
	recordTradeAt(o, "buy", 2.0, 105, base.Add(2*time.Second))
	recordTradeAt(o, "buy", 1.0, 110, base.Add(3*time.Second))

	res := o.Result()

	// Acceleration should be positive (buying pressure increasing).
	if res.OFIAcceleration <= 0 {
		t.Errorf("OFIAcceleration should be positive, got %g", res.OFIAcceleration)
	}
}

func TestTradeEntropy(t *testing.T) {
	o := NewObserver()
	// All same size → low entropy.
	for i := 0; i < 10; i++ {
		o.RecordTrade("buy", 0.3, 100)
	}
	res := o.Result()
	if res.TradeEntropy > 0.1 {
		t.Errorf("same-size trades should have low entropy, got %g", res.TradeEntropy)
	}
}

func TestTradeEntropyDiverse(t *testing.T) {
	o := NewObserver()
	// Diverse sizes → higher entropy.
	o.RecordTrade("buy", 0.005, 100)
	o.RecordTrade("buy", 0.05, 102)
	o.RecordTrade("buy", 0.3, 105)
	o.RecordTrade("buy", 0.7, 108)
	o.RecordTrade("buy", 2.0, 115)
	o.RecordTrade("buy", 7.0, 130)

	res := o.Result()
	if res.TradeEntropy < 1.5 {
		t.Errorf("diverse trades should have high entropy, got %g", res.TradeEntropy)
	}
}

func TestMaxTradeSize(t *testing.T) {
	o := NewObserver()
	o.RecordTrade("buy", 0.5, 100)
	o.RecordTrade("buy", 3.0, 110)
	o.RecordTrade("buy", 1.0, 115)
	o.RecordTrade("sell", 5.0, 105) // sells don't count

	res := o.Result()
	if res.MaxTradeSize != 3.0 {
		t.Errorf("MaxTradeSize = %g, want 3.0", res.MaxTradeSize)
	}
}

func TestCurveProgress(t *testing.T) {
	o := NewObserver()
	o.RecordTrade("buy", 1.0, 50) // mcapSOL = 50

	res := o.Result()
	if res.CurveProgress <= 0 {
		t.Error("CurveProgress should be positive for mcap > 0")
	}
	if res.CurveProgress >= 1.0 {
		t.Error("CurveProgress should be < 1.0 for low mcap")
	}
}

func assertClose(t *testing.T, name string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("%s: expected %f, got %f", name, want, got)
	}
}
