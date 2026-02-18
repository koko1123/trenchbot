package report

import (
	"math"
	"sort"
	"time"
)

// Assessment is the structured post-run assessment analyzing trade outcomes vs entry signals.
type Assessment struct {
	RunDuration        time.Duration      `json:"run_duration"`
	TotalTrades        int                `json:"total_trades"`
	WinRate            float64            `json:"win_rate"`
	TotalPnLPct        float64            `json:"total_pnl_pct"`
	SignalCorrelations []SignalCorrelation `json:"signal_correlations"`
	ExitBreakdown      []ExitStats        `json:"exit_breakdown"`
	ScoreBands         []ScoreBandStats   `json:"score_bands"`
	AvgHoldWin         time.Duration      `json:"avg_hold_win"`
	AvgHoldLoss        time.Duration      `json:"avg_hold_loss"`
	HeatBuckets        [3]TradeStats      `json:"heat_buckets"` // low(<0.2), mid(0.2-0.6), high(>0.6)
	BotActivityStats   *BotImpactStats    `json:"bot_activity_stats,omitempty"`
	Recommendations    []Recommendation   `json:"recommendations"`
}

// SignalCorrelation captures how a signal correlates with win/loss outcomes.
type SignalCorrelation struct {
	Signal       string  `json:"signal"`
	AvgScoreWin  float64 `json:"avg_score_win"`
	AvgScoreLoss float64 `json:"avg_score_loss"`
	Predictive   bool    `json:"predictive"`
	Direction    string  `json:"direction"` // "higher_better" or "higher_worse"
}

// ExitStats captures per-exit-reason statistics.
type ExitStats struct {
	Reason    string  `json:"reason"`
	Count     int     `json:"count"`
	AvgPnLPct float64 `json:"avg_pnl_pct"`
	TotalPnL  float64 `json:"total_pnl"`
}

// ScoreBandStats captures per-score-band statistics.
type ScoreBandStats struct {
	MinScore int     `json:"min_score"`
	MaxScore int     `json:"max_score"`
	Trades   int     `json:"trades"`
	WinRate  float64 `json:"win_rate"`
	AvgPnL   float64 `json:"avg_pnl"`
	KellyF   float64 `json:"kelly_f"`
}

// TradeStats captures aggregated trade stats for a bucket.
type TradeStats struct {
	Label   string  `json:"label"`
	Trades  int     `json:"trades"`
	WinRate float64 `json:"win_rate"`
	AvgPnL  float64 `json:"avg_pnl"`
}

// BotImpactStats compares bot-sniped vs organic trades.
type BotImpactStats struct {
	BotSnipedTrades  int     `json:"bot_sniped_trades"`
	OrganicTrades    int     `json:"organic_trades"`
	BotSnipedWinRate float64 `json:"bot_sniped_win_rate"`
	OrganicWinRate   float64 `json:"organic_win_rate"`
	BotSnipedAvgPnL  float64 `json:"bot_sniped_avg_pnl"`
	OrganicAvgPnL    float64 `json:"organic_avg_pnl"`
}

// Recommendation suggests a parameter tuning.
type Recommendation struct {
	Parameter string `json:"parameter"`
	Current   string `json:"current"`
	Suggested string `json:"suggested"`
	Reason    string `json:"reason"`
}

// TradeRecord captures the data needed from a single trade for assessment.
type TradeRecord struct {
	PnLPct        float64
	Won           bool
	ExitReason    string
	FilterScore   int
	SignalScores  map[string]int
	OFI           float64
	ObsGrowthRate float64
	ObsTimingCV   float64
	HolderTopPct  float64
	EntryHeat     float64
	HoldDuration  time.Duration
	BotBuyCount   int // 0 if no bot activity detected

	// Quantitative signals.
	LiquidityVelocity float64
	OFIAcceleration   float64
	TradeEntropy      float64
	CurveProgress     float64
	MaxTradeSize      float64
}

