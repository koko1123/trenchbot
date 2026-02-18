package solana

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/gagliardetto/solana-go"
	"github.com/gagliardetto/solana-go/rpc"
)

type Client struct {
	rpc    *rpc.Client
	wallet solana.PrivateKey
	log    *slog.Logger
}

func NewClient(rpcURL string, privateKeyBase58 string, log *slog.Logger) (*Client, error) {
	c := rpc.New(rpcURL)
	client := &Client{
		rpc: c,
		log: log,
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
