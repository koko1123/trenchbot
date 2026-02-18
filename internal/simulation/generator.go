package simulation

import (
	"fmt"
	"math"
	"math/rand"
	"sort"
	"time"

	"github.com/cindocode/trenchbot/internal/scanner"
	"github.com/cindocode/trenchbot/internal/state"
)

// hourWeights defines hour-of-day token emission weight multipliers (UTC).
// Peak activity is during US afternoon/evening hours (16-19 UTC).
var hourWeights = []float64{
	0.3, 0.3, 0.3, 0.3, // 0-3
	0.6, 0.6, 0.6, 0.6, // 4-7
	0.8, 0.8, 0.8, 0.8, // 8-11
	1.0, 1.0, 1.0, 1.0, // 12-15
	1.8, 1.8, 1.8, 1.8, // 16-19
	1.0, 1.0, 1.0, 1.0, // 20-23
}

type TokenArchetype string

const (
	ArchetypeNakedRug    TokenArchetype = "naked-rug"    // 28% — sparse metadata, obvious rug
	ArchetypePolishedRug TokenArchetype = "polished-rug"  // 24% — full metadata, passes filter, still rugs
	ArchetypeSlowBleed   TokenArchetype = "slow-bleed"    // 15% — pumps modestly then slow-fades below entry
	ArchetypeSlow        TokenArchetype = "slow"           // 10% — modest pump then settles near entry
	ArchetypeModerate    TokenArchetype = "moderate"       // 10% — 2-4x
	ArchetypeMoonshot    TokenArchetype = "moonshot"       // 3%  — 5-15x (capped, not fantasy 50x)
	ArchetypeScam        TokenArchetype = "scam"           // 5%  — perfect metadata, pumps then instant rug
	ArchetypeDelayedRug  TokenArchetype = "delayed-rug"    // 2%  — looks moderate for 10min, then rugs
	ArchetypeHoneypot    TokenArchetype = "honeypot"       // 3%  — passes filter, sell always reverts
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
	RugClusterProb    float64 // probability of a rug cluster per token slot (default 0.03)
	RugClusterSize    int     // number of tokens in a cluster (default 4)
	TimeOfDayEnabled  bool    // default true
}

// GenerateResult holds the generated tokens and metadata about the generation.
type GenerateResult struct {
	Tokens      []SyntheticToken
	RugClusters int
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
	result := g.GenerateWithResult()
	return result.Tokens
}

func (g *TokenGenerator) GenerateWithResult() GenerateResult {
	totalTokens := int(g.cfg.SimulatedDuration.Hours()) * g.cfg.TokensPerHour
	if totalTokens == 0 {
		totalTokens = 1
	}

	// Build emit times based on time-of-day weighting or uniform distribution.
	emitTimes := g.buildEmitTimes(totalTokens)

	tokens := make([]SyntheticToken, 0, totalTokens)
	rugClusters := 0
	tokenIdx := 0

	for tokenIdx < totalTokens {
		// Check for rug cluster generation.
		if g.cfg.RugClusterProb > 0 && g.rng.Float64() < g.cfg.RugClusterProb {
			clusterSize := g.cfg.RugClusterSize
			if clusterSize <= 0 {
				clusterSize = 4
			}

			rugClusters++
			creator := fmt.Sprintf("cluster-creator-%d", rugClusters)
			baseTime := emitTimes[tokenIdx]

			for j := 0; j < clusterSize; j++ {
				// Pick a rug-like archetype for cluster tokens.
				var archetype TokenArchetype
				if g.rng.Float64() < 0.5 {
					archetype = ArchetypePolishedRug
				} else {
					archetype = ArchetypeScam
				}

				tok := g.generateToken(tokenIdx+j, archetype)
				tok.Token.Creator = creator
				tok.Token.Address = fmt.Sprintf("token_%04d_%s_cluster%d", tokenIdx+j, archetype, rugClusters)

				// Cluster tokens emit 30-60 seconds apart from the first.
				clusterOffset := time.Duration(j) * (30*time.Second + time.Duration(g.rng.Intn(31))*time.Second)
				tok.EmitTime = baseTime + clusterOffset

				tokens = append(tokens, tok)
			}

			// Advance past the slot we used; extra cluster tokens are additive.
			tokenIdx++
			continue
		}

		archetype := g.pickArchetype()
		tok := g.generateToken(tokenIdx, archetype)
		tok.EmitTime = emitTimes[tokenIdx]
		tokens = append(tokens, tok)
		tokenIdx++
	}

	// Sort by emit time to maintain chronological order after cluster insertion.
	sort.Slice(tokens, func(i, j int) bool {
		return tokens[i].EmitTime < tokens[j].EmitTime
	})

	return GenerateResult{
		Tokens:      tokens,
		RugClusters: rugClusters,
	}
}

