package report

import (
	"testing"
	"time"
)

func TestGenerateAssessment_Empty(t *testing.T) {
	a := GenerateAssessment(nil, 10*time.Minute)
	if a.TotalTrades != 0 {
		t.Errorf("expected 0 total trades, got %d", a.TotalTrades)
	}
	if a.WinRate != 0 {
		t.Errorf("expected 0 win rate, got %f", a.WinRate)
	}
	if a.TotalPnLPct != 0 {
		t.Errorf("expected 0 total pnl, got %f", a.TotalPnLPct)
	}
	if len(a.Recommendations) != 0 {
		t.Errorf("expected 0 recommendations, got %d", len(a.Recommendations))
	}
	if len(a.SignalCorrelations) != 0 {
		t.Errorf("expected 0 signal correlations, got %d", len(a.SignalCorrelations))
	}
	if len(a.ExitBreakdown) != 0 {
		t.Errorf("expected 0 exit breakdown, got %d", len(a.ExitBreakdown))
	}
	if a.RunDuration != 10*time.Minute {
		t.Errorf("expected 10m run duration, got %v", a.RunDuration)
	}
}

func TestGenerateAssessment_AllWins(t *testing.T) {
	records := make([]TradeRecord, 20)
	for i := range records {
		records[i] = TradeRecord{
			PnLPct:       10.0 + float64(i),
			Won:          true,
			ExitReason:   "take-profit",
			FilterScore:  80,
			SignalScores: map[string]int{"momentum": 80},
			OFI:          0.5,
			EntryHeat:    0.3,
			HoldDuration: 5 * time.Minute,
		}
	}

	a := GenerateAssessment(records, time.Hour)

	if a.WinRate != 1.0 {
		t.Errorf("expected 100%% win rate, got %f", a.WinRate)
	}
	if a.TotalTrades != 20 {
		t.Errorf("expected 20 trades, got %d", a.TotalTrades)
	}
	if a.TotalPnLPct <= 0 {
		t.Errorf("expected positive total PnL, got %f", a.TotalPnLPct)
	}

	// Check score band for [75-85) which should have all trades.
	found := false
	for _, sb := range a.ScoreBands {
		if sb.MinScore == 75 && sb.MaxScore == 85 {
			found = true
			if sb.Trades != 20 {
				t.Errorf("expected 20 trades in [75,85) band, got %d", sb.Trades)
			}
			if sb.WinRate != 1.0 {
				t.Errorf("expected 100%% win rate in band, got %f", sb.WinRate)
			}
			// Half-Kelly should be positive (capped at 0.5 for all-wins).
			if sb.KellyF <= 0 {
				t.Errorf("expected positive Kelly for all-wins band, got %f", sb.KellyF)
			}
		}
	}
	if !found {
		t.Error("did not find [75,85) score band")
	}
}

func TestGenerateAssessment_AllLosses(t *testing.T) {
	records := make([]TradeRecord, 15)
	for i := range records {
		records[i] = TradeRecord{
			PnLPct:       -10.0 - float64(i),
			Won:          false,
			ExitReason:   "stop-loss",
			FilterScore:  40,
			SignalScores: map[string]int{"momentum": 20},
			OFI:          0.1,
			EntryHeat:    0.1,
			HoldDuration: 2 * time.Minute,
		}
	}

	a := GenerateAssessment(records, 30*time.Minute)

	if a.WinRate != 0 {
		t.Errorf("expected 0%% win rate, got %f", a.WinRate)
	}
	if a.TotalPnLPct >= 0 {
		t.Errorf("expected negative total PnL, got %f", a.TotalPnLPct)
	}

	// Kelly should be negative for all-losses.
	for _, sb := range a.ScoreBands {
		if sb.Trades > 0 && sb.KellyF > 0 {
			t.Errorf("expected non-positive Kelly for all-losses band [%d,%d), got %f", sb.MinScore, sb.MaxScore, sb.KellyF)
		}
	}

	// Stop-loss exits should be >50%, triggering recommendation.
	foundRec := false
	for _, r := range a.Recommendations {
		if r.Parameter == "STOP_LOSS_PCT" {
			foundRec = true
		}
	}
	if !foundRec {
		t.Error("expected STOP_LOSS_PCT recommendation for >50% stop-loss exits")
	}
}

