// file: internal/server/catalog_coverage_op_test.go
// version: 1.2.0
// guid: c3d94f17-8a2e-4b60-9d15-7e1b0a6f3c82
// last-edited: 2026-10-12

package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/oklog/ulid/v2"

	"github.com/falkcorp/audiobook-organizer/internal/authorjunk"
	"github.com/falkcorp/audiobook-organizer/internal/catalog"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// coverageReporter records the logs and the last progress message.
type coverageReporter struct {
	catalogTestReporter
	pmu      sync.Mutex
	progress string
}

func (r *coverageReporter) UpdateProgress(_, _ int, msg string) error {
	r.pmu.Lock()
	r.progress = msg
	r.pmu.Unlock()
	return nil
}

func (r *coverageReporter) lastProgress() string {
	r.pmu.Lock()
	defer r.pmu.Unlock()
	return r.progress
}

// result decodes the single JSON line the op logged.
func (r *coverageReporter) result(t testing.TB) catalogCoverageResult {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	var found []string
	for _, l := range r.logs {
		if strings.HasPrefix(l, "{") {
			found = append(found, l)
		}
	}
	if len(found) != 1 {
		t.Fatalf("want exactly one JSON result line, got %d in logs %q", len(found), r.logs)
	}
	var res catalogCoverageResult
	if err := json.Unmarshal([]byte(found[0]), &res); err != nil {
		t.Fatalf("decode %q: %v", found[0], err)
	}
	return res
}

// failingAuthorsStore makes the credit read of one book fail.
type failingAuthorsStore struct {
	*database.PebbleStore
	failFor string
}

func (f failingAuthorsStore) GetBookAuthors(bookID string) ([]database.BookAuthor, error) {
	if bookID == f.failFor {
		return nil, errors.New("synthetic read failure")
	}
	return f.PebbleStore.GetBookAuthors(bookID)
}

func newCoverageStore(t testing.TB) (*database.PebbleStore, *database.CatalogStore) {
	t.Helper()
	ps, err := database.NewPebbleStoreInMemory("cov_" + ulid.Make().String())
	if err != nil {
		t.Fatal(err)
	}
	ps.WaitForWarmup()
	t.Cleanup(func() { _ = ps.Close() })
	cat := database.NewCatalogStoreFromStore(ps)
	if cat == nil {
		t.Fatal("no catalog store")
	}
	return ps, cat
}

// seedCoverageBook creates a book credited to authors (in order) and returns
// its id. mutate edits the row before it is created.
func seedCoverageBook(t testing.TB, ps *database.PebbleStore, title string, authors []string, mutate func(*database.Book)) string {
	t.Helper()
	b := &database.Book{Title: title, FilePath: "/lib/" + title}
	if mutate != nil {
		mutate(b)
	}
	book, err := ps.CreateBook(b)
	if err != nil {
		t.Fatal(err)
	}
	links := make([]database.BookAuthor, 0, len(authors))
	for i, name := range authors {
		a, err := ps.CreateAuthor(name)
		if err != nil {
			t.Fatalf("create author %q: %v", name, err)
		}
		links = append(links, database.BookAuthor{BookID: book.ID, AuthorID: a.ID, Role: "author", Position: i})
	}
	if len(links) > 0 {
		if err := ps.SetBookAuthors(book.ID, links); err != nil {
			t.Fatal(err)
		}
	}
	return book.ID
}

// seedCatalogBlock stores a real catalog entry through the store, filed under
// the author's name key exactly as the harvest files it.
func seedCatalogBlock(t testing.TB, cat *database.CatalogStore, author string) {
	t.Helper()
	key := catalog.AuthorNameKey(author)
	_, err := cat.UpsertEntries([]database.CatalogUpsert{{Entry: database.CatalogEntry{
		Provider: "audible", Marketplace: "us", ProviderID: "P-" + author, Title: "Title for " + author, AuthorKeys: []string{key},
	}}}, catalog.HarvestKey(author))
	if err != nil {
		t.Fatal(err)
	}
}

func runCoverage(t *testing.T, books catalogCoverageBooks, ps *database.PebbleStore, cat *database.CatalogStore) (*coverageReporter, error) {
	t.Helper()
	rep := &coverageReporter{}
	err := runCatalogCoverage(context.Background(), rep, books, ps, cat)
	return rep, err
}

