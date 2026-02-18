#!/usr/bin/env python3
"""
Train a Cox Proportional Hazards model on PumpFun token survival data.
Outputs a JSON file compatible with internal/flow/survival.go SurvivalModel.

Usage:
    # Export data from Postgres:
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

    # Train:
    python3 scripts/train_survival.py token_obs.csv

    # Deploy:
    SURVIVAL_MODEL_PATH=./survival_model.json
"""
import json
import sys
import numpy as np
import pandas as pd
from lifelines import CoxPHFitter
from lifelines.statistics import proportional_hazard_test

FEATURES = [
    "liquidity_velocity",
    "ofi",
    "ofi_acceleration",
    "trade_entropy",
    "timing_cv",
    "bot_buy_count",
    "buy_count",
    "curve_progress",
    "filter_score",
]

# Death = token failed. Censored = token survived past our exit.
DEATH_REASONS = {
    "stop-loss",
    "stale-position",
    "stale-max-hold",
    "no-trade-activity",
    "force-close-honeypot",
    "sell-pressure",
    "universal-trailing-stop",
    "gas-refuel",
}


def main():
    path = sys.argv[1] if len(sys.argv) > 1 else "token_obs.csv"
    df = pd.read_csv(path)

    print(f"Loaded {len(df)} rows")
    print(f"  Events (deaths): {df['event'].sum():.0f}")
    print(f"  Censored:        {(1 - df['event']).sum():.0f}")

    # Clean
    df = df.dropna(subset=FEATURES + ["duration_minutes", "event"])
    df = df[df["duration_minutes"] > 0]

    # Clip outliers at 1st/99th percentile
    for col in FEATURES:
        lo, hi = df[col].quantile([0.01, 0.99])
        df[col] = df[col].clip(lo, hi)

    # Scale filter_score to 0-1 range
    df["filter_score"] = df["filter_score"] / 100.0

    print(f"After cleaning: {len(df)} rows")

    n_events = int(df["event"].sum())
    if n_events < 50:
        print(f"\nWARNING: Only {n_events} events. Need at least 90 (10 per feature).")
        print("Model will be unreliable. Consider using DefaultSurvivalModel() instead.")
        if n_events < 20:
            print("Too few events to train. Exiting.")
            sys.exit(1)

    # Regularization: more aggressive with less data
    penalizer = 0.1
    if n_events < 100:
        penalizer = 0.5
    elif n_events < 200:
        penalizer = 0.3

    print(f"Using penalizer={penalizer} (based on {n_events} events)")

    # Fit Cox PH
    cox_df = df[FEATURES + ["duration_minutes", "event"]].copy()
    cph = CoxPHFitter(penalizer=penalizer)
    cph.fit(cox_df, duration_col="duration_minutes", event_col="event", show_progress=True)
    cph.print_summary()

    # Validate PH assumption
    try:
        results = proportional_hazard_test(cph, cox_df, time_transform="rank")
        print("\nProportional Hazards Test (p < 0.05 = violation):")
        print(results.summary)
        violations = results.summary[results.summary["p"] < 0.05]
        if len(violations) > 0:
            print(f"WARNING: PH violated for: {list(violations.index)}")
    except Exception as e:
        print(f"PH test failed (non-critical): {e}")

    # Extract and negate coefficients.
    # Cox PH: positive coef = higher hazard = worse survival.
    # survival.go: positive beta = better survival.
    # So we negate.
    params = cph.params_
    betas_raw = [float(params[f]) for f in FEATURES]
    betas = [-b for b in betas_raw]

    print("\nCoefficients (raw Cox PH → negated for survival.go):")
    for f, raw, neg in zip(FEATURES, betas_raw, betas):
        direction = "protective" if raw < 0 else "hazardous"
        print(f"  {f:25s}: {raw:+.4f} ({direction}) → {neg:+.4f}")

    # Model quality
    c_index = cph.concordance_index_
    print(f"\nConcordance Index: {c_index:.4f}")
    if c_index < 0.55:
        print("  Model barely beats random. Use hardcoded defaults as fallback.")
    elif c_index < 0.65:
        print("  Marginal. Use with caution.")
    else:
        print("  Acceptable. Deploy and retrain monthly.")

    # Write JSON
    model = {"features": FEATURES, "betas": betas}
    output = "survival_model.json"
    with open(output, "w") as f:
        json.dump(model, f, indent=2)

    print(f"\nModel written to {output}")
    print("Deploy with: SURVIVAL_MODEL_PATH=./survival_model.json")


if __name__ == "__main__":
    main()
