// file: internal/authority/store_test.go
// version: 1.0.0
// guid: 1174eeba-dc3c-4dc8-a8fe-ed7d13c8bb81
// last-edited: 2026-10-04

package authority

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

func stringsReader(s string) io.Reader { return strings.NewReader(s) }

func newStore(t *testing.T) *database.PebbleStore {
	t.Helper()
	ps, err := database.NewPebbleStoreInMemory(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = ps.Close() })
	return ps
}

func countOwned(t *testing.T, kv database.RawKVStore) map[string]int64 {
	t.Helper()
	out := map[string]int64{}
	for _, p := range KeyPrefixes() {
		n, err := kv.CountPrefix(p)
		require.NoError(t, err)
		out[p] = n
	}
	return out
}

func sampleResult() *Result {
	b := NewBuilder()
	b.AddProduct(SourceCatalog, prod("P1", []metadata.CatalogContributor{c("Ann Leckie", "B001JP7W9E")}, []metadata.CatalogContributor{c("Adjoa Andoh", "")}))
	b.AddProduct(SourceCatalog, prod("P2", []metadata.CatalogContributor{c("Dennis E. Taylor", "B00LDQ7AWU")}, []metadata.CatalogContributor{c("Ray Porter", "B00AAAAAAA")}))
	return b.Finish()
}

var t0 = time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)

func TestPlan_DryRunWritesNothing(t *testing.T) {
	ps := newStore(t)
	before := countOwned(t, ps)
	plan, err := PlanApply(context.Background(), ps, sampleResult(), t0, 4)
	require.NoError(t, err)
	require.NotEmpty(t, plan.Writes)
	require.Equal(t, before, countOwned(t, ps), "planning must not write")
}

func TestApply_IsIdempotent(t *testing.T) {
	ps := newStore(t)
	ctx := context.Background()
	res := sampleResult()
	plan, err := PlanApply(ctx, ps, res, t0, 4)
	require.NoError(t, err)
	got, err := Apply(ctx, ps, plan, 4)
	require.NoError(t, err)
	require.Equal(t, len(plan.Writes), got.Written)
	require.EqualValues(t, len(res.Ledger), countOwned(t, ps)[SourcePrefix], "one ledger row per product")

	// Same sources an hour later: nothing changed, so nothing is written
	// (timestamps are not content) and nothing is stale.
	plan2, err := PlanApply(ctx, ps, sampleResult(), t0.Add(time.Hour), 4)
	require.NoError(t, err)
	require.Empty(t, plan2.Writes)
	require.Empty(t, plan2.Stale)
	for _, p := range RebuildablePrefixes() {
		require.Zero(t, plan2.Counts[p].Write, p)
	}

	// A changed product rewrites its rows and keeps FirstSeen.
	b := NewBuilder()
	b.AddProduct(SourceCatalog, prod("P1", []metadata.CatalogContributor{c("Ann Leckie", "B001JP7W9E")}, []metadata.CatalogContributor{c("Adjoa Andoh", ""), c("Celeste Ciulla", "")}))
	b.AddProduct(SourceCatalog, prod("P2", []metadata.CatalogContributor{c("Dennis E. Taylor", "B00LDQ7AWU")}, []metadata.CatalogContributor{c("Ray Porter", "B00AAAAAAA")}))
	plan3, err := PlanApply(ctx, ps, b.Finish(), t0.Add(2*time.Hour), 4)
	require.NoError(t, err)
	require.Equal(t, 1, plan3.Counts[SourcePrefix].Write, "only P1's ledger digest changed")
	_, err = Apply(ctx, ps, plan3, 4)
	require.NoError(t, err)
	raw, err := ps.GetRaw(PersonPrefix + "celesteciulla")
	require.NoError(t, err)
	require.NotNil(t, raw)
	raw, err = ps.GetRaw(PersonPrefix + "annleckie")
	require.NoError(t, err)
	var p Person
	require.NoError(t, json.Unmarshal(raw, &p))
	require.True(t, p.FirstSeen.Equal(t0), "an unchanged row keeps its first write")
}

