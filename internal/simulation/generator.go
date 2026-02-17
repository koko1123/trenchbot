package simulation

import (
	"fmt"
	"math/rand"
	"time"

	"github.com/cindocode/trenchbot/internal/scanner"
	"github.com/cindocode/trenchbot/internal/state"
)

type TokenArchetype string

const (
	ArchetypeNakedRug    TokenArchetype = "naked-rug"    // 30% — sparse metadata, obvious rug
	ArchetypePolishedRug TokenArchetype = "polished-rug"  // 25% — full metadata, passes filter, still rugs
	ArchetypeSlowBleed   TokenArchetype = "slow-bleed"    // 15% — pumps modestly then slow-fades below entry
	ArchetypeSlow        TokenArchetype = "slow"           // 10% — modest pump then settles near entry
	ArchetypeModerate    TokenArchetype = "moderate"       // 10% — 2-4x
	ArchetypeMoonshot    TokenArchetype = "moonshot"       // 3%  — 5-15x (capped, not fantasy 50x)
	ArchetypeScam        TokenArchetype = "scam"           // 5%  — perfect metadata, pumps then instant rug
	ArchetypeDelayedRug  TokenArchetype = "delayed-rug"    // 2%  — looks moderate for 10min, then rugs
)

type PricePoint struct {
	Offset     time.Duration
	Multiplier float64
}

type SyntheticToken struct {
	Token      scanner.NewToken
	Archetype  TokenArchetype
	PriceCurve []PricePoint
	EmitTime   time.Duration // offset from simulation start
}

type GeneratorConfig struct {
	Seed              int64
	TokensPerHour     int
	SimulatedDuration time.Duration
	Chain             state.Chain
}

type TokenGenerator struct {
	rng *rand.Rand
	cfg GeneratorConfig
}

func NewTokenGenerator(cfg GeneratorConfig) *TokenGenerator {
	return &TokenGenerator{
		rng: rand.New(rand.NewSource(cfg.Seed)),
		cfg: cfg,
	}
}

func (g *TokenGenerator) Generate() []SyntheticToken {
	totalTokens := int(g.cfg.SimulatedDuration.Hours()) * g.cfg.TokensPerHour
	if totalTokens == 0 {
		totalTokens = 1
	}

	tokens := make([]SyntheticToken, 0, totalTokens)
	interval := g.cfg.SimulatedDuration / time.Duration(totalTokens)

	for i := 0; i < totalTokens; i++ {
		archetype := g.pickArchetype()
		token := g.generateToken(i, archetype)
		token.EmitTime = time.Duration(i) * interval
		tokens = append(tokens, token)
	}

	return tokens
}

func (g *TokenGenerator) pickArchetype() TokenArchetype {
	r := g.rng.Float64() * 100
	switch {
	case r < 30:
		return ArchetypeNakedRug
	case r < 55:
		return ArchetypePolishedRug
	case r < 70:
		return ArchetypeSlowBleed
	case r < 80:
		return ArchetypeSlow
	case r < 90:
		return ArchetypeModerate
	case r < 93:
		return ArchetypeMoonshot
	case r < 98:
		return ArchetypeScam
	default:
		return ArchetypeDelayedRug
	}
}

