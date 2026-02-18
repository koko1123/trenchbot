package wallet

import (
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/gagliardetto/solana-go"
)

// Wallet represents a Solana wallet with its key pair.
type Wallet struct {
	PrivateKey solana.PrivateKey
	PublicKey  string // base58 encoded
}

// Pool manages a set of wallets for round-robin rotation.
type Pool struct {
	mu      sync.RWMutex
	wallets []*Wallet
	primary *Wallet // first wallet, used for consolidation
	next    uint64  // atomic counter for round-robin
}

// NewPool creates a wallet pool from a slice of base58-encoded private keys.
// The first key is designated as the primary wallet. If only one key is
// provided, the pool still works as a single-wallet pool.
// Returns an error if any key is invalid.
func NewPool(privateKeys []string) (*Pool, error) {
	if len(privateKeys) == 0 {
		return nil, fmt.Errorf("at least one private key is required")
	}

	wallets := make([]*Wallet, 0, len(privateKeys))
	for i, keyStr := range privateKeys {
		pk, err := solana.PrivateKeyFromBase58(keyStr)
		if err != nil {
			return nil, fmt.Errorf("parsing private key %d: %w", i, err)
		}
		wallets = append(wallets, &Wallet{
			PrivateKey: pk,
			PublicKey:  pk.PublicKey().String(),
		})
	}

	return &Pool{
		wallets: wallets,
		primary: wallets[0],
	}, nil
}

// Next returns the next wallet in round-robin rotation.
// Safe for concurrent use.
func (p *Pool) Next() *Wallet {
	n := atomic.AddUint64(&p.next, 1) - 1
	idx := n % uint64(len(p.wallets))
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.wallets[idx]
}

// Primary returns the primary (first) wallet.
func (p *Pool) Primary() *Wallet {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.primary
}

// All returns all wallets in the pool.
func (p *Pool) All() []*Wallet {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]*Wallet, len(p.wallets))
	copy(out, p.wallets)
	return out
}

// Count returns the number of wallets in the pool.
func (p *Pool) Count() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.wallets)
}
