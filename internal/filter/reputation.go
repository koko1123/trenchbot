package filter

import (
	"math"
	"sync"
	"time"
)

// TrustTier represents the reputation level of a token creator.
type TrustTier string

const (
	TrustTierTrusted     TrustTier = "trusted"
	TrustTierNeutral     TrustTier = "neutral"
	TrustTierSuspicious  TrustTier = "suspicious"
	TrustTierBlacklisted TrustTier = "blacklisted"
)

// CreatorReputation tracks the historical performance of a token creator.
type CreatorReputation struct {
	Address       string
	TotalLaunches int
	WinCount      int // tokens that peaked >2x
	RugCount      int // tokens that went to <0.3x
	AvgPeakMult   float64
	AvgLifespan   time.Duration
	TrustTier     TrustTier

	// Bayesian Beta-Binomial posterior for win rate estimation.
	Alpha float64 // successes + 1 (prior)
	Beta  float64 // failures + 1 (prior)
}

// ExpectedWinRate returns the posterior mean win rate: Alpha / (Alpha + Beta).
func (r *CreatorReputation) ExpectedWinRate() float64 {
	if r.Alpha+r.Beta <= 0 {
		return 0.5 // uniform prior
	}
	return r.Alpha / (r.Alpha + r.Beta)
}

// Confidence returns the number of observations (Alpha + Beta - 2, since prior is Beta(1,1)).
func (r *CreatorReputation) Confidence() float64 {
	return r.Alpha + r.Beta - 2
}

// LowerCredible returns the lower bound of a p-credible interval for the win rate.
// Uses a normal approximation to the Beta distribution quantile for efficiency.
func (r *CreatorReputation) LowerCredible(p float64) float64 {
	a, b := r.Alpha, r.Beta
	if a <= 0 || b <= 0 {
		return 0
	}
	mean := a / (a + b)
	variance := (a * b) / ((a + b) * (a + b) * (a + b + 1))
	sd := math.Sqrt(variance)

	// Normal approximation: lower bound = mean - z * sd
	// z for common credible intervals: p=0.80 → z≈0.842, p=0.90 → z≈1.282, p=0.95 → z≈1.645
	z := normalQuantile((1 - p) / 2)
	lower := mean + z*sd // z is negative for lower tail
	if lower < 0 {
		return 0
	}
	if lower > 1 {
		return 1
	}
	return lower
}

// normalQuantile approximates the quantile function of the standard normal distribution.
// Uses the rational approximation from Abramowitz and Stegun (formula 26.2.23).
func normalQuantile(p float64) float64 {
	if p <= 0 {
		return -4.0
	}
	if p >= 1 {
		return 4.0
	}
	if p > 0.5 {
		return -normalQuantile(1 - p)
	}
	t := math.Sqrt(-2.0 * math.Log(p))
	// Coefficients for rational approximation.
	c0, c1, c2 := 2.515517, 0.802853, 0.010328
	d1, d2, d3 := 1.432788, 0.189269, 0.001308
	return -(t - (c0+c1*t+c2*t*t)/(1+d1*t+d2*t*t+d3*t*t*t))
}

// ReputationDB stores and manages creator reputations.
type ReputationDB struct {
	mu       sync.RWMutex
	creators map[string]*CreatorReputation
}

// NewReputationDB creates a new empty ReputationDB.
func NewReputationDB() *ReputationDB {
	return &ReputationDB{
		creators: make(map[string]*CreatorReputation),
	}
}

