package filter

import (
	"testing"
	"time"
)

func TestReputationDB_NeutralByDefault(t *testing.T) {
	db := NewReputationDB()
	if tier := db.GetTier("unknown"); tier != TrustTierNeutral {
		t.Fatalf("expected neutral for unknown creator, got %s", tier)
	}
	if rep := db.GetReputation("unknown"); rep != nil {
		t.Fatal("expected nil reputation for unknown creator")
	}
}

func TestReputationDB_NeutralToTrusted(t *testing.T) {
	db := NewReputationDB()
	creator := "TrustedCreator111111111111111111111111111"

	// 3 good launches: peakMult > 2.0, not rugs.
	db.RecordOutcome(creator, 3.0, 10*time.Minute, false)
	db.RecordOutcome(creator, 2.5, 15*time.Minute, false)
	db.RecordOutcome(creator, 4.0, 20*time.Minute, false)

	rep := db.GetReputation(creator)
	if rep == nil {
		t.Fatal("expected non-nil reputation")
	}
	if rep.TrustTier != TrustTierTrusted {
		t.Fatalf("expected trusted tier, got %s", rep.TrustTier)
	}
	if rep.TotalLaunches != 3 {
		t.Fatalf("expected 3 launches, got %d", rep.TotalLaunches)
	}
	if rep.WinCount != 3 {
		t.Fatalf("expected 3 wins, got %d", rep.WinCount)
	}
	if rep.RugCount != 0 {
		t.Fatalf("expected 0 rugs, got %d", rep.RugCount)
	}
}

func TestReputationDB_NeutralToSuspicious(t *testing.T) {
	db := NewReputationDB()
	creator := "SuspiciousCreator11111111111111111111111"

	// 3 launches, all rugs.
	db.RecordOutcome(creator, 0.2, 1*time.Minute, true)
	db.RecordOutcome(creator, 0.1, 30*time.Second, true)
	db.RecordOutcome(creator, 0.15, 45*time.Second, true)

	tier := db.GetTier(creator)
	if tier != TrustTierSuspicious {
		t.Fatalf("expected suspicious tier, got %s", tier)
	}
}

func TestReputationDB_SuspiciousToBlacklisted(t *testing.T) {
	db := NewReputationDB()
	creator := "BlacklistedCreator1111111111111111111111"

	// 5 launches, all rugs -> 100% rug rate -> blacklisted.
	for i := 0; i < 5; i++ {
		db.RecordOutcome(creator, 0.1, 30*time.Second, true)
	}

	tier := db.GetTier(creator)
	if tier != TrustTierBlacklisted {
		t.Fatalf("expected blacklisted tier, got %s", tier)
	}
}

func TestReputationDB_ScoreModifiers(t *testing.T) {
	db := NewReputationDB()

	// Unknown -> neutral -> 0.
	if mod := db.ScoreModifier("unknown"); mod != 0 {
		t.Fatalf("expected 0 for unknown, got %d", mod)
	}

	// Build a trusted creator.
	trusted := "TrustedForScore1111111111111111111111111"
	for i := 0; i < 3; i++ {
		db.RecordOutcome(trusted, 3.0, 10*time.Minute, false)
	}
	if mod := db.ScoreModifier(trusted); mod != 10 {
		t.Fatalf("expected +10 for trusted, got %d", mod)
	}

	// Build a suspicious creator.
	suspicious := "SuspForScore111111111111111111111111111111"
	for i := 0; i < 3; i++ {
		db.RecordOutcome(suspicious, 0.1, 30*time.Second, true)
	}
	if mod := db.ScoreModifier(suspicious); mod != -15 {
		t.Fatalf("expected -15 for suspicious, got %d", mod)
	}

	// Build a blacklisted creator.
	blacklisted := "BlackForScore1111111111111111111111111111"
	for i := 0; i < 5; i++ {
		db.RecordOutcome(blacklisted, 0.1, 30*time.Second, true)
	}
	if mod := db.ScoreModifier(blacklisted); mod != -100 {
		t.Fatalf("expected -100 for blacklisted, got %d", mod)
	}
}

