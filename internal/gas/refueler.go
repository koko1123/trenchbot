package gas

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"time"

	"github.com/cindocode/trenchbot/internal/monitor"
	"github.com/cindocode/trenchbot/internal/state"
	solanaclient "github.com/cindocode/trenchbot/pkg/solana"
)

// Refueler monitors gas balance and automatically refuels when it drops below
// a threshold. Strategy: try USDC->SOL swap first, fall back to selling the
// worst-performing open position.
type Refueler struct {
	solClient  *solanaclient.Client
	store      *state.Store
	monitor    *monitor.Monitor
	log        *slog.Logger
	shadow     bool

	usdcMint   string
	threshold  float64       // gas balance below which to refuel
	amount     float64       // SOL worth to recover per refuel
	cooldown   time.Duration // min time between refuel attempts
	lastRefuel time.Time
}

// RefuelerConfig holds configuration for the gas refueler.
type RefuelerConfig struct {
	USDCMint   string
	Threshold  float64 // SOL balance below which to refuel (default 0.0015)
	Amount     float64 // SOL worth to swap/recover per refuel (default 0.05)
	CooldownMin int    // minutes between refuel attempts (default 5)
	Shadow     bool
}

// NewRefueler creates a gas refueler.
func NewRefueler(
	solClient *solanaclient.Client,
	store *state.Store,
	mon *monitor.Monitor,
	cfg RefuelerConfig,
	log *slog.Logger,
) *Refueler {
	if cfg.Threshold <= 0 {
		cfg.Threshold = 0.0015
	}
	if cfg.Amount <= 0 {
		cfg.Amount = 0.05
	}
	cooldown := time.Duration(cfg.CooldownMin) * time.Minute
	if cooldown <= 0 {
		cooldown = 5 * time.Minute
	}
	return &Refueler{
		solClient: solClient,
		store:     store,
		monitor:   mon,
		log:       log,
		shadow:    cfg.Shadow,
		usdcMint:  cfg.USDCMint,
		threshold: cfg.Threshold,
		amount:    cfg.Amount,
		cooldown:  cooldown,
	}
}

// Check evaluates gas balance and triggers refueling if needed.
// Call this periodically (e.g., every 30 seconds from the risk ticker).
func (r *Refueler) Check(ctx context.Context) {
	gasBalance := r.store.GetGasBalance(state.ChainSolana)
	if gasBalance >= r.threshold {
		return
	}

	// Enforce cooldown.
	if !r.lastRefuel.IsZero() && time.Since(r.lastRefuel) < r.cooldown {
		return
	}

	r.log.Warn("gas balance critical, attempting refuel",
		"gas_balance", gasBalance,
		"threshold", r.threshold,
	)

	// Step 1: Try USDC -> SOL swap.
	if err := r.swapUSDCToSOL(ctx); err == nil {
		r.lastRefuel = time.Now()
		return
	} else {
		r.log.Debug("USDC swap failed, trying position sell", "err", err)
	}

	// Step 2: Force-sell worst position.
	if err := r.sellWorstPosition(ctx); err == nil {
		r.lastRefuel = time.Now()
		return
	} else {
		r.log.Error("gas refuel failed: no USDC and no positions to sell", "err", err)
	}
}

// swapUSDCToSOL attempts to swap USDC to SOL via Jupiter.
func (r *Refueler) swapUSDCToSOL(ctx context.Context) error {
	if r.usdcMint == "" {
		return fmt.Errorf("USDC mint not configured")
	}

	// Check USDC balance.
	usdcBalance, err := r.solClient.GetTokenBalance(ctx, r.usdcMint)
	if err != nil {
		return fmt.Errorf("check USDC balance: %w", err)
	}

	if usdcBalance < 1.0 { // less than $1 USDC
		return fmt.Errorf("insufficient USDC balance: %.2f", usdcBalance)
	}

	// Calculate USDC amount needed for desired SOL.
	// Rough estimate: use Jupiter quote to get the exact amount.
	// USDC has 6 decimals. Request a quote for the desired SOL amount.
	// We'll request enough USDC for r.amount SOL.
	// As a rough estimate, assume ~$150/SOL and add 5% buffer.
	usdcNeeded := r.amount * 160 // rough estimate, Jupiter will optimize
	if usdcNeeded > usdcBalance {
		usdcNeeded = usdcBalance // use all available if not enough
	}
	usdcAmountRaw := uint64(usdcNeeded * 1e6) // USDC has 6 decimals

	solMint := "So11111111111111111111111111111111111111112" // wrapped SOL

	if r.shadow {
		r.log.Info("SHADOW GAS REFUEL: would swap USDC to SOL",
			"usdc_amount", usdcNeeded,
			"target_sol", r.amount,
		)
		// Simulate gas recovery in shadow mode.
		r.store.SetGasBalance(state.ChainSolana, r.store.GetGasBalance(state.ChainSolana)+r.amount)
		return nil
	}

	// Get Jupiter quote.
	quote, err := r.solClient.JupiterQuote(ctx, r.usdcMint, solMint, usdcAmountRaw, 100)
	if err != nil {
		return fmt.Errorf("jupiter quote: %w", err)
	}

	// Execute swap.
	txHash, err := r.solClient.JupiterSwap(ctx, quote)
	if err != nil {
		return fmt.Errorf("jupiter swap: %w", err)
	}

	r.log.Info("gas refueled via USDC swap",
		"usdc_spent", usdcNeeded,
		"tx", txHash,
	)

	// Refresh actual balance after swap.
	if bal, err := r.solClient.GetBalanceWithFallback(ctx); err == nil {
		r.store.SetGasBalance(state.ChainSolana, bal)
	}

	return nil
}

// sellWorstPosition force-sells the worst-performing open position to recover gas.
func (r *Refueler) sellWorstPosition(ctx context.Context) error {
	worst := r.monitor.WorstPosition(state.ChainSolana)
	if worst == nil {
		return fmt.Errorf("no open positions to sell")
	}

	mult := 0.0
	if worst.EntryPrice > 0 {
		mult = worst.CurrentPrice / worst.EntryPrice
	}

	r.log.Warn("selling worst position for gas",
		"token", worst.TokenSymbol,
		"multiplier", math.Round(mult*100)/100,
		"position_id", worst.ID,
	)

	if r.shadow {
		r.log.Info("SHADOW GAS REFUEL: would sell worst position",
			"token", worst.TokenSymbol,
			"multiplier", mult,
		)
		// Simulate gas recovery.
		recovered := worst.Amount * mult * 0.95 // 5% slippage estimate
		r.store.SetGasBalance(state.ChainSolana, r.store.GetGasBalance(state.ChainSolana)+recovered)
		return nil
	}

	err := r.monitor.ForceExitPosition(ctx, worst.ID, "gas-refuel")
	if err != nil {
		return fmt.Errorf("force sell failed: %w", err)
	}

	// Refresh balance after sell.
	if bal, err := r.solClient.GetBalanceWithFallback(ctx); err == nil {
		r.store.SetGasBalance(state.ChainSolana, bal)
	}

	return nil
}
