// file: internal/database/pebble_store_metadata_cache_summaries_test.go
// version: 1.0.0
// guid: 9b4e1f73-0c28-4d6a-a5e9-3f7d2c8b6e10
// last-edited: 2026-10-06

package database

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"
)

func newSummaryStore(t testing.TB) *PebbleStore {
	t.Helper()
	p, err := NewPebbleStoreInMemory(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	return p
}

func putCache(t testing.TB, p *PebbleStore, id string, n int, at time.Time) {
	t.Helper()
	cands := make([]json.RawMessage, n)
	for i := range cands {
		cands[i] = json.RawMessage(fmt.Sprintf(`{"title":"c%d","description":%q}`, i, strings.Repeat("d", 200)))
	}
	if err := p.PutMetadataCache(&MetadataCandidateCache{BookID: id, Candidates: cands, FetchedAt: at}); err != nil {
		t.Fatal(err)
	}
}

// scanOracle is what ListMetadataCacheKeys returned before the index: a full
// decode of every row, sorted FetchedAt desc / BookID asc.
func scanOracle(t testing.TB, p *PebbleStore) []MetadataCacheSummary {
	t.Helper()
	m, err := p.scanMetadataCacheSummaries()
	if err != nil {
		t.Fatal(err)
	}
	out := make([]MetadataCacheSummary, 0, len(m))
	for _, s := range m {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].FetchedAt.Equal(out[j].FetchedAt) {
			return out[i].FetchedAt.After(out[j].FetchedAt)
		}
		return out[i].BookID < out[j].BookID
	})
	return out
}

func assertSummariesMatchScan(t *testing.T, p *PebbleStore, when string) {
	t.Helper()
	got, err := p.ListMetadataCacheKeys()
	if err != nil {
		t.Fatal(err)
	}
	want := scanOracle(t, p)
	if len(got) == 0 && len(want) == 0 {
		return
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s: index (%d rows) differs from a fresh scan (%d rows)", when, len(got), len(want))
	}
}

// TestMetadataCacheSummaries_TracksEveryWriter: after the first (scanning)
// call, every writer of the keyspace -- Put, Delete, the UpdateBook identity
// delete, DeleteBook, SetRaw, DeleteRaw and Reset -- is reflected by the next
// call exactly as a fresh scan would see it.
func TestMetadataCacheSummaries_TracksEveryWriter(t *testing.T) {
	p := newSummaryStore(t)
	base := time.Now().UTC().Truncate(time.Second)
	for i := range 30 {
		id := fmt.Sprintf("bk%02d", i)
		if _, err := p.CreateBook(&Book{ID: id, Title: "Title " + id, FilePath: "/l/" + id + ".m4b", Format: "m4b"}); err != nil {
			t.Fatal(err)
		}
		putCache(t, p, id, i%4, base.Add(-time.Duration(i%5)*time.Minute)) // ties
	}
	assertSummariesMatchScan(t, p, "first build")

	putCache(t, p, "bk03", 9, base.Add(time.Hour))
	putCache(t, p, "new1", 1, base)
	assertSummariesMatchScan(t, p, "after Put")

	if err := p.DeleteMetadataCache("bk04"); err != nil {
		t.Fatal(err)
	}
	assertSummariesMatchScan(t, p, "after DeleteMetadataCache")

	b, err := p.GetBookByID("bk05")
	if err != nil || b == nil {
		t.Fatalf("get: %v", err)
	}
	b.Title = "A Completely Different Title"
	if _, err := p.UpdateBook("bk05", b); err != nil {
		t.Fatal(err)
	}
	if e, _ := p.GetMetadataCache("bk05"); e != nil {
		t.Fatal("fixture: identity change should have deleted the cache row")
	}
	assertSummariesMatchScan(t, p, "after UpdateBook identity delete")

	if err := p.DeleteBook("bk06"); err != nil {
		t.Fatal(err)
	}
	assertSummariesMatchScan(t, p, "after DeleteBook")

	if err := p.SetRaw(metadataCacheKeyPrefix+"bk07", []byte("{not json")); err != nil {
		t.Fatal(err)
	}
	assertSummariesMatchScan(t, p, "after SetRaw corrupt row")
	if err := p.DeleteRaw(metadataCacheKeyPrefix + "bk08"); err != nil {
		t.Fatal(err)
	}
	assertSummariesMatchScan(t, p, "after DeleteRaw")

	if err := p.Reset(); err != nil {
		t.Fatal(err)
	}
	assertSummariesMatchScan(t, p, "after Reset")
	putCache(t, p, "post", 2, base)
	assertSummariesMatchScan(t, p, "after Put following Reset")
}

