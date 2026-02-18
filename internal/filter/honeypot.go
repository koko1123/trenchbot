package filter

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/cindocode/trenchbot/internal/state"
)

// HoneypotChecker queries the GoPlus Security API to detect honeypot tokens.
type HoneypotChecker struct {
	client *http.Client
}

func NewHoneypotChecker() *HoneypotChecker {
	return &HoneypotChecker{
		client: &http.Client{Timeout: 2 * time.Second},
	}
}

// goPlusResponse is the top-level response from GoPlus Security API.
type goPlusResponse struct {
	Code    int                       `json:"code"`
	Result  map[string]goPlusToken    `json:"result"`
}

type goPlusToken struct {
	IsHoneypot          string `json:"is_honeypot"`
	SellTax             string `json:"sell_tax"`
	BuyTax              string `json:"buy_tax"`
	CannotSellAll       string `json:"cannot_sell_all"`
	IsBlacklisted       string `json:"is_blacklisted"`
	OwnerChangeBalance  string `json:"owner_change_balance"`
}

// Check queries GoPlus for honeypot indicators. Returns safe=true if the token
// passes all checks. On API error, fails open (returns safe=true) to avoid
// blocking trades due to API outages.
func (h *HoneypotChecker) Check(ctx context.Context, chain state.Chain, address string) (safe bool, reasons []string, err error) {
	var url string
	switch chain {
	case state.ChainSolana:
		url = fmt.Sprintf("https://api.gopluslabs.io/api/v1/solana/token_security/%s", address)
	case state.ChainBNB:
		url = fmt.Sprintf("https://api.gopluslabs.io/api/v1/token_security/56?contract_addresses=%s", address)
	default:
		return true, nil, nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return true, nil, err // fail open
	}

	resp, err := h.client.Do(req)
	if err != nil {
		return true, nil, err // fail open
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return true, nil, fmt.Errorf("goplus returned status %d", resp.StatusCode)
	}

	var result goPlusResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return true, nil, err // fail open
	}

	// GoPlus returns results keyed by lowercase address.
	var token goPlusToken
	var found bool
	for _, v := range result.Result {
		token = v
		found = true
		break
	}
	if !found {
		// No data for this token (likely too new). Fail open.
		return true, nil, nil
	}

	safe = true

	if token.IsHoneypot == "1" {
		safe = false
		reasons = append(reasons, "honeypot detected")
	}
	if token.CannotSellAll == "1" {
		safe = false
		reasons = append(reasons, "cannot sell all tokens")
	}
	if token.IsBlacklisted == "1" {
		safe = false
		reasons = append(reasons, "token has blacklist")
	}
	if token.OwnerChangeBalance == "1" {
		safe = false
		reasons = append(reasons, "owner can change balance")
	}
	// High tax check: GoPlus returns tax as a decimal string (e.g. "0.1" = 10%).
	if taxVal := parseFloat(token.SellTax); taxVal > 0.10 {
		safe = false
		reasons = append(reasons, fmt.Sprintf("high sell tax: %.0f%%", taxVal*100))
	}
	if taxVal := parseFloat(token.BuyTax); taxVal > 0.10 {
		safe = false
		reasons = append(reasons, fmt.Sprintf("high buy tax: %.0f%%", taxVal*100))
	}

	return safe, reasons, nil
}

func parseFloat(s string) float64 {
	var f float64
	fmt.Sscanf(s, "%f", &f)
	return f
}
