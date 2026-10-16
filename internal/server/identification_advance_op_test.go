// file: internal/server/identification_advance_op_test.go
// version: 1.0.0
// guid: e7a545a5-fab9-47d7-9f05-5851bcf6cdb8
// last-edited: 2026-10-16

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/catalog"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/identification"
	"github.com/falkcorp/audiobook-organizer/internal/metabatch"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// identTestStore opens a Pebble store with a ready identification index.
func identTestStore(t *testing.T) *database.PebbleStore {
	t.Helper()
	ps, err := database.NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ps.Close() })
	if err := ps.EnsureIdentIndex(context.Background()); err != nil {
		t.Fatal(err)
	}
	return ps
}

func markBooks(t *testing.T, ps *database.PebbleStore, n int) []string {
	t.Helper()
	ids := make([]string, n)
	for i := range n {
		ids[i] = fmt.Sprintf("book-%06d", i)
		if _, err := ps.MarkIdentDirty(ids[i]); err != nil {
			t.Fatal(err)
		}
	}
	return ids
}

func identDirtyCount(t *testing.T, ps *database.PebbleStore) int {
	t.Helper()
	n, after := 0, ""
	for {
		page, err := ps.IdentDirtyPage(after, 1000)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			return n
		}
		n += len(page)
		after = page[len(page)-1].BookID
	}
}

// syntheticBook is a primary book for any id (the restart tests do not need
// book rows: LoadBook is the injected reader).
func syntheticBook(id string) (*database.Book, error) {
	author := 7
	return &database.Book{ID: id, Title: "Title " + id[len(id)-6:], AuthorID: &author}, nil
}

// countingFetch is a FetchOne that counts calls and the calls in flight.
type countingFetch struct {
	calls    atomic.Int64
	inflight atomic.Int64
	onCall   func(n int64, book *database.Book)
	delay    time.Duration
}

func (c *countingFetch) fetch(ctx context.Context, b *database.Book) identification.FetchResult {
	c.inflight.Add(1)
	defer c.inflight.Add(-1)
	n := c.calls.Add(1)
	if c.onCall != nil {
		c.onCall(n, b)
	}
	if c.delay > 0 {
		select {
		case <-time.After(c.delay):
		case <-ctx.Done():
		}
	}
	return identification.FetchResult{Outcome: identification.OutcomeFetched}
}

func testAdvance(ps *database.PebbleStore, fetch func(context.Context, *database.Book) identification.FetchResult, workers int, sum *identification.Summary) identification.Advance {
	item := identification.NewItem(identification.ItemDeps{LoadBook: syntheticBook, Store: ps, FetchOne: fetch})
	return identification.New(identification.DirtySet{Store: ps}, item, func(s identification.Summary) {
		if sum != nil {
			*sum = s
		}
	}, workers, "test")
}