// RecordOutcome updates a creator's reputation after a trade closes.
// peakMult is the peak price multiplier (e.g. 2.5 means it peaked at 2.5x).
// isRug indicates the token went to <0.3x.
func (db *ReputationDB) RecordOutcome(creator string, peakMult float64, lifespan time.Duration, isRug bool) {
	db.mu.Lock()
	defer db.mu.Unlock()

	rep, ok := db.creators[creator]
	if !ok {
		rep = &CreatorReputation{
			Address:   creator,
			TrustTier: TrustTierNeutral,
			Alpha:     1, // Beta(1,1) uniform prior
			Beta:      1,
		}
		db.creators[creator] = rep
	}

	// Update running average for peak mult.
	totalPeak := rep.AvgPeakMult * float64(rep.TotalLaunches)
	totalLifespan := rep.AvgLifespan * time.Duration(rep.TotalLaunches)

	rep.TotalLaunches++
	isWin := peakMult > 2.0
	if isWin {
		rep.WinCount++
		rep.Alpha++ // Bayesian update: success
	} else {
		rep.Beta++ // Bayesian update: failure
	}
	if isRug {
		rep.RugCount++
	}

	rep.AvgPeakMult = (totalPeak + peakMult) / float64(rep.TotalLaunches)
	rep.AvgLifespan = (totalLifespan + lifespan) / time.Duration(rep.TotalLaunches)

	// Recompute trust tier.
	rep.TrustTier = computeTier(rep)
}

// GetReputation returns the current reputation for a creator.
// Returns nil if creator is unknown.
func (db *ReputationDB) GetReputation(creator string) *CreatorReputation {
	db.mu.RLock()
	defer db.mu.RUnlock()
	rep, ok := db.creators[creator]
	if !ok {
		return nil
	}
	// Return a copy to avoid races.
	copy := *rep
	return &copy
}

// GetTier returns the trust tier for a creator (Neutral if unknown).
func (db *ReputationDB) GetTier(creator string) TrustTier {
	db.mu.RLock()
	defer db.mu.RUnlock()
	rep, ok := db.creators[creator]
	if !ok {
		return TrustTierNeutral
	}
	return rep.TrustTier
}

// ScoreModifier returns a filter score adjustment using Bayesian credible intervals.
// This is strictly better than discrete tiers because it accounts for uncertainty:
// - 2 wins, 0 losses → lower bound ~0.33 → neutral (not enough data)
// - 20 wins, 5 losses → lower bound ~0.62 → trusted (confident)
// - 0 wins, 6 losses → lower bound ~0.0 → blacklisted
func (db *ReputationDB) ScoreModifier(creator string) int {
	rep := db.GetReputation(creator)
	if rep == nil {
		return 0
	}

	// Use lower bound of 80% credible interval (conservative).
	lowerBound := rep.LowerCredible(0.80)
	confidence := rep.Confidence()

	// Blacklist: zero wins with many attempts — serial rugger.
	if rep.WinCount == 0 && rep.RugCount >= 5 {
		return -100
	}

	// Confidently good: lower bound > 0.55 means we're 80% sure win rate > 55%.
	if lowerBound > 0.55 {
		return 10
	}

	// Confidently bad: lower bound < 0.2 with enough data.
	if lowerBound < 0.2 && confidence >= 3 {
		return -15
	}

	return 0
}

// ObservationMultiplier returns how to adjust the observation window:
// Trusted: 0.4 (2s instead of 5s), Neutral: 1.0, Suspicious: 2.0 (10s).
func (db *ReputationDB) ObservationMultiplier(creator string) float64 {
	tier := db.GetTier(creator)
	switch tier {
	case TrustTierTrusted:
		return 0.4
	case TrustTierSuspicious:
		return 2.0
	case TrustTierBlacklisted:
		return 2.0
	default:
		return 1.0
	}
}

// computeTier determines the trust tier based on the creator's track record.
func computeTier(rep *CreatorReputation) TrustTier {
	if rep.TotalLaunches == 0 {
		return TrustTierNeutral
	}

	rugRate := float64(rep.RugCount) / float64(rep.TotalLaunches)

	// Blacklisted: 5+ launches and 100% rug rate.
	if rep.TotalLaunches >= 5 && rugRate == 1.0 {
		return TrustTierBlacklisted
	}

	// Suspicious: 3+ launches and (>70% rug rate OR 3+ rugs).
	if rep.TotalLaunches >= 3 && (rugRate > 0.70 || rep.RugCount >= 3) {
		return TrustTierSuspicious
	}

	// Trusted: 3+ launches, <30% rug rate, avg peak >2x.
	if rep.TotalLaunches >= 3 && rugRate < 0.30 && rep.AvgPeakMult > 2.0 {
		return TrustTierTrusted
	}

	return TrustTierNeutral
}
