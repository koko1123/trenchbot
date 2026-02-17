package monitor

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/cindocode/trenchbot/internal/clock"
	"github.com/cindocode/trenchbot/internal/executor"
	"github.com/cindocode/trenchbot/internal/state"
	"github.com/cindocode/trenchbot/internal/testutil"
)

var testLog = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

func setupMonitor(clk *clock.SimClock) (*Monitor, *state.Store, *testutil.MockExecutor, *testutil.MockNotifier) {
	store := state.NewStore()
	exec := testutil.NewMockExecutor(state.ChainSolana)
	notif := testutil.NewMockNotifier()
	executors := map[state.Chain]executor.Executor{
		state.ChainSolana: exec,
	}
	mon := New(store, executors, notif, DefaultExitConfig(), clk, true, testLog)
	return mon, store, exec, notif
}

func TestEvaluateExit_StopLoss(t *testing.T) {
	clk := clock.NewSimClock(time.Now())
	mon, store, exec, _ := setupMonitor(clk)

	store.AddPosition(&state.Position{
		ID: "p1", Chain: state.ChainSolana, TokenAddress: "addr1", TokenSymbol: "TEST",
		EntryPrice: 1.0, CurrentPrice: 0.49, PeakPrice: 1.0, EntryTime: clk.Now(),
	})

	mon.CheckPositions(context.Background())

	sells := exec.GetSellCalls()
	if len(sells) != 1 {
		t.Fatalf("expected 1 sell, got %d", len(sells))
	}
	if sells[0].AmountPct != 100 {
		t.Errorf("stop-loss should sell 100%%, got %.0f%%", sells[0].AmountPct)
	}
}

func TestEvaluateExit_Tranche1(t *testing.T) {
	clk := clock.NewSimClock(time.Now())
	mon, store, exec, _ := setupMonitor(clk)

	store.AddPosition(&state.Position{
		ID: "p1", Chain: state.ChainSolana, TokenAddress: "addr1", TokenSymbol: "TEST",
		EntryPrice: 1.0, CurrentPrice: 2.0, PeakPrice: 2.0, SoldPct: 0, EntryTime: clk.Now(),
	})

	mon.CheckPositions(context.Background())

	sells := exec.GetSellCalls()
	if len(sells) != 1 {
		t.Fatalf("expected 1 sell, got %d", len(sells))
	}
	if sells[0].AmountPct != 25 {
		t.Errorf("tranche-1 should sell 25%%, got %.0f%%", sells[0].AmountPct)
	}
}

func TestEvaluateExit_Tranche2(t *testing.T) {
	clk := clock.NewSimClock(time.Now())
	mon, store, exec, _ := setupMonitor(clk)

	store.AddPosition(&state.Position{
		ID: "p1", Chain: state.ChainSolana, TokenAddress: "addr1", TokenSymbol: "TEST",
		EntryPrice: 1.0, CurrentPrice: 5.0, PeakPrice: 5.0, SoldPct: 25, EntryTime: clk.Now(),
	})

	mon.CheckPositions(context.Background())

	sells := exec.GetSellCalls()
	if len(sells) != 1 {
		t.Fatalf("expected 1 sell, got %d", len(sells))
	}
	if sells[0].AmountPct != 50 {
		t.Errorf("tranche-2 should sell 50%%, got %.0f%%", sells[0].AmountPct)
	}
}

func TestEvaluateExit_TrailingStop(t *testing.T) {
	clk := clock.NewSimClock(time.Now())
	mon, store, exec, _ := setupMonitor(clk)

	// Peak was 10x, now dropped 40% from peak to 6x. Tranches 1+2 already done (75%).
	store.AddPosition(&state.Position{
		ID: "p1", Chain: state.ChainSolana, TokenAddress: "addr1", TokenSymbol: "TEST",
		EntryPrice: 1.0, CurrentPrice: 6.0, PeakPrice: 10.0, SoldPct: 75, EntryTime: clk.Now(),
	})

	mon.CheckPositions(context.Background())

	sells := exec.GetSellCalls()
	if len(sells) != 1 {
		t.Fatalf("expected 1 sell for trailing stop, got %d", len(sells))
	}
	if sells[0].AmountPct != 25 {
		t.Errorf("trailing stop should sell remaining 25%%, got %.0f%%", sells[0].AmountPct)
	}
}

