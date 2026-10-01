// file: internal/catalog/harvest_test.go
// version: 1.0.0
// guid: 6a2f8c14-3b9d-4e70-a1c5-9d4e2b7f0c68
// last-edited: 2026-10-01

package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// fixtureServer serves the recorded Tchaikovsky listing, sliced by the
// request's 0-indexed page and num_results, and records every request.
type fixtureServer struct {
	srv       *httptest.Server
	products  []json.RawMessage
	total     int
	mu        sync.Mutex
	pages     []int
	failPage  int // -1 = never
	drop      map[string]bool
	productBy map[string]json.RawMessage
}

func newFixtureServer(t *testing.T) *fixtureServer {
	t.Helper()
	raw, err := os.ReadFile("testdata/audible_author_tchaikovsky.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Total    int               `json:"total_results"`
		Products []json.RawMessage `json:"products"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	f := &fixtureServer{products: doc.Products, total: doc.Total, failPage: -1, drop: map[string]bool{}, productBy: map[string]json.RawMessage{}}
	for _, p := range doc.Products {
		var id struct {
			ASIN string `json:"asin"`
		}
		_ = json.Unmarshal(p, &id)
		f.productBy[id.ASIN] = p
	}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fixtureServer) serve(w http.ResponseWriter, r *http.Request) {
	if asin, ok := cutPrefix(r.URL.Path, "/catalog/products/"); ok {
		p, found := f.productBy[asin]
		if !found {
			http.NotFound(w, r)
			return
		}
		_, _ = fmt.Fprintf(w, `{"product":%s}`, p)
		return
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	n, _ := strconv.Atoi(r.URL.Query().Get("num_results"))
	f.mu.Lock()
	f.pages = append(f.pages, page)
	fail := f.failPage == page
	var live []json.RawMessage
	for _, p := range f.products {
		var id struct {
			ASIN string `json:"asin"`
		}
		_ = json.Unmarshal(p, &id)
		if !f.drop[id.ASIN] {
			live = append(live, p)
		}
	}
	f.mu.Unlock()
	if fail {
		http.Error(w, "boom", http.StatusBadRequest)
		return
	}
	lo := min(page*n, len(live))
	hi := min(lo+n, len(live))
	out := struct {
		Products []json.RawMessage `json:"products"`
		Total    int               `json:"total_results"`
	}{Products: live[lo:hi], Total: len(live)}
	if out.Products == nil {
		out.Products = []json.RawMessage{}
	}
	_ = json.NewEncoder(w).Encode(out)
}

func cutPrefix(s, p string) (string, bool) {
	if len(s) >= len(p) && s[:len(p)] == p {
		return s[len(p):], true
	}
	return "", false
}

func (f *fixtureServer) requestedPages() []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int(nil), f.pages...)
}

func openCatalog(t *testing.T) (*database.CatalogStore, *pebble.DB) {
	t.Helper()
	db, err := pebble.Open(t.TempDir(), &pebble.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return database.NewCatalogStore(db), db
}

var tchaikovsky = ScopeAuthor{Key: HarvestKey("Adrian Tchaikovsky"), Name: "Adrian Tchaikovsky", OwnedASINs: []string{"B071Y9TTHC"}}

func newTestHarvester(f *fixtureServer, st *database.CatalogStore, pageSize int) *Harvester {
	return NewHarvester(metadata.NewAudibleClientWithBaseURL(f.srv.URL), st, Settings{
		Language: "english", PageSize: pageSize, Concurrency: 4, OpID: "op-test",
	})
}

func TestHarvest_PagingIsZeroIndexedAndStopsAtTotal(t *testing.T) {
	f := newFixtureServer(t)
	st, _ := openCatalog(t)
	h := newTestHarvester(f, st, 20)
	s, err := h.HarvestAuthor(context.Background(), tchaikovsky, nil)
	if err != nil {
		t.Fatal(err)
	}
	pages := f.requestedPages()
	// 86 products / 20 per page = pages 0..4; page 5 would be past total.
	if fmt.Sprint(pages) != "[0 1 2 3 4]" {
		t.Errorf("requested pages %v; want [0 1 2 3 4]", pages)
	}
	if s.State != database.CatalogHarvestComplete || s.Fetched != 86 || s.TotalResults != 86 || s.PagesDone != 5 || s.Capped {
		t.Errorf("state = %+v", s)
	}
	if len(s.AuthorASINs) != 1 || s.AuthorASINs[0] != "B002XLHS8Q" {
		t.Errorf("author ASINs = %v; want [B002XLHS8Q] (read off the owned book's product in the listing)", s.AuthorASINs)
	}
}

func TestHarvest_LanguageFilterNameOnlyAndGroups(t *testing.T) {
	f := newFixtureServer(t)
	st, _ := openCatalog(t)
	h := newTestHarvester(f, st, 50)
	s, err := h.HarvestAuthor(context.Background(), tchaikovsky, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, german := range []string{"3961540519", "3961541280"} {
		if _, err := st.GetEntryByProviderID("audible", "us", german); !errors.Is(err, database.ErrCatalogEntryNotFound) {
			t.Errorf("german product %s stored (err=%v)", german, err)
		}
	}
	// The Tiger and the Wolf credits the name with no author ASIN: kept, name_only.
	tiger, err := st.GetEntryByProviderID("audible", "us", "B071JW2X84")
	if err != nil {
		t.Fatal(err)
	}
	if !tiger.NameOnlyAuthor {
		t.Error("product crediting the name without an author ASIN not flagged name_only")
	}
	if s.NameOnly < 1 || s.Kept+s.Dropped != 86 {
		t.Errorf("counts kept=%d dropped=%d name_only=%d", s.Kept, s.Dropped, s.NameOnly)
	}
	// Two ASINs of Cage of Souls (different subtitles) share one edition group.
	a, _ := st.GetEntryByProviderID("audible", "us", "B0BTDSTWG9")
	b, _ := st.GetEntryByProviderID("audible", "us", "B0H1HQY88W")
	if a == nil || b == nil || a.EditionGroupID == "" || a.EditionGroupID != b.EditionGroupID {
		t.Fatalf("Cage of Souls editions not grouped: %+v / %+v", a, b)
	}
	members, _ := st.EditionGroupMembers(a.EditionGroupID)
	if len(members) != 2 {
		t.Errorf("group members = %v", members)
	}
	// Children of Time is unabridged with sequence "1".
	cot, _ := st.GetEntryByProviderID("audible", "us", "B071Y9TTHC")
	if cot == nil || cot.EditionKind != database.EditionUnabridged || len(cot.Series) != 1 || cot.Series[0].SeqLo == nil || *cot.Series[0].SeqLo != 1 {
		t.Errorf("Children of Time = %+v", cot)
	}
	raw, _ := st.GetRaw(cot.ID)
	if len(raw) == 0 {
		t.Error("raw payload not stored")
	}
	// The read index finds the author by name and by ASIN.
	byName, total, _ := st.ListEntries(database.CatalogListQuery{AuthorKey: AuthorNameKey("Adrian Tchaikovsky"), Limit: 500})
	byASIN, totalA, _ := st.ListEntries(database.CatalogListQuery{AuthorKey: AuthorASINKey("B002XLHS8Q"), Limit: 500})
	if total != s.Kept || len(byName) != s.Kept || totalA != s.Kept-s.NameOnly || len(byASIN) != totalA {
		t.Errorf("index: by name %d/%d, by asin %d/%d, kept %d name_only %d", len(byName), total, len(byASIN), totalA, s.Kept, s.NameOnly)
	}
}

func TestHarvest_IdempotentUpsert(t *testing.T) {
	f := newFixtureServer(t)
	st, _ := openCatalog(t)
	h := newTestHarvester(f, st, 50)
	s1, err := h.HarvestAuthor(context.Background(), tchaikovsky, nil)
	if err != nil {
		t.Fatal(err)
	}
	cot1, _ := st.GetEntryByProviderID("audible", "us", "B071Y9TTHC")
	n1, _ := st.CountEntries()
	s2, err := h.HarvestAuthor(context.Background(), tchaikovsky, nil)
	if err != nil {
		t.Fatal(err)
	}
	cot2, _ := st.GetEntryByProviderID("audible", "us", "B071Y9TTHC")
	n2, _ := st.CountEntries()
	if n1 != n2 || n1 != s1.Kept || s1.Kept != s2.Kept {
		t.Errorf("entry count %d -> %d (kept %d, %d)", n1, n2, s1.Kept, s2.Kept)
	}
	if cot1.ID != cot2.ID || !cot1.FirstSeenAt.Equal(cot2.FirstSeenAt) || cot1.EditionGroupID != cot2.EditionGroupID {
		t.Errorf("re-upsert changed identity: %+v -> %+v", cot1, cot2)
	}
	if stale, _ := st.CountStale(); stale != 0 {
		t.Errorf("stale after identical re-harvest = %d", stale)
	}
}

func TestHarvest_StaleOnlyAfterCompleteFetch(t *testing.T) {
	f := newFixtureServer(t)
	st, _ := openCatalog(t)
	h := newTestHarvester(f, st, 20)
	if _, err := h.HarvestAuthor(context.Background(), tchaikovsky, nil); err != nil {
		t.Fatal(err)
	}
	const gone = "B0FMLC4795" // Green City Wars
	f.mu.Lock()
	f.drop[gone] = true
	f.failPage = 2
	f.mu.Unlock()

	// Partial fetch (page 2 fails): nothing may go stale.
	s, err := h.HarvestAuthor(context.Background(), tchaikovsky, nil)
	if err != nil {
		t.Fatal(err)
	}
	if s.State != database.CatalogHarvestPartial || s.LastError == "" {
		t.Fatalf("state after failed page = %+v; want partial with an error", s)
	}
	if n, _ := st.CountStale(); n != 0 {
		t.Fatalf("partial fetch marked %d stale", n)
	}
	// The partial run makes no lookups, but must not downgrade entries the
	// complete run confirmed by author ASIN to name_only.
	if len(s.AuthorASINs) != 1 {
		t.Errorf("partial run lost the known author ASIN: %v", s.AuthorASINs)
	}
	for _, asin := range []string{"B0H444SXQB", "1529050278"} {
		if e, _ := st.GetEntryByProviderID("audible", "us", asin); e == nil || e.NameOnlyAuthor {
			t.Errorf("%s downgraded to name_only by a partial run: %+v", asin, e)
		}
	}

	// Complete fetch without the product: it goes stale, nothing else.
	f.mu.Lock()
	f.failPage = -1
	f.mu.Unlock()
	s, err = h.HarvestAuthor(context.Background(), tchaikovsky, nil)
	if err != nil {
		t.Fatal(err)
	}
	if s.State != database.CatalogHarvestComplete || s.MarkedStale != 1 {
		t.Fatalf("state = %+v; want complete with 1 marked stale", s)
	}
	e, _ := st.GetEntryByProviderID("audible", "us", gone)
	if e == nil || e.StaleSince == nil {
		t.Fatalf("dropped product not stale: %+v", e)
	}
	// It comes back: stale_since clears.
	f.mu.Lock()
	delete(f.drop, gone)
	f.mu.Unlock()
	if _, err := h.HarvestAuthor(context.Background(), tchaikovsky, nil); err != nil {
		t.Fatal(err)
	}
	e, _ = st.GetEntryByProviderID("audible", "us", gone)
	if e.StaleSince != nil {
		t.Error("returned product still stale")
	}
	if n, _ := st.CountStale(); n != 0 {
		t.Errorf("stale count %d after return", n)
	}
}

func TestHarvest_CapIsLoggedAndNeverStales(t *testing.T) {
	f := newFixtureServer(t)
	st, _ := openCatalog(t)
	h := newTestHarvester(f, st, 20)
	if _, err := h.HarvestAuthor(context.Background(), tchaikovsky, nil); err != nil {
		t.Fatal(err)
	}
	h.Cfg.MaxProducts = 40
	f.mu.Lock()
	f.pages = nil
	f.mu.Unlock()
	s, err := h.HarvestAuthor(context.Background(), tchaikovsky, nil)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(f.requestedPages()) != "[0 1]" {
		t.Errorf("capped run requested %v; want [0 1]", f.requestedPages())
	}
	if !s.Capped || s.State != database.CatalogHarvestComplete || s.Fetched != 40 {
		t.Errorf("state = %+v; want complete, capped, 40 fetched", s)
	}
	if n, _ := st.CountStale(); n != 0 {
		t.Errorf("capped run marked %d stale (the unlisted tail must not go stale)", n)
	}
}

func TestHarvest_StandDownWhenThrottled(t *testing.T) {
	f := newFixtureServer(t)
	st, _ := openCatalog(t)
	h := newTestHarvester(f, st, 20)
	h.Cfg.Throttled = func() bool { return true }
	s, err := h.HarvestAuthor(context.Background(), tchaikovsky, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.requestedPages()) != 0 {
		t.Error("requests made while throttled")
	}
	if s.State != database.CatalogHarvestFailed {
		t.Errorf("state = %s; want failed (no page fetched)", s.State)
	}
}

// fakeLister is an in-memory AuthorLister for the concurrency test.
type fakeLister struct {
	byAuthor map[string][]metadata.CatalogProduct
	calls    atomic.Int64
}

func (l *fakeLister) ProviderID() string { return "audible" }
func (l *fakeLister) ListByAuthor(_ context.Context, name string, page, size int) (metadata.AuthorPage, error) {
	l.calls.Add(1)
	time.Sleep(time.Millisecond) // let workers interleave
	all := l.byAuthor[name]
	lo := min(page*size, len(all))
	return metadata.AuthorPage{Products: all[lo:min(lo+size, len(all))], TotalResults: len(all)}, nil
}
func (l *fakeLister) LookupProduct(context.Context, string) (*metadata.CatalogProduct, error) {
	return nil, errors.New("not used")
}

type fakeReporter struct {
	mu          sync.Mutex
	checkpoints int
}

func (r *fakeReporter) UpdateProgress(int, int, string) error      { return nil }
func (r *fakeReporter) Log(slog.Level, string, ...slog.Attr) error { return nil }
func (r *fakeReporter) Logger() *slog.Logger                       { return slog.Default() }
func (r *fakeReporter) Checkpoint(any) error                       { r.mu.Lock(); r.checkpoints++; r.mu.Unlock(); return nil }
func (r *fakeReporter) IsCanceled() bool                           { return false }
func (r *fakeReporter) Trigger(context.Context, string, any) error { return nil }
func (r *fakeReporter) SetCurrentItem(string)                      {}
func (r *fakeReporter) RunPhase(ctx context.Context, _ string, fn func(context.Context, registry.Reporter) error) error {
	return fn(ctx, r)
}

// TestRun_ConcurrentCoAuthorsShareOneEntry drives a real concurrent run
// (Concurrency 4) in which every author lists the SAME co-authored product.
// Without one lock across pid lookup and commit, workers mint duplicate
// entries and groups; -race cannot see that, so the assertion is the count.
func TestRun_ConcurrentCoAuthorsShareOneEntry(t *testing.T) {
	st, _ := openCatalog(t)
	l := &fakeLister{byAuthor: map[string][]metadata.CatalogProduct{}}
	var authors []ScopeAuthor
	var credits []metadata.CatalogContributor
	for i := range 12 {
		credits = append(credits, metadata.CatalogContributor{Name: fmt.Sprintf("Author %c Person", 'A'+i), ASIN: fmt.Sprintf("AU%02d", i)})
	}
	shared := metadata.CatalogProduct{ASIN: "SHARED", Title: "Anthology", Language: "english", FormatType: "unabridged", Authors: credits}
	shared2 := metadata.CatalogProduct{ASIN: "SHARED2", Title: "Anthology (Unabridged)", Language: "english", FormatType: "unabridged", Authors: credits}
	for i, c := range credits {
		solo := metadata.CatalogProduct{ASIN: fmt.Sprintf("SOLO%02d", i), Title: fmt.Sprintf("Solo %d", i), Language: "english", Authors: []metadata.CatalogContributor{c}}
		l.byAuthor[c.Name] = []metadata.CatalogProduct{shared, solo, shared2}
		authors = append(authors, ScopeAuthor{Key: HarvestKey(c.Name), Name: c.Name})
	}
	h := NewHarvester(l, st, Settings{Language: "english", PageSize: 2, Concurrency: 4})
	rep := &fakeReporter{}
	tally, err := h.Run(context.Background(), rep, authors, RunOptions{Checkpoint: func() error { return rep.Checkpoint(nil) }, CheckpointEvery: time.Nanosecond})
	if err != nil {
		t.Fatal(err)
	}
	if tally.Complete.Load() != 12 || tally.Done.Load() != 12 {
		t.Fatalf("tally %s done=%d", tally.Summary(), tally.Done.Load())
	}
	n, _ := st.CountEntries()
	if n != 12+2 {
		t.Errorf("entries = %d; want 14 (12 solo + 2 shared, no duplicates)", n)
	}
	e, err := st.GetEntryByProviderID("audible", "us", "SHARED")
	if err != nil {
		t.Fatal(err)
	}
	if len(e.HarvestedBy) != 12 {
		t.Errorf("shared harvested_by = %d keys; want 12", len(e.HarvestedBy))
	}
	e2, _ := st.GetEntryByProviderID("audible", "us", "SHARED2")
	if e.EditionGroupID != e2.EditionGroupID {
		t.Error("the two editions of the shared title got different groups under concurrency")
	}
	if rep.checkpoints == 0 {
		t.Error("no checkpoint taken")
	}
	// One author's later complete listing without the shared product must
	// NOT stale it: the other eleven still list it.
	l.byAuthor[credits[0].Name] = l.byAuthor[credits[0].Name][1:2]
	if _, err := h.HarvestAuthor(context.Background(), authors[0], nil); err != nil {
		t.Fatal(err)
	}
	e, _ = st.GetEntryByProviderID("audible", "us", "SHARED")
	if e.StaleSince != nil || len(e.HarvestedBy) != 11 {
		t.Errorf("co-authored entry after one author dropped it: stale=%v harvested_by=%d", e.StaleSince, len(e.HarvestedBy))
	}
}

func TestSelectDue_PerAuthorResume(t *testing.T) {
	st, _ := openCatalog(t)
	now := time.Now().UTC()
	recent := now.Add(-time.Hour)
	for _, s := range []database.CatalogAuthorState{
		{Key: "done", State: database.CatalogHarvestComplete, LastCompleteAt: &recent},
		{Key: "partial", State: database.CatalogHarvestPartial, LastCompleteAt: &recent},
		{Key: "failed", State: database.CatalogHarvestFailed},
	} {
		if err := st.PutAuthorState(&s); err != nil {
			t.Fatal(err)
		}
	}
	h := NewHarvester(&fakeLister{}, st, Settings{})
	all := []ScopeAuthor{{Key: "done"}, {Key: "partial"}, {Key: "failed"}, {Key: "new"}}
	due, err := h.SelectDue(all, nil)
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, a := range due {
		keys = append(keys, a.Key)
	}
	if fmt.Sprint(keys) != "[partial failed new]" {
		t.Errorf("due = %v", keys)
	}
	due, _ = h.SelectDue(all, map[string]bool{"done": true})
	if len(due) != 4 {
		t.Errorf("forced author not due: %d", len(due))
	}
}

func TestEstimateRun_WritesNothing(t *testing.T) {
	f := newFixtureServer(t)
	st, db := openCatalog(t)
	h := newTestHarvester(f, st, 50)
	est := h.EstimateRun(context.Background(), ScopeCensus{Authors: 1, AuthorsWithASIN: 1}, []ScopeAuthor{tchaikovsky}, 5)
	if est.SampleAuthors != 1 || est.AvgTotalResults != 86 || est.AuthorsDue != 1 || est.DueWithASIN != 1 {
		t.Errorf("estimate = %+v", est)
	}
	if est.EstimatedRequests < 1 || est.EstimatedEntries < 1 || est.EstimatedBytes <= 0 || est.AvgKeptRatio <= 0 || est.AvgKeptRatio > 1 {
		t.Errorf("estimate = %+v", est)
	}
	for _, p := range database.CatalogKeyPrefixes() {
		it, err := db.NewIter(&pebble.IterOptions{LowerBound: []byte(p), UpperBound: []byte(p + "\xff")})
		if err != nil {
			t.Fatal(err)
		}
		if it.First() {
			t.Errorf("dry run wrote under %s: %q", p, it.Key())
		}
		_ = it.Close()
	}
}