func TestCatalogCoverage_ThreeCounts(t *testing.T) {
	ps, cat := newCoverageStore(t)
	var unreadableID string
	for i := range 10 {
		author := fmt.Sprintf("Author %02d", i)
		id := seedCoverageBook(t, ps, fmt.Sprintf("Title %06d", i), []string{author}, nil)
		switch {
		case i < 6:
			seedCatalogBlock(t, cat, author) // covered
		case i == 9:
			unreadableID = id // its author list cannot be read
		}
	}
	rep, err := runCoverage(t, failingAuthorsStore{PebbleStore: ps, failFor: unreadableID}, ps, cat)
	if err != nil {
		t.Fatal(err)
	}
	res := rep.result(t)
	t.Logf("covered=%d uncovered=%d unreadable=%d considered=%d blocks=%d progress=%q",
		res.BooksCovered, res.BooksUncovered, res.BooksUnreadable, res.BooksConsidered, res.CatalogAuthorBlocks, rep.lastProgress())
	if res.BooksCovered != 6 || res.BooksUncovered != 3 || res.BooksUnreadable != 1 || res.BooksConsidered != 10 {
		t.Fatalf("got covered=%d uncovered=%d unreadable=%d considered=%d; want 6/3/1/10",
			res.BooksCovered, res.BooksUncovered, res.BooksUnreadable, res.BooksConsidered)
	}
	if res.CatalogAuthorBlocks != 6 {
		t.Errorf("catalog_author_blocks = %d; want 6", res.CatalogAuthorBlocks)
	}
	if !strings.Contains(res.UnreadableNote, "unknown scope") {
		t.Errorf("unreadable_note = %q; want it to say unreadable books are counted with unknown scope", res.UnreadableNote)
	}
	if got, want := rep.lastProgress(), "covered 6 of 10 (60.0%)"; got != want {
		t.Errorf("final progress = %q; want %q", got, want)
	}
}

func TestCatalogCoverage_SecondCreditCounts(t *testing.T) {
	ps, cat := newCoverageStore(t)
	seedCoverageBook(t, ps, "Title 000001", []string{"Author 20", "Author 21"}, nil)
	seedCatalogBlock(t, cat, "Author 21") // only the SECOND credit has a block
	rep, err := runCoverage(t, ps, ps, cat)
	if err != nil {
		t.Fatal(err)
	}
	res := rep.result(t)
	if res.BooksCovered != 1 || res.BooksUncovered != 0 {
		t.Fatalf("covered=%d uncovered=%d; want a book covered through its second credit", res.BooksCovered, res.BooksUncovered)
	}
	if len(res.TopUnharvestedCredits) != 0 {
		t.Errorf("a covered book contributed unharvested credits: %+v", res.TopUnharvestedCredits)
	}
}

func TestCatalogCoverage_IgnoresIdentifiedAndNonPrimary(t *testing.T) {
	ps, cat := newCoverageStore(t)
	seedCatalogBlock(t, cat, "Author 30") // every book below is by an author WITH a block, except the first
	yes, no := true, false
	matched, noMatch := "matched", "no_match"

	seedCoverageBook(t, ps, "Title 000001", []string{"Author 31"}, nil) // unasked, uncovered: counts
	askedEmpty := seedCoverageBook(t, ps, "Title 000002", []string{"Author 30"}, nil)
	if err := ps.PutMetadataCache(&database.MetadataCandidateCache{BookID: askedEmpty}); err != nil { // asked_empty: counts
		t.Fatal(err)
	}
	seedCoverageBook(t, ps, "Title 000003", []string{"Author 30"}, func(b *database.Book) { b.MetadataReviewStatus = &matched })
	seedCoverageBook(t, ps, "Title 000004", []string{"Author 30"}, func(b *database.Book) { b.MetadataReviewStatus = &noMatch })
	seedCoverageBook(t, ps, "Title 000005", []string{"Author 30"}, func(b *database.Book) { b.IsPrimaryVersion = &no })
	seedCoverageBook(t, ps, "Title 000006", []string{"Author 30"}, func(b *database.Book) { b.MarkedForDeletion = &yes })
	seedCoverageBook(t, ps, "Title 000007", []string{"Author 30"}, func(b *database.Book) {
		now := time.Now()
		b.QuarantinedAt = &now
	})
	seedCoverageBook(t, ps, "Title 000008", []string{"Author 31"}, func(b *database.Book) { b.IsPrimaryVersion = &yes })
	// has_candidates: a covered author and a metadata_cache row with one
	// candidate. Fetched-but-unapproved is not "missing", so it is excluded.
	hasCandidates := seedCoverageBook(t, ps, "Title 000009", []string{"Author 30"}, nil)
	if err := ps.PutMetadataCache(&database.MetadataCandidateCache{
		BookID:     hasCandidates,
		Candidates: []json.RawMessage{json.RawMessage(`{"title":"a candidate"}`)},
	}); err != nil {
		t.Fatal(err)
	}

	rep, err := runCoverage(t, ps, ps, cat)
	if err != nil {
		t.Fatal(err)
	}
	res := rep.result(t)
	// Considered: 000001 (nil flag = primary, uncovered), 000002 (asked_empty,
	// covered), 000008 (explicit primary, uncovered).
	if res.BooksConsidered != 3 || res.BooksCovered != 1 || res.BooksUncovered != 2 {
		t.Fatalf("considered=%d covered=%d uncovered=%d; want 3/1/2 (identified, held, non-primary, trashed and quarantined books are not measured)",
			res.BooksConsidered, res.BooksCovered, res.BooksUncovered)
	}
	// The op's selection must agree with the number the Library's
	// "ident:missing" filter and the review summary show.
	counts := ps.IdentCounts()
	if !counts.Ready {
		t.Fatal("ident index not ready")
	}
	if want := database.IdentMissing(counts.Primary); int64(res.BooksConsidered) != want {
		t.Fatalf("books_considered = %d; the ident index's primary missing count = %d", res.BooksConsidered, want)
	}
}

