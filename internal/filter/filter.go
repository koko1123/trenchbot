package filter

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/cindocode/trenchbot/internal/scanner"
)

// CreatorLookup provides creator rug history from the database.
type CreatorLookup interface {
	CreatorHistory(ctx context.Context, creator string) (total int, rugCount int, err error)
}

type Result struct {
	Token    scanner.NewToken
	Score    int
	Reasons  []string
	Approved bool
}

type Filter struct {
	minScore      int
	log           *slog.Logger
	creatorLookup CreatorLookup
}

func New(minScore int, log *slog.Logger) *Filter {
	return &Filter{
		minScore: minScore,
		log:      log,
	}
}

// SetCreatorLookup sets the optional creator history lookup.
func (f *Filter) SetCreatorLookup(cl CreatorLookup) {
	f.creatorLookup = cl
}

func (f *Filter) Evaluate(ctx context.Context, token scanner.NewToken) Result {
	score := 0
	var reasons []string

	// Metadata quality (0-25 points)
	metaScore, metaReasons := f.scoreMetadata(token)
	score += metaScore
	reasons = append(reasons, metaReasons...)

	// Creator analysis (0-25 points)
	creatorScore, creatorReasons := f.scoreCreator(ctx, token)
	score += creatorScore
	reasons = append(reasons, creatorReasons...)

	// Initial momentum (0-25 points)
	momentumScore, momentumReasons := f.scoreMomentum(token)
	score += momentumScore
	reasons = append(reasons, momentumReasons...)

	// Chain-specific bonuses (0-25 points)
	chainScore, chainReasons := f.scoreChainSpecific(token)
	score += chainScore
	reasons = append(reasons, chainReasons...)

	approved := score >= f.minScore

	result := Result{
		Token:    token,
		Score:    score,
		Reasons:  reasons,
		Approved: approved,
	}

	f.log.Info("token scored",
		"chain", token.Chain,
		"symbol", token.Symbol,
		"address", token.Address,
		"score", score,
		"approved", approved,
		"reasons", strings.Join(reasons, "; "),
	)

	return result
}

func (f *Filter) scoreMetadata(token scanner.NewToken) (int, []string) {
	score := 0
	var reasons []string

	if token.Name != "" {
		score += 5
		reasons = append(reasons, "has name (+5)")
	}
	if token.Symbol != "" {
		score += 5
		reasons = append(reasons, "has symbol (+5)")
	}
	if token.Description != "" && len(token.Description) > 10 {
		score += 10
		reasons = append(reasons, "has description (+10)")
	}
	if token.ImageURL != "" {
		score += 5
		reasons = append(reasons, "has image (+5)")
	}

	return score, reasons
}

func (f *Filter) scoreCreator(ctx context.Context, token scanner.NewToken) (int, []string) {
	score := 0
	var reasons []string

	if token.Creator != "" {
		score += 10
		reasons = append(reasons, "creator identified (+10)")
	}

	if token.Creator != "" && len(token.Creator) >= 32 {
		score += 5
		reasons = append(reasons, "creator wallet looks valid (+5)")
	}

	// Creator history lookup from Postgres.
	if f.creatorLookup != nil && token.Creator != "" {
		total, rugs, err := f.creatorLookup.CreatorHistory(ctx, token.Creator)
		if err == nil && total > 0 {
			rugRate := float64(rugs) / float64(total)
			if rugRate > 0.7 {
				score -= 20
				reasons = append(reasons, fmt.Sprintf("serial rugger: %d/%d rugs (-20)", rugs, total))
			} else if total >= 3 && rugRate < 0.3 {
				score += 10
				reasons = append(reasons, fmt.Sprintf("clean creator: %d tokens, %d rugs (+10)", total, rugs))
			}
		}
	}

	return score, reasons
}

func (f *Filter) scoreMomentum(token scanner.NewToken) (int, []string) {
	score := 0
	var reasons []string

	if token.MarketCapUSD > 0 {
		score += 5
		reasons = append(reasons, "has market cap data (+5)")
	}
	if token.MarketCapUSD > 1000 {
		score += 10
		reasons = append(reasons, "mcap > $1K (+10)")
	}

	if initialBuy, ok := token.Metadata["initialBuy"]; ok {
		if buy, ok := initialBuy.(float64); ok && buy > 0 {
			score += 10
			reasons = append(reasons, "has initial buy (+10)")
			_ = buy
		}
	}

	return score, reasons
}

func (f *Filter) scoreChainSpecific(token scanner.NewToken) (int, []string) {
	score := 0
	var reasons []string

	if mcapSol, ok := token.Metadata["marketCapSol"]; ok {
		if mc, ok := mcapSol.(float64); ok && mc > 0 {
			score += 10
			reasons = append(reasons, "bonding curve active (+10)")
			_ = mc
		}
	}

	// BNB tokens start with lower base score since we have less metadata
	// from Bitquery than from PumpPortal
	if token.Metadata["txHash"] != nil {
		score += 10
		reasons = append(reasons, "verified on-chain event (+10)")
	}

	return score, reasons
}
