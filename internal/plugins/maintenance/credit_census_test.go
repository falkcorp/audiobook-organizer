// file: internal/plugins/maintenance/credit_census_test.go
// version: 1.0.0
// guid: 974607e6-4eb5-4df4-8e22-9eefce977bf4
// last-edited: 2026-10-04

package maintenance

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// censusFakeStore is a hand-written creditCensusStore. A real PebbleStore runs
// the narrator junction sync on CreateBook, which repairs exactly the drifted
// states this census must find, so the per-class matrix is seeded here. It
// reproduces author tombstones: GetAuthorByID follows them, GetAllAuthors does
// not list the merged-away id.
type censusFakeStore struct {
	mu         sync.Mutex
	books      map[string]*database.Book
	joins      map[string][]database.BookAuthor
	junctions  map[string][]database.BookNarrator
	authors    map[int]database.Author
	tombstones map[int]int
	narrators  map[int]database.Narrator
	// failBook makes every read of that book fail.
	failBook string
	// listed overrides ListBookIDs (to list an id whose row is gone).
	listed []string
	reads  int
}

func newCensusFakeStore() *censusFakeStore {
	return &censusFakeStore{
		books:      map[string]*database.Book{},
		joins:      map[string][]database.BookAuthor{},
		junctions:  map[string][]database.BookNarrator{},
		authors:    map[int]database.Author{},
		tombstones: map[int]int{},
		narrators:  map[int]database.Narrator{},
	}
}

func (s *censusFakeStore) ListBookIDs() ([]string, error) {
	if s.listed != nil {
		return append([]string(nil), s.listed...), nil
	}
	ids := make([]string, 0, len(s.books))
	for id := range s.books {
		ids = append(ids, id)
	}
	return ids, nil
}

func (s *censusFakeStore) GetBookByID(id string) (*database.Book, error) {
	s.mu.Lock()
	s.reads++
	s.mu.Unlock()
	if id == s.failBook {
		return nil, errors.New("disk on fire")
	}
	b, ok := s.books[id]
	if !ok {
		return nil, nil
	}
	cp := *b
	return &cp, nil
}

func (s *censusFakeStore) GetBookAuthors(id string) ([]database.BookAuthor, error) {
	return append([]database.BookAuthor(nil), s.joins[id]...), nil
}

func (s *censusFakeStore) GetBookNarrators(id string) ([]database.BookNarrator, error) {
	return append([]database.BookNarrator(nil), s.junctions[id]...), nil
}

func (s *censusFakeStore) GetAllAuthors() ([]database.Author, error) {
	out := make([]database.Author, 0, len(s.authors))
	for _, a := range s.authors {
		out = append(out, a)
	}
	return out, nil
}

func (s *censusFakeStore) GetAuthorByID(id int) (*database.Author, error) {
	if a, ok := s.authors[id]; ok {
		return &a, nil
	}
	if canon, ok := s.tombstones[id]; ok {
		return s.GetAuthorByID(canon)
	}
	return nil, nil
}

func (s *censusFakeStore) ListNarrators() ([]database.Narrator, error) {
	out := make([]database.Narrator, 0, len(s.narrators))
	for _, n := range s.narrators {
		out = append(out, n)
	}
	return out, nil
}

func (s *censusFakeStore) GetNarratorByID(id int) (*database.Narrator, error) {
	if n, ok := s.narrators[id]; ok {
		return &n, nil
	}
	return nil, nil
}

var _ creditCensusStore = (*censusFakeStore)(nil)

func strp(s string) *string { return &s }
func intp(i int) *int       { return &i }

func authorsJoin(bookID string, pairs ...[2]int) []database.BookAuthor {
	out := make([]database.BookAuthor, len(pairs))
	for i, p := range pairs {
		out[i] = database.BookAuthor{BookID: bookID, AuthorID: p[0], Position: p[1], Role: "author"}
	}
	return out
}

func narratorsJunction(bookID string, pairs ...[2]int) []database.BookNarrator {
	out := make([]database.BookNarrator, len(pairs))
	for i, p := range pairs {
		out[i] = database.BookNarrator{BookID: bookID, NarratorID: p[0], Position: p[1], Role: "narrator"}
	}
	return out
}

