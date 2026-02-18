package executor

import (
	"context"

	"github.com/cindocode/trenchbot/internal/state"
)

type BuyParams struct {
	Chain        state.Chain
	TokenAddress string
	TokenSymbol  string
	Amount       float64 // native token amount (SOL or BNB)
	Shadow       bool
}

type SellParams struct {
	Chain        state.Chain
	TokenAddress string
	TokenSymbol  string
	AmountPct    float64 // percentage of position to sell (0-100)
	TokenBalance float64 // total token balance held (raw token count)
	MaxSlippage  int     // override slippage for desperate sells (0 = use default)
	Shadow       bool
}

type BuyResult struct {
	Success     bool
	TxHash      string
	Price       float64
	Amount      float64
	TokenAmount float64 // raw token count received from buy
	GasCost     float64 // gas paid in native token (SOL or BNB)
	Error       error
}

type SellResult struct {
	Success  bool
	TxHash   string
	Price    float64
	Amount   float64
	PnLPct   float64
	GasCost  float64 // gas paid in native token (SOL or BNB)
	Error    error
}

type Executor interface {
	Buy(ctx context.Context, params BuyParams) BuyResult
	Sell(ctx context.Context, params SellParams) SellResult
	Chain() state.Chain
}

// PriceFeed can return the current price of a token. Executors that support
// real-time pricing should implement this.
type PriceFeed interface {
	CurrentPrice(ctx context.Context, tokenAddress string) (float64, error)
}

// SafePrefix returns the first n characters of s, or s itself if shorter than n.
func SafePrefix(s string, n int) string {
	if len(s) < n {
		return s
	}
	return s[:n]
}
