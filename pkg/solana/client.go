package solana

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/gagliardetto/solana-go"
	"github.com/gagliardetto/solana-go/rpc"
)

type Client struct {
	rpc       *rpc.Client
	rpcURL    string
	wallet    solana.PrivateKey
	log       *slog.Logger
	http      *http.Client
	heliusKey string
	fallbacks []*rpc.Client
}

func NewClient(rpcURL string, privateKeyBase58 string, log *slog.Logger) (*Client, error) {
	c := rpc.New(rpcURL)
	client := &Client{
		rpc:    c,
		rpcURL: rpcURL,
		log:    log,
		http:   &http.Client{Timeout: 10 * time.Second},
	}
	if privateKeyBase58 != "" {
		pk, err := solana.PrivateKeyFromBase58(privateKeyBase58)
		if err != nil {
			return nil, fmt.Errorf("parsing solana private key: %w", err)
		}
		client.wallet = pk
	}
	return client, nil
}

// SetHeliusKey configures the Helius API key for DAS API access.
func (c *Client) SetHeliusKey(key string) {
	c.heliusKey = key
}

// SetFallbackRPCs adds fallback RPC endpoints for redundancy.
func (c *Client) SetFallbackRPCs(urls []string) {
	c.fallbacks = nil
	for _, u := range urls {
		if u != "" {
			c.fallbacks = append(c.fallbacks, rpc.New(u))
		}
	}
}

func (c *Client) PublicKey() string {
	if c.wallet == nil {
		return ""
	}
	return c.wallet.PublicKey().String()
}

func (c *Client) GetBalance(ctx context.Context) (float64, error) {
	if c.wallet == nil {
		return 0, fmt.Errorf("no wallet configured")
	}
	result, err := c.rpc.GetBalance(ctx, c.wallet.PublicKey(), rpc.CommitmentConfirmed)
	if err != nil {
		return 0, fmt.Errorf("getting balance: %w", err)
	}
	return float64(result.Value) / 1e9, nil // lamports to SOL
}

func (c *Client) GetSignatureStatus(ctx context.Context, sig string) (bool, error) {
	// Shadow transactions are always considered confirmed.
	if strings.HasPrefix(sig, "shadow-") {
		return true, nil
	}

	s, err := solana.SignatureFromBase58(sig)
	if err != nil {
		return false, fmt.Errorf("parsing signature: %w", err)
	}
	result, err := c.rpc.GetSignatureStatuses(ctx, false, s)
	if err != nil {
		return false, fmt.Errorf("getting signature status: %w", err)
	}
	if len(result.Value) == 0 || result.Value[0] == nil {
		return false, nil
	}
	return result.Value[0].Err == nil, nil
}

func (c *Client) Wallet() solana.PrivateKey {
	return c.wallet
}

func (c *Client) RPC() *rpc.Client {
	return c.rpc
}

// --- Helius DAS API methods ---

// TokenHolder represents a holder of a token with their balance.
type TokenHolder struct {
	Owner   string
	Amount  uint64
	Percent float64 // percentage of total supply held
}

// HolderDistribution holds the analysis of a token's holder distribution.
type HolderDistribution struct {
	TotalHolders  int
	TopHolders    []TokenHolder
	TopHolderPct  float64 // percentage held by the largest holder
	Top5HolderPct float64 // percentage held by top 5 holders
}

// heliusTokenAccountResponse models the Helius DAS getTokenAccounts response.
type heliusTokenAccountResponse struct {
	Result struct {
		TokenAccounts []struct {
			Address string `json:"address"`
			Owner   string `json:"owner"`
			Amount  uint64 `json:"amount"`
		} `json:"token_accounts"`
		Total int `json:"total"`
	} `json:"result"`
}

// GetTokenHolders queries Helius DAS API for the top holders of a token.
// Returns an empty distribution if Helius key is not configured.
func (c *Client) GetTokenHolders(ctx context.Context, mint string) (HolderDistribution, error) {
	if c.heliusKey == "" {
		return HolderDistribution{}, fmt.Errorf("helius API key not configured")
	}

	url := fmt.Sprintf("https://mainnet.helius-rpc.com/?api-key=%s", c.heliusKey)
	payload := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "getTokenAccounts",
		"params": map[string]interface{}{
			"mint":  mint,
			"limit": 20,
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return HolderDistribution{}, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return HolderDistribution{}, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return HolderDistribution{}, fmt.Errorf("helius request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return HolderDistribution{}, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return HolderDistribution{}, fmt.Errorf("helius returned status %d: %s", resp.StatusCode, string(respBody[:min(len(respBody), 200)]))
	}

	var result heliusTokenAccountResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return HolderDistribution{}, fmt.Errorf("decode response: %w", err)
	}

	accounts := result.Result.TokenAccounts
	if len(accounts) == 0 {
		return HolderDistribution{TotalHolders: result.Result.Total}, nil
	}

	// Compute total supply from visible accounts.
	var totalVisible uint64
	for _, a := range accounts {
		totalVisible += a.Amount
	}

	holders := make([]TokenHolder, 0, len(accounts))
	for _, a := range accounts {
		pct := 0.0
		if totalVisible > 0 {
			pct = float64(a.Amount) / float64(totalVisible) * 100
		}
		holders = append(holders, TokenHolder{
			Owner:   a.Owner,
			Amount:  a.Amount,
			Percent: pct,
		})
	}
	sort.Slice(holders, func(i, j int) bool { return holders[i].Amount > holders[j].Amount })

	dist := HolderDistribution{
		TotalHolders: result.Result.Total,
		TopHolders:   holders,
	}
	if len(holders) > 0 {
		dist.TopHolderPct = holders[0].Percent
	}
	top5Sum := 0.0
	for i := 0; i < min(5, len(holders)); i++ {
		top5Sum += holders[i].Percent
	}
	dist.Top5HolderPct = top5Sum

	return dist, nil
}

