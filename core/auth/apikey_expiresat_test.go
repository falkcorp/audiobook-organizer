// file: core/auth/apikey_expiresat_test.go
// version: 0.1.0
// guid: 7d3c1f84-52ae-4b09-9c61-0e8a4f2b5d17
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

func expiresAtSpec(at time.Time) auth.KeySpec {
	return auth.KeySpec{Name: "grapnel", Scopes: []rbac.Permission{"gotham:patrol"}, ExpiresAt: at}
}

// TestAPIKey_ExpiresAtIsAnInstantOnTheServiceClock: ExpiresAt becomes a
// lifetime against the service's own clock, so the key ends at exactly the
// instant asked for (seams.d/F4-08-apikey-expiry.md item 2).
func TestAPIKey_ExpiresAtIsAnInstantOnTheServiceClock(t *testing.T) {
	f := newFx(t)
	dick := f.user("dick", "gotham.detective")
	at := f.clock().Add(72 * time.Hour)
	_, k, err := f.svc.CreateAPIKey(context.Background(), f.session(dick), expiresAtSpec(at))
	if err != nil {
		t.Fatal(err)
	}
	if !k.ExpiresAt.Equal(at) {
		t.Fatalf("key ends %s, want %s: the instant was not counted from the service clock", k.ExpiresAt, at)
	}
	// The conversion follows the clock when it moves.
	f.advance(10 * 24 * time.Hour)
	at = f.clock().Add(time.Hour)
	_, k, err = f.svc.CreateAPIKey(context.Background(), f.session(dick), expiresAtSpec(at))
	if err != nil || !k.ExpiresAt.Equal(at) {
		t.Fatalf("after the clock moved: %v, %v", k, err)
	}
}

// TestAPIKey_ExpiresAtMustBeInTheFuture: an instant at or before now is
// refused, never read as "the default lifetime" or as an already-dead key.
func TestAPIKey_ExpiresAtMustBeInTheFuture(t *testing.T) {
	f := newFx(t)
	dick := f.user("dick", "gotham.detective")
	for name, at := range map[string]time.Time{
		"now":              f.clock(),
		"a nanosecond ago": f.clock().Add(-time.Nanosecond),
		"yesterday":        f.clock().Add(-24 * time.Hour),
		"before the epoch": time.Unix(-1<<40, 0),
	} {
		_, _, err := f.svc.CreateAPIKey(context.Background(), f.session(dick), expiresAtSpec(at))
		if !errors.Is(err, auth.ErrBadInput) {
			t.Errorf("%s: %v, want ErrBadInput", name, err)
		}
	}
	if n := f.keyCount(); n != 0 {
		t.Fatalf("%d keys stored by refused calls", n)
	}
}

// TestAPIKey_ExpiresAtIsHeldToTheMaximum: the 1096-day ceiling and a shorter
// configured max_ttl both bound an instant exactly as they bound ExpiresIn.
func TestAPIKey_ExpiresAtIsHeldToTheMaximum(t *testing.T) {
	for _, tc := range []struct {
		name       string
		configured time.Duration
		ok, refuse time.Duration
	}{
		{"default ceiling", 0, threeYears, threeYears + time.Second},
		{"a configured value above the ceiling", 10 * 365 * 24 * time.Hour, threeYears, threeYears + time.Second},
		{"a shorter configured max_ttl", 30 * 24 * time.Hour, 30 * 24 * time.Hour, 30*24*time.Hour + time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFx(t)
			f.setSettings(auth.Settings{KeyMaxTTL: tc.configured})
			dick := f.user("dick", "gotham.detective")
			at := f.clock().Add(tc.ok)
			_, k, err := f.svc.CreateAPIKey(context.Background(), f.session(dick), expiresAtSpec(at))
			if err != nil || !k.ExpiresAt.Equal(at) {
				t.Fatalf("an instant %s out: %v, %v", tc.ok, k, err)
			}
			_, _, err = f.svc.CreateAPIKey(context.Background(), f.session(dick), expiresAtSpec(f.clock().Add(tc.refuse)))
			if !errors.Is(err, auth.ErrBadInput) {
				t.Fatalf("an instant %s out: %v, want ErrBadInput", tc.refuse, err)
			}
		})
	}
	// An instant so far out that the difference overflows a Duration.
	f := newFx(t)
	dick := f.user("dick", "gotham.detective")
	_, _, err := f.svc.CreateAPIKey(context.Background(), f.session(dick), expiresAtSpec(time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)))
	if !errors.Is(err, auth.ErrBadInput) {
		t.Fatalf("year 9999: %v, want ErrBadInput", err)
	}
}

// TestAPIKey_ExpiresAtAndExpiresInTogetherIsRefused: a spec that sets both
// is ambiguous and is refused whichever is shorter.
func TestAPIKey_ExpiresAtAndExpiresInTogetherIsRefused(t *testing.T) {
	f := newFx(t)
	dick := f.user("dick", "gotham.detective")
	for _, in := range []time.Duration{time.Hour, 48 * time.Hour, -time.Hour} {
		spec := expiresAtSpec(f.clock().Add(24 * time.Hour))
		spec.ExpiresIn = in
		if _, _, err := f.svc.CreateAPIKey(context.Background(), f.session(dick), spec); !errors.Is(err, auth.ErrBadInput) {
			t.Errorf("ExpiresIn %s with ExpiresAt: %v, want ErrBadInput", in, err)
		}
	}
}

// TestAPIKey_ExpiresAtIsClampedToTheCallingKey: the key-actor clamp still
// applies after the conversion, so a key cannot mint a key that outlives it
// by naming a far instant.
func TestAPIKey_ExpiresAtIsClampedToTheCallingKey(t *testing.T) {
	f := newFx(t)
	dick := f.user("dick", "gotham.detective")
	_, parent, err := f.svc.CreateAPIKey(context.Background(), f.session(dick), auth.KeySpec{
		Name: "parent", Scopes: []rbac.Permission{"gotham:patrol"}, ExpiresIn: 10 * 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	kp := f.keyPrincipal(f.reload(dick.ID), parent)
	_, child, err := f.svc.CreateAPIKey(context.Background(), kp, expiresAtSpec(f.clock().Add(100*24*time.Hour)))
	if err != nil {
		t.Fatal(err)
	}
	if !child.ExpiresAt.Equal(parent.ExpiresAt) {
		t.Fatalf("child ends %s, parent ends %s: the clamp did not apply", child.ExpiresAt, parent.ExpiresAt)
	}
	// Inside the parent's life the instant is kept.
	at := f.clock().Add(24 * time.Hour)
	_, child, err = f.svc.CreateAPIKey(context.Background(), kp, expiresAtSpec(at))
	if err != nil || !child.ExpiresAt.Equal(at) {
		t.Fatalf("%v, %v", child, err)
	}
}

// keyCount is how many API keys the store holds.
func (f *fx) keyCount() int {
	f.t.Helper()
	n := 0
	for _, err := range f.st.ScanAPIKeys(context.Background(), "") {
		if err != nil {
			f.t.Fatal(err)
		}
		n++
	}
	return n
}
