package testutil

import (
	"context"
	"sync"

	"github.com/cindocode/trenchbot/internal/executor"
	"github.com/cindocode/trenchbot/internal/scanner"
	"github.com/cindocode/trenchbot/internal/state"
)

// MockExecutor records all buy/sell calls and returns configurable results.
type MockExecutor struct {
	mu        sync.Mutex
	chain     state.Chain
	BuyCalls  []executor.BuyParams
	SellCalls []executor.SellParams
	BuyFn     func(ctx context.Context, params executor.BuyParams) executor.BuyResult
	SellFn    func(ctx context.Context, params executor.SellParams) executor.SellResult
}

func NewMockExecutor(chain state.Chain) *MockExecutor {
	return &MockExecutor{chain: chain}
}

func (m *MockExecutor) Chain() state.Chain { return m.chain }

func (m *MockExecutor) Buy(ctx context.Context, params executor.BuyParams) executor.BuyResult {
	m.mu.Lock()
	m.BuyCalls = append(m.BuyCalls, params)
	m.mu.Unlock()

	if m.BuyFn != nil {
		return m.BuyFn(ctx, params)
	}
	return executor.BuyResult{
		Success: true,
		TxHash:  "mock-buy-" + params.TokenAddress,
		Price:   1.0,
		Amount:  params.Amount,
		GasCost: 0.000505,
	}
}

func (m *MockExecutor) Sell(ctx context.Context, params executor.SellParams) executor.SellResult {
	m.mu.Lock()
	m.SellCalls = append(m.SellCalls, params)
	m.mu.Unlock()

	if m.SellFn != nil {
		return m.SellFn(ctx, params)
	}
	return executor.SellResult{
		Success: true,
		TxHash:  "mock-sell-" + params.TokenAddress,
		Price:   1.0,
		Amount:  params.AmountPct,
		GasCost: 0.000505,
	}
}

func (m *MockExecutor) GetSellCalls() []executor.SellParams {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]executor.SellParams, len(m.SellCalls))
	copy(cp, m.SellCalls)
	return cp
}

// MockScanner emits pre-loaded tokens then blocks until context cancelled.
type MockScanner struct {
	chain  state.Chain
	Tokens []scanner.NewToken
}

func NewMockScanner(chain state.Chain, tokens []scanner.NewToken) *MockScanner {
	return &MockScanner{chain: chain, Tokens: tokens}
}

func (s *MockScanner) Chain() state.Chain { return s.chain }

func (s *MockScanner) Scan(ctx context.Context, out chan<- scanner.NewToken) error {
	for _, t := range s.Tokens {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case out <- t:
		}
	}
	<-ctx.Done()
	return ctx.Err()
}

// MockNotifier records all notifications.
type MockNotifier struct {
	mu       sync.Mutex
	Messages []string
	Snipes   []SnipeRecord
	Exits    []ExitRecord
}

type SnipeRecord struct {
	Chain, Symbol, Token string
	Amount, Price        float64
	Shadow               bool
}

type ExitRecord struct {
	Chain, Symbol string
	PnLPct        float64
	Reason        string
}

func NewMockNotifier() *MockNotifier { return &MockNotifier{} }

func (n *MockNotifier) Send(_ context.Context, msg string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.Messages = append(n.Messages, msg)
}

func (n *MockNotifier) Snipe(_ context.Context, chain, symbol, token string, amount, price float64, shadow bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.Snipes = append(n.Snipes, SnipeRecord{chain, symbol, token, amount, price, shadow})
}

func (n *MockNotifier) Exit(_ context.Context, chain, symbol string, pnlPct float64, reason string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.Exits = append(n.Exits, ExitRecord{chain, symbol, pnlPct, reason})
}

func (n *MockNotifier) DrawdownWarning(_ context.Context, chain string, drawdownPct float64) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.Messages = append(n.Messages, "DRAWDOWN: "+chain)
}

func (n *MockNotifier) GetExits() []ExitRecord {
	n.mu.Lock()
	defer n.mu.Unlock()
	cp := make([]ExitRecord, len(n.Exits))
	copy(cp, n.Exits)
	return cp
}

func (n *MockNotifier) DrainExits() []ExitRecord {
	n.mu.Lock()
	defer n.mu.Unlock()
	cp := make([]ExitRecord, len(n.Exits))
	copy(cp, n.Exits)
	n.Exits = n.Exits[:0]
	return cp
}
