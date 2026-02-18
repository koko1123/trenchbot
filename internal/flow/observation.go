package flow

import (
	"math"
	"sync"
	"time"

	"github.com/cindocode/trenchbot/internal/curve"
)

type trade struct {
	txType    string
	solAmount float64
	mcapSol   float64
	ts        time.Time
}

// Observer collects trade events during a pre-buy observation window
// and computes aggregate signals for token quality assessment.
type Observer struct {
	mu       sync.Mutex
	trades   []trade
	anomaly  *TokenAnomalyDetector // CUSUM anomaly detector fed in real time
	anomalySig AnomalySignal       // accumulated anomaly signals
}

// ObservationResult holds the computed signals from an observation window.
type ObservationResult struct {
	TradeCount       int
	BuyCount         int
	SellCount        int
	BuyVolSOL        float64
	SellVolSOL       float64
	OFI              float64 // order flow imbalance, -1.0 to +1.0
	StartMcapSOL     float64
	EndMcapSOL       float64
	GrowthRate       float64 // (end - start) / start
	TimingCV         float64 // coefficient of variation of inter-trade intervals
	IsBotLike        bool    // true if TimingCV < 0.3
	BotBuyCount      int     // buys within first 2 seconds of token creation
	RoundAmountCount int     // buys of exactly 0.1, 0.5, 1.0 SOL (±0.001 tolerance)
	BotSniped        bool    // true if BotBuyCount > 2
	SuggestDelay     bool    // true if bot activity detected and delayed entry is recommended

	// Quantitative signals (Phase: Quant Selection Engine).
	LiquidityVelocity float64 // net SOL per trade — strongest graduation predictor
	OFIAcceleration   float64 // second-half OFI minus first-half OFI
	TradeEntropy      float64 // Shannon entropy of trade size distribution (log-scale buckets)
	CurveProgress     float64 // 0.0-1.0 bonding curve graduation progress
	MaxTradeSize      float64 // largest single buy in SOL

	// CUSUM anomaly detection (Phase 3).
	AnomalyDetected bool          // true if any anomaly fired during observation
	AnomalySignal   AnomalySignal // which specific anomalies were detected
}

func NewObserver() *Observer {
	return &Observer{
		anomaly: NewTokenAnomalyDetector(),
	}
}

// RecordTrade records a single trade event with its timestamp.
func (o *Observer) RecordTrade(txType string, solAmount float64, mcapSol float64) {
	o.mu.Lock()
	defer o.mu.Unlock()
	now := time.Now()
	o.trades = append(o.trades, trade{
		txType:    txType,
		solAmount: solAmount,
		mcapSol:   mcapSol,
		ts:        now,
	})

	// Feed into CUSUM anomaly detector in real time.
	if o.anomaly != nil {
		sig := o.anomaly.Feed(txType, solAmount, now)
		if sig.SellOnset {
			o.anomalySig.SellOnset = true
		}
		if sig.BuyCollapse {
			o.anomalySig.BuyCollapse = true
		}
		if sig.SizeShift {
			o.anomalySig.SizeShift = true
		}
	}
}

