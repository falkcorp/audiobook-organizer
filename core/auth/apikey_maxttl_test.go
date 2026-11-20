// file: core/auth/apikey_maxttl_test.go
// version: 0.1.0
// guid: 04ab9c7e-efa2-4aef-89d5-10b858cb98c3
// last-edited: 2026-11-20

package auth_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"
)

const threeYears = 1095 * 24 * time.Hour

// TestAPIKey_MaxTTLIsThreeYears (§8, ABO owner decision 2026-11-20): a new
// key may live at most core.auth.apikey.max_ttl, three years by default; a
// configured value may shorten that but never raise it past three years.
func TestAPIKey_MaxTTLIsThreeYears(t *testing.T) {
	for _, tc := range []struct {
		name       string
		configured time.Duration
		ok, refuse time.Duration // the longest accepted, the shortest refused
	}{
		{"default", 0, threeYears, threeYears + time.Second},
		{"configured above the ceiling", 10 * 365 * 24 * time.Hour, threeYears, threeYears + time.Second},
		{"configured at the ceiling", threeYears, threeYears, threeYears + time.Second},
		{"configured shorter", 30 * 24 * time.Hour, 30 * 24 * time.Hour, 30*24*time.Hour + time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFx(t)
			f.setSettings(auth.Settings{KeyMaxTTL: tc.configured})
			ctx := context.Background()
			dick := f.user("dick", "gotham.detective")
			spec := func(d time.Duration) auth.KeySpec {
				return auth.KeySpec{Name: "grapnel", Scopes: []rbac.Permission{"gotham:patrol"}, ExpiresIn: d}
			}
			_, k, err := f.svc.CreateAPIKey(ctx, f.session(dick), spec(tc.ok))
			if err != nil {
				t.Fatalf("a %s key: %v", tc.ok, err)
			}
			if got := k.ExpiresAt.Sub(f.clock()); got != tc.ok {
				t.Fatalf("key lives %s, want %s", got, tc.ok)
			}
			if _, _, err := f.svc.CreateAPIKey(ctx, f.session(dick), spec(tc.refuse)); !errors.Is(err, auth.ErrBadInput) {
				t.Fatalf("a %s key: %v, want ErrBadInput", tc.refuse, err)
			}
		})
	}
}
