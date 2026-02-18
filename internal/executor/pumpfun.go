package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/cindocode/trenchbot/internal/state"
	solanaclient "github.com/cindocode/trenchbot/pkg/solana"
)

type PumpFunExecutor struct {
	tradeURL       string
	jupiterBaseURL string
	client         *solanaclient.Client
	http           *http.Client
	slippage       int
	log            *slog.Logger
}

func NewPumpFunExecutor(tradeURL string, solClient *solanaclient.Client, slippage int, log *slog.Logger) *PumpFunExecutor {
	if slippage <= 0 {
		slippage = 25
	}
	return &PumpFunExecutor{
		tradeURL:       tradeURL,
		jupiterBaseURL: "https://api.jup.ag",
		client:         solClient,
		http:           &http.Client{Timeout: 30 * time.Second},
		slippage:       slippage,
		log:            log,
	}
}

// SetJupiterBaseURL overrides the Jupiter API base URL (for testing).
func (e *PumpFunExecutor) SetJupiterBaseURL(url string) {
	e.jupiterBaseURL = url
}

func (e *PumpFunExecutor) Chain() state.Chain {
	return state.ChainSolana
}

type pumpTradeRequest struct {
	PublicKey        string  `json:"publicKey"`
	Action           string  `json:"action"` // "buy" or "sell"
	Mint             string  `json:"mint"`
	Amount           float64 `json:"amount"`
	DenominatedInSol string  `json:"denominatedInSol"`
	Slippage         int     `json:"slippage"`
	PriorityFee      float64 `json:"priorityFee"`
	Pool             string  `json:"pool"`
}

// solanaGasCost is the estimated gas per transaction: base fee (5000 lamports) + priority fee.
const solanaGasCost = 0.000505 // SOL

func (e *PumpFunExecutor) Buy(ctx context.Context, params BuyParams) BuyResult {
	if params.Shadow {
		// Fetch real price for shadow mode so exit logic works.
		price, err := e.CurrentPrice(ctx, params.TokenAddress)
		if err != nil || price <= 0 {
			price = 1.0 // fallback for brand-new tokens not yet on Jupiter
		}
		tokenAmount := 0.0
		if price > 0 {
			tokenAmount = params.Amount / price
		}

		e.log.Info("SHADOW BUY",
			"chain", "solana",
			"token", params.TokenAddress,
			"symbol", params.TokenSymbol,
			"amount_sol", params.Amount,
			"price", price,
		)
		return BuyResult{
			Success:     true,
			TxHash:      "shadow-" + SafePrefix(params.TokenAddress, 8),
			Price:       price,
			Amount:      params.Amount,
			TokenAmount: tokenAmount,
			GasCost:     solanaGasCost,
		}
	}

	req := pumpTradeRequest{
		PublicKey:        e.client.PublicKey(),
		Action:           "buy",
		Mint:             params.TokenAddress,
		Amount:           params.Amount,
		DenominatedInSol: "true",
		Slippage:         e.slippage,
		PriorityFee:      0.0005,
		Pool:             "auto",
	}

	result, err := e.sendTrade(ctx, req)
	if err != nil {
		e.log.Error("buy failed", "token", params.TokenAddress, "err", err)
		return BuyResult{Error: err}
	}

	// Fetch actual price after buy to track position correctly.
	price, priceErr := e.CurrentPrice(ctx, params.TokenAddress)
	if priceErr != nil || price <= 0 {
		price = 1.0 // fallback
	}
	tokenAmount := 0.0
	if price > 0 {
		tokenAmount = params.Amount / price
	}

	e.log.Info("BUY executed",
		"chain", "solana",
		"token", params.TokenAddress,
		"symbol", params.TokenSymbol,
		"tx", result,
		"amount_sol", params.Amount,
		"price", price,
	)

	return BuyResult{
		Success:     true,
		TxHash:      result,
		Price:       price,
		Amount:      params.Amount,
		TokenAmount: tokenAmount,
		GasCost:     solanaGasCost,
	}
}

