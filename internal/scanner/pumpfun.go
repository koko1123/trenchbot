package scanner

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/cindocode/trenchbot/internal/state"
	"github.com/gorilla/websocket"
)

type PumpFunScanner struct {
	wsURL    string
	log      *slog.Logger
	mu       sync.Mutex
	seen     map[string]struct{}
	prevSeen map[string]struct{}

	// Trade subscription support.
	tradeCh          chan<- TokenTrade
	subCh            chan subRequest
	subscribedTokens map[string]struct{} // tracks active trade subscriptions for reconnect replay
	subMu            sync.Mutex
}

type subRequest struct {
	method string
	keys   []string
}

func NewPumpFunScanner(wsURL string, log *slog.Logger) *PumpFunScanner {
	return &PumpFunScanner{
		wsURL:            wsURL,
		log:              log,
		seen:             make(map[string]struct{}),
		subCh:            make(chan subRequest, 64),
		subscribedTokens: make(map[string]struct{}),
	}
}

// SetTradeChannel sets the output channel for trade events (price updates).
func (s *PumpFunScanner) SetTradeChannel(ch chan<- TokenTrade) {
	s.tradeCh = ch
}

// SubscribeToken subscribes to real-time trade events for a token mint.
func (s *PumpFunScanner) SubscribeToken(mint string) {
	s.subMu.Lock()
	s.subscribedTokens[mint] = struct{}{}
	s.subMu.Unlock()

	select {
	case s.subCh <- subRequest{method: "subscribeTokenTrade", keys: []string{mint}}:
	default:
		s.log.Warn("subscribe channel full, dropping subscribe request", "mint", mint)
	}
}

// UnsubscribeToken stops receiving trade events for a token mint.
func (s *PumpFunScanner) UnsubscribeToken(mint string) {
	s.subMu.Lock()
	delete(s.subscribedTokens, mint)
	s.subMu.Unlock()

	select {
	case s.subCh <- subRequest{method: "unsubscribeTokenTrade", keys: []string{mint}}:
	default:
		s.log.Warn("subscribe channel full, dropping unsubscribe request", "mint", mint)
	}
}

func (s *PumpFunScanner) Chain() state.Chain {
	return state.ChainSolana
}

// pumpPortalToken is a new token creation event from PumpPortal.
type pumpPortalToken struct {
	Mint            string  `json:"mint"`
	Name            string  `json:"name"`
	Symbol          string  `json:"symbol"`
	Description     string  `json:"description"`
	ImageURI        string  `json:"image_uri"`
	TraderPublicKey string  `json:"traderPublicKey"`
	InitialBuy      float64 `json:"initialBuy"`
	MarketCapSol    float64 `json:"marketCapSol"`
	UsdMarketCap    float64 `json:"usdMarketCap"`
}

// pumpPortalTrade is a trade event from subscribeTokenTrade.
type pumpPortalTrade struct {
	Mint         string  `json:"mint"`
	TxType       string  `json:"txType"` // "buy" or "sell"
	SolAmount    float64 `json:"solAmount"`
	MarketCapSol float64 `json:"marketCapSol"`
	UsdMarketCap float64 `json:"usdMarketCap"`
}

func (s *PumpFunScanner) Scan(ctx context.Context, out chan<- NewToken) error {
	for {
		if err := s.connect(ctx, out); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			s.log.Error("pumpfun ws disconnected, reconnecting", "err", err)
			time.Sleep(2 * time.Second)
			continue
		}
	}
}

