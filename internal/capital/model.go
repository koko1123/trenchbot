package capital

import (
	"fmt"
	"math"
	"strings"

	"github.com/cindocode/trenchbot/internal/config"
)

// ModelInputs holds the parameters for the theoretical profit model.
type ModelInputs struct {
	// From config
	BaseSnipeSOL     float64 // SOLANA_SNIPE_AMOUNT_SOL
	MaxPositions     int     // MAX_CONCURRENT_POSITIONS_TOTAL
	MaxSnipesPerHour int     // MAX_SNIPES_PER_HOUR
	MaxImpactPct     float64 // MAX_TRADE_IMPACT_PCT
	StopLossPct      float64 // STOP_LOSS_PCT
	Tranche1X        float64 // TRANCHE1_X
	GasCostPerTx     float64 // GAS_COST_PER_TX_SOL
	SweepReserveSOL  float64 // SWEEP_RESERVE_SOL

	// Estimated or from shadow data
	FilterPassRate float64 // fraction of tokens passing all filters (0.002 default)
	AvgHoldMinutes float64 // average position hold time
	WinRate        float64 // fraction of trades that are profitable
	AvgWinPct      float64 // average winning trade PnL%
	AvgLossPct     float64 // average losing trade PnL% (positive number)
	TokensPerDay   int     // PumpFun launch rate
}

// ModelOutput holds the computed profit projections.
type ModelOutput struct {
	// Throughput
	MaxBuysPerHour      float64
	MaxBuysPerDay       float64
	RealisticBuysPerDay float64
	AvgPositionTurnover float64 // positions closed per hour

	// Per-trade economics
	AvgTradeSize         float64 // SOL per trade
	GrossEVPerTrade      float64 // SOL before costs
	GasCostPerRoundTrip  float64
	SlippageCostPerTrade float64
	NetEVPerTrade        float64

	// Daily projections
	DailyGrossProfit      float64
	DailyGasCost          float64
	DailySlippageCost     float64
	DailyNetProfit        float64
	MaxConcurrentExposure float64

	// Monthly projections
	MonthlyNetProfit     float64
	MonthlySweepEstimate float64
	AnnualizedROIPct     float64 // % return on reserve

	// Risk
	HalfKellyFraction   float64
	ExpectedMaxDrawdown float64 // % of equity
	RuinProbability     float64 // simplified estimate

	// Input echo (for display)
	InputWinRate   float64
	InputAvgWinPct float64
	InputAvgLossPct float64
}

// DefaultModelInputs creates model inputs from config with conservative
// estimates for the unknown parameters (filter pass rate, win rate, etc.).
func DefaultModelInputs(cfg *config.Config) ModelInputs {
	return ModelInputs{
		BaseSnipeSOL:     cfg.SolanaSnipeAmount,
		MaxPositions:     cfg.MaxPositionsTotal,
		MaxSnipesPerHour: cfg.MaxSnipesPerHour,
		MaxImpactPct:     cfg.MaxTradeImpactPct,
		StopLossPct:      cfg.StopLossPct,
		Tranche1X:        cfg.Tranche1X,
		GasCostPerTx:     cfg.GasCostPerTxSOL,
		SweepReserveSOL:  cfg.SweepReserveSOL,

		FilterPassRate: 0.002,  // 0.2% of tokens pass all filters
		AvgHoldMinutes: 15,     // 15 min average hold
		WinRate:        0.55,   // 55% win rate
		AvgWinPct:      30,     // +30% on winners
		AvgLossPct:     25,     // -25% on losers
		TokensPerDay:   10_000, // PumpFun daily launches
	}
}

