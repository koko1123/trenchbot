package scanner

import (
	"context"
	"time"

	"github.com/cindocode/trenchbot/internal/state"
)

type NewToken struct {
	Chain        state.Chain
	Address      string
	Name         string
	Symbol       string
	Description  string
	ImageURL     string
	Creator      string
	Timestamp    time.Time
	InitialBuys  int
	MarketCapUSD float64
	Metadata     map[string]interface{}
}

type Scanner interface {
	// Scan starts scanning for new tokens and sends them to the channel.
	// It blocks until the context is cancelled.
	Scan(ctx context.Context, out chan<- NewToken) error

	// Chain returns which chain this scanner monitors.
	Chain() state.Chain
}
