// file: internal/books/searchindex/searchindex_test.go
// version: 0.1.0
// guid: d95185a6-4312-49fa-8dfb-351e9fcba44c
// last-edited: 2026-11-20

package searchindex_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/index"

	"github.com/falkcorp/audiobook-organizer/internal/books/searchindex"
	"github.com/falkcorp/audiobook-organizer/internal/books/searchindex/searchindextest"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/search"
)

var now = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

// library builds n Batman-titled books with ids b000..., one author, one series
// and one tag each, so a document has every relation to resolve.
func library(n int) *searchindextest.FakeSource {
	src := &searchindextest.FakeSource{
		Authors: map[int]*database.Author{1: {ID: 1, Name: "Lucius Fox"}},
		Series:  map[int]*database.Series{1: {ID: 1, Name: "Knightfall"}},
		Tags:    map[string][]string{},
	}
	for i := range n {
		id := fmt.Sprintf("b%04d", i)
		src.Books = append(src.Books, database.Book{
			ID: id, Title: fmt.Sprintf("Year One part %d", i), AuthorID: new(1), SeriesID: new(1),
			Format: "mp3", FilePath: "/lib/" + id + ".mp3",
		})
		src.Tags[id] = []string{"noir"}
	}
	return src
}

// collector is a RebuildWriter that records every Put.
type collector struct {
	mu   sync.Mutex
	docs map[string]index.Doc
	dup  []string
}

func (c *collector) Put(id string, d index.Doc) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.docs == nil {
		c.docs = map[string]index.Doc{}
	}
	if _, ok := c.docs[id]; ok {
		c.dup = append(c.dup, id)
	}
	c.docs[id] = d
	return nil
}

func TestSpecIsValidAndCoversTheDocument(t *testing.T) {
	spec := searchindex.NewSpec(library(1), searchindex.Options{})
	if err := spec.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if spec.Name != "books" || spec.MappingVersion != "4" {
		t.Errorf("Name/MappingVersion = %q/%q", spec.Name, spec.MappingVersion)
	}
	declared := map[string]bool{}
	for _, f := range spec.Fields {
		declared[f.Name] = true
	}
	// Every field HEAD's BookDocument carries (but the dropped _type marker) is
	// declared, and a full document puts nothing outside the declared set.
	b := search.BookDocument{}
	_ = b
	full := database.Book{
		ID: "x", Title: "t", AuthorID: new(1), SeriesID: new(1), Format: "mp3", FilePath: "/p",
		Narrator: new("n"), Publisher: new("p"), Description: new("d"), Genre: new("g"), Language: new("en"),
		LibraryState: new("organized"), ISBN10: new("1"), ISBN13: new("2"), ASIN: new("3"),
		AudiobookReleaseYear: new(2001), SeriesSequence: new(2), CreatedAt: new(now), UpdatedAt: new(now),
	}
	rel := &search.BookRelations{
		Authors: map[int]*database.Author{1: {ID: 1, Name: "a"}},
		Series:  map[int]*database.Series{1: {ID: 1, Name: "s"}},
		Tags:    map[string][]string{"x": {"noir"}},
	}
	doc := searchindex.Document(&full, rel)
	for k := range doc {
		if !declared[k] {
			t.Errorf("document field %q is not declared in the spec", k)
		}
	}
	for _, k := range []string{"title", "author", "series", "tags", "year", "series_number", "date_added", "date_modified"} {
		if _, ok := doc[k]; !ok {
			t.Errorf("full document lacks %q: %v", k, doc)
		}
	}
	if _, ok := doc["_type"]; ok {
		t.Error("the reserved _type marker must not be indexed")
	}
	if got := doc["year"]; got != int64(2001) {
		t.Errorf("year = %#v, want int64(2001)", got)
	}
	if got := doc["date_added"]; got != now {
		t.Errorf("date_added = %#v", got)
	}
	// Zero values are omitted, so year:<2000 cannot match a book with no year.
	bare := searchindex.Document(&database.Book{ID: "y", Title: "t"}, nil)
	for _, k := range []string{"year", "series_number", "duration_seconds", "date_added", "has_cover", "tags"} {
		if _, ok := bare[k]; ok {
			t.Errorf("a bare book indexed %q: %v", k, bare)
		}
	}
}

