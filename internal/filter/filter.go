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

// HolderChecker provides token holder distribution data.
type HolderChecker interface {
	GetTokenHolders(ctx context.Context, mint string) (HolderDistribution, error)
}

// OnChainCreatorLookup provides creator token launch history from Helius.
type OnChainCreatorLookup interface {
	GetCreatorTokenHistory(ctx context.Context, creator string) (totalTokens int, err error)
}

// HolderDistribution holds the analysis of a token's holder distribution.
type HolderDistribution struct {
	TotalHolders  int
	TopHolderPct  float64
	Top5HolderPct float64
}

type Result struct {
	Token           scanner.NewToken
	Score           int
	Reasons         []string
	Approved        bool
	SignalBreakdown map[string]int // per-dimension score: "metadata"->20, "creator"->15, etc.
}

type Filter struct {
	minScore         int
	maxMinScore      int            // upper bound for dynamic threshold (0 = disabled)
	heatFn           func() float64 // returns current heat level 0.0–1.0
	log              *slog.Logger
	creatorLookup    CreatorLookup
	honeypotChecker  *HoneypotChecker
	holderChecker        HolderChecker
	maxTopHolderPct      float64 // max top holder percentage before penalty (default 50)
	onChainCreatorLookup OnChainCreatorLookup
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

// SetDynamicThreshold enables heat-based threshold tightening. When heat > 0,
// the effective min score rises from base toward maxScore.
func (f *Filter) SetDynamicThreshold(maxMinScore int, heatFn func() float64) {
	f.maxMinScore = maxMinScore
	f.heatFn = heatFn
}

// SetHoneypotChecker enables pre-buy honeypot detection via GoPlus API.
func (f *Filter) SetHoneypotChecker(hc *HoneypotChecker) {
	f.honeypotChecker = hc
}

// SetOnChainCreatorLookup enables on-chain creator history checking via Helius.
func (f *Filter) SetOnChainCreatorLookup(cl OnChainCreatorLookup) {
	f.onChainCreatorLookup = cl
}

// SetHolderChecker enables on-chain holder distribution checking.
func (f *Filter) SetHolderChecker(hc HolderChecker, maxTopHolderPct float64) {
	f.holderChecker = hc
	f.maxTopHolderPct = maxTopHolderPct
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

	// On-chain holder distribution (-15 to +15 points)
	holderScore := 0
	if f.holderChecker != nil {
		var holderReasons []string
		holderScore, holderReasons = f.scoreHolders(ctx, token)
		score += holderScore
		reasons = append(reasons, holderReasons...)
	}

	effectiveMin := f.minScore
	if f.heatFn != nil && f.maxMinScore > f.minScore {
		heat := f.heatFn()
		effectiveMin = f.minScore + int(heat*float64(f.maxMinScore-f.minScore))
	}
	approved := score >= effectiveMin

	breakdown := map[string]int{
		"metadata": metaScore,
		"creator":  creatorScore,
		"momentum": momentumScore,
		"chain":    chainScore,
	}
	if f.holderChecker != nil {
		breakdown["holders"] = holderScore
	}

	result := Result{
		Token:           token,
		Score:           score,
		Reasons:         reasons,
		Approved:        approved,
		SignalBreakdown: breakdown,
	}

	f.log.Debug("token scored",
		"chain", token.Chain,
		"symbol", token.Symbol,
		"address", token.Address,
		"score", score,
		"min_score", effectiveMin,
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

	// On-chain creator history via Helius (broader than our Postgres data).
	if f.onChainCreatorLookup != nil && token.Creator != "" {
		totalTokens, err := f.onChainCreatorLookup.GetCreatorTokenHistory(ctx, token.Creator)
		if err == nil {
			switch {
			case totalTokens >= 10:
				score -= 10
				reasons = append(reasons, fmt.Sprintf("serial launcher: %d prior tokens (-10)", totalTokens))
			case totalTokens >= 1 && totalTokens <= 3:
				score += 5
				reasons = append(reasons, fmt.Sprintf("experienced creator: %d prior tokens (+5)", totalTokens))
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
			if buy > 2.0 {
				score += 5
				reasons = append(reasons, "initial buy > 2.0 SOL (+5)")
			}
			if buy > 5.0 {
				score += 5
				reasons = append(reasons, "initial buy > 5.0 SOL (+5)")
			}
		}
	}

	return score, reasons
}

func (f *Filter) scoreHolders(ctx context.Context, token scanner.NewToken) (int, []string) {
	score := 0
	var reasons []string

	dist, err := f.holderChecker.GetTokenHolders(ctx, token.Address)
	if err != nil {
		f.log.Debug("holder check failed (skipping)", "token", token.Address, "err", err)
		return 0, nil
	}

	maxPct := f.maxTopHolderPct
	if maxPct <= 0 {
		maxPct = 50
	}

	if dist.TopHolderPct > maxPct {
		score -= 15
		reasons = append(reasons, fmt.Sprintf("top holder %.0f%% > %.0f%% (-15)", dist.TopHolderPct, maxPct))
	} else if dist.TopHolderPct < 30 {
		score += 10
		reasons = append(reasons, fmt.Sprintf("distributed: top holder %.0f%% (+10)", dist.TopHolderPct))
	}

	if dist.TotalHolders >= 10 {
		score += 5
		reasons = append(reasons, fmt.Sprintf("%d holders (+5)", dist.TotalHolders))
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
			if mc > 50 {
				score += 5
				reasons = append(reasons, "mcap > 50 SOL (+5)")
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
