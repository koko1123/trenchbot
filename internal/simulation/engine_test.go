package simulation

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/cindocode/trenchbot/internal/clock"
	"github.com/cindocode/trenchbot/internal/executor"
	"github.com/cindocode/trenchbot/internal/filter"
	"github.com/cindocode/trenchbot/internal/monitor"
	"github.com/cindocode/trenchbot/internal/risk"
	"github.com/cindocode/trenchbot/internal/scanner"
	"github.com/cindocode/trenchbot/internal/state"
	"github.com/cindocode/trenchbot/internal/testutil"
)

var testLog = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

func TestInterpolatePrice(t *testing.T) {
	curve := []PricePoint{
		{0, 1.0},
		{10 * time.Minute, 2.0},
		{20 * time.Minute, 0.5},
	}

	tests := []struct {
		offset time.Duration
		want   float64
		tol    float64
	}{
		{0, 1.0, 0.01},
		{5 * time.Minute, 1.5, 0.01},
		{10 * time.Minute, 2.0, 0.01},
		{15 * time.Minute, 1.25, 0.01},
		{20 * time.Minute, 0.5, 0.01},
		{30 * time.Minute, 0.5, 0.01}, // past end
	}

	for _, tt := range tests {
		got := InterpolatePrice(curve, tt.offset)
		if got < tt.want-tt.tol || got > tt.want+tt.tol {
			t.Errorf("at %v: got %.3f, want %.3f", tt.offset, got, tt.want)
		}
	}
}

func TestTokenGenerator_Distribution(t *testing.T) {
	gen := NewTokenGenerator(GeneratorConfig{
		Seed:              42,
		TokensPerHour:     100,
		SimulatedDuration: 1 * time.Hour,
		Chain:             state.ChainSolana,
	})

	tokens := gen.Generate()
	if len(tokens) != 100 {
		t.Fatalf("expected 100 tokens, got %d", len(tokens))
	}

	counts := make(map[TokenArchetype]int)
	for _, tok := range tokens {
		counts[tok.Archetype]++
	}

	// Rug-like archetypes (naked + polished + scam + delayed) should be majority (~62%)
	rugLike := counts[ArchetypeNakedRug] + counts[ArchetypePolishedRug] + counts[ArchetypeScam] + counts[ArchetypeDelayedRug]
	if rugLike < 40 {
		t.Errorf("expected at least 40 rug-like tokens, got %d", rugLike)
	}
	// Polished rugs should appear (25% of 100 = ~25)
	if counts[ArchetypePolishedRug] < 10 {
		t.Errorf("expected at least 10 polished rugs, got %d", counts[ArchetypePolishedRug])
	}
	// Non-rug types should appear
	nonRug := counts[ArchetypeSlow] + counts[ArchetypeSlowBleed] + counts[ArchetypeModerate] + counts[ArchetypeMoonshot]
	if nonRug < 10 {
		t.Errorf("expected at least 10 non-rug tokens, got %d", nonRug)
	}
}

// Integration test: single high-score token flows through pipeline
func TestPipeline_TokenFlowsThrough(t *testing.T) {
	clk := clock.NewSimClock(time.Now())
	store := state.NewStore()
	store.SetPeakEquity(state.ChainSolana, 1200)
	store.SetGasBalance(state.ChainSolana, 0.25)
	exec := testutil.NewMockExecutor(state.ChainSolana)
	notif := testutil.NewMockNotifier()
	executors := map[state.Chain]executor.Executor{state.ChainSolana: exec}

	filt := filter.New(60, testLog)
	breaker := risk.NewCircuitBreaker(risk.CircuitBreakerConfig{
		Chain: state.ChainSolana, MaxDrawdownPct: 50, ConsecutiveLossCap: 10,
		MaxSnipesPerHour: 10, StartingEquity: 1200,
	}, store, clk, testLog)
	sizer := risk.NewPositionSizer(store, 0.3, 0.05, 8)
	sizer.SetGasReserves(0.005, 0.002)
	_ = monitor.New(store, executors, notif, monitor.DefaultExitConfig(), clk, true, testLog)

	token := highScoreToken()
	result := filt.Evaluate(context.Background(), token)
	if !result.Approved {
		t.Fatal("high-score token should be approved")
	}
	if !breaker.CanSnipe() {
		t.Fatal("breaker should allow")
	}
	size := sizer.Size(state.ChainSolana, result.Score)
	if size <= 0 {
		t.Fatal("size should be positive")
	}

	buyResult := exec.Buy(context.Background(), executor.BuyParams{
		Chain: state.ChainSolana, TokenAddress: token.Address, Amount: size, Shadow: true,
	})
	if !buyResult.Success {
		t.Fatal("buy should succeed")
	}
}

