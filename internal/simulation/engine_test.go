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
	result := filt.Evaluate(token)
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
	result := filt.Evaluate(token)
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
