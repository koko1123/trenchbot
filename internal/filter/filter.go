package filter

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/cindocode/trenchbot/internal/notify"
	"github.com/cindocode/trenchbot/internal/scanner"
	"github.com/cindocode/trenchbot/internal/state"
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
	minScore         int
	log              *slog.Logger
	creatorLookup    CreatorLookup
	honeypotChecker  *HoneypotChecker
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

// SetHoneypotChecker enables pre-buy honeypot detection via GoPlus API.
func (f *Filter) SetHoneypotChecker(hc *HoneypotChecker) {
	f.honeypotChecker = hc
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

	f.log.Debug("token scored",
		"chain", token.Chain,
		"symbol", token.Symbol,
		"address", token.Address,
		"score", score,
		"approved", approved,
		"reasons", strings.Join(reasons, "; "),
	)

	return result
}

// CheckHoneypotAsync runs the GoPlus honeypot check asynchronously. If the
// token is flagged as a honeypot, the position is force-closed and the notifier
// is alerted. Designed to be called in a goroutine after the buy has executed.
func (f *Filter) CheckHoneypotAsync(ctx context.Context, chain state.Chain, tokenAddress string, positionID string, store *state.Store, notifier notify.Notifier, log *slog.Logger) {
	if f.honeypotChecker == nil {
		return
	}

	safe, reasons, err := f.honeypotChecker.Check(ctx, chain, tokenAddress)
	if err != nil {
		log.Debug("honeypot check error (failing open)", "token", tokenAddress, "err", err)
		return
	}
	if !safe {
		log.Warn("honeypot detected, force-closing position", "token", tokenAddress, "reasons", reasons)
		store.UpdatePosition(positionID, func(p *state.Position) {
			p.Closed = true
			p.PnL = -100.0
		})
		notifier.Exit(ctx, string(chain), "", tokenAddress, -100.0, "honeypot-detected")
	}
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
		score += 5
		reasons = append(reasons, "has description (+5)")
		if len(token.Description) > 50 {
			score += 5
			reasons = append(reasons, "detailed description (+5)")
		}
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
			score += 5
			reasons = append(reasons, "has initial buy (+5)")
			if buy > 0.5 {
				score += 5
				reasons = append(reasons, "initial buy > 0.5 SOL (+5)")
			}
			if buy > 1.0 {
				score += 5
				reasons = append(reasons, "initial buy > 1.0 SOL (+5)")
			}
		}
	}

	return score, reasons
}

func (f *Filter) scoreChainSpecific(token scanner.NewToken) (int, []string) {
	score := 0
	var reasons []string

	if mcapSol, ok := token.Metadata["marketCapSol"]; ok {
		if mc, ok := mcapSol.(float64); ok && mc > 0 {
			score += 5
			reasons = append(reasons, "bonding curve active (+5)")
			if mc > 10 {
				score += 5
				reasons = append(reasons, "mcap > 10 SOL (+5)")
			}
			if mc > 30 {
				score += 5
				reasons = append(reasons, "mcap > 30 SOL (+5)")
			}
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