func TestEvaluateExit_TrailingNotBeforeTranches(t *testing.T) {
	clk := clock.NewSimClock(time.Now())
	mon, store, exec, _ := setupMonitor(clk)

	// Price dropped 40% from peak but tranches not completed yet (SoldPct=25)
	store.AddPosition(&state.Position{
		ID: "p1", Chain: state.ChainSolana, TokenAddress: "addr1", TokenSymbol: "TEST",
		EntryPrice: 1.0, CurrentPrice: 6.0, PeakPrice: 10.0, SoldPct: 25, EntryTime: clk.Now(),
	})

	mon.CheckPositions(context.Background())

	sells := exec.GetSellCalls()
	// Should trigger tranche-2 (5x met, SoldPct=25 < 75), NOT trailing stop
	if len(sells) != 1 {
		t.Fatalf("expected 1 sell, got %d", len(sells))
	}
	if sells[0].AmountPct != 50 {
		t.Errorf("should trigger tranche-2, not trailing stop, got %.0f%%", sells[0].AmountPct)
	}
}

func TestEvaluateExit_StalePosition(t *testing.T) {
	start := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	clk := clock.NewSimClock(start)
	mon, store, exec, _ := setupMonitor(clk)

	store.AddPosition(&state.Position{
		ID: "p1", Chain: state.ChainSolana, TokenAddress: "addr1", TokenSymbol: "TEST",
		EntryPrice: 1.0, CurrentPrice: 1.3, PeakPrice: 1.3, SoldPct: 0, EntryTime: start,
	})

	// Advance 31 minutes
	clk.Advance(31 * time.Minute)
	mon.CheckPositions(context.Background())

	sells := exec.GetSellCalls()
	if len(sells) != 1 {
		t.Fatalf("expected 1 sell for stale position, got %d", len(sells))
	}
	if sells[0].AmountPct != 100 {
		t.Errorf("stale exit should sell 100%%, got %.0f%%", sells[0].AmountPct)
	}
}

func TestEvaluateExit_StaleNotIfProfitable(t *testing.T) {
	start := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	clk := clock.NewSimClock(start)
	mon, store, exec, _ := setupMonitor(clk)

	store.AddPosition(&state.Position{
		ID: "p1", Chain: state.ChainSolana, TokenAddress: "addr1", TokenSymbol: "TEST",
		EntryPrice: 1.0, CurrentPrice: 1.6, PeakPrice: 1.6, SoldPct: 0, EntryTime: start,
	})

	clk.Advance(31 * time.Minute)
	mon.CheckPositions(context.Background())

	sells := exec.GetSellCalls()
	if len(sells) != 0 {
		t.Errorf("profitable (1.6x) stale position should NOT be sold, got %d sells", len(sells))
	}
}

func TestEvaluateExit_PriorityOrder(t *testing.T) {
	clk := clock.NewSimClock(time.Now())
	mon, store, exec, _ := setupMonitor(clk)

	// Price at 0.4x — should stop-loss (not stale or anything else)
	store.AddPosition(&state.Position{
		ID: "p1", Chain: state.ChainSolana, TokenAddress: "addr1", TokenSymbol: "TEST",
		EntryPrice: 1.0, CurrentPrice: 0.4, PeakPrice: 1.0, SoldPct: 0,
		EntryTime: clk.Now().Add(-31 * time.Minute),
	})

	mon.CheckPositions(context.Background())

	sells := exec.GetSellCalls()
	if len(sells) != 1 {
		t.Fatalf("expected 1 sell, got %d", len(sells))
	}
	if sells[0].AmountPct != 100 {
		t.Errorf("stop-loss should sell 100%%, got %.0f%%", sells[0].AmountPct)
	}
}

func TestEvaluateExit_ZeroPrices(t *testing.T) {
	clk := clock.NewSimClock(time.Now())
	mon, store, exec, _ := setupMonitor(clk)

	store.AddPosition(&state.Position{
		ID: "p1", Chain: state.ChainSolana, TokenAddress: "addr1", TokenSymbol: "TEST",
		EntryPrice: 0, CurrentPrice: 0, PeakPrice: 0, EntryTime: clk.Now(),
	})

	mon.CheckPositions(context.Background())

	sells := exec.GetSellCalls()
	if len(sells) != 0 {
		t.Errorf("zero prices should not trigger any sell, got %d", len(sells))
	}
}

func TestEvaluateExit_PeakUpdate(t *testing.T) {
	clk := clock.NewSimClock(time.Now())
	mon, store, _, _ := setupMonitor(clk)

	store.AddPosition(&state.Position{
		ID: "p1", Chain: state.ChainSolana, TokenAddress: "addr1", TokenSymbol: "TEST",
		EntryPrice: 1.0, CurrentPrice: 1.5, PeakPrice: 1.2, EntryTime: clk.Now(),
	})

	mon.CheckPositions(context.Background())

	pos, _ := store.GetPosition("p1")
	if pos.PeakPrice != 1.5 {
		t.Errorf("peak should be updated to 1.5, got %f", pos.PeakPrice)
	}
}
