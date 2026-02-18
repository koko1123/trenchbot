# trenchbot

Automated memecoin sniper for Solana (PumpFun) and BNB Chain (Four.meme). Runs in shadow mode by default — logs trades without executing on-chain.

## Strategy

### Entry

New tokens are discovered in real-time via websocket (PumpFun) and GraphQL subscription (Bitquery for Four.meme). Each token is scored on a 100-point scale across four dimensions:

| Dimension | What it checks | Max |
|---|---|---|
| Metadata quality | Name, symbol, description length, image URL | 25 |
| Creator analysis | Creator address present, wallet activity | 25 |
| Momentum | Market cap > $1K, initial buy size | 25 |
| Chain-specific | SOL market cap signal, transaction hash | 25 |

Tokens scoring below 60 are rejected. This filters out ~65% of tokens (most naked rugs with sparse metadata) but deliberately allows polished rugs through — the exit strategy handles those.

### Position Sizing

Base size: 0.3 SOL (Solana) / 0.05 BNB (BNB Chain), scaled by score:

- Score 60 → 0.75x base
- Score 80 → 1.0x base
- Score 100+ → 1.25x base (capped)

Size is halved when daily losses exceed 4% of peak equity. Sizing is refused entirely when gas balance drops below reserve (10 round-trips worth).

### Exit (Tranche-Based)

Positions are monitored every 5 seconds with exits evaluated in strict priority:

1. **Stop-loss at -50%** → sell 100% of remaining position
2. **Tranche 1 at 2x** → sell 25%
3. **Tranche 2 at 5x** → sell 50%
4. **Trailing stop** (after both tranches) → sell remaining when price drops 40% from peak
5. **Stale exit at 30 min** → sell remaining if still below 1.5x

This means the worst case on any single position is a 50% loss on the position size (0.15 SOL at default sizing).

### Risk Controls

- **Max 5 positions per chain, 8 total** — prevents overexposure
- **Circuit breaker** — pauses for 1 hour after 10 consecutive losses, halts permanently at 50% drawdown from peak equity
- **Rate limiting** — max 10 snipes per hour per chain
- **Gas tracking** — separate gas balance from trading equity, refuses to trade when gas runs low
- **Gas-adjusted P&L** — all reported P&L includes entry + exit gas costs

### Gas Budget

Gas is tracked as a separate balance from trading equity (default 0.25 SOL / 0.08 BNB, ~$50 each). Every buy and sell deducts the per-transaction gas cost. When gas drops below the reserve threshold, the bot stops opening new positions.

## Architecture

```
cmd/sniper/     → live trading bot
cmd/watcher/    → GeckoTerminal → PostgreSQL data collector
cmd/backtest/   → replay historical data through the pipeline

internal/
  scanner/      → websocket/GraphQL token discovery
  filter/       → 100-point scoring system
  risk/         → position sizer + circuit breaker
  executor/     → PumpFun (Solana) + Four.meme (BNB) trade execution
  monitor/      → exit strategy engine (tranches, stops, stale)
  state/        → in-memory position/trade/gas tracking
  simulation/   → synthetic token generator + replay engine
  backtest/     → historical replay from PostgreSQL
  notify/       → Sentry event tracking
  config/       → environment-based configuration
```

## Running

```bash
# Shadow mode (default — logs trades, no on-chain execution)
make shadow

# Run simulation (synthetic adversarial tokens, 6 simulated hours)
make simulate

# Run the data watcher (polls GeckoTerminal, writes to Postgres)
make watcher

# Run backtest against historical data
make backtest

# Full CI: lint + test + simulate + build
make ci
```

## Configuration

All config is via environment variables. Key ones:

| Variable | Default | Description |
|---|---|---|
| `MODE` | `shadow` | `shadow` or `live` |
| `SENTRY_DSN` | — | Sentry DSN for event tracking |
| `SOLANA_RPC_URL` | mainnet | Solana RPC endpoint |
| `SOLANA_PRIVATE_KEY` | — | Required for live mode |
| `SOLANA_SNIPE_AMOUNT_SOL` | `0.3` | Base position size |
| `GAS_BUDGET_SOL` | `0.25` | Starting gas balance (~$50) |
| `MIN_SCORE_THRESHOLD` | `60` | Minimum filter score to buy |
| `MAX_SNIPES_PER_HOUR` | `10` | Rate limit |
| `DAILY_LOSS_LIMIT_PCT` | `8` | Daily loss limit (% of peak equity) |
| `DATABASE_URL` | — | PostgreSQL URL (watcher/backtest only) |

## Deployment

Docker + Railway:

```bash
docker build -t trenchbot .
```

The `Dockerfile` builds the sniper bot. The `railway.json` configures Railway for always-on deployment. Set environment variables in Railway's dashboard.
