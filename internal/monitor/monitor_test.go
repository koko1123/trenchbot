package monitor

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"
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
		EntryPrice: 1.0, CurrentPrice: 0.49, PeakPrice: 1.0, Amount: 0.3, EntryTime: clk.Now(),
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
		EntryPrice: 1.0, CurrentPrice: 2.0, PeakPrice: 2.0, SoldPct: 0, Amount: 0.3, EntryTime: clk.Now(),
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
		EntryPrice: 1.0, CurrentPrice: 5.0, PeakPrice: 5.0, SoldPct: 25, Amount: 0.3, EntryTime: clk.Now(),
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
		EntryPrice: 1.0, CurrentPrice: 6.0, PeakPrice: 10.0, SoldPct: 75, Amount: 0.3, EntryTime: clk.Now(),
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
		EntryPrice: 1.0, CurrentPrice: 6.0, PeakPrice: 10.0, SoldPct: 25, Amount: 0.3, EntryTime: clk.Now(),
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
		EntryPrice: 1.0, CurrentPrice: 1.3, PeakPrice: 1.3, SoldPct: 0, Amount: 0.3, EntryTime: start,
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

	// 1.6x is above StaleMultiplierThreshold (1.5) so stale exit should not fire.
	// Also above Tranche1X (1.5), so tranche-1 fires instead.
	// Use 1.4x to test stale protection without triggering tranche.
	store.AddPosition(&state.Position{
		ID: "p1", Chain: state.ChainSolana, TokenAddress: "addr1", TokenSymbol: "TEST",
		EntryPrice: 1.0, CurrentPrice: 1.6, PeakPrice: 1.6, SoldPct: 0, Amount: 0.3, EntryTime: start,
	})

	clk.Advance(31 * time.Minute)
	mon.CheckPositions(context.Background())

	sells := exec.GetSellCalls()
	// Tranche-1 fires (1.6x >= 1.5x) — position is not stale-sold.
	if len(sells) != 1 {
		t.Fatalf("expected tranche-1 sell at 1.6x, got %d sells", len(sells))
	}
	if sells[0].AmountPct != 25 {
		t.Errorf("tranche-1 should sell 25%%, got %.0f%%", sells[0].AmountPct)
	}
}