// 1,000 marks; the run is cancelled after about 400 fetches and restarted.
// Every mark ends cleared, and FetchOne ran at most 1,000 + (the items in
// flight at the cancel) times: no work lost, none repeated beyond the
// in-flight items.
func TestAdvance_DrainsDirtySetUnderRestart(t *testing.T) {
	ps := identTestStore(t)
	const total, cancelAt, workers = 1000, 400, 16
	markBooks(t, ps, total)

	cf := &countingFetch{delay: time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	var inflightAtCancel atomic.Int64
	cf.onCall = func(n int64, _ *database.Book) {
		if n == cancelAt {
			inflightAtCancel.Store(cf.inflight.Load())
			cancel()
		}
	}
	var sum1 identification.Summary
	err := runIdentificationAdvance(ctx, &resumeRecorder{opID: "op-ident-1"}, testAdvance(ps, cf.fetch, workers, &sum1), &identification.Tally{})
	if err == nil {
		t.Fatal("first run was not cancelled")
	}
	left := identDirtyCount(t, ps)
	firstCalls := cf.calls.Load()
	if !sum1.Canceled || left == 0 || left == total {
		t.Fatalf("after cancel: canceled=%v marks left=%d", sum1.Canceled, left)
	}

	cf.onCall = nil
	var sum2 identification.Summary
	if err := runIdentificationAdvance(context.Background(), &resumeRecorder{opID: "op-ident-2"}, testAdvance(ps, cf.fetch, workers, &sum2), &identification.Tally{}); err != nil {
		t.Fatal(err)
	}
	calls := cf.calls.Load()
	inflight := inflightAtCancel.Load()
	t.Logf("restart: marks=%d first_run_fetches=%d marks_left_after_cancel=%d in_flight_at_cancel=%d total_fetches=%d bound=%d marks_left_at_end=%d",
		total, firstCalls, left, inflight, calls, total+workers, identDirtyCount(t, ps))
	if n := identDirtyCount(t, ps); n != 0 {
		t.Fatalf("%d marks remain after the restarted run", n)
	}
	if calls > int64(total)+inflight || calls > total+workers {
		t.Fatalf("FetchOne called %d times; bound %d + %d in flight", calls, total, inflight)
	}
	if calls < total {
		t.Fatalf("FetchOne called %d times for %d marks: work was lost", calls, total)
	}
}

// A mark re-written while its book is in Item survives (compare-and-delete),
// and the book is processed a second time by the next run.
func TestAdvance_ReMarkDuringItemSurvives(t *testing.T) {
	ps := identTestStore(t)
	ids := markBooks(t, ps, 3)
	target := ids[1]
	cf := &countingFetch{}
	var perBook sync.Map
	cf.onCall = func(_ int64, b *database.Book) {
		v, _ := perBook.LoadOrStore(b.ID, new(atomic.Int64))
		if v.(*atomic.Int64).Add(1) == 1 && b.ID == target {
			if _, err := ps.MarkIdentDirty(target); err != nil {
				t.Error(err)
			}
		}
	}
	if err := runIdentificationAdvance(context.Background(), &resumeRecorder{opID: "op-remark-1"}, testAdvance(ps, cf.fetch, 4, nil), &identification.Tally{}); err != nil {
		t.Fatal(err)
	}
	page, _ := ps.IdentDirtyPage("", 10)
	if len(page) != 1 || page[0].BookID != target {
		t.Fatalf("after the first run the dirty set is %v; want only the re-marked %s", page, target)
	}
	if err := runIdentificationAdvance(context.Background(), &resumeRecorder{opID: "op-remark-2"}, testAdvance(ps, cf.fetch, 4, nil), &identification.Tally{}); err != nil {
		t.Fatal(err)
	}
	v, _ := perBook.Load(target)
	t.Logf("re-mark: %s processed %d times, marks left %d", target, v.(*atomic.Int64).Load(), identDirtyCount(t, ps))
	if v.(*atomic.Int64).Load() != 2 || identDirtyCount(t, ps) != 0 {
		t.Fatalf("re-marked book processed %d times, marks left %d", v.(*atomic.Int64).Load(), identDirtyCount(t, ps))
	}
}

// The adapter passes RunItems an explicit Concurrency equal to
// candidateFetchWorkers for the budget: for Audible's 8/s it is > 1. This
// is the "sequential loop wearing a worker-pool API" guard: it records what
// the real call site passes.
func TestAdvance_RunItemsConcurrencySetExplicitly(t *testing.T) {
	prevCfg := config.AppConfig.MetadataCandidateFetchWorkers
	config.AppConfig.MetadataCandidateFetchWorkers = 0
	t.Cleanup(func() { config.AppConfig.MetadataCandidateFetchWorkers = prevCfg })
	budget := metafetch.SourcesBudget{RPS: 8, Burst: 8}
	n := identificationAdvanceWorkers(budget)

	var mu sync.Mutex
	var seen []opsregistry.RunItemsOptions
	prev := identAdvanceRunItems
	identAdvanceRunItems = func(ctx context.Context, r opsregistry.Reporter, items []identification.Mark, fn func(context.Context, identification.Mark) error, opts ...opsregistry.RunItemsOptions) error {
		mu.Lock()
		seen = append(seen, opts...)
		mu.Unlock()
		return prev(ctx, r, items, fn, opts...)
	}
	t.Cleanup(func() { identAdvanceRunItems = prev })

	ps := identTestStore(t)
	markBooks(t, ps, 10)
	cf := &countingFetch{}
	if err := runIdentificationAdvance(context.Background(), &resumeRecorder{opID: "op-conc"}, testAdvance(ps, cf.fetch, n, nil), &identification.Tally{}); err != nil {
		t.Fatal(err)
	}
	want := candidateFetchWorkers(budget, 0)
	t.Logf("concurrency passed to RunItems: %d (candidateFetchWorkers for 8 rps = %d)", seen[0].Concurrency, want)
	if len(seen) != 1 || seen[0].Concurrency <= 1 || seen[0].Concurrency != want {
		t.Fatalf("RunItems options %+v; want Concurrency=%d (> 1)", seen, want)
	}
}

// The Label RunItems calls inside its workers reads only the item: run it
// concurrently with the items under -race.
func TestAdvance_LabelIsRaceFree(t *testing.T) {
	ps := identTestStore(t)
	markBooks(t, ps, 300)
	cf := &countingFetch{}
	rep := &labelRecorder{}
	if err := runIdentificationAdvance(context.Background(), rep, testAdvance(ps, cf.fetch, 16, nil), &identification.Tally{}); err != nil {
		t.Fatal(err)
	}
	if rep.labels.Load() < 300 {
		t.Fatalf("labels = %d", rep.labels.Load())
	}
}

type labelRecorder struct {
	resumeRecorder
	labels atomic.Int64
}

func (r *labelRecorder) SetCurrentItem(label string) {
	if label != "" {
		r.labels.Add(1)
	}
}

// Pages are read 256 at a time (D28a's chunk) and no more than one page is
// held at once.
func TestAdvance_PageSize256(t *testing.T) {
	ps := identTestStore(t)
	markBooks(t, ps, 600)
	cf := &countingFetch{}
	rec := &pageRecorder{inner: identification.DirtySet{Store: ps}}
	adv := testAdvance(ps, cf.fetch, 8, nil)
	adv.Source = rec
	if err := runIdentificationAdvance(context.Background(), &resumeRecorder{opID: "op-pages"}, adv, &identification.Tally{}); err != nil {
		t.Fatal(err)
	}
	t.Logf("page limits=%v sizes=%v", rec.limits, rec.sizes)
	if !slices.Equal(rec.sizes, []int{256, 256, 88, 0}) {
		t.Fatalf("page sizes %v; want [256 256 88 0]", rec.sizes)
	}
	for _, l := range rec.limits {
		if l != 256 {
			t.Fatalf("page limit %d; want 256", l)
		}
	}
}

type pageRecorder struct {
	inner         identification.Source
	limits, sizes []int
}

func (p *pageRecorder) Pages(ctx context.Context, after string, limit int) ([]identification.Mark, string, error) {
	page, next, err := p.inner.Pages(ctx, after, limit)
	p.limits = append(p.limits, limit)
	p.sizes = append(p.sizes, len(page))
	return page, next, err
}

// identification.advance and the three ops that share Audible's budget list
// each other in DependsOn (it is one-directional).
func TestAdvance_DependsOnMutualExclusion(t *testing.T) {
	srv, _, _ := bootRegisteredOpIDs(t)
	adv, ok := srv.opRegistry.Def(identificationAdvanceOpID)
	if !ok {
		t.Fatalf("%s is not registered", identificationAdvanceOpID)
	}
	for _, other := range []string{"metadata.candidate-fetch", "metafetch.asin-backfill", catalogHarvestOpID} {
		d, ok := srv.opRegistry.Def(other)
		if !ok {
			t.Fatalf("%s is not registered", other)
		}
		if !slices.Contains(adv.DependsOn, other) {
			t.Errorf("%s does not list %s", identificationAdvanceOpID, other)
		}
		if !slices.Contains(d.DependsOn, identificationAdvanceOpID) {
			t.Errorf("%s does not list %s", other, identificationAdvanceOpID)
		}
	}
	if adv.ConcurrencyKey != identificationAdvanceOpID || adv.DefaultPriority != opsregistry.PriorityLow ||
		adv.ResumePolicy != opsregistry.ResumeRestart || adv.Liveness != opsregistry.LivenessRunItems {
		t.Errorf("def = key %q priority %v resume %v liveness %v", adv.ConcurrencyKey, adv.DefaultPriority, adv.ResumePolicy, adv.Liveness)
	}
}

// A completed harvest that filed new entries marks the author's unanswered
// primary books; a repeat that changed nothing marks none.
func TestHarvestMarksAuthorBooksDirty(t *testing.T) {
	withCatalogEnabled(t, true)
	ps := seedCatalogLibrary(t)
	if err := ps.EnsureIdentIndex(context.Background()); err != nil {
		t.Fatal(err)
	}
	cat := database.NewCatalogStoreFromStore(ps)
	lister := &catalogFakeLister{}
	rep := &catalogTestReporter{}
	if err := runCatalogHarvest(context.Background(), rep, ps, cat, lister, catalogHarvestParams{}, false, config.AppConfig.Catalog); err != nil {
		t.Fatal(err)
	}
	live, err := ps.GetBookByFilePath("/lib/Live Book")
	if err != nil || live == nil {
		t.Fatalf("Live Book: %v", err)
	}
	page, _ := ps.IdentDirtyPage("", 10)
	if len(page) != 1 || page[0].BookID != live.ID {
		t.Fatalf("dirty set after the first harvest = %v; want only the author's book %s", page, live.ID)
	}
	if _, err := ps.ClearIdentDirty(live.ID, page[0].Epoch); err != nil {
		t.Fatal(err)
	}
	// Same listing again (manual trigger): no new entry, nothing staled.
	if err := runCatalogHarvest(context.Background(), rep, ps, cat, lister, catalogHarvestParams{Authors: []string{"Ann Author"}}, false, config.AppConfig.Catalog); err != nil {
		t.Fatal(err)
	}
	if st, _ := cat.GetAuthorState(catalog.HarvestKey("Ann Author")); st == nil || st.Created != 0 {
		t.Fatalf("repeat harvest state = %+v", st)
	}
	if n := identDirtyCount(t, ps); n != 0 {
		t.Fatalf("an unchanged harvest marked %d books", n)
	}
}

// The full op through the server: a marked book is fetched once from the
// providers; marked again with its question unchanged, it costs 0 live
// provider calls (the candidate cache and the shared provider cache answer).
func TestAdvance_UnchangedQuestionCostsNoProviderCall(t *testing.T) {
	f := newFallbackFixture(t, 10)
	f.audible.answer = answersTitle("Title 000123", "Author 07")
	b := f.book(t, "Title 000123")
	ps := database.AsPebbleStore(f.store)
	if ps == nil {
		t.Fatal("not a pebble store")
	}
	run := func(opID string) int64 {
		t.Helper()
		f.resetCalls()
		if _, err := ps.MarkIdentDirty(b.ID); err != nil {
			t.Fatal(err)
		}
		if err := f.s.runIdentificationAdvanceOp(context.Background(), json.RawMessage(`{"dry_run":false}`), &resumeRecorder{opID: opID}); err != nil {
			t.Fatal(err)
		}
		if n := identDirtyCount(t, ps); n != 0 {
			t.Fatalf("%d marks left", n)
		}
		return f.audible.calls.Load() + f.openlib.calls.Load() + f.google.calls.Load()
	}
	first := run("op-ident-cache-1")
	second := run("op-ident-cache-2")
	t.Logf("live provider calls: first mark=%d, second mark (unchanged question)=%d", first, second)
	if first == 0 {
		t.Fatal("the first run asked no provider")
	}
	if second != 0 {
		t.Fatalf("an unchanged question cost %d live provider calls", second)
	}
}

// With dry_run omitted the op only counts the dirty set.
func TestAdvance_DryRunByDefault(t *testing.T) {
	f := newFallbackFixture(t, 10)
	b := f.book(t, "Title 000124")
	ps := database.AsPebbleStore(f.store)
	if _, err := ps.MarkIdentDirty(b.ID); err != nil {
		t.Fatal(err)
	}
	rec := &resumeRecorder{opID: "op-dry"}
	if err := f.s.runIdentificationAdvanceOp(context.Background(), nil, rec); err != nil {
		t.Fatal(err)
	}
	if f.audible.calls.Load() != 0 || identDirtyCount(t, ps) != 1 {
		t.Fatalf("dry run fetched (%d calls) or cleared marks (%d left)", f.audible.calls.Load(), identDirtyCount(t, ps))
	}
}

// The candidate result maps to the driver's outcome and reason.
func TestIdentFetchResultOf(t *testing.T) {
	fragment := func() string { return metafetch.FetchStatusSkippedFragment }
	noTitle := func() string { return metafetch.FetchStatusSkippedNoTitle }
	cases := []struct {
		res    CandidateResult
		status func() string
		want   identification.FetchResult
	}{
		{CandidateResult{Status: "matched"}, nil, identification.FetchResult{Outcome: identification.OutcomeFetched}},
		{CandidateResult{Status: "matched", Candidate: &metafetch.MetadataCandidate{ManualOnly: true}}, nil,
			identification.FetchResult{Outcome: identification.OutcomeFetched, Reason: database.IdentReasonManualOnly}},
		{CandidateResult{Status: candidateStatusDeferred}, nil,
			identification.FetchResult{Outcome: identification.OutcomeDeferred, Reason: database.IdentReasonQuotaDeferred}},
		{CandidateResult{Status: "skipped", Error: "skipped: chapter fragment, " + metabatch.SkipDetailNoUsableTitle}, fragment,
			identification.FetchResult{Outcome: identification.OutcomeSkippedDone, Reason: database.IdentReasonFragment}},
		{CandidateResult{Status: "skipped", Error: "skipped: empty title, " + metabatch.SkipDetailNoUsableTitle}, noTitle,
			identification.FetchResult{Outcome: identification.OutcomeSkippedDone, Reason: database.IdentReasonNoUsableTitle}},
		{CandidateResult{Status: "skipped", Error: "skipped: marked no match"}, nil,
			identification.FetchResult{Outcome: identification.OutcomeSkippedDone}},
	}
	for i, c := range cases {
		got := identFetchResultOf(c.res, c.status)
		if got.Outcome != c.want.Outcome || got.Reason != c.want.Reason {
			t.Errorf("case %d: got %+v want %+v", i, got, c.want)
		}
	}
	if got := identFetchResultOf(CandidateResult{Status: "error", Error: "boom"}, nil); got.Outcome != identification.OutcomeError || got.Err == nil {
		t.Errorf("error case: %+v", got)
	}
}