func (g *TokenGenerator) generateToken(idx int, archetype TokenArchetype) SyntheticToken {
	addr := fmt.Sprintf("token_%04d_%s", idx, archetype)
	creator := fmt.Sprintf("creator_%04d", idx)

	token := scanner.NewToken{
		Chain:    g.cfg.Chain,
		Address:  addr,
		Creator:  creator,
		Metadata: make(map[string]interface{}),
	}

	switch archetype {
	case ArchetypeNakedRug:
		// Sparse metadata — most should fail the filter
		if g.rng.Float64() < 0.3 {
			token.Name = fmt.Sprintf("X%d", idx)
			token.Symbol = fmt.Sprintf("X%d", idx)
		}
		if g.rng.Float64() < 0.15 {
			token.MarketCapUSD = g.rng.Float64() * 300
		}

	case ArchetypePolishedRug:
		// Looks legit: full metadata, mcap, initial buy — but it's a rug
		token.Name = fmt.Sprintf("Safe%d", idx)
		token.Symbol = fmt.Sprintf("SF%d", idx)
		token.Description = "Community-driven token with locked liquidity"
		token.ImageURL = "https://example.com/polished.png"
		token.MarketCapUSD = 1500 + g.rng.Float64()*4000
		token.Metadata["initialBuy"] = 0.5 + g.rng.Float64()*2.0
		token.Metadata["marketCapSol"] = 10.0 + g.rng.Float64()*40.0

	case ArchetypeSlowBleed:
		// Medium metadata, passes filter sometimes
		token.Name = fmt.Sprintf("Bleed%d", idx)
		token.Symbol = fmt.Sprintf("BL%d", idx)
		token.Description = "New project launching today"
		token.MarketCapUSD = 800 + g.rng.Float64()*2000
		token.Metadata["initialBuy"] = 0.3 + g.rng.Float64()*1.0
		token.Metadata["marketCapSol"] = 5.0 + g.rng.Float64()*20.0
		if g.rng.Float64() < 0.5 {
			token.ImageURL = "https://example.com/bleed.png"
		}

	case ArchetypeSlow:
		token.Name = fmt.Sprintf("Slow%d", idx)
		token.Symbol = fmt.Sprintf("SL%d", idx)
		token.Description = "Interesting project with potential"
		token.MarketCapUSD = 500 + g.rng.Float64()*2000
		token.Metadata["initialBuy"] = 0.3 + g.rng.Float64()
		token.Metadata["marketCapSol"] = 5.0 + g.rng.Float64()*20.0
		if g.rng.Float64() < 0.6 {
			token.ImageURL = "https://example.com/slow.png"
		}

	case ArchetypeModerate:
		token.Name = fmt.Sprintf("Token%d", idx)
		token.Symbol = fmt.Sprintf("TK%d", idx)
		token.Description = "A promising new token with strong community backing"
		token.ImageURL = "https://example.com/img.png"
		token.MarketCapUSD = 1000 + g.rng.Float64()*5000
		token.Metadata["initialBuy"] = 0.5 + g.rng.Float64()*2.0
		token.Metadata["marketCapSol"] = 10.0 + g.rng.Float64()*50.0

	case ArchetypeMoonshot:
		token.Name = fmt.Sprintf("Moon%d", idx)
		token.Symbol = fmt.Sprintf("MN%d", idx)
		token.Description = "Next 100x gem, NFA DYOR"
		token.ImageURL = "https://example.com/moon.png"
		token.MarketCapUSD = 2000 + g.rng.Float64()*6000
		token.Metadata["initialBuy"] = 1.0 + g.rng.Float64()*3.0
		token.Metadata["marketCapSol"] = 15.0 + g.rng.Float64()*50.0

	case ArchetypeScam:
		// Perfect metadata, designed to trick filters
		token.Name = fmt.Sprintf("Elite%d", idx)
		token.Symbol = fmt.Sprintf("EL%d", idx)
		token.Description = "Revolutionary DeFi protocol with innovative tokenomics and doxxed team"
		token.ImageURL = "https://example.com/legit.png"
		token.MarketCapUSD = 3000 + g.rng.Float64()*8000
		token.Metadata["initialBuy"] = 1.5 + g.rng.Float64()*3.0
		token.Metadata["marketCapSol"] = 25.0 + g.rng.Float64()*60.0

	case ArchetypeDelayedRug:
		// Looks like moderate for first 10min
		token.Name = fmt.Sprintf("Trust%d", idx)
		token.Symbol = fmt.Sprintf("TR%d", idx)
		token.Description = "Building the future of decentralized finance"
		token.ImageURL = "https://example.com/trust.png"
		token.MarketCapUSD = 2000 + g.rng.Float64()*5000
		token.Metadata["initialBuy"] = 0.8 + g.rng.Float64()*2.0
		token.Metadata["marketCapSol"] = 15.0 + g.rng.Float64()*40.0
	}

	curve := g.generatePriceCurve(archetype)

	return SyntheticToken{
		Token:      token,
		Archetype:  archetype,
		PriceCurve: curve,
	}
}