func TestCatalogCoverage_TopUnharvestedOrdered(t *testing.T) {
	ps, cat := newCoverageStore(t)
	seedCatalogBlock(t, cat, "Author 40") // the catalog must not be empty
	n := 0
	add := func(credit string, books int) {
		for range books {
			n++
			seedCoverageBook(t, ps, fmt.Sprintf("Title %06d", n), []string{credit}, nil)
		}
	}
	add("Zed Author", 3)
	add("Amy Author", 3) // ties with Zed: name order breaks it
	add("Bob Author", 5)
	add("Cy Author", 1)
	for i := range 60 {
		add(fmt.Sprintf("Single %03d", i), 1)
	}
	rep, err := runCoverage(t, ps, ps, cat)
	if err != nil {
		t.Fatal(err)
	}
	top := rep.result(t).TopUnharvestedCredits
	if len(top) != catalogCoverageTopN {
		t.Fatalf("top list has %d entries; want %d", len(top), catalogCoverageTopN)
	}
	want := []catalogCoverageCredit{{"Bob Author", 5}, {"Amy Author", 3}, {"Zed Author", 3}, {"Cy Author", 1}, {"Single 000", 1}}
	if !slices.Equal(top[:len(want)], want) {
		t.Fatalf("head of top list = %+v; want %+v", top[:len(want)], want)
	}
	if !slices.IsSortedFunc(top, func(a, b catalogCoverageCredit) int {
		if a.Books != b.Books {
			return b.Books - a.Books
		}
		return strings.Compare(a.Credit, b.Credit)
	}) {
		t.Errorf("top list is not ordered by books desc, then name: %+v", top)
	}
}

func TestCatalogCoverage_WritesNothing(t *testing.T) {
	ps, cat := newCoverageStore(t)
	for i := range 12 {
		author := fmt.Sprintf("Author %02d", i)
		seedCoverageBook(t, ps, fmt.Sprintf("Title %06d", i), []string{author}, nil)
		if i%2 == 0 {
			seedCatalogBlock(t, cat, author)
		}
	}
	before := snapshotAllKeys(t, ps)
	if len(before) == 0 {
		t.Fatal("empty snapshot")
	}
	if _, err := runCoverage(t, ps, ps, cat); err != nil {
		t.Fatal(err)
	}
	after := snapshotAllKeys(t, ps)
	var added, changed, removed int
	for k, v := range after {
		old, ok := before[k]
		switch {
		case !ok:
			added++
		case old != v:
			changed++
		}
	}
	for k := range before {
		if _, ok := after[k]; !ok {
			removed++
		}
	}
	t.Logf("keys before=%d after=%d added=%d changed=%d removed=%d", len(before), len(after), added, changed, removed)
	if added != 0 || changed != 0 || removed != 0 {
		t.Fatalf("the report wrote: added=%d changed=%d removed=%d", added, changed, removed)
	}
}

