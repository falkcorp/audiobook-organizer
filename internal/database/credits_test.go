// file: internal/database/credits_test.go
// version: 1.0.0
// guid: a5a7e839-5ad1-49ad-8401-9428121f66e7
// last-edited: 2026-10-04

package database

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"

	"github.com/cockroachdb/pebble/v2"
)

// ---- pure helpers ----

func TestJoinCreditNames(t *testing.T) {
	cases := []struct {
		in   []string
		want string
	}{
		{nil, ""},
		{[]string{}, ""},
		{[]string{"  ", ""}, ""},
		{[]string{"A"}, "A"},
		{[]string{" A "}, "A"},
		{[]string{"A", "B"}, "A and B"},
		{[]string{"A", "", "B"}, "A and B"},
		{[]string{"A", "B", "C"}, "A, B and C"},
		{[]string{"A", "B", "C", "D"}, "A, B, C and D"},
		// A name that itself holds "and" or a comma is joined as-is: the
		// joined string is output only, never split back.
		{[]string{"Simon and Schuster Audio", "Le Guin, Ursula"}, "Simon and Schuster Audio and Le Guin, Ursula"},
	}
	for _, c := range cases {
		if got := JoinCreditNames(c.in); got != c.want {
			t.Errorf("JoinCreditNames(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestJoinCreditNamesABS(t *testing.T) {
	cases := []struct {
		in   []string
		want string
	}{
		{nil, ""},
		{[]string{"A"}, "A"},
		{[]string{"A", " ", "B"}, "A, B"},
		{[]string{"A", "B", "C"}, "A, B, C"},
	}
	for _, c := range cases {
		if got := JoinCreditNamesABS(c.in); got != c.want {
			t.Errorf("JoinCreditNamesABS(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func creditAuthorIDs(rows []BookAuthor) []int {
	out := make([]int, len(rows))
	for i, r := range rows {
		out[i] = r.AuthorID
	}
	return out
}

func positionsOfAuthors(rows []BookAuthor) []int {
	out := make([]int, len(rows))
	for i, r := range rows {
		out[i] = r.Position
	}
	return out
}

// The production shape found on 2026-10-04: an old copy path wrote A and B
// both at position 0, then an add-only apply appended the combined A+B row at
// max+1 = 1. Stored order is credit order, so the result must be A, B, A+B at
// 0, 1, 2 -- and it must be the same on every run.
func TestNormalizeBookAuthors_ProdShapeKeepsStoredOrder(t *testing.T) {
	const a, b, ab = 11, 12, 13
	in := []BookAuthor{
		{AuthorID: a, Role: "author", Position: 0},
		{AuthorID: b, Role: "author", Position: 0},
		{AuthorID: ab, Role: "author", Position: 1},
	}
	orig := append([]BookAuthor(nil), in...)
	for run := 0; run < 50; run++ {
		got := NormalizeBookAuthors(in)
		if ids := creditAuthorIDs(got); !reflect.DeepEqual(ids, []int{a, b, ab}) {
			t.Fatalf("run %d: order = %v, want [A B A+B]", run, ids)
		}
		if pos := positionsOfAuthors(got); !reflect.DeepEqual(pos, []int{0, 1, 2}) {
			t.Fatalf("run %d: positions = %v, want [0 1 2]", run, pos)
		}
	}
	if !reflect.DeepEqual(in, orig) {
		t.Errorf("input mutated: %+v", in)
	}
	// Idempotent: normalising canonical rows changes nothing.
	once := NormalizeBookAuthors(in)
	if twice := NormalizeBookAuthors(once); !reflect.DeepEqual(once, twice) {
		t.Errorf("not idempotent: %+v vs %+v", once, twice)
	}
}

func TestNormalizeBookAuthors_SortsGapsDuplicatesAndZeroIDs(t *testing.T) {
	in := []BookAuthor{
		{AuthorID: 3, Role: "co-author", Position: 7},
		{AuthorID: 1, Role: "author", Position: 2},
		{AuthorID: 0, Role: "author", Position: 0}, // no author: dropped
		{AuthorID: 2, Role: "editor", Position: 5},
		{AuthorID: 1, Role: "author", Position: 9},   // exact (author, role) repeat: dropped
		{AuthorID: 2, Role: "narrator", Position: 6}, // same person, another role: kept
		{AuthorID: -4, Role: "author", Position: 1},  // invalid: dropped
	}
	got := NormalizeBookAuthors(in)
	if ids := creditAuthorIDs(got); !reflect.DeepEqual(ids, []int{1, 2, 2, 3}) {
		t.Fatalf("order = %v, want [1 2 2 3]", ids)
	}
	if got[1].Role != "editor" || got[2].Role != "narrator" {
		t.Errorf("an author credited in two roles must keep both rows, got %+v", got)
	}
	if pos := positionsOfAuthors(got); !reflect.DeepEqual(pos, []int{0, 1, 2, 3}) {
		t.Fatalf("positions = %v", pos)
	}
	if got[0].Role != "author" {
		t.Errorf("the lowest-position row of a repeated author must win, got role %q", got[0].Role)
	}
	if out := NormalizeBookAuthors(nil); out == nil || len(out) != 0 {
		t.Errorf("nil input must give an empty, non-nil slice (encodes as [] not null), got %#v", out)
	}
}

func TestNormalizeBookNarrators(t *testing.T) {
	in := []BookNarrator{
		{NarratorID: 5, Role: "narrator", Position: 0},
		{NarratorID: 6, Role: "narrator", Position: 0},
		{NarratorID: 7, Role: "co-narrator", Position: 1},
		{NarratorID: 5, Role: "narrator", Position: 3},
		{NarratorID: 0, Position: 0},
	}
	got := NormalizeBookNarrators(in)
	var ids, pos []int
	for _, r := range got {
		ids = append(ids, r.NarratorID)
		pos = append(pos, r.Position)
	}
	if !reflect.DeepEqual(ids, []int{5, 6, 7}) || !reflect.DeepEqual(pos, []int{0, 1, 2}) {
		t.Fatalf("got ids %v positions %v", ids, pos)
	}
	if in[1].Position != 0 || len(in) != 5 {
		t.Errorf("input mutated: %+v", in)
	}
}

func TestBookCreditsAccessors(t *testing.T) {
	var empty BookCredits
	if _, ok := empty.PrimaryAuthor(); ok {
		t.Error("PrimaryAuthor on empty credits reported ok")
	}
	if _, ok := empty.PrimaryNarrator(); ok {
		t.Error("PrimaryNarrator on empty credits reported ok")
	}
	if len(empty.AuthorNames()) != 0 || len(empty.NarratorNames()) != 0 {
		t.Error("names of empty credits are not empty")
	}
	c := BookCredits{
		Authors: []CreditedAuthor{
			{Author: Author{ID: 1, Name: "Ann"}, Position: 0},
			{Author: Author{ID: 2, Name: "Bo"}, Position: 1},
		},
		Narrators: []CreditedNarrator{{Narrator: Narrator{ID: 9, Name: "Kate"}, Position: 0}},
	}
	if a, ok := c.PrimaryAuthor(); !ok || a.ID != 1 {
		t.Errorf("PrimaryAuthor = %+v, %v", a, ok)
	}
	if n, ok := c.PrimaryNarrator(); !ok || n.ID != 9 {
		t.Errorf("PrimaryNarrator = %+v, %v", n, ok)
	}
	if got := JoinCreditNames(c.AuthorNames()); got != "Ann and Bo" {
		t.Errorf("joined authors = %q", got)
	}
	if got := c.NarratorNames(); !reflect.DeepEqual(got, []string{"Kate"}) {
		t.Errorf("narrator names = %v", got)
	}
}

// ---- store ----

func newCreditsTestStore(t *testing.T) *PebbleStore {
	t.Helper()
	s, err := NewPebbleStoreInMemory("db")
	if err != nil {
		t.Fatalf("NewPebbleStoreInMemory: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func mustCreditAuthor(t *testing.T, s *PebbleStore, name string) *Author {
	t.Helper()
	a, err := s.CreateAuthor(name)
	if err != nil || a == nil {
		t.Fatalf("CreateAuthor(%q): %v", name, err)
	}
	return a
}

func mustNarrator(t *testing.T, s *PebbleStore, name string) *Narrator {
	t.Helper()
	n, err := s.CreateNarrator(name)
	if err != nil || n == nil {
		t.Fatalf("CreateNarrator(%q): %v", name, err)
	}
	return n
}

func mustCreditsBook(t *testing.T, s *PebbleStore, path string) *Book {
	t.Helper()
	b, err := s.CreateBook(&Book{Title: "Credits " + path, FilePath: path})
	if err != nil {
		t.Fatalf("CreateBook: %v", err)
	}
	return b
}

// seedRawCredits writes credit rows straight to Pebble, bypassing the
// normalising write path, the way rows written by older builds sit on disk.
func seedRawCredits(t *testing.T, s *PebbleStore, key []byte, rows any) {
	t.Helper()
	data, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.db.Set(key, data, pebble.Sync); err != nil {
		t.Fatal(err)
	}
}

func TestSetBookAuthors_NormalisesStoredRows(t *testing.T) {
	s := newCreditsTestStore(t)
	b := mustCreditsBook(t, s, "/tmp/credits-set-authors.m4b")
	a, bb, ab := mustCreditAuthor(t, s, "Ann A"), mustCreditAuthor(t, s, "Bob B"), mustCreditAuthor(t, s, "Ann A, Bob B")
	if err := s.SetBookAuthors(b.ID, []BookAuthor{
		{AuthorID: a.ID, Role: "author", Position: 0},
		{AuthorID: bb.ID, Role: "author", Position: 0},
		{AuthorID: ab.ID, Role: "author", Position: 1},
		{AuthorID: a.ID, Role: "author", Position: 4},
	}); err != nil {
		t.Fatal(err)
	}
	rows, err := s.GetBookAuthors(b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if ids := creditAuthorIDs(rows); !reflect.DeepEqual(ids, []int{a.ID, bb.ID, ab.ID}) {
		t.Fatalf("stored order = %v", ids)
	}
	if pos := positionsOfAuthors(rows); !reflect.DeepEqual(pos, []int{0, 1, 2}) {
		t.Fatalf("stored positions = %v", pos)
	}
	for _, r := range rows {
		if r.BookID != b.ID {
			t.Errorf("row not stamped with book id: %+v", r)
		}
	}
}

func TestModifyBookAuthors_ReturnsNormalisedRows(t *testing.T) {
	s := newCreditsTestStore(t)
	b := mustCreditsBook(t, s, "/tmp/credits-modify-authors.m4b")
	a, bb := mustCreditAuthor(t, s, "Ann A"), mustCreditAuthor(t, s, "Bob B")
	got, err := s.ModifyBookAuthors(b.ID, func(cur []BookAuthor) ([]BookAuthor, error) {
		return append(cur, BookAuthor{AuthorID: bb.ID, Position: 3}, BookAuthor{AuthorID: a.ID, Position: 3}), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if ids, pos := creditAuthorIDs(got), positionsOfAuthors(got); !reflect.DeepEqual(ids, []int{bb.ID, a.ID}) || !reflect.DeepEqual(pos, []int{0, 1}) {
		t.Fatalf("returned ids %v positions %v", ids, pos)
	}
}

func TestSetBookNarrators_NormalisesStoredRows(t *testing.T) {
	s := newCreditsTestStore(t)
	b := mustCreditsBook(t, s, "/tmp/credits-set-narrators.m4b")
	n1, n2 := mustNarrator(t, s, "Kate Reading"), mustNarrator(t, s, "Michael Kramer")
	if err := s.SetBookNarrators(b.ID, []BookNarrator{
		{NarratorID: n2.ID, Position: 5},
		{NarratorID: n1.ID, Position: 2},
		{NarratorID: n2.ID, Position: 9},
	}); err != nil {
		t.Fatal(err)
	}
	rows, err := s.GetBookNarrators(b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].NarratorID != n1.ID || rows[1].NarratorID != n2.ID || rows[1].Position != 1 {
		t.Fatalf("stored rows = %+v", rows)
	}
}

func TestModifyBookNarrators_NoLostUpdates(t *testing.T) {
	s := newCreditsTestStore(t)
	b := mustCreditsBook(t, s, "/tmp/credits-narrators-race.m4b")
	const workers = 16
	ids := make([]int, workers)
	for i := range ids {
		ids[i] = mustNarrator(t, s, fmt.Sprintf("Narrator %02d", i)).ID
	}
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			_, err := s.ModifyBookNarrators(b.ID, func(cur []BookNarrator) ([]BookNarrator, error) {
				return append(cur, BookNarrator{NarratorID: id, Role: "narrator", Position: len(cur)}), nil
			})
			errs <- err
		}(ids[i])
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	rows, err := s.GetBookNarrators(b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != workers {
		t.Fatalf("got %d narrators after %d concurrent adds: lost update", len(rows), workers)
	}
	for i, r := range rows {
		if r.Position != i {
			t.Errorf("row %d has position %d", i, r.Position)
		}
	}
}

func TestModifyBookNarrators_SkipAndError(t *testing.T) {
	s := newCreditsTestStore(t)
	b := mustCreditsBook(t, s, "/tmp/credits-narrators-skip.m4b")
	n := mustNarrator(t, s, "Kate Reading")
	if err := s.SetBookNarrators(b.ID, []BookNarrator{{NarratorID: n.ID}}); err != nil {
		t.Fatal(err)
	}
	got, err := s.ModifyBookNarrators(b.ID, func([]BookNarrator) ([]BookNarrator, error) {
		return nil, ErrSkipBookNarratorsWrite
	})
	if err != nil || len(got) != 1 || got[0].NarratorID != n.ID {
		t.Fatalf("skip: got %+v, %v", got, err)
	}
	boom := errors.New("boom")
	if _, err := s.ModifyBookNarrators(b.ID, func([]BookNarrator) ([]BookNarrator, error) {
		return nil, boom
	}); !errors.Is(err, boom) {
		t.Fatalf("error not returned: %v", err)
	}
	rows, _ := s.GetBookNarrators(b.ID)
	if len(rows) != 1 {
		t.Fatalf("an aborted modify wrote: %+v", rows)
	}
}

type reindexRecorder struct {
	mu      sync.Mutex
	reindex []string
}

func (r *reindexRecorder) BooksChanged(...string) {}
func (r *reindexRecorder) BooksNeedReindex(ids ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reindex = append(r.reindex, ids...)
}
func (r *reindexRecorder) AuthorRenamed(int) {}
func (r *reindexRecorder) SeriesRenamed(int) {}

func (r *reindexRecorder) saw(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, x := range r.reindex {
		if x == id {
			return true
		}
	}
	return false
}

func TestModifyBookCredits_OneBatchWithRow(t *testing.T) {
	s := newCreditsTestStore(t)
	b := mustCreditsBook(t, s, "/tmp/credits-modify.m4b")
	a, bb := mustCreditAuthor(t, s, "Ann A"), mustCreditAuthor(t, s, "Bob B")
	n := mustNarrator(t, s, "Kate Reading")
	rec := &reindexRecorder{}
	s.SetChangeObserver(rec)

	// When the row's batch is about to commit, the credits must not be in
	// Pebble yet: they ride in that batch, not in a separate earlier write.
	hookRan := false
	bookWriteBatchPreCommitHook = func(op, id string, _ *pebble.Batch) {
		if op != "update" || id != b.ID {
			return
		}
		hookRan = true
		if rows, _ := s.GetBookAuthors(b.ID); len(rows) != 0 {
			t.Errorf("authors written before the row batch committed: %+v", rows)
		}
		if rows, _ := s.GetBookNarrators(b.ID); len(rows) != 0 {
			t.Errorf("narrators written before the row batch committed: %+v", rows)
		}
	}
	t.Cleanup(func() { bookWriteBatchPreCommitHook = nil })

	updated, credits, err := s.ModifyBookCredits(b.ID, func(book *Book, c *BookCreditsEdit) error {
		book.Title = "Renamed"
		c.Authors = []BookAuthor{{AuthorID: bb.ID, Position: 1}, {AuthorID: a.ID, Position: 1}}
		c.Narrators = []BookNarrator{{NarratorID: n.ID, Position: 4}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !hookRan {
		t.Fatal("the row write never reached the batch commit")
	}
	if updated == nil || updated.Title != "Renamed" {
		t.Fatalf("updated = %+v", updated)
	}
	if ids := creditAuthorIDs(credits.Authors); !reflect.DeepEqual(ids, []int{bb.ID, a.ID}) || credits.Authors[1].Position != 1 {
		t.Fatalf("returned authors = %+v", credits.Authors)
	}
	stored, _ := s.GetBookAuthors(b.ID)
	if !reflect.DeepEqual(creditAuthorIDs(stored), []int{bb.ID, a.ID}) || !reflect.DeepEqual(positionsOfAuthors(stored), []int{0, 1}) {
		t.Fatalf("stored authors = %+v", stored)
	}
	narr, _ := s.GetBookNarrators(b.ID)
	if len(narr) != 1 || narr[0].NarratorID != n.ID || narr[0].Position != 0 || narr[0].BookID != b.ID {
		t.Fatalf("stored narrators = %+v", narr)
	}
	row, _ := s.GetBookByID(b.ID)
	if row.Title != "Renamed" {
		t.Errorf("row title = %q", row.Title)
	}
	if !rec.saw(b.ID) {
		t.Error("a credits write did not queue the book for search reindexing")
	}
}

func TestModifyBookCredits_SkipErrorMissingAndNoNarratorSync(t *testing.T) {
	s := newCreditsTestStore(t)
	b := mustCreditsBook(t, s, "/tmp/credits-modify-skip.m4b")
	a := mustCreditAuthor(t, s, "Ann A")
	if err := s.SetBookAuthors(b.ID, []BookAuthor{{AuthorID: a.ID}}); err != nil {
		t.Fatal(err)
	}

	got, credits, err := s.ModifyBookCredits(b.ID, func(book *Book, c *BookCreditsEdit) error {
		c.Authors = nil
		return ErrSkipBookWrite
	})
	if err != nil || got == nil || len(credits.Authors) != 1 {
		t.Fatalf("skip: %+v %+v %v", got, credits, err)
	}

	boom := errors.New("boom")
	if _, _, err := s.ModifyBookCredits(b.ID, func(book *Book, c *BookCreditsEdit) error {
		book.Title = "Never"
		c.Authors = nil
		return boom
	}); !errors.Is(err, boom) {
		t.Fatalf("error not returned: %v", err)
	}
	if rows, _ := s.GetBookAuthors(b.ID); len(rows) != 1 {
		t.Fatalf("an aborted modify changed the credits: %+v", rows)
	}
	if row, _ := s.GetBookByID(b.ID); row.Title == "Never" {
		t.Fatal("an aborted modify changed the row")
	}

	called := false
	if got, _, err := s.ModifyBookCredits("01NOSUCHBOOK0000000000000", func(*Book, *BookCreditsEdit) error {
		called = true
		return nil
	}); err != nil || got != nil || called {
		t.Fatalf("missing book: got %+v err %v called %v", got, err, called)
	}

	// The narrator list is whatever fn leaves: setting the column does not
	// run the column -> junction sync that ModifyBook runs.
	if _, _, err := s.ModifyBookCredits(b.ID, func(book *Book, c *BookCreditsEdit) error {
		book.Narrator = strp("Kate Reading, Michael Kramer")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if rows, _ := s.GetBookNarrators(b.ID); len(rows) != 0 {
		t.Fatalf("ModifyBookCredits ran the narrator sync: %+v", rows)
	}
}

// ModifyBookCredits and the single-list writers take the same stripes, so
// concurrent additions through either never drop each other.
func TestModifyBookCredits_ConcurrentWithModifyBookAuthors(t *testing.T) {
	s := newCreditsTestStore(t)
	b := mustCreditsBook(t, s, "/tmp/credits-modify-race.m4b")
	const each = 8
	var viaCredits, viaAuthors []int
	for i := 0; i < each; i++ {
		viaCredits = append(viaCredits, mustCreditAuthor(t, s, fmt.Sprintf("Credits Writer %02d", i)).ID)
		viaAuthors = append(viaAuthors, mustCreditAuthor(t, s, fmt.Sprintf("Authors Writer %02d", i)).ID)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2*each)
	for i := 0; i < each; i++ {
		wg.Add(2)
		go func(id int) {
			defer wg.Done()
			_, _, err := s.ModifyBookCredits(b.ID, func(_ *Book, c *BookCreditsEdit) error {
				c.Authors = append(c.Authors, BookAuthor{AuthorID: id, Position: len(c.Authors)})
				return nil
			})
			errs <- err
		}(viaCredits[i])
		go func(id int) {
			defer wg.Done()
			_, err := s.ModifyBookAuthors(b.ID, func(cur []BookAuthor) ([]BookAuthor, error) {
				return append(cur, BookAuthor{AuthorID: id, Position: len(cur)}), nil
			})
			errs <- err
		}(viaAuthors[i])
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	rows, _ := s.GetBookAuthors(b.ID)
	if len(rows) != 2*each {
		t.Fatalf("got %d authors after %d concurrent adds: lost update", len(rows), 2*each)
	}
}

func TestGetBookCredits(t *testing.T) {
	s := newCreditsTestStore(t)
	legacy := mustCreditsBook(t, s, "/tmp/credits-get-legacy.m4b")
	empty := mustCreditsBook(t, s, "/tmp/credits-get-empty.m4b")
	a, bb, ab := mustCreditAuthor(t, s, "Ann A"), mustCreditAuthor(t, s, "Bob B"), mustCreditAuthor(t, s, "Ann A, Bob B")
	n1, n2 := mustNarrator(t, s, "Kate Reading"), mustNarrator(t, s, "Michael Kramer")

	// A tombstoned id that redirects to Ann, and an id that resolves to
	// nothing at all.
	const tombstoned, dangling = 90001, 90002
	if err := s.CreateAuthorTombstone(tombstoned, a.ID); err != nil {
		t.Fatal(err)
	}
	// Rows as an older build left them: the prod "A @0, B @0, A+B @1"
	// shape, plus the tombstoned twin of A and a dangling id.
	seedRawCredits(t, s, bookAuthorsKey(legacy.ID), []BookAuthor{
		{BookID: legacy.ID, AuthorID: a.ID, Role: "author", Position: 0},
		{BookID: legacy.ID, AuthorID: bb.ID, Role: "author", Position: 0},
		{BookID: legacy.ID, AuthorID: dangling, Role: "author", Position: 0},
		{BookID: legacy.ID, AuthorID: ab.ID, Role: "author", Position: 1},
		{BookID: legacy.ID, AuthorID: tombstoned, Role: "author", Position: 2},
	})
	seedRawCredits(t, s, bookNarratorsKey(legacy.ID), []BookNarrator{
		{BookID: legacy.ID, NarratorID: n2.ID, Position: 3},
		{BookID: legacy.ID, NarratorID: n1.ID, Position: 1},
	})

	for run := 0; run < 20; run++ {
		got, err := s.GetBookCredits(context.Background(), []string{legacy.ID, empty.ID, legacy.ID, "01NOSUCHBOOK0000000000000"})
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 3 {
			t.Fatalf("got %d entries, want 3 (duplicates folded, missing book present)", len(got))
		}
		c := got[legacy.ID]
		if names := c.AuthorNames(); !reflect.DeepEqual(names, []string{"Ann A", "Bob B", "Ann A, Bob B"}) {
			t.Fatalf("run %d: authors = %v", run, names)
		}
		for i, ca := range c.Authors {
			if ca.Position != i {
				t.Errorf("author %d has position %d", i, ca.Position)
			}
		}
		if p, ok := c.PrimaryAuthor(); !ok || p.ID != a.ID {
			t.Errorf("primary = %+v", p)
		}
		if names := c.NarratorNames(); !reflect.DeepEqual(names, []string{"Kate Reading", "Michael Kramer"}) {
			t.Fatalf("narrators = %v", names)
		}
		if JoinCreditNames(c.AuthorNames()) != "Ann A, Bob B and Ann A, Bob B" {
			t.Errorf("joined = %q", JoinCreditNames(c.AuthorNames()))
		}
		for _, id := range []string{empty.ID, "01NOSUCHBOOK0000000000000"} {
			e, ok := got[id]
			if !ok || len(e.Authors) != 0 || len(e.Narrators) != 0 || e.Authors == nil {
				t.Errorf("%s: %+v (want present, empty, non-nil lists)", id, e)
			}
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.GetBookCredits(ctx, []string{legacy.ID}); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled context: err = %v", err)
	}
}