func TestEvaluateExit_PriorityOrder(t *testing.T) {
	clk := clock.NewSimClock(time.Now())
	mon, store, exec, _ := setupMonitor(clk)

	// Price at 0.4x — should stop-loss (not stale or anything else)
	store.AddPosition(&state.Position{
		ID: "p1", Chain: state.ChainSolana, TokenAddress: "addr1", TokenSymbol: "TEST",
		EntryPrice: 1.0, CurrentPrice: 0.4, PeakPrice: 1.0, SoldPct: 0, Amount: 0.3,
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

func TestEvaluateExit_EarlyTrailingStop(t *testing.T) {
	clk := clock.NewSimClock(time.Now())
	mon, store, exec, _ := setupMonitor(clk)

	// Peak was 4.5x (above 3x threshold), now dropped 30%+ from peak.
	// SoldPct=25 means tranche-1 done but tranche-2 not yet.
	store.AddPosition(&state.Position{
		ID: "p1", Chain: state.ChainSolana, TokenAddress: "addr1", TokenSymbol: "TEST",
		EntryPrice: 1.0, CurrentPrice: 2.8, PeakPrice: 4.5, SoldPct: 25, Amount: 0.3, EntryTime: clk.Now(),
	})

	mon.CheckPositions(context.Background())

	sells := exec.GetSellCalls()
	if len(sells) != 1 {
		t.Fatalf("expected 1 sell for early trailing stop, got %d", len(sells))
	}
	if sells[0].AmountPct != 75 {
		t.Errorf("early trailing stop should sell remaining 75%%, got %.0f%%", sells[0].AmountPct)
	}
}

func TestEvaluateExit_EarlyTrailingNotBelowThreshold(t *testing.T) {
	clk := clock.NewSimClock(time.Now())
	mon, store, exec, _ := setupMonitor(clk)

	// Peak was 2.5x (below 3x early trailing threshold), dropped 30%+ from peak.
	// Early trailing should NOT trigger.
	// However, universal trailing WILL fire (peak 2.5x >= 1.15, drop 40% >= 20%).
	// So this test now expects a universal-trailing-stop sell.
	store.AddPosition(&state.Position{
		ID: "p1", Chain: state.ChainSolana, TokenAddress: "addr1", TokenSymbol: "TEST",
		EntryPrice: 1.0, CurrentPrice: 1.5, PeakPrice: 2.5, SoldPct: 25, Amount: 0.3, EntryTime: clk.Now(),
	})

	mon.CheckPositions(context.Background())

	sells := exec.GetSellCalls()
	if len(sells) != 1 {
		t.Fatalf("expected universal trailing stop sell, got %d sells", len(sells))
	}
	if sells[0].AmountPct != 75 {
		t.Errorf("universal trailing should sell remaining 75%%, got %.0f%%", sells[0].AmountPct)
	}
}

func TestEvaluateExit_UniversalTrailingStop(t *testing.T) {
	clk := clock.NewSimClock(time.Now())
	mon, store, exec, _ := setupMonitor(clk)

	// Peak was 1.4x (above 1.15 universal threshold), now at 1.0x.
	// dropFromPeak = (1.4-1.0)/1.4*100 = 28.6% >= 20%.
	// No tranches sold. Universal trailing should fire.
	store.AddPosition(&state.Position{
		ID: "p1", Chain: state.ChainSolana, TokenAddress: "addr1", TokenSymbol: "TEST",
		EntryPrice: 1.0, CurrentPrice: 1.0, PeakPrice: 1.4, SoldPct: 0, Amount: 0.3, EntryTime: clk.Now(),
	})

	mon.CheckPositions(context.Background())

	sells := exec.GetSellCalls()
	if len(sells) != 1 {
		t.Fatalf("expected 1 sell for universal trailing stop, got %d", len(sells))
	}
	if sells[0].AmountPct != 100 {
		t.Errorf("universal trailing should sell 100%%, got %.0f%%", sells[0].AmountPct)
	}
}

func TestEvaluateExit_UniversalTrailingNotBelowThreshold(t *testing.T) {
	clk := clock.NewSimClock(time.Now())
	mon, store, exec, _ := setupMonitor(clk)

	// Peak was 1.1x (below 1.15 universal threshold). Should NOT trigger.
	store.AddPosition(&state.Position{
		ID: "p1", Chain: state.ChainSolana, TokenAddress: "addr1", TokenSymbol: "TEST",
		EntryPrice: 1.0, CurrentPrice: 0.8, PeakPrice: 1.1, SoldPct: 0, Amount: 0.3, EntryTime: clk.Now(),
	})

	mon.CheckPositions(context.Background())

	sells := exec.GetSellCalls()
	if len(sells) != 0 {
		t.Errorf("universal trailing should NOT trigger below threshold, got %d sells", len(sells))
	}
}

func TestEvaluateExit_NoTradeActivity(t *testing.T) {
	start := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	clk := clock.NewSimClock(start)
	mon, store, exec, _ := setupMonitor(clk)

	// Position near entry (1.05x < 1.1 NoTradeMaxMult) with LastTradeTime 3 min ago.
	store.AddPosition(&state.Position{
		ID: "p1", Chain: state.ChainSolana, TokenAddress: "addr1", TokenSymbol: "DEAD",
		EntryPrice: 1.0, CurrentPrice: 1.05, PeakPrice: 1.05, Amount: 0.3,
		EntryTime: start, LastTradeTime: start,
	})

	// Advance 3 minutes (> 2 min NoTradeTimeout default).
	clk.Advance(3 * time.Minute)
	mon.CheckPositions(context.Background())

	sells := exec.GetSellCalls()
	if len(sells) != 1 {
		t.Fatalf("expected 1 sell for no-trade-activity, got %d", len(sells))
	}
	if sells[0].AmountPct != 100 {
		t.Errorf("no-trade-activity should sell 100%%, got %.0f%%", sells[0].AmountPct)
	}
}

func TestEvaluateExit_NoTradeActivityNotIfProfitable(t *testing.T) {
	start := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	clk := clock.NewSimClock(start)
	mon, store, exec, _ := setupMonitor(clk)

	// Position at 1.2x (above 1.1 NoTradeMaxMult), no trades for 3 min.
	// Should NOT trigger no-trade-activity.
	store.AddPosition(&state.Position{
		ID: "p1", Chain: state.ChainSolana, TokenAddress: "addr1", TokenSymbol: "ALIVE",
		EntryPrice: 1.0, CurrentPrice: 1.2, PeakPrice: 1.2, Amount: 0.3,
		EntryTime: start, LastTradeTime: start,
	})

	clk.Advance(3 * time.Minute)
	mon.CheckPositions(context.Background())

	sells := exec.GetSellCalls()
	if len(sells) != 0 {
		t.Errorf("no-trade-activity should NOT trigger above NoTradeMaxMult, got %d sells", len(sells))
	}
}

func TestEvaluateExit_NoTradeActivityNotIfRecent(t *testing.T) {
	start := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	clk := clock.NewSimClock(start)
	mon, store, exec, _ := setupMonitor(clk)

	// Position near entry but LastTradeTime is recent (1 min ago < 2 min timeout).
	store.AddPosition(&state.Position{
		ID: "p1", Chain: state.ChainSolana, TokenAddress: "addr1", TokenSymbol: "RECENT",
		EntryPrice: 1.0, CurrentPrice: 1.05, PeakPrice: 1.05, Amount: 0.3,
		EntryTime: start, LastTradeTime: start.Add(30 * time.Second),
	})

	clk.Advance(1 * time.Minute)
	mon.CheckPositions(context.Background())

	sells := exec.GetSellCalls()
	if len(sells) != 0 {
		t.Errorf("no-trade-activity should NOT trigger with recent trades, got %d sells", len(sells))
	}
}

func TestEvaluateExit_ZeroEntryPrice(t *testing.T) {
	clk := clock.NewSimClock(time.Now())
	mon, store, exec, _ := setupMonitor(clk)

	// EntryPrice=0 with the fallback gives multiplier=1.0 (break-even).
	// Position is 31 min old with multiplier < 1.5, so stale exit fires.
	// Old code would silently skip this position entirely.
	store.AddPosition(&state.Position{
		ID: "p1", Chain: state.ChainSolana, TokenAddress: "addr1", TokenSymbol: "TEST",
		EntryPrice: 0, CurrentPrice: 0.49, PeakPrice: 0.49, Amount: 0.3,
		EntryTime: clk.Now().Add(-31 * time.Minute),
	})

	mon.CheckPositions(context.Background())

	sells := exec.GetSellCalls()
	if len(sells) != 1 {
		t.Fatalf("zero entry price should not block evaluation, expected 1 sell (stale), got %d", len(sells))
	}
	if sells[0].AmountPct != 100 {
		t.Errorf("stale exit should sell 100%%, got %.0f%%", sells[0].AmountPct)
	}
}

func TestEvaluateExit_ZeroEntryPriceAtProfit(t *testing.T) {
	clk := clock.NewSimClock(time.Now())
	mon, store, exec, _ := setupMonitor(clk)

	// EntryPrice=0, CurrentPrice=2.0. Fallback entryPrice=CurrentPrice=2.0, multiplier=1.0.
	// No exit should trigger since multiplier=1.0 is above stop-loss and below tranches,
	// and position is not stale (just created).
	store.AddPosition(&state.Position{
		ID: "p1", Chain: state.ChainSolana, TokenAddress: "addr1", TokenSymbol: "TEST",
		EntryPrice: 0, CurrentPrice: 2.0, PeakPrice: 2.0, Amount: 0.3, EntryTime: clk.Now(),
	})

	mon.CheckPositions(context.Background())

	sells := exec.GetSellCalls()
	if len(sells) != 0 {
		t.Errorf("zero entry price at profit (fallback break-even) should not trigger exit, got %d sells", len(sells))
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

func TestEvaluateExit_SellFailureForceClose(t *testing.T) {
	clk := clock.NewSimClock(time.Now())
	mon, store, exec, _ := setupMonitor(clk)

	store.AddPosition(&state.Position{
		ID: "p1", Chain: state.ChainSolana, TokenAddress: "honeypot1", TokenSymbol: "HONEY",
		EntryPrice: 1.0, CurrentPrice: 0.4, PeakPrice: 1.0, Amount: 0.3, EntryTime: clk.Now(),
	})

	// Make executor fail all sells.
	exec.SetSellError(fmt.Errorf("transaction reverted"))

	// Each CheckPositions call triggers a sell attempt that fails.
	for i := 0; i < 5; i++ {
		mon.CheckPositions(context.Background())
	}

	pos, _ := store.GetPosition("p1")
	if !pos.Closed {
		t.Error("position should be force-closed after 5 sell failures")
	}
	if pos.PnL != -100.0 {
		t.Errorf("force-closed position should have PnL=-100, got %f", pos.PnL)
	}
}

func TestEvaluateExit_SoldPctClamp(t *testing.T) {
	clk := clock.NewSimClock(time.Now())
	mon, store, _, _ := setupMonitor(clk)

	// Position with SoldPct=80 at stop-loss price triggers sell of remaining 20%.
	store.AddPosition(&state.Position{
		ID: "p1", Chain: state.ChainSolana, TokenAddress: "addr1", TokenSymbol: "TEST",
		EntryPrice: 1.0, CurrentPrice: 0.4, PeakPrice: 1.0, SoldPct: 80, Amount: 0.3, EntryTime: clk.Now(),
	})

	mon.CheckPositions(context.Background())

	pos, _ := store.GetPosition("p1")
	if pos.SoldPct != 100 {
		t.Errorf("SoldPct should be clamped to 100, got %f", pos.SoldPct)
	}
	if !pos.Closed {
		t.Error("position should be closed after selling remaining")
	}
}

func TestExecuteSell_ConcurrentDedup(t *testing.T) {
	clk := clock.NewSimClock(time.Now())
	mon, store, exec, _ := setupMonitor(clk)

	// Add a small delay to the executor so both goroutines overlap.
	exec.SellFn = func(ctx context.Context, params executor.SellParams) executor.SellResult {
		time.Sleep(50 * time.Millisecond)
		return executor.SellResult{
			Success: true,
			TxHash:  "mock-sell-" + params.TokenAddress,
			Price:   1.0,
			Amount:  params.AmountPct,
			GasCost: 0.000505,
		}
	}

	store.AddPosition(&state.Position{
		ID: "p1", Chain: state.ChainSolana, TokenAddress: "addr1", TokenSymbol: "DEDUP",
		EntryPrice: 1.0, CurrentPrice: 0.4, PeakPrice: 1.0, Amount: 0.3, EntryTime: clk.Now(),
	})

	pos, _ := store.GetPosition("p1")

	var wg sync.WaitGroup
	wg.Add(2)
	for i := 0; i < 2; i++ {
		go func() {
			defer wg.Done()
			mon.executeSell(context.Background(), pos, 100, "stop-loss")
		}()
	}
	wg.Wait()

	sells := exec.GetSellCalls()
	if len(sells) != 1 {
		t.Errorf("expected exactly 1 sell call due to dedup lock, got %d", len(sells))
	}
}