// TestMetadataCacheSummaries_RandomWrites: random Put/Delete sequences,
// checked against a fresh scan after every step.
func TestMetadataCacheSummaries_RandomWrites(t *testing.T) {
	p := newSummaryStore(t)
	rng := rand.New(rand.NewPCG(11, 11))
	base := time.Now().UTC().Truncate(time.Second)
	for step := range 300 {
		id := fmt.Sprintf("r%02d", rng.IntN(40))
		if rng.IntN(3) == 0 {
			if err := p.DeleteMetadataCache(id); err != nil {
				t.Fatal(err)
			}
		} else {
			putCache(t, p, id, rng.IntN(6), base.Add(time.Duration(rng.IntN(20))*time.Second))
		}
		if step%7 == 0 {
			assertSummariesMatchScan(t, p, fmt.Sprintf("step %d", step))
		}
	}
	assertSummariesMatchScan(t, p, "end")
}

// TestMetadataCacheSummaries_WarmCallReadsOnlyChangedRows is the fast-path
// proof: once built, a call after one write does not touch the rest of the
// keyspace. A row changed underneath the store WITHOUT going through a writer
// (so without a generation bump) stays as the index last saw it -- which
// shows the call did not rescan.
func TestMetadataCacheSummaries_WarmCallReadsOnlyChangedRows(t *testing.T) {
	p := newSummaryStore(t)
	base := time.Now().UTC().Truncate(time.Second)
	putCache(t, p, "a", 1, base)
	putCache(t, p, "b", 1, base)
	if _, err := p.ListMetadataCacheKeys(); err != nil {
		t.Fatal(err)
	}
	// Rewrite "a" behind the store's back: no generation bump.
	data, _ := json.Marshal(&MetadataCandidateCache{BookID: "a", Candidates: make([]json.RawMessage, 0), FetchedAt: base})
	if err := p.db.Set(metadataCacheKey("a"), data, pebble.Sync); err != nil {
		t.Fatal(err)
	}
	putCache(t, p, "b", 5, base) // a real write, named by the change log
	got, err := p.ListMetadataCacheKeys()
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, s := range got {
		counts[s.BookID] = s.CandidateCount
	}
	if counts["b"] != 5 {
		t.Fatalf("changed row b not re-read: %v", counts)
	}
	if counts["a"] != 1 {
		t.Fatalf("unchanged row a was re-read (count %d): the warm call rescanned the keyspace", counts["a"])
	}
}

// BenchmarkListMetadataCacheKeys: 40k cached books with ten candidates each,
// cold (full scan) versus warm after one write.
func BenchmarkListMetadataCacheKeys(b *testing.B) {
	p := newSummaryStore(b)
	base := time.Now().UTC()
	cands := make([]json.RawMessage, 10)
	for i := range cands {
		cands[i] = json.RawMessage(fmt.Sprintf(`{"title":"c%d","description":%q}`, i, strings.Repeat("d", 1500)))
	}
	batch := p.db.NewBatch()
	for i := range 40_000 {
		data, _ := json.Marshal(&MetadataCandidateCache{BookID: fmt.Sprintf("b%06d", i), Candidates: cands, FetchedAt: base.Add(-time.Duration(i) * time.Second)})
		_ = batch.Set(metadataCacheKey(fmt.Sprintf("b%06d", i)), data, nil)
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		b.Fatal(err)
	}
	b.Run("cold_scan", func(b *testing.B) {
		for b.Loop() {
			p.cacheSummaries.mu.Lock()
			p.cacheSummaries.built = false
			p.cacheSummaries.mu.Unlock()
			if _, err := p.ListMetadataCacheKeys(); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("warm_after_one_write", func(b *testing.B) {
		i := 0
		for b.Loop() {
			putCache(b, p, fmt.Sprintf("b%06d", i%40_000), 3, base)
			i++
			if _, err := p.ListMetadataCacheKeys(); err != nil {
				b.Fatal(err)
			}
		}
	})
}
