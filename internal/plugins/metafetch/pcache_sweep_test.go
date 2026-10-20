// file: internal/plugins/metafetch/pcache_sweep_test.go
// version: 1.0.2
// guid: 04ccc271-b21f-4dfa-b788-6ab9e21ca3ca
// last-edited: 2026-10-19

package metafetch

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// runPCacheSweep reaches the store through a runtime type assertion, so a
// production store that stopped satisfying pcacheSweepStore would only fail
// at run time. This keeps that a compile error. It lives in a test file so
// the wide database.Store reference stays out of the store-reference count.
var _ pcacheSweepStore = (database.Store)(nil)

// Synthetic fixtures only: "Title 000123", book ids "book-gone-01".

// sweepStore wraps the test store: GetBookByID fails for one id, and a scan
// can be made to return a stale value for one key (the row was rewritten
// fresh after the page was read).
type sweepStore struct {
	*database.PebbleStore
	failBook   string
	staleKey   string
	staleValue []byte
}

func (s *sweepStore) GetBookByID(id string) (*database.Book, error) {
	if id == s.failBook {
		return nil, errors.New("synthetic lookup failure")
	}
	return s.PebbleStore.GetBookByID(id)
}

func (s *sweepStore) ScanPrefixPage(prefix, after string, limit int) ([]database.KVPair, string, error) {
	pairs, next, err := s.PebbleStore.ScanPrefixPage(prefix, after, limit)
	for i := range pairs {
		if pairs[i].Key == s.staleKey {
			pairs[i].Value = s.staleValue
		}
	}
	return pairs, next, err
}

func putEntry(t *testing.T, s *database.PebbleStore, key, provider string, empty bool, askedAt time.Time) {
	t.Helper()
	e := database.PCacheEntry{Provider: provider, Endpoint: "search_title", AskedAt: askedAt.UTC(), Empty: empty,
		Results: json.RawMessage(`[{"title":"Title 000123"}]`)}
	if empty {
		e.Results = json.RawMessage(`[]`)
	}
	blob, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetRaw(key, blob); err != nil {
		t.Fatal(err)
	}
}

type sweepFixture struct {
	plugin *Plugin
	store  *database.PebbleStore
	wrap   *sweepStore
	keep   []string // rows the sweep must leave
	dead   []string // rows an apply deletes
}

func newSweepFixture(t *testing.T) *sweepFixture {
	t.Helper()
	withSharedProviderCache(t) // positive 180 days; Audible negative 14, Google 7
	_, s := newASINTestPlugin(t, &fakeAudible{})
	now := time.Now()
	day := 24 * time.Hour
	f := &sweepFixture{store: s, wrap: &sweepStore{PebbleStore: s}}
	f.plugin = &Plugin{store: f.wrap}

	keep := func(k, provider string, empty bool, age time.Duration) {
		putEntry(t, s, k, provider, empty, now.Add(-age))
		f.keep = append(f.keep, k)
	}
	dead := func(k, provider string, empty bool, age time.Duration) {
		putEntry(t, s, k, provider, empty, now.Add(-age))
		f.dead = append(f.dead, k)
	}
	keep("pcache:q:audible:search_title:fresh-positive", "audible", false, 179*day)
	dead("pcache:q:audible:search_title:old-positive", "audible", false, 181*day)
	keep("pcache:q:audible:search_title:fresh-empty", "audible", true, 13*day)
	dead("pcache:q:audible:search_title:old-empty", "audible", true, 15*day)
	keep("pcache:q:openlibrary:search_title:ol-empty-day-15", "openlibrary", true, 15*day)
	dead("pcache:q:google-books:search_title:google-empty-day-8", "google-books", true, 8*day)
	// Hardcover has no negative TTL: an empty row is never served.
	dead("pcache:q:hardcover:search_title:negative-off", "hardcover", true, time.Hour)
	keep("pcache:asin:audible:B0SYNTH001", "audible", false, 29*day)
	dead("pcache:asin:audible:B0SYNTH002", "audible", false, 31*day)
	if err := s.SetRaw("pcache:q:audible:search_title:corrupt", []byte("not json")); err != nil {
		t.Fatal(err)
	}
	f.dead = append(f.dead, "pcache:q:audible:search_title:corrupt")

	// Pointer rows: a live book's, a gone book's, and one whose lookup fails.
	aid := mkAuthor(t, s, "Author 07")
	live := mkBook(t, s, &database.Book{Title: "Title 000123", AuthorID: &aid, FilePath: "/synthetic/a.m4b"})
	for _, id := range []string{live.ID, "book-gone-01", "book-lookup-fails"} {
		if err := database.RecordPCachePointer(s, id, map[string][]string{"audible": {"pcache:q:audible:search_title:fresh-positive"}}, now); err != nil {
			t.Fatal(err)
		}
	}
	f.wrap.failBook = "book-lookup-fails"
	f.keep = append(f.keep, database.PCacheBookPrefix+live.ID, database.PCacheBookPrefix+"book-lookup-fails")
	f.dead = append(f.dead, database.PCacheBookPrefix+"book-gone-01")

	// Outside the family: never touched, expired or not.
	legacy, err := json.Marshal(database.CachedMetadataEntry{BookID: "book-gone-01", Source: "audible",
		Results: json.RawMessage(`[]`), CachedAt: now.Add(-400 * day), SearchIdentity: "v1:x"})
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string][]byte{
		"metadata_fetch_cache:book-gone-01:audible": legacy,
		"opchange:synthetic-01":                     []byte(`{}`),
	} {
		if err := s.SetRaw(k, v); err != nil {
			t.Fatal(err)
		}
		f.keep = append(f.keep, k)
	}
	return f
}

