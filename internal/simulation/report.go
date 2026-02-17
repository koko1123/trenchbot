package simulation

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type ArchetypeStats struct {
	Generated int     `json:"generated"`
	Bought    int     `json:"bought"`
	AvgPnL    float64 `json:"avg_pnl"`
	WinRate   float64 `json:"win_rate"`
}

type Report struct {
	Seed              int64         `json:"seed"`
	DataSource        string        `json:"data_source,omitempty"`
	SimulatedDuration time.Duration `json:"simulated_duration"`
	WallClockElapsed  time.Duration `json:"wall_clock_elapsed"`

	TokensGenerated int `json:"tokens_generated"`
	TokensFiltered  int `json:"tokens_filtered"`
	TokensBlocked   int `json:"tokens_blocked"`
	TokensBought    int `json:"tokens_bought"`

	ExitsByReason map[string]int `json:"exits_by_reason"`

	TotalPnLPct float64 `json:"total_pnl_pct"`
	WinCount    int     `json:"win_count"`
	LossCount   int     `json:"loss_count"`
	WinRate     float64 `json:"win_rate"`
	BestTrade   float64 `json:"best_trade"`
	WorstTrade  float64 `json:"worst_trade"`
	AveragePnL  float64 `json:"average_pnl"`

	MaxDrawdownPct       float64 `json:"max_drawdown_pct"`
	CircuitBreakerHalts  int     `json:"circuit_breaker_halts"`
	CircuitBreakerPauses int     `json:"circuit_breaker_pauses"`
	MarketShocks         int     `json:"market_shocks"`

	ArchetypeResults map[TokenArchetype]ArchetypeStats `json:"archetype_results"`
}

func (r *Report) Finalize() {
	total := r.WinCount + r.LossCount
	if total > 0 {
		r.WinRate = float64(r.WinCount) / float64(total)
		r.AveragePnL = r.TotalPnLPct / float64(total)
	}
}

func (r *Report) JSON() ([]byte, error) {
	return json.MarshalIndent(r, "", "  ")
}

func (r *Report) String() string {
	var b strings.Builder
	b.WriteString("=== SIMULATION REPORT ===\n")
	if r.DataSource != "" {
		b.WriteString(fmt.Sprintf("Data Source:        %s\n", r.DataSource))
	}
	b.WriteString(fmt.Sprintf("Seed:               %d\n", r.Seed))
	b.WriteString(fmt.Sprintf("Simulated Duration: %v\n", r.SimulatedDuration))
	b.WriteString(fmt.Sprintf("Wall Clock:         %v\n", r.WallClockElapsed))
	b.WriteString("\n--- Token Flow ---\n")
	b.WriteString(fmt.Sprintf("Generated:  %d\n", r.TokensGenerated))
	b.WriteString(fmt.Sprintf("Filtered:   %d (passed scoring)\n", r.TokensFiltered))
	b.WriteString(fmt.Sprintf("Blocked:    %d (circuit breaker/limits)\n", r.TokensBlocked))
	b.WriteString(fmt.Sprintf("Bought:     %d\n", r.TokensBought))
	b.WriteString("\n--- P&L ---\n")
	b.WriteString(fmt.Sprintf("Total PnL:  %.1f%%\n", r.TotalPnLPct))
	b.WriteString(fmt.Sprintf("Wins:       %d\n", r.WinCount))
	b.WriteString(fmt.Sprintf("Losses:     %d\n", r.LossCount))
	b.WriteString(fmt.Sprintf("Win Rate:   %.1f%%\n", r.WinRate*100))
	b.WriteString(fmt.Sprintf("Best:       %.1f%%\n", r.BestTrade))
	b.WriteString(fmt.Sprintf("Worst:      %.1f%%\n", r.WorstTrade))
	b.WriteString(fmt.Sprintf("Average:    %.1f%%\n", r.AveragePnL))
	b.WriteString("\n--- Risk ---\n")
	b.WriteString(fmt.Sprintf("Max Drawdown: %.1f%%\n", r.MaxDrawdownPct))
	b.WriteString(fmt.Sprintf("CB Halts:     %d\n", r.CircuitBreakerHalts))
	b.WriteString(fmt.Sprintf("CB Pauses:    %d\n", r.CircuitBreakerPauses))
	b.WriteString(fmt.Sprintf("Mkt Shocks:   %d\n", r.MarketShocks))
	b.WriteString("\n--- Exits by Reason ---\n")
	for reason, count := range r.ExitsByReason {
		b.WriteString(fmt.Sprintf("  %-20s %d\n", reason, count))
	}
	b.WriteString("\n--- Archetype Breakdown ---\n")
	for arch, stats := range r.ArchetypeResults {
		b.WriteString(fmt.Sprintf("  %-12s gen=%d bought=%d avgPnL=%.1f%% winRate=%.0f%%\n",
			arch, stats.Generated, stats.Bought, stats.AvgPnL, stats.WinRate*100))
	}
	return b.String()
}