// ComputeModel runs the theoretical profit model and returns projections.
func ComputeModel(in ModelInputs) ModelOutput {
	var out ModelOutput
	out.InputWinRate = in.WinRate
	out.InputAvgWinPct = in.AvgWinPct
	out.InputAvgLossPct = in.AvgLossPct

	// --- Throughput ---

	// Position turnover: how many positions close per hour.
	if in.AvgHoldMinutes > 0 {
		out.AvgPositionTurnover = float64(in.MaxPositions) * (60.0 / in.AvgHoldMinutes)
	}

	// Max buys/hour is the lesser of rate limit and position turnover.
	out.MaxBuysPerHour = math.Min(float64(in.MaxSnipesPerHour), out.AvgPositionTurnover)
	out.MaxBuysPerDay = out.MaxBuysPerHour * 24

	// Realistic buys/day: limited by token supply × filter pass rate.
	filterCapacity := float64(in.TokensPerDay) * in.FilterPassRate
	out.RealisticBuysPerDay = math.Min(filterCapacity, out.MaxBuysPerDay)

	// --- Per-trade economics ---

	// Avg trade size: base amount capped by bonding curve impact.
	// Fresh PumpFun token has vSOL ≈ 30, so max entry = 30 × impactPct / 100.
	maxEntryOnFreshToken := 30.0 * in.MaxImpactPct / 100.0
	out.AvgTradeSize = math.Min(in.BaseSnipeSOL, maxEntryOnFreshToken)

	// Gross expected value per trade.
	winContribution := in.WinRate * (in.AvgWinPct / 100.0)
	lossContribution := (1 - in.WinRate) * (in.AvgLossPct / 100.0)
	out.GrossEVPerTrade = out.AvgTradeSize * (winContribution - lossContribution)

	// Costs.
	out.GasCostPerRoundTrip = in.GasCostPerTx * 2
	// Slippage: approximate as ~1-2% of trade size on PumpFun bonding curve.
	out.SlippageCostPerTrade = out.AvgTradeSize * 0.015

	out.NetEVPerTrade = out.GrossEVPerTrade - out.GasCostPerRoundTrip - out.SlippageCostPerTrade

	// --- Daily projections ---

	out.DailyGrossProfit = out.RealisticBuysPerDay * out.GrossEVPerTrade
	out.DailyGasCost = out.RealisticBuysPerDay * out.GasCostPerRoundTrip
	out.DailySlippageCost = out.RealisticBuysPerDay * out.SlippageCostPerTrade
	out.DailyNetProfit = out.RealisticBuysPerDay * out.NetEVPerTrade
	out.MaxConcurrentExposure = float64(in.MaxPositions) * out.AvgTradeSize

	// --- Monthly projections ---

	out.MonthlyNetProfit = out.DailyNetProfit * 30
	out.MonthlySweepEstimate = out.MonthlyNetProfit // sweep = net profit (reserve stays)
	if in.SweepReserveSOL > 0 {
		out.AnnualizedROIPct = (out.DailyNetProfit * 365 / in.SweepReserveSOL) * 100
	}

	// --- Risk ---

	// Half-Kelly fraction.
	if in.AvgLossPct > 0 {
		rewardRatio := in.AvgWinPct / in.AvgLossPct
		kelly := in.WinRate - (1-in.WinRate)/rewardRatio
		out.HalfKellyFraction = kelly * 0.5
	}

	// Expected max drawdown: approximate worst consecutive loss streak.
	// In N trades, expected max streak ≈ log(N) / log(1/(1-winRate)).
	// Each loss costs avgLossPct% of avgTradeSize. Express as % of reserve.
	dailyTrades := out.RealisticBuysPerDay
	monthlyTrades := dailyTrades * 30
	if monthlyTrades > 0 && in.WinRate > 0 && in.WinRate < 1 && in.SweepReserveSOL > 0 {
		expectedStreak := math.Log(monthlyTrades) / math.Log(1/(1-in.WinRate))
		drawdownSOL := expectedStreak * (in.AvgLossPct / 100.0) * out.AvgTradeSize
		out.ExpectedMaxDrawdown = (drawdownSOL / in.SweepReserveSOL) * 100
	}

	// Simplified gambler's ruin probability.
	// P(ruin) ≈ ((1-p)/p)^(bankroll/bet) where p = winRate adjusted for asymmetric payoffs.
	if in.WinRate > 0 && in.WinRate < 1 && in.SweepReserveSOL > 0 && out.AvgTradeSize > 0 {
		// Effective win probability accounting for asymmetric payoffs.
		effectiveP := in.WinRate * in.AvgWinPct / (in.WinRate*in.AvgWinPct + (1-in.WinRate)*in.AvgLossPct)
		if effectiveP > 0.5 {
			ratio := (1 - effectiveP) / effectiveP
			units := in.SweepReserveSOL / out.AvgTradeSize
			out.RuinProbability = math.Pow(ratio, units) * 100 // as percentage
		} else {
			out.RuinProbability = 100 // negative edge = certain ruin
		}
	}

	return out
}

