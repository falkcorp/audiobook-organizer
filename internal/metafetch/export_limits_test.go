// file: internal/metafetch/export_limits_test.go
// version: 1.0.0
// guid: 6e0a45c9-31b7-42fd-8c69-0d5b7e2af183
// last-edited: 2026-09-02

package metafetch

import (
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/metadata/providerhttp"
)

// These read back the limits that applyProviderLimits actually installed,
// rather than recomputing the expected value in the test. Asserting on stored
// state is the whole point: a test that recomputes the formula passes even when
// the value never reached providerhttp.
func effectiveRPSForTest(provider string) float64 {
	return providerhttp.EffectiveLimitsFor(provider).RPS
}
func effectiveBurstForTest(provider string) int {
	return providerhttp.EffectiveLimitsFor(provider).Burst
}
func effectiveTimeoutForTest(provider string) time.Duration {
	return providerhttp.EffectiveLimitsFor(provider).Timeout
}