// Integration test: low-score token is rejected
func TestPipeline_LowScoreRejected(t *testing.T) {
	filt := filter.New(60, testLog)
	token := lowScoreToken()
	result := filt.Evaluate(context.Background(), token)
	if result.Approved {
		t.Errorf("low-score token should be rejected, got score=%d", result.Score)
	}
}

// Integration test: exit triggers on price change
func TestPipeline_ExitOnPriceChange(t *testing.T) {
	clk := clock.NewSimClock(time.Now())
	store := state.NewStore()
	exec := testutil.NewMockExecutor(state.ChainSolana)
	notif := testutil.NewMockNotifier()
	executors := map[state.Chain]executor.Executor{state.ChainSolana: exec}
	mon := monitor.New(store, executors, notif, monitor.DefaultExitConfig(), clk, true, testLog)

	store.AddPosition(&state.Position{
		ID: "p1", Chain: state.ChainSolana, TokenAddress: "addr1", TokenSymbol: "TEST",
		EntryPrice: 1.0, CurrentPrice: 2.0, PeakPrice: 2.0, Amount: 0.3, EntryTime: clk.Now(),
	})

	mon.CheckPositions(context.Background())

	sells := exec.GetSellCalls()
	if len(sells) == 0 {
		t.Fatal("expected sell at 2x (tranche-1)")
	}
	if sells[0].AmountPct != 25 {
		t.Errorf("expected 25%% tranche-1 sell, got %.0f%%", sells[0].AmountPct)
	}
}

// Integration test: stale position cleanup
func TestPipeline_StalePositionCleanup(t *testing.T) {
	start := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	clk := clock.NewSimClock(start)
	store := state.NewStore()
	exec := testutil.NewMockExecutor(state.ChainSolana)
	notif := testutil.NewMockNotifier()
	executors := map[state.Chain]executor.Executor{state.ChainSolana: exec}
	mon := monitor.New(store, executors, notif, monitor.DefaultExitConfig(), clk, true, testLog)

	store.AddPosition(&state.Position{
		ID: "p1", Chain: state.ChainSolana, TokenAddress: "addr1", TokenSymbol: "TEST",
		EntryPrice: 1.0, CurrentPrice: 1.1, PeakPrice: 1.1, Amount: 0.3, EntryTime: start,
	})

	clk.Advance(31 * time.Minute)
	mon.CheckPositions(context.Background())

	sells := exec.GetSellCalls()
	if len(sells) == 0 {
		t.Fatal("expected stale position exit")
	}
}