func snapshotAllKeys(t testing.TB, ps *database.PebbleStore) map[string]string {
	t.Helper()
	iter, err := ps.DB().NewIter(&pebble.IterOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer iter.Close()
	out := map[string]string{}
	for iter.First(); iter.Valid(); iter.Next() {
		out[string(iter.Key())] = string(iter.Value())
	}
	if err := iter.Error(); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestCatalogCoverage_RunsConcurrently(t *testing.T) {
	ps, cat := newCoverageStore(t)
	const books = 240
	wantCovered, wantUncovered := 0, 0
	for i := range books {
		author := fmt.Sprintf("Author %03d", i)
		seedCoverageBook(t, ps, fmt.Sprintf("Title %06d", i), []string{author}, nil)
		if i%3 == 0 {
			seedCatalogBlock(t, cat, author)
			wantCovered++
		} else {
			wantUncovered++
		}
	}
	var seen []opsregistry.RunItemsOptions
	catalogCoverageOptsObserver = func(o opsregistry.RunItemsOptions) { seen = append(seen, o) }
	t.Cleanup(func() { catalogCoverageOptsObserver = nil })

	rep, err := runCoverage(t, ps, ps, cat)
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) != 1 || seen[0].Concurrency <= 1 {
		t.Fatalf("RunItems options = %+v; want exactly one call with Concurrency > 1", seen)
	}
	res := rep.result(t)

	// Sequential recount, independent of the op's loop.
	covered, uncovered := 0, 0
	all, err := ps.IdentBookIDs([]database.IdentState{database.IdentUnasked})
	if err != nil {
		t.Fatal(err)
	}
	for id := range all {
		b, err := ps.GetBookByID(id)
		if err != nil || b == nil {
			t.Fatalf("book %s: %v", id, err)
		}
		names, err := database.LiveBookAuthorNames(ps, b)
		if err != nil {
			t.Fatal(err)
		}
		hit := false
		for _, n := range names {
			if ok, err := cat.HasAuthorKey(authorjunk.FoldKey(n)); err != nil {
				t.Fatal(err)
			} else if ok {
				hit = true
			}
		}
		if hit {
			covered++
		} else {
			uncovered++
		}
	}
	if res.BooksCovered != covered || res.BooksUncovered != uncovered || covered != wantCovered || uncovered != wantUncovered {
		t.Fatalf("concurrent covered/uncovered = %d/%d; sequential recount = %d/%d; constructed = %d/%d",
			res.BooksCovered, res.BooksUncovered, covered, uncovered, wantCovered, wantUncovered)
	}
	if res.BooksConsidered != books {
		t.Errorf("considered = %d; want %d", res.BooksConsidered, books)
	}
}

func TestCatalogCoverage_EmptyCatalogIsReportedPlainly(t *testing.T) {
	ps, cat := newCoverageStore(t)
	seedCoverageBook(t, ps, "Title 000001", []string{"Author 50"}, nil)
	rep, err := runCoverage(t, ps, ps, cat)
	if err != nil {
		t.Fatal(err)
	}
	rep.mu.Lock()
	defer rep.mu.Unlock()
	if len(rep.logs) != 1 || strings.HasPrefix(rep.logs[0], "{") || !strings.Contains(rep.logs[0], "no harvested author blocks") {
		t.Fatalf("logs = %q; want one plain message, not a JSON line of zeros", rep.logs)
	}
}

type notReadyIdents struct{}

func (notReadyIdents) IdentCounts() database.IdentCounts { return database.IdentCounts{} }
func (notReadyIdents) IdentBookIDs([]database.IdentState) (map[string]struct{}, error) {
	return nil, database.ErrIdentIndexBuilding
}

func TestCatalogCoverage_FailsWhenIdentIndexNotReady(t *testing.T) {
	ps, cat := newCoverageStore(t)
	seedCatalogBlock(t, cat, "Author 60")
	err := runCatalogCoverage(context.Background(), &coverageReporter{}, ps, notReadyIdents{}, cat)
	if !errors.Is(err, database.ErrIdentIndexBuilding) {
		t.Fatalf("err = %v; want it to wrap ErrIdentIndexBuilding", err)
	}
}

// failingCatalog is a catalog whose exact-key read fails.
type failingCatalog struct{ err error }

func (f failingCatalog) HasAuthorKey(string) (bool, error) { return false, f.err }
func (failingCatalog) CountAuthorNameBlocks() (int, error) { return 5, nil }

// A catalog read error is not a book-level problem: the catalog is what the
// report measures against, so the run must fail rather than report numbers
// that quietly treat a failing read as "no block".
func TestCatalogCoverage_CatalogReadErrorFailsTheOp(t *testing.T) {
	ps, _ := newCoverageStore(t)
	for i := range 5 {
		seedCoverageBook(t, ps, fmt.Sprintf("Title %06d", i), []string{fmt.Sprintf("Author %02d", i)}, nil)
	}
	boom := errors.New("synthetic catalog failure")
	rep := &coverageReporter{}
	err := runCatalogCoverage(context.Background(), rep, ps, ps, failingCatalog{err: boom})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v; want the catalog error to fail the op", err)
	}
	rep.mu.Lock()
	defer rep.mu.Unlock()
	for _, l := range rep.logs {
		if strings.HasPrefix(l, "{") {
			t.Fatalf("a failed run still logged a result line: %q", l)
		}
	}
}

