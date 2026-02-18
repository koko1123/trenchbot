package flow

import (
	"fmt"
	"math"
	"math/rand"
	"sync"
)

// TokenBucket holds the Beta-Binomial posterior for a discretized token type.
type TokenBucket struct {
	Key   string
	Alpha float64 // wins + 1
	Beta  float64 // losses + 1
}

// ThompsonSampler implements Thompson sampling for multi-token allocation.
// When multiple tokens pass filters simultaneously, this picks the best allocation
// by sampling from each token type's posterior win rate distribution.
type ThompsonSampler struct {
	mu      sync.RWMutex
	buckets map[string]*TokenBucket
}

// NewThompsonSampler creates a sampler with empty buckets.
func NewThompsonSampler() *ThompsonSampler {
	return &ThompsonSampler{
		buckets: make(map[string]*TokenBucket),
	}
}

// Score samples a value from Beta(alpha, beta) for the given bucket key.
// Returns 0.5 (uniform prior) if the bucket doesn't exist.
func (ts *ThompsonSampler) Score(key string) float64 {
	ts.mu.RLock()
	b, ok := ts.buckets[key]
	ts.mu.RUnlock()

	if !ok {
		return betaSample(1, 1) // uniform prior
	}
	return betaSample(b.Alpha, b.Beta)
}

// Update records a win or loss for the given bucket key.
func (ts *ThompsonSampler) Update(key string, won bool) {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	b, ok := ts.buckets[key]
	if !ok {
		b = &TokenBucket{Key: key, Alpha: 1, Beta: 1}
		ts.buckets[key] = b
	}
	if won {
		b.Alpha++
	} else {
		b.Beta++
	}
}

// BucketKey discretizes a token into a type key based on its features.
// OFI bucket (3) × Velocity bucket (3) × Bot presence (2) = 18 buckets.
func BucketKey(obs ObservationResult, filterScore int) string {
	ofiBucket := "mid"
	switch {
	case obs.OFI < 0.3:
		ofiBucket = "lo"
	case obs.OFI > 0.6:
		ofiBucket = "hi"
	}

	velBucket := "mid"
	switch {
	case obs.LiquidityVelocity < 0.05:
		velBucket = "lo"
	case obs.LiquidityVelocity > 0.2:
		velBucket = "hi"
	}

	botPresent := "no"
	if obs.BotBuyCount > 2 {
		botPresent = "yes"
	}

	return fmt.Sprintf("ofi_%s:vel_%s:bot_%s", ofiBucket, velBucket, botPresent)
}

// WinRate returns the expected win rate for a bucket (posterior mean).
func (ts *ThompsonSampler) WinRate(key string) float64 {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	b, ok := ts.buckets[key]
	if !ok {
		return 0.5
	}
	return b.Alpha / (b.Alpha + b.Beta)
}

// betaSample generates a sample from a Beta(alpha, beta) distribution using
// the Gamma distribution relationship: X ~ Beta(a,b) = Ga/(Ga+Gb).
func betaSample(alpha, beta float64) float64 {
	if alpha <= 0 {
		alpha = 1
	}
	if beta <= 0 {
		beta = 1
	}
	ga := gammaSample(alpha)
	gb := gammaSample(beta)
	if ga+gb == 0 {
		return 0.5
	}
	return ga / (ga + gb)
}

// gammaSample generates a sample from Gamma(shape, 1) using Marsaglia and Tsang's method.
func gammaSample(shape float64) float64 {
	if shape < 1 {
		// For shape < 1, use the boost: Gamma(a) = Gamma(a+1) * U^(1/a)
		return gammaSample(shape+1) * math.Pow(rand.Float64(), 1.0/shape)
	}
	d := shape - 1.0/3.0
	c := 1.0 / math.Sqrt(9.0*d)
	for {
		var x, v float64
		for {
			x = rand.NormFloat64()
			v = 1.0 + c*x
			if v > 0 {
				break
			}
		}
		v = v * v * v
		u := rand.Float64()
		if u < 1.0-0.0331*(x*x)*(x*x) {
			return d * v
		}
		if math.Log(u) < 0.5*x*x+d*(1.0-v+math.Log(v)) {
			return d * v
		}
	}
}