// PriorityFeeEstimate holds Helius priority fee recommendations.
type PriorityFeeEstimate struct {
	Low      float64 // in SOL
	Medium   float64
	High     float64
	VeryHigh float64
}

type heliusFeeResponse struct {
	Result struct {
		PriorityFeeEstimate  float64 `json:"priorityFeeEstimate"`
		PriorityFeeLevels    struct {
			Low      float64 `json:"low"`
			Medium   float64 `json:"medium"`
			High     float64 `json:"high"`
			VeryHigh float64 `json:"veryHigh"`
		} `json:"priorityFeeLevels"`
	} `json:"result"`
}

// GetPriorityFeeEstimate queries Helius for current priority fee recommendations.
// Returns fees in SOL (converted from microlamports).
func (c *Client) GetPriorityFeeEstimate(ctx context.Context) (PriorityFeeEstimate, error) {
	if c.heliusKey == "" {
		return PriorityFeeEstimate{
			Low:      0.0001,
			Medium:   0.0005,
			High:     0.001,
			VeryHigh: 0.002,
		}, nil
	}

	url := fmt.Sprintf("https://mainnet.helius-rpc.com/?api-key=%s", c.heliusKey)
	payload := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "getPriorityFeeEstimate",
		"params": []interface{}{
			map[string]interface{}{
				"options": map[string]interface{}{
					"includeAllPriorityFeeLevels": true,
					"recommended":                true,
				},
			},
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return PriorityFeeEstimate{}, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return PriorityFeeEstimate{}, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return PriorityFeeEstimate{}, fmt.Errorf("helius fee request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return PriorityFeeEstimate{}, fmt.Errorf("helius fee API returned status %d", resp.StatusCode)
	}

	var result heliusFeeResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return PriorityFeeEstimate{}, fmt.Errorf("decode fee response: %w", err)
	}

	// Helius returns fees in microlamports per compute unit.
	// Convert to SOL: microlamports * 200000 CU / 1e12 (typical tx = 200k CU).
	const cuPerTx = 200_000
	toSOL := func(microlamports float64) float64 {
		return microlamports * cuPerTx / 1e12
	}

	levels := result.Result.PriorityFeeLevels
	return PriorityFeeEstimate{
		Low:      toSOL(levels.Low),
		Medium:   toSOL(levels.Medium),
		High:     toSOL(levels.High),
		VeryHigh: toSOL(levels.VeryHigh),
	}, nil
}

// GetBalanceWithFallback tries the primary RPC and falls back to alternates.
func (c *Client) GetBalanceWithFallback(ctx context.Context) (float64, error) {
	bal, err := c.GetBalance(ctx)
	if err == nil {
		return bal, nil
	}

	for _, fb := range c.fallbacks {
		if c.wallet == nil {
			continue
		}
		result, fbErr := fb.GetBalance(ctx, c.wallet.PublicKey(), rpc.CommitmentConfirmed)
		if fbErr == nil {
			return float64(result.Value) / 1e9, nil
		}
	}
	return 0, err
}

// --- SPL Token Balance ---

// GetTokenBalance returns the balance of an SPL token in the wallet.
// Returns balance in token units (adjusted for decimals).
func (c *Client) GetTokenBalance(ctx context.Context, tokenMint string) (float64, error) {
	if c.wallet == nil {
		return 0, fmt.Errorf("no wallet configured")
	}

	mint, err := solana.PublicKeyFromBase58(tokenMint)
	if err != nil {
		return 0, fmt.Errorf("invalid mint address: %w", err)
	}

	// Find the associated token account.
	accounts, err := c.rpc.GetTokenAccountsByOwner(
		ctx,
		c.wallet.PublicKey(),
		&rpc.GetTokenAccountsConfig{Mint: &mint},
		&rpc.GetTokenAccountsOpts{Encoding: solana.EncodingJSONParsed},
	)
	if err != nil {
		return 0, fmt.Errorf("get token accounts: %w", err)
	}

	if len(accounts.Value) == 0 {
		return 0, nil // no token account = zero balance
	}

	// Parse the JSON-parsed account data to extract the amount.
	// The structure is: account.Data.Parsed.Info.TokenAmount.UiAmount
	type parsedTokenAccount struct {
		Parsed struct {
			Info struct {
				TokenAmount struct {
					UiAmount float64 `json:"uiAmount"`
				} `json:"tokenAmount"`
			} `json:"info"`
		} `json:"parsed"`
	}

	data := accounts.Value[0].Account.Data
	raw, err := json.Marshal(data)
	if err != nil {
		return 0, fmt.Errorf("marshal account data: %w", err)
	}

	var parsed parsedTokenAccount
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return 0, fmt.Errorf("parse token account: %w", err)
	}

	return parsed.Parsed.Info.TokenAmount.UiAmount, nil
}