// GenerateAssessment produces a structured assessment from a slice of trade records.
func GenerateAssessment(records []TradeRecord, runDuration time.Duration) *Assessment {
	a := &Assessment{
		RunDuration:     runDuration,
		TotalTrades:     len(records),
		Recommendations: []Recommendation{},
	}

	if len(records) == 0 {
		a.SignalCorrelations = []SignalCorrelation{}
		a.ExitBreakdown = []ExitStats{}
		a.ScoreBands = []ScoreBandStats{}
		a.HeatBuckets = [3]TradeStats{
			{Label: "low"},
			{Label: "mid"},
			{Label: "high"},
		}
		return a
	}

	// Split winners and losers.
	var winners, losers []TradeRecord
	for _, r := range records {
		if r.PnLPct > 0 {
			winners = append(winners, r)
		} else {
			losers = append(losers, r)
		}
		a.TotalPnLPct += r.PnLPct
	}

	if len(records) > 0 {
		a.WinRate = float64(len(winners)) / float64(len(records))
	}

	// Average hold durations.
	a.AvgHoldWin = avgDuration(winners)
	a.AvgHoldLoss = avgDuration(losers)

	// Signal correlations from SignalScores map.
	a.SignalCorrelations = computeIntSignalCorrelations(winners, losers)

	// Float signal correlations for OFI, ObsGrowthRate, ObsTimingCV, HolderTopPct.
	floatCorrelations := computeFloatSignalCorrelations(winners, losers)
	a.SignalCorrelations = append(a.SignalCorrelations, floatCorrelations...)

	// Exit breakdown.
	a.ExitBreakdown = computeExitBreakdown(records)

	// Score bands.
	a.ScoreBands = computeScoreBands(records)

	// Heat buckets.
	a.HeatBuckets = computeHeatBuckets(records)

	// Bot impact.
	a.BotActivityStats = computeBotImpact(records)

	// Recommendations.
	a.Recommendations = generateRecommendations(a)

	return a
}

func avgDuration(records []TradeRecord) time.Duration {
	if len(records) == 0 {
		return 0
	}
	var total time.Duration
	for _, r := range records {
		total += r.HoldDuration
	}
	return total / time.Duration(len(records))
}

func computeIntSignalCorrelations(winners, losers []TradeRecord) []SignalCorrelation {
	// Collect all signal names.
	signalSet := map[string]struct{}{}
	for _, r := range winners {
		for k := range r.SignalScores {
			signalSet[k] = struct{}{}
		}
	}
	for _, r := range losers {
		for k := range r.SignalScores {
			signalSet[k] = struct{}{}
		}
	}

	// Sort signal names for deterministic output.
	var signals []string
	for s := range signalSet {
		signals = append(signals, s)
	}
	sort.Strings(signals)

	var correlations []SignalCorrelation
	for _, sig := range signals {
		var winSum, lossSum float64
		var winCount, lossCount int
		var minVal, maxVal int
		first := true

		updateMinMax := func(v int) {
			if first {
				minVal = v
				maxVal = v
				first = false
			} else {
				if v < minVal {
					minVal = v
				}
				if v > maxVal {
					maxVal = v
				}
			}
		}

		for _, r := range winners {
			if v, ok := r.SignalScores[sig]; ok {
				winSum += float64(v)
				winCount++
				updateMinMax(v)
			}
		}
		for _, r := range losers {
			if v, ok := r.SignalScores[sig]; ok {
				lossSum += float64(v)
				lossCount++
				updateMinMax(v)
			}
		}

		avgWin := safeDiv(winSum, float64(winCount))
		avgLoss := safeDiv(lossSum, float64(lossCount))
		sigRange := float64(maxVal - minVal)

		diff := avgWin - avgLoss
		predictive := sigRange > 0 && math.Abs(diff) > 0.15*sigRange

		direction := "higher_better"
		if diff < 0 {
			direction = "higher_worse"
		}

		correlations = append(correlations, SignalCorrelation{
			Signal:       sig,
			AvgScoreWin:  avgWin,
			AvgScoreLoss: avgLoss,
			Predictive:   predictive,
			Direction:    direction,
		})
	}

	return correlations
}