// seedCreditCensus builds one book per class plus clean books, and returns the
// expected class -> book ids.
func seedCreditCensus(t *testing.T) (*censusFakeStore, map[string][]string) {
	t.Helper()
	s := newCensusFakeStore()
	// Authors.
	s.authors[1] = database.Author{ID: 1, Name: "Brandon Sanderson"}
	s.authors[2] = database.Author{ID: 2, Name: "Mary Robinette Kowal"}
	s.authors[3] = database.Author{ID: 3, Name: "Brandon Sanderson, Mary Robinette Kowal"} // combined
	s.authors[4] = database.Author{ID: 4, Name: "Ursula K. Le Guin"}
	s.tombstones[90] = 4 // 90 was merged into 4
	// Narrators.
	s.narrators[10] = database.Narrator{ID: 10, Name: "Michael Kramer"}
	s.narrators[11] = database.Narrator{ID: 11, Name: "Kate Reading"}
	s.narrators[12] = database.Narrator{ID: 12, Name: "Kate Reading, Michael Kramer"} // combined

	// cleanBook: AuthorID primary in a normalized join, column == junction.
	cleanBook := func(id string) {
		s.books[id] = &database.Book{ID: id, Title: id, AuthorID: intp(1), Narrator: strp("Michael Kramer, Kate Reading")}
		s.joins[id] = authorsJoin(id, [2]int{1, 0}, [2]int{2, 1})
		s.junctions[id] = narratorsJunction(id, [2]int{10, 0}, [2]int{11, 1})
	}
	// Enough clean books that the worker pool really runs concurrently.
	for i := 0; i < 300; i++ {
		cleanBook(fmt.Sprintf("clean-%03d", i))
	}
	// A clean book whose snapshot matches and whose NarratorsJSON agrees.
	cleanBook("clean-snapshot")
	s.books["clean-snapshot"].Author = &database.Author{ID: 1, Name: "Brandon Sanderson"}
	s.books["clean-snapshot"].NarratorsJSON = strp(`["Michael Kramer","Kate Reading"]`)
	// A co-narrating author: CleanNarratorCredit drops the author's name, so the
	// cleaned column equals the junction.
	cleanBook("clean-selfread")
	s.joins["clean-selfread"] = authorsJoin("clean-selfread", [2]int{1, 0})
	s.books["clean-selfread"].Narrator = strp("Michael Kramer, Brandon Sanderson")
	s.junctions["clean-selfread"] = narratorsJunction("clean-selfread", [2]int{10, 0})

	want := map[string][]string{}
	put := func(class, id string) { want[class] = append(want[class], id) }

	// no_author
	s.books["b-noauthor"] = &database.Book{ID: "b-noauthor"}
	put(ccNoAuthor, "b-noauthor")

	// author_id_join_empty
	s.books["b-joinempty"] = &database.Book{ID: "b-joinempty", AuthorID: intp(1)}
	put(ccAuthorIDJoinEmpty, "b-joinempty")

	// author_id_not_primary: AuthorID 2 is at position 1, author 1 at 0.
	s.books["b-notprimary"] = &database.Book{ID: "b-notprimary", AuthorID: intp(2)}
	s.joins["b-notprimary"] = authorsJoin("b-notprimary", [2]int{1, 0}, [2]int{2, 1})
	put(ccAuthorIDNotPrimary, "b-notprimary")

	// author_id_not_in_join
	s.books["b-notinjoin"] = &database.Book{ID: "b-notinjoin", AuthorID: intp(4)}
	s.joins["b-notinjoin"] = authorsJoin("b-notinjoin", [2]int{1, 0})
	put(ccAuthorIDNotInJoin, "b-notinjoin")

	// author_positions_not_normalized + all_zero: "A @0, B @0".
	s.books["b-allzero"] = &database.Book{ID: "b-allzero", AuthorID: intp(1)}
	s.joins["b-allzero"] = authorsJoin("b-allzero", [2]int{1, 0}, [2]int{2, 0})
	put(ccAuthorPositionsNotNorm, "b-allzero")
	put(ccAuthorPositionsAllZero, "b-allzero")

	// author_positions_not_normalized only: a gap (0, 2).
	s.books["b-gap"] = &database.Book{ID: "b-gap", AuthorID: intp(1)}
	s.joins["b-gap"] = authorsJoin("b-gap", [2]int{1, 0}, [2]int{2, 2})
	put(ccAuthorPositionsNotNorm, "b-gap")

	// author_join_combined
	s.books["b-combined"] = &database.Book{ID: "b-combined", AuthorID: intp(3)}
	s.joins["b-combined"] = authorsJoin("b-combined", [2]int{3, 0})
	put(ccAuthorJoinCombined, "b-combined")

	// author_snapshot_stale: snapshot names author 2, AuthorID is 1.
	s.books["b-snapshot"] = &database.Book{ID: "b-snapshot", AuthorID: intp(1), Author: &database.Author{ID: 2, Name: "Mary Robinette Kowal"}}
	s.joins["b-snapshot"] = authorsJoin("b-snapshot", [2]int{1, 0})
	put(ccAuthorSnapshotStale, "b-snapshot")

	// author_snapshot_stale: same id, old name.
	s.books["b-snapname"] = &database.Book{ID: "b-snapname", AuthorID: intp(1), Author: &database.Author{ID: 1, Name: "B. Sanderson"}}
	s.joins["b-snapname"] = authorsJoin("b-snapname", [2]int{1, 0})
	put(ccAuthorSnapshotStale, "b-snapname")

	// author_id_dangling: join names author 77, which does not exist.
	s.books["b-dangling"] = &database.Book{ID: "b-dangling", AuthorID: intp(1)}
	s.joins["b-dangling"] = authorsJoin("b-dangling", [2]int{1, 0}, [2]int{77, 1})
	put(ccAuthorIDDangling, "b-dangling")

	// author_id_tombstoned: AuthorID 90 redirects to 4, join credits 90.
	s.books["b-tomb"] = &database.Book{ID: "b-tomb", AuthorID: intp(90)}
	s.joins["b-tomb"] = authorsJoin("b-tomb", [2]int{90, 0})
	put(ccAuthorIDTombstoned, "b-tomb")

	// A tombstoned co-author: the narrator column names author 4's canonical
	// spelling, reached through merged id 90. Cleaning must still drop it as one
	// of the book's own authors, so the narrator classes stay clean.
	put(ccAuthorIDTombstoned, "b-selfread-tomb")
	s.books["b-selfread-tomb"] = &database.Book{ID: "b-selfread-tomb", AuthorID: intp(1), Narrator: strp("Michael Kramer, Ursula K. Le Guin")}
	s.joins["b-selfread-tomb"] = authorsJoin("b-selfread-tomb", [2]int{1, 0}, [2]int{90, 1})
	s.junctions["b-selfread-tomb"] = narratorsJunction("b-selfread-tomb", [2]int{10, 0})

	// narrator_column_no_junction
	s.books["n-nojunction"] = &database.Book{ID: "n-nojunction", AuthorID: intp(1), Narrator: strp("Michael Kramer")}
	s.joins["n-nojunction"] = authorsJoin("n-nojunction", [2]int{1, 0})
	put(ccNarratorColumnNoJunction, "n-nojunction")

	// narrator_column_differs_junction: column says Kate, junction says Michael.
	s.books["n-differs"] = &database.Book{ID: "n-differs", AuthorID: intp(1), Narrator: strp("Kate Reading")}
	s.joins["n-differs"] = authorsJoin("n-differs", [2]int{1, 0})
	s.junctions["n-differs"] = narratorsJunction("n-differs", [2]int{10, 0})
	put(ccNarratorColumnDiffers, "n-differs")

	// narrator_column_differs_junction: same people, different order.
	s.books["n-order"] = &database.Book{ID: "n-order", AuthorID: intp(1), Narrator: strp("Kate Reading, Michael Kramer")}
	s.joins["n-order"] = authorsJoin("n-order", [2]int{1, 0})
	s.junctions["n-order"] = narratorsJunction("n-order", [2]int{10, 0}, [2]int{11, 1})
	put(ccNarratorColumnDiffers, "n-order")

	// narrator_junction_no_column
	s.books["n-nocolumn"] = &database.Book{ID: "n-nocolumn", AuthorID: intp(1)}
	s.joins["n-nocolumn"] = authorsJoin("n-nocolumn", [2]int{1, 0})
	s.junctions["n-nocolumn"] = narratorsJunction("n-nocolumn", [2]int{10, 0})
	put(ccNarratorJunctionNoColumn, "n-nocolumn")

	// narrator_column_not_people: a self-read (column names only the author).
	// Junction empty, and NOT counted as narrator_column_no_junction.
	s.books["n-selfread"] = &database.Book{ID: "n-selfread", AuthorID: intp(1), Narrator: strp("Brandon Sanderson")}
	s.joins["n-selfread"] = authorsJoin("n-selfread", [2]int{1, 0})
	put(ccNarratorColumnNotPeople, "n-selfread")

	// narrators_json_junction_empty + narrators_json_disagrees_both: JSON names
	// Kate, junction empty, column names Michael.
	s.books["n-json"] = &database.Book{ID: "n-json", AuthorID: intp(1), Narrator: strp("Michael Kramer"), NarratorsJSON: strp(`["Kate Reading"]`)}
	s.joins["n-json"] = authorsJoin("n-json", [2]int{1, 0})
	put(ccNarratorsJSONNoJunction, "n-json")
	put(ccNarratorsJSONDisagrees, "n-json")
	put(ccNarratorColumnNoJunction, "n-json")

	// NarratorsJSON as a bare string with no column and no junction: the only
	// narrator source, so it matches neither.
	s.books["n-jsonok"] = &database.Book{ID: "n-jsonok", AuthorID: intp(1), NarratorsJSON: strp("Kate Reading")}
	s.joins["n-jsonok"] = authorsJoin("n-jsonok", [2]int{1, 0})
	put(ccNarratorsJSONNoJunction, "n-jsonok")
	put(ccNarratorsJSONDisagrees, "n-jsonok") // no column, no junction: matches neither
	// A JSON that agrees with the junction (set equality) is not a disagreement.
	s.books["n-jsonjunction"] = &database.Book{ID: "n-jsonjunction", AuthorID: intp(1), Narrator: strp("Michael Kramer"),
		NarratorsJSON: strp(`["Michael Kramer"]`)}
	s.joins["n-jsonjunction"] = authorsJoin("n-jsonjunction", [2]int{1, 0})
	s.junctions["n-jsonjunction"] = narratorsJunction("n-jsonjunction", [2]int{10, 0})

	// narrator_positions_not_normalized
	s.books["n-pos"] = &database.Book{ID: "n-pos", AuthorID: intp(1), Narrator: strp("Michael Kramer, Kate Reading")}
	s.joins["n-pos"] = authorsJoin("n-pos", [2]int{1, 0})
	s.junctions["n-pos"] = narratorsJunction("n-pos", [2]int{10, 0}, [2]int{11, 0})
	put(ccNarratorPositionsNotNorm, "n-pos")

	// narrator_junction_combined: one record named for two people. The column
	// splits into the two people, so it also differs from the junction.
	s.books["n-combined"] = &database.Book{ID: "n-combined", AuthorID: intp(1), Narrator: strp("Kate Reading, Michael Kramer")}
	s.joins["n-combined"] = authorsJoin("n-combined", [2]int{1, 0})
	s.junctions["n-combined"] = narratorsJunction("n-combined", [2]int{12, 0})
	put(ccNarratorJunctionCombined, "n-combined")
	put(ccNarratorColumnDiffers, "n-combined")

	// narrator_id_dangling: junction names narrator 99. Its name is unknown, so
	// the column also differs from what the junction resolves to.
	s.books["n-dangling"] = &database.Book{ID: "n-dangling", AuthorID: intp(1), Narrator: strp("Michael Kramer, Kate Reading")}
	s.joins["n-dangling"] = authorsJoin("n-dangling", [2]int{1, 0})
	s.junctions["n-dangling"] = narratorsJunction("n-dangling", [2]int{10, 0}, [2]int{99, 1})
	put(ccNarratorIDDangling, "n-dangling")
	put(ccNarratorColumnDiffers, "n-dangling")

	return s, want
}