// String formats the model output as a human-readable report.
func (o ModelOutput) String() string {
	var b strings.Builder

	b.WriteString("=== TRENCHBOT PROFIT MODEL ===\n\n")

	b.WriteString("--- Throughput ---\n")
	b.WriteString(fmt.Sprintf("  Max buys/hour:           %.0f\n", o.MaxBuysPerHour))
	b.WriteString(fmt.Sprintf("  Max buys/day:            %.0f\n", o.MaxBuysPerDay))
	b.WriteString(fmt.Sprintf("  Realistic buys/day:      %.0f\n", o.RealisticBuysPerDay))
	b.WriteString(fmt.Sprintf("  Position turnover/hour:  %.1f\n", o.AvgPositionTurnover))

	b.WriteString("\n--- Per-Trade Economics ---\n")
	b.WriteString(fmt.Sprintf("  Avg trade size:          %.4f SOL\n", o.AvgTradeSize))
	b.WriteString(fmt.Sprintf("  Gross EV/trade:          %+.4f SOL\n", o.GrossEVPerTrade))
	b.WriteString(fmt.Sprintf("  Gas cost (round-trip):   -%.4f SOL\n", o.GasCostPerRoundTrip))
	b.WriteString(fmt.Sprintf("  Slippage cost:           -%.4f SOL\n", o.SlippageCostPerTrade))
	b.WriteString(fmt.Sprintf("  Net EV/trade:            %+.4f SOL\n", o.NetEVPerTrade))

	b.WriteString("\n--- Daily Projection ---\n")
	b.WriteString(fmt.Sprintf("  Buys/day:                %.0f\n", o.RealisticBuysPerDay))
	b.WriteString(fmt.Sprintf("  Gross profit:            %.4f SOL\n", o.DailyGrossProfit))
	b.WriteString(fmt.Sprintf("  Gas costs:               %.4f SOL\n", o.DailyGasCost))
	b.WriteString(fmt.Sprintf("  Slippage costs:          %.4f SOL\n", o.DailySlippageCost))
	b.WriteString(fmt.Sprintf("  Net daily profit:        %.4f SOL\n", o.DailyNetProfit))
	b.WriteString(fmt.Sprintf("  Max concurrent exposure: %.4f SOL\n", o.MaxConcurrentExposure))

	b.WriteString("\n--- Monthly Projection ---\n")
	b.WriteString(fmt.Sprintf("  Net monthly profit:      %.2f SOL\n", o.MonthlyNetProfit))
	b.WriteString(fmt.Sprintf("  Monthly sweep estimate:  %.2f SOL\n", o.MonthlySweepEstimate))
	b.WriteString(fmt.Sprintf("  Annualized ROI:          %.1f%% (on reserve)\n", o.AnnualizedROIPct))

	b.WriteString("\n--- Risk ---\n")
	b.WriteString(fmt.Sprintf("  Half-Kelly fraction:     %.3f\n", o.HalfKellyFraction))
	b.WriteString(fmt.Sprintf("  Expected max drawdown:   %.1f%%\n", o.ExpectedMaxDrawdown))
	b.WriteString(fmt.Sprintf("  Ruin probability:        %.4f%%\n", o.RuinProbability))

	return b.String()
}