func (f *sweepFixture) present(t *testing.T, key string) bool {
	t.Helper()
	v, err := f.store.GetRaw(key)
	if err != nil {
		t.Fatal(err)
	}
	return v != nil
}

func runSweep(t *testing.T, p *Plugin, params string) pcacheSweepResult {
	t.Helper()
	rep := &asinTestReporter{}
	if err := p.runPCacheSweep(context.Background(), json.RawMessage(params), rep); err != nil {
		t.Fatalf("runPCacheSweep(%s): %v", params, err)
	}
	rep.mu.Lock()
	defer rep.mu.Unlock()
	res, ok := rep.result.(pcacheSweepResult)
	if !ok {
		t.Fatalf("result = %#v, want pcacheSweepResult", rep.result)
	}
	return res
}

func TestPCacheSweep_DefaultIsDryRunAndDeletesNothing(t *testing.T) {
	f := newSweepFixture(t)
	for _, params := range []string{``, `{}`, `{"dry_run":true}`} {
		res := runSweep(t, f.plugin, params)
		if !res.DryRun {
			t.Fatalf("params %q: not a dry run", params)
		}
		if res.Expired != 6 || res.OrphanPointers != 1 || res.PointerLookupErrors != 1 {
			t.Fatalf("params %q: expired=%d orphans=%d lookup_errors=%d, want 6, 1, 1 (%s)",
				params, res.Expired, res.OrphanPointers, res.PointerLookupErrors, res.summary())
		}
		if res.ExpiredDeleted != 0 || res.OrphanPointersDeleted != 0 {
			t.Fatalf("params %q: a dry run deleted rows: %s", params, res.summary())
		}
	}
	for _, k := range append(append([]string{}, f.keep...), f.dead...) {
		if !f.present(t, k) {
			t.Errorf("a dry run deleted %s", k)
		}
	}
}

