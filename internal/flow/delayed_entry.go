package flow

import (
	"sync"
	"time"
)

// DelayedEntry tracks tokens that are waiting for a post-bot-dump re-evaluation.
// Speed bots dump within 30-120s of buying. When bot activity is detected during
// the observation window, we schedule a delayed re-check so we can buy at a
// better price after the dump wave.
type DelayedEntry struct {
	mu      sync.Mutex
	pending map[string]*delayedToken // mint -> pending re-check
	delay   time.Duration            // how long to wait (default 90s)
}

type delayedToken struct {
	Mint       string
	ScheduleAt time.Time   // when to re-evaluate
	Original   interface{} // original token data (stored as interface for flexibility)
}

// NewDelayedEntry creates a new DelayedEntry with the given delay duration.
func NewDelayedEntry(delay time.Duration) *DelayedEntry {
	return &DelayedEntry{
		pending: make(map[string]*delayedToken),
		delay:   delay,
	}
}

// Schedule adds a token to the delayed entry queue.
// Returns true if scheduled, false if already pending.
func (de *DelayedEntry) Schedule(mint string, original interface{}) bool {
	de.mu.Lock()
	defer de.mu.Unlock()

	if _, exists := de.pending[mint]; exists {
		return false
	}

	de.pending[mint] = &delayedToken{
		Mint:       mint,
		ScheduleAt: time.Now().Add(de.delay),
		Original:   original,
	}
	return true
}

// Ready returns all tokens that are past their delay and ready for re-evaluation.
// Removes them from the pending map.
func (de *DelayedEntry) Ready() []delayedToken {
	de.mu.Lock()
	defer de.mu.Unlock()

	now := time.Now()
	var ready []delayedToken

	for mint, dt := range de.pending {
		if now.After(dt.ScheduleAt) || now.Equal(dt.ScheduleAt) {
			ready = append(ready, *dt)
			delete(de.pending, mint)
		}
	}

	return ready
}

// Cancel removes a token from the pending queue (e.g., if it was bought elsewhere).
func (de *DelayedEntry) Cancel(mint string) {
	de.mu.Lock()
	defer de.mu.Unlock()
	delete(de.pending, mint)
}

// Count returns the number of pending delayed entries.
func (de *DelayedEntry) Count() int {
	de.mu.Lock()
	defer de.mu.Unlock()
	return len(de.pending)
}