// censusResultReporter is fakeReporter (whose UpdateProgress is safe from
// RunItems workers) plus a SetResult sink. resultReporter records progress in
// an unguarded slice, so it cannot sit under a concurrent RunItems.
type censusResultReporter struct {
	fakeReporter
	result any
}

func (r *censusResultReporter) SetResult(v any) error { r.result = v; return nil }

// censusDefRegistry captures the defs a plugin registers.
type censusDefRegistry struct{ defs []sdk.OperationDef }

func (c *censusDefRegistry) RegisterOp(def sdk.OperationDef) error {
	c.defs = append(c.defs, def)
	return nil
}

func (c *censusDefRegistry) EnqueueOp(context.Context, string, any, ...sdk.EnqueueOption) (string, error) {
	return "", nil
}

// creditCensusRegisteredDef returns the census def as Plugin.Register hands it
// to the registry, failing when it is not registered.
func creditCensusRegisteredDef(t *testing.T, p *Plugin) *sdk.OperationDef {
	t.Helper()
	reg := &censusDefRegistry{}
	if err := p.Register(reg); err != nil {
		t.Fatalf("Register: %v", err)
	}
	for i := range reg.defs {
		if reg.defs[i].ID == "maintenance.credit-census" {
			return &reg.defs[i]
		}
	}
	t.Fatalf("maintenance.credit-census is not registered (%d defs)", len(reg.defs))
	return nil
}

