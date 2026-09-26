// file: internal/searchcache/cache_patch_cap_metric_test.go
// version: 1.1.0
// guid: 3f0b6a2e-9d41-4c77-8e15-b2a6c9d07e43
// last-edited: 2026-09-26

package searchcache

import (
	"context"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/falkcorp/audiobook-organizer/internal/metrics"
)

const patchCapMetric = "audiobook_organizer_search_cache_patch_cap_rebuilds_total"

const rebuildsMetric = "audiobook_organizer_search_cache_rebuilds_total"

// scrapePatchCapRebuilds reads the counter through the default registry, the
// one /metrics serves, so the test also proves the counter is registered.
func scrapePatchCapRebuilds(t *testing.T) float64 {
	t.Helper()
	return scrapeCounter(t, patchCapMetric)
}

// scrapeCounter reads one unlabelled counter through the default registry.
func scrapeCounter(t *testing.T, name string) float64 {
	t.Helper()
	metrics.Register()
	fams, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range fams {
		if f.GetName() == name {
			return f.GetMetric()[0].GetCounter().GetValue()
		}
	}
	t.Fatalf("%s is not registered with the default registry", name)
	return 0
}

// A change set past MaxPatchChanged forces a rebuild and is counted, both in
// Stats and in the Prometheus counter. A change set within the cap, and a
// rebuild forced by PatchLimit instead, are not.
func TestCache_PatchCapRebuildIsCounted(t *testing.T) {
	changes := NewChangeLog(64)
	c := New(changes, Config{PatchLimit: 1, MaxPatchChanged: 4})
	fe := newFake(map[string]string{"b1": "red"}, "red")
	ctx := context.Background()
	if _, err := c.Lookup(ctx, "k", fe, exactOpts); err != nil {
		t.Fatal(err)
	}
	before := scrapePatchCapRebuilds(t)

	// Past the cap: counted once.
	changes.Record("x1", "x2", "x3", "x4", "x5")
	if _, err := c.Lookup(ctx, "k", fe, webOpts(time.Second)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return c.Stats().Building == 0 })
	if got := c.Stats().PatchCapRebuilds; got != 1 {
		t.Fatalf("Stats().PatchCapRebuilds after a change set past the cap = %d; want 1", got)
	}
	if got := scrapePatchCapRebuilds(t) - before; got != 1 {
		t.Fatalf("%s rose by %v after a change set past the cap; want 1", patchCapMetric, got)
	}

	// Within the cap but more matches than PatchLimit: a rebuild, not counted.
	if _, err := c.Lookup(ctx, "k", fe, exactOpts); err != nil {
		t.Fatal(err)
	}
	fe.set("b2", "red")
	fe.set("b3", "red")
	changes.Record("b2", "b3")
	if _, err := c.Lookup(ctx, "k", fe, webOpts(time.Second)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return c.Stats().Building == 0 })

	// Within the cap and within PatchLimit: patched, not counted.
	if _, err := c.Lookup(ctx, "k", fe, exactOpts); err != nil {
		t.Fatal(err)
	}
	fe.set("b4", "red")
	changes.Record("b4")
	if _, err := c.Lookup(ctx, "k", fe, exactOpts); err != nil {
		t.Fatal(err)
	}

	st := c.Stats()
	if st.PatchCapRebuilds != 1 {
		t.Fatalf("Stats().PatchCapRebuilds = %d after PatchLimit and in-cap lookups; want still 1", st.PatchCapRebuilds)
	}
	if st.Rebuilds < 2 || st.Patches < 1 {
		t.Fatalf("stats %+v: want >= 2 rebuilds (cap + PatchLimit) and >= 1 patch", st)
	}
	if got := scrapePatchCapRebuilds(t) - before; got != 1 {
		t.Fatalf("%s rose by %v in total; want 1", patchCapMetric, got)
	}
}

// Every build the cache starts moves the Prometheus rebuild counter with
// Stats().Rebuilds: the first build after a miss, and the rebuild an
// out-of-date entry asks for. A lookup served from a current entry does not.
func TestCache_RebuildsAreExported(t *testing.T) {
	changes := NewChangeLog(4)
	c := New(changes, Config{})
	fe := newFake(map[string]string{"b1": "red"}, "red")
	ctx := context.Background()
	metricBefore := scrapeCounter(t, rebuildsMetric)
	statsBefore := c.Stats().Rebuilds

	if _, err := c.Lookup(ctx, "k", fe, exactOpts); err != nil { // miss: first build
		t.Fatal(err)
	}
	if _, err := c.Lookup(ctx, "k", fe, exactOpts); err != nil { // hit: no build
		t.Fatal(err)
	}
	// Overflow the 4-record ring: the entry can no longer be patched.
	changes.Record("x1", "x2", "x3", "x4", "x5")
	if _, err := c.Lookup(ctx, "k", fe, webOpts(time.Second)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return c.Stats().Building == 0 })

	statsDelta := c.Stats().Rebuilds - statsBefore
	if statsDelta != 2 {
		t.Fatalf("Stats().Rebuilds rose by %d; want 2 (first build + overflow rebuild)", statsDelta)
	}
	if got := scrapeCounter(t, rebuildsMetric) - metricBefore; got != float64(statsDelta) {
		t.Fatalf("%s rose by %v; want %d, the Stats().Rebuilds delta", rebuildsMetric, got, statsDelta)
	}
}
