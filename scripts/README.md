# Survival Model Training

## Overview

The survival model predicts P(token survives > T minutes) from early trade signals observed during the pre-buy observation window. It runs at inference time as a simple dot product (9 features x 9 betas), adding negligible latency to the pipeline.

The model is a Cox Proportional Hazards model trained offline in Python and exported as a JSON coefficient file that the Go bot loads at startup.

## Data Requirements

### Minimum Data

| Events (deaths) | Reliability | Recommendation |
|-----------------|-------------|----------------|
| < 50            | Unusable    | Use `DefaultSurvivalModel()` hardcoded coefficients |
| 50-100          | Unreliable  | Train with `penalizer=0.5`, treat with skepticism |
| 100-200         | Usable      | Train with `penalizer=0.3`, retrain weekly |
| 200-500         | Good        | C-index will be meaningful, retrain monthly |
| 500+            | Excellent   | Coefficient signs will be stable across retrains |

**Events = positions that ended in failure** (stop-loss, stale-position, no-trade-activity, sell-pressure, etc.). Profitable exits (tranche-1, trailing-stop while up) are censored observations, not events.

**Rule of thumb:** 10 events per feature (EPV). We have 9 features, so minimum 90 events. At ~3-10 buys/hour with ~30% loss rate, this takes 30-90 hours of live trading.

### Accumulation Timeline

| Shadow mode buys/hr | Loss rate | Time to 90 events | Time to 200 events |
|---------------------|-----------|--------------------|--------------------|
| 5                   | 30%       | ~60 hours          | ~133 hours         |
| 10                  | 30%       | ~30 hours          | ~67 hours          |
| 5                   | 50%       | ~36 hours          | ~80 hours          |

Shadow mode trades are included in training data (they have the same features, just no real execution). Run shadow alongside live to accumulate data faster.

## Features Stored

All 9 features are computed during the pre-buy observation window and written to the `token_observations` Postgres table at buy time:

| Feature | Description | Expected Sign |
|---------|-------------|---------------|
| `liquidity_velocity` | Net SOL per trade | Positive (higher = better survival) |
| `ofi` | Order flow imbalance [-1, 1] | Positive |
| `ofi_acceleration` | Second-half vs first-half OFI change | Positive |
| `trade_entropy` | Shannon entropy of trade size distribution | Positive |
| `timing_cv` | Coefficient of variation of inter-trade intervals | Negative (low CV = bot-like) |
| `bot_buy_count` | Number of buys in first 2 seconds | Negative |
| `buy_count` | Total buys in observation window | Mildly positive |
| `curve_progress` | Bonding curve graduation progress [0, 1] | Negative (higher = less upside) |
| `filter_score` | Pre-buy filter score | Mildly positive |

## Censoring Rules

Getting censoring right is critical. Wrong censoring biases every coefficient.

**event = 1 (death/failure):**
- `stop-loss` - dropped below entry
- `stale-position` - flatlined, no buyers
- `stale-max-hold` - held too long below tranche-1
- `no-trade-activity` - dead token
- `force-close-honeypot` - rug detected
- `sell-pressure` - coordinated dump
- `universal-trailing-stop` (if triggered below 1.3x)
- `gas-refuel` - force-sold for gas

**event = 0 (censored/survived):**
- `tranche-1` - sold at 1.5x profit
- `tranche-2` - sold at 5x
- `trailing-stop` - had a good run
- `early-trailing-stop` - was up 3x+
- `pre-graduation-trailing` - near graduation, took profit
- Still open positions

## Training

### Prerequisites

```bash
pip install lifelines pandas numpy
```

### Export Data

```bash
psql $DATABASE_URL -c "\COPY (
  SELECT liquidity_velocity, ofi, ofi_acceleration, trade_entropy, timing_cv,
         bot_buy_count, buy_count, curve_progress, filter_score,
         hold_duration_sec / 60.0 AS duration_minutes,
         CASE WHEN exit_reason IN ('stop-loss','stale-position','stale-max-hold',
              'no-trade-activity','force-close-honeypot','sell-pressure',
              'universal-trailing-stop','gas-refuel') THEN 1 ELSE 0 END AS event
  FROM token_observations
  WHERE chain='solana' AND hold_duration_sec IS NOT NULL AND hold_duration_sec > 0
) TO STDOUT CSV HEADER" > token_obs.csv
```

### Train

```bash
python3 scripts/train_survival.py token_obs.csv
```

This outputs `survival_model.json` in the current directory.

### Validate

The script prints:
- **Concordance Index (C-index):** 0.5 = random, 0.7+ = good, 0.8+ = excellent
- **Proportional Hazards test:** p < 0.05 for any feature means PH assumption is violated for that feature (consider removing or stratifying)
- **Coefficient signs:** Verify they match the "Expected Sign" column above. If `liquidity_velocity` has a negative beta (= higher velocity predicts worse survival), something is wrong with your data.

## Deployment

### Where to Store the Model

The model is stored in the `models/` directory at the repo root and bundled into the Docker image at build time:

```
trenchbot/
  models/
    survival_model.json    # trained model (committed to git)
    .gitkeep               # keeps directory tracked when empty
  Dockerfile               # copies models/ into image
```

### Workflow

```bash
# 1. Train
python3 scripts/train_survival.py token_obs.csv

# 2. Save to repo
cp survival_model.json models/survival_model.json

# 3. Commit and build
git add models/survival_model.json
git commit -m "Update survival model (C-index: 0.XX)"
docker build -t trenchbot .
```

The Dockerfile copies `models/` into the image and sets `SURVIVAL_MODEL_PATH=/models/survival_model.json` automatically. No network calls or external storage needed.

### Environment Variable

To override the model path at runtime:

```bash
export SURVIVAL_MODEL_PATH=/custom/path/survival_model.json
```

If `SURVIVAL_MODEL_PATH` is empty, the survival model is disabled. No inference is performed and the pipeline falls through to other filters.

### Model Format

```json
{
  "features": [
    "liquidity_velocity",
    "ofi",
    "ofi_acceleration",
    "trade_entropy",
    "timing_cv",
    "bot_buy_count",
    "buy_count",
    "curve_progress",
    "filter_score"
  ],
  "betas": [2.0, 1.0, 0.5, 0.3, -0.2, -0.3, 0.1, -0.5, 0.02]
}
```

Positive beta = higher feature value predicts better survival.
Negative beta = higher feature value predicts worse survival.

The runtime inference is a dot product: `score = sum(beta_i * feature_i)`. Tokens with `score < -0.5` are skipped.

## Retraining Schedule

- **Weekly** during the first month (data is growing fast, coefficients may shift)
- **Monthly** once you have 500+ events (coefficients should be stable)
- **On-demand** after major pipeline changes (new filters, changed observation window, etc.)

Compare the new model's C-index with the old one. If the new model's C-index is lower, keep the old model.

## Cold-Start (No Data Yet)

If you don't have enough data yet, the bot uses `DefaultSurvivalModel()` with hardcoded coefficients derived from PumpFun academic research (Ferretti et al., arXiv:2602.14860). These give highest weight to `liquidity_velocity` (the strongest graduation predictor) and are a reasonable starting point.

To bootstrap faster, scrape historical PumpFun tokens from the PumpPortal API (`/api/coins` + `/api/trades/all`), compute a 5-feature subset, and train an initial model within a week.
