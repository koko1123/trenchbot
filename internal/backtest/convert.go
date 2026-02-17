package backtest

import (
	"sort"
	"time"

	"github.com/cindocode/trenchbot/internal/scanner"
	"github.com/cindocode/trenchbot/internal/simulation"
	"github.com/cindocode/trenchbot/internal/state"
)

// ConvertToSyntheticTokens converts stored historical tokens into SyntheticTokens
// that the backtest engine can replay. Returns the tokens and the simStart time
// (the earliest token's PoolCreatedAt).
func ConvertToSyntheticTokens(stored []StoredToken) ([]simulation.SyntheticToken, time.Time) {
	if len(stored) == 0 {
		return nil, time.Time{}
	}

	sort.Slice(stored, func(i, j int) bool {
		return stored[i].PoolCreatedAt.Before(stored[j].PoolCreatedAt)
	})

	simStart := stored[0].PoolCreatedAt

	var tokens []simulation.SyntheticToken
	for _, st := range stored {
		if len(st.Candles) == 0 {
			continue
		}

		basePrice := st.Candles[0].Open
		if basePrice <= 0 {
			continue
		}

		createdAt := st.PoolCreatedAt
		emitTime := createdAt.Sub(simStart)

		// Convert candles to PricePoints relative to the token's creation time.
		curve := make([]simulation.PricePoint, 0, len(st.Candles))
		for _, c := range st.Candles {
			candleTime := time.Unix(c.UnixTime, 0).UTC()
			offset := candleTime.Sub(createdAt)
			if offset < 0 {
				offset = 0
			}
			curve = append(curve, simulation.PricePoint{
				Offset:     offset,
				Multiplier: c.Close / basePrice,
			})
		}

		metadata := make(map[string]interface{})
		if st.FdvUSD > 0 {
			metadata["fdv_usd"] = st.FdvUSD
		}
		if st.ReserveUSD > 0 {
			metadata["reserve_usd"] = st.ReserveUSD
		}
		if st.VolumeUSDH1 > 0 {
			metadata["volume_usd_h1"] = st.VolumeUSDH1
		}

		token := simulation.SyntheticToken{
			Token: scanner.NewToken{
				Chain:        state.ChainSolana,
				Address:      st.Address,
				Name:         st.Name,
				Symbol:       st.Symbol,
				ImageURL:     st.ImageURL,
				Timestamp:    createdAt,
				MarketCapUSD: st.MarketCapUSD,
				Metadata:     metadata,
			},
			Archetype:  "historical",
			PriceCurve: curve,
			EmitTime:   emitTime,
		}

		tokens = append(tokens, token)
	}

	return tokens, simStart
}
