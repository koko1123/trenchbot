package reporter

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	_ "github.com/lib/pq"
)

// TradeRow represents a sniper trade stored in Postgres.
type TradeRow struct {
	ID           string
	Chain        string
	TokenAddress string
	TokenSymbol  string
	Side         string // "buy" or "sell"
	Price        float64
	Amount       float64
	PnLPct       *float64 // nil for buys
	ExitReason   *string  // nil for buys
	GasCost      float64
	Shadow       bool
	CreatedAt    time.Time
}

// ReportStore manages Postgres persistence for trades, reports, and creator lookups.
type ReportStore struct {
	db *sql.DB
}

// NewReportStore opens a connection to Postgres and creates tables.
func NewReportStore(databaseURL string) (*ReportStore, error) {
	db, err := sql.Open("postgres", databaseURL)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("ping database: %w", err)
	}

	s := &ReportStore{db: db}
	if err := s.createTables(context.Background()); err != nil {
		db.Close()
		return nil, fmt.Errorf("create tables: %w", err)
	}
	return s, nil
}

func (s *ReportStore) createTables(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS sniper_trades (
			id            TEXT PRIMARY KEY,
			chain         TEXT NOT NULL,
			token_address TEXT NOT NULL,
			token_symbol  TEXT NOT NULL,
			side          TEXT NOT NULL,
			price         DOUBLE PRECISION NOT NULL,
			amount        DOUBLE PRECISION NOT NULL,
			pnl_pct       DOUBLE PRECISION,
			exit_reason   TEXT,
			gas_cost      DOUBLE PRECISION,
			shadow        BOOLEAN NOT NULL DEFAULT true,
			created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);

		CREATE INDEX IF NOT EXISTS idx_sniper_trades_created ON sniper_trades (created_at);
		CREATE INDEX IF NOT EXISTS idx_sniper_trades_chain ON sniper_trades (chain, created_at);

		CREATE TABLE IF NOT EXISTS reports (
			id              SERIAL PRIMARY KEY,
			period          TEXT NOT NULL,
			period_start    TIMESTAMPTZ NOT NULL,
			period_end      TIMESTAMPTZ NOT NULL,

			open_positions  INT NOT NULL,
			trades_closed   INT NOT NULL,
			win_count       INT NOT NULL,
			loss_count      INT NOT NULL,
			win_rate        DOUBLE PRECISION NOT NULL,
			total_pnl_pct   DOUBLE PRECISION NOT NULL,
			best_trade      DOUBLE PRECISION,
			worst_trade     DOUBLE PRECISION,
			avg_pnl         DOUBLE PRECISION,

			gas_remaining   DOUBLE PRECISION,
			gas_spent       DOUBLE PRECISION,
			drawdown_pct    DOUBLE PRECISION,
			cb_status       TEXT,

			exits_by_reason JSONB,
			raw_snapshot    JSONB,

			created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);

		CREATE INDEX IF NOT EXISTS idx_reports_period ON reports (period, period_start);
	`)
	return err
}

// InsertTrade writes a sniper trade to Postgres.
func (s *ReportStore) InsertTrade(ctx context.Context, t TradeRow) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO sniper_trades (id, chain, token_address, token_symbol, side, price, amount, pnl_pct, exit_reason, gas_cost, shadow, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		ON CONFLICT (id) DO NOTHING
	`, t.ID, t.Chain, t.TokenAddress, t.TokenSymbol, t.Side, t.Price, t.Amount, t.PnLPct, t.ExitReason, t.GasCost, t.Shadow, t.CreatedAt)
	return err
}