func computeFloatSignalCorrelations(winners, losers []TradeRecord) []SignalCorrelation {
	type floatExtractor struct {
		name string
		get  func(r TradeRecord) float64
	}

	extractors := []floatExtractor{
		{"OFI", func(r TradeRecord) float64 { return r.OFI }},
		{"ObsGrowthRate", func(r TradeRecord) float64 { return r.ObsGrowthRate }},
		{"ObsTimingCV", func(r TradeRecord) float64 { return r.ObsTimingCV }},
		{"HolderTopPct", func(r TradeRecord) float64 { return r.HolderTopPct }},
		{"LiquidityVelocity", func(r TradeRecord) float64 { return r.LiquidityVelocity }},
		{"OFIAcceleration", func(r TradeRecord) float64 { return r.OFIAcceleration }},
		{"TradeEntropy", func(r TradeRecord) float64 { return r.TradeEntropy }},
		{"CurveProgress", func(r TradeRecord) float64 { return r.CurveProgress }},
		{"MaxTradeSize", func(r TradeRecord) float64 { return r.MaxTradeSize }},
	}

	var correlations []SignalCorrelation
	for _, ext := range extractors {
		var winSum, lossSum float64
		minVal := math.MaxFloat64
		maxVal := -math.MaxFloat64

		for _, r := range winners {
			v := ext.get(r)
			winSum += v
			if v < minVal {
				minVal = v
			}
			if v > maxVal {
				maxVal = v
			}
		}
		for _, r := range losers {
			v := ext.get(r)
			lossSum += v
			if v < minVal {
				minVal = v
			}
			if v > maxVal {
				maxVal = v
			}
		}

		// If no records exist at all, skip.
		if len(winners)+len(losers) == 0 {
			continue
		}

		avgWin := safeDiv(winSum, float64(len(winners)))
		avgLoss := safeDiv(lossSum, float64(len(losers)))

		sigRange := maxVal - minVal
		diff := avgWin - avgLoss
		predictive := sigRange > 0 && math.Abs(diff) > 0.15*sigRange

		direction := "higher_better"
		if diff < 0 {
			direction = "higher_worse"
		}

		correlations = append(correlations, SignalCorrelation{
			Signal:       ext.name,
			AvgScoreWin:  avgWin,
			AvgScoreLoss: avgLoss,
			Predictive:   predictive,
			Direction:    direction,
		})
	}

	return correlations
}

func computeExitBreakdown(records []TradeRecord) []ExitStats {
	type accumulator struct {
		count    int
		pnlSum  float64
	}

	byReason := map[string]*accumulator{}
	for _, r := range records {
		acc, ok := byReason[r.ExitReason]
		if !ok {
			acc = &accumulator{}
			byReason[r.ExitReason] = acc
		}
		acc.count++
		acc.pnlSum += r.PnLPct
	}

	var stats []ExitStats
	for reason, acc := range byReason {
		stats = append(stats, ExitStats{
			Reason:    reason,
			Count:     acc.count,
			AvgPnLPct: safeDiv(acc.pnlSum, float64(acc.count)),
			TotalPnL:  acc.pnlSum,
		})
	}

	sort.Slice(stats, func(i, j int) bool {
		return stats[i].Reason < stats[j].Reason
	})

	return stats
}

