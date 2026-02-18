package flow

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultSurvivalModel_PositiveSignals(t *testing.T) {
	m := DefaultSurvivalModel()
	obs := ObservationResult{
		LiquidityVelocity: 0.3,
		OFI:               0.8,
		OFIAcceleration:   0.2,
		TradeEntropy:       1.5,
		TimingCV:           0.6,
		BotBuyCount:        0,
		BuyCount:           10,
		CurveProgress:      0.1,
	}
	score := m.SurvivalScore(obs, 75)
	if score <= 0 {
		t.Errorf("expected positive survival score for good signals, got %g", score)
	}
}

func TestDefaultSurvivalModel_NegativeSignals(t *testing.T) {
	m := DefaultSurvivalModel()
	obs := ObservationResult{
		LiquidityVelocity: 0,
		OFI:               -0.5,
		OFIAcceleration:   -0.5,
		TradeEntropy:       0,
		TimingCV:           0.1,
		BotBuyCount:        5,
		BuyCount:           1,
		CurveProgress:      0.95,
	}
	score := m.SurvivalScore(obs, 30)
	if score >= 0 {
		t.Errorf("expected negative survival score for bad signals, got %g", score)
	}
}

func TestSurvivalModel_LoadFromFile(t *testing.T) {
	m := DefaultSurvivalModel()
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "model.json")
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadSurvivalModel(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Betas) != len(m.Betas) {
		t.Fatalf("expected %d betas, got %d", len(m.Betas), len(loaded.Betas))
	}
	for i := range m.Betas {
		if loaded.Betas[i] != m.Betas[i] {
			t.Errorf("beta[%d]: expected %g, got %g", i, m.Betas[i], loaded.Betas[i])
		}
	}
}

func TestSurvivalModel_MismatchedDimensions(t *testing.T) {
	data := `{"betas":[1.0,2.0],"features":["a"]}`
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "bad_model.json")
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadSurvivalModel(path)
	if err == nil {
		t.Error("expected error for mismatched dimensions")
	}
}