func runCensusForTest(t *testing.T, s creditCensusStore, concurrency int) (*creditCensusResult, error) {
	t.Helper()
	return runCreditCensusScan(context.Background(), s, &fakeReporter{}, concurrency)
}

func TestCreditCensus_EveryClass(t *testing.T) {
	s, want := seedCreditCensus(t)
	res, err := runCensusForTest(t, s, 8)
	if err != nil {
		t.Fatalf("census: %v", err)
	}
	if !res.Complete {
		t.Fatalf("Complete = false on a clean run: %s", res.Error)
	}
	if len(res.Classes) != len(creditCensusClasses) {
		t.Fatalf("result has %d classes, want every defined class (%d)", len(res.Classes), len(creditCensusClasses))
	}

	got := map[string][]string{}
	for _, c := range res.Classes {
		if c.Count != len(c.BookIDs) {
			t.Errorf("class %s: count %d but %d book ids — every count must click through to its books",
				c.Key, c.Count, len(c.BookIDs))
		}
		if res.Counts[c.Key] != c.Count {
			t.Errorf("class %s: Counts=%d, class count=%d", c.Key, res.Counts[c.Key], c.Count)
		}
		if !sort.StringsAreSorted(c.BookIDs) {
			t.Errorf("class %s: book ids not sorted", c.Key)
		}
		if c.Definition == "" || c.Kind == "" {
			t.Errorf("class %s: missing kind or definition", c.Key)
		}
		if len(c.BookIDs) > 0 {
			got[c.Key] = c.BookIDs
		}
	}
	for k := range want {
		sort.Strings(want[k])
	}
	// Every class except read_error must be exercised by the seed.
	for _, def := range creditCensusClasses {
		if def.Key == ccReadError {
			continue
		}
		if len(want[def.Key]) == 0 {
			t.Errorf("seed exercises no book for class %s", def.Key)
		}
	}
	for k, ids := range want {
		if !reflect.DeepEqual(got[k], ids) {
			t.Errorf("class %s: got %v, want %v", k, got[k], ids)
		}
	}
	for k, ids := range got {
		if _, ok := want[k]; !ok {
			t.Errorf("class %s: unexpected books %v", k, ids)
		}
	}
	// Clean books land in no class at all.
	for _, c := range res.Classes {
		for _, id := range c.BookIDs {
			if len(id) > 6 && id[:6] == "clean-" {
				t.Errorf("clean book %s landed in class %s", id, c.Key)
			}
		}
	}
	if res.BooksScanned != len(s.books) || res.BooksListed != len(s.books) {
		t.Errorf("scanned=%d listed=%d, want %d", res.BooksScanned, res.BooksListed, len(s.books))
	}

	// DisagreementBooks counts distinct books in disagreement classes only.
	dis := map[string]bool{}
	for _, def := range creditCensusClasses {
		if def.Kind == creditKindDisagreement {
			for _, id := range want[def.Key] {
				dis[id] = true
			}
		}
	}
	if res.DisagreementBooks != len(dis) {
		t.Errorf("DisagreementBooks = %d, want %d", res.DisagreementBooks, len(dis))
	}
}

