package curve

import "math"

// PumpFun bonding curve constants.
// The curve uses a constant product (x*y=k) model with virtual reserves.
const (
	InitVirtualTokens = 1_073_000_000.0 // initial virtual token reserves (6-decimal normalized)
	InitVirtualSOL    = 30.0            // initial virtual SOL reserves
	RealTokenSupply   = 793_100_000.0   // purchasable tokens on the curve
	TotalTokenSupply  = 1_000_000_000.0 // total token supply
	K                 = InitVirtualTokens * InitVirtualSOL // constant product invariant
	GraduationVSOL    = 115.0           // virtual SOL at graduation (~30 virtual + 85 real)
	GraduationRealSOL = 85.0            // real SOL accumulated at graduation
)

// SpotPrice returns the current price per token in SOL.
func SpotPrice(virtualSOL, virtualTokens float64) float64 {
	if virtualTokens <= 0 {
		return 0
	}
	return virtualSOL / virtualTokens
}

// PriceImpact returns the price impact of a buy as a fraction (0.01 = 1%).
// On a constant product curve, impact = buySOL / virtualSOL.
func PriceImpact(buySOL, virtualSOL float64) float64 {
	if virtualSOL <= 0 {
		return 0
	}
	return buySOL / virtualSOL
}

// TokensReceived returns the number of tokens received for a given SOL buy.
func TokensReceived(buySOL, virtualTokens, virtualSOL float64) float64 {
	if virtualSOL+buySOL <= 0 {
		return 0
	}
	return virtualTokens * buySOL / (virtualSOL + buySOL)
}

// SOLReceived returns the SOL received for selling a given number of tokens.
func SOLReceived(sellTokens, virtualTokens, virtualSOL float64) float64 {
	if virtualTokens+sellTokens <= 0 {
		return 0
	}
	return virtualSOL * sellTokens / (virtualTokens + sellTokens)
}

// EstimateVirtualSOL estimates the current virtual SOL reserves from mcapSOL.
// mcapSOL ≈ spotPrice * totalSupply ≈ (vSOL/vTokens) * totalSupply.
// Since vTokens = K/vSOL, we get mcapSOL ≈ vSOL² * totalSupply / K.
// Solving: vSOL = sqrt(mcapSOL * K / totalSupply).
func EstimateVirtualSOL(mcapSOL float64) float64 {
	if mcapSOL <= 0 {
		return InitVirtualSOL
	}
	vs := math.Sqrt(mcapSOL * K / TotalTokenSupply)
	if vs < InitVirtualSOL {
		return InitVirtualSOL
	}
	return vs
}

// Progress returns 0.0-1.0 representing how far the token is toward graduation.
// 0.0 = just created, 1.0 = ready to graduate.
func Progress(mcapSOL float64) float64 {
	vs := EstimateVirtualSOL(mcapSOL)
	realSOL := vs - InitVirtualSOL
	if realSOL <= 0 {
		return 0
	}
	p := realSOL / GraduationRealSOL
	if p > 1 {
		return 1
	}
	return p
}

// MaxDumpImpact estimates the price impact if the top holder dumps their entire position.
// Returns a fraction (0.05 = 5% price drop).
func MaxDumpImpact(topHolderPct, mcapSOL float64) float64 {
	if topHolderPct <= 0 || mcapSOL <= 0 {
		return 0
	}
	vs := EstimateVirtualSOL(mcapSOL)
	vt := K / vs

	// Estimate tokens sold so far.
	tokensSold := InitVirtualTokens - vt
	if tokensSold <= 0 {
		return 0
	}

	// Top holder's tokens.
	topHolderTokens := tokensSold * topHolderPct / 100.0

	// Price impact of dumping those tokens.
	if vt <= 0 {
		return 0
	}
	return topHolderTokens / vt
}

// MaxEntrySOL returns the maximum buy size that keeps price impact below maxImpactPct.
// On a constant product curve, impact = buySOL / virtualSOL, so maxBuy = virtualSOL * maxImpact.
func MaxEntrySOL(mcapSOL, maxImpactPct float64) float64 {
	if maxImpactPct <= 0 {
		return 0
	}
	vs := EstimateVirtualSOL(mcapSOL)
	return vs * maxImpactPct / 100.0
}

// OptimalSlippage computes the exact slippage tolerance for a buy, based on
// bonding curve math. Adds a buffer for concurrent trades.
// Returns slippage as an integer percentage (e.g., 5 = 5%).
func OptimalSlippage(buySOL, mcapSOL float64) int {
	vs := EstimateVirtualSOL(mcapSOL)

	// Base impact from our trade.
	impact := PriceImpact(buySOL, vs) * 100 // as percentage

	// Buffer for concurrent trades: 3x base impact.
	// Rationale: other bots may be buying simultaneously, shifting the curve.
	slippage := int(math.Ceil(impact * 3))

	if slippage < 3 {
		slippage = 3 // minimum floor
	}
	if slippage > 49 {
		slippage = 49 // PumpFun max
	}
	return slippage
}
