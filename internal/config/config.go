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

	// PumpPortal
	PumpPortalWSURL    string `envconfig:"PUMPPORTAL_WS_URL" default:"wss://pumpportal.fun/api/data"`
	PumpPortalTradeURL string `envconfig:"PUMPPORTAL_TRADE_URL" default:"https://pumpportal.fun/api/trade-local"`

	// Risk
	MaxPositionsPerChain       int     `envconfig:"MAX_CONCURRENT_POSITIONS_PER_CHAIN" default:"10"`
	MaxPositionsTotal          int     `envconfig:"MAX_CONCURRENT_POSITIONS_TOTAL" default:"15"`
	MaxSnipesPerHour           int     `envconfig:"MAX_SNIPES_PER_HOUR" default:"10"`
	HeatFullPct                float64 `envconfig:"HEAT_FULL_PCT" default:"15"`
	TotalDrawdownLimitPct      float64 `envconfig:"TOTAL_DRAWDOWN_LIMIT_PCT" default:"50"`
	ConsecutiveLossPauseThresh int     `envconfig:"CONSECUTIVE_LOSS_PAUSE_THRESHOLD" default:"10"`

	// Gas
	GasBudgetSOL     float64 `envconfig:"GAS_BUDGET_SOL" default:"0.25"`
	GasCostPerTxSOL  float64 `envconfig:"GAS_COST_PER_TX_SOL" default:"0.000505"`
	MinGasReserveSOL float64 `envconfig:"MIN_GAS_RESERVE_SOL" default:"0.005"`

	// Filter
	MinScoreThreshold    int  `envconfig:"MIN_SCORE_THRESHOLD" default:"55"`
	HoneypotCheckEnabled bool `envconfig:"HONEYPOT_CHECK_ENABLED" default:"true"`

	// Execution
	SlippagePctSOL       int `envconfig:"SLIPPAGE_PCT_SOL" default:"25"`
	MaxConcurrentBuys    int `envconfig:"MAX_CONCURRENT_BUYS" default:"10"`

	// Exit strategy
	StopLossPct              float64 `envconfig:"STOP_LOSS_PCT" default:"30"`
	Tranche1X                float64 `envconfig:"TRANCHE1_X" default:"1.5"`
	UniversalTrailingThreshold float64 `envconfig:"TRAILING_THRESHOLD" default:"1.15"`
	UniversalTrailingStop    float64 `envconfig:"TRAILING_STOP_PCT" default:"20"`
	NoTradeTimeoutSec        int     `envconfig:"NO_TRADE_TIMEOUT_S" default:"120"`
	NoTradeMaxMult           float64 `envconfig:"NO_TRADE_MAX_MULT" default:"1.1"`
	MinTradesBeforeBuy       int     `envconfig:"MIN_TRADES_BEFORE_BUY" default:"5"`
	TradeObservationSecs     int     `envconfig:"TRADE_OBSERVATION_SECS" default:"5"`

	// Order flow intelligence (Phase 1)
	MinOFIThreshold          float64 `envconfig:"MIN_OFI_THRESHOLD" default:"0.3"`
	MaxObservationGrowthPct  float64 `envconfig:"MAX_OBSERVATION_GROWTH_PCT" default:"500"`
	MinObservationGrowthPct  float64 `envconfig:"MIN_OBSERVATION_GROWTH_PCT" default:"0"`
	MinTradeTimingCV         float64 `envconfig:"MIN_TRADE_TIMING_CV" default:"0.3"`

	// On-chain intelligence (Phase 2)
	HeliusAPIKey             string  `envconfig:"HELIUS_API_KEY"`
	HolderCheckEnabled       bool    `envconfig:"HOLDER_CHECK_ENABLED" default:"false"`
	MaxTopHolderPct          float64 `envconfig:"MAX_TOP_HOLDER_PCT" default:"50"`
	BundleDetectionEnabled   bool    `envconfig:"BUNDLE_DETECTION_ENABLED" default:"false"`

	// Adaptive execution (Phase 3)
	DynamicSlippageEnabled   bool    `envconfig:"DYNAMIC_SLIPPAGE_ENABLED" default:"false"`
	SolanaRPCFallbackURLs    string  `envconfig:"SOLANA_RPC_FALLBACK_URLS"`

	// Kelly criterion sizing (Phase 4)
	KellyEnabled             bool    `envconfig:"KELLY_ENABLED" default:"false"`
	KellyWindowSize          int     `envconfig:"KELLY_WINDOW_SIZE" default:"50"`

	// Alpha monitoring (Phase 6)
	AlphaMonitorEnabled      bool    `envconfig:"ALPHA_MONITOR_ENABLED" default:"false"`

	// Dynamic position limits
	DynamicPositionLimits    bool    `envconfig:"DYNAMIC_POSITION_LIMITS" default:"false"`
	PositionScaleFactor      float64 `envconfig:"POSITION_SCALE_FACTOR" default:"3.0"`

	// Auto gas refueling
	GasRefuelEnabled         bool    `envconfig:"GAS_REFUEL_ENABLED" default:"false"`
	GasRefuelThreshold       float64 `envconfig:"GAS_REFUEL_THRESHOLD" default:"0.0015"`
	GasRefuelAmount          float64 `envconfig:"GAS_REFUEL_AMOUNT" default:"0.05"`
	GasRefuelCooldownMin     int     `envconfig:"GAS_REFUEL_COOLDOWN_MIN" default:"5"`
	USDCMint                 string  `envconfig:"USDC_MINT" default:"EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v"`

	// Bot dump exploitation (Phase 3D)
	BotDumpDelayEnabled bool `envconfig:"BOT_DUMP_DELAY_ENABLED" default:"false"`
	BotDumpDelaySec     int  `envconfig:"BOT_DUMP_DELAY_SEC" default:"90"`

	// Jito bundle submission (Phase 2A)
	JitoEnabled        bool   `envconfig:"JITO_ENABLED" default:"false"`
	JitoBlockEngineURL string `envconfig:"JITO_BLOCK_ENGINE_URL" default:"https://mainnet.block-engine.jito.wtf"`
	JitoTipLamports    uint64 `envconfig:"JITO_TIP_LAMPORTS" default:"10000"`

	// Wallet rotation (Phase 2B)
	WalletRotationEnabled bool   `envconfig:"WALLET_ROTATION_ENABLED" default:"false"`
	SolanaPrivateKeys     string `envconfig:"SOLANA_PRIVATE_KEYS"` // comma-separated (first is primary)

	// State persistence
	StateSnapshotPath string `envconfig:"STATE_SNAPSHOT_PATH" default:"state.json"`

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
		if c.SolanaPrivateKey == "" {
			return fmt.Errorf("live mode requires SOLANA_PRIVATE_KEY")
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
	if c.HeatFullPct <= 0 || c.HeatFullPct > 100 {
		return fmt.Errorf("HEAT_FULL_PCT must be in (0, 100], got %g", c.HeatFullPct)
	}
	if c.ConsecutiveLossPauseThresh <= 0 {
		return fmt.Errorf("CONSECUTIVE_LOSS_PAUSE_THRESHOLD must be > 0, got %d", c.ConsecutiveLossPauseThresh)
	}
	if c.SolanaSnipeAmount <= 0 {
		return fmt.Errorf("SOLANA_SNIPE_AMOUNT_SOL must be > 0")
	}
	if c.MinScoreThreshold < 0 || c.MinScoreThreshold > 100 {
		return fmt.Errorf("MIN_SCORE_THRESHOLD must be in [0, 100], got %d", c.MinScoreThreshold)
	}
	if c.GasBudgetSOL > 0 && c.MinGasReserveSOL > 0 && c.GasBudgetSOL < c.MinGasReserveSOL {
		return fmt.Errorf("GAS_BUDGET_SOL (%g) must be >= MIN_GAS_RESERVE_SOL (%g)", c.GasBudgetSOL, c.MinGasReserveSOL)
	}

	return nil
}