// The same seed classified sequentially and with a wide pool must agree: the
// accumulator is the only shared state, and -race watches it.
func TestCreditCensus_ConcurrencyMatchesSequential(t *testing.T) {
	s, _ := seedCreditCensus(t)
	seq, err := runCensusForTest(t, s, 1)
	if err != nil {
		t.Fatalf("sequential: %v", err)
	}
	par, err := runCensusForTest(t, s, 16)
	if err != nil {
		t.Fatalf("parallel: %v", err)
	}
	if !reflect.DeepEqual(seq.Counts, par.Counts) || !reflect.DeepEqual(seq.Classes, par.Classes) {
		t.Fatalf("parallel result differs from sequential:\nseq=%v\npar=%v", seq.Counts, par.Counts)
	}
}

// A read failure must never shorten a count: the book goes to read_error, the
// result is persisted as incomplete, and the run fails.
func TestCreditCensus_ReadErrorMarksIncomplete(t *testing.T) {
	s, _ := seedCreditCensus(t)
	s.failBook = "b-joinempty"
	res, err := runCensusForTest(t, s, 4)
	if err == nil {
		t.Fatal("census with an unreadable book returned nil error")
	}
	if res == nil || res.Complete {
		t.Fatalf("result = %+v, want a persisted incomplete result", res)
	}
	if got := res.Counts[ccReadError]; got != 1 {
		t.Errorf("read_error count = %d, want 1", got)
	}
	if res.Error == "" {
		t.Error("incomplete result carries no error text")
	}
}

