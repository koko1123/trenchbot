package flow

import (
	"sync"
	"testing"
)

func TestBotDB_ThresholdTracking(t *testing.T) {
	db := NewBotDB(5)

	addr := "BotAddr1111111111111111111111111111111111111"

	// Record 4 sightings: should NOT be a bot yet.
	for i := 0; i < 4; i++ {
		db.RecordEarlyBuyer(addr)
	}
	if db.IsKnownBot(addr) {
		t.Fatalf("expected address not to be flagged as bot after 4 sightings")
	}
	if db.Count() != 0 {
		t.Fatalf("expected 0 bots, got %d", db.Count())
	}

	// 5th sighting: should now be a bot.
	db.RecordEarlyBuyer(addr)
	if !db.IsKnownBot(addr) {
		t.Fatalf("expected address to be flagged as bot after 5 sightings")
	}
	if db.Count() != 1 {
		t.Fatalf("expected 1 bot, got %d", db.Count())
	}

	bots := db.KnownBots()
	if len(bots) != 1 || bots[0] != addr {
		t.Fatalf("expected KnownBots to return [%s], got %v", addr, bots)
	}
}

func TestBotDB_UnknownAddressNotBot(t *testing.T) {
	db := NewBotDB(5)
	if db.IsKnownBot("unknown") {
		t.Fatal("unknown address should not be a bot")
	}
}

func TestBotDB_DefaultThreshold(t *testing.T) {
	db := NewBotDB(0)
	if db.threshold != 5 {
		t.Fatalf("expected default threshold 5, got %d", db.threshold)
	}
}

func TestBotDB_ConcurrentAccess(t *testing.T) {
	db := NewBotDB(5)
	addr := "ConcurrentAddr1111111111111111111111111111"

	var wg sync.WaitGroup
	// 10 goroutines each record 1 sighting.
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			db.RecordEarlyBuyer(addr)
		}()
	}
	wg.Wait()

	if !db.IsKnownBot(addr) {
		t.Fatal("expected address to be flagged as bot after 10 concurrent sightings")
	}

	// Concurrent reads should not race.
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			db.IsKnownBot(addr)
			db.KnownBots()
			db.Count()
		}()
	}
	wg.Wait()
}