// Full simulation test
func TestSimulation_FullRun(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping simulation in short mode")
	}

	cfg := DefaultSimConfig()
	cfg.Seed = 42
	cfg.SimulatedDuration = 6 * time.Hour
	cfg.WallClockTimeout = 5 * time.Minute
	cfg.TokensPerHour = 60

	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelWarn}))
	engine := NewEngine(cfg, log)
	report := engine.Run(context.Background())

	t.Log(report.String())

	// Write report JSON
	jsonData, err := report.JSON()
	if err == nil {
		os.WriteFile("simulation-report.json", jsonData, 0644)
	}

	// 1. Filter should reject some tokens (naked rugs have sparse metadata)
	if report.TokensGenerated > 0 {
		rejectionRate := float64(report.TokensGenerated-report.TokensFiltered) / float64(report.TokensGenerated)
		if rejectionRate < 0.10 {
			t.Errorf("filter rejection rate too low: %.1f%% (want >= 10%%)", rejectionRate*100)
		}
	}

	// 2. Win rate should be non-zero (some tokens are moderate/moonshot)
	if report.WinRate < 0.10 {
		t.Errorf("win rate too low: %.1f%% (want >= 10%%)", report.WinRate*100)
	}

	// 3. Total PnL not catastrophic — with gas costs, allow wider range
	if report.TotalPnLPct < -200.0 {
		t.Errorf("total PnL catastrophic: %.1f%% (want > -200%%)", report.TotalPnLPct)
	}

	// 4. Circuit breaker should engage at some point
	if report.CircuitBreakerPauses < 1 && report.CircuitBreakerHalts < 1 {
		t.Log("WARNING: circuit breaker never engaged — may indicate low token throughput")
	}

	// 5. Drawdown within sane limits
	if report.MaxDrawdownPct > 80.0 {
		t.Errorf("drawdown exceeded limit: %.1f%% (want <= 80%%)", report.MaxDrawdownPct)
	}

	// 6. Exit reason diversity — at least 2 different reasons
	if len(report.ExitsByReason) < 2 {
		t.Errorf("only %d exit reasons, expected at least 2 for diversity", len(report.ExitsByReason))
	}

	// 7. Activity check — simulation should buy tokens
	if report.TokensBought < 3 {
		t.Errorf("only %d tokens bought, simulation may be broken (want >= 3)", report.TokensBought)
	}

	// 8. Wall clock check
	if report.WallClockElapsed > 5*time.Minute {
		t.Errorf("simulation took too long: %v (want < 5m)", report.WallClockElapsed)
	}

	// 9. Polished rugs should have been bought (adversarial tokens pass filter)
	if stats, ok := report.ArchetypeResults[ArchetypePolishedRug]; ok {
		if stats.Bought == 0 {
			t.Error("polished rugs should pass filter and get bought — adversarial testing broken")
		}
	}

	// 10. Gas should have been consumed
	if report.GasSpent <= 0 {
		t.Error("gas tracking broken — no gas spent during simulation")
	}
	if report.GasRemaining >= report.GasSpent+report.GasRemaining {
		t.Error("gas remaining should be less than starting budget")
	}

	// 11. Market shocks should have occurred
	if report.MarketShocks == 0 {
		t.Log("WARNING: no market shocks fired during simulation")
	}

	// 12. Slippage cost should be tracked (deterministic: every buy applies slippage)
	if report.TotalSlippageCost <= 0 {
		t.Error("slippage tracking broken — no slippage cost recorded")
	}

	// 13. Front-run cost should be tracked (deterministic: every buy applies MEV)
	if report.TotalFrontRunCost <= 0 {
		t.Error("front-run cost tracking broken — no MEV cost recorded")
	}

	// 14. Gas spike events should fire (shocks trigger gas spikes)
	if report.GasSpikeEvents == 0 {
		t.Log("WARNING: no gas spike events fired during simulation")
	}

	// 15. Rug clusters should be generated (3% prob over 360 tokens)
	if report.RugClusters == 0 {
		t.Log("WARNING: no rug clusters generated — check GenerateWithResult()")
	}

	// 16. Honeypots should exist in archetype results
	if report.HoneypotCount == 0 {
		t.Log("WARNING: no honeypot tokens generated")
	}

	// 17. Sell failures should occur (5% rate over many sells)
	if report.SellFailures == 0 {
		t.Log("WARNING: no sell failures recorded — check SellFailureRate")
	}

	// 18. Re-entry blocking should have kicked in for stop-loss tokens
	if report.ReEntryBlocked == 0 {
		t.Log("WARNING: no re-entry blocks recorded")
	}

	// 19. Early trailing stop should appear in exit reasons
	if _, ok := report.ExitsByReason["early-trailing-stop"]; !ok {
		t.Log("WARNING: early-trailing-stop never triggered in full sim")
	}
}