func TestGenerateAssessment_SignalCorrelation(t *testing.T) {
	var records []TradeRecord

	// Winners with high OFI.
	for i := 0; i < 20; i++ {
		records = append(records, TradeRecord{
			PnLPct:       15.0,
			Won:          true,
			ExitReason:   "take-profit",
			FilterScore:  70,
			SignalScores: map[string]int{"momentum": 90},
			OFI:          0.8,
			ObsGrowthRate: 100,
			HoldDuration: 5 * time.Minute,
		})
	}

	// Losers with low OFI.
	for i := 0; i < 20; i++ {
		records = append(records, TradeRecord{
			PnLPct:       -10.0,
			Won:          false,
			ExitReason:   "stop-loss",
			FilterScore:  70,
			SignalScores: map[string]int{"momentum": 30},
			OFI:          0.1,
			ObsGrowthRate: 500,
			HoldDuration: 2 * time.Minute,
		})
	}

	a := GenerateAssessment(records, time.Hour)

	// Find OFI correlation.
	var ofiCorr *SignalCorrelation
	for i, sc := range a.SignalCorrelations {
		if sc.Signal == "OFI" {
			ofiCorr = &a.SignalCorrelations[i]
			break
		}
	}
	if ofiCorr == nil {
		t.Fatal("expected OFI signal correlation")
	}
	if !ofiCorr.Predictive {
		t.Error("expected OFI to be predictive")
	}
	if ofiCorr.Direction != "higher_better" {
		t.Errorf("expected higher_better direction for OFI, got %s", ofiCorr.Direction)
	}
	if ofiCorr.AvgScoreWin <= ofiCorr.AvgScoreLoss {
		t.Errorf("expected OFI avg win (%f) > avg loss (%f)", ofiCorr.AvgScoreWin, ofiCorr.AvgScoreLoss)
	}

	// Verify momentum is also predictive (90 vs 30 across range of 60).
	var momCorr *SignalCorrelation
	for i, sc := range a.SignalCorrelations {
		if sc.Signal == "momentum" {
			momCorr = &a.SignalCorrelations[i]
			break
		}
	}
	if momCorr == nil {
		t.Fatal("expected momentum signal correlation")
	}
	if !momCorr.Predictive {
		t.Error("expected momentum to be predictive")
	}
	if momCorr.Direction != "higher_better" {
		t.Errorf("expected higher_better for momentum, got %s", momCorr.Direction)
	}

	// OFI recommendation should fire since AvgScoreWin > AvgScoreLoss + 0.15.
	foundOFIRec := false
	for _, r := range a.Recommendations {
		if r.Parameter == "MIN_OFI_THRESHOLD" {
			foundOFIRec = true
		}
	}
	if !foundOFIRec {
		t.Error("expected MIN_OFI_THRESHOLD recommendation")
	}
}

func TestGenerateAssessment_ScoreBands(t *testing.T) {
	records := []TradeRecord{
		{PnLPct: -20, FilterScore: 30, ExitReason: "stop-loss", HoldDuration: time.Minute},
		{PnLPct: -15, FilterScore: 40, ExitReason: "stop-loss", HoldDuration: time.Minute},
		{PnLPct: 5, FilterScore: 60, ExitReason: "take-profit", HoldDuration: time.Minute},
		{PnLPct: -5, FilterScore: 62, ExitReason: "stop-loss", HoldDuration: time.Minute},
		{PnLPct: 10, FilterScore: 70, ExitReason: "take-profit", HoldDuration: time.Minute},
		{PnLPct: 15, FilterScore: 72, ExitReason: "take-profit", HoldDuration: time.Minute},
		{PnLPct: 20, FilterScore: 80, ExitReason: "take-profit", HoldDuration: time.Minute},
		{PnLPct: 25, FilterScore: 90, ExitReason: "take-profit", HoldDuration: time.Minute},
		{PnLPct: 30, FilterScore: 95, ExitReason: "take-profit", HoldDuration: time.Minute},
	}

	a := GenerateAssessment(records, time.Hour)

	// Verify band bucketing.
	bandMap := map[int]ScoreBandStats{}
	for _, sb := range a.ScoreBands {
		bandMap[sb.MinScore] = sb
	}

	// [0,55) should have 2 trades (scores 30, 40).
	if b, ok := bandMap[0]; ok {
		if b.Trades != 2 {
			t.Errorf("[0,55) expected 2 trades, got %d", b.Trades)
		}
		if b.WinRate != 0 {
			t.Errorf("[0,55) expected 0%% win rate, got %f", b.WinRate)
		}
	} else {
		t.Error("missing [0,55) band")
	}

	// [55,65) should have 2 trades (scores 60, 62).
	if b, ok := bandMap[55]; ok {
		if b.Trades != 2 {
			t.Errorf("[55,65) expected 2 trades, got %d", b.Trades)
		}
	} else {
		t.Error("missing [55,65) band")
	}

	// [65,75) should have 2 trades (scores 70, 72).
	if b, ok := bandMap[65]; ok {
		if b.Trades != 2 {
			t.Errorf("[65,75) expected 2 trades, got %d", b.Trades)
		}
		if b.WinRate != 1.0 {
			t.Errorf("[65,75) expected 100%% win rate, got %f", b.WinRate)
		}
	} else {
		t.Error("missing [65,75) band")
	}

	// [75,85) should have 1 trade (score 80).
	if b, ok := bandMap[75]; ok {
		if b.Trades != 1 {
			t.Errorf("[75,85) expected 1 trade, got %d", b.Trades)
		}
	} else {
		t.Error("missing [75,85) band")
	}

	// [85,100) should have 2 trades (scores 90, 95).
	if b, ok := bandMap[85]; ok {
		if b.Trades != 2 {
			t.Errorf("[85,100) expected 2 trades, got %d", b.Trades)
		}
	} else {
		t.Error("missing [85,100) band")
	}
}

