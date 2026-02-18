package filter

// BundleDetector checks if a token's creation transaction was bundled with
// initial buy transactions from the same creator -- a strong rug signal.
type BundleDetector struct {
	enabled bool
}

// NewBundleDetector creates a new BundleDetector.
func NewBundleDetector(enabled bool) *BundleDetector {
	return &BundleDetector{enabled: enabled}
}

// IsBundled checks if the creator address matches any of the early buyer addresses.
// earlyBuyers is a list of addresses that bought within the first 2 seconds.
// Returns true if creator is among early buyers (bundled launch), or if any
// single address appears 3+ times in earlyBuyers (multi-buy bundle).
func (bd *BundleDetector) IsBundled(creator string, earlyBuyers []string) bool {
	if !bd.enabled {
		return false
	}

	counts := make(map[string]int, len(earlyBuyers))
	for _, addr := range earlyBuyers {
		if addr == creator {
			return true
		}
		counts[addr]++
		if counts[addr] >= 3 {
			return true
		}
	}

	return false
}
