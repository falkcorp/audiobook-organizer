// file: internal/metafetch/provider_limits_test.go
// version: 1.1.0
// guid: 2b6f9a34-c07e-4d81-95a2-e34b7c018df6
// last-edited: 2026-09-02

package metafetch

import (
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/metadata/providerhttp"
)

// TestApplyProviderLimits_TierScalesTheBuiltin proves the tier is a multiplier
// over each provider's OWN built-in budget, not an absolute rate. Hardcover's
// documented 60/min is the anchor that must survive: an absolute per-tier
// number would either exceed it or throw away the other providers' headroom.
func TestApplyProviderLimits_TierScalesTheBuiltin(t *testing.T) {
	base := providerhttp.BuiltinLimitsFor("hardcover")

	for _, tc := range []struct {
		tier    config.RateLimitTier
		wantRPS float64
	}{
		{config.RateLimitTierLow, base.RPS * 0.5},
		{config.RateLimitTierMedium, base.RPS},
		{config.RateLimitTierHigh, base.RPS * 2.0},
		{"", base.RPS},         // unset
		{"nonsense", base.RPS}, // typo must not change the rate
	} {
		t.Run(string(tc.tier), func(t *testing.T) {
			applyProviderLimits(config.MetadataSource{
				ID:        "hardcover",
				RateLimit: config.MetadataSourceRateLimit{Tier: tc.tier},
			})
			got := effectiveRPSForTest("hardcover")
			if got != tc.wantRPS {
				t.Errorf("tier %q gave RPS %v, want %v (built-in %v)", tc.tier, got, tc.wantRPS, base.RPS)
			}
		})
	}
}

// TestApplyProviderLimits_AdvancedOverridesWinPerField: entering an exact RPS
// must not silently discard the tier-derived burst. Per-field precedence is the
// point of having both controls.
func TestApplyProviderLimits_AdvancedOverridesWinPerField(t *testing.T) {
	applyProviderLimits(config.MetadataSource{
		ID: "hardcover",
		RateLimit: config.MetadataSourceRateLimit{
			Tier:           config.RateLimitTierHigh,
			RPS:            7.5,
			TimeoutSeconds: 45,
		},
	})
	if got := effectiveRPSForTest("hardcover"); got != 7.5 {
		t.Errorf("explicit RPS = %v, want 7.5", got)
	}
	if got := effectiveTimeoutForTest("hardcover"); got != 45*time.Second {
		t.Errorf("explicit timeout = %v, want 45s", got)
	}
	// Burst was NOT set explicitly, so it must still reflect the high tier
	// rather than falling back to the unscaled built-in.
	base := providerhttp.BuiltinLimitsFor("hardcover")
	if got := effectiveBurstForTest("hardcover"); got <= base.Burst && base.Burst > 0 {
		t.Errorf("burst = %d, expected the high tier to have scaled it above the built-in %d", got, base.Burst)
	}
}

// TestApplyProviderLimits_EmptyIDCreatesNoBudget.
//
// A source with no id must not get a budget stored under "": that budget
// applies to no traffic at all, while reading back as a configured limit.
//
// Note what is deliberately NOT asserted here any more: an *unknown but
// non-empty* id now legitimately gets a budget. Nothing translates provider
// spellings, so an id that we do not ship is simply a provider we do not ship,
// and providerhttp gives unknown providers a conservative default. The phantom
// key this test originally guarded could only arise from the spelling
// translation that no longer exists.
func TestApplyProviderLimits_EmptyIDCreatesNoBudget(t *testing.T) {
	applyProviderLimits(config.MetadataSource{
		ID:        "",
		RateLimit: config.MetadataSourceRateLimit{Tier: config.RateLimitTierHigh},
	})
	if providerhttp.HasOverride("") {
		t.Error("a source with no id stored a budget under \"\"; it applies to no traffic " +
			"but reads as a configured limit")
	}
}

// TestApplyProviderLimits_RebuildsTheLiveClient is the guard against the
// setting that stores correctly and changes nothing.
//
// providerhttp.Client caches one client per provider for the life of the
// process, and that client keeps the rate limiter it was constructed with. So
// SetLimits alone updates a value that the running client never consults: the
// UI moves a slider, the config persists, and the actual request rate is
// unchanged until a restart.
//
// Asserting on the stored limits cannot see this — SetLimits stores them
// whether or not the client is rebuilt — so this test asserts on the CLIENT
// identity instead.
func TestApplyProviderLimits_RebuildsTheLiveClient(t *testing.T) {
	first := providerhttp.Client("hardcover")

	applyProviderLimits(config.MetadataSource{
		ID:        "hardcover",
		RateLimit: config.MetadataSourceRateLimit{Tier: config.RateLimitTierHigh},
	})

	second := providerhttp.Client("hardcover")
	if first == second {
		t.Fatal("the cached client survived a limits change; it still holds the OLD rate limiter, " +
			"so the new budget will not take effect until the process restarts")
	}
}