// InsertReport writes a periodic report snapshot to Postgres.
func (s *ReportStore) InsertReport(ctx context.Context, snap Snapshot) error {
	exitsJSON, err := json.Marshal(snap.ExitsByReason)
	if err != nil {
		return fmt.Errorf("marshal exits: %w", err)
	}
	rawJSON, err := json.Marshal(snap)
	if err != nil {
		return fmt.Errorf("marshal snapshot: %w", err)
	}

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO reports (period, period_start, period_end, open_positions, trades_closed,
			win_count, loss_count, win_rate, total_pnl_pct, best_trade, worst_trade, avg_pnl,
			gas_remaining, gas_spent, drawdown_pct, cb_status, exits_by_reason, raw_snapshot)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18)
	`, snap.Period, snap.PeriodStart, snap.PeriodEnd, snap.OpenPositions, snap.TradesClosed,
		snap.WinCount, snap.LossCount, snap.WinRate, snap.TotalPnLPct,
		snap.BestTrade, snap.WorstTrade, snap.AvgPnL,
		snap.GasRemaining, snap.GasSpent, snap.DrawdownPct, snap.CBStatus,
		exitsJSON, rawJSON)
	return err
}

// QueryTrades reads trades for a given chain and time range.
func (s *ReportStore) QueryTrades(ctx context.Context, chain string, since, until time.Time) ([]TradeRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, chain, token_address, token_symbol, side, price, amount,
		       pnl_pct, exit_reason, gas_cost, shadow, created_at
		FROM sniper_trades
		WHERE chain = $1 AND created_at >= $2 AND created_at < $3
		ORDER BY created_at
	`, chain, since, until)
	if err != nil {
		return nil, fmt.Errorf("query trades: %w", err)
	}
	defer rows.Close()

	var trades []TradeRow
	for rows.Next() {
		var t TradeRow
		if err := rows.Scan(&t.ID, &t.Chain, &t.TokenAddress, &t.TokenSymbol, &t.Side,
			&t.Price, &t.Amount, &t.PnLPct, &t.ExitReason, &t.GasCost, &t.Shadow, &t.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan trade: %w", err)
		}
		trades = append(trades, t)
	}
	return trades, rows.Err()
}

// QueryAllTrades reads all trades in a time range (all chains).
func (s *ReportStore) QueryAllTrades(ctx context.Context, since, until time.Time) ([]TradeRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, chain, token_address, token_symbol, side, price, amount,
		       pnl_pct, exit_reason, gas_cost, shadow, created_at
		FROM sniper_trades
		WHERE created_at >= $1 AND created_at < $2
		ORDER BY created_at
	`, since, until)
	if err != nil {
		return nil, fmt.Errorf("query trades: %w", err)
	}
	defer rows.Close()

	var trades []TradeRow
	for rows.Next() {
		var t TradeRow
		if err := rows.Scan(&t.ID, &t.Chain, &t.TokenAddress, &t.TokenSymbol, &t.Side,
			&t.Price, &t.Amount, &t.PnLPct, &t.ExitReason, &t.GasCost, &t.Shadow, &t.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan trade: %w", err)
		}
		trades = append(trades, t)
	}
	return trades, rows.Err()
}

// UpsertTokenCreator stores a token's creator in the shared tokens table.
func (s *ReportStore) UpsertTokenCreator(ctx context.Context, address, creator string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE tokens SET creator = $2 WHERE address = $1 AND creator = ''
	`, address, creator)
	return err
}

// CreatorHistory returns the number of tokens this creator has launched
// and how many had negative price action (last close < 20% of first open).
func (s *ReportStore) CreatorHistory(ctx context.Context, creator string) (total int, rugCount int, err error) {
	err = s.db.QueryRowContext(ctx, `
		SELECT
			COUNT(DISTINCT t.address) AS total,
			COUNT(DISTINCT CASE WHEN c_last.close < c_first.open * 0.2 THEN t.address END) AS rug_count
		FROM tokens t
		JOIN LATERAL (
			SELECT open FROM candles WHERE pool_address = t.pool_address ORDER BY unix_time ASC LIMIT 1
		) c_first ON true
		JOIN LATERAL (
			SELECT close FROM candles WHERE pool_address = t.pool_address ORDER BY unix_time DESC LIMIT 1
		) c_last ON true
		WHERE t.creator = $1 AND t.creator != ''
	`, creator).Scan(&total, &rugCount)
	if err != nil {
		return 0, 0, fmt.Errorf("creator history: %w", err)
	}
	return total, rugCount, nil
}

// Close closes the database connection.
func (s *ReportStore) Close() error {
	return s.db.Close()
}
