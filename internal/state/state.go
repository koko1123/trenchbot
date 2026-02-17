package state

import (
	"sync"
	"time"
)

type Chain string

const (
	ChainSolana Chain = "solana"
	ChainBNB    Chain = "bnb"
)

type Position struct {
	ID            string
	Chain         Chain
	TokenAddress  string
	TokenSymbol   string
	EntryPrice    float64
	CurrentPrice  float64
	PeakPrice     float64
	Amount        float64
	EntryTime     time.Time
	SoldPct       float64 // percentage already sold (0-100)
	Closed        bool
	PnL           float64
}

type Trade struct {
	ID           string
	Chain        Chain
	TokenAddress string
	TokenSymbol  string
	Side         string // "buy" or "sell"
	Price        float64
	Amount       float64
	Timestamp    time.Time
	TxHash       string
	Shadow       bool
}

type Store struct {
	mu         sync.RWMutex
	positions  map[string]*Position // ID -> Position
	trades     []Trade
	dailyPnL   map[Chain]float64
	peakEquity map[Chain]float64
}

func NewStore() *Store {
	return &Store{
		positions:  make(map[string]*Position),
		dailyPnL:   make(map[Chain]float64),
		peakEquity: make(map[Chain]float64),
	}
}

func (s *Store) AddPosition(p *Position) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.positions[p.ID] = p
}

func (s *Store) GetPosition(id string) (*Position, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.positions[id]
	return p, ok
}

func (s *Store) UpdatePosition(id string, fn func(p *Position)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p, ok := s.positions[id]; ok {
		fn(p)
	}
}

func (s *Store) OpenPositions(chain Chain) []*Position {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var result []*Position
	for _, p := range s.positions {
		if p.Chain == chain && !p.Closed {
			result = append(result, p)
		}
	}
	return result
}

func (s *Store) AllOpenPositions() []*Position {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var result []*Position
	for _, p := range s.positions {
		if !p.Closed {
			result = append(result, p)
		}
	}
	return result
}

func (s *Store) OpenPositionCount(chain Chain) int {
	return len(s.OpenPositions(chain))
}

func (s *Store) TotalOpenPositionCount() int {
	return len(s.AllOpenPositions())
}

func (s *Store) AddTrade(t Trade) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.trades = append(s.trades, t)
}

func (s *Store) RecentTrades(chain Chain, since time.Time) []Trade {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var result []Trade
	for _, t := range s.trades {
		if t.Chain == chain && t.Timestamp.After(since) {
			result = append(result, t)
		}
	}
	return result
}

func (s *Store) UpdateDailyPnL(chain Chain, delta float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dailyPnL[chain] += delta
}

func (s *Store) GetDailyPnL(chain Chain) float64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.dailyPnL[chain]
}

func (s *Store) ResetDailyPnL() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k := range s.dailyPnL {
		s.dailyPnL[k] = 0
	}
}

func (s *Store) SetPeakEquity(chain Chain, equity float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if equity > s.peakEquity[chain] {
		s.peakEquity[chain] = equity
	}
}

func (s *Store) GetPeakEquity(chain Chain) float64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.peakEquity[chain]
}