func (g *TokenGenerator) generatePriceCurve(archetype TokenArchetype) []PricePoint {
	switch archetype {
	case ArchetypeNakedRug:
		return g.nakedRugCurve()
	case ArchetypePolishedRug:
		return g.polishedRugCurve()
	case ArchetypeSlowBleed:
		return g.slowBleedCurve()
	case ArchetypeSlow:
		return g.slowCurve()
	case ArchetypeModerate:
		return g.moderateCurve()
	case ArchetypeMoonshot:
		return g.moonshotCurve()
	case ArchetypeScam:
		return g.scamCurve()
	case ArchetypeDelayedRug:
		return g.delayedRugCurve()
	default:
		return g.nakedRugCurve()
	}
}

func (g *TokenGenerator) nakedRugCurve() []PricePoint {
	peak := 1.05 + g.rng.Float64()*0.2 // 1.05-1.25x barely pumps
	crashTo := 0.02 + g.rng.Float64()*0.08

	return []PricePoint{
		{0, 1.0},
		{30 * time.Second, peak},
		{1 * time.Minute, peak * 0.7},
		{2 * time.Minute, crashTo * 2},
		{5 * time.Minute, crashTo},
		{30 * time.Minute, crashTo * 0.3},
	}
}

func (g *TokenGenerator) polishedRugCurve() []PricePoint {
	// Pumps convincingly for 2-5 minutes, then rugs
	pumpDuration := 2 + g.rng.Intn(4) // 2-5 min
	peak := 1.3 + g.rng.Float64()*0.7 // 1.3-2.0x — enough to look real
	crashTo := 0.03 + g.rng.Float64()*0.1

	return []PricePoint{
		{0, 1.0},
		{time.Duration(pumpDuration/2) * time.Minute, peak * 0.7},
		{time.Duration(pumpDuration) * time.Minute, peak},
		{time.Duration(pumpDuration+1) * time.Minute, peak * 0.4},
		{time.Duration(pumpDuration+2) * time.Minute, crashTo * 2},
		{time.Duration(pumpDuration+4) * time.Minute, crashTo},
		{30 * time.Minute, crashTo * 0.5},
	}
}

func (g *TokenGenerator) slowBleedCurve() []PricePoint {
	// Pumps to 1.3-1.8x then slowly bleeds below entry
	peak := 1.3 + g.rng.Float64()*0.5
	finalPrice := 0.4 + g.rng.Float64()*0.3 // ends 0.4-0.7x

	return []PricePoint{
		{0, 1.0},
		{3 * time.Minute, 1.1},
		{8 * time.Minute, peak},
		{15 * time.Minute, peak * 0.85},
		{25 * time.Minute, 0.9},
		{35 * time.Minute, finalPrice + 0.1},
		{60 * time.Minute, finalPrice},
	}
}

func (g *TokenGenerator) slowCurve() []PricePoint {
	peak := 1.2 + g.rng.Float64()*0.6 // 1.2-1.8x
	settle := 0.75 + g.rng.Float64()*0.35 // 0.75-1.1x — often below entry

	return []PricePoint{
		{0, 1.0},
		{5 * time.Minute, 1.0 + (peak-1.0)*0.5},
		{15 * time.Minute, peak},
		{20 * time.Minute, peak * 0.85},
		{30 * time.Minute, settle},
		{60 * time.Minute, settle * 0.9},
	}
}

