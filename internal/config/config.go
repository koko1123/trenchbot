package config

import (
	"fmt"

	"github.com/kelseyhightower/envconfig"
)

type Config struct {
	Mode string `envconfig:"MODE" default:"shadow"`

	// Solana
	SolanaRPCURL       string  `envconfig:"SOLANA_RPC_URL" default:"https://api.mainnet-beta.solana.com"`
	SolanaPrivateKey   string  `envconfig:"SOLANA_PRIVATE_KEY"`
	SolanaSnipeAmount  float64 `envconfig:"SOLANA_SNIPE_AMOUNT_SOL" default:"0.3"`

	// BNB Chain
	BNBRPCURL        string  `envconfig:"BNB_RPC_URL" default:"https://bsc-dataseed.binance.org"`
	BNBPrivateKey    string  `envconfig:"BNB_PRIVATE_KEY"`
	BNBSnipeAmount   float64 `envconfig:"BNB_SNIPE_AMOUNT_BNB" default:"0.05"`

	// PumpPortal
	PumpPortalWSURL    string `envconfig:"PUMPPORTAL_WS_URL" default:"wss://pumpportal.fun/api/data"`
	PumpPortalTradeURL string `envconfig:"PUMPPORTAL_TRADE_URL" default:"https://pumpportal.fun/api/trade-local"`

	// Four.meme
	FourMemeProxyContract string `envconfig:"FOURMEME_PROXY_CONTRACT" default:"0x5c952063c7fc8610FFDB798152D69F0B9550762b"`

	// Bitquery
	BitqueryAPIKey string `envconfig:"BITQUERY_API_KEY"`
	BitqueryAPIURL string `envconfig:"BITQUERY_API_URL" default:"https://streaming.bitquery.io/graphql"`

	// Risk
	MaxPositionsPerChain       int     `envconfig:"MAX_CONCURRENT_POSITIONS_PER_CHAIN" default:"5"`
	MaxPositionsTotal          int     `envconfig:"MAX_CONCURRENT_POSITIONS_TOTAL" default:"8"`
	MaxSnipesPerHour           int     `envconfig:"MAX_SNIPES_PER_HOUR" default:"10"`
	DailyLossLimitPct          float64 `envconfig:"DAILY_LOSS_LIMIT_PCT" default:"8"`
	TotalDrawdownLimitPct      float64 `envconfig:"TOTAL_DRAWDOWN_LIMIT_PCT" default:"40"`
	ConsecutiveLossPauseThresh int     `envconfig:"CONSECUTIVE_LOSS_PAUSE_THRESHOLD" default:"10"`

	// Gas
	GasBudgetSOL     float64 `envconfig:"GAS_BUDGET_SOL" default:"0.25"`
	GasBudgetBNB     float64 `envconfig:"GAS_BUDGET_BNB" default:"0.08"`
	GasCostPerTxSOL  float64 `envconfig:"GAS_COST_PER_TX_SOL" default:"0.000505"`
	GasCostPerTxBNB  float64 `envconfig:"GAS_COST_PER_TX_BNB" default:"0.0003"`
	MinGasReserveSOL float64 `envconfig:"MIN_GAS_RESERVE_SOL" default:"0.005"`
	MinGasReserveBNB float64 `envconfig:"MIN_GAS_RESERVE_BNB" default:"0.002"`

	// Filter
	MinScoreThreshold int `envconfig:"MIN_SCORE_THRESHOLD" default:"40"`

	// PostgreSQL
	DatabaseURL string `envconfig:"DATABASE_URL"`

	// Observability
	SentryDSN string `envconfig:"SENTRY_DSN"`
}

func Load() (*Config, error) {
	var cfg Config
	if err := envconfig.Process("", &cfg); err != nil {
		return nil, fmt.Errorf("loading config: %w", err)
	}
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("validating config: %w", err)
	}
	return &cfg, nil
}

func (c *Config) IsLive() bool {
	return c.Mode == "live"
}

func (c *Config) validate() error {
	if c.Mode != "shadow" && c.Mode != "live" {
		return fmt.Errorf("MODE must be 'shadow' or 'live', got %q", c.Mode)
	}
	if c.IsLive() {
		if c.SolanaPrivateKey == "" && c.BNBPrivateKey == "" {
			return fmt.Errorf("live mode requires at least one private key")
		}
	}

	if c.MaxPositionsPerChain <= 0 {
		return fmt.Errorf("MAX_CONCURRENT_POSITIONS_PER_CHAIN must be > 0, got %d", c.MaxPositionsPerChain)
	}
	if c.MaxPositionsTotal <= 0 {
		return fmt.Errorf("MAX_CONCURRENT_POSITIONS_TOTAL must be > 0, got %d", c.MaxPositionsTotal)
	}
	if c.MaxSnipesPerHour <= 0 {
		return fmt.Errorf("MAX_SNIPES_PER_HOUR must be > 0, got %d", c.MaxSnipesPerHour)
	}
	if c.DailyLossLimitPct <= 0 || c.DailyLossLimitPct > 100 {
		return fmt.Errorf("DAILY_LOSS_LIMIT_PCT must be in (0, 100], got %g", c.DailyLossLimitPct)
	}
	if c.ConsecutiveLossPauseThresh <= 0 {
		return fmt.Errorf("CONSECUTIVE_LOSS_PAUSE_THRESHOLD must be > 0, got %d", c.ConsecutiveLossPauseThresh)
	}
	if c.SolanaSnipeAmount <= 0 && c.BNBSnipeAmount <= 0 {
		return fmt.Errorf("at least one of SOLANA_SNIPE_AMOUNT_SOL or BNB_SNIPE_AMOUNT_BNB must be > 0")
	}
	if c.MinScoreThreshold < 0 || c.MinScoreThreshold > 100 {
		return fmt.Errorf("MIN_SCORE_THRESHOLD must be in [0, 100], got %d", c.MinScoreThreshold)
	}
	if c.GasBudgetSOL > 0 && c.MinGasReserveSOL > 0 && c.GasBudgetSOL < c.MinGasReserveSOL {
		return fmt.Errorf("GAS_BUDGET_SOL (%g) must be >= MIN_GAS_RESERVE_SOL (%g)", c.GasBudgetSOL, c.MinGasReserveSOL)
	}
	if c.GasBudgetBNB > 0 && c.MinGasReserveBNB > 0 && c.GasBudgetBNB < c.MinGasReserveBNB {
		return fmt.Errorf("GAS_BUDGET_BNB (%g) must be >= MIN_GAS_RESERVE_BNB (%g)", c.GasBudgetBNB, c.MinGasReserveBNB)
	}

	return nil
}
