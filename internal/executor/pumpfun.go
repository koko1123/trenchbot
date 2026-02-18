package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/cindocode/trenchbot/internal/state"
	solanaclient "github.com/cindocode/trenchbot/pkg/solana"
)

type PumpFunExecutor struct {
	tradeURL string
	client   *solanaclient.Client
	http     *http.Client
	log      *slog.Logger
}

func NewPumpFunExecutor(tradeURL string, solClient *solanaclient.Client, log *slog.Logger) *PumpFunExecutor {
	return &PumpFunExecutor{
		tradeURL: tradeURL,
		client:   solClient,
		http:     &http.Client{Timeout: 30 * time.Second},
		log:      log,
	}
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
		e.log.Info("SHADOW BUY",
			"chain", "solana",
			"token", params.TokenAddress,
			"symbol", params.TokenSymbol,
			"amount_sol", params.Amount,
		)
		return BuyResult{
			Success: true,
			TxHash:  "shadow-" + SafePrefix(params.TokenAddress, 8),
			Price:   1.0,
			Amount:  params.Amount,
			GasCost: solanaGasCost,
		}
	}

	req := pumpTradeRequest{
		PublicKey:        e.client.PublicKey(),
		Action:           "buy",
		Mint:             params.TokenAddress,
		Amount:           params.Amount,
		DenominatedInSol: "true",
		Slippage:         25,
		PriorityFee:      0.0005,
		Pool:             "auto",
	}

	result, err := e.sendTrade(ctx, req)
	if err != nil {
		e.log.Error("buy failed", "token", params.TokenAddress, "err", err)
		return BuyResult{Error: err}
	}

	e.log.Info("BUY executed",
		"chain", "solana",
		"token", params.TokenAddress,
		"symbol", params.TokenSymbol,
		"tx", result,
		"amount_sol", params.Amount,
	)

	return BuyResult{
		Success: true,
		TxHash:  result,
		Price:   1.0,
		Amount:  params.Amount,
		GasCost: solanaGasCost,
	}
}

func (e *PumpFunExecutor) Sell(ctx context.Context, params SellParams) SellResult {
	if params.Shadow {
		e.log.Info("SHADOW SELL",
			"chain", "solana",
			"token", params.TokenAddress,
			"symbol", params.TokenSymbol,
			"pct", params.AmountPct,
		)
		return SellResult{
			Success: true,
			TxHash:  "shadow-sell-" + SafePrefix(params.TokenAddress, 8),
			GasCost: solanaGasCost,
		}
	}

	// For sells, amount is percentage of tokens (as token amount, not SOL)
	req := pumpTradeRequest{
		PublicKey:        e.client.PublicKey(),
		Action:           "sell",
		Mint:             params.TokenAddress,
		Amount:           params.AmountPct,
		DenominatedInSol: "false",
		Slippage:         25,
		PriorityFee:      0.0005,
		Pool:             "auto",
	}

	result, err := e.sendTrade(ctx, req)
	if err != nil {
		e.log.Error("sell failed", "token", params.TokenAddress, "err", err)
		return SellResult{Error: err}
	}

	e.log.Info("SELL executed",
		"chain", "solana",
		"token", params.TokenAddress,
		"symbol", params.TokenSymbol,
		"tx", result,
	)

	return SellResult{
		Success: true,
		TxHash:  result,
		GasCost: solanaGasCost,
	}
}

func (e *PumpFunExecutor) CurrentPrice(_ context.Context, _ string) (float64, error) {
	// TODO: Implement price feed using Jupiter API or Solana RPC bonding curve query.
	// For now, PumpFun tokens use normalized pricing (entry = 1.0).
	// The monitor requires an external price update mechanism.
	return 0, fmt.Errorf("price feed not yet implemented for PumpFun")
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