func (s *PumpFunScanner) connect(ctx context.Context, out chan<- NewToken) error {
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, s.wsURL, nil)
	if err != nil {
		return fmt.Errorf("ws dial: %w", err)
	}
	defer conn.Close()

	// Subscribe to new token events.
	if err := conn.WriteJSON(map[string]interface{}{"method": "subscribeNewToken"}); err != nil {
		return fmt.Errorf("subscribe new tokens: %w", err)
	}

	// Replay active trade subscriptions from before the reconnect.
	s.subMu.Lock()
	replayMints := make([]string, 0, len(s.subscribedTokens))
	for mint := range s.subscribedTokens {
		replayMints = append(replayMints, mint)
	}
	s.subMu.Unlock()
	if len(replayMints) > 0 {
		if err := conn.WriteJSON(map[string]interface{}{
			"method": "subscribeTokenTrade",
			"keys":   replayMints,
		}); err != nil {
			return fmt.Errorf("replay trade subscriptions: %w", err)
		}
		s.log.Info("replayed trade subscriptions on reconnect", "count", len(replayMints))
	}

	s.log.Info("pumpfun scanner connected and subscribed")

	// Set up ping/pong keepalive.
	conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})

	// Drain subCh before starting the main loop to pick up any requests
	// that arrived between disconnect and reconnect.
	drainDone := false
	for !drainDone {
		select {
		case req := <-s.subCh:
			if wErr := conn.WriteJSON(map[string]interface{}{
				"method": req.method,
				"keys":   req.keys,
			}); wErr != nil {
				return fmt.Errorf("drain sub write: %w", wErr)
			}
		default:
			drainDone = true
		}
	}

	// Write goroutine: handles pings and subscription requests.
	// gorilla/websocket requires serialized writes, so all writes go through this goroutine.
	writeDone := make(chan struct{})
	go func() {
		defer close(writeDone)
		pingTicker := time.NewTicker(30 * time.Second)
		defer pingTicker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case req := <-s.subCh:
				if wErr := conn.WriteJSON(map[string]interface{}{
					"method": req.method,
					"keys":   req.keys,
				}); wErr != nil {
					s.log.Debug("ws write failed", "err", wErr)
					return
				}
				s.log.Debug("ws subscription sent", "method", req.method, "keys", req.keys)
			case <-pingTicker.C:
				if wErr := conn.WriteMessage(websocket.PingMessage, nil); wErr != nil {
					return
				}
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		_, msg, err := conn.ReadMessage()
		if err != nil {
			return fmt.Errorf("ws read: %w", err)
		}

		// Parse into a generic map to determine message type.
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(msg, &raw); err != nil {
			s.log.Debug("pumpfun: ignoring unparseable message", "raw", string(msg[:min(len(msg), 100)]))
			continue
		}

		// Discriminate by txType value:
		//   "create" → new token event (has name, symbol, initialBuy)
		//   "buy"/"sell" → trade event (has tokenAmount, newTokenBalance)
		if txTypeRaw, ok := raw["txType"]; ok {
			var txType string
			json.Unmarshal(txTypeRaw, &txType)
			if txType == "buy" || txType == "sell" {
				s.handleTradeEvent(msg)
				continue
			}
		}

		s.handleNewTokenEvent(ctx, msg, out)
	}
}

func (s *PumpFunScanner) handleTradeEvent(msg []byte) {
	if s.tradeCh == nil {
		return
	}

	var trade pumpPortalTrade
	if err := json.Unmarshal(msg, &trade); err != nil {
		s.log.Debug("pumpfun: failed to parse trade event", "err", err)
		return
	}
	if trade.Mint == "" {
		return
	}

	select {
	case s.tradeCh <- TokenTrade{
		Mint:         trade.Mint,
		TxType:       trade.TxType,
		SolAmount:    trade.SolAmount,
		MarketCapSol: trade.MarketCapSol,
		UsdMarketCap: trade.UsdMarketCap,
	}:
	default:
		// Drop if channel is full rather than blocking the read loop.
	}
}

func (s *PumpFunScanner) handleNewTokenEvent(ctx context.Context, msg []byte, out chan<- NewToken) {
	var token pumpPortalToken
	if err := json.Unmarshal(msg, &token); err != nil {
		s.log.Debug("pumpfun: ignoring non-token message", "err", err)
		return
	}

	if token.Mint == "" {
		return
	}

	s.mu.Lock()
	if _, already := s.seen[token.Mint]; already {
		s.mu.Unlock()
		return
	}
	if _, already := s.prevSeen[token.Mint]; already {
		s.mu.Unlock()
		return
	}
	s.seen[token.Mint] = struct{}{}
	// Two-generation LRU: rotate instead of clearing to avoid re-processing.
	if len(s.seen) > 10000 {
		s.prevSeen = s.seen
		s.seen = make(map[string]struct{})
	}
	s.mu.Unlock()

	newToken := NewToken{
		Chain:        state.ChainSolana,
		Address:      token.Mint,
		Name:         token.Name,
		Symbol:       token.Symbol,
		Description:  token.Description,
		ImageURL:     token.ImageURI,
		Creator:      token.TraderPublicKey,
		Timestamp:    time.Now(),
		MarketCapUSD: token.UsdMarketCap,
		Metadata: map[string]interface{}{
			"initialBuy":   token.InitialBuy,
			"marketCapSol": token.MarketCapSol,
		},
	}

	s.log.Debug("new token detected",
		"chain", "solana",
		"symbol", token.Symbol,
		"mint", token.Mint,
		"mcap_usd", token.UsdMarketCap,
	)

	select {
	case out <- newToken:
	case <-ctx.Done():
	}
}
