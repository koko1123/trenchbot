package flow

import (
	"encoding/json"
	"fmt"
	"os"
)

// SurvivalModel performs lightweight runtime inference from an offline-trained
// survival model. It predicts P(token survives > T minutes) given early signals.
// The model is a simple linear predictor: sum(beta_i * feature_i).
type SurvivalModel struct {
	Betas    []float64 `json:"betas"`
	Features []string  `json:"features"`
	Means    []float64 `json:"means"` // feature means from training data for centering
}

// LoadSurvivalModel reads a model from a JSON file.
func LoadSurvivalModel(path string) (*SurvivalModel, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading survival model: %w", err)
	}
	var m SurvivalModel
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parsing survival model: %w", err)
	}
	if len(m.Betas) != len(m.Features) {
		return nil, fmt.Errorf("survival model: betas (%d) != features (%d)", len(m.Betas), len(m.Features))
	}
	return &m, nil
}

// DefaultSurvivalModel returns a model with hardcoded coefficients derived from
// academic research findings. Liquidity velocity gets the highest weight.
// These should be replaced with trained coefficients after accumulating 200+ trades.
func DefaultSurvivalModel() *SurvivalModel {
	return &SurvivalModel{
		Features: []string{
			"liquidity_velocity",
			"ofi",
			"ofi_acceleration",
			"trade_entropy",
			"timing_cv",
			"bot_buy_count",
			"buy_count",
			"curve_progress",
			"filter_score",
		},
		Betas: []float64{
			2.0,   // liquidity_velocity: strongest predictor
			1.0,   // ofi: positive flow is bullish
			0.5,   // ofi_acceleration: increasing pressure helps
			0.3,   // trade_entropy: diverse buyers correlate with survival
			-0.2,  // timing_cv: low CV (bot-like) is slightly negative
			-0.3,  // bot_buy_count: more bots → worse
			0.1,   // buy_count: more buyers is mildly positive
			-0.5,  // curve_progress: higher progress = less upside
			0.02,  // filter_score: higher score is mildly positive
		},
	}
}

// SurvivalScore computes the linear predictor from observation and filter results.
// Higher = better survival probability. If means are provided (from training data),
// features are centered: score = sum(beta_i * (x_i - mean_i)).
func (m *SurvivalModel) SurvivalScore(obs ObservationResult, filterScore int) float64 {
	featureValues := m.extractFeatures(obs, filterScore)
	score := 0.0
	for i, beta := range m.Betas {
		if i < len(featureValues) {
			x := featureValues[i]
			if i < len(m.Means) {
				x -= m.Means[i]
			}
			score += beta * x
		}
	}
	return score
}

// extractFeatures maps observation results to the feature vector.
func (m *SurvivalModel) extractFeatures(obs ObservationResult, filterScore int) []float64 {
	values := make([]float64, len(m.Features))
	for i, f := range m.Features {
		switch f {
		case "liquidity_velocity":
			values[i] = obs.LiquidityVelocity
		case "ofi":
			values[i] = obs.OFI
		case "ofi_acceleration":
			values[i] = obs.OFIAcceleration
		case "trade_entropy":
			values[i] = obs.TradeEntropy
		case "timing_cv":
			values[i] = obs.TimingCV
		case "bot_buy_count":
			values[i] = float64(obs.BotBuyCount)
		case "buy_count":
			values[i] = float64(obs.BuyCount)
		case "curve_progress":
			values[i] = obs.CurveProgress
		case "filter_score":
			values[i] = float64(filterScore) / 100.0 // scale to 0-1 to match training
		}
	}
	return values
}
