package flow

import "sync"

// BotDB tracks addresses that appear repeatedly in first-block buys across tokens.
// Addresses seen in early buys on threshold+ different tokens are flagged as bots.
type BotDB struct {
	mu        sync.RWMutex
	sightings map[string]int // address -> count of tokens where seen in early buys
	threshold int            // sightings needed to be flagged (default 5)
}

// NewBotDB creates a new BotDB with the given threshold.
// If threshold <= 0, defaults to 5.
func NewBotDB(threshold int) *BotDB {
	if threshold <= 0 {
		threshold = 5
	}
	return &BotDB{
		sightings: make(map[string]int),
		threshold: threshold,
	}
}

// RecordEarlyBuyer increments the sighting count for the given address.
func (db *BotDB) RecordEarlyBuyer(address string) {
	db.mu.Lock()
	defer db.mu.Unlock()
	db.sightings[address]++
}

// IsKnownBot returns true if the address has been seen in early buys on
// at least threshold different tokens.
func (db *BotDB) IsKnownBot(address string) bool {
	db.mu.RLock()
	defer db.mu.RUnlock()
	return db.sightings[address] >= db.threshold
}

// KnownBots returns all addresses that have been flagged as bots.
func (db *BotDB) KnownBots() []string {
	db.mu.RLock()
	defer db.mu.RUnlock()
	var bots []string
	for addr, count := range db.sightings {
		if count >= db.threshold {
			bots = append(bots, addr)
		}
	}
	return bots
}

// Count returns the number of addresses currently flagged as bots.
func (db *BotDB) Count() int {
	db.mu.RLock()
	defer db.mu.RUnlock()
	count := 0
	for _, c := range db.sightings {
		if c >= db.threshold {
			count++
		}
	}
	return count
}
