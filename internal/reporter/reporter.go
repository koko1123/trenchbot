package reporter

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/cindocode/trenchbot/internal/risk"
	"github.com/cindocode/trenchbot/internal/state"
)

// Snapshot holds computed stats for a reporting period.
type Snapshot struct {
	Period      string    `json:"period"`
	PeriodStart time.Time `json:"period_start"`
	PeriodEnd   time.Time `json:"period_end"`

	OpenPositions int     `json:"open_positions"`
	TradesClosed  int     `json:"trades_closed"`
	WinCount      int     `json:"win_count"`
	LossCount     int     `json:"loss_count"`
	WinRate       float64 `json:"win_rate"`
	TotalPnLPct   float64 `json:"total_pnl_pct"`
	BestTrade     float64 `json:"best_trade"`
	WorstTrade    float64 `json:"worst_trade"`
	AvgPnL        float64 `json:"avg_pnl"`

	ExitsByReason map[string]int `json:"exits_by_reason"`

	GasRemaining float64 `json:"gas_remaining"`
	GasSpent     float64 `json:"gas_spent"`
	DrawdownPct  float64 `json:"drawdown_pct"`
	CBStatus     string  `json:"cb_status"`
}

// Reporter computes and persists periodic reports.
type Reporter struct {
	store      *ReportStore // nil if no DATABASE_URL
	stateStore *state.Store
	breakers   map[state.Chain]*risk.CircuitBreaker
	log        *slog.Logger
}

// New creates a Reporter. store can be nil if Postgres is not configured.
func New(store *ReportStore, stateStore *state.Store, log *slog.Logger) *Reporter {
	return &Reporter{
		store:      store,
		stateStore: stateStore,
		breakers:   make(map[state.Chain]*risk.CircuitBreaker),
		log:        log,
	}
}

// SetCircuitBreaker registers a circuit breaker for status reporting.
func (r *Reporter) SetCircuitBreaker(chain state.Chain, cb *risk.CircuitBreaker) {
	r.breakers[chain] = cb
}

// ComputeSnapshot reads trades from Postgres and computes stats for the period.
// If store is nil, returns a snapshot with only in-memory data (open positions, gas, CB status).
func (r *Reporter) ComputeSnapshot(ctx context.Context, period string, since, until time.Time) Snapshot {
	snap := Snapshot{
		Period:        period,
		PeriodStart:   since,
		PeriodEnd:     until,
		OpenPositions: r.stateStore.TotalOpenPositionCount(),
		ExitsByReason: make(map[string]int),
		GasRemaining:  r.stateStore.GetGasBalance(state.ChainSolana),
		GasSpent:      r.stateStore.GetGasSpent(state.ChainSolana),
	}

	// Circuit breaker status (use Solana as primary).
	if cb, ok := r.breakers[state.ChainSolana]; ok {
		snap.CBStatus = cb.Status()
		peak := r.stateStore.GetPeakEquity(state.ChainSolana)
		if peak > 0 {
			equity := peak // approximate; exact requires iterating positions
			for _, pos := range r.stateStore.AllOpenPositions() {
				if pos.Chain == state.ChainSolana && pos.EntryPrice > 0 {
					pnlMult := pos.CurrentPrice / pos.EntryPrice
					equity += pos.Amount * (pnlMult - 1)
				}
			}
			snap.DrawdownPct = ((peak - equity) / peak) * 100
			if snap.DrawdownPct < 0 {
				snap.DrawdownPct = 0
			}
		}
	}

	if r.store == nil {
		return snap
	}

	trades, err := r.store.QueryAllTrades(ctx, since, until)
	if err != nil {
		r.log.Error("failed to query trades for snapshot", "err", err)
		return snap
	}

	for _, t := range trades {
		if t.Side == "sell" && t.PnLPct != nil {
			snap.TradesClosed++
			pnl := *t.PnLPct
			snap.TotalPnLPct += pnl
			if pnl >= 0 {
				snap.WinCount++
			} else {
				snap.LossCount++
			}
			if pnl > snap.BestTrade {
				snap.BestTrade = pnl
			}
			if pnl < snap.WorstTrade {
				snap.WorstTrade = pnl
			}
			if t.ExitReason != nil {
				snap.ExitsByReason[*t.ExitReason]++
			}
		}
	}

	if snap.TradesClosed > 0 {
		snap.WinRate = float64(snap.WinCount) / float64(snap.TradesClosed)
		snap.AvgPnL = snap.TotalPnLPct / float64(snap.TradesClosed)
	}

	return snap
}

// RecordTrade writes a trade to Postgres. No-op if store is nil.
func (r *Reporter) RecordTrade(ctx context.Context, t TradeRow) {
	if r.store == nil {
		return
	}
	if err := r.store.InsertTrade(ctx, t); err != nil {
		r.log.Error("failed to record trade", "id", t.ID, "err", err)
	}
}

// SaveReport writes a snapshot to Postgres. No-op if store is nil.
func (r *Reporter) SaveReport(ctx context.Context, snap Snapshot) {
	if r.store == nil {
		return
	}
	if err := r.store.InsertReport(ctx, snap); err != nil {
		r.log.Error("failed to save report", "period", snap.Period, "err", err)
	}
}

// FormatText returns a human-readable report string.
func FormatText(snap Snapshot) string {
	var b strings.Builder
	fmt.Fprintf(&b, "=== %s REPORT (%s) ===\n", strings.ToUpper(snap.Period), snap.PeriodStart.Format("2006-01-02"))
	fmt.Fprintf(&b, "Open Positions:  %d\n", snap.OpenPositions)
	fmt.Fprintf(&b, "Trades Closed:   %d\n", snap.TradesClosed)
	fmt.Fprintf(&b, "  Wins: %d (%.1f%%)  |  Losses: %d\n", snap.WinCount, snap.WinRate*100, snap.LossCount)
	fmt.Fprintf(&b, "  Avg P&L: %+.1f%%   |  Total: %+.1f%%\n", snap.AvgPnL, snap.TotalPnLPct)
	fmt.Fprintf(&b, "  Best: %+.1f%%     |  Worst: %+.1f%%\n", snap.BestTrade, snap.WorstTrade)
	b.WriteString("\n")

	if len(snap.ExitsByReason) > 0 {
		parts := make([]string, 0, len(snap.ExitsByReason))
		for reason, count := range snap.ExitsByReason {
			parts = append(parts, fmt.Sprintf("%s=%d", reason, count))
		}
		fmt.Fprintf(&b, "Exits: %s\n", strings.Join(parts, ", "))
		b.WriteString("\n")
	}

	fmt.Fprintf(&b, "Gas: %.3f SOL remaining (%.3f spent)\n", snap.GasRemaining, snap.GasSpent)
	fmt.Fprintf(&b, "Drawdown: %.1f%% | Circuit Breaker: %s\n", snap.DrawdownPct, snap.CBStatus)

	return b.String()
}

// FormatJSON returns a JSON-encoded snapshot.
func FormatJSON(snap Snapshot) ([]byte, error) {
	return json.MarshalIndent(snap, "", "  ")
}