func TestApply_StaleRowsGoOverridesSurvive(t *testing.T) {
	ps := newStore(t)
	ctx := context.Background()
	plan, err := PlanApply(ctx, ps, sampleResult(), t0, 4)
	require.NoError(t, err)
	_, err = Apply(ctx, ps, plan, 4)
	require.NoError(t, err)

	require.NoError(t, PutPersonOverride(ps, PersonOverride{Name: "Dragon Born", Roles: map[Role]bool{RoleAuthor: false}, Note: "series tag"}))
	require.NoError(t, PutPersonOverride(ps, PersonOverride{Name: "Ray Porter", Roles: map[Role]bool{RoleAuthor: true}}))
	require.NoError(t, PutPublisherOverride(ps, PublisherOverride{Name: "Podium Publishing", Known: true, Canonical: "Podium Audio"}))

	// Rebuild from nothing: every rebuilt row is stale, overrides stay.
	empty := NewBuilder().Finish()
	plan2, err := PlanApply(ctx, ps, empty, t0, 4)
	require.NoError(t, err)
	require.NotEmpty(t, plan2.Stale)
	for _, k := range plan2.Stale {
		require.False(t, strings.HasPrefix(k, OverridePrefix), k)
	}
	_, err = Apply(ctx, ps, plan2, 4)
	require.NoError(t, err)
	n := countOwned(t, ps)
	for _, p := range RebuildablePrefixes() {
		require.Zero(t, n[p], p)
	}
	require.EqualValues(t, 3, n[OverridePrefix])

	x := NewIndex(ps)
	e, err := x.Lookup("ray porter")
	require.NoError(t, err)
	require.True(t, e.Qualifies(RoleAuthor), "an override alone is tier-O evidence")
	require.Equal(t, TierO, e.Tier)
	ok, err := x.IsKnownPerson("Dragon Born", RoleAuthor)
	require.NoError(t, err)
	require.False(t, ok)
	cn, ok, err := x.CanonicalPublisher("podium publishing")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "Podium Audio", cn)

	// An apply refuses to touch an override key even if handed one.
	_, err = Apply(ctx, ps, &Plan{Stale: []string{OverridePrefix + "person:rayporter"}}, 1)
	require.Error(t, err)
	_, err = Apply(ctx, ps, &Plan{Writes: []KeyValue{{Key: OverridePrefix + "person:x", Value: []byte("{}")}}}, 1)
	require.Error(t, err)
}

