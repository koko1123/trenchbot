package scanner

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
)

type FourMemeScanner struct {
	apiURL        string
	apiKey        string
	proxyContract string
	log           *slog.Logger
	client        *http.Client
}

func NewFourMemeScanner(apiURL, apiKey, proxyContract string, log *slog.Logger) *FourMemeScanner {
	return &FourMemeScanner{
		apiURL:        apiURL,
		apiKey:        apiKey,
		proxyContract: proxyContract,
		log:           log,
		client:        &http.Client{Timeout: 30 * time.Second},
	}
}

func (s *FourMemeScanner) Chain() state.Chain {
	return state.ChainBNB
}

type bitqueryResponse struct {
	Data struct {
		EVM struct {
			Events []struct {
				Transaction struct {
					Hash   string `json:"Hash"`
					From   string `json:"From"`
				} `json:"Transaction"`
				Log struct {
					SmartContract string `json:"SmartContract"`
				} `json:"Log"`
				Arguments []struct {
					Name  string      `json:"Name"`
					Value interface{} `json:"Value"`
				} `json:"Arguments"`
				Block struct {
					Time string `json:"Time"`
				} `json:"Block"`
			} `json:"Events"`
		} `json:"EVM"`
	} `json:"data"`
}

const bitqueryNewTokenQuery = `{
  EVM(network: bsc) {
    Events(
      where: {
        Log: {
          SmartContract: {
            is: "%s"
          }
        }
      }
      limit: {count: 10}
      orderBy: {descending: Block_Time}
    ) {
      Transaction {
        Hash
        From
      }
      Log {
        SmartContract
      }
      Arguments {
        Name
        Value
      }
      Block {
        Time
      }
    }
  }
}`

func (s *FourMemeScanner) Scan(ctx context.Context, out chan<- NewToken) error {
	seen := make(map[string]bool)

	s.log.Info("four.meme scanner started", "poll_interval", "5s")

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			tokens, err := s.pollNewTokens(ctx, s.proxyContract)
			if err != nil {
				s.log.Error("four.meme poll error", "err", err)
				continue
			}
			for _, t := range tokens {
				if seen[t.Address] {
					continue
				}
				seen[t.Address] = true
				if len(seen) > 50000 {
					seen = make(map[string]bool)
				}
				s.log.Info("new token detected",
					"chain", "bnb",
					"address", t.Address,
					"creator", t.Creator,
				)
				select {
				case out <- t:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
		}
	}
}

func (s *FourMemeScanner) pollNewTokens(ctx context.Context, proxyContract string) ([]NewToken, error) {
	query := fmt.Sprintf(bitqueryNewTokenQuery, proxyContract)
	body, _ := json.Marshal(map[string]string{"query": query})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.apiURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if s.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+s.apiKey)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("bitquery request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("bitquery status %d: %s", resp.StatusCode, string(respBody[:min(len(respBody), 200)]))
	}

	var result bitqueryResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("parsing response: %w", err)
	}

	var tokens []NewToken
	for _, evt := range result.Data.EVM.Events {
		tokenAddr := ""
		for _, arg := range evt.Arguments {
			if arg.Name == "token" {
				if addr, ok := arg.Value.(string); ok {
					tokenAddr = addr
				}
			}
		}
		if tokenAddr == "" {
			continue
		}

		tokens = append(tokens, NewToken{
			Chain:     state.ChainBNB,
			Address:   tokenAddr,
			Creator:   evt.Transaction.From,
			Timestamp: time.Now(),
			Metadata: map[string]interface{}{
				"txHash": evt.Transaction.Hash,
			},
		})
	}

	return tokens, nil
}
