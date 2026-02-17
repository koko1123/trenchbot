package clock

import (
	"sync"
	"time"
)

type Clock interface {
	Now() time.Time
	Since(t time.Time) time.Duration
}

// RealClock uses the system clock.
type RealClock struct{}

func (RealClock) Now() time.Time                  { return time.Now() }
func (RealClock) Since(t time.Time) time.Duration { return time.Since(t) }

// SimClock is a manually-controlled clock for testing and simulation.
type SimClock struct {
	mu      sync.RWMutex
	current time.Time
}

func NewSimClock(start time.Time) *SimClock {
	return &SimClock{current: start}
}

func (c *SimClock) Now() time.Time {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.current
}

func (c *SimClock) Since(t time.Time) time.Duration {
	return c.Now().Sub(t)
}

func (c *SimClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.current = c.current.Add(d)
}

func (c *SimClock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.current = t
}