func computeScoreBands(records []TradeRecord) []ScoreBandStats {
	type band struct {
		min, max int
	}
	bands := []band{
		{0, 55},
		{55, 65},
		{65, 75},
		{75, 85},
		{85, 100},
	}

	result := make([]ScoreBandStats, len(bands))
	for i, b := range bands {
		result[i] = ScoreBandStats{MinScore: b.min, MaxScore: b.max}
	}

	for _, r := range records {
		for i, b := range bands {
			if r.FilterScore >= b.min && r.FilterScore < b.max {
				result[i].Trades++
				result[i].AvgPnL += r.PnLPct
				if r.PnLPct > 0 {
					result[i].WinRate++
				}
				break
			}
		}
	}

	for i := range result {
		if result[i].Trades > 0 {
			result[i].WinRate = result[i].WinRate / float64(result[i].Trades)
			result[i].AvgPnL = result[i].AvgPnL / float64(result[i].Trades)
			result[i].KellyF = computeHalfKelly(records, func(r TradeRecord) bool {
				return r.FilterScore >= bands[i].min && r.FilterScore < bands[i].max
			})
		}
	}

	return result
}

func computeHalfKelly(records []TradeRecord, filter func(TradeRecord) bool) float64 {
	var wins, losses []float64
	for _, r := range records {
		if !filter(r) {
			continue
		}
		if r.PnLPct > 0 {
			wins = append(wins, r.PnLPct)
		} else {
			losses = append(losses, r.PnLPct)
		}
	}

	totalTrades := len(wins) + len(losses)
	if totalTrades == 0 {
		return 0
	}

	winRate := float64(len(wins)) / float64(totalTrades)

	if len(wins) == 0 || len(losses) == 0 {
		// Edge case: if all wins, Kelly is maximally positive but bounded.
		if len(losses) == 0 && len(wins) > 0 {
			return 0.5 // cap at 0.5 for half-Kelly with no losses
		}
		// All losses → kelly is negative.
		return -0.5
	}

	var avgWin, avgLoss float64
	for _, w := range wins {
		avgWin += w
	}
	avgWin /= float64(len(wins))

	for _, l := range losses {
		avgLoss += math.Abs(l)
	}
	avgLoss /= float64(len(losses))

	if avgLoss == 0 {
		return 0.5
	}

	kelly := winRate - (1-winRate)/(avgWin/avgLoss)
	return kelly * 0.5
}

func computeHeatBuckets(records []TradeRecord) [3]TradeStats {
	buckets := [3]TradeStats{
		{Label: "low"},
		{Label: "mid"},
		{Label: "high"},
	}

	var winCounts [3]int

	for _, r := range records {
		var idx int
		switch {
		case r.EntryHeat < 0.2:
			idx = 0
		case r.EntryHeat <= 0.6:
			idx = 1
		default:
			idx = 2
		}
		buckets[idx].Trades++
		buckets[idx].AvgPnL += r.PnLPct
		if r.PnLPct > 0 {
			winCounts[idx]++
		}
	}

	for i := range buckets {
		if buckets[i].Trades > 0 {
			buckets[i].WinRate = float64(winCounts[i]) / float64(buckets[i].Trades)
			buckets[i].AvgPnL /= float64(buckets[i].Trades)
		}
	}

	return buckets
}

func computeBotImpact(records []TradeRecord) *BotImpactStats {
	var botRecords, organicRecords []TradeRecord
	for _, r := range records {
		if r.BotBuyCount > 0 {
			botRecords = append(botRecords, r)
		} else {
			organicRecords = append(organicRecords, r)
		}
	}

	// Only report if there are both groups.
	if len(botRecords) == 0 && len(organicRecords) == 0 {
		return nil
	}

	stats := &BotImpactStats{
		BotSnipedTrades: len(botRecords),
		OrganicTrades:   len(organicRecords),
	}

	if len(botRecords) > 0 {
		var wins int
		var pnlSum float64
		for _, r := range botRecords {
			if r.PnLPct > 0 {
				wins++
			}
			pnlSum += r.PnLPct
		}
		stats.BotSnipedWinRate = float64(wins) / float64(len(botRecords))
		stats.BotSnipedAvgPnL = pnlSum / float64(len(botRecords))
	}

	if len(organicRecords) > 0 {
		var wins int
		var pnlSum float64
		for _, r := range organicRecords {
			if r.PnLPct > 0 {
				wins++
			}
			pnlSum += r.PnLPct
		}
		stats.OrganicWinRate = float64(wins) / float64(len(organicRecords))
		stats.OrganicAvgPnL = pnlSum / float64(len(organicRecords))
	}

	return stats
}

