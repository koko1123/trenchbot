package filter

import (
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
		}
		db.creators[creator] = rep
	}

	// Update running average for peak mult.
	totalPeak := rep.AvgPeakMult * float64(rep.TotalLaunches)
	totalLifespan := rep.AvgLifespan * time.Duration(rep.TotalLaunches)

	rep.TotalLaunches++
	if peakMult > 2.0 {
		rep.WinCount++
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

// ScoreModifier returns a filter score adjustment based on creator trust:
// Trusted: +10, Neutral: 0, Suspicious: -15, Blacklisted: -100 (effectively blocks).
func (db *ReputationDB) ScoreModifier(creator string) int {
	tier := db.GetTier(creator)
	switch tier {
	case TrustTierTrusted:
		return 10
	case TrustTierSuspicious:
		return -15
	case TrustTierBlacklisted:
		return -100
	default:
		return 0
	}
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
