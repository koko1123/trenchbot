package capital

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/cindocode/trenchbot/internal/state"
	solanaclient "github.com/cindocode/trenchbot/pkg/solana"
)

// Sweeper monitors wallet balance and transfers excess idle capital to a
// protected bank address (e.g., multisig or hardware wallet). It only sweeps
// when excess capital has been idle above a threshold for a configurable period,
// so funds remain available during hot streaks and drawdown recovery.
type Sweeper struct {
	solClient *solanaclient.Client
	store     *state.Store
	log       *slog.Logger
	shadow    bool

	bankAddress    string
	reserveSOL     float64       // total SOL to keep in hot wallet
	idleThreshold  float64       // fraction of reserve that counts as "excess" (e.g., 0.3)
	idleDuration   time.Duration // how long excess must persist before sweep
	cooldown       time.Duration // min time between sweeps
	minSweep       float64       // don't sweep less than this

	excessSince time.Time // when excess was first detected (zero = no excess)
	lastSweep   time.Time
}

// SweeperConfig holds configuration for the capital sweeper.
type SweeperConfig struct {
	BankAddress   string  // destination pubkey for excess capital
	ReserveSOL    float64 // total SOL to keep in hot wallet (default 10)
	IdleThreshold float64 // sweep when excess > reserve * threshold (default 0.3)
	IdleMinutes   int     // minutes excess must be idle before sweep (default 60)
	CooldownMin   int     // minutes between sweeps (default 10)
	MinSweepSOL   float64 // minimum amount worth sweeping (default 0.5)
	Shadow        bool
}

// NewSweeper creates a capital sweeper. Returns nil if BankAddress is empty.
func NewSweeper(
	solClient *solanaclient.Client,
	store *state.Store,
	cfg SweeperConfig,
	log *slog.Logger,
) *Sweeper {
	if cfg.BankAddress == "" {
		return nil
	}
	if cfg.ReserveSOL <= 0 {
		cfg.ReserveSOL = 10.0
	}
	if cfg.IdleThreshold <= 0 {
		cfg.IdleThreshold = 0.3
	}
	if cfg.IdleMinutes <= 0 {
		cfg.IdleMinutes = 60
	}
	if cfg.CooldownMin <= 0 {
		cfg.CooldownMin = 10
	}
	if cfg.MinSweepSOL <= 0 {
		cfg.MinSweepSOL = 0.5
	}

	return &Sweeper{
		solClient:     solClient,
		store:         store,
		log:           log,
		shadow:        cfg.Shadow,
		bankAddress:   cfg.BankAddress,
		reserveSOL:    cfg.ReserveSOL,
		idleThreshold: cfg.IdleThreshold,
		idleDuration:  time.Duration(cfg.IdleMinutes) * time.Minute,
		cooldown:      time.Duration(cfg.CooldownMin) * time.Minute,
		minSweep:      cfg.MinSweepSOL,
	}
}

// Check evaluates whether excess capital has been idle long enough to sweep.
// Call this periodically (e.g., every 30 seconds from the capital ticker).
func (s *Sweeper) Check(ctx context.Context) {
	balance := s.store.GetGasBalance(state.ChainSolana)
	excess := balance - s.reserveSOL

	// Not enough excess to care about.
	if excess < s.reserveSOL*s.idleThreshold {
		if !s.excessSince.IsZero() {
			s.log.Debug("excess capital below threshold, resetting idle timer",
				"balance", balance,
				"reserve", s.reserveSOL,
				"excess", excess,
			)
		}
		s.excessSince = time.Time{}
		return
	}

	// First time noticing excess — start the clock.
	if s.excessSince.IsZero() {
		s.excessSince = time.Now()
		s.log.Info("excess capital detected, starting idle timer",
			"balance", balance,
			"excess_sol", fmt.Sprintf("%.4f", excess),
			"sweep_after", s.idleDuration,
		)
		return
	}

	// Excess hasn't been idle long enough.
	idleFor := time.Since(s.excessSince)
	if idleFor < s.idleDuration {
		s.log.Debug("excess capital idle, waiting",
			"idle_for", idleFor.Round(time.Second),
			"sweep_at", s.idleDuration,
		)
		return
	}

	// Enforce cooldown between sweeps.
	if !s.lastSweep.IsZero() && time.Since(s.lastSweep) < s.cooldown {
		return
	}

	// Re-check: balance may have changed during idle period.
	sweepAmount := balance - s.reserveSOL
	if sweepAmount < s.minSweep {
		s.excessSince = time.Time{}
		return
	}

	s.log.Info("sweeping excess capital to bank",
		"sweep_sol", fmt.Sprintf("%.4f", sweepAmount),
		"bank_address", s.bankAddress,
		"balance_before", fmt.Sprintf("%.4f", balance),
		"reserve", s.reserveSOL,
	)

	if s.shadow {
		s.log.Info("SHADOW SWEEP: would transfer to bank",
			"amount_sol", fmt.Sprintf("%.4f", sweepAmount),
			"bank", s.bankAddress,
		)
		s.lastSweep = time.Now()
		s.excessSince = time.Time{}
		return
	}

	sig, err := s.solClient.TransferSOL(ctx, s.bankAddress, sweepAmount)
	if err != nil {
		s.log.Error("capital sweep failed", "err", err, "amount", sweepAmount)
		return
	}

	s.log.Info("capital sweep complete",
		"tx_sig", sig,
		"swept_sol", fmt.Sprintf("%.4f", sweepAmount),
		"bank", s.bankAddress,
	)

	// Refresh balance after sweep.
	if bal, err := s.solClient.GetBalanceWithFallback(ctx); err == nil {
		s.store.SetGasBalance(state.ChainSolana, bal)
	}

	s.lastSweep = time.Now()
	s.excessSince = time.Time{}
}