func generateRecommendations(a *Assessment) []Recommendation {
	var recs []Recommendation

	// If lowest score band has <25% win rate and 10+ trades, recommend raising MIN_SCORE_THRESHOLD.
	if len(a.ScoreBands) > 0 {
		lowest := a.ScoreBands[0]
		if lowest.Trades >= 10 && lowest.WinRate < 0.25 {
			recs = append(recs, Recommendation{
				Parameter: "MIN_SCORE_THRESHOLD",
				Current:   "current",
				Suggested: "raise by 5",
				Reason:    "lowest score band has <25% win rate with 10+ trades",
			})
		}
	}

	// If a signal's avg is higher on losers, recommend negative weighting.
	for _, sc := range a.SignalCorrelations {
		if sc.Predictive && sc.Direction == "higher_worse" {
			recs = append(recs, Recommendation{
				Parameter: sc.Signal + "_WEIGHT",
				Current:   "positive",
				Suggested: "negative",
				Reason:    sc.Signal + " signal average is higher on losers than winners",
			})
		}
	}

	// If OFI avg_win > avg_loss + 0.15, recommend raising MIN_OFI_THRESHOLD.
	for _, sc := range a.SignalCorrelations {
		if sc.Signal == "OFI" && sc.AvgScoreWin > sc.AvgScoreLoss+0.15 {
			recs = append(recs, Recommendation{
				Parameter: "MIN_OFI_THRESHOLD",
				Current:   "current",
				Suggested: "raise by 0.1",
				Reason:    "OFI is significantly higher on winning trades",
			})
		}
	}

	// If "no-trade-activity" or "stale-position" exits dominate (>40%).
	if a.TotalTrades > 0 {
		staleCount := 0
		for _, es := range a.ExitBreakdown {
			if es.Reason == "no-trade-activity" || es.Reason == "stale-position" {
				staleCount += es.Count
			}
		}
		if float64(staleCount)/float64(a.TotalTrades) > 0.40 {
			recs = append(recs, Recommendation{
				Parameter: "STALE_TIMEOUT",
				Current:   "current",
				Suggested: "decrease by 30s",
				Reason:    "stale/no-activity exits dominate (>40% of trades)",
			})
		}
	}

	// If "stop-loss" exits >50%.
	if a.TotalTrades > 0 {
		for _, es := range a.ExitBreakdown {
			if es.Reason == "stop-loss" && float64(es.Count)/float64(a.TotalTrades) > 0.50 {
				recs = append(recs, Recommendation{
					Parameter: "STOP_LOSS_PCT",
					Current:   "current",
					Suggested: "tighten by 5%",
					Reason:    "stop-loss exits exceed 50% of trades",
				})
			}
		}
	}

	// If bot-sniped trades have significantly worse PnL.
	if a.BotActivityStats != nil && a.BotActivityStats.BotSnipedTrades > 0 && a.BotActivityStats.OrganicTrades > 0 {
		if a.BotActivityStats.BotSnipedAvgPnL < a.BotActivityStats.OrganicAvgPnL-5 {
			recs = append(recs, Recommendation{
				Parameter: "BOT_FILTER",
				Current:   "current",
				Suggested: "stricter",
				Reason:    "bot-sniped trades have significantly worse PnL than organic trades",
			})
		}
	}

	return recs
}

func safeDiv(num, denom float64) float64 {
	if denom == 0 {
		return 0
	}
	return num / denom
}
