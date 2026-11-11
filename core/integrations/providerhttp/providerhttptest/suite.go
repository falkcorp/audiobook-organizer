// file: core/integrations/providerhttp/providerhttptest/suite.go
// version: 1.0.0
// guid: aa23d90d-18ca-4cc2-9bdc-69dc1a2a2e7b
// last-edited: 2026-11-11

// Package providerhttptest is the public test-support package of
// integrations/providerhttp (03 §3.3, MOD5-6): the conformance suite every
// providerhttp.GateStore passes.
package providerhttptest

import (
	"context"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/integrations/providerhttp"
)

// Factory returns a fresh, empty GateStore for one subtest.
type Factory func(t *testing.T) providerhttp.GateStore

// GateStoreSuite runs the GateStore contract (05-metadata M2.3) against the
// stores factory returns: hold records round trip and a zero Until deletes
// them; quota counters round trip per scope and counter; the newest day is
// the one read back; and a scope or counter holding any byte stays inside its
// own record.
func GateStoreSuite(t *testing.T, factory Factory) {
	t.Helper()
	ctx := context.Background()
	at := time.Date(2039, 4, 1, 22, 0, 0, 0, time.UTC)
	hold := func(scope string, c integrations.Class) integrations.Hold {
		return integrations.Hold{Scope: scope, Class: c, Detail: "class=" + c.String(), Until: at.Add(time.Hour), SetAt: at}
	}

	t.Run("EmptyStore", func(t *testing.T) {
		s := factory(t)
		hs, err := s.Holds(ctx)
		if err != nil || len(hs) != 0 {
			t.Fatalf("Holds = %v, %v; want none", hs, err)
		}
		day, used, err := s.Quota(ctx, "gotham", "requests")
		if err != nil || day != "" || used != 0 {
			t.Fatalf("Quota = %q %d, %v; want empty", day, used, err)
		}
	})

	t.Run("HoldRoundTrip", func(t *testing.T) {
		s := factory(t)
		want := hold("metadata/batcave", integrations.ClassRateLimited)
		if err := s.PutHold(ctx, want); err != nil {
			t.Fatal(err)
		}
		hs, err := s.Holds(ctx)
		if err != nil || len(hs) != 1 || !sameHold(hs[0], want) {
			t.Fatalf("Holds = %+v, %v; want %+v", hs, err, want)
		}
		// A second Put of the scope replaces the record.
		want.Class, want.Until = integrations.ClassAuth, want.Until.Add(time.Hour)
		if err := s.PutHold(ctx, want); err != nil {
			t.Fatal(err)
		}
		hs, _ = s.Holds(ctx)
		if len(hs) != 1 || !sameHold(hs[0], want) {
			t.Fatalf("after overwrite Holds = %+v, want %+v", hs, want)
		}
	})

	t.Run("ZeroUntilDeletes", func(t *testing.T) {
		s := factory(t)
		a, b := hold("alfred", integrations.ClassQuota), hold("barbara", integrations.ClassAuth)
		for _, h := range []integrations.Hold{a, b} {
			if err := s.PutHold(ctx, h); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.PutHold(ctx, integrations.Hold{Scope: "alfred"}); err != nil {
			t.Fatal(err)
		}
		hs, _ := s.Holds(ctx)
		if len(hs) != 1 || hs[0].Scope != "barbara" {
			t.Fatalf("Holds = %+v, want only barbara", hs)
		}
		if err := s.PutHold(ctx, integrations.Hold{Scope: "never-was"}); err != nil {
			t.Fatalf("deleting an absent record: %v", err)
		}
	})

	t.Run("AwkwardScopes", func(t *testing.T) {
		s := factory(t)
		scopes := []string{"a", "a/b", "a\x00b", "a\x00", "metadata/x:0123456789ab", "ünï"}
		for _, sc := range scopes {
			if err := s.PutHold(ctx, hold(sc, integrations.ClassTransient)); err != nil {
				t.Fatalf("PutHold(%q): %v", sc, err)
			}
			if err := s.PutQuota(ctx, sc, "requests", "2039-04-01", len(sc)); err != nil {
				t.Fatalf("PutQuota(%q): %v", sc, err)
			}
		}
		hs, err := s.Holds(ctx)
		if err != nil || len(hs) != len(scopes) {
			t.Fatalf("Holds = %d, %v; want %d distinct records", len(hs), err, len(scopes))
		}
		got := map[string]bool{}
		for _, h := range hs {
			got[h.Scope] = true
		}
		for _, sc := range scopes {
			if !got[sc] {
				t.Errorf("hold %q lost", sc)
			}
			if _, used, err := s.Quota(ctx, sc, "requests"); err != nil || used != len(sc) {
				t.Errorf("Quota(%q) = %d, %v; want %d: a scope leaked into a neighbour's records", sc, used, err, len(sc))
			}
		}
	})

	t.Run("QuotaRoundTrip", func(t *testing.T) {
		s := factory(t)
		if err := s.PutQuota(ctx, "gotham", "query", "2039-04-01", 7); err != nil {
			t.Fatal(err)
		}
		if err := s.PutQuota(ctx, "gotham", "grab", "2039-04-01", 2); err != nil {
			t.Fatal(err)
		}
		if err := s.PutQuota(ctx, "metropolis", "query", "2039-04-01", 99); err != nil {
			t.Fatal(err)
		}
		for _, c := range []struct {
			scope, counter string
			used           int
		}{{"gotham", "query", 7}, {"gotham", "grab", 2}, {"metropolis", "query", 99}} {
			day, used, err := s.Quota(ctx, c.scope, c.counter)
			if err != nil || day != "2039-04-01" || used != c.used {
				t.Errorf("Quota(%s,%s) = %q %d, %v; want 2039-04-01 %d", c.scope, c.counter, day, used, err, c.used)
			}
		}
		if _, used, _ := s.Quota(ctx, "gotham", "other"); used != 0 {
			t.Errorf("an unwritten counter read %d", used)
		}
	})

	t.Run("NewestDayWins", func(t *testing.T) {
		s := factory(t)
		for _, w := range []struct {
			day  string
			used int
		}{{"2039-04-01", 5}, {"2039-04-03", 1}, {"2039-04-02", 9}} {
			if err := s.PutQuota(ctx, "gotham", "requests", w.day, w.used); err != nil {
				t.Fatal(err)
			}
		}
		day, used, err := s.Quota(ctx, "gotham", "requests")
		if err != nil || day != "2039-04-03" || used != 1 {
			t.Fatalf("Quota = %q %d, %v; want the newest day, 2039-04-03 1", day, used, err)
		}
	})

	t.Run("SameDayOverwrite", func(t *testing.T) {
		s := factory(t)
		for n := 1; n <= 3; n++ {
			if err := s.PutQuota(ctx, "gotham", "requests", "2039-04-01", n); err != nil {
				t.Fatal(err)
			}
		}
		if _, used, _ := s.Quota(ctx, "gotham", "requests"); used != 3 {
			t.Fatalf("used = %d, want the last write, 3", used)
		}
	})
}

func sameHold(a, b integrations.Hold) bool {
	return a.Scope == b.Scope && a.Class == b.Class && a.Detail == b.Detail && a.Until.Equal(b.Until) && a.SetAt.Equal(b.SetAt)
}
