package flow

import (
	"sync"
	"time"
)

// CUSUMDetector implements a cumulative sum control chart for detecting
// persistent shifts in a process mean. It is more sensitive to small,
// sustained shifts than simple threshold checks.
type CUSUMDetector struct {
	target    float64 // expected mean of the process
	allowance float64 // slack parameter (k) — filters noise
	threshold float64 // decision boundary (h)
	cPlus     float64 // upper cumulative sum (detects upward shift)
	cMinus    float64 // lower cumulative sum (detects downward shift)
}

// NewCUSUM creates a detector with the given parameters.
// target: expected process mean. allowance: typically 0.5 * shift_to_detect.
// threshold: decision boundary, higher = fewer false alarms.
func NewCUSUM(target, allowance, threshold float64) CUSUMDetector {
	return CUSUMDetector{
		target:    target,
		allowance: allowance,
		threshold: threshold,
	}
}

// Update feeds a new observation and returns whether an upward or downward
// shift has been detected.
func (c *CUSUMDetector) Update(observation float64) (shiftUp, shiftDown bool) {
	c.cPlus = max(0, c.cPlus+observation-c.target-c.allowance)
	c.cMinus = max(0, c.cMinus-observation+c.target-c.allowance)
	return c.cPlus > c.threshold, c.cMinus > c.threshold
}

// Reset clears the cumulative sums.
func (c *CUSUMDetector) Reset() {
	c.cPlus = 0
	c.cMinus = 0
}

// AnomalySignal describes which anomalies were detected.
type AnomalySignal struct {
	SellOnset   bool // sudden onset of selling activity
	BuyCollapse bool // buy rate dropped from initial burst
	SizeShift   bool // trade sizes shifted regime
}

// Any returns true if any anomaly was detected.
func (a AnomalySignal) Any() bool {
	return a.SellOnset || a.BuyCollapse || a.SizeShift
}

// TokenAnomalyDetector runs multiple CUSUM detectors on a token's trade stream
// to detect rug-pull setup patterns in real time.
type TokenAnomalyDetector struct {
	mu sync.Mutex

	sellOnset   CUSUMDetector // detect sudden sell rate change
	buyCollapse CUSUMDetector // detect buy rate drop
	sizeShift   CUSUMDetector // detect trade size regime change

	// Running state for windowed rate computation.
	windowStart  time.Time
	windowBuys   int
	windowSells  int
	windowBuyVol float64
	lastWindow   time.Time

	// Baseline from first few seconds.
	baselineBuyRate float64
	baselineSet     bool
	tradeCount      int
}

// NewTokenAnomalyDetector creates a detector with PumpFun-calibrated defaults.
func NewTokenAnomalyDetector() *TokenAnomalyDetector {
	return &TokenAnomalyDetector{
		// SellOnset: expect 0 sells initially, detect any persistent selling.
		sellOnset: NewCUSUM(0, 0.5, 2.0),
		// BuyCollapse: baseline set dynamically after first window.
		buyCollapse: NewCUSUM(0, 0.3, 3.0),
		// SizeShift: detect when average trade size changes regime.
		sizeShift: NewCUSUM(0, 0.2, 2.5),
	}
}

// Feed processes a single trade and returns any detected anomalies.
func (d *TokenAnomalyDetector) Feed(txType string, solAmount float64, ts time.Time) AnomalySignal {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.tradeCount++

	// Initialize window.
	if d.windowStart.IsZero() {
		d.windowStart = ts
		d.lastWindow = ts
	}

	// Accumulate in 1-second windows.
	if txType == "buy" {
		d.windowBuys++
		d.windowBuyVol += solAmount
	} else if txType == "sell" {
		d.windowSells++
	}

	// Process completed windows (every 1 second).
	elapsed := ts.Sub(d.windowStart)
	if elapsed < time.Second {
		return AnomalySignal{}
	}

	// Compute rates for this window.
	sellRate := float64(d.windowSells)
	buyRate := float64(d.windowBuys)
	avgSize := 0.0
	if d.windowBuys > 0 {
		avgSize = d.windowBuyVol / float64(d.windowBuys)
	}

	// Set baseline from first window.
	if !d.baselineSet && buyRate > 0 {
		d.baselineBuyRate = buyRate
		d.buyCollapse.target = buyRate
		d.sizeShift.target = avgSize
		d.baselineSet = true
	}

	var signal AnomalySignal

	// Sell onset: any persistent selling is unusual early in a token's life.
	up, _ := d.sellOnset.Update(sellRate)
	signal.SellOnset = up

	// Buy collapse: detect when buy rate drops below baseline.
	if d.baselineSet {
		_, down := d.buyCollapse.Update(buyRate)
		signal.BuyCollapse = down

		// Size shift: detect when avg trade size changes.
		if avgSize > 0 {
			up, _ := d.sizeShift.Update(avgSize)
			signal.SizeShift = up
		}
	}

	// Reset window for next second.
	d.windowStart = ts
	d.windowBuys = 0
	d.windowSells = 0
	d.windowBuyVol = 0

	return signal
}

func max(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