func TestPCacheSweep_ApplyDeletesOnlyDeadRows(t *testing.T) {
	f := newSweepFixture(t)
	res := runSweep(t, f.plugin, `{"dry_run":false}`)
	if res.DryRun {
		t.Fatal("dry_run=false ran as a dry run")
	}
	if res.Questions != 8 || res.Products != 2 || res.Pointers != 3 {
		t.Errorf("scanned questions=%d products=%d pointers=%d, want 8, 2, 3", res.Questions, res.Products, res.Pointers)
	}
	if res.ExpiredDeleted != 6 || res.OrphanPointersDeleted != 1 || res.DeleteErrors != 0 {
		t.Errorf("deleted expired=%d orphans=%d errors=%d, want 6, 1, 0 (%s)",
			res.ExpiredDeleted, res.OrphanPointersDeleted, res.DeleteErrors, res.summary())
	}
	if res.ExpiredByReason[database.PCacheMissExpired] != 4 || res.ExpiredByReason[database.PCacheMissNegativeOff] != 1 ||
		res.ExpiredByReason[database.PCacheMissCorrupt] != 1 {
		t.Errorf("expired by reason = %v", res.ExpiredByReason)
	}
	for _, k := range f.dead {
		if f.present(t, k) {
			t.Errorf("dead row %s survived the apply", k)
		}
	}
	for _, k := range f.keep {
		if !f.present(t, k) {
			t.Errorf("the apply deleted %s, which it must keep", k)
		}
	}
	// The gone book's pointer row listed a live shared answer: only the
	// pointer row went.
	if !f.present(t, "pcache:q:audible:search_title:fresh-positive") {
		t.Error("deleting an orphan pointer row deleted the shared answer it listed")
	}

	again := runSweep(t, f.plugin, `{"dry_run":false}`)
	if again.Expired != 0 || again.OrphanPointers != 0 {
		t.Errorf("a second apply found more to delete: %s", again.summary())
	}
}

// A row rewritten with a fresh answer after its page was read is kept: the
// delete re-judges every row it is about to delete.
func TestPCacheSweep_RowRefreshedSinceTheScanIsKept(t *testing.T) {
	f := newSweepFixture(t)
	const k = "pcache:q:audible:search_title:fresh-positive"
	stale, err := json.Marshal(database.PCacheEntry{Provider: "audible", AskedAt: time.Now().Add(-400 * 24 * time.Hour),
		Results: json.RawMessage(`[{"title":"Title 000123"}]`)})
	if err != nil {
		t.Fatal(err)
	}
	f.wrap.staleKey, f.wrap.staleValue = k, stale
	res := runSweep(t, f.plugin, `{"dry_run":false}`)
	if res.ExpiredKept != 1 {
		t.Errorf("expired_kept_on_recheck = %d, want 1 (%s)", res.ExpiredKept, res.summary())
	}
	if !f.present(t, k) {
		t.Error("a row refreshed after the scan was deleted")
	}
}

// The sweep and the read path apply the same rule: every row the sweep calls
// dead is a row GetPCache would not serve, and every row it keeps is served.
func TestPCacheSweep_DeadMatchesTheReadPath(t *testing.T) {
	f := newSweepFixture(t)
	pairs, err := f.store.ScanPrefix(database.PCachePrefix)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for _, kv := range pairs {
		kind := database.PCacheKindOf(kv.Key)
		if kind == database.PCacheKindPointer {
			continue
		}
		provider, _, _ := strings.Cut(strings.TrimPrefix(strings.TrimPrefix(kv.Key, database.PCacheQuestionPrefix), database.PCacheASINPrefix), ":")
		e, _, err := database.GetPCache(f.store, kv.Key, metafetch.PCacheTTLFor(kind, provider), now)
		if err != nil {
			t.Fatal(err)
		}
		dead, _ := database.PCacheRowDead(kv.Key, kv.Value, metafetch.PCacheTTLFor, now)
		if dead == (e != nil) {
			t.Errorf("%s: sweep dead=%v but the read path serves=%v", kv.Key, dead, e != nil)
		}
	}
}

func TestPCacheSweep_DefIsAPreviewWriteOp(t *testing.T) {
	def := (&Plugin{}).pcacheSweepDef()
	if def.ID != pcacheSweepOpID || def.Run == nil {
		t.Fatalf("def = %+v", def)
	}
	writes := false
	for _, c := range def.Capabilities {
		writes = writes || c == sdk.CapLibraryWrite
	}
	if !writes {
		t.Fatalf("the sweep deletes rows and must declare library.write: %v", def.Capabilities)
	}
	found := false
	for _, d := range (&Plugin{}).OperationDefs() {
		if d.ID == pcacheSweepOpID {
			found = true
		}
	}
	if !found {
		t.Fatal("pcache sweep is not in OperationDefs")
	}
}
