package state

import (
	"sync"
	"testing"
	"time"
)

func TestStore_AddAndGet(t *testing.T) {
	s := NewStore()
	p := &Position{ID: "p1", Chain: ChainSolana, TokenSymbol: "TEST", EntryPrice: 1.0}
	s.AddPosition(p)

	got, ok := s.GetPosition("p1")
	if !ok {
		t.Fatal("position not found")
	}
	if got.TokenSymbol != "TEST" {
		t.Errorf("got symbol %q, want TEST", got.TokenSymbol)
	}
}

func TestStore_GetPosition_NotFound(t *testing.T) {
	s := NewStore()
	_, ok := s.GetPosition("nonexistent")
	if ok {
		t.Error("should not find nonexistent position")
	}
}

func TestStore_UpdatePosition(t *testing.T) {
	s := NewStore()
	s.AddPosition(&Position{ID: "p1", CurrentPrice: 1.0})

	s.UpdatePosition("p1", func(p *Position) {
		p.CurrentPrice = 5.0
	})

	got, _ := s.GetPosition("p1")
	if got.CurrentPrice != 5.0 {
		t.Errorf("price not updated: got %f", got.CurrentPrice)
	}
}

func TestStore_OpenPositions(t *testing.T) {
	s := NewStore()
	s.AddPosition(&Position{ID: "p1", Chain: ChainSolana, Closed: false})
	s.AddPosition(&Position{ID: "p2", Chain: ChainSolana, Closed: true})
	s.AddPosition(&Position{ID: "p3", Chain: ChainBNB, Closed: false})

	sol := s.OpenPositions(ChainSolana)
	if len(sol) != 1 {
		t.Errorf("expected 1 open solana position, got %d", len(sol))
	}
	if s.OpenPositionCount(ChainSolana) != 1 {
		t.Errorf("OpenPositionCount mismatch")
	}
	if s.TotalOpenPositionCount() != 2 {
		t.Errorf("expected 2 total open positions, got %d", s.TotalOpenPositionCount())
	}
}

func TestStore_DailyPnL(t *testing.T) {
	s := NewStore()
	s.UpdateDailyPnL(ChainSolana, -10.0)
	s.UpdateDailyPnL(ChainSolana, -5.0)

	if got := s.GetDailyPnL(ChainSolana); got != -15.0 {
		t.Errorf("daily pnl = %f, want -15", got)
	}

	s.ResetDailyPnL()
	if got := s.GetDailyPnL(ChainSolana); got != 0 {
		t.Errorf("after reset, daily pnl = %f, want 0", got)
	}
}

func TestStore_PeakEquity(t *testing.T) {
	s := NewStore()
	s.SetPeakEquity(ChainSolana, 1000)
	s.SetPeakEquity(ChainSolana, 1200)
	s.SetPeakEquity(ChainSolana, 900) // should not decrease

	if got := s.GetPeakEquity(ChainSolana); got != 1200 {
		t.Errorf("peak equity = %f, want 1200", got)
	}
}

func TestStore_RecentTrades(t *testing.T) {
	s := NewStore()
	old := time.Now().Add(-2 * time.Hour)
	recent := time.Now().Add(-5 * time.Minute)

	s.AddTrade(Trade{Chain: ChainSolana, Timestamp: old})
	s.AddTrade(Trade{Chain: ChainSolana, Timestamp: recent})
	s.AddTrade(Trade{Chain: ChainBNB, Timestamp: recent})

	trades := s.RecentTrades(ChainSolana, time.Now().Add(-1*time.Hour))
	if len(trades) != 1 {
		t.Errorf("expected 1 recent solana trade, got %d", len(trades))
	}
}

func TestStore_GasBalance(t *testing.T) {
	s := NewStore()
	s.SetGasBalance(ChainSolana, 0.25)

	if got := s.GetGasBalance(ChainSolana); got != 0.25 {
		t.Errorf("gas balance = %f, want 0.25", got)
	}

	s.DeductGas(ChainSolana, 0.000505)
	if got := s.GetGasBalance(ChainSolana); got < 0.249 || got > 0.2496 {
		t.Errorf("gas balance after deduct = %f, want ~0.2495", got)
	}

	if got := s.GetGasSpent(ChainSolana); got < 0.0005 || got > 0.00051 {
		t.Errorf("gas spent = %f, want ~0.000505", got)
	}

	// Deduct more than balance — should floor at 0.
	s.DeductGas(ChainSolana, 1.0)
	if got := s.GetGasBalance(ChainSolana); got != 0 {
		t.Errorf("gas balance should be 0 after overdraw, got %f", got)
	}
}

func TestStore_ConcurrentAccess(t *testing.T) {
	s := NewStore()
	var wg sync.WaitGroup

	// 50 writers
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			p := &Position{
				ID:    "p" + string(rune('A'+id)),
				Chain: ChainSolana,
			}
			s.AddPosition(p)
			s.UpdateDailyPnL(ChainSolana, 1.0)
			s.SetPeakEquity(ChainSolana, float64(id))
		}(i)
	}

	// 50 readers
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = s.AllOpenPositions()
			_ = s.GetDailyPnL(ChainSolana)
			_ = s.GetPeakEquity(ChainSolana)
			_ = s.TotalOpenPositionCount()
		}()
	}

	wg.Wait()
	// If we get here without panic/race, the test passes
}
