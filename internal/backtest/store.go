package backtest

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "github.com/lib/pq"
)

type Store struct {
	db *sql.DB
}

func NewStore(databaseURL string) (*Store, error) {
	db, err := sql.Open("postgres", databaseURL)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) CreateTables(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS tokens (
			address         TEXT PRIMARY KEY,
			pool_address    TEXT NOT NULL UNIQUE,
			name            TEXT NOT NULL DEFAULT '',
			symbol          TEXT NOT NULL DEFAULT '',
			image_url       TEXT NOT NULL DEFAULT '',
			fdv_usd         DOUBLE PRECISION,
			market_cap_usd  DOUBLE PRECISION,
			volume_usd_h1   DOUBLE PRECISION,
			reserve_usd     DOUBLE PRECISION,
			pool_created_at TIMESTAMPTZ NOT NULL,
			fetched_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);

		CREATE INDEX IF NOT EXISTS idx_tokens_created ON tokens (pool_created_at);

		ALTER TABLE tokens ADD COLUMN IF NOT EXISTS creator TEXT NOT NULL DEFAULT '';
		CREATE INDEX IF NOT EXISTS idx_tokens_creator ON tokens (creator) WHERE creator != '';

		CREATE TABLE IF NOT EXISTS candles (
			pool_address TEXT NOT NULL REFERENCES tokens(pool_address),
			unix_time    BIGINT NOT NULL,
			open         DOUBLE PRECISION NOT NULL,
			high         DOUBLE PRECISION NOT NULL,
			low          DOUBLE PRECISION NOT NULL,
			close        DOUBLE PRECISION NOT NULL,
			volume       DOUBLE PRECISION NOT NULL,
			PRIMARY KEY (pool_address, unix_time)
		);
	`)
	return err
}

// TokenRow represents a token to insert.
type TokenRow struct {
	Address       string
	PoolAddress   string
	Name          string
	Symbol        string
	ImageURL      string
	FdvUSD        float64
	MarketCapUSD  float64
	VolumeUSDH1   float64
	ReserveUSD    float64
	PoolCreatedAt time.Time
}

func (s *Store) InsertToken(ctx context.Context, t TokenRow) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO tokens (address, pool_address, name, symbol, image_url, fdv_usd, market_cap_usd, volume_usd_h1, reserve_usd, pool_created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (address) DO NOTHING
	`, t.Address, t.PoolAddress, t.Name, t.Symbol, t.ImageURL, t.FdvUSD, t.MarketCapUSD, t.VolumeUSDH1, t.ReserveUSD, t.PoolCreatedAt)
	return err
}

// Candle represents a single OHLCV candle.
type Candle struct {
	UnixTime int64
	Open     float64
	High     float64
	Low      float64
	Close    float64
	Volume   float64
}

func (s *Store) InsertCandles(ctx context.Context, poolAddress string, candles []Candle) error {
	if len(candles) == 0 {
		return nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO candles (pool_address, unix_time, open, high, low, close, volume)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (pool_address, unix_time) DO NOTHING
	`)
	if err != nil {
		return fmt.Errorf("prepare: %w", err)
	}
	defer stmt.Close()

	for _, c := range candles {
		if _, err := stmt.ExecContext(ctx, poolAddress, c.UnixTime, c.Open, c.High, c.Low, c.Close, c.Volume); err != nil {
			return fmt.Errorf("insert candle: %w", err)
		}
	}

	return tx.Commit()
}

func (s *Store) HasCandles(ctx context.Context, poolAddress string) (bool, error) {
	var exists bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM candles WHERE pool_address = $1)`, poolAddress).Scan(&exists)
	return exists, err
}

// StoredToken is a token with its candle data, as read from the database.
type StoredToken struct {
	Address       string
	PoolAddress   string
	Name          string
	Symbol        string
	ImageURL      string
	FdvUSD        float64
	MarketCapUSD  float64
	VolumeUSDH1   float64
	ReserveUSD    float64
	PoolCreatedAt time.Time
	Candles       []Candle
}

func (s *Store) QueryTokens(ctx context.Context, start, end time.Time) ([]StoredToken, error) {
	// First get all tokens in date range that have candles.
	rows, err := s.db.QueryContext(ctx, `
		SELECT t.address, t.pool_address, t.name, t.symbol, t.image_url,
		       t.fdv_usd, t.market_cap_usd, t.volume_usd_h1, t.reserve_usd, t.pool_created_at
		FROM tokens t
		WHERE t.pool_created_at >= $1 AND t.pool_created_at < $2
		  AND EXISTS (SELECT 1 FROM candles c WHERE c.pool_address = t.pool_address)
		ORDER BY t.pool_created_at
	`, start, end)
	if err != nil {
		return nil, fmt.Errorf("query tokens: %w", err)
	}
	defer rows.Close()

	var tokens []StoredToken
	for rows.Next() {
		var t StoredToken
		if err := rows.Scan(&t.Address, &t.PoolAddress, &t.Name, &t.Symbol, &t.ImageURL,
			&t.FdvUSD, &t.MarketCapUSD, &t.VolumeUSDH1, &t.ReserveUSD, &t.PoolCreatedAt); err != nil {
			return nil, fmt.Errorf("scan token: %w", err)
		}
		tokens = append(tokens, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Load candles for each token.
	for i := range tokens {
		candles, err := s.loadCandles(ctx, tokens[i].PoolAddress)
		if err != nil {
			return nil, fmt.Errorf("load candles for %s: %w", tokens[i].PoolAddress, err)
		}
		tokens[i].Candles = candles
	}

	return tokens, nil
}

func (s *Store) loadCandles(ctx context.Context, poolAddress string) ([]Candle, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT unix_time, open, high, low, close, volume
		FROM candles
		WHERE pool_address = $1
		ORDER BY unix_time
	`, poolAddress)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var candles []Candle
	for rows.Next() {
		var c Candle
		if err := rows.Scan(&c.UnixTime, &c.Open, &c.High, &c.Low, &c.Close, &c.Volume); err != nil {
			return nil, err
		}
		candles = append(candles, c)
	}
	return candles, rows.Err()
}