// --- Jupiter V6 Swap API ---

// JupiterQuoteResponse holds a Jupiter swap quote.
type JupiterQuoteResponse struct {
	InputMint  string `json:"inputMint"`
	OutputMint string `json:"outputMint"`
	InAmount   string `json:"inAmount"`
	OutAmount  string `json:"outAmount"`
	// Raw quote for passing to swap endpoint.
	raw json.RawMessage
}

// JupiterQuote gets a swap quote from Jupiter V6 API.
// amount is in the smallest unit of inputMint (e.g., USDC has 6 decimals: 1 USDC = 1_000_000).
func (c *Client) JupiterQuote(ctx context.Context, inputMint, outputMint string, amount uint64, slippageBps int) (*JupiterQuoteResponse, error) {
	if slippageBps <= 0 {
		slippageBps = 100 // 1% default
	}

	url := fmt.Sprintf("https://quote-api.jup.ag/v6/quote?inputMint=%s&outputMint=%s&amount=%d&slippageBps=%d",
		inputMint, outputMint, amount, slippageBps)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create quote request: %w", err)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("jupiter quote request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read quote response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("jupiter quote API returned status %d: %s", resp.StatusCode, string(respBody[:min(len(respBody), 200)]))
	}

	var quote JupiterQuoteResponse
	if err := json.Unmarshal(respBody, &quote); err != nil {
		return nil, fmt.Errorf("decode quote: %w", err)
	}
	quote.raw = respBody

	return &quote, nil
}

// jupiterSwapRequest is the request body for Jupiter V6 swap endpoint.
type jupiterSwapRequest struct {
	QuoteResponse          json.RawMessage `json:"quoteResponse"`
	UserPublicKey          string          `json:"userPublicKey"`
	DynamicComputeUnitLimit bool           `json:"dynamicComputeUnitLimit"`
	PrioritizationFeeLamports string       `json:"prioritizationFeeLamports"`
}

// jupiterSwapResponse is the response from Jupiter V6 swap endpoint.
type jupiterSwapResponse struct {
	SwapTransaction string `json:"swapTransaction"` // base64 encoded transaction
}

// JupiterSwap executes a swap using a Jupiter quote.
// Returns the transaction signature.
func (c *Client) JupiterSwap(ctx context.Context, quote *JupiterQuoteResponse) (string, error) {
	if c.wallet == nil {
		return "", fmt.Errorf("no wallet configured for signing")
	}

	swapReq := jupiterSwapRequest{
		QuoteResponse:           quote.raw,
		UserPublicKey:           c.wallet.PublicKey().String(),
		DynamicComputeUnitLimit: true,
		PrioritizationFeeLamports: "auto",
	}

	body, err := json.Marshal(swapReq)
	if err != nil {
		return "", fmt.Errorf("marshal swap request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://quote-api.jup.ag/v6/swap", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("create swap request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("jupiter swap request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read swap response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("jupiter swap API returned status %d: %s", resp.StatusCode, string(respBody[:min(len(respBody), 200)]))
	}

	var swapResp jupiterSwapResponse
	if err := json.Unmarshal(respBody, &swapResp); err != nil {
		return "", fmt.Errorf("decode swap response: %w", err)
	}

	if swapResp.SwapTransaction == "" {
		return "", fmt.Errorf("jupiter swap returned empty transaction")
	}

	// Decode, sign, and send the transaction.
	tx, err := solana.TransactionFromBase64(swapResp.SwapTransaction)
	if err != nil {
		return "", fmt.Errorf("decode swap transaction: %w", err)
	}

	// Sign with wallet.
	_, err = tx.Sign(func(key solana.PublicKey) *solana.PrivateKey {
		if key.Equals(c.wallet.PublicKey()) {
			return &c.wallet
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("sign swap transaction: %w", err)
	}

	// Send transaction.
	sig, err := c.rpc.SendTransactionWithOpts(ctx, tx, rpc.TransactionOpts{
		SkipPreflight: true,
	})
	if err != nil {
		return "", fmt.Errorf("send swap transaction: %w", err)
	}

	return sig.String(), nil
}
