// file: internal/catalog/harvest_test.go
// version: 1.7.2
// guid: 6a2f8c14-3b9d-4e70-a1c5-9d4e2b7f0c68
// last-edited: 2026-10-02

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
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/metadata/providerhttp"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// TestMain lifts the process-wide Audible token bucket (2 req/s built in)
// for the fixture server, which is local; the pacing under test is the
// harvest's own sub-limiter, not the provider bucket.
func TestMain(m *testing.M) {
	providerhttp.SetLimits(metadata.SourceIDAudible, providerhttp.Limits{RPS: 10000, Burst: 1000, MaxRetries: 0, Timeout: 10 * time.Second})
	providerhttp.ResetProvider(metadata.SourceIDAudible)
	os.Exit(m.Run())
}

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
	// lookupStatus forces a status for one product lookup (e.g. 503).
	lookupStatus map[string]int
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
	f := &fixtureServer{products: doc.Products, total: doc.Total, failPage: -1, drop: map[string]bool{}, productBy: map[string]json.RawMessage{}, lookupStatus: map[string]int{}}
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
		if code := f.lookupStatus[asin]; code != 0 {
			http.Error(w, "forced", code)
			return
		}
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
	// The owned book (Spiderlight) is product 55: past the 40 the partial
	// run below receives, so that run cannot re-read the author ASIN from it
	// and must fall back to the ASIN the complete run stored.
	tchaikovsky := tchaikovsky
	tchaikovsky.OwnedASINs = []string{"B0D4ZNFVBT"}
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

// TestUpsert_SameProviderIDTwiceInOneBatch: one call carrying the same pid
// twice must produce one entry (the second occurrence sees the first).
func TestUpsert_SameProviderIDTwiceInOneBatch(t *testing.T) {
	st, _ := openCatalog(t)
	e := BuildEntry(metadata.CatalogProduct{ASIN: "DUP", Title: "Twice", Authors: []metadata.CatalogContributor{{Name: "A B", ASIN: "AB"}}}, "audible", "us")
	res, err := st.UpsertEntries([]database.CatalogUpsert{{Entry: e}, {Entry: e}}, "ab")
	if err != nil {
		t.Fatal(err)
	}
	if res.IDs[0] != res.IDs[1] || res.Created != 1 || res.Updated != 1 {
		t.Errorf("result = %+v; want one id, 1 created + 1 updated", res)
	}
	if n, _ := st.CountEntries(); n != 1 {
		t.Errorf("entries = %d; want 1", n)
	}
}