// Through the real wiring: runCatalogCoverageOp resolves the store, the
// catalog store and the ident index capability from the Server itself, so a
// regression in storeForWiring, NewCatalogStoreFromStore or AsCapability
// shows here and not in the injected-dependency tests above.
func TestCatalogCoverage_RunsThroughServerWiring(t *testing.T) {
	s, cleanup := setupTestServer(t)
	defer cleanup()
	ps := database.AsPebbleStore(s.storeForWiring())
	if ps == nil {
		t.Fatal("test server has no Pebble store")
	}
	cat := database.NewCatalogStoreFromStore(ps)
	seedCoverageBook(t, ps, "Title 000001", []string{"Author 70"}, nil)
	seedCoverageBook(t, ps, "Title 000002", []string{"Author 71"}, nil)
	seedCatalogBlock(t, cat, "Author 70")

	rep := &coverageReporter{}
	if err := s.runCatalogCoverageOp(context.Background(), nil, rep); err != nil {
		t.Fatal(err)
	}
	res := rep.result(t)
	if res.BooksConsidered != 2 || res.BooksCovered != 1 || res.BooksUncovered != 1 || res.CatalogAuthorBlocks != 1 {
		t.Fatalf("considered=%d covered=%d uncovered=%d blocks=%d; want 2/1/1/1",
			res.BooksConsidered, res.BooksCovered, res.BooksUncovered, res.CatalogAuthorBlocks)
	}

	// The def the server registered declares what the review asked for.
	def, ok := s.OpRegistry().Def(catalogCoverageOpID)
	if !ok {
		t.Fatalf("%s is not registered on the server", catalogCoverageOpID)
	}
	if !slices.Contains(def.DependsOn, catalogHarvestOpID) {
		t.Errorf("DependsOn = %v; want it to include %s", def.DependsOn, catalogHarvestOpID)
	}
	// Both directions: DependsOn is one-way, so the harvest must name the
	// report too or a harvest could start under a running report.
	harvest, ok := s.OpRegistry().Def(catalogHarvestOpID)
	if !ok {
		t.Fatalf("%s is not registered on the server", catalogHarvestOpID)
	}
	if !slices.Contains(harvest.DependsOn, catalogCoverageOpID) {
		t.Errorf("%s DependsOn = %v; want it to include %s", catalogHarvestOpID, harvest.DependsOn, catalogCoverageOpID)
	}
	if !slices.Contains(def.Reads, opsregistry.ResCatalog) || len(def.Writes) != 0 {
		t.Errorf("Reads = %v Writes = %v; want a catalog reader that writes nothing", def.Reads, def.Writes)
	}
	if !strings.Contains(def.Description, "upper bound") {
		t.Errorf("description does not say the covered count is an upper bound: %q", def.Description)
	}
}

// BenchmarkCatalogCoverage_20kBooks measures the report on 20,000 synthetic
// unidentified books (one author each, 4,000 distinct authors) against a
// catalog of 2,000 author blocks. Run:
//
//	go test ./internal/server/ -run '^$' -bench CatalogCoverage_20kBooks -benchtime 3x
func BenchmarkCatalogCoverage_20kBooks(b *testing.B) {
	const books, authors, blocks = 20000, 4000, 2000
	ps, cat := newCoverageStore(b)
	for i := range books {
		seedCoverageBook(b, ps, fmt.Sprintf("Title %06d", i), []string{fmt.Sprintf("Author %04d", i%authors)}, nil)
	}
	for i := range blocks {
		seedCatalogBlock(b, cat, fmt.Sprintf("Author %04d", i))
	}
	b.ResetTimer()
	for range b.N {
		rep := &coverageReporter{}
		if err := runCatalogCoverage(context.Background(), rep, ps, ps, cat); err != nil {
			b.Fatal(err)
		}
		if res := rep.result(b); res.BooksConsidered != books {
			b.Fatalf("considered %d; want %d", res.BooksConsidered, books)
		}
	}
	b.ReportMetric(float64(books)*float64(b.N)/b.Elapsed().Seconds(), "books/s")
}
