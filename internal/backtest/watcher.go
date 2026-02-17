package backtest

import (
	"context"
	"log/slog"
	"time"
)

// RunWatcher polls GeckoTerminal for new pump.fun pools and writes them to
// Postgres. It runs until the context is cancelled.
func RunWatcher(ctx context.Context, cfg WatcherConfig, store *Store, client *GeckoClient, log *slog.Logger) error {
	pollInterval := time.Duration(cfg.PollInterval) * time.Second
	log.Info("watcher started", "poll_interval", pollInterval, "max_pages", cfg.MaxPages, "ohlcv_limit", cfg.OHLCVLimit)

	// Run first poll immediately, then on interval.
	if err := poll(ctx, cfg, store, client, log); err != nil {
		log.Error("poll failed", "error", err)
	}

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Info("watcher stopped")
			return nil
		case <-ticker.C:
			if err := poll(ctx, cfg, store, client, log); err != nil {
				log.Error("poll failed", "error", err)
			}
		}
	}
}

func poll(ctx context.Context, cfg WatcherConfig, store *Store, client *GeckoClient, log *slog.Logger) error {
	discovered := 0
	newTokens := 0
	ohlcvFetched := 0

	for page := 1; page <= cfg.MaxPages; page++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		pools, err := client.FetchPools(ctx, page)
		if err != nil {
			log.Error("fetch pools failed", "page", page, "error", err)
			break
		}

		if len(pools) == 0 {
			break
		}

		discovered += len(pools)

		for _, p := range pools {
			if ctx.Err() != nil {
				return ctx.Err()
			}

			row := TokenRow{
				Address:       p.TokenAddress,
				PoolAddress:   p.PoolAddress,
				Name:          p.Name,
				Symbol:        p.Symbol,
				ImageURL:      p.ImageURL,
				FdvUSD:        p.FdvUSD,
				MarketCapUSD:  p.MarketCapUSD,
				VolumeUSDH1:   p.VolumeUSDH1,
				ReserveUSD:    p.ReserveUSD,
				PoolCreatedAt: p.PoolCreatedAt,
			}

			if err := store.InsertToken(ctx, row); err != nil {
				log.Error("insert token failed", "address", p.TokenAddress, "error", err)
				continue
			}

			// Only fetch OHLCV for tokens that don't have candles yet.
			has, err := store.HasCandles(ctx, p.PoolAddress)
			if err != nil {
				log.Error("check candles failed", "pool", p.PoolAddress, "error", err)
				continue
			}
			if has {
				continue
			}

			newTokens++

			candles, err := client.FetchOHLCV(ctx, p.PoolAddress, cfg.OHLCVLimit)
			if err != nil {
				log.Error("fetch ohlcv failed", "pool", p.PoolAddress, "error", err)
				continue
			}

			if err := store.InsertCandles(ctx, p.PoolAddress, candles); err != nil {
				log.Error("insert candles failed", "pool", p.PoolAddress, "error", err)
				continue
			}
			ohlcvFetched++
		}
	}

	log.Info("poll complete", "discovered", discovered, "new", newTokens, "ohlcv_fetched", ohlcvFetched)
	return nil
}
