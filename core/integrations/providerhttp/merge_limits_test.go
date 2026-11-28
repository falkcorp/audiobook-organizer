// file: core/integrations/providerhttp/merge_limits_test.go
// version: 0.1.0
// guid: 7b3e9c14-5a60-4d82-9f1b-c2d84e06a375
// last-edited: 2026-11-22

package providerhttp_test

import (
	"testing"

	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/integrations/providerhttp"
)

// MaxConcurrent follows the rule of every Limits field: the instance's value
// replaces the adapter's when it is non-zero, and zero inherits.
func TestMergeLimits_MaxConcurrent(t *testing.T) {
	base := integrations.Limits{RPS: 2, MaxConcurrent: 2}
	for name, tc := range map[string]struct {
		over *integrations.Limits
		want int
	}{
		"no override":           {nil, 2},
		"a zero field inherits": {&integrations.Limits{RPS: 5}, 2},
		"a positive value wins": {&integrations.Limits{MaxConcurrent: 7}, 7},
		"a negative value wins": {&integrations.Limits{MaxConcurrent: -1}, -1},
		"it does not touch RPS": {&integrations.Limits{MaxConcurrent: 1}, 1},
	} {
		if got := providerhttp.MergeLimits(base, tc.over).MaxConcurrent; got != tc.want {
			t.Errorf("%s: MaxConcurrent = %d, want %d", name, got, tc.want)
		}
	}
	if got := providerhttp.MergeLimits(base, &integrations.Limits{MaxConcurrent: 1}).RPS; got != 2 {
		t.Errorf("an override of MaxConcurrent changed RPS to %v", got)
	}
}