// TestUpsert_GroupIDIsStable: a later upsert whose computed group key differs
// (the author ASIN appeared) keeps the group id assigned on first sight (R6).
func TestUpsert_GroupIDIsStable(t *testing.T) {
	st, _ := openCatalog(t)
	p := metadata.CatalogProduct{ASIN: "G1", Title: "Stable", Authors: []metadata.CatalogContributor{{Name: "A B"}}}
	r1, err := st.UpsertEntries([]database.CatalogUpsert{{Entry: BuildEntry(p, "audible", "us")}}, "ab")
	if err != nil {
		t.Fatal(err)
	}
	first, _ := st.GetEntry(r1.IDs[0])
	// The provider now credits the author under another spelling, so the
	// group key it computes differs: the stored group id must still win.
	p.Authors[0].Name = "A. B. Renamed"
	if _, err := st.UpsertEntries([]database.CatalogUpsert{{Entry: BuildEntry(p, "audible", "us")}}, "ab"); err != nil {
		t.Fatal(err)
	}
	again, _ := st.GetEntry(r1.IDs[0])
	if again.EditionGroupID != first.EditionGroupID {
		t.Errorf("group id changed on re-upsert: %s -> %s", first.EditionGroupID, again.EditionGroupID)
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

// TestHarvest_OwnedASINNotFoundIsNotALookupError: an owned ASIN the provider
// has no product for (a 404) is an answer, so the author ends complete. A
// 5xx on the same lookup is a failure: the author stays partial (retried)
// and the failure reaches the throttle registry hook.
func TestHarvest_OwnedASINNotFoundIsNotALookupError(t *testing.T) {
	f := newFixtureServer(t)
	st, _ := openCatalog(t)
	h := newTestHarvester(f, st, 50)
	var reported atomic.Int64
	h.Cfg.OnProviderError = func(error) { reported.Add(1) }
	a := tchaikovsky
	a.OwnedASINs = []string{"B071Y9TTHC", "BNOTREAL00"}
	s, err := h.HarvestAuthor(context.Background(), a, nil)
	if err != nil {
		t.Fatal(err)
	}
	if s.State != database.CatalogHarvestComplete || s.LastError != "" {
		t.Errorf("404 lookup: state=%s err=%q; want complete (not found is not a failure)", s.State, s.LastError)
	}
	if reported.Load() != 0 {
		t.Errorf("404 lookup reported %d provider errors; want 0", reported.Load())
	}

	f.lookupStatus["BNOTREAL00"] = http.StatusServiceUnavailable
	s, err = h.HarvestAuthor(context.Background(), a, nil)
	if err != nil {
		t.Fatal(err)
	}
	if s.State != database.CatalogHarvestPartial {
		t.Errorf("503 lookup: state=%s; want partial", s.State)
	}
	if reported.Load() != 1 {
		t.Errorf("503 lookup reported %d provider errors; want 1", reported.Load())
	}
}

// standDownLister fails every listing with a 429 and, like the shared
// throttle registry, holds the provider off from the first failure on.
type standDownLister struct {
	calls     atomic.Int64
	throttled atomic.Bool
}

func (l *standDownLister) ProviderID() string { return "audible" }
func (l *standDownLister) ListByAuthor(context.Context, string, int, int) (metadata.AuthorPage, error) {
	l.calls.Add(1)
	return metadata.AuthorPage{}, &metadata.ProviderStatusError{Provider: "audible", Status: http.StatusTooManyRequests}
}
func (l *standDownLister) LookupProduct(context.Context, string) (*metadata.CatalogProduct, error) {
	return nil, errors.New("not used")
}

// TestRun_StandsDownOnThrottle: a 429 is fed to the registry hook, and once
// the provider is held off the run stops handing out authors, so the rest
// keep no state (still due) instead of each being written failed.
func TestRun_StandsDownOnThrottle(t *testing.T) {
	st, _ := openCatalog(t)
	l := &standDownLister{}
	var authors []ScopeAuthor
	for i := range 10 {
		n := fmt.Sprintf("Author %c Person", 'A'+i)
		authors = append(authors, ScopeAuthor{Key: HarvestKey(n), Name: n})
	}
	h := NewHarvester(l, st, Settings{
		Language: "english", Concurrency: 1,
		Throttled:       l.throttled.Load,
		OnProviderError: func(error) { l.throttled.Store(true) },
	})
	tally, err := h.Run(context.Background(), &fakeReporter{}, authors, RunOptions{})
	if !errors.Is(err, ErrProviderStandDown) {
		t.Fatalf("Run err = %v; want ErrProviderStandDown", err)
	}
	if l.calls.Load() != 1 {
		t.Errorf("listing calls = %d; want 1 (stand down after the first 429)", l.calls.Load())
	}
	written := 0
	for _, a := range authors {
		if s, _ := st.GetAuthorState(a.Key); s != nil {
			written++
		}
	}
	if written != 1 || tally.Done.Load() != 1 {
		t.Errorf("author states written = %d, done = %d; want 1 and 1", written, tally.Done.Load())
	}
}

// TestUpsert_CoAuthorVerdictsMerge: a co-authored product judged confirmed
// by one author's harvest and name_only by the other's is confirmed, in
// either write order; the flags follow the verdicts when a harvester drops it.
func TestUpsert_CoAuthorVerdictsMerge(t *testing.T) {
	for _, order := range [][2]string{{"a", "b"}, {"b", "a"}} {
		st, _ := openCatalog(t)
		base := BuildEntry(metadata.CatalogProduct{ASIN: "KMF", Title: "The King Must Fall", Authors: []metadata.CatalogContributor{{Name: "A", ASIN: "AA"}, {Name: "B", ASIN: "BB"}}}, "audible", "us")
		verdict := map[string]database.CatalogEntry{}
		ea := base
		ea.NameOnlyAuthor = false // a has an owned ASIN-tagged book
		eb := base
		eb.NameOnlyAuthor, eb.AuthorConflict = true, true // b does not, and b's identity conflicts
		verdict["a"], verdict["b"] = ea, eb
		for _, k := range order {
			if _, err := st.UpsertEntries([]database.CatalogUpsert{{Entry: verdict[k]}}, k); err != nil {
				t.Fatal(err)
			}
		}
		e, err := st.GetEntryByProviderID("audible", "us", "KMF")
		if err != nil {
			t.Fatal(err)
		}
		if e.NameOnlyAuthor || !e.AuthorConflict {
			t.Errorf("order %v: name_only=%v conflict=%v; want false/true", order, e.NameOnlyAuthor, e.AuthorConflict)
		}
		// a's complete listing drops it: only b's verdict is left.
		if _, err := st.MarkUnseen("a", map[string]bool{}); err != nil {
			t.Fatal(err)
		}
		e, _ = st.GetEntryByProviderID("audible", "us", "KMF")
		if !e.NameOnlyAuthor || !e.AuthorConflict || e.StaleSince != nil {
			t.Errorf("order %v after a drops it: name_only=%v conflict=%v stale=%v; want true/true/nil", order, e.NameOnlyAuthor, e.AuthorConflict, e.StaleSince)
		}
		// b re-harvests without a conflict: the conflict clears.
		eb.AuthorConflict = false
		if _, err := st.UpsertEntries([]database.CatalogUpsert{{Entry: eb}}, "b"); err != nil {
			t.Fatal(err)
		}
		e, _ = st.GetEntryByProviderID("audible", "us", "KMF")
		if e.AuthorConflict {
			t.Errorf("order %v: conflict survived b's clean re-harvest", order)
		}
	}
}

// scriptLister is an AuthorLister whose listing is a function of the call,
// so a test can serve a truncated or inconsistent reply on a chosen page. It
// counts every request (listing pages and lookups). Lookups are not found.
type scriptLister struct {
	list func(name string, page, size int) metadata.AuthorPage
	// fail, when set and returning non-nil, fails that listing request.
	fail    func(name string, page int) error
	pages   atomic.Int64
	lookups atomic.Int64
}

func (l *scriptLister) ProviderID() string { return "audible" }
func (l *scriptLister) ListByAuthor(_ context.Context, name string, page, size int) (metadata.AuthorPage, error) {
	l.pages.Add(1)
	if l.fail != nil {
		if err := l.fail(name, page); err != nil {
			return metadata.AuthorPage{}, err
		}
	}
	return l.list(name, page, size), nil
}
func (l *scriptLister) LookupProduct(_ context.Context, asin string) (*metadata.CatalogProduct, error) {
	l.lookups.Add(1)
	return nil, fmt.Errorf("%s: %w", asin, metadata.ErrCatalogProductNotFound)
}

func sixProducts() []metadata.CatalogProduct {
	out := make([]metadata.CatalogProduct, 6)
	for i := range out {
		out[i] = metadata.CatalogProduct{ASIN: fmt.Sprintf("P%d", i), Title: fmt.Sprintf("Book %d", i), Language: "english",
			Authors: []metadata.CatalogContributor{{Name: "Ann Author"}}}
	}
	return out
}

func slicePage(all []metadata.CatalogProduct, page, size int) metadata.AuthorPage {
	lo := min(page*size, len(all))
	return metadata.AuthorPage{Products: all[lo:min(lo+size, len(all))], TotalResults: len(all)}
}

// harvestTwice harvests Ann Author once against the full six-product
// listing (complete, 6 kept), then again against second, and returns the
// second run's state.
func harvestTwice(t *testing.T, st *database.CatalogStore, second func(page, size int) metadata.AuthorPage) database.CatalogAuthorState {
	t.Helper()
	all := sixProducts()
	run := 0
	l := &scriptLister{list: func(_ string, page, size int) metadata.AuthorPage {
		if run == 0 {
			return slicePage(all, page, size)
		}
		return second(page, size)
	}}
	h := NewHarvester(l, st, Settings{Language: "english", PageSize: 2, Concurrency: 1, OpID: "op-test"})
	a := ScopeAuthor{Key: HarvestKey("Ann Author"), Name: "Ann Author"}
	s, err := h.HarvestAuthor(context.Background(), a, nil)
	if err != nil || s.State != database.CatalogHarvestComplete || s.Kept != 6 {
		t.Fatalf("first run = %+v, %v; want complete with 6 kept", s, err)
	}
	run = 1
	s, err = h.HarvestAuthor(context.Background(), a, nil)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// TestHarvest_EmptyPageBeforeTotalIsPartial: page 1 comes back empty while
// total_results still says 6. That is a truncated reply, not the end of the
// listing: the author is partial and nothing goes stale (R10).
func TestHarvest_EmptyPageBeforeTotalIsPartial(t *testing.T) {
	st, _ := openCatalog(t)
	all := sixProducts()
	s := harvestTwice(t, st, func(page, size int) metadata.AuthorPage {
		if page >= 1 {
			return metadata.AuthorPage{Products: []metadata.CatalogProduct{}, TotalResults: 6}
		}
		return slicePage(all, page, size)
	})
	if s.State != database.CatalogHarvestPartial || s.MarkedStale != 0 || s.LastError == "" {
		t.Errorf("state = %+v; want partial, 0 marked stale, an error recorded", s)
	}
	if n, _ := st.CountStale(); n != 0 {
		t.Errorf("truncated listing marked %d entries stale", n)
	}
}

// TestHarvest_ZeroTotalForKnownAuthorIsPartial: a transient "0 results"
// reply for an author whose last harvest kept entries must not stale them.
func TestHarvest_ZeroTotalForKnownAuthorIsPartial(t *testing.T) {
	st, _ := openCatalog(t)
	s := harvestTwice(t, st, func(int, int) metadata.AuthorPage {
		return metadata.AuthorPage{Products: []metadata.CatalogProduct{}, TotalResults: 0}
	})
	if s.State != database.CatalogHarvestPartial || s.MarkedStale != 0 {
		t.Errorf("state = %+v; want partial with nothing marked stale", s)
	}
	if n, _ := st.CountStale(); n != 0 {
		t.Errorf("zero-total reply marked %d entries stale", n)
	}
}

// TestHarvest_ZeroTotalForNewAuthorIsComplete: an author with nothing kept
// before and no products now is a real, complete, empty answer.
func TestHarvest_ZeroTotalForNewAuthorIsComplete(t *testing.T) {
	st, _ := openCatalog(t)
	l := &scriptLister{list: func(string, int, int) metadata.AuthorPage {
		return metadata.AuthorPage{Products: []metadata.CatalogProduct{}}
	}}
	h := NewHarvester(l, st, Settings{Language: "english", PageSize: 2, Concurrency: 1})
	s, err := h.HarvestAuthor(context.Background(), ScopeAuthor{Key: HarvestKey("Nobody"), Name: "Nobody"}, nil)
	if err != nil || s.State != database.CatalogHarvestComplete {
		t.Errorf("state = %+v, %v; want complete", s, err)
	}
}

// TestHarvest_SkippedProductCountsTowardTotalButStalesNothing: an
// undecodable product the lister skipped still counts toward total_results,
// so the author completes; but since its id is unknown, the stale pass is
// skipped rather than staling a stored entry it may be.
func TestHarvest_SkippedProductCountsTowardTotalButStalesNothing(t *testing.T) {
	st, _ := openCatalog(t)
	all := sixProducts()
	s := harvestTwice(t, st, func(page, size int) metadata.AuthorPage {
		pg := slicePage(all, page, size)
		if page == 2 { // P5 arrives malformed
			pg.Products, pg.Skipped = pg.Products[:1], 1
		}
		return pg
	})
	if s.State != database.CatalogHarvestComplete || s.Fetched != 6 || s.Skipped != 1 || s.MarkedStale != 0 {
		t.Errorf("state = %+v; want complete, 6 fetched, 1 skipped, 0 stale", s)
	}
	if e, _ := st.GetEntryByProviderID("audible", "us", "P5"); e == nil || e.StaleSince != nil {
		t.Errorf("the skipped product's stored entry went stale: %+v", e)
	}
}

// TestEstimateRun_CoversPagesAndOwnedLookups: for a fully sampled scope the
// request estimate is not below what the run then makes (it is an estimate,
// not a bound, once authors go unsampled). Pages are summed per author as ceilings (1
// and 3 products at 2 per page = 1 + 2 pages, not 2 x ceil(2/2)), and every
// owned ASIN the listing does not return costs one lookup.
func TestEstimateRun_CoversPagesAndOwnedLookups(t *testing.T) {
	st, _ := openCatalog(t)
	mk := func(name string, n int) []metadata.CatalogProduct {
		out := make([]metadata.CatalogProduct, n)
		for i := range out {
			out[i] = metadata.CatalogProduct{ASIN: fmt.Sprintf("%s%d", name, i), Title: fmt.Sprintf("%s %d", name, i), Language: "english",
				Authors: []metadata.CatalogContributor{{Name: name}}}
		}
		return out
	}
	by := map[string][]metadata.CatalogProduct{"Ay": mk("Ay", 1), "Bee": mk("Bee", 3)}
	l := &scriptLister{list: func(name string, page, size int) metadata.AuthorPage { return slicePage(by[name], page, size) }}
	h := NewHarvester(l, st, Settings{Language: "english", PageSize: 2, Concurrency: 1})
	due := []ScopeAuthor{
		{Key: HarvestKey("Ay"), Name: "Ay"},
		{Key: HarvestKey("Bee"), Name: "Bee", OwnedASINs: []string{"X1", "X2", "X3"}},
	}
	est := h.EstimateRun(context.Background(), ScopeCensus{}, due, 2)
	l.pages.Store(0)
	l.lookups.Store(0)
	if _, err := h.Run(context.Background(), &fakeReporter{}, due, RunOptions{}); err != nil {
		t.Fatal(err)
	}
	actual := int(l.pages.Load() + l.lookups.Load())
	if actual != 6 {
		t.Fatalf("run made %d requests; the fixture expects 6 (3 pages + 3 lookups)", actual)
	}
	if est.EstimatedRequests < actual {
		t.Errorf("estimate %d < actual %d (%+v)", est.EstimatedRequests, actual, est)
	}
}

// TestMarkUnseen_LastHarvesterDropsItsVerdict: when the only harvester that
// confirmed an entry stops listing it, the entry goes stale AND loses that
// confirmation; a stale entry must not keep reading "confirmed by ASIN".
func TestMarkUnseen_LastHarvesterDropsItsVerdict(t *testing.T) {
	st, _ := openCatalog(t)
	e := BuildEntry(metadata.CatalogProduct{ASIN: "Z1", Title: "Zed", Authors: []metadata.CatalogContributor{{Name: "A", ASIN: "AA"}}}, "audible", "us")
	e.NameOnlyAuthor, e.AuthorConflict = false, true
	if _, err := st.UpsertEntries([]database.CatalogUpsert{{Entry: e}}, "a"); err != nil {
		t.Fatal(err)
	}
	if n, err := st.MarkUnseen("a", map[string]bool{}); err != nil || n != 1 {
		t.Fatalf("MarkUnseen = %d, %v; want 1 stale", n, err)
	}
	got, _ := st.GetEntryByProviderID("audible", "us", "Z1")
	if got.StaleSince == nil || len(got.HarvestedBy) != 0 || len(got.ConfirmedBy) != 0 || len(got.ConflictBy) != 0 ||
		!got.NameOnlyAuthor || got.AuthorConflict {
		t.Errorf("stale entry kept a dropped harvester's verdict: %+v", got)
	}
}

// harvestN harvests Ann Author once against the full six-product listing
// (complete, 6 kept), then n more times against next, returning each later
// run's state.
func harvestN(t *testing.T, st *database.CatalogStore, n int, next func(page, size int) metadata.AuthorPage) ([]database.CatalogAuthorState, *scriptLister) {
	t.Helper()
	all := sixProducts()
	run := 0
	l := &scriptLister{list: func(_ string, page, size int) metadata.AuthorPage {
		if run == 0 {
			return slicePage(all, page, size)
		}
		return next(page, size)
	}}
	h := NewHarvester(l, st, Settings{Language: "english", PageSize: 2, Concurrency: 1, OpID: "op-test"})
	a := ScopeAuthor{Key: HarvestKey("Ann Author"), Name: "Ann Author"}
	if s, err := h.HarvestAuthor(context.Background(), a, nil); err != nil || s.State != database.CatalogHarvestComplete {
		t.Fatalf("first run = %+v, %v", s, err)
	}
	var out []database.CatalogAuthorState
	for run = 1; run <= n; run++ {
		s, err := h.HarvestAuthor(context.Background(), a, nil)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return out, l
}

// TestHarvest_DuplicatesAcrossPagesStaleNothing: the listing repeats P1 and
// omits P4 while still reaching total_results 6. The author is complete
// (never partial forever) but P4 must not go stale on that evidence.
func TestHarvest_DuplicatesAcrossPagesStaleNothing(t *testing.T) {
	st, _ := openCatalog(t)
	all := sixProducts()
	walk := []metadata.CatalogProduct{all[0], all[1], all[1], all[2], all[3], all[5]}
	states, _ := harvestN(t, st, 1, func(page, size int) metadata.AuthorPage { return slicePage(walk, page, size) })
	s := states[0]
	if s.State != database.CatalogHarvestComplete || s.Duplicates != 1 || s.MarkedStale != 0 {
		t.Errorf("state = %+v; want complete, 1 duplicate, 0 stale", s)
	}
	if e, _ := st.GetEntryByProviderID("audible", "us", "P4"); e == nil || e.StaleSince != nil {
		t.Errorf("omitted product went stale on a walk with duplicates: %+v", e)
	}
}

// TestHarvest_PersistentShortListingIsAcceptedAfterThreeRuns: total 6, only
// 4 ever delivered. Runs 1-2 are partial (retried); run 3 agrees with them
// and is accepted as complete WITHOUT a stale pass, so the author stops being
// re-walked every run. A run that delivers a different count resets the run
// count.
func TestHarvest_PersistentShortListingIsAcceptedAfterThreeRuns(t *testing.T) {
	st, _ := openCatalog(t)
	all := sixProducts()
	states, l := harvestN(t, st, 4, func(page, size int) metadata.AuthorPage {
		pg := slicePage(all[:4], page, size)
		pg.TotalResults = 6
		return pg
	})
	want := []string{database.CatalogHarvestPartial, database.CatalogHarvestPartial, database.CatalogHarvestComplete, database.CatalogHarvestComplete}
	for i, s := range states {
		if s.State != want[i] || s.MarkedStale != 0 || s.ShortRuns != i+1 {
			t.Errorf("run %d: state=%s short_runs=%d stale=%d; want %s, %d, 0", i+1, s.State, s.ShortRuns, s.MarkedStale, want[i], i+1)
		}
	}
	if n, _ := st.CountStale(); n != 0 {
		t.Errorf("accepted short listing marked %d stale", n)
	}
	h := NewHarvester(l, st, Settings{})
	if due, _ := h.SelectDue([]ScopeAuthor{{Key: HarvestKey("Ann Author"), Name: "Ann Author"}}, nil); len(due) != 0 {
		t.Error("an accepted short listing is still due every run")
	}

	// A different delivered count is a different answer: the count restarts.
	st2, _ := openCatalog(t)
	counts := []int{4, 5, 4}
	i := 0
	states, _ = harvestN(t, st2, 3, func(page, size int) metadata.AuthorPage {
		pg := slicePage(all[:counts[i]], page, size)
		pg.TotalResults = 6
		if len(pg.Products) == 0 {
			i = min(i+1, len(counts)-1)
		}
		return pg
	})
	for j, s := range states {
		if s.State != database.CatalogHarvestPartial || s.ShortRuns != 1 {
			t.Errorf("varying run %d: state=%s short_runs=%d; want partial, 1", j+1, s.State, s.ShortRuns)
		}
	}
}

// clock is a settable test clock.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) Add(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

// annRuns harvests Ann Author once against the full six-product listing,
// then once per entry of advance (moving the clock by it first) against
// next, returning each later run's state.
func annRuns(t *testing.T, st *database.CatalogStore, l *scriptLister, c *clock, advance []time.Duration) []database.CatalogAuthorState {
	t.Helper()
	h := NewHarvester(l, st, Settings{Language: "english", PageSize: 2, Concurrency: 1, OpID: "op-test", Now: c.Now})
	a := ScopeAuthor{Key: HarvestKey("Ann Author"), Name: "Ann Author"}
	var out []database.CatalogAuthorState
	for i := -1; i < len(advance); i++ {
		if i >= 0 {
			c.Add(advance[i])
		}
		s, err := h.HarvestAuthor(context.Background(), a, nil)
		if err != nil {
			t.Fatal(err)
		}
		if i >= 0 {
			out = append(out, s)
		}
	}
	return out
}

// phasedLister serves the full six-product listing on the first harvest and
// then whatever next returns.
func phasedLister(next func(page, size int) metadata.AuthorPage) (*scriptLister, *atomic.Int64) {
	all := sixProducts()
	var calls atomic.Int64
	l := &scriptLister{list: func(_ string, page, size int) metadata.AuthorPage {
		if calls.Add(1) <= 3 { // the first walk is pages 0..2
			return slicePage(all, page, size)
		}
		return next(page, size)
	}}
	return l, &calls
}

// TestHarvest_ZeroTotalNeedsTheWindowNotJustRuns: three "0 results" replies
// inside an hour (an outage, or a decode regression reading total as 0) must
// NOT stale anything. Acceptance needs the agreeing runs to span
// ZeroTotalAcceptWindow; once they do, the stale pass runs.
func TestHarvest_ZeroTotalNeedsTheWindowNotJustRuns(t *testing.T) {
	st, _ := openCatalog(t)
	l, _ := phasedLister(func(int, int) metadata.AuthorPage { return metadata.AuthorPage{Products: []metadata.CatalogProduct{}} })
	c := &clock{t: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)}
	states := annRuns(t, st, l, c, []time.Duration{time.Minute, 20 * time.Minute, 20 * time.Minute, 4 * 24 * time.Hour, 4 * 24 * time.Hour})
	for i, s := range states[:4] {
		if s.State != database.CatalogHarvestPartial || s.MarkedStale != 0 {
			t.Errorf("zero run %d (within the window) = %s, stale %d; want partial, 0", i+1, s.State, s.MarkedStale)
		}
	}
	if s := states[4]; s.State != database.CatalogHarvestComplete || s.MarkedStale != 6 {
		t.Errorf("zero run 5 (window spanned) = %s, stale %d; want complete, 6", s.State, s.MarkedStale)
	}
}

// TestHarvest_ShortRunsRequireSameTotal: the same delivered count under a
// different total_results is a different answer and restarts the count.
func TestHarvest_ShortRunsRequireSameTotal(t *testing.T) {
	st, _ := openCatalog(t)
	all := sixProducts()
	totals := []int{6, 7, 6}
	run := 0
	l, _ := phasedLister(func(page, size int) metadata.AuthorPage {
		pg := slicePage(all[:4], page, size)
		pg.TotalResults = totals[run]
		if len(pg.Products) == 0 {
			run = min(run+1, len(totals)-1)
		}
		return pg
	})
	c := &clock{t: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)}
	for i, s := range annRuns(t, st, l, c, []time.Duration{time.Hour, time.Hour, time.Hour}) {
		if s.ShortRuns != 1 || s.State != database.CatalogHarvestPartial {
			t.Errorf("run %d: short_runs=%d state=%s; want 1, partial (total changed)", i+1, s.ShortRuns, s.State)
		}
	}
}

// TestHarvest_ErrorRunResetsShortRuns: short, provider error, short -- the
// error breaks the streak, so the third run starts again at 1.
func TestHarvest_ErrorRunResetsShortRuns(t *testing.T) {
	st, _ := openCatalog(t)
	all := sixProducts()
	l, _ := phasedLister(func(page, size int) metadata.AuthorPage {
		pg := slicePage(all[:4], page, size)
		pg.TotalResults = 6
		return pg
	})
	var failing atomic.Bool
	l.fail = func(string, int) error {
		if failing.Load() {
			return errors.New("provider 500")
		}
		return nil
	}
	h := NewHarvester(l, st, Settings{Language: "english", PageSize: 2, Concurrency: 1})
	a := ScopeAuthor{Key: HarvestKey("Ann Author"), Name: "Ann Author"}
	var got []int
	for i := 0; i < 4; i++ {
		failing.Store(i == 2)
		s, err := h.HarvestAuthor(context.Background(), a, nil)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, s.ShortRuns)
	}
	if fmt.Sprint(got) != "[0 1 0 1]" {
		t.Errorf("short_runs per run = %v; want [0 1 0 1] (full, short, error resets, short)", got)
	}
}

// breakerAuthors returns n authors, each listing two products of its own.
func breakerAuthors(n int) ([]ScopeAuthor, map[string][]metadata.CatalogProduct) {
	var authors []ScopeAuthor
	by := map[string][]metadata.CatalogProduct{}
	for i := range n {
		name := fmt.Sprintf("Author %c", 'A'+i)
		authors = append(authors, ScopeAuthor{Key: HarvestKey(name), Name: name})
		for j := range 2 {
			by[name] = append(by[name], metadata.CatalogProduct{ASIN: fmt.Sprintf("%c%d", 'A'+i, j), Title: fmt.Sprintf("%s %d", name, j),
				Language: "english", Authors: []metadata.CatalogContributor{{Name: name}}})
		}
	}
	return authors, by
}

// TestRun_ZeroTotalBreakerTreatsMassZeroAsOutage: when most of a run's
// authors answer "0 results", that is the provider failing, not every catalog
// vanishing at once. The zero replies must not count toward the acceptance
// streak (nor finish a new author as complete), and the run reports it.
func TestRun_ZeroTotalBreakerTreatsMassZeroAsOutage(t *testing.T) {
	st, _ := openCatalog(t)
	authors, by := breakerAuthors(6)
	var zero atomic.Bool
	l := &scriptLister{list: func(name string, page, size int) metadata.AuthorPage {
		if zero.Load() {
			return metadata.AuthorPage{Products: []metadata.CatalogProduct{}}
		}
		return slicePage(by[name], page, size)
	}}
	h := NewHarvester(l, st, Settings{Language: "english", PageSize: 2, Concurrency: 3})
	// A seventh author has never been harvested: under the outage it must
	// not be recorded complete-and-empty for a whole reharvest interval.
	fresh := ScopeAuthor{Key: HarvestKey("Fresh Author"), Name: "Fresh Author"}
	if _, err := h.Run(context.Background(), &fakeReporter{}, authors, RunOptions{}); err != nil {
		t.Fatal(err)
	}
	zero.Store(true)
	tally, err := h.Run(context.Background(), &fakeReporter{}, append(slices.Clone(authors), fresh), RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range append(slices.Clone(authors), fresh) {
		// Filed authors keep their pre-run streak (0). The never-harvested
		// one counts its held empty run (1): only the window, which an
		// outage cannot fake, can complete it.
		want := 0
		if a.Key == fresh.Key {
			want = 1
		}
		s, _ := st.GetAuthorState(a.Key)
		if s == nil || s.State != database.CatalogHarvestPartial || s.ShortRuns != want || s.MarkedStale != 0 {
			t.Errorf("%s under a mass zero reply: %+v; want partial, short_runs %d, nothing stale", a.Name, s, want)
		}
	}
	if n, _ := st.CountStale(); n != 0 {
		t.Errorf("mass zero reply staled %d entries", n)
	}
	if !tally.ZeroBreakerTripped.Load() || tally.ZeroTotal.Load() != 7 {
		t.Errorf("tally zero_total=%d tripped=%v; want 7, true", tally.ZeroTotal.Load(), tally.ZeroBreakerTripped.Load())
	}

	// A run too small to judge is not a breaker: a 2-author run counts.
	st2, _ := openCatalog(t)
	h2 := NewHarvester(l, st2, Settings{Language: "english", PageSize: 2, Concurrency: 2})
	zero.Store(false)
	if _, err := h2.Run(context.Background(), &fakeReporter{}, authors[:2], RunOptions{}); err != nil {
		t.Fatal(err)
	}
	zero.Store(true)
	if _, err := h2.Run(context.Background(), &fakeReporter{}, authors[:2], RunOptions{}); err != nil {
		t.Fatal(err)
	}
	if s, _ := st2.GetAuthorState(authors[0].Key); s == nil || s.ShortRuns != 1 {
		t.Errorf("2-author zero run: %+v; want short_runs 1 (too small to judge)", s)
	}
}

// TestRun_AcceptedZeroTotalStalesAfterTheRun: inside Run an accepted zero
// total defers its stale pass until the breaker has seen the whole run; with
// one zero author among six the breaker stays closed and the pass runs.
func TestRun_AcceptedZeroTotalStalesAfterTheRun(t *testing.T) {
	st, _ := openCatalog(t)
	authors, by := breakerAuthors(6)
	var gone atomic.Bool
	l := &scriptLister{list: func(name string, page, size int) metadata.AuthorPage {
		if gone.Load() && name == authors[0].Name {
			return metadata.AuthorPage{Products: []metadata.CatalogProduct{}}
		}
		return slicePage(by[name], page, size)
	}}
	c := &clock{t: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)}
	h := NewHarvester(l, st, Settings{Language: "english", PageSize: 2, Concurrency: 3, Now: c.Now})
	if _, err := h.Run(context.Background(), &fakeReporter{}, authors, RunOptions{}); err != nil {
		t.Fatal(err)
	}
	gone.Store(true)
	for _, d := range []time.Duration{time.Hour, 4 * 24 * time.Hour, 4 * 24 * time.Hour} {
		c.Add(d)
		if _, err := h.Run(context.Background(), &fakeReporter{}, authors, RunOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	s, _ := st.GetAuthorState(authors[0].Key)
	if s == nil || s.State != database.CatalogHarvestComplete || s.MarkedStale != 2 {
		t.Errorf("gone author after the window: %+v; want complete, 2 marked stale", s)
	}
	if n, _ := st.CountStale(); n != 2 {
		t.Errorf("stale count %d; want 2 (only the gone author's entries)", n)
	}
}

// TestRun_TallyReportsDuplicatedAuthors: a walk with duplicate ASINs never
// stales, so the run must at least say how many authors that applied to.
func TestRun_TallyReportsDuplicatedAuthors(t *testing.T) {
	st, _ := openCatalog(t)
	all := sixProducts()
	walk := []metadata.CatalogProduct{all[0], all[1], all[1], all[2], all[3], all[5]}
	l := &scriptLister{list: func(_ string, page, size int) metadata.AuthorPage { return slicePage(walk, page, size) }}
	h := NewHarvester(l, st, Settings{Language: "english", PageSize: 2, Concurrency: 1})
	tally, err := h.Run(context.Background(), &fakeReporter{}, []ScopeAuthor{{Key: HarvestKey("Ann Author"), Name: "Ann Author"}}, RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if tally.Duplicated.Load() != 1 || !strings.Contains(tally.Summary(), "duplicated=1") {
		t.Errorf("tally = %s; want duplicated=1", tally.Summary())
	}
}

// TestRun_ZeroTotalUnsettledIsRetriedNotComplete: a worker inside Run must
// not persist an accepted zero total (or an advanced streak) before the
// breaker settles. If the process dies between the worker and settleZero,
// the stored state must be the pre-run one marked partial, so the author is
// simply retried.
func TestRun_ZeroTotalUnsettledIsRetriedNotComplete(t *testing.T) {
	st, _ := openCatalog(t)
	l, _ := phasedLister(func(int, int) metadata.AuthorPage { return metadata.AuthorPage{Products: []metadata.CatalogProduct{}} })
	c := &clock{t: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)}
	annRuns(t, st, l, c, []time.Duration{time.Minute, 4 * 24 * time.Hour})
	a := ScopeAuthor{Key: HarvestKey("Ann Author"), Name: "Ann Author"}
	before, _ := st.GetAuthorState(a.Key)
	c.Add(4 * 24 * time.Hour) // the third zero run now spans the window
	h := NewHarvester(l, st, Settings{Language: "english", PageSize: 2, Concurrency: 1, Now: c.Now})
	if _, err := h.harvestAuthor(context.Background(), a, nil, newZeroLedger()); err != nil {
		t.Fatal(err)
	}
	// No settleZero: the "crash".
	got, _ := st.GetAuthorState(a.Key)
	if got.State != database.CatalogHarvestPartial || got.ShortRuns != before.ShortRuns ||
		got.LastCompleteAt == nil || !got.LastCompleteAt.Equal(*before.LastCompleteAt) {
		t.Errorf("unsettled zero author persisted as %s short_runs=%d last_complete=%v; want partial, %d, %v",
			got.State, got.ShortRuns, got.LastCompleteAt, before.ShortRuns, before.LastCompleteAt)
	}
	if n, _ := st.CountStale(); n != 0 {
		t.Errorf("unsettled run staled %d", n)
	}
}

// TestRun_BreakerRestoreKeepsTruncatedStreak: a tripped breaker must put back
// the WHOLE pre-run state. Author A is two runs into a truncated streak
// (total 3, 2 delivered); an outage run in between must not reset it, so the
// next truncated run is the third and is accepted.
func TestRun_BreakerRestoreKeepsTruncatedStreak(t *testing.T) {
	st, _ := openCatalog(t)
	authors, by := breakerAuthors(6)
	var phase atomic.Int32 // 0 full, 1 A truncated, 2 everyone zero
	l := &scriptLister{list: func(name string, page, size int) metadata.AuthorPage {
		switch {
		case phase.Load() == 2:
			return metadata.AuthorPage{Products: []metadata.CatalogProduct{}}
		case phase.Load() == 1 && name == authors[0].Name:
			pg := slicePage(by[name], page, size)
			pg.TotalResults = 3
			return pg
		}
		return slicePage(by[name], page, size)
	}}
	h := NewHarvester(l, st, Settings{Language: "english", PageSize: 2, Concurrency: 3})
	for _, p := range []int32{0, 1, 1, 2, 1} {
		phase.Store(p)
		if _, err := h.Run(context.Background(), &fakeReporter{}, authors, RunOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	s, _ := st.GetAuthorState(authors[0].Key)
	if s == nil || s.ShortRuns != 3 || s.State != database.CatalogHarvestComplete {
		t.Errorf("author A after short, short, outage, short: %+v; want short_runs 3, complete", s)
	}
}

// TestRun_NeverHarvestedZeroAuthorsDoNotTripBreaker: the breaker judges only
// authors with entries on file. A run of mostly never-harvested authors with
// no catalog answers 0 legitimately; they complete (empty) and are not due
// again next run.
func TestRun_NeverHarvestedZeroAuthorsDoNotTripBreaker(t *testing.T) {
	st, _ := openCatalog(t)
	// Six filed authors (enough to judge) answer normally; seven
	// never-harvested ones answer 0. Counting the unfiled zeros would make
	// it 7 of 13 and trip the breaker.
	authors, by := breakerAuthors(6)
	var nobodies []ScopeAuthor
	for i := range 7 {
		n := fmt.Sprintf("Nobody %d", i)
		nobodies = append(nobodies, ScopeAuthor{Key: HarvestKey(n), Name: n})
	}
	l := &scriptLister{list: func(name string, page, size int) metadata.AuthorPage {
		return slicePage(by[name], page, size) // nobodies have no products
	}}
	h := NewHarvester(l, st, Settings{Language: "english", PageSize: 2, Concurrency: 3})
	if _, err := h.Run(context.Background(), &fakeReporter{}, authors, RunOptions{}); err != nil {
		t.Fatal(err)
	}
	all := append(slices.Clone(authors), nobodies...)
	tally, err := h.Run(context.Background(), &fakeReporter{}, all, RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if tally.ZeroBreakerTripped.Load() {
		t.Error("breaker tripped on never-harvested authors with no catalog")
	}
	for _, a := range nobodies {
		if s, _ := st.GetAuthorState(a.Key); s == nil || s.State != database.CatalogHarvestComplete {
			t.Errorf("%s: %+v; want complete", a.Name, s)
		}
	}
	if due, _ := h.SelectDue(nobodies, nil); len(due) != 0 {
		t.Errorf("%d never-harvested zero authors still due after a clean run", len(due))
	}
}

// zeroRun runs Run over filed authors (harvested once first, then answering
// normally) plus unfiled never-harvested authors that answer "0 results"
// (zeroFiled: the filed ones answer 0 too).
func zeroRun(t *testing.T, filedN, unfiledN int, zeroFiled bool) (*database.CatalogStore, []ScopeAuthor, []ScopeAuthor, *Tally) {
	t.Helper()
	st, _ := openCatalog(t)
	filed, by := breakerAuthors(filedN)
	var unfiled []ScopeAuthor
	for i := range unfiledN {
		n := fmt.Sprintf("New Author %d", i)
		unfiled = append(unfiled, ScopeAuthor{Key: HarvestKey(n), Name: n})
	}
	var zero atomic.Bool
	l := &scriptLister{list: func(name string, page, size int) metadata.AuthorPage {
		if zero.Load() {
			return metadata.AuthorPage{Products: []metadata.CatalogProduct{}}
		}
		return slicePage(by[name], page, size) // unfiled authors have none
	}}
	h := NewHarvester(l, st, Settings{Language: "english", PageSize: 2, Concurrency: 3})
	if filedN > 0 {
		if _, err := h.Run(context.Background(), &fakeReporter{}, filed, RunOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	zero.Store(zeroFiled)
	tally, err := h.Run(context.Background(), &fakeReporter{}, append(slices.Clone(filed), unfiled...), RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return st, filed, unfiled, tally
}

func assertAllPartialAndDue(t *testing.T, st *database.CatalogStore, authors []ScopeAuthor) {
	t.Helper()
	for _, a := range authors {
		if s, _ := st.GetAuthorState(a.Key); s == nil || s.State != database.CatalogHarvestPartial {
			t.Errorf("%s: %+v; want partial", a.Name, s)
		}
	}
	h := NewHarvester(nil, st, Settings{})
	if due, _ := h.SelectDue(authors, nil); len(due) != len(authors) {
		t.Errorf("%d of %d authors due; want all", len(due), len(authors))
	}
}

// TestRun_AllUnfiledOutageStaysPartial: the very first harvest, during an
// outage: ten new authors, every listing empty. With no filed authors to
// judge, the breaker judges every author that answered, trips, and leaves
// all ten partial and due instead of complete-and-empty for 30 days.
func TestRun_AllUnfiledOutageStaysPartial(t *testing.T) {
	st, _, unfiled, tally := zeroRun(t, 0, 10, true)
	if !tally.ZeroBreakerTripped.Load() {
		t.Errorf("breaker did not trip on 10 of 10 empty answers: %s", tally.Summary())
	}
	assertAllPartialAndDue(t, st, unfiled)
}

// TestRun_FewFiledFallsBackToAllAnswered: 3 filed authors answer normally,
// 4 new authors answer 0. Three filed authors are too few to judge, so the
// judgement falls back to all 7 that answered: 4 of 7 empty trips it.
func TestRun_FewFiledFallsBackToAllAnswered(t *testing.T) {
	st, _, unfiled, tally := zeroRun(t, 3, 4, false)
	if !tally.ZeroBreakerTripped.Load() {
		t.Errorf("breaker did not trip on 4 of 7 empty answers: %s", tally.Summary())
	}
	assertAllPartialAndDue(t, st, unfiled)
}

// TestRun_TooSmallToJudgeLeavesUnfiledZeroPartial: 2 new authors, both
// empty. Nothing can be judged from 2 answers, so neither is recorded
// complete: both stay partial and are retried.
func TestRun_TooSmallToJudgeLeavesUnfiledZeroPartial(t *testing.T) {
	st, _, unfiled, tally := zeroRun(t, 0, 2, true)
	if tally.ZeroBreakerTripped.Load() {
		t.Error("a 2-author run tripped the breaker; it is too small to judge")
	}
	if !tally.ZeroBreakerUnjudged.Load() {
		t.Errorf("tally does not say the run was too small to judge: %s", tally.Summary())
	}
	assertAllPartialAndDue(t, st, unfiled)
}

// TestSettleZero_SkipsAStateChangedSinceItsHold: settleZero must not
// overwrite an author whose state a newer harvest wrote after the hold.
func TestSettleZero_SkipsAStateChangedSinceItsHold(t *testing.T) {
	st, _ := openCatalog(t)
	l := &scriptLister{list: func(string, int, int) metadata.AuthorPage {
		return metadata.AuthorPage{Products: []metadata.CatalogProduct{}}
	}}
	c := &clock{t: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)}
	h := NewHarvester(l, st, Settings{Language: "english", PageSize: 2, Concurrency: 1, Now: c.Now})
	a := ScopeAuthor{Key: HarvestKey("Ann Author"), Name: "Ann Author"}
	rz := newZeroLedger()
	if _, err := h.harvestAuthor(context.Background(), a, nil, rz); err != nil {
		t.Fatal(err)
	}
	c.Add(time.Hour)
	newer, err := h.HarvestAuthor(context.Background(), a, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.settleZero(nil, rz, &Tally{}); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetAuthorState(a.Key)
	if !got.LastAttemptAt.Equal(newer.LastAttemptAt) {
		t.Errorf("settle overwrote a newer state: last_attempt %v; want %v", got.LastAttemptAt, newer.LastAttemptAt)
	}
}

// emptyRuns drives SelectDue+Run over withCatalog authors (each lists two
// products) and empty never-harvested authors (no products ever), once per
// entry of advance (moving the clock first), and returns the store and the
// tally of each run.
func emptyRuns(t *testing.T, withCatalog, empty int, advance []time.Duration) (*database.CatalogStore, []ScopeAuthor, []ScopeAuthor, []*Tally) {
	t.Helper()
	st, _ := openCatalog(t)
	full, by := breakerAuthors(withCatalog)
	var empties []ScopeAuthor
	for i := range empty {
		n := fmt.Sprintf("Empty Author %d", i)
		empties = append(empties, ScopeAuthor{Key: HarvestKey(n), Name: n})
	}
	l := &scriptLister{list: func(name string, page, size int) metadata.AuthorPage { return slicePage(by[name], page, size) }}
	c := &clock{t: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)}
	h := NewHarvester(l, st, Settings{Language: "english", PageSize: 2, Concurrency: 3, Now: c.Now})
	all := append(slices.Clone(full), empties...)
	var tallies []*Tally
	for _, d := range advance {
		c.Add(d)
		due, err := h.SelectDue(all, nil)
		if err != nil {
			t.Fatal(err)
		}
		tally, err := h.Run(context.Background(), &fakeReporter{}, due, RunOptions{})
		if err != nil {
			t.Fatal(err)
		}
		tallies = append(tallies, tally)
	}
	return st, full, empties, tallies
}

func states(t *testing.T, st *database.CatalogStore, authors []ScopeAuthor) []string {
	t.Helper()
	var out []string
	for _, a := range authors {
		s, _ := st.GetAuthorState(a.Key)
		if s == nil {
			out = append(out, "none")
			continue
		}
		out = append(out, s.State)
	}
	return out
}

// TestRun_EmptyNeverHarvestedAuthorsCompleteAfterTheWindow: 4 authors with
// catalogs and 6 that really have none, run repeatedly. The empty six trip
// the fallback breaker every run they are judged, but each held zero run
// still counts toward an empty streak; once ShortAcceptRuns runs span
// ZeroTotalAcceptWindow they complete as empty and stop being due.
func TestRun_EmptyNeverHarvestedAuthorsCompleteAfterTheWindow(t *testing.T) {
	st, _, empties, _ := emptyRuns(t, 4, 6, []time.Duration{0, time.Hour, 4 * 24 * time.Hour})
	for _, s := range states(t, st, empties) {
		if s != database.CatalogHarvestPartial {
			t.Fatalf("empties before the window: %v; want all partial", states(t, st, empties))
		}
	}
	st, full, empties, tallies := emptyRuns(t, 4, 6, []time.Duration{0, time.Hour, 4 * 24 * time.Hour, 4 * 24 * time.Hour})
	for _, s := range append(states(t, st, full), states(t, st, empties)...) {
		if s != database.CatalogHarvestComplete {
			t.Fatalf("after the window: catalog %v empty %v; want all complete", states(t, st, full), states(t, st, empties))
		}
	}
	h := NewHarvester(nil, st, Settings{})
	if due, _ := h.SelectDue(append(slices.Clone(full), empties...), nil); len(due) != 0 {
		t.Errorf("%d authors still due after the empties were accepted", len(due))
	}
	if last := tallies[len(tallies)-1]; !strings.Contains(last.Summary(), "repeat") && last.ZeroBreakerTripped.Load() {
		t.Errorf("trip summary does not separate repeat-zero authors from first-time ones: %s", last.Summary())
	}
}

// TestRun_TinyScopedEmptyRunCompletesAfterTheWindowNotSooner: 3 empty
// authors, a run too small to judge. Three runs inside two hours (an outage
// mid-streak looks exactly like this) must NOT complete them: the window, not
// the run count, is what an outage cannot fake. A run past the window does.
func TestRun_TinyScopedEmptyRunCompletesAfterTheWindowNotSooner(t *testing.T) {
	st, _, empties, _ := emptyRuns(t, 0, 3, []time.Duration{0, time.Hour, time.Hour})
	for _, s := range states(t, st, empties) {
		if s != database.CatalogHarvestPartial {
			t.Fatalf("3 runs inside 2h: %v; want partial (no fast track)", states(t, st, empties))
		}
	}
	st, _, empties, _ = emptyRuns(t, 0, 3, []time.Duration{0, time.Hour, time.Hour, 8 * 24 * time.Hour})
	for _, s := range states(t, st, empties) {
		if s != database.CatalogHarvestComplete {
			t.Errorf("after the window: %v; want complete", states(t, st, empties))
			break
		}
	}
}

// TestSettleZero_EmptyStreakAcceptsOnlyACompleteRun: an empty-streak author
// whose run itself ended partial (an owned-ASIN lookup failed) must not be
// accepted as empty, however long its streak, and the tally must not move.
func TestSettleZero_EmptyStreakAcceptsOnlyACompleteRun(t *testing.T) {
	st, _ := openCatalog(t)
	h := NewHarvester(nil, st, Settings{})
	now := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
	since := now.Add(-8 * 24 * time.Hour)
	final := database.CatalogAuthorState{Key: "emptyauthor", Name: "Empty Author", State: database.CatalogHarvestPartial,
		LastError: "1 of 1 owned-ASIN lookups failed", LastAttemptAt: now, OpID: "op",
		ShortKind: shortEmpty, ShortRuns: ShortAcceptRuns, ShortSince: &since}
	hold := pendingHold(nil, final, "held")
	if err := st.PutAuthorState(&hold); err != nil {
		t.Fatal(err)
	}
	rz := newZeroLedger()
	rz.answered(false)
	rz.observe(final.Key, zeroObs{final: final})
	tally := &Tally{}
	tally.Partial.Store(1) // what Run recorded for the held author
	if err := h.settleZero(nil, rz, tally); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetAuthorState(final.Key)
	if got.State != database.CatalogHarvestPartial {
		t.Errorf("partial run accepted as empty: state %s", got.State)
	}
	if tally.Partial.Load() != 1 || tally.Complete.Load() != 0 || tally.EmptyAccepted.Load() != 0 {
		t.Errorf("tally moved: %s", tally.Summary())
	}
}
