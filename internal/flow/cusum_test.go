package flow

import (
	"testing"
	"time"
)

func TestCUSUM_NoShift(t *testing.T) {
	c := NewCUSUM(5.0, 0.5, 3.0)
	// Feed observations around the target — no shift expected.
	for _, v := range []float64{5.0, 5.1, 4.9, 5.0, 5.2, 4.8} {
		up, down := c.Update(v)
		if up || down {
			t.Errorf("unexpected shift detected at value %g", v)
		}
	}
}

func TestCUSUM_UpwardShift(t *testing.T) {
	c := NewCUSUM(0, 0.5, 2.0) // target=0, detect positive shifts
	// Sustained positive values should trigger upward shift.
	triggered := false
	for i := 0; i < 10; i++ {
		up, _ := c.Update(1.0)
		if up {
			triggered = true
			break
		}
	}
	if !triggered {
		t.Error("expected upward shift to be detected")
	}
}

func TestCUSUM_DownwardShift(t *testing.T) {
	c := NewCUSUM(5.0, 0.5, 2.0) // target=5
	// Sustained low values should trigger downward shift.
	triggered := false
	for i := 0; i < 10; i++ {
		_, down := c.Update(2.0)
		if down {
			triggered = true
			break
		}
	}
	if !triggered {
		t.Error("expected downward shift to be detected")
	}
}

func TestCUSUM_Reset(t *testing.T) {
	c := NewCUSUM(0, 0.5, 2.0)
	c.Update(3.0)
	c.Update(3.0)
	c.Reset()
	// After reset, cumulative sums should be zero.
	up, down := c.Update(0.1) // small value, shouldn't trigger
	if up || down {
		t.Error("expected no shift immediately after reset")
	}
}

func TestTokenAnomalyDetector_SellOnset(t *testing.T) {
	d := NewTokenAnomalyDetector()
	base := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)

	// First window: only buys.
	for i := 0; i < 5; i++ {
		d.Feed("buy", 0.5, base.Add(time.Duration(i)*100*time.Millisecond))
	}

	// Second window: sells appear.
	sellOnsetDetected := false
	for s := 1; s <= 5; s++ {
		ts := base.Add(time.Duration(s) * time.Second)
		for i := 0; i < 3; i++ {
			signal := d.Feed("sell", 1.0, ts.Add(time.Duration(i)*100*time.Millisecond))
			if signal.SellOnset {
				sellOnsetDetected = true
			}
		}
	}

	if !sellOnsetDetected {
		t.Error("expected sell onset to be detected")
	}
}

func TestTokenAnomalyDetector_NormalTrading(t *testing.T) {
	d := NewTokenAnomalyDetector()
	base := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)

	// Steady buying with no sells — no anomalies expected.
	for s := 0; s < 5; s++ {
		ts := base.Add(time.Duration(s) * time.Second)
		for i := 0; i < 3; i++ {
			signal := d.Feed("buy", 0.5, ts.Add(time.Duration(i)*200*time.Millisecond))
			if signal.SellOnset {
				t.Error("unexpected sell onset during normal buying")
			}
		}
	}
}

func TestAnomalySignal_Any(t *testing.T) {
	if (AnomalySignal{}).Any() {
		t.Error("empty signal should not be Any()")
	}
	if !(AnomalySignal{SellOnset: true}).Any() {
		t.Error("SellOnset should be Any()")
	}
	if !(AnomalySignal{BuyCollapse: true}).Any() {
		t.Error("BuyCollapse should be Any()")
	}
	if !(AnomalySignal{SizeShift: true}).Any() {
		t.Error("SizeShift should be Any()")
	}
}
