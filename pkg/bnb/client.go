package bnb

import (
	"context"
	"crypto/ecdsa"
	"fmt"
	"log/slog"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
)

type Client struct {
	eth     *ethclient.Client
	privKey *ecdsa.PrivateKey
	address common.Address
	chainID *big.Int
	log     *slog.Logger
}

func NewClient(rpcURL string, privateKeyHex string, log *slog.Logger) (*Client, error) {
	eth, err := ethclient.Dial(rpcURL)
	if err != nil {
		return nil, fmt.Errorf("connecting to BNB RPC: %w", err)
	}

	client := &Client{
		eth: eth,
		log: log,
	}

	if privateKeyHex != "" {
		pk, err := crypto.HexToECDSA(privateKeyHex)
		if err != nil {
			return nil, fmt.Errorf("parsing BNB private key: %w", err)
		}
		client.privKey = pk
		client.address = crypto.PubkeyToAddress(pk.PublicKey)
	}

	chainID, err := eth.ChainID(context.Background())
	if err != nil {
		return nil, fmt.Errorf("getting chain ID: %w", err)
	}
	client.chainID = chainID

	return client, nil
}

func (c *Client) Address() common.Address {
	return c.address
}

func (c *Client) GetBalance(ctx context.Context) (*big.Int, error) {
	if c.address == (common.Address{}) {
		return nil, fmt.Errorf("BNB client has no address configured (missing private key)")
	}
	return c.eth.BalanceAt(ctx, c.address, nil)
}

func (c *Client) GetBalanceBNB(ctx context.Context) (float64, error) {
	if c.address == (common.Address{}) {
		return 0, fmt.Errorf("BNB client has no address configured (missing private key)")
	}
	bal, err := c.GetBalance(ctx)
	if err != nil {
		return 0, err
	}
	// Convert wei to BNB
	f := new(big.Float).SetInt(bal)
	bnb, _ := new(big.Float).Quo(f, big.NewFloat(1e18)).Float64()
	return bnb, nil
}

func (c *Client) Eth() *ethclient.Client {
	return c.eth
}

func (c *Client) PrivateKey() *ecdsa.PrivateKey {
	return c.privKey
}

func (c *Client) ChainID() *big.Int {
	return c.chainID
}