func TestRebuildPutsEveryLiveBookOnceAndSkipsSoftDeleted(t *testing.T) {
	src := library(257)
	src.Books[3].MarkedForDeletion = new(true)
	c := &collector{}
	spec := searchindex.NewSpec(src, searchindex.Options{Workers: 8, PageSize: 50, ChunkSize: 7})
	if err := spec.Rebuild(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if len(c.docs) != 256 || len(c.dup) != 0 {
		t.Fatalf("docs = %d dup = %v, want 256 and none", len(c.docs), c.dup)
	}
	if _, ok := c.docs[src.Books[3].ID]; ok {
		t.Error("a soft-deleted book was indexed")
	}
	d := c.docs["b0000"]
	if d["author"] != "Lucius Fox" || d["series"] != "Knightfall" || !slices.Equal(d["tags"].([]string), []string{"noir"}) {
		t.Errorf("relations not resolved: %v", d)
	}
}

// countingSource counts the batch relation reads so the rebuild's store traffic
// is shown to be O(chunks), not O(books).
type countingSource struct {
	*searchindextest.FakeSource
	authorReads, bookReads atomic.Int64
	failAuthors            bool
	inflight, peak         atomic.Int64
}

func (s *countingSource) GetAuthorsByIDs(ids []int) (map[int]*database.Author, error) {
	s.authorReads.Add(1)
	cur := s.inflight.Add(1)
	for {
		p := s.peak.Load()
		if cur <= p || s.peak.CompareAndSwap(p, cur) {
			break
		}
	}
	time.Sleep(2 * time.Millisecond)
	s.inflight.Add(-1)
	if s.failAuthors {
		return nil, errors.New("authors unavailable")
	}
	return s.FakeSource.GetAuthorsByIDs(ids)
}

func (s *countingSource) GetBookByID(id string) (*database.Book, error) {
	s.bookReads.Add(1)
	return s.FakeSource.GetBookByID(id)
}

func TestRebuildIsParallelBoundedAndBatched(t *testing.T) {
	src := &countingSource{FakeSource: library(400)}
	c := &collector{}
	spec := searchindex.NewSpec(src, searchindex.Options{Workers: 4, PageSize: 100, ChunkSize: 10})
	if err := spec.Rebuild(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if got := src.authorReads.Load(); got != 40 {
		t.Errorf("author batch reads = %d, want 40 (one per chunk of 10)", got)
	}
	if got := src.bookReads.Load(); got != 0 {
		t.Errorf("point reads during a rebuild = %d, want 0", got)
	}
	if peak := src.peak.Load(); peak < 2 || peak > 4 {
		t.Errorf("peak concurrent chunk reads = %d, want 2..4 (parallel, bounded by Workers)", peak)
	}
}

func TestRebuildFailsRatherThanIndexPartialRelations(t *testing.T) {
	src := &countingSource{FakeSource: library(30), failAuthors: true}
	c := &collector{}
	spec := searchindex.NewSpec(src, searchindex.Options{Workers: 2, PageSize: 10, ChunkSize: 5})
	err := spec.Rebuild(context.Background(), c)
	if err == nil || !strings.Contains(err.Error(), "authors unavailable") {
		t.Fatalf("Rebuild = %v, want the relation error", err)
	}
}

func TestRebuildStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := searchindex.NewSpec(library(50), searchindex.Options{}).Rebuild(ctx, &collector{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Rebuild on a cancelled context = %v", err)
	}
}

func TestDocumentReconcilerContract(t *testing.T) {
	src := library(2)
	src.Books[1].MarkedForDeletion = new(true)
	spec := searchindex.NewSpec(src, searchindex.Options{})
	ctx := context.Background()
	if d, ok, err := spec.Document(ctx, "b0000"); err != nil || !ok || d["author"] != "Lucius Fox" {
		t.Errorf("live book: %v %v %v", d, ok, err)
	}
	for _, id := range []string{"b0001", "nope"} {
		if _, ok, err := spec.Document(ctx, id); err != nil || ok {
			t.Errorf("%s: ok=%v err=%v, want a clean delete signal", id, ok, err)
		}
	}
}

// translation of the search grammar, shown by what the index answers: the
// stand-in opener runs the translated query against a real bleve index.
func TestTranslateAnswersLikeTheGrammarSays(t *testing.T) {
	src := &searchindextest.FakeSource{
		Authors: map[int]*database.Author{1: {ID: 1, Name: "Lucius Fox"}, 2: {ID: 2, Name: "Selina Kyle"}},
		Series:  map[int]*database.Series{},
		Books: []database.Book{
			{ID: "a", Title: "Year One", AuthorID: new(1), Format: "mp3", AudiobookReleaseYear: new(1987), CreatedAt: new(now.Add(-48 * time.Hour))},
			{ID: "b", Title: "Knightfall Begins", AuthorID: new(2), Format: "m4b", AudiobookReleaseYear: new(1993), CreatedAt: new(now.Add(-24 * 30 * time.Hour))},
			{ID: "c", Title: "The Long Halloween", AuthorID: new(2), Format: "m4b", AudiobookReleaseYear: new(1996), Duration: new(7200)},
			{ID: "d", Title: "Dark Victory", AuthorID: new(1), Format: "mp3"},
			{ID: "e", Title: "Bat/Signal Rising", AuthorID: new(1), Format: "flac"},
		},
	}
	ctx := context.Background()
	hold := make(chan struct{})
	close(hold)
	op := &searchindextest.Opener{DBParent: t.TempDir(), Module: searchindex.Module, HoldRebuild: hold}
	ix, err := op.Open(ctx, searchindex.NewSpec(src, searchindex.Options{}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ix.Close() })
	if err := ix.(searchindextest.Rebuilt).WaitRebuilt(ctx); err != nil {
		t.Fatal(err)
	}
	run := func(text string) []string {
		t.Helper()
		tr, err := searchindex.Translate(text, now)
		if err != nil {
			t.Fatalf("%q: %v", text, err)
		}
		q := tr.Query
		if tr.MatchAll {
			q = searchindex.MatchAllQuery()
		}
		res, err := ix.Search(ctx, q, index.Page{Size: 100, SortBy: []string{"_id"}})
		if err != nil {
			t.Fatalf("%q: search: %v", text, err)
		}
		var ids []string
		for _, h := range res.Hits {
			ids = append(ids, h.ID)
		}
		return ids
	}
	for _, tc := range []struct {
		text string
		want []string
	}{
		{"", []string{"a", "b", "c", "d", "e"}},
		{"bat/signal", []string{"e"}},
		{`"bat/signal"`, []string{"e"}},
		{"knightfall", []string{"b"}},
		{"Selina Kyle", []string{"b", "c"}},
		{"selina halloween", []string{"c"}},
		{`"year one"`, []string{"a"}},
		{"title:dark", []string{"d"}},
		{"title:knightfal~", []string{"b"}},
		{"title:kni*", []string{"b"}},
		{"format:m4b", []string{"b", "c"}},
		{"year:>1990", []string{"b", "c"}},
		{"year:>=1993", []string{"b", "c"}},
		{"year:<1993", []string{"a"}},
		{"year:[1987 TO 1993]", []string{"a", "b"}},
		{"duration:>1h", []string{"c"}},
		{"added:>-7d", []string{"a"}},
		{"-format:mp3", []string{"b", "c", "e"}},
		{"format:m4b -title:halloween", []string{"b"}},
		{"title:year OR title:dark", []string{"a", "d"}},
		{"author:(lucius|selina)", []string{"a", "b", "c", "d", "e"}},
		{"title:year^3", []string{"a"}},
		{"zzzz", nil},
	} {
		if got := run(tc.text); !slices.Equal(got, tc.want) {
			t.Errorf("search %q = %v, want %v", tc.text, got, tc.want)
		}
	}
}

func TestTranslateRefusesWhatTheContractCannotExpress(t *testing.T) {
	for _, text := range []string{
		"title:*fall",                    // a leading wildcard
		"read_status:read OR year:>1990", // per-user term under OR
	} {
		_, err := searchindex.Translate(text, now)
		if err == nil {
			t.Errorf("%q translated; want a refusal", text)
			continue
		}
		if !errors.Is(err, searchindex.ErrNotExpressible) {
			t.Logf("%q refused by HEAD's own check: %v", text, err)
		}
	}
	// The two shapes the owner named are ErrNotExpressible, not a HEAD error.
	for _, text := range []string{"title:*fall", "read_status:read OR year:>1990"} {
		if _, err := searchindex.Translate(text, now); !errors.Is(err, searchindex.ErrNotExpressible) {
			t.Errorf("%q = %v, want ErrNotExpressible", text, err)
		}
	}
}

func TestTranslateSplitsPerUserAndTagTerms(t *testing.T) {
	tr, err := searchindex.Translate("knightfall read_status:read tag:noir", now)
	if err != nil {
		t.Fatal(err)
	}
	if len(tr.PerUser) != 1 || len(tr.Tags) != 1 || tr.Query == nil || tr.MatchAll {
		t.Errorf("translation = %+v", tr)
	}
	tr, err = searchindex.Translate("-read_status:read", now)
	if err != nil || !tr.MatchAll || len(tr.PerUser) != 1 || !tr.PerUser[0].Negated {
		t.Errorf("negated per-user only: %+v %v", tr, err)
	}
	tr, err = searchindex.Translate("read_status:read", now)
	if err != nil || !tr.MatchAll || len(tr.PerUser) != 1 {
		t.Errorf("per-user only: %+v %v", tr, err)
	}
}

func TestNumericBoundsAreExactAtTheEdge(t *testing.T) {
	// year:>1990 must exclude 1990 itself: the contract's range is inclusive, so
	// the translator nudges an exclusive bound by one ulp.
	tr, err := searchindex.Translate("year:>1990", now)
	if err != nil {
		t.Fatal(err)
	}
	n, err := index.Describe(tr.Query)
	if err != nil {
		t.Fatal(err)
	}
	min, ok := n.Min.(float64)
	if n.Op != index.OpRange || !ok || !(min > 1990) || min >= 1991 || n.Max != nil {
		t.Errorf("node = %+v", n)
	}
}

func TestDerivedCatalog(t *testing.T) {
	ds := searchindex.DerivedIndexes()
	var names []string
	for _, d := range ds {
		if err := d.Validate(); err != nil {
			t.Errorf("%s: %v", d.Name, err)
		}
		names = append(names, d.Name)
	}
	if want := []string{"books", "memdb", "hnsw", "chromem"}; !slices.Equal(names, want) {
		t.Errorf("names = %v, want %v", names, want)
	}
	cat := searchindex.Catalog(searchindex.HNSWConfig{})
	for _, d := range cat {
		hasHooks := d.Hooks.Snapshot != nil || d.Hooks.Load != nil
		if hasHooks != (d.Index.Name == "hnsw") {
			t.Errorf("%s: hooks present = %v", d.Index.Name, hasHooks)
		}
	}
}

// fakeHNSW stands in for *store.HNSWEmbeddingStore.
type fakeHNSW struct {
	exportErr, importErr error
	reconcile            bool
	count                int
	exported             []string
	discarded            int
	types                []string
}

func (f *fakeHNSW) ExportWithTruth(dir string, truth func(string) (int, error)) error {
	f.exported = append(f.exported, dir)
	return f.exportErr
}

func (f *fakeHNSW) ImportCountChecked(dir string, truth func(string) (int, error)) error {
	for _, ty := range f.types {
		_, _ = truth(ty)
	}
	return f.importErr
}
func (f *fakeHNSW) NeedsReconcile() bool { return f.reconcile }
func (f *fakeHNSW) DiscardForRebuild()   { f.discarded++ }
func (f *fakeHNSW) CountByType(context.Context, string) (int, error) {
	return f.count, nil
}

func gen(b byte) storage.Generation { var g storage.Generation; g[15] = b; return g }

func TestHNSWHooks(t *testing.T) {
	ctx := context.Background()
	truth := func(string) (int, error) { return 7, nil }

	t.Run("snapshot stamps the generation after the export", func(t *testing.T) {
		f := &fakeHNSW{}
		h := searchindex.HNSWHooks(searchindex.HNSWConfig{Store: f, Truth: truth})
		dir := t.TempDir() + "/hnsw"
		if err := h.Snapshot(ctx, dir, gen(5)); err != nil {
			t.Fatal(err)
		}
		if ok, err := storage.ArtifactAccepted(dir, gen(5)); err != nil || !ok {
			t.Errorf("accepted at its own generation = %v %v", ok, err)
		}
		if ok, _ := storage.ArtifactAccepted(dir, gen(6)); ok {
			t.Error("accepted at a later generation")
		}
	})
	t.Run("a failed export stamps nothing and clears the old stamp", func(t *testing.T) {
		f := &fakeHNSW{}
		h := searchindex.HNSWHooks(searchindex.HNSWConfig{Store: f, Truth: truth})
		dir := t.TempDir() + "/hnsw"
		if err := h.Snapshot(ctx, dir, gen(5)); err != nil {
			t.Fatal(err)
		}
		f.exportErr = database.ErrHNSWIncomplete
		if err := h.Snapshot(ctx, dir, gen(6)); !errors.Is(err, database.ErrHNSWIncomplete) {
			t.Fatalf("Snapshot = %v", err)
		}
		if ok, _ := storage.ArtifactAccepted(dir, gen(5)); ok {
			t.Error("the old stamp survived a failed export over its content")
		}
	})
	t.Run("zero generation is refused", func(t *testing.T) {
		h := searchindex.HNSWHooks(searchindex.HNSWConfig{Store: &fakeHNSW{}, Truth: truth})
		if err := h.Snapshot(ctx, t.TempDir(), storage.Generation{}); err == nil {
			t.Error("stamped the zero generation")
		}
	})

	stamped := func(t *testing.T, f *fakeHNSW) (storage.DerivedHooks, string) {
		h := searchindex.HNSWHooks(searchindex.HNSWConfig{Store: f, Truth: truth})
		dir := t.TempDir() + "/hnsw"
		if err := h.Snapshot(ctx, dir, gen(5)); err != nil {
			t.Fatal(err)
		}
		return h, dir
	}
	t.Run("load accepts a matching, complete, non-empty snapshot", func(t *testing.T) {
		f := &fakeHNSW{count: 3, types: []string{"book"}}
		h, dir := stamped(t, f)
		if ok, err := h.Load(ctx, dir, gen(5)); err != nil || !ok {
			t.Errorf("Load = %v %v", ok, err)
		}
		if f.discarded != 0 {
			t.Error("an accepted snapshot was discarded")
		}
	})
	for name, tc := range map[string]struct {
		f   *fakeHNSW
		cur storage.Generation
	}{
		"a generation mismatch":                      {&fakeHNSW{count: 3, types: []string{"book"}}, gen(6)},
		"a snapshot pending a reconcile":             {&fakeHNSW{count: 3, reconcile: true, types: []string{"book"}}, gen(5)},
		"an empty graph (discarded as inconsistent)": {&fakeHNSW{count: 0, types: []string{"book"}}, gen(5)},
		"no entity type consulted":                   {&fakeHNSW{count: 3}, gen(5)},
	} {
		t.Run("load refuses "+name, func(t *testing.T) {
			h, dir := stamped(t, tc.f)
			if ok, err := h.Load(ctx, dir, tc.cur); err != nil || ok {
				t.Errorf("Load = %v %v, want a clean refusal", ok, err)
			}
		})
	}
	t.Run("load: no snapshot files is a refusal, not an error", func(t *testing.T) {
		f := &fakeHNSW{importErr: database.ErrNoHNSWSnapshot}
		h, dir := stamped(t, f)
		if ok, err := h.Load(ctx, dir, gen(5)); err != nil || ok {
			t.Errorf("Load = %v %v", ok, err)
		}
	})
	t.Run("load: an import error discards and surfaces", func(t *testing.T) {
		f := &fakeHNSW{importErr: errors.New("boom")}
		h, dir := stamped(t, f)
		if ok, err := h.Load(ctx, dir, gen(5)); err == nil || ok || f.discarded != 1 {
			t.Errorf("Load = %v %v discarded=%d", ok, err, f.discarded)
		}
	})
	t.Run("load: an unstamped directory is never accepted", func(t *testing.T) {
		h := searchindex.HNSWHooks(searchindex.HNSWConfig{Store: &fakeHNSW{count: 3, types: []string{"book"}}, Truth: truth})
		if ok, err := h.Load(ctx, t.TempDir(), gen(5)); err != nil || ok {
			t.Errorf("Load = %v %v", ok, err)
		}
	})
}