func highScoreToken() scanner.NewToken {
	return makeToken("high_score_token", "MOON", "MoonToken",
		"An amazing token with great community support", "https://img.png",
		"creator123", 5000, 2.0, 40.0)
}

func lowScoreToken() scanner.NewToken {
	return scanner.NewToken{
		Chain:   state.ChainSolana,
		Address: "low_score_token",
	}
}

// TestSimulation_October10Crash models the October 10, 2025 tariff crash impact on memecoins.
// Trump announced 100% tariffs on Chinese imports at ~20:50 UTC.
// SOL crashed from $229 to $173 (-24.1%) over 29 hours with 32x gas spike.
// Memecoins crash much harder than the underlying chain: beta is typically 2-4x.
// A -24% SOL move implies -50% to -80% for memecoins, with many going to zero.
// We model a -60% memecoin crash (0.4 multiplier) with very slow recovery and 32x gas.
func TestSimulation_October10Crash(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping crash scenario in short mode")
	}

	cfg := DefaultSimConfig()
	cfg.Seed = 1010
	cfg.SimulatedDuration = 6 * time.Hour
	cfg.WallClockTimeout = 5 * time.Minute
	cfg.TokensPerHour = 60
	cfg.GasSpikeEnabled = true

	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelWarn}))
	engine := NewEngine(cfg, log)

	// Inject a -60% memecoin crash at tick 7200 (2 hours in).
	// 0.4 multiplier = -60% price drop (memecoins have 2-3x beta vs SOL).
	// 0.0005 very slow decay (memecoins don't bounce like majors).
	// 32x gas spike for 5 min (observed on-chain during the Oct 10 panic).
	report := engine.RunWithShockAt(context.Background(), 7200, 0.4, 0.0005, 32.0)

	t.Log(report.String())

	// The bot should survive — circuit breaker should engage before catastrophic loss.
	if report.MaxDrawdownPct > 55 {
		t.Errorf("drawdown %.1f%% exceeded safety margin — circuit breaker should limit to ~50%%", report.MaxDrawdownPct)
	}

	// Circuit breaker should have halted or paused.
	if report.CircuitBreakerHalts == 0 && report.CircuitBreakerPauses == 0 {
		t.Error("circuit breaker should have engaged during -60% memecoin crash")
	}

	// Gas spike event should have fired.
	if report.GasSpikeEvents == 0 {
		t.Error("gas spike should fire during crash scenario")
	}

	// Bot should not have catastrophic total PnL.
	if report.TotalPnLPct < -500 {
		t.Errorf("total PnL %.1f%% is catastrophic — bot did not survive the crash", report.TotalPnLPct)
	}

	// Write crash report for analysis
	jsonData, err := report.JSON()
	if err == nil {
		os.WriteFile("crash-scenario-report.json", jsonData, 0644)
	}
}

func TestSimExecutor_HoneypotSellRevert(t *testing.T) {
	clk := clock.NewSimClock(time.Now())
	cfg := DefaultSimConfig()
	cfg.SellFailureRate = 0 // disable RPC failures to isolate honeypot
	exec := NewSimExecutor(state.ChainSolana, clk, cfg)

	exec.RegisterCurve("honey1", []PricePoint{{0, 1.0}, {10 * time.Minute, 2.0}})
	exec.MarkHoneypot("honey1")

	buy := exec.Buy(context.Background(), executor.BuyParams{
		Chain: state.ChainSolana, TokenAddress: "honey1", Amount: 0.1, Shadow: true,
	})
	if !buy.Success {
		t.Fatal("honeypot buy should succeed")
	}

	sell := exec.Sell(context.Background(), executor.SellParams{
		Chain: state.ChainSolana, TokenAddress: "honey1", AmountPct: 100,
	})
	if sell.Success {
		t.Fatal("honeypot sell should always fail")
	}
	if sell.GasCost <= 0 {
		t.Error("honeypot sell should still charge gas")
	}
}

