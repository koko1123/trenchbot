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

// Refueler monitors SOL gas balance and automatically sells the worst-performing
// open position when gas drops below a threshold. All transactions are SOL-denominated.
type Refueler struct {
	solClient  *solanaclient.Client
	store      *state.Store
	monitor    *monitor.Monitor
	log        *slog.Logger
	shadow     bool

	threshold  float64       // gas balance below which to refuel
	cooldown   time.Duration // min time between refuel attempts
	lastRefuel time.Time
}

// RefuelerConfig holds configuration for the gas refueler.
type RefuelerConfig struct {
	Threshold   float64 // SOL balance below which to refuel (default 0.0015)
	CooldownMin int     // minutes between refuel attempts (default 5)
	Shadow      bool
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
		threshold: cfg.Threshold,
		cooldown:  cooldown,
	}
}

// Check evaluates gas balance and sells the worst position if gas is critical.
// Call this periodically (e.g., every 30 seconds from the capital ticker).
func (r *Refueler) Check(ctx context.Context) {
	gasBalance := r.store.GetGasBalance(state.ChainSolana)
	if gasBalance >= r.threshold {
		return
	}

	// Enforce cooldown.
	if !r.lastRefuel.IsZero() && time.Since(r.lastRefuel) < r.cooldown {
		return
	}

	r.log.Warn("gas balance critical, selling worst position",
		"gas_balance", gasBalance,
		"threshold", r.threshold,
	)

	if err := r.sellWorstPosition(ctx); err == nil {
		r.lastRefuel = time.Now()
	} else {
		r.log.Error("gas refuel failed: no positions to sell", "err", err)
	}
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
