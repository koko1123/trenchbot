package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
	Amount        float64 // native token amount (SOL or BNB)
	TokenBalance  float64 // raw token count received from buy
	EntryTime     time.Time
	SoldPct       float64 // percentage already sold (0-100)
	Closed        bool
	PnL           float64
	EntryGasCost  float64 // gas paid on buy tx (native token)
	ExitGasCost   float64 // cumulative gas paid on sell tx(s)
	SellFailures  int     // consecutive sell failures
	EntryPriceUSD float64 // USD price at entry (set on first price lookup)
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
	GasCost      float64 // gas cost in native token
}

type Store struct {
	mu            sync.RWMutex
	positions     map[string]*Position // ID -> Position
	trades        []Trade
	dailyPnL      map[Chain]float64
	peakEquity    map[Chain]float64
	gasBalance    map[Chain]float64
	gasSpent      map[Chain]float64
	reservedSlots map[Chain]int // reserved but not yet filled slots
}

func NewStore() *Store {
	return &Store{
		positions:     make(map[string]*Position),
		dailyPnL:      make(map[Chain]float64),
		peakEquity:    make(map[Chain]float64),
		gasBalance:    make(map[Chain]float64),
		gasSpent:      make(map[Chain]float64),
		reservedSlots: make(map[Chain]int),
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

func (s *Store) SetGasBalance(chain Chain, amount float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gasBalance[chain] = amount
}

func (s *Store) GetGasBalance(chain Chain) float64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.gasBalance[chain]
}

func (s *Store) DeductGas(chain Chain, cost float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gasBalance[chain] -= cost
	if s.gasBalance[chain] < 0 {
		s.gasBalance[chain] = 0
	}
	s.gasSpent[chain] += cost
}

func (s *Store) GetGasSpent(chain Chain) float64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.gasSpent[chain]
}

// TryReserveSlot atomically checks if a slot is available and reserves it.
// Returns true if the slot was reserved, false if at the limit.
func (s *Store) TryReserveSlot(chain Chain, maxPerChain, maxTotal int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	chainCount := 0
	totalCount := 0
	for _, p := range s.positions {
		if !p.Closed && p.SoldPct < 75 {
			totalCount++
			if p.Chain == chain {
				chainCount++
			}
		}
	}
	chainCount += s.reservedSlots[chain]
	totalCount += s.totalReservedSlots()

	if chainCount >= maxPerChain || totalCount >= maxTotal {
		return false
	}
	s.reservedSlots[chain]++
	return true
}

// ReleaseSlot releases a previously reserved slot (e.g., if the buy failed).
func (s *Store) ReleaseSlot(chain Chain) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.reservedSlots[chain] > 0 {
		s.reservedSlots[chain]--
	}
}

// ConsumeSlot converts a reserved slot into an actual position (call after AddPosition).
func (s *Store) ConsumeSlot(chain Chain) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.reservedSlots[chain] > 0 {
		s.reservedSlots[chain]--
	}
}

func (s *Store) totalReservedSlots() int {
	total := 0
	for _, v := range s.reservedSlots {
		total += v
	}
	return total
}

// stateSnapshot is the JSON-serializable form of the store.
type stateSnapshot struct {
	Positions  map[string]*Position `json:"positions"`
	DailyPnL   map[Chain]float64    `json:"daily_pnl"`
	PeakEquity map[Chain]float64    `json:"peak_equity"`
	GasBalance map[Chain]float64    `json:"gas_balance"`
	GasSpent   map[Chain]float64    `json:"gas_spent"`
}

// SaveSnapshot writes the store to a JSON file atomically.
func (s *Store) SaveSnapshot(path string) error {
	s.mu.RLock()
	snap := stateSnapshot{
		Positions:  s.positions,
		DailyPnL:   s.dailyPnL,
		PeakEquity: s.peakEquity,
		GasBalance: s.gasBalance,
		GasSpent:   s.gasSpent,
	}
	data, err := json.MarshalIndent(snap, "", "  ")
	s.mu.RUnlock()
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename snapshot: %w", err)
	}
	return nil
}

// LoadSnapshot restores the store from a JSON snapshot file.
// No-op if the file does not exist.
func (s *Store) LoadSnapshot(path string) error {
	data, err := os.ReadFile(filepath.Clean(path))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read snapshot: %w", err)
	}

	var snap stateSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return fmt.Errorf("unmarshal snapshot: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if snap.Positions != nil {
		s.positions = snap.Positions
	}
	if snap.DailyPnL != nil {
		s.dailyPnL = snap.DailyPnL
	}
	if snap.PeakEquity != nil {
		s.peakEquity = snap.PeakEquity
	}
	if snap.GasBalance != nil {
		s.gasBalance = snap.GasBalance
	}
	if snap.GasSpent != nil {
		s.gasSpent = snap.GasSpent
	}
	return nil
}
