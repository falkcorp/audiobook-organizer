// file: core/integrations/providerhttp/store_test.go
// version: 1.0.0
// guid: b6fe429f-bf7f-4747-a83b-6a00a1ab305b
// last-edited: 2026-11-11

package providerhttp_test

import (
	"context"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/integrations/providerhttp"
	"github.com/falkcorp/mediacore/integrations/providerhttp/providerhttptest"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/keys"
)

// TestNativeGateStore_Suite runs the conformance suite against NativeGateStore.
func TestNativeGateStore_Suite(t *testing.T) {
	providerhttptest.GateStoreSuite(t, func(t *testing.T) providerhttp.GateStore {
		return providerhttp.NativeGateStore(store(t))
	})
}

func rawKeys(t *testing.T, ks storage.Keyspace) map[string]string {
	t.Helper()
	out := map[string]string{}
	for kv, err := range ks.ScanPrefix(context.Background(), nil) {
		if err != nil {
			t.Fatal(err)
		}
		out[string(kv.Key)] = string(kv.Value)
	}
	return out
}

// TestNativeGateStore_RoundTrip (SIMP4-3): the layout is holds/<scope> and
// quota/<scope>/<counter>/<day>, days before yesterday go on the next write
// of the counter, and a damaged row fails the way M2.5 says.
func TestNativeGateStore_RoundTrip(t *testing.T) {
	ctx := context.Background()
	ks := store(t)
	gs := providerhttp.NativeGateStore(ks)
	at := time.Date(2039, 4, 1, 22, 0, 0, 0, time.UTC)
	h := integrations.Hold{Scope: "metadata/batcave", Class: integrations.ClassQuota, Detail: "class=quota", Until: at.Add(4 * time.Hour), SetAt: at}
	if err := gs.PutHold(ctx, h); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"2039-03-28", "2039-03-30", "2039-03-31", "2039-04-01"} {
		if err := gs.PutQuota(ctx, "metadata/batcave", "requests", d, 3); err != nil {
			t.Fatal(err)
		}
	}
	raw := rawKeys(t, ks)
	want := []string{
		string(keys.Join(keys.Str("holds"), keys.Str("metadata/batcave"))),
		string(keys.Join(keys.Str("quota"), keys.Str("metadata/batcave"), keys.Str("requests"), keys.Str("2039-03-31"))),
		string(keys.Join(keys.Str("quota"), keys.Str("metadata/batcave"), keys.Str("requests"), keys.Str("2039-04-01"))),
	}
	if len(raw) != len(want) {
		t.Fatalf("rows = %d, want %d (the hold, yesterday and today): %q", len(raw), len(want), raw)
	}
	for _, k := range want {
		if _, ok := raw[k]; !ok {
			t.Errorf("missing row %q", k)
		}
	}

	// A hold row nobody can decode is skipped, the rest are served.
	if err := ks.Set(ctx, keys.Join(keys.Str("holds"), keys.Str("damaged")), []byte("not a record")); err != nil {
		t.Fatal(err)
	}
	hs, err := gs.Holds(ctx)
	if err != nil || len(hs) != 1 || hs[0].Scope != h.Scope {
		t.Fatalf("Holds with a damaged row = %+v, %v; want the good one only", hs, err)
	}
	// A quota row nobody can decode is an ERROR: the counter fails closed.
	if err := ks.Set(ctx, keys.Join(keys.Str("quota"), keys.Str("metadata/batcave"), keys.Str("requests"), keys.Str("2039-04-02")), []byte("junk")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := gs.Quota(ctx, "metadata/batcave", "requests"); err == nil {
		t.Fatal("a damaged quota row was read as a count")
	}
	// A bad day is refused, not stored.
	if err := gs.PutQuota(ctx, "x", "requests", "yesterday", 1); err == nil {
		t.Fatal("a quota day that is not YYYY-MM-DD was stored")
	}
	if err := gs.PutHold(ctx, integrations.Hold{Until: at}); err == nil {
		t.Fatal("a hold with no scope was stored")
	}
}
