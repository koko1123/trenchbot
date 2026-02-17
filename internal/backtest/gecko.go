package backtest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
)

const geckoBaseURL = "https://api.geckoterminal.com/api/v2"

type GeckoClient struct {
	httpClient *http.Client
	log        *slog.Logger
}

func NewGeckoClient(log *slog.Logger) *GeckoClient {
	return &GeckoClient{
		httpClient: &http.Client{Timeout: 30 * time.Second},
		log:        log,
	}
}

// PoolEntry represents a discovered pool with its base token info.
type PoolEntry struct {
	PoolAddress   string
	TokenAddress  string
	Name          string
	Symbol        string
	ImageURL      string
	FdvUSD        float64
	MarketCapUSD  float64
	VolumeUSDH1   float64
	ReserveUSD    float64
	PoolCreatedAt time.Time
}

// jsonAPIResponse is the top-level JSON:API envelope.
type jsonAPIResponse struct {
	Data     []jsonAPIPool    `json:"data"`
	Included []jsonAPIInclude `json:"included"`
}

type jsonAPIPool struct {
	ID            string         `json:"id"`
	Type          string         `json:"type"`
	Attributes    poolAttributes `json:"attributes"`
	Relationships struct {
		BaseToken struct {
			Data struct {
				ID   string `json:"id"`
				Type string `json:"type"`
			} `json:"data"`
		} `json:"base_token"`
	} `json:"relationships"`
}

type poolAttributes struct {
	Address       string            `json:"address"`
	Name          string            `json:"name"`
	PoolCreatedAt string            `json:"pool_created_at"`
	FdvUSD        jsonFloat         `json:"fdv_usd"`
	MarketCapUSD  jsonFloat         `json:"market_cap_usd"`
	ReserveUSD    jsonFloat         `json:"reserve_in_usd"`
	VolumeUSD     map[string]string `json:"volume_usd"`
}

type jsonAPIInclude struct {
	ID         string `json:"id"`
	Type       string `json:"type"`
	Attributes struct {
		Address  string `json:"address"`
		Name     string `json:"name"`
		Symbol   string `json:"symbol"`
		ImageURL string `json:"image_url"`
	} `json:"attributes"`
}

// jsonFloat handles GeckoTerminal's string-encoded floats.
type jsonFloat float64

func (f *jsonFloat) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		var val float64
		if _, err := fmt.Sscanf(s, "%f", &val); err == nil {
			*f = jsonFloat(val)
		}
		return nil
	}
	var val float64
	if err := json.Unmarshal(data, &val); err != nil {
		*f = 0
		return nil
	}
	*f = jsonFloat(val)
	return nil
}

// FetchPools fetches pump.fun pools from GeckoTerminal with base token info.
func (c *GeckoClient) FetchPools(ctx context.Context, page int) ([]PoolEntry, error) {
	time.Sleep(2 * time.Second) // 30 req/min rate limit

	url := fmt.Sprintf("%s/networks/solana/dexes/pump-fun/pools?page=%d&include=base_token", geckoBaseURL, page)
	c.log.Debug("fetching pools", "url", url, "page", page)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch pools: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("gecko pools status %d: %s", resp.StatusCode, string(body))
	}

	var result jsonAPIResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode pools: %w", err)
	}

	// Build token lookup from included data.
	tokenInfo := make(map[string]jsonAPIInclude)
	for _, inc := range result.Included {
		if inc.Type == "token" {
			tokenInfo[inc.ID] = inc
		}
	}

	var pools []PoolEntry
	for _, p := range result.Data {
		createdAt, err := time.Parse(time.RFC3339, p.Attributes.PoolCreatedAt)
		if err != nil {
			c.log.Warn("skip pool: bad created_at", "pool", p.Attributes.Address, "error", err)
			continue
		}

		entry := PoolEntry{
			PoolAddress:   p.Attributes.Address,
			FdvUSD:        float64(p.Attributes.FdvUSD),
			MarketCapUSD:  float64(p.Attributes.MarketCapUSD),
			ReserveUSD:    float64(p.Attributes.ReserveUSD),
			PoolCreatedAt: createdAt,
		}

		// Parse h1 volume from the map.
		if v, ok := p.Attributes.VolumeUSD["h1"]; ok {
			fmt.Sscanf(v, "%f", &entry.VolumeUSDH1)
		}

		// Look up base token info from included data.
		baseTokenID := p.Relationships.BaseToken.Data.ID
		if info, ok := tokenInfo[baseTokenID]; ok {
			entry.TokenAddress = info.Attributes.Address
			entry.Name = info.Attributes.Name
			entry.Symbol = info.Attributes.Symbol
			entry.ImageURL = info.Attributes.ImageURL
		}

		if entry.TokenAddress == "" {
			continue
		}

		pools = append(pools, entry)
	}

	c.log.Debug("fetched pools", "page", page, "count", len(pools))
	return pools, nil
}

// ohlcvResponse is the GeckoTerminal OHLCV response format.
type ohlcvResponse struct {
	Data struct {
		Attributes struct {
			OHLCVList [][]float64 `json:"ohlcv_list"`
		} `json:"attributes"`
	} `json:"data"`
}

// FetchOHLCV fetches 1-minute OHLCV candles for a pool.
func (c *GeckoClient) FetchOHLCV(ctx context.Context, poolAddress string, limit int) ([]Candle, error) {
	time.Sleep(2 * time.Second) // 30 req/min rate limit

	url := fmt.Sprintf("%s/networks/solana/pools/%s/ohlcv/minute?aggregate=1&limit=%d&currency=usd",
		geckoBaseURL, poolAddress, limit)
	c.log.Debug("fetching ohlcv", "pool", poolAddress, "url", url)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch ohlcv: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("gecko ohlcv status %d: %s", resp.StatusCode, string(body))
	}

	var result ohlcvResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode ohlcv: %w", err)
	}

	candles := make([]Candle, 0, len(result.Data.Attributes.OHLCVList))
	for _, row := range result.Data.Attributes.OHLCVList {
		if len(row) < 6 {
			continue
		}
		candles = append(candles, Candle{
			UnixTime: int64(row[0]),
			Open:     row[1],
			High:     row[2],
			Low:      row[3],
			Close:    row[4],
			Volume:   row[5],
		})
	}

	c.log.Debug("fetched candles", "pool", poolAddress, "count", len(candles))
	return candles, nil
}