// A listed id whose row is gone (deleted mid-run) is counted as gone, not as
// a disagreement and not as an error.
func TestCreditCensus_GoneBook(t *testing.T) {
	s := newCensusFakeStore()
	s.authors[1] = database.Author{ID: 1, Name: "A Person"}
	s.books["b1"] = &database.Book{ID: "b1", AuthorID: intp(1)}
	s.joins["b1"] = authorsJoin("b1", [2]int{1, 0})
	s.listed = []string{"b1", "b-gone"}
	res, err := runCensusForTest(t, s, 2)
	if err != nil {
		t.Fatalf("census: %v", err)
	}
	if res.BooksGone != 1 || res.BooksScanned != 1 || !res.Complete {
		t.Fatalf("gone=%d scanned=%d complete=%t, want 1/1/true", res.BooksGone, res.BooksScanned, res.Complete)
	}
}

// The def is read-only: no write capability, no write resources, and it is in
// the plugin's registered set (the golden ledger in internal/server covers the
// production boot).
func TestCreditCensus_DefIsReadOnlyAndRegistered(t *testing.T) {
	def := creditCensusRegisteredDef(t, New(fakeDeps{}))
	if len(def.Writes) != 0 {
		t.Errorf("Writes = %v, want none", def.Writes)
	}
	for _, c := range def.Capabilities {
		if c != sdk.CapLibraryRead {
			t.Errorf("capability %v requested; the census is read only", c)
		}
	}
}

// End to end through the plugin's Run against a real PebbleStore: the result
// is persisted through the reporter, and the book rows are untouched.
func TestCreditCensus_RunOnPebblePersistsResultWithoutWrites(t *testing.T) {
	s := newRepairPebble(t)
	a := repairAuthor(t, s, "Robin Hobb")
	repairBook(t, s, "book-ok", &a.ID, a.ID)
	repairBook(t, s, "book-nojoin", &a.ID)

	before := map[string]*database.Book{}
	for _, id := range []string{"book-ok", "book-nojoin"} {
		b, err := s.GetBookByID(id)
		if err != nil || b == nil {
			t.Fatalf("GetBookByID(%s): %v", id, err)
		}
		before[id] = b
	}

	def := creditCensusRegisteredDef(t, New(fakeDeps{store: s}))
	rep := &censusResultReporter{}
	if err := def.Run(context.Background(), nil, rep); err != nil {
		t.Fatalf("Run: %v", err)
	}
	res, ok := rep.result.(*creditCensusResult)
	if !ok || res == nil {
		t.Fatalf("persisted result = %T, want *creditCensusResult", rep.result)
	}
	if !res.Complete || res.Counts[ccAuthorIDJoinEmpty] != 1 {
		t.Fatalf("complete=%t author_id_join_empty=%d, want true/1", res.Complete, res.Counts[ccAuthorIDJoinEmpty])
	}
	for _, c := range res.Classes {
		if c.Key == ccAuthorIDJoinEmpty && (len(c.BookIDs) != 1 || c.BookIDs[0] != "book-nojoin") {
			t.Errorf("author_id_join_empty books = %v, want [book-nojoin]", c.BookIDs)
		}
	}
	for id, b := range before {
		after, err := s.GetBookByID(id)
		if err != nil || after == nil {
			t.Fatalf("re-read %s: %v", id, err)
		}
		if !reflect.DeepEqual(b, after) {
			t.Errorf("book %s changed during a read-only census", id)
		}
		join, err := s.GetBookAuthors(id)
		if err != nil {
			t.Fatalf("GetBookAuthors(%s): %v", id, err)
		}
		if id == "book-nojoin" && len(join) != 0 {
			t.Errorf("census wrote a join for %s: %v", id, join)
		}
	}
}
