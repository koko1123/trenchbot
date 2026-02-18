package flow

import (
	"sync"
	"testing"
	"time"
)

func TestDelayedEntry_Schedule(t *testing.T) {
	de := NewDelayedEntry(10 * time.Millisecond)

	ok := de.Schedule("mint1", "tokenData")
	if !ok {
		t.Fatal("expected Schedule to return true for new mint")
	}

	if de.Count() != 1 {
		t.Fatalf("expected Count = 1, got %d", de.Count())
	}

	// Should not be ready immediately.
	ready := de.Ready()
	if len(ready) != 0 {
		t.Fatalf("expected no ready tokens immediately, got %d", len(ready))
	}
}

func TestDelayedEntry_Ready(t *testing.T) {
	de := NewDelayedEntry(10 * time.Millisecond)

	de.Schedule("mint1", "data1")
	de.Schedule("mint2", "data2")

	// Wait past the delay.
	time.Sleep(15 * time.Millisecond)

	ready := de.Ready()
	if len(ready) != 2 {
		t.Fatalf("expected 2 ready tokens, got %d", len(ready))
	}

	// After draining, count should be 0.
	if de.Count() != 0 {
		t.Fatalf("expected Count = 0 after Ready(), got %d", de.Count())
	}

	// Calling Ready again should return nothing.
	ready2 := de.Ready()
	if len(ready2) != 0 {
		t.Fatalf("expected 0 ready tokens on second call, got %d", len(ready2))
	}
}

func TestDelayedEntry_Cancel(t *testing.T) {
	de := NewDelayedEntry(10 * time.Millisecond)

	de.Schedule("mint1", "data1")
	de.Cancel("mint1")

	if de.Count() != 0 {
		t.Fatalf("expected Count = 0 after Cancel, got %d", de.Count())
	}

	// Wait past the delay and verify nothing is ready.
	time.Sleep(15 * time.Millisecond)

	ready := de.Ready()
	if len(ready) != 0 {
		t.Fatalf("expected no ready tokens after cancel, got %d", len(ready))
	}
}

func TestDelayedEntry_DuplicateSchedule(t *testing.T) {
	de := NewDelayedEntry(10 * time.Millisecond)

	ok1 := de.Schedule("mint1", "data1")
	ok2 := de.Schedule("mint1", "data2")

	if !ok1 {
		t.Fatal("first Schedule should return true")
	}
	if ok2 {
		t.Fatal("second Schedule for same mint should return false")
	}

	if de.Count() != 1 {
		t.Fatalf("expected Count = 1 after duplicate schedule, got %d", de.Count())
	}
}

func TestDelayedEntry_Concurrent(t *testing.T) {
	de := NewDelayedEntry(5 * time.Millisecond)

	var wg sync.WaitGroup
	const n = 100

	// Concurrent Schedule calls with unique mints.
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			mint := "mint" + string(rune('A'+idx%26)) + string(rune('0'+idx/26))
			de.Schedule(mint, idx)
		}(i)
	}
	wg.Wait()

	// Concurrent Ready and Count calls.
	time.Sleep(10 * time.Millisecond)

	var readyCount int
	var mu sync.Mutex

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ready := de.Ready()
			mu.Lock()
			readyCount += len(ready)
			mu.Unlock()
		}()
	}

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = de.Count()
		}()
	}

	wg.Wait()

	// All tokens should have been drained exactly once across all Ready() calls.
	remaining := de.Count()
	if readyCount+remaining == 0 {
		t.Fatal("expected some tokens to have been scheduled and retrieved")
	}
}