func (g *TokenGenerator) moderateCurve() []PricePoint {
	peak := 2.0 + g.rng.Float64()*2.0 // 2-4x (not 2-5x)
	// Sometimes moderates fail and settle below 2x
	settle := peak * (0.3 + g.rng.Float64()*0.4) // 30-70% of peak

	return []PricePoint{
		{0, 1.0},
		{3 * time.Minute, 1.4},
		{8 * time.Minute, peak * 0.75},
		{12 * time.Minute, peak},
		{18 * time.Minute, peak * 0.7},
		{25 * time.Minute, settle + 0.3},
		{40 * time.Minute, settle},
		{60 * time.Minute, settle * 0.85},
	}
}

func (g *TokenGenerator) moonshotCurve() []PricePoint {
	peak := 5.0 + g.rng.Float64()*10.0 // 5-15x (capped, not 50x fantasy)
	// Moonshots have violent corrections
	dip1 := peak * (0.4 + g.rng.Float64()*0.2)  // 40-60% correction mid-run
	settle := peak * (0.15 + g.rng.Float64()*0.25) // settles at 15-40% of peak

	return []PricePoint{
		{0, 1.0},
		{2 * time.Minute, 1.8},
		{5 * time.Minute, peak * 0.3},
		{8 * time.Minute, dip1}, // violent dip
		{12 * time.Minute, peak * 0.8},
		{18 * time.Minute, peak},
		{25 * time.Minute, peak * 0.6},
		{35 * time.Minute, settle + 0.5},
		{50 * time.Minute, settle},
		{60 * time.Minute, settle * 0.8},
	}
}

func (g *TokenGenerator) scamCurve() []PricePoint {
	// Pumps hard to build confidence, then instant rug
	pumpPeak := 1.5 + g.rng.Float64()*1.0 // 1.5-2.5x
	return []PricePoint{
		{0, 1.0},
		{30 * time.Second, 1.2},
		{1 * time.Minute, pumpPeak * 0.8},
		{90 * time.Second, pumpPeak},
		{2 * time.Minute, 0.08},
		{3 * time.Minute, 0.03},
		{5 * time.Minute, 0.01},
		{30 * time.Minute, 0.005},
	}
}

func (g *TokenGenerator) delayedRugCurve() []PricePoint {
	// Looks like a moderate winner for 10-15 minutes, then rugs
	fakePeak := 2.0 + g.rng.Float64()*1.5 // 2-3.5x
	rugMin := 8 + g.rng.Intn(8)           // rugs at 8-15 min mark

	return []PricePoint{
		{0, 1.0},
		{3 * time.Minute, 1.5},
		{6 * time.Minute, fakePeak * 0.8},
		{time.Duration(rugMin) * time.Minute, fakePeak},
		{time.Duration(rugMin+1) * time.Minute, fakePeak * 0.3},
		{time.Duration(rugMin+2) * time.Minute, 0.1},
		{time.Duration(rugMin+5) * time.Minute, 0.03},
		{60 * time.Minute, 0.01},
	}
}

// InterpolatePrice returns the price at a given time offset by linearly interpolating the curve.
func InterpolatePrice(curve []PricePoint, offset time.Duration) float64 {
	if len(curve) == 0 {
		return 1.0
	}
	if offset <= curve[0].Offset {
		return curve[0].Multiplier
	}
	if offset >= curve[len(curve)-1].Offset {
		return curve[len(curve)-1].Multiplier
	}

	for i := 1; i < len(curve); i++ {
		if offset <= curve[i].Offset {
			prev := curve[i-1]
			curr := curve[i]
			segDuration := curr.Offset - prev.Offset
			if segDuration == 0 {
				return curr.Multiplier
			}
			t := float64(offset-prev.Offset) / float64(segDuration)
			return prev.Multiplier + t*(curr.Multiplier-prev.Multiplier)
		}
	}
	return curve[len(curve)-1].Multiplier
}