func TestGenerateAssessment_Recommendations(t *testing.T) {
	t.Run("low_score_band", func(t *testing.T) {
		// 12 trades in [0,55) with <25% win rate.
		var records []TradeRecord
		for i := 0; i < 12; i++ {
			pnl := -10.0
			if i < 2 { // 2/12 = 16.7% win rate
				pnl = 5.0
			}
			records = append(records, TradeRecord{
				PnLPct:       pnl,
				FilterScore:  30,
				ExitReason:   "stop-loss",
				HoldDuration: time.Minute,
			})
		}

		a := GenerateAssessment(records, time.Hour)

		found := false
		for _, r := range a.Recommendations {
			if r.Parameter == "MIN_SCORE_THRESHOLD" {
				found = true
			}
		}
		if !found {
			t.Error("expected MIN_SCORE_THRESHOLD recommendation for low-score-band with <25% win rate")
		}
	})

	t.Run("stale_exits_dominant", func(t *testing.T) {
		var records []TradeRecord
		// 6 stale exits out of 10 = 60%.
		for i := 0; i < 6; i++ {
			records = append(records, TradeRecord{
				PnLPct:       -5.0,
				FilterScore:  60,
				ExitReason:   "stale-position",
				HoldDuration: time.Minute,
			})
		}
		for i := 0; i < 4; i++ {
			records = append(records, TradeRecord{
				PnLPct:       10.0,
				FilterScore:  70,
				ExitReason:   "take-profit",
				HoldDuration: time.Minute,
			})
		}

		a := GenerateAssessment(records, time.Hour)

		found := false
		for _, r := range a.Recommendations {
			if r.Parameter == "STALE_TIMEOUT" {
				found = true
			}
		}
		if !found {
			t.Error("expected STALE_TIMEOUT recommendation for dominant stale exits")
		}
	})
}

