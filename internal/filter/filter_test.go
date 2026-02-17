package filter

import (
	"log/slog"
	"os"
	"testing"

	"github.com/cindocode/trenchbot/internal/scanner"
	"github.com/cindocode/trenchbot/internal/state"
)

var testLog = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

func TestEvaluate_EmptyToken(t *testing.T) {
	f := New(60, testLog)
	result := f.Evaluate(scanner.NewToken{})
	if result.Score != 0 {
		t.Errorf("expected score 0, got %d", result.Score)
	}
	if result.Approved {
		t.Error("empty token should not be approved")
	}
}

func TestEvaluate_FullMetadata(t *testing.T) {
	f := New(60, testLog)
	token := scanner.NewToken{
		Chain:       state.ChainSolana,
		Address:     "So11111111111111111111111111111111",
		Name:        "TestToken",
		Symbol:      "TEST",
		Description: "A test token with a longer description",
		ImageURL:    "https://example.com/img.png",
		Creator:     "creator123wallet",
		MarketCapUSD: 5000,
		Metadata: map[string]interface{}{
			"initialBuy":   1.5,
			"marketCapSol": 30.0,
		},
	}
	result := f.Evaluate(token)
	// metadata: 5+5+10+5=25, creator: 10+5=15, momentum: 5+10+10=25, chain: 10=10 = 75
	if result.Score < 70 {
		t.Errorf("full metadata token should score >=70, got %d", result.Score)
	}
	if !result.Approved {
		t.Error("full metadata token should be approved")
	}
}

func TestEvaluate_ThresholdBoundary(t *testing.T) {
	tests := []struct {
		name      string
		threshold int
		token     scanner.NewToken
		wantPass  bool
	}{
		{
			name:      "at threshold passes",
			threshold: 30,
			token: scanner.NewToken{
				Name:    "T",
				Symbol:  "T",
				Creator: "c",
				Description: "long enough description",
				ImageURL: "https://img.png",
			},
			wantPass: true,
		},
		{
			name:      "high threshold rejects sparse token",
			threshold: 80,
			token: scanner.NewToken{
				Name:   "T",
				Symbol: "T",
			},
			wantPass: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := New(tt.threshold, testLog)
			result := f.Evaluate(tt.token)
			if result.Approved != tt.wantPass {
				t.Errorf("approved=%v, want %v (score=%d)", result.Approved, tt.wantPass, result.Score)
			}
		})
	}
}

func TestEvaluate_MomentumScoring(t *testing.T) {
	f := New(0, testLog) // threshold 0 so everything passes

	noMcap := f.Evaluate(scanner.NewToken{MarketCapUSD: 0})
	withMcap := f.Evaluate(scanner.NewToken{MarketCapUSD: 500})
	highMcap := f.Evaluate(scanner.NewToken{MarketCapUSD: 1500})

	if withMcap.Score <= noMcap.Score {
		t.Error("mcap=500 should score higher than mcap=0")
	}
	if highMcap.Score <= withMcap.Score {
		t.Error("mcap=1500 should score higher than mcap=500")
	}
}

func TestEvaluate_ChainSpecificSolana(t *testing.T) {
	f := New(0, testLog)
	base := f.Evaluate(scanner.NewToken{})
	withBonding := f.Evaluate(scanner.NewToken{
		Metadata: map[string]interface{}{"marketCapSol": 10.0},
	})
	if withBonding.Score <= base.Score {
		t.Error("solana bonding curve metadata should add points")
	}
}

func TestEvaluate_ChainSpecificBNB(t *testing.T) {
	f := New(0, testLog)
	base := f.Evaluate(scanner.NewToken{})
	withTxHash := f.Evaluate(scanner.NewToken{
		Metadata: map[string]interface{}{"txHash": "0xabc123"},
	})
	if withTxHash.Score <= base.Score {
		t.Error("BNB txHash metadata should add points")
	}
}

func TestEvaluate_RealisticSolanaToken(t *testing.T) {
	f := New(60, testLog)
	token := scanner.NewToken{
		Chain:        state.ChainSolana,
		Address:      "pump123456789abcdef",
		Name:         "MOONCAT",
		Symbol:       "MCAT",
		Description:  "The next big memecoin on Solana! Community-driven.",
		ImageURL:     "https://pump.fun/img/mooncat.png",
		Creator:      "7xKXtg2CW87d97TXJSDpbD5jBkheTqA83TZRuJosgAsU",
		MarketCapUSD: 12000,
		Metadata: map[string]interface{}{
			"initialBuy":   2.5,
			"marketCapSol": 75.0,
		},
	}
	result := f.Evaluate(token)
	if !result.Approved {
		t.Errorf("realistic solana token should be approved, score=%d", result.Score)
	}
}

func TestEvaluate_RealisticBNBToken(t *testing.T) {
	f := New(60, testLog)
	// BNB tokens from Bitquery have sparser metadata
	token := scanner.NewToken{
		Chain:   state.ChainBNB,
		Address: "0xabc123def456",
		Creator: "0x1234567890abcdef",
		Metadata: map[string]interface{}{
			"txHash": "0xdeadbeef",
		},
	}
	result := f.Evaluate(token)
	// sparse BNB token: creator 15 + chain 10 = 25 -- should NOT pass at 60
	if result.Approved {
		t.Errorf("sparse BNB token should not pass threshold 60, score=%d", result.Score)
	}
}
