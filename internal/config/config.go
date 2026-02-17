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

	// Filter
	MinScoreThreshold int `envconfig:"MIN_SCORE_THRESHOLD" default:"60"`

	// Notifications
	TelegramBotToken  string `envconfig:"TELEGRAM_BOT_TOKEN"`
	TelegramChatID    string `envconfig:"TELEGRAM_CHAT_ID"`
	DiscordWebhookURL string `envconfig:"DISCORD_WEBHOOK_URL"`
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
	return nil
}