func TestLookup_SnapshotMatchesIndex(t *testing.T) {
	ps := newStore(t)
	ctx := context.Background()
	b := NewBuilder()
	b.AddProduct(SourceCatalog, prod("P1", []metadata.CatalogContributor{c("Ann Leckie", "B001JP7W9E")}, []metadata.CatalogContributor{c("Adjoa Andoh", "")}))
	cast := prod("P2", []metadata.CatalogContributor{c("Nicholas Briggs", "")}, nil)
	cast.Publisher = "Big Finish Productions"
	b.AddProduct(SourceCatalog, cast)
	plan, err := PlanApply(ctx, ps, b.Finish(), t0, 4)
	require.NoError(t, err)
	_, err = Apply(ctx, ps, plan, 4)
	require.NoError(t, err)
	require.NoError(t, PutPersonOverride(ps, PersonOverride{Name: "Adjoa Andoh", Roles: map[Role]bool{RoleNarrator: false}}))

	snap, err := LoadSnapshot(ctx, ps)
	require.NoError(t, err)
	x := NewIndex(ps)
	for _, name := range []string{"Ann Leckie", "ANN LECKIE", "Adjoa Andoh", "Nicholas Briggs", "Nobody Known"} {
		for _, role := range []Role{RoleAuthor, RoleNarrator, RoleCastAuthor} {
			want, err := x.IsKnownPerson(name, role)
			require.NoError(t, err)
			require.Equal(t, want, snap.IsKnownPerson(name, role), "%s/%s", name, role)
		}
	}
	require.True(t, snap.IsKnownPerson("Ann Leckie", RoleAuthor))
	require.False(t, snap.IsKnownPerson("Nicholas Briggs", RoleAuthor), "cast never counts as author")
	require.True(t, snap.IsKnownPerson("Nicholas Briggs", RoleCastAuthor))
	require.False(t, snap.IsKnownPerson("Adjoa Andoh", RoleNarrator), "blocked by override")
	require.Nil(t, snap.Person("Nobody Known"))

	cn, ok := snap.CanonicalPublisher("big finish productions")
	require.True(t, ok)
	require.Equal(t, "Big Finish Productions", cn)
	pub := snap.Publisher("Big Finish Productions")
	require.Equal(t, 1, pub.ManualOnlyCount)

	require.Equal(t, []string{"B001JP7W9E"}, snap.AuthorASINs("Ann Leckie"))
	require.Nil(t, snap.AuthorASINs("Nicholas Briggs"))

	toks := snap.ClassifyTokens([]string{"Ann Leckie", "Ancillary Justice", "Nicholas Briggs", "Podium Audio"})
	require.Len(t, toks, 4)
	require.Equal(t, []Role{RoleAuthor}, toks[0].Roles)
	require.Nil(t, toks[1].Entry)
	require.Equal(t, []Role{RoleCastAuthor}, toks[2].Roles)
	require.NotNil(t, toks[3].Publisher)

	var empty Lookup = Empty()
	require.False(t, empty.IsKnownPerson("Ann Leckie", RoleAuthor))
	require.Len(t, empty.ClassifyTokens([]string{"a", "b"}), 2)
	ref, err := x.LookupASIN("b001jp7w9e")
	require.NoError(t, err)
	require.Equal(t, []string{"annleckie"}, ref.Folds)
}

// TestCatalogRaw_ReadableThroughRawKV proves the catalog store and the raw
// KV surface share one Pebble DB: a payload CatalogStore wrote is what
// ScanCatalogRaw reads. An empty catalog yields zero pages.
func TestCatalogRaw_ReadableThroughRawKV(t *testing.T) {
	ps := newStore(t)
	ctx := context.Background()
	calls := 0
	require.NoError(t, ScanCatalogRaw(ctx, ps, 10, func([]database.KVPair) error { calls++; return nil }))
	require.Zero(t, calls, "empty catalog: 0 rows")

	raw := []byte(`{"asin":"B0TESTPROD","title":"T","authors":[{"name":"Ann Leckie","asin":"B001JP7W9E"}],"narrators":[{"name":"Adjoa Andoh","asin":null}],"publisher_name":"Orbit"}`)
	cs := database.NewCatalogStore(ps.DB())
	_, err := cs.UpsertEntries([]database.CatalogUpsert{{
		Entry: database.CatalogEntry{Provider: "audible", Marketplace: "us", ProviderID: "B0TESTPROD", Title: "T"},
		Raw:   raw,
	}}, "annleckie")
	require.NoError(t, err)

	b := NewBuilder()
	require.NoError(t, ScanCatalogRaw(ctx, ps, 10, func(pairs []database.KVPair) error {
		calls++
		for _, p := range pairs {
			require.NoError(t, b.AddRawProduct(SourceCatalog, p.Value))
		}
		return nil
	}))
	require.Equal(t, 1, calls)
	res := b.Finish()
	require.Equal(t, TierA, res.Persons["annleckie"].Tier)
	require.Equal(t, TierB, res.Persons["adjoaandoh"].Tier)
	require.Contains(t, res.Ledger, SourcePrefix+SourceCatalog+":B0TESTPROD")
}