func (e *PumpFunExecutor) Sell(ctx context.Context, params SellParams) SellResult {
	// Compute actual token amount from balance and percentage.
	tokenAmount := params.TokenBalance * (params.AmountPct / 100.0)

	if params.Shadow {
		price, _ := e.CurrentPrice(ctx, params.TokenAddress)
		e.log.Info("SHADOW SELL",
			"chain", "solana",
			"token", params.TokenAddress,
			"symbol", params.TokenSymbol,
			"pct", params.AmountPct,
			"token_amount", tokenAmount,
		)
		return SellResult{
			Success: true,
			TxHash:  "shadow-sell-" + SafePrefix(params.TokenAddress, 8),
			Price:   price,
			GasCost: solanaGasCost,
		}
	}

	slippage := e.slippage
	if params.MaxSlippage > 0 {
		slippage = params.MaxSlippage
	}

	req := pumpTradeRequest{
		PublicKey:        e.client.PublicKey(),
		Action:           "sell",
		Mint:             params.TokenAddress,
		Amount:           tokenAmount,
		DenominatedInSol: "false",
		Slippage:         slippage,
		PriorityFee:      0.0005,
		Pool:             "auto",
	}

	result, err := e.sendTrade(ctx, req)
	if err != nil {
		e.log.Error("sell failed", "token", params.TokenAddress, "err", err)
		return SellResult{Error: err}
	}

	// Get current price for PnL tracking.
	price, _ := e.CurrentPrice(ctx, params.TokenAddress)

	e.log.Info("SELL executed",
		"chain", "solana",
		"token", params.TokenAddress,
		"symbol", params.TokenSymbol,
		"tx", result,
		"token_amount", tokenAmount,
	)

	return SellResult{
		Success: true,
		TxHash:  result,
		Price:   price,
		GasCost: solanaGasCost,
	}
}

// jupiterPriceResponse is the response from Jupiter Price API v2.
type jupiterPriceResponse struct {
	Data map[string]struct {
		Price string `json:"price"`
	} `json:"data"`
}

// CurrentPrice returns the current USD price for a token via Jupiter Price API.
// This works for graduated tokens (post-Raydium). Pre-graduation tokens get
// prices from the PumpPortal WebSocket trade feed instead.
func (e *PumpFunExecutor) CurrentPrice(ctx context.Context, tokenAddress string) (float64, error) {
	return e.jupiterPrice(ctx, tokenAddress)
}

func (e *PumpFunExecutor) jupiterPrice(ctx context.Context, tokenAddress string) (float64, error) {
	url := fmt.Sprintf("%s/price/v2?ids=%s", e.jupiterBaseURL, tokenAddress)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, fmt.Errorf("creating jupiter price request: %w", err)
	}

	resp, err := e.http.Do(req)
	if err != nil {
		return 0, fmt.Errorf("fetching jupiter price: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("jupiter price API returned status %d", resp.StatusCode)
	}

	var result jupiterPriceResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return 0, fmt.Errorf("decoding jupiter price response: %w", err)
	}

	data, ok := result.Data[tokenAddress]
	if !ok {
		return 0, fmt.Errorf("token %s not found in jupiter response", SafePrefix(tokenAddress, 12))
	}

	price, err := strconv.ParseFloat(data.Price, 64)
	if err != nil {
		return 0, fmt.Errorf("parsing jupiter price %q: %w", data.Price, err)
	}
	return price, nil
}

func (e *PumpFunExecutor) sendTrade(ctx context.Context, trade pumpTradeRequest) (string, error) {
	body, err := json.Marshal(trade)
	if err != nil {
		return "", fmt.Errorf("marshal trade: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.tradeURL, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := e.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("send trade: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("trade failed (status %d): %s", resp.StatusCode, string(respBody))
	}

	// PumpPortal returns the transaction signature as plain text
	return string(respBody), nil
}
