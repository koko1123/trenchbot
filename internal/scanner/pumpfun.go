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
}

func NewPumpFunScanner(wsURL string, log *slog.Logger) *PumpFunScanner {
	return &PumpFunScanner{
		wsURL: wsURL,
		log:   log,
		seen:  make(map[string]struct{}),
	}
}

func (s *PumpFunScanner) Chain() state.Chain {
	return state.ChainSolana
}

type pumpPortalToken struct {
	Mint               string  `json:"mint"`
	Name               string  `json:"name"`
	Symbol             string  `json:"symbol"`
	Description        string  `json:"description"`
	ImageURI           string  `json:"image_uri"`
	TraderPublicKey    string  `json:"traderPublicKey"`
	InitialBuy         float64 `json:"initialBuy"`
	MarketCapSol       float64 `json:"marketCapSol"`
	UsdMarketCap       float64 `json:"usdMarketCap"`
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

	// Subscribe to new token events
	sub := map[string]interface{}{
		"method": "subscribeNewToken",
	}
	if err := conn.WriteJSON(sub); err != nil {
		return fmt.Errorf("subscribe: %w", err)
	}
	s.log.Info("pumpfun scanner connected and subscribed")

	// Set up ping/pong keepalive.
	conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})

	// Ping goroutine exits when conn.Close() causes WriteMessage to fail.
	go func() {
		pingTicker := time.NewTicker(30 * time.Second)
		defer pingTicker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-pingTicker.C:
				if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
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

		var token pumpPortalToken
		if err := json.Unmarshal(msg, &token); err != nil {
			s.log.Debug("pumpfun: ignoring non-token message", "raw", string(msg))
			continue
		}

		if token.Mint == "" {
			continue
		}

		s.mu.Lock()
		if _, already := s.seen[token.Mint]; already {
			s.mu.Unlock()
			continue
		}
		if _, already := s.prevSeen[token.Mint]; already {
			s.mu.Unlock()
			continue
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
			return ctx.Err()
		}
	}
}