// Result computes all derived metrics from the raw trade data.
func (o *Observer) Result() ObservationResult {
	o.mu.Lock()
	defer o.mu.Unlock()

	n := len(o.trades)
	if n == 0 {
		return ObservationResult{}
	}

	var res ObservationResult
	res.TradeCount = n
	res.StartMcapSOL = o.trades[0].mcapSol
	res.EndMcapSOL = o.trades[n-1].mcapSol

	for _, t := range o.trades {
		switch t.txType {
		case "buy":
			res.BuyCount++
			res.BuyVolSOL += t.solAmount
		case "sell":
			res.SellCount++
			res.SellVolSOL += t.solAmount
		}
	}

	// Order flow imbalance.
	totalVol := res.BuyVolSOL + res.SellVolSOL
	if totalVol > 0 {
		res.OFI = (res.BuyVolSOL - res.SellVolSOL) / totalVol
	}

	// Price velocity.
	if res.StartMcapSOL > 0 {
		res.GrowthRate = (res.EndMcapSOL - res.StartMcapSOL) / res.StartMcapSOL
	}

	// Timing analysis: coefficient of variation of inter-trade intervals.
	if n >= 2 {
		res.TimingCV = interTradeCV(o.trades)
		res.IsBotLike = res.TimingCV < 0.3
	}

	// Bot fingerprinting: count buys within first 2 seconds of the first trade.
	firstTS := o.trades[0].ts
	for _, t := range o.trades {
		if t.txType == "buy" && t.ts.Sub(firstTS) <= 2*time.Second {
			res.BotBuyCount++
		}
	}
	res.BotSniped = res.BotBuyCount > 2

	// Round amount detection: count buy trades with amounts near common round numbers.
	roundAmounts := []float64{0.1, 0.25, 0.5, 1.0, 2.0, 5.0}
	for _, t := range o.trades {
		if t.txType == "buy" {
			for _, ra := range roundAmounts {
				if math.Abs(t.solAmount-ra) <= 0.001 {
					res.RoundAmountCount++
					break
				}
			}
		}
	}

	// Bot dump delay suggestion: recommend delayed entry when bot activity is detected.
	// Bots typically dump within 30-120s of buying, so delaying entry lets us buy post-dump.
	res.SuggestDelay = res.BotSniped || (res.BotBuyCount >= 2 && res.RoundAmountCount >= 2)

	// Liquidity velocity: net SOL per trade. Academic research shows this is the
	// single strongest predictor of token graduation on PumpFun.
	if res.TradeCount > 0 {
		res.LiquidityVelocity = (res.BuyVolSOL - res.SellVolSOL) / float64(res.TradeCount)
	}

	// OFI acceleration: compare first-half vs second-half OFI.
	// Positive = buying pressure increasing; negative = fading.
	res.OFIAcceleration = ofiAcceleration(o.trades)

	// Trade entropy: Shannon entropy of trade size distribution.
	// Low = coordinated (all same size), high = diverse organic buyers.
	res.TradeEntropy = tradeEntropy(o.trades)

	// Bonding curve progress from final mcap observation.
	if res.EndMcapSOL > 0 {
		res.CurveProgress = curve.Progress(res.EndMcapSOL)
	}

	// Max single buy.
	for _, t := range o.trades {
		if t.txType == "buy" && t.solAmount > res.MaxTradeSize {
			res.MaxTradeSize = t.solAmount
		}
	}

	// CUSUM anomaly detection results.
	res.AnomalySignal = o.anomalySig
	res.AnomalyDetected = o.anomalySig.Any()

	return res
}

// ofiAcceleration computes the difference between second-half and first-half OFI.
func ofiAcceleration(trades []trade) float64 {
	n := len(trades)
	if n < 4 {
		return 0
	}
	mid := n / 2

	computeOFI := func(subset []trade) float64 {
		var buyVol, sellVol float64
		for _, t := range subset {
			if t.txType == "buy" {
				buyVol += t.solAmount
			} else if t.txType == "sell" {
				sellVol += t.solAmount
			}
		}
		total := buyVol + sellVol
		if total <= 0 {
			return 0
		}
		return (buyVol - sellVol) / total
	}

	firstHalf := computeOFI(trades[:mid])
	secondHalf := computeOFI(trades[mid:])
	return secondHalf - firstHalf
}

// tradeEntropy computes the Shannon entropy of trade sizes using log-scale buckets.
func tradeEntropy(trades []trade) float64 {
	if len(trades) < 2 {
		return 0
	}

	// Discretize into log-scale buckets: <0.01, 0.01-0.1, 0.1-0.5, 0.5-1, 1-5, >5 SOL.
	buckets := make(map[int]int)
	total := 0
	for _, t := range trades {
		if t.txType != "buy" {
			continue
		}
		var bucket int
		switch {
		case t.solAmount < 0.01:
			bucket = 0
		case t.solAmount < 0.1:
			bucket = 1
		case t.solAmount < 0.5:
			bucket = 2
		case t.solAmount < 1.0:
			bucket = 3
		case t.solAmount < 5.0:
			bucket = 4
		default:
			bucket = 5
		}
		buckets[bucket]++
		total++
	}

	if total == 0 {
		return 0
	}

	entropy := 0.0
	for _, count := range buckets {
		p := float64(count) / float64(total)
		if p > 0 {
			entropy -= p * math.Log2(p)
		}
	}
	return entropy
}

// interTradeCV computes the coefficient of variation (stddev / mean) of the
// time intervals between consecutive trades.
func interTradeCV(trades []trade) float64 {
	n := len(trades)
	if n < 2 {
		return 0
	}

	intervals := make([]float64, n-1)
	for i := 1; i < n; i++ {
		intervals[i-1] = trades[i].ts.Sub(trades[i-1].ts).Seconds()
	}

	// Mean.
	var sum float64
	for _, v := range intervals {
		sum += v
	}
	mean := sum / float64(len(intervals))
	if mean == 0 {
		return 0
	}

	// Standard deviation.
	var sqDiffSum float64
	for _, v := range intervals {
		diff := v - mean
		sqDiffSum += diff * diff
	}
	stddev := math.Sqrt(sqDiffSum / float64(len(intervals)))

	return stddev / mean
}
