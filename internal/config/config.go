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
	MaxSnipesPerHour           int     `envconfig:"MAX_SNIPES_PER_HOUR" default:"30"`
	HeatFullPct                float64 `envconfig:"HEAT_FULL_PCT" default:"15"`
	TotalDrawdownLimitPct      float64 `envconfig:"TOTAL_DRAWDOWN_LIMIT_PCT" default:"50"`
	ConsecutiveLossPauseThresh int     `envconfig:"CONSECUTIVE_LOSS_PAUSE_THRESHOLD" default:"15"`

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
	SnipeCooldownSec     int `envconfig:"SNIPE_COOLDOWN_SEC" default:"15"`

	// Exit strategy
	StopLossPct              float64 `envconfig:"STOP_LOSS_PCT" default:"40"`
	Tranche1X                float64 `envconfig:"TRANCHE1_X" default:"1.5"`
	UniversalTrailingThreshold float64 `envconfig:"TRAILING_THRESHOLD" default:"1.25"`
	UniversalTrailingStop    float64 `envconfig:"TRAILING_STOP_PCT" default:"20"`
	NoTradeTimeoutSec        int     `envconfig:"NO_TRADE_TIMEOUT_S" default:"90"`
	NoTradeMaxMult           float64 `envconfig:"NO_TRADE_MAX_MULT" default:"1.05"`
	NoTradeTimeoutFastSec    int     `envconfig:"NO_TRADE_TIMEOUT_FAST_S" default:"30"`
	NoTradeFastMaxMult       float64 `envconfig:"NO_TRADE_FAST_MAX_MULT" default:"0.90"`

	// Dead zone — skip sniping during hours with 0% historical win rate
	DeadZoneEnabled          bool `envconfig:"DEAD_ZONE_ENABLED" default:"true"`
	DeadZoneStartUTC         int  `envconfig:"DEAD_ZONE_START_UTC" default:"10"`
	DeadZoneEndUTC           int  `envconfig:"DEAD_ZONE_END_UTC" default:"13"`
	MinTradesBeforeBuy       int     `envconfig:"MIN_TRADES_BEFORE_BUY" default:"3"`
	TradeObservationSecs     int     `envconfig:"TRADE_OBSERVATION_SECS" default:"5"`

	// Order flow intelligence (Phase 1)
	MinOFIThreshold          float64 `envconfig:"MIN_OFI_THRESHOLD" default:"0.15"`
	MaxObservationGrowthPct  float64 `envconfig:"MAX_OBSERVATION_GROWTH_PCT" default:"500"`
	MinObservationGrowthPct  float64 `envconfig:"MIN_OBSERVATION_GROWTH_PCT" default:"0"`
	MinTradeTimingCV         float64 `envconfig:"MIN_TRADE_TIMING_CV" default:"0.3"`

	// On-chain intelligence (Phase 2)
	HeliusAPIKey             string  `envconfig:"HELIUS_API_KEY"`
	HolderCheckEnabled       bool    `envconfig:"HOLDER_CHECK_ENABLED" default:"true"`
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

	// Liquidity-aware sizing: cap entry size so we don't move the bonding curve too much.
	MaxTradeImpactPct        float64 `envconfig:"MAX_TRADE_IMPACT_PCT" default:"2.0"`

	// Auto gas refueling (sells worst position when gas runs low)
	GasRefuelEnabled         bool    `envconfig:"GAS_REFUEL_ENABLED" default:"false"`
	GasRefuelThreshold       float64 `envconfig:"GAS_REFUEL_THRESHOLD" default:"0.0015"`
	GasRefuelCooldownMin     int     `envconfig:"GAS_REFUEL_COOLDOWN_MIN" default:"5"`

	// Quantitative selection engine
	MinLiquidityVelocity     float64 `envconfig:"MIN_LIQUIDITY_VELOCITY" default:"0.15"`
	MaxLiquidityVelocity     float64 `envconfig:"MAX_LIQUIDITY_VELOCITY" default:"0.30"`
	MaxOFIThreshold          float64 `envconfig:"MAX_OFI_THRESHOLD" default:"0"`
	OFIToxicLow              float64 `envconfig:"OFI_TOXIC_LOW" default:"0.60"`
	OFIToxicHigh             float64 `envconfig:"OFI_TOXIC_HIGH" default:"0.95"`
	MaxOFIDeceleration       float64 `envconfig:"MAX_OFI_DECELERATION" default:"-0.3"`
	MaxCurveProgressEntry    float64 `envconfig:"MAX_CURVE_PROGRESS_ENTRY" default:"0.10"`
	MaxTradeEntropy          float64 `envconfig:"MAX_TRADE_ENTROPY" default:"1.0"`
	MaxBuyCount              int     `envconfig:"MAX_BUY_COUNT" default:"7"`
	PreGradExitProgress      float64 `envconfig:"PRE_GRADUATION_EXIT_PROGRESS" default:"0.90"`
	CUSUMEnabled             bool    `envconfig:"CUSUM_ENABLED" default:"true"`
	AdaptiveObservation      bool    `envconfig:"ADAPTIVE_OBSERVATION" default:"false"`
	ObservationMaxSecs       int     `envconfig:"OBSERVATION_MAX_SECS" default:"10"`
	SurvivalModelPath        string  `envconfig:"SURVIVAL_MODEL_PATH" default:"survival_model.json"`
	ThompsonSamplingEnabled  bool    `envconfig:"THOMPSON_SAMPLING_ENABLED" default:"true"`

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

	// Capital sweep — move excess idle SOL to a protected address
	BankAddress        string  `envconfig:"BANK_ADDRESS"`
	SweepReserveSOL    float64 `envconfig:"SWEEP_RESERVE_SOL" default:"10"`
	SweepIdleThreshold float64 `envconfig:"SWEEP_IDLE_THRESHOLD" default:"0.3"`
	SweepIdleMinutes   int     `envconfig:"SWEEP_IDLE_MINUTES" default:"60"`
	SweepCooldownMin   int     `envconfig:"SWEEP_COOLDOWN_MIN" default:"10"`
	SweepMinSOL        float64 `envconfig:"SWEEP_MIN_SOL" default:"0.5"`

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
		if c.TotalDrawdownLimitPct <= 0 || c.TotalDrawdownLimitPct > 100 {
			return fmt.Errorf("live mode: TOTAL_DRAWDOWN_LIMIT_PCT must be in (0, 100], got %g", c.TotalDrawdownLimitPct)
		}
		if c.StopLossPct <= 0 || c.StopLossPct > 100 {
			return fmt.Errorf("live mode: STOP_LOSS_PCT must be in (0, 100], got %g", c.StopLossPct)
		}
		if c.SentryDSN == "" {
			return fmt.Errorf("live mode: SENTRY_DSN is required for alerting")
		}
		if c.SlippagePctSOL > 49 {
			return fmt.Errorf("live mode: SLIPPAGE_PCT_SOL cannot exceed 49, got %d", c.SlippagePctSOL)
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
