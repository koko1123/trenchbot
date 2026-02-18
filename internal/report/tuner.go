package report

// TuningPatch represents conservative parameter adjustments derived from an assessment.
type TuningPatch struct {
	MinScoreThreshold *int     `json:"min_score_threshold,omitempty"`
	OFIThreshold      *float64 `json:"ofi_threshold,omitempty"`
	MaxObsGrowth      *float64 `json:"max_obs_growth,omitempty"`
	StaleTimeoutSec   *int     `json:"stale_timeout_sec,omitempty"`
	StopLossPct       *float64 `json:"stop_loss_pct,omitempty"`
	TrailingThreshold *float64 `json:"trailing_threshold,omitempty"`
}

// PatchableConfig holds current values of each tunable parameter.
type PatchableConfig struct {
	MinScoreThreshold int
	OFIThreshold      float64
	MaxObsGrowth      float64
	StaleTimeoutSec   int
	StopLossPct       float64
	TrailingThreshold float64
}

// GeneratePatch produces a conservative TuningPatch based on assessment results and current config.
// All adjustments are bounded to prevent large swings.
func GeneratePatch(assessment *Assessment, currentConfig PatchableConfig) *TuningPatch {
	if assessment == nil {
		return &TuningPatch{}
	}

	patch := &TuningPatch{}

	// MinScore: move ±5 max. If lowest band <25% win rate over 10+ trades → raise by 5.
	if len(assessment.ScoreBands) > 0 {
		lowest := assessment.ScoreBands[0]
		if lowest.Trades >= 10 && lowest.WinRate < 0.25 {
			newVal := currentConfig.MinScoreThreshold + 5
			patch.MinScoreThreshold = &newVal
		}
	}

	// OFI: move ±0.1 max. If OFI signal is predictive and direction=higher_better → raise by 0.1.
	for _, sc := range assessment.SignalCorrelations {
		if sc.Signal == "OFI" && sc.Predictive && sc.Direction == "higher_better" {
			newVal := currentConfig.OFIThreshold + 0.1
			patch.OFIThreshold = &newVal
			break
		}
	}

	// MaxObsGrowth: move ±100 max. If ObsGrowthRate signal predictive and direction=lower_better → lower by 100.
	// "lower_better" means lower values on winners → direction would be "higher_worse"
	// (since higher = worse for winners). So we check for higher_worse direction.
	for _, sc := range assessment.SignalCorrelations {
		if sc.Signal == "ObsGrowthRate" && sc.Predictive && sc.Direction == "higher_worse" {
			newVal := currentConfig.MaxObsGrowth - 100
			patch.MaxObsGrowth = &newVal
			break
		}
	}

	// StaleTimeout: move ±30s max, min 60s. If stale exits >40% with avg PnL < -20% → decrease by 30.
	if assessment.TotalTrades > 0 {
		staleCount := 0
		stalePnLSum := 0.0
		for _, es := range assessment.ExitBreakdown {
			if es.Reason == "no-trade-activity" || es.Reason == "stale-position" {
				staleCount += es.Count
				stalePnLSum += es.TotalPnL
			}
		}
		staleFrac := float64(staleCount) / float64(assessment.TotalTrades)
		staleAvgPnL := 0.0
		if staleCount > 0 {
			staleAvgPnL = stalePnLSum / float64(staleCount)
		}
		if staleFrac > 0.40 && staleAvgPnL < -20 {
			newVal := currentConfig.StaleTimeoutSec - 30
			if newVal < 60 {
				newVal = 60
			}
			patch.StaleTimeoutSec = &newVal
		}
	}

	// StopLoss: move ±5 max, min 15%. If stop-loss exits >50% → tighten by 5.
	if assessment.TotalTrades > 0 {
		for _, es := range assessment.ExitBreakdown {
			if es.Reason == "stop-loss" && float64(es.Count)/float64(assessment.TotalTrades) > 0.50 {
				newVal := currentConfig.StopLossPct - 5
				if newVal < 15 {
					newVal = 15
				}
				patch.StopLossPct = &newVal
				break
			}
		}
	}

	return patch
}