// buildEmitTimes generates emit time offsets for the given number of tokens,
// using hour-of-day weighting when TimeOfDayEnabled is true.
func (g *TokenGenerator) buildEmitTimes(totalTokens int) []time.Duration {
	if !g.cfg.TimeOfDayEnabled {
		interval := g.cfg.SimulatedDuration / time.Duration(totalTokens)
		times := make([]time.Duration, totalTokens)
		for i := range times {
			times[i] = time.Duration(i) * interval
		}
		return times
	}

	totalHours := int(math.Ceil(g.cfg.SimulatedDuration.Hours()))
	if totalHours == 0 {
		totalHours = 1
	}

	// Calculate total weight across all simulated hours.
	totalWeight := 0.0
	for h := 0; h < totalHours; h++ {
		hourOfDay := h % 24
		totalWeight += hourWeights[hourOfDay]
	}

	// Allocate tokens per hour proportional to each hour's weight.
	tokensPerHour := make([]int, totalHours)
	allocated := 0
	for h := 0; h < totalHours; h++ {
		hourOfDay := h % 24
		count := int(math.Round(float64(totalTokens) * hourWeights[hourOfDay] / totalWeight))
		tokensPerHour[h] = count
		allocated += count
	}

	// Distribute rounding remainder to the highest-weight hours.
	diff := totalTokens - allocated
	for diff != 0 {
		for h := 0; h < totalHours && diff != 0; h++ {
			if diff > 0 {
				tokensPerHour[h]++
				diff--
			} else if diff < 0 && tokensPerHour[h] > 0 {
				tokensPerHour[h]--
				diff++
			}
		}
	}

	// Build emit times by distributing tokens uniformly within each hour.
	times := make([]time.Duration, 0, totalTokens)
	for h := 0; h < totalHours; h++ {
		count := tokensPerHour[h]
		if count == 0 {
			continue
		}
		hourStart := time.Duration(h) * time.Hour
		interval := time.Hour / time.Duration(count)
		for j := 0; j < count; j++ {
			times = append(times, hourStart+time.Duration(j)*interval)
		}
	}

	return times
}

func (g *TokenGenerator) pickArchetype() TokenArchetype {
	r := g.rng.Float64() * 100
	switch {
	case r < 28:
		return ArchetypeNakedRug
	case r < 52:
		return ArchetypePolishedRug
	case r < 67:
		return ArchetypeSlowBleed
	case r < 77:
		return ArchetypeSlow
	case r < 87:
		return ArchetypeModerate
	case r < 90:
		return ArchetypeMoonshot
	case r < 95:
		return ArchetypeScam
	case r < 97:
		return ArchetypeDelayedRug
	default:
		return ArchetypeHoneypot
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

	case ArchetypeHoneypot:
		// Full metadata like polished-rug — passes filter, but sell always reverts
		token.Name = fmt.Sprintf("Honey%d", idx)
		token.Symbol = fmt.Sprintf("HN%d", idx)
		token.Description = "Decentralized yield aggregator with audited contracts"
		token.ImageURL = "https://example.com/honey.png"
		token.MarketCapUSD = 1500 + g.rng.Float64()*4000
		token.Metadata["initialBuy"] = 0.5 + g.rng.Float64()*2.0
		token.Metadata["marketCapSol"] = 10.0 + g.rng.Float64()*40.0
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
	case ArchetypeHoneypot:
		return g.honeypotCurve()
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

func (g *TokenGenerator) honeypotCurve() []PricePoint {
	// Pumps to 1.5-2.5x like polished-rug, then dumps — but sell will revert anyway
	peak := 1.5 + g.rng.Float64()*1.0
	return []PricePoint{
		{0, 1.0},
		{5 * time.Minute, peak},
		{10 * time.Minute, peak * 0.8},
		{20 * time.Minute, peak * 0.3},
	}
}

// ArchetypeVolatility defines the stochastic price noise coefficient per archetype.
var ArchetypeVolatility = map[TokenArchetype]float64{
	ArchetypeNakedRug:    0.15,
	ArchetypePolishedRug: 0.10,
	ArchetypeSlowBleed:   0.08,
	ArchetypeSlow:        0.06,
	ArchetypeModerate:    0.08,
	ArchetypeMoonshot:    0.20,
	ArchetypeScam:        0.12,
	ArchetypeDelayedRug:  0.08,
	ArchetypeHoneypot:    0.10,
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