func TestReputationDB_ObservationMultipliers(t *testing.T) {
	db := NewReputationDB()

	// Unknown -> neutral -> 1.0.
	if mult := db.ObservationMultiplier("unknown"); mult != 1.0 {
		t.Fatalf("expected 1.0 for unknown, got %f", mult)
	}

	// Trusted creator.
	trusted := "TrustedForMult11111111111111111111111111"
	for i := 0; i < 3; i++ {
		db.RecordOutcome(trusted, 3.0, 10*time.Minute, false)
	}
	if mult := db.ObservationMultiplier(trusted); mult != 0.4 {
		t.Fatalf("expected 0.4 for trusted, got %f", mult)
	}

	// Suspicious creator.
	suspicious := "SuspForMult11111111111111111111111111111111"
	for i := 0; i < 3; i++ {
		db.RecordOutcome(suspicious, 0.1, 30*time.Second, true)
	}
	if mult := db.ObservationMultiplier(suspicious); mult != 2.0 {
		t.Fatalf("expected 2.0 for suspicious, got %f", mult)
	}
}

func TestCreatorReputation_BayesianPriors(t *testing.T) {
	db := NewReputationDB()

	// Creator with 2 wins, 0 losses: Alpha=3, Beta=1.
	// Should be neutral — not enough data to be confident.
	twoWins := "TwoWins111111111111111111111111111111111111"
	db.RecordOutcome(twoWins, 3.0, 10*time.Minute, false)
	db.RecordOutcome(twoWins, 2.5, 15*time.Minute, false)
	rep := db.GetReputation(twoWins)
	if rep.Alpha != 3 || rep.Beta != 1 {
		t.Fatalf("expected Alpha=3, Beta=1, got Alpha=%g, Beta=%g", rep.Alpha, rep.Beta)
	}
	if mod := db.ScoreModifier(twoWins); mod != 0 {
		t.Fatalf("expected 0 for 2 wins (insufficient data), got %d", mod)
	}

	// Creator with 20 wins, 5 losses: Alpha=21, Beta=6.
	// Should be trusted — confident win rate estimate.
	manyWins := "ManyWins11111111111111111111111111111111111"
	for i := 0; i < 20; i++ {
		db.RecordOutcome(manyWins, 3.0, 10*time.Minute, false)
	}
	for i := 0; i < 5; i++ {
		db.RecordOutcome(manyWins, 0.5, 2*time.Minute, false) // losses but not rugs
	}
	rep = db.GetReputation(manyWins)
	if rep.Alpha != 21 || rep.Beta != 6 {
		t.Fatalf("expected Alpha=21, Beta=6, got Alpha=%g, Beta=%g", rep.Alpha, rep.Beta)
	}
	lower := rep.LowerCredible(0.80)
	if lower < 0.55 {
		t.Fatalf("expected lower credible bound > 0.55 for 20/25 wins, got %g", lower)
	}
	if mod := db.ScoreModifier(manyWins); mod != 10 {
		t.Fatalf("expected +10 for many wins, got %d", mod)
	}
}

func TestCreatorReputation_ExpectedWinRate(t *testing.T) {
	rep := &CreatorReputation{Alpha: 4, Beta: 2}
	expected := 4.0 / 6.0
	got := rep.ExpectedWinRate()
	if got < expected-0.01 || got > expected+0.01 {
		t.Fatalf("expected win rate ~%g, got %g", expected, got)
	}
}

func TestCreatorReputation_LowerCredibleMonotonic(t *testing.T) {
	// Lower bound should increase as we add more wins.
	var bounds []float64
	rep := &CreatorReputation{Alpha: 1, Beta: 1}
	for i := 0; i < 20; i++ {
		rep.Alpha++
		bounds = append(bounds, rep.LowerCredible(0.80))
	}
	for i := 1; i < len(bounds); i++ {
		if bounds[i] < bounds[i-1]-0.001 { // small tolerance for floating point
			t.Fatalf("lower bound decreased at step %d: %g -> %g", i, bounds[i-1], bounds[i])
		}
	}
}

func TestReputationDB_MixedOutcomes(t *testing.T) {
	db := NewReputationDB()
	creator := "MixedCreator1111111111111111111111111111111"

	// 2 wins, 1 rug = rugRate 0.33, only 3 launches but rug rate not < 0.30.
	db.RecordOutcome(creator, 3.0, 10*time.Minute, false)
	db.RecordOutcome(creator, 2.5, 15*time.Minute, false)
	db.RecordOutcome(creator, 0.2, 1*time.Minute, true)

	tier := db.GetTier(creator)
	if tier != TrustTierNeutral {
		t.Fatalf("expected neutral for mixed outcomes, got %s", tier)
	}
}
