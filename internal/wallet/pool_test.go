package wallet

import (
	"crypto/ed25519"
	"crypto/rand"
	"sync"
	"testing"

	"github.com/mr-tron/base58"
)

// generateTestKey creates a random ed25519 private key and returns it as base58.
func generateTestKey(t *testing.T) string {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return base58.Encode(priv)
}

func TestNewPool_SingleKey(t *testing.T) {
	key := generateTestKey(t)
	pool, err := NewPool([]string{key})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if pool.Count() != 1 {
		t.Errorf("expected 1 wallet, got %d", pool.Count())
	}

	primary := pool.Primary()
	if primary == nil {
		t.Fatal("primary should not be nil")
	}
	if primary.PublicKey == "" {
		t.Error("primary public key should not be empty")
	}

	// With a single key, Next always returns the same wallet.
	next := pool.Next()
	if next.PublicKey != primary.PublicKey {
		t.Errorf("single-key pool: Next() should return primary, got different wallet")
	}

	next2 := pool.Next()
	if next2.PublicKey != primary.PublicKey {
		t.Errorf("single-key pool: second Next() should still return primary")
	}
}

func TestNewPool_MultiKey(t *testing.T) {
	keys := make([]string, 3)
	for i := range keys {
		keys[i] = generateTestKey(t)
	}

	pool, err := NewPool(keys)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if pool.Count() != 3 {
		t.Errorf("expected 3 wallets, got %d", pool.Count())
	}

	// Round-robin should cycle through all 3 wallets.
	seen := make(map[string]int)
	for i := 0; i < 6; i++ {
		w := pool.Next()
		seen[w.PublicKey]++
	}

	if len(seen) != 3 {
		t.Errorf("expected 3 distinct wallets in round-robin, got %d", len(seen))
	}

	for pk, count := range seen {
		if count != 2 {
			t.Errorf("wallet %s should appear 2 times in 6 calls, got %d", pk[:8], count)
		}
	}
}

func TestNewPool_InvalidKey(t *testing.T) {
	_, err := NewPool([]string{"not-a-valid-key"})
	if err == nil {
		t.Fatal("expected error for invalid key, got nil")
	}
}

func TestNewPool_Empty(t *testing.T) {
	_, err := NewPool([]string{})
	if err == nil {
		t.Fatal("expected error for empty keys, got nil")
	}
}

func TestNewPool_Nil(t *testing.T) {
	_, err := NewPool(nil)
	if err == nil {
		t.Fatal("expected error for nil keys, got nil")
	}
}

func TestNext_Concurrent(t *testing.T) {
	keys := make([]string, 4)
	for i := range keys {
		keys[i] = generateTestKey(t)
	}

	pool, err := NewPool(keys)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	const goroutines = 100
	var wg sync.WaitGroup
	var mu sync.Mutex
	seen := make(map[string]int)

	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			w := pool.Next()
			mu.Lock()
			seen[w.PublicKey]++
			mu.Unlock()
		}()
	}
	wg.Wait()

	// All 4 wallets should have been used.
	if len(seen) != 4 {
		t.Errorf("expected 4 distinct wallets from concurrent access, got %d", len(seen))
	}

	// Each wallet should get exactly 25 calls (100 / 4).
	for pk, count := range seen {
		if count != 25 {
			t.Errorf("wallet %s: expected 25 calls, got %d", pk[:8], count)
		}
	}
}

func TestAll_ReturnsCopy(t *testing.T) {
	key := generateTestKey(t)
	pool, err := NewPool([]string{key})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	all := pool.All()
	if len(all) != 1 {
		t.Fatalf("expected 1 wallet, got %d", len(all))
	}

	// Modifying returned slice should not affect the pool.
	all[0] = nil
	fresh := pool.All()
	if fresh[0] == nil {
		t.Error("All() should return a copy, not the original slice")
	}
}
