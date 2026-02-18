package reporter

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/cindocode/trenchbot/internal/state"
)

var testLog = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

func TestComputeSnapshot_Empty(t *testing.T) {
	store := state.NewStore()
	rep := New(nil, store, testLog)

	now := time.Now()
	snap := rep.ComputeSnapshot(context.Background(), "hourly", now.Add(-1*time.Hour), now)

	if snap.Period != "hourly" {
		t.Errorf("expected period 'hourly', got %q", snap.Period)
	}
	if snap.TradesClosed != 0 {
		t.Errorf("expected 0 trades closed, got %d", snap.TradesClosed)
	}
	if snap.WinCount != 0 {
		t.Errorf("expected 0 wins, got %d", snap.WinCount)
	}
	if snap.LossCount != 0 {
		t.Errorf("expected 0 losses, got %d", snap.LossCount)
	}
	if snap.WinRate != 0 {
		t.Errorf("expected 0 win rate, got %f", snap.WinRate)
	}
	if snap.TotalPnLPct != 0 {
		t.Errorf("expected 0 total PnL, got %f", snap.TotalPnLPct)
	}
}

func TestComputeSnapshot_OpenPositions(t *testing.T) {
	store := state.NewStore()
	store.AddPosition(&state.Position{
		ID: "p1", Chain: state.ChainSolana, TokenAddress: "addr1",
		EntryPrice: 1.0, CurrentPrice: 1.5, Amount: 0.3,
	})
	store.AddPosition(&state.Position{
		ID: "p2", Chain: state.ChainSolana, TokenAddress: "addr2",
		EntryPrice: 1.0, CurrentPrice: 0.5, Amount: 0.3,
	})

	rep := New(nil, store, testLog)
	snap := rep.ComputeSnapshot(context.Background(), "hourly", time.Now().Add(-1*time.Hour), time.Now())

	if snap.OpenPositions != 2 {
		t.Errorf("expected 2 open positions, got %d", snap.OpenPositions)
	}
}

func TestFormatText(t *testing.T) {
	snap := Snapshot{
		Period:        "daily",
		PeriodStart:   time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC),
		PeriodEnd:     time.Date(2025, 6, 2, 0, 0, 0, 0, time.UTC),
		OpenPositions: 3,
		TradesClosed:  18,
		WinCount:      8,
		LossCount:     10,
		WinRate:       0.444,
		TotalPnLPct:   57.6,
		BestTrade:     170.9,
		WorstTrade:    -52.1,
		AvgPnL:        3.2,
		ExitsByReason:  map[string]int{"stop-loss": 6, "tranche-1": 5},
		GasRemaining:  0.182,
		GasSpent:      0.068,
		DrawdownPct:   12.3,
		CBStatus:      "ok",
	}

	text := FormatText(snap)

	if !strings.Contains(text, "DAILY REPORT") {
		t.Error("expected DAILY REPORT header")
	}
	if !strings.Contains(text, "Open Positions:  3") {
		t.Error("expected open positions")
	}
	if !strings.Contains(text, "Trades Closed:   18") {
		t.Error("expected trades closed")
	}
	if !strings.Contains(text, "Circuit Breaker: ok") {
		t.Error("expected circuit breaker status")
	}
}

func TestFormatJSON(t *testing.T) {
	snap := Snapshot{
		Period:      "hourly",
		PeriodStart: time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC),
		PeriodEnd:   time.Date(2025, 6, 1, 13, 0, 0, 0, time.UTC),
		WinCount:    5,
		LossCount:   3,
		WinRate:     0.625,
	}

	data, err := FormatJSON(snap)
	if err != nil {
		t.Fatalf("FormatJSON error: %v", err)
	}
	if len(data) == 0 {
		t.Error("expected non-empty JSON")
	}
	if !strings.Contains(string(data), `"win_rate"`) {
		t.Error("expected win_rate in JSON output")
	}
}

func TestRecordTrade_NilStore(t *testing.T) {
	store := state.NewStore()
	rep := New(nil, store, testLog)

	// Should not panic with nil store.
	rep.RecordTrade(context.Background(), TradeRow{
		ID:    "test-trade",
		Chain: "solana",
		Side:  "buy",
	})
}

func TestSaveReport_NilStore(t *testing.T) {
	store := state.NewStore()
	rep := New(nil, store, testLog)

	// Should not panic with nil store.
	rep.SaveReport(context.Background(), Snapshot{Period: "test"})
}
