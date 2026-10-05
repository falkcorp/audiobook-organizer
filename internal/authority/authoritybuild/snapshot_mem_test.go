// file: internal/authority/authoritybuild/snapshot_mem_test.go
// version: 1.0.0
// guid: 0d3f7b29-84c1-4e6a-9b52-c7e18a4d6f03
// last-edited: 2026-10-05

package authoritybuild

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/authority"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

// TestSnapshot_MemoryAtRealisticSize measures what LoadSnapshot keeps and
// allocates for an index of AUTHORITY_SNAPSHOT_PERSONS persons (default
// 80,000: a full author catalog, well above the ~77k-book library's own
// credits) and 5,000 publishers. Opt-in (AUTHORITY_SNAPSHOT_MEM=1): it is a
// measurement, recorded on authority.LoadSnapshot, not a gate.
func TestSnapshot_MemoryAtRealisticSize(t *testing.T) {
	if os.Getenv("AUTHORITY_SNAPSHOT_MEM") == "" {
		t.Skip("AUTHORITY_SNAPSHOT_MEM not set")
	}
	persons := 80000
	if v := os.Getenv("AUTHORITY_SNAPSHOT_PERSONS"); v != "" {
		_, err := fmt.Sscan(v, &persons)
		require.NoError(t, err)
	}
	ps := newStore(t)
	ctx := context.Background()
	b := NewBuilder()
	for i := 0; i < persons; i += 2 {
		b.AddProduct(authority.SourceCatalog, "", metadata.CatalogProduct{
			ASIN:      fmt.Sprintf("P%09d", i),
			Authors:   []metadata.CatalogContributor{{Name: fmt.Sprintf("Author Person %d", i), ASIN: fmt.Sprintf("B%09d", i)}},
			Narrators: []metadata.CatalogContributor{{Name: fmt.Sprintf("Narrator Person %d", i+1)}},
			Publisher: fmt.Sprintf("Publisher %d", (i/2)%5000),
		})
	}
	plan, err := PlanApply(ctx, ps, b.Finish(), allRan, t0, Options{Workers: 8})
	require.NoError(t, err)
	_, err = Apply(ctx, ps, plan, Options{Workers: 8})
	require.NoError(t, err)

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	snap, err := authority.LoadSnapshot(ctx, ps)
	require.NoError(t, err)
	runtime.ReadMemStats(&after)
	allocated := after.TotalAlloc - before.TotalAlloc
	runtime.GC()
	runtime.ReadMemStats(&after)
	retained := int64(after.HeapAlloc) - int64(before.HeapAlloc)
	t.Logf("snapshot persons=%d publishers=%d: allocated during load %.1f MiB, retained %.1f MiB",
		snap.Persons(), snap.Publishers(), float64(allocated)/(1<<20), float64(retained)/(1<<20))
	runtime.KeepAlive(snap)
}
