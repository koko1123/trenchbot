package backtest

import (
	"time"

	"github.com/kelseyhightower/envconfig"
)

type BacktestConfig struct {
	DatabaseURL    string  `envconfig:"DATABASE_URL" required:"true"`
	StartDate      string  `envconfig:"BACKTEST_START_DATE" required:"true"`
	EndDate        string  `envconfig:"BACKTEST_END_DATE" required:"true"`
	StartingEquity float64 `envconfig:"BACKTEST_EQUITY" default:"1200"`
	MinScore       int     `envconfig:"BACKTEST_MIN_SCORE" default:"60"`
}

func LoadBacktestConfig() (BacktestConfig, error) {
	var cfg BacktestConfig
	err := envconfig.Process("", &cfg)
	return cfg, err
}

func (c BacktestConfig) ParsedStartDate() (time.Time, error) {
	return time.Parse("2006-01-02", c.StartDate)
}

func (c BacktestConfig) ParsedEndDate() (time.Time, error) {
	return time.Parse("2006-01-02", c.EndDate)
}

type WatcherConfig struct {
	DatabaseURL  string `envconfig:"DATABASE_URL" required:"true"`
	PollInterval int    `envconfig:"WATCHER_POLL_INTERVAL_SEC" default:"300"`
	OHLCVLimit   int    `envconfig:"WATCHER_OHLCV_LIMIT" default:"60"`
	MaxPages     int    `envconfig:"WATCHER_MAX_PAGES" default:"10"`
}

func LoadWatcherConfig() (WatcherConfig, error) {
	var cfg WatcherConfig
	err := envconfig.Process("", &cfg)
	return cfg, err
}
