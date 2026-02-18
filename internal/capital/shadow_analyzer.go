package capital

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/cindocode/trenchbot/internal/config"
)

// ShadowSummary holds aggregated metrics from a shadow (or live) run period.
type ShadowSummary struct {
	Duration       time.Duration
	TradeCount     int
	WinCount       int
	LossCount      int
	TotalPnLPct    float64
	AvgWinPct      float64
	AvgLossPct     float64 // positive number
	AvgHoldMinutes float64
	GasSpent       float64
	TokensSeen     int // total tokens received by scanner
	TokensPassed   int // tokens that passed filter scoring
}

// WinRate returns the win rate as a fraction.
func (s ShadowSummary) WinRate() float64 {
	if s.TradeCount == 0 {
		return 0
	}
	return float64(s.WinCount) / float64(s.TradeCount)
}

// FilterPassRate returns the fraction of tokens that passed all filters.
func (s ShadowSummary) FilterPassRate() float64 {
	if s.TokensSeen == 0 {
		return 0
	}
	return float64(s.TokensPassed) / float64(s.TokensSeen)
}

// ComparisonReport shows theoretical vs shadow-extrapolated projections.
type ComparisonReport struct {
	Shadow       ShadowSummary
	Theoretical  ModelOutput // from default assumptions
	Extrapolated ModelOutput // seeded with actual shadow rates
}

