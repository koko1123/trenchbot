package jito

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const (
	DefaultBlockEngineURL = "https://mainnet.block-engine.jito.wtf"
	DefaultTipLamports    = 10000
)

// tipAccounts are well-known Jito tip account addresses.
var tipAccounts = []string{
	"96gYZGLnJYVFmbjzopPSU6QiEV5fGqZNyN9nmNhvrZU5",
	"HFqU5x63VTqvQss8hp11i4bVqkfRtQ7NmXwkiY89EXZ",
	"Cw8CFyM9FkoMi7K7Crf6HNQqf4uEMzpKw6QNghXLvLkY",
	"ADaUMid9yfUytqMBgopwjb2DTLSNzxkNPpkfcLcJV7mm",
	"DfXygSm4jCyNCybVYYK6DwvWqjKee8pbDmJGcLWNDXjh",
	"ADuUkR4vqLUMWXxW9gh6D6L8pMSawimctcNZ5pGwDcEt",
	"DttWaMuVvTiduZRnguLF7jNxTgiMBZ1hyAumKUiL2KRL",
	"3AVi9Tg9Uo68tJfuvoKvqKNWKkC5wPdSSdeBnizKZ6jT",
}

// Client is a Jito block engine client for submitting transaction bundles.
type Client struct {
	blockEngineURL string
	tipLamports    uint64
	httpClient     *http.Client
}

// NewClient creates a new Jito block engine client.
func NewClient(blockEngineURL string, tipLamports uint64) *Client {
	if blockEngineURL == "" {
		blockEngineURL = DefaultBlockEngineURL
	}
	if tipLamports == 0 {
		tipLamports = DefaultTipLamports
	}
	return &Client{
		blockEngineURL: blockEngineURL,
		tipLamports:    tipLamports,
		httpClient:     &http.Client{Timeout: 5 * time.Second},
	}
}

// jsonRPCRequest is a standard JSON-RPC 2.0 request.
type jsonRPCRequest struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      int         `json:"id"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params"`
}

// jsonRPCResponse is a standard JSON-RPC 2.0 response.
type jsonRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// SendBundle submits a bundle of base64-encoded serialized transactions to Jito.
// Returns the bundle ID on success.
//
// The tip transaction should be included as the last tx in the bundle, sending
// tipLamports to one of the Jito tip accounts.
func (c *Client) SendBundle(ctx context.Context, txns []string) (string, error) {
	req := jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "sendBundle",
		Params:  []interface{}{txns},
	}

	body, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("marshal bundle request: %w", err)
	}

	url := c.blockEngineURL + "/api/v1/bundles"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("create bundle request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("send bundle request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read bundle response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("jito returned status %d: %s", resp.StatusCode, string(respBody[:min(len(respBody), 200)]))
	}

	var rpcResp jsonRPCResponse
	if err := json.Unmarshal(respBody, &rpcResp); err != nil {
		return "", fmt.Errorf("decode bundle response: %w", err)
	}

	if rpcResp.Error != nil {
		return "", fmt.Errorf("jito RPC error %d: %s", rpcResp.Error.Code, rpcResp.Error.Message)
	}

	// Result is a bundle ID string.
	var bundleID string
	if err := json.Unmarshal(rpcResp.Result, &bundleID); err != nil {
		return "", fmt.Errorf("decode bundle ID: %w", err)
	}

	return bundleID, nil
}

// GetBundleStatus checks the status of a previously submitted bundle.
// Returns: "landed", "pending", "failed", or error.
func (c *Client) GetBundleStatus(ctx context.Context, bundleID string) (string, error) {
	req := jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "getBundleStatuses",
		Params:  []interface{}{[]string{bundleID}},
	}

	body, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("marshal status request: %w", err)
	}

	url := c.blockEngineURL + "/api/v1/bundles"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("create status request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("send status request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read status response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("jito status returned %d: %s", resp.StatusCode, string(respBody[:min(len(respBody), 200)]))
	}

	var rpcResp jsonRPCResponse
	if err := json.Unmarshal(respBody, &rpcResp); err != nil {
		return "", fmt.Errorf("decode status response: %w", err)
	}

	if rpcResp.Error != nil {
		return "", fmt.Errorf("jito status RPC error %d: %s", rpcResp.Error.Code, rpcResp.Error.Message)
	}

	// Result is an object with a "value" array of bundle statuses.
	var result struct {
		Value []struct {
			BundleID           string `json:"bundle_id"`
			ConfirmationStatus string `json:"confirmation_status"`
			Err                *struct {
				Ok interface{} `json:"Ok"`
			} `json:"err"`
		} `json:"value"`
	}
	if err := json.Unmarshal(rpcResp.Result, &result); err != nil {
		return "", fmt.Errorf("decode status result: %w", err)
	}

	if len(result.Value) == 0 {
		return "pending", nil
	}

	status := result.Value[0].ConfirmationStatus
	if status == "" {
		return "pending", nil
	}
	if status == "finalized" || status == "confirmed" {
		return "landed", nil
	}
	return status, nil
}

// TipAccounts returns the list of Jito tip accounts.
func (c *Client) TipAccounts() []string {
	out := make([]string, len(tipAccounts))
	copy(out, tipAccounts)
	return out
}

// TipLamports returns the configured tip amount.
func (c *Client) TipLamports() uint64 {
	return c.tipLamports
}