func TestGeneratePatch(t *testing.T) {
	t.Run("raise_min_score", func(t *testing.T) {
		assessment := &Assessment{
			TotalTrades: 20,
			ScoreBands: []ScoreBandStats{
				{MinScore: 0, MaxScore: 55, Trades: 15, WinRate: 0.15, AvgPnL: -12},
			},
		}
		config := PatchableConfig{MinScoreThreshold: 50}

		patch := GeneratePatch(assessment, config)
		if patch.MinScoreThreshold == nil {
			t.Fatal("expected MinScoreThreshold to be set")
		}
		if *patch.MinScoreThreshold != 55 {
			t.Errorf("expected 55, got %d", *patch.MinScoreThreshold)
		}
	})

	t.Run("raise_ofi", func(t *testing.T) {
		assessment := &Assessment{
			SignalCorrelations: []SignalCorrelation{
				{Signal: "OFI", Predictive: true, Direction: "higher_better"},
			},
		}
		config := PatchableConfig{OFIThreshold: 0.3}

		patch := GeneratePatch(assessment, config)
		if patch.OFIThreshold == nil {
			t.Fatal("expected OFIThreshold to be set")
		}
		if *patch.OFIThreshold < 0.39 || *patch.OFIThreshold > 0.41 {
			t.Errorf("expected ~0.4, got %f", *patch.OFIThreshold)
		}
	})

	t.Run("lower_obs_growth", func(t *testing.T) {
		assessment := &Assessment{
			SignalCorrelations: []SignalCorrelation{
				{Signal: "ObsGrowthRate", Predictive: true, Direction: "higher_worse"},
			},
		}
		config := PatchableConfig{MaxObsGrowth: 500}

		patch := GeneratePatch(assessment, config)
		if patch.MaxObsGrowth == nil {
			t.Fatal("expected MaxObsGrowth to be set")
		}
		if *patch.MaxObsGrowth != 400 {
			t.Errorf("expected 400, got %f", *patch.MaxObsGrowth)
		}
	})

	t.Run("tighten_stop_loss", func(t *testing.T) {
		assessment := &Assessment{
			TotalTrades: 20,
			ExitBreakdown: []ExitStats{
				{Reason: "stop-loss", Count: 12},
			},
		}
		config := PatchableConfig{StopLossPct: 30}

		patch := GeneratePatch(assessment, config)
		if patch.StopLossPct == nil {
			t.Fatal("expected StopLossPct to be set")
		}
		if *patch.StopLossPct != 25 {
			t.Errorf("expected 25, got %f", *patch.StopLossPct)
		}
	})

	t.Run("stop_loss_min_bound", func(t *testing.T) {
		assessment := &Assessment{
			TotalTrades: 20,
			ExitBreakdown: []ExitStats{
				{Reason: "stop-loss", Count: 12},
			},
		}
		config := PatchableConfig{StopLossPct: 17}

		patch := GeneratePatch(assessment, config)
		if patch.StopLossPct == nil {
			t.Fatal("expected StopLossPct to be set")
		}
		if *patch.StopLossPct != 15 {
			t.Errorf("expected 15 (min bound), got %f", *patch.StopLossPct)
		}
	})

	t.Run("stale_timeout_decrease", func(t *testing.T) {
		assessment := &Assessment{
			TotalTrades: 10,
			ExitBreakdown: []ExitStats{
				{Reason: "stale-position", Count: 5, TotalPnL: -150, AvgPnLPct: -30},
				{Reason: "take-profit", Count: 5, TotalPnL: 50, AvgPnLPct: 10},
			},
		}
		config := PatchableConfig{StaleTimeoutSec: 120}

		patch := GeneratePatch(assessment, config)
		if patch.StaleTimeoutSec == nil {
			t.Fatal("expected StaleTimeoutSec to be set")
		}
		if *patch.StaleTimeoutSec != 90 {
			t.Errorf("expected 90, got %d", *patch.StaleTimeoutSec)
		}
	})

	t.Run("stale_timeout_min_bound", func(t *testing.T) {
		assessment := &Assessment{
			TotalTrades: 10,
			ExitBreakdown: []ExitStats{
				{Reason: "stale-position", Count: 5, TotalPnL: -150, AvgPnLPct: -30},
				{Reason: "take-profit", Count: 5, TotalPnL: 50, AvgPnLPct: 10},
			},
		}
		config := PatchableConfig{StaleTimeoutSec: 70}

		patch := GeneratePatch(assessment, config)
		if patch.StaleTimeoutSec == nil {
			t.Fatal("expected StaleTimeoutSec to be set")
		}
		if *patch.StaleTimeoutSec != 60 {
			t.Errorf("expected 60 (min bound), got %d", *patch.StaleTimeoutSec)
		}
	})

	t.Run("nil_assessment", func(t *testing.T) {
		patch := GeneratePatch(nil, PatchableConfig{})
		if patch.MinScoreThreshold != nil || patch.OFIThreshold != nil {
			t.Error("expected empty patch for nil assessment")
		}
	})

	t.Run("no_changes_needed", func(t *testing.T) {
		assessment := &Assessment{
			TotalTrades: 10,
			ScoreBands: []ScoreBandStats{
				{MinScore: 0, MaxScore: 55, Trades: 5, WinRate: 0.60, AvgPnL: 5},
			},
			ExitBreakdown: []ExitStats{
				{Reason: "take-profit", Count: 7},
				{Reason: "stop-loss", Count: 3},
			},
		}
		config := PatchableConfig{MinScoreThreshold: 50, StopLossPct: 25}

		patch := GeneratePatch(assessment, config)
		if patch.MinScoreThreshold != nil {
			t.Error("did not expect MinScoreThreshold change")
		}
		if patch.StopLossPct != nil {
			t.Error("did not expect StopLossPct change")
		}
	})
}