// AnalyzeShadowRun queries Postgres for trades in the given time window and
// computes a ShadowSummary. The tokensSeen/tokensPassed counters come from
// the state store (passed in directly since they may not be in the DB).
func AnalyzeShadowRun(ctx context.Context, db *sql.DB, since time.Time, tokensSeen, tokensPassed int) (ShadowSummary, error) {
	until := time.Now()
	duration := until.Sub(since)

	// Query sell-side trades (which have PnL data).
	rows, err := db.QueryContext(ctx, `
		SELECT pnl_pct, gas_cost, exit_reason
		FROM sniper_trades
		WHERE side = 'sell' AND created_at >= $1 AND created_at < $2
		ORDER BY created_at
	`, since, until)
	if err != nil {
		return ShadowSummary{}, fmt.Errorf("query trades: %w", err)
	}
	defer rows.Close()

	var s ShadowSummary
	s.Duration = duration
	s.TokensSeen = tokensSeen
	s.TokensPassed = tokensPassed

	var totalWinPct, totalLossPct float64

	for rows.Next() {
		var pnlPct sql.NullFloat64
		var gasCost float64
		var exitReason sql.NullString

		if err := rows.Scan(&pnlPct, &gasCost, &exitReason); err != nil {
			return ShadowSummary{}, fmt.Errorf("scan trade: %w", err)
		}

		if !pnlPct.Valid {
			continue
		}

		s.TradeCount++
		s.GasSpent += gasCost

		if pnlPct.Float64 >= 0 {
			s.WinCount++
			totalWinPct += pnlPct.Float64
		} else {
			s.LossCount++
			totalLossPct += math.Abs(pnlPct.Float64)
		}
		s.TotalPnLPct += pnlPct.Float64
	}
	if err := rows.Err(); err != nil {
		return ShadowSummary{}, fmt.Errorf("iterate trades: %w", err)
	}

	if s.WinCount > 0 {
		s.AvgWinPct = totalWinPct / float64(s.WinCount)
	}
	if s.LossCount > 0 {
		s.AvgLossPct = totalLossPct / float64(s.LossCount)
	}

	// Query average hold duration from token_observations.
	var avgHold sql.NullFloat64
	err = db.QueryRowContext(ctx, `
		SELECT AVG(hold_duration_sec) / 60.0
		FROM token_observations
		WHERE hold_duration_sec IS NOT NULL AND hold_duration_sec > 0
		  AND closed_at >= $1 AND closed_at < $2
	`, since, until).Scan(&avgHold)
	if err == nil && avgHold.Valid {
		s.AvgHoldMinutes = avgHold.Float64
	} else {
		s.AvgHoldMinutes = 15 // fallback default
	}

	// Also add gas from buy-side trades.
	var buyGas sql.NullFloat64
	err = db.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(gas_cost), 0)
		FROM sniper_trades
		WHERE side = 'buy' AND created_at >= $1 AND created_at < $2
	`, since, until).Scan(&buyGas)
	if err == nil && buyGas.Valid {
		s.GasSpent += buyGas.Float64
	}

	return s, nil
}

// CompareToModel runs the theoretical model with default inputs and then again
// with shadow-observed rates, returning both for comparison.
func CompareToModel(shadow ShadowSummary, cfg *config.Config) ComparisonReport {
	// Pure theoretical with default assumptions.
	theoretical := ComputeModel(DefaultModelInputs(cfg))

	// Extrapolated: seed with actual shadow data.
	extraInputs := DefaultModelInputs(cfg)
	if shadow.TradeCount > 0 {
		extraInputs.WinRate = shadow.WinRate()
		if shadow.AvgWinPct > 0 {
			extraInputs.AvgWinPct = shadow.AvgWinPct
		}
		if shadow.AvgLossPct > 0 {
			extraInputs.AvgLossPct = shadow.AvgLossPct
		}
	}
	if shadow.AvgHoldMinutes > 0 {
		extraInputs.AvgHoldMinutes = shadow.AvgHoldMinutes
	}
	if shadow.FilterPassRate() > 0 {
		extraInputs.FilterPassRate = shadow.FilterPassRate()
	}
	// Extrapolate tokens/day from shadow duration.
	if shadow.Duration > 0 && shadow.TokensSeen > 0 {
		hoursObserved := shadow.Duration.Hours()
		if hoursObserved > 0 {
			extraInputs.TokensPerDay = int(float64(shadow.TokensSeen) / hoursObserved * 24)
		}
	}

	extrapolated := ComputeModel(extraInputs)

	return ComparisonReport{
		Shadow:       shadow,
		Theoretical:  theoretical,
		Extrapolated: extrapolated,
	}
}

// String formats the comparison as a human-readable report.
func (r ComparisonReport) String() string {
	var b strings.Builder

	b.WriteString(r.Theoretical.String())

	if r.Shadow.TradeCount == 0 {
		b.WriteString("\n(No shadow data available for comparison)\n")
		return b.String()
	}

	b.WriteString(fmt.Sprintf("\n--- Shadow Run (%s) ---\n", r.Shadow.Duration.Round(time.Minute)))
	b.WriteString(fmt.Sprintf("  Trades:                  %d (%d W / %d L)\n", r.Shadow.TradeCount, r.Shadow.WinCount, r.Shadow.LossCount))
	b.WriteString(fmt.Sprintf("  Win rate:                %.1f%%\n", r.Shadow.WinRate()*100))
	b.WriteString(fmt.Sprintf("  Avg win:                 +%.1f%%\n", r.Shadow.AvgWinPct))
	b.WriteString(fmt.Sprintf("  Avg loss:                -%.1f%%\n", r.Shadow.AvgLossPct))
	b.WriteString(fmt.Sprintf("  Total PnL:               %+.1f%%\n", r.Shadow.TotalPnLPct))
	b.WriteString(fmt.Sprintf("  Avg hold:                %.1f min\n", r.Shadow.AvgHoldMinutes))
	b.WriteString(fmt.Sprintf("  Gas spent:               %.6f SOL\n", r.Shadow.GasSpent))
	b.WriteString(fmt.Sprintf("  Tokens seen:             %d\n", r.Shadow.TokensSeen))
	b.WriteString(fmt.Sprintf("  Filter pass rate:        %.3f%%\n", r.Shadow.FilterPassRate()*100))

	b.WriteString("\n--- Comparison: Theoretical vs Shadow (extrapolated to 24h) ---\n")
	b.WriteString(fmt.Sprintf("  %-28s %12s %12s\n", "", "Theoretical", "Extrapolated"))
	b.WriteString(fmt.Sprintf("  %-28s %12.0f %12.0f\n", "Buys/day:", r.Theoretical.RealisticBuysPerDay, r.Extrapolated.RealisticBuysPerDay))
	b.WriteString(fmt.Sprintf("  %-28s %11.1f%% %11.1f%%\n", "Win rate:", r.Theoretical.InputWinRate*100, r.Shadow.WinRate()*100))
	b.WriteString(fmt.Sprintf("  %-28s %10.4f %10.4f\n", "Net EV/trade (SOL):", r.Theoretical.NetEVPerTrade, r.Extrapolated.NetEVPerTrade))
	b.WriteString(fmt.Sprintf("  %-28s %10.4f %10.4f\n", "Daily net profit (SOL):", r.Theoretical.DailyNetProfit, r.Extrapolated.DailyNetProfit))
	b.WriteString(fmt.Sprintf("  %-28s %10.2f %10.2f\n", "Monthly net profit (SOL):", r.Theoretical.MonthlyNetProfit, r.Extrapolated.MonthlyNetProfit))
	b.WriteString(fmt.Sprintf("  %-28s %10.2f %10.2f\n", "Monthly sweep (SOL):", r.Theoretical.MonthlySweepEstimate, r.Extrapolated.MonthlySweepEstimate))
	b.WriteString(fmt.Sprintf("  %-28s %9.1f%% %9.1f%%\n", "Annualized ROI:", r.Theoretical.AnnualizedROIPct, r.Extrapolated.AnnualizedROIPct))

	return b.String()
}