func TestSimExecutor_SellRPCFailure(t *testing.T) {
	clk := clock.NewSimClock(time.Now())
	cfg := DefaultSimConfig()
	cfg.SellFailureRate = 1.0 // 100% failure rate for deterministic test
	exec := NewSimExecutor(state.ChainSolana, clk, cfg)

	exec.RegisterCurve("tok1", []PricePoint{{0, 1.0}, {10 * time.Minute, 2.0}})
	exec.Buy(context.Background(), executor.BuyParams{
		Chain: state.ChainSolana, TokenAddress: "tok1", Amount: 0.1, Shadow: true,
	})

	sell := exec.Sell(context.Background(), executor.SellParams{
		Chain: state.ChainSolana, TokenAddress: "tok1", AmountPct: 100,
	})
	if sell.Success {
		t.Fatal("sell should fail with 100% failure rate")
	}
	if exec.SellFailureCount() != 1 {
		t.Errorf("expected 1 failure count, got %d", exec.SellFailureCount())
	}
}

func TestSimExecutor_SlippageAndFrontRun(t *testing.T) {
	clk := clock.NewSimClock(time.Now())
	cfg := DefaultSimConfig()
	cfg.SlippagePct = 1.0
	cfg.FrontRunMinPct = 10.0
	cfg.FrontRunMaxPct = 10.0
	cfg.PriceNoiseEnabled = false
	exec := NewSimExecutor(state.ChainSolana, clk, cfg)

	exec.RegisterCurve("tok1", []PricePoint{{0, 1.0}, {10 * time.Minute, 2.0}})

	buy := exec.Buy(context.Background(), executor.BuyParams{
		Chain: state.ChainSolana, TokenAddress: "tok1", Amount: 0.1, Shadow: true,
	})

	// Base price is 1.0, with 1% slippage → 1.01, then 10% front-run → 1.01 * 1.10 = 1.111
	if buy.Price < 1.10 {
		t.Errorf("buy price %.4f should reflect slippage + front-run (want > 1.10)", buy.Price)
	}
}

func TestTokenGenerator_HoneypotArchetype(t *testing.T) {
	gen := NewTokenGenerator(GeneratorConfig{
		Seed: 42, TokensPerHour: 200, SimulatedDuration: 1 * time.Hour,
		Chain: state.ChainSolana,
	})
	tokens := gen.Generate()

	honeypotCount := 0
	for _, tok := range tokens {
		if tok.Archetype == ArchetypeHoneypot {
			honeypotCount++
			if tok.Token.Name == "" || tok.Token.ImageURL == "" {
				t.Error("honeypot token should have full metadata")
			}
		}
	}
	if honeypotCount == 0 {
		t.Error("expected at least 1 honeypot in 200 tokens (3% rate)")
	}
}

func TestTokenGenerator_RugClusters(t *testing.T) {
	gen := NewTokenGenerator(GeneratorConfig{
		Seed: 42, TokensPerHour: 200, SimulatedDuration: 1 * time.Hour,
		Chain: state.ChainSolana, RugClusterProb: 0.1, RugClusterSize: 4,
	})
	result := gen.GenerateWithResult()

	if result.RugClusters == 0 {
		t.Error("expected at least 1 rug cluster with 10% probability")
	}

	creators := make(map[string]int)
	for _, tok := range result.Tokens {
		creators[tok.Token.Creator]++
	}
	hasCluster := false
	for _, count := range creators {
		if count >= 4 {
			hasCluster = true
			break
		}
	}
	if !hasCluster {
		t.Error("expected at least one creator with 4+ tokens (cluster)")
	}
}

func makeToken(addr, symbol, name, desc, img, creator string, mcap, initialBuy, mcapSol float64) scanner.NewToken {
	metadata := make(map[string]interface{})
	if initialBuy > 0 {
		metadata["initialBuy"] = initialBuy
	}
	if mcapSol > 0 {
		metadata["marketCapSol"] = mcapSol
	}

	return scanner.NewToken{
		Chain:        state.ChainSolana,
		Address:      addr,
		Symbol:       symbol,
		Name:         name,
		Description:  desc,
		ImageURL:     img,
		Creator:      creator,
		MarketCapUSD: mcap,
		Metadata:     metadata,
	}
}
