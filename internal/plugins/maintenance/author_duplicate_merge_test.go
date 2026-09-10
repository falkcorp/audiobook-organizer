// file: internal/plugins/maintenance/author_duplicate_merge_test.go
// version: 1.0.0
// guid: 47ea834f-d1ec-4c9f-a8c5-e26626311d2e
// last-edited: 2026-09-10

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// authorDupWrites records every mutating call the op makes, so a dry-run test can
// assert on SILENCE rather than on a reported count. A dry run that reported
// "would merge 3" while deleting three rows would pass any assertion made on the
// return value alone.
type authorDupWrites struct {
	setBookAuthors map[string][]database.BookAuthor
	deletedAuthors []int
	updatedBooks   []string
	ledger         []database.OperationChange
}

func (w *authorDupWrites) total() int {
	return len(w.setBookAuthors) + len(w.deletedAuthors) + len(w.updatedBooks)
}

// ledgerFor returns the ledger rows recorded for one book id.
func (w *authorDupWrites) ledgerFor(bookID string) []database.OperationChange {
	var out []database.OperationChange
	for _, c := range w.ledger {
		if c.BookID == bookID {
			out = append(out, c)
		}
	}
	return out
}

// authorDupFixture is two independent duplicate groups plus one unrelated row.
//
//	"Raymond L. Weil"  -> ids 1 (2 books) and 2 (1 book)   <- the listed group
//	"Karen Joy Fowler" -> ids 3 (1 book) and 4 (1 book)    <- NOT listed, must not move
//	"Brandon Sanderson"-> id 5, no duplicate
//
// The second group exists so a test can prove the op acts on the allowlist and
// not on "every duplicate it can see".
//
// The duplicate rows differ by CASE, not by interior spacing: util.NormalizeAuthor
// is `strings.ToLower(strings.TrimSpace(...))` today and does not collapse runs of
// whitespace (TODO.md L3790 tracks that fix). The op groups by NormalizeAuthor at
// call time, so it picks up a wider normalizer automatically when one lands.
func authorDupFixture() (
	authors []database.Author,
	booksByAuthor map[int][]database.BookCore,
	joins map[string][]database.BookAuthor,
	bookCounts map[int]int,
	refCounts map[int]int,
) {
	authors = []database.Author{
		{ID: 1, Name: "Raymond L. Weil"},
		{ID: 2, Name: "RAYMOND L. WEIL"},
		{ID: 3, Name: "Karen Joy Fowler"},
		{ID: 4, Name: "KAREN JOY FOWLER"},
		{ID: 5, Name: "Brandon Sanderson"},
	}
	booksByAuthor = map[int][]database.BookCore{
		1: {{ID: "b1"}, {ID: "b2"}},
		2: {{ID: "b3"}},
		3: {{ID: "b4"}},
		4: {{ID: "b5"}},
		5: {{ID: "b6"}},
	}
	joins = map[string][]database.BookAuthor{
		"b1": {{BookID: "b1", AuthorID: 1, Role: "author", Position: 0}},
		"b2": {{BookID: "b2", AuthorID: 1, Role: "author", Position: 0}},
		"b3": {{BookID: "b3", AuthorID: 2, Role: "author", Position: 0}},
		"b4": {{BookID: "b4", AuthorID: 3, Role: "author", Position: 0}},
		"b5": {{BookID: "b5", AuthorID: 4, Role: "author", Position: 0}},
		"b6": {{BookID: "b6", AuthorID: 5, Role: "author", Position: 0}},
	}
	bookCounts = map[int]int{1: 2, 2: 1, 3: 1, 4: 1, 5: 1}
	// refCounts mirrors bookCounts here: nothing is trashed, non-primary, or a
	// junction-only credit. TestAuthorDuplicateMerge_RefusesDeleteWhileStillReferenced
	// overrides it to make them diverge.
	refCounts = map[int]int{1: 2, 2: 1, 3: 1, 4: 1, 5: 1}
	return
}

// newAuthorDupPlugin wires a MockStore over the fixture and records every write.
func newAuthorDupPlugin(
	authors []database.Author,
	booksByAuthor map[int][]database.BookCore,
	joins map[string][]database.BookAuthor,
	bookCounts, refCounts map[int]int,
	w *authorDupWrites,
) *Plugin {
	w.setBookAuthors = map[string][]database.BookAuthor{}
	store := &database.MockStore{
		GetAllAuthorsFunc:              func() ([]database.Author, error) { return authors, nil },
		GetAllAuthorBookCountsFunc:     func() (map[int]int, error) { return bookCounts, nil },
		GetAllAuthorBookRefCountsFunc:  func() (map[int]int, error) { return refCounts, nil },
		GetBooksByAuthorIDWithRoleFunc: func(id int) ([]database.BookCore, error) { return booksByAuthor[id], nil },
		GetBookAuthorsFunc:             func(bookID string) ([]database.BookAuthor, error) { return joins[bookID], nil },
		SetBookAuthorsFunc: func(bookID string, ba []database.BookAuthor) error {
			w.setBookAuthors[bookID] = ba
			return nil
		},
		DeleteAuthorFunc: func(id int) error {
			w.deletedAuthors = append(w.deletedAuthors, id)
			return nil
		},
		UpdateBookFunc: func(id string, b *database.Book) (*database.Book, error) {
			w.updatedBooks = append(w.updatedBooks, id)
			return b, nil
		},
		CreateOperationChangeFunc: func(c *database.OperationChange) error {
			w.ledger = append(w.ledger, *c)
			return nil
		},
	}
	return New(&fakeDeps{store: store})
}

func runAuthorDup(t *testing.T, params string, w *authorDupWrites) {
	t.Helper()
	authors, books, joins, counts, refs := authorDupFixture()
	p := newAuthorDupPlugin(authors, books, joins, counts, refs, w)
	var raw json.RawMessage
	if params != "" {
		raw = json.RawMessage(params)
	}
	if err := p.runAuthorDuplicateMerge(context.Background(), raw, &fakeReporter{}); err != nil {
		t.Fatalf("runAuthorDuplicateMerge(%s): %v", params, err)
	}
}

// 🔴 DRY RUN MUST WRITE NOTHING — not a book, not an author row, not a ledger row.
// This op deletes author rows from a production library and an author's name lives
// only in the row it deletes, so the default has to be inert.
func TestAuthorDuplicateMerge_DryRunDefaultTrue(t *testing.T) {
	var w authorDupWrites
	// No dry_run key at all: the default must be the harmless one.
	runAuthorDup(t, `{"names":["Raymond L. Weil"]}`, &w)
	if w.total() != 0 {
		t.Fatalf("dry run wrote: setBookAuthors=%v deletedAuthors=%v updatedBooks=%v",
			w.setBookAuthors, w.deletedAuthors, w.updatedBooks)
	}
	if len(w.ledger) != 0 {
		t.Fatalf("dry run wrote %d undo-ledger rows; a run that changed nothing must journal nothing", len(w.ledger))
	}

	// And explicitly, since a client may send the flag rather than omit it.
	w = authorDupWrites{}
	runAuthorDup(t, `{"names":["Raymond L. Weil"],"dry_run":true}`, &w)
	if w.total() != 0 || len(w.ledger) != 0 {
		t.Fatalf("dry_run=true wrote: %+v", w)
	}
}

// 🔴 THE ANTI-LAUNDERING GUARD, half one: an empty allowlist means NOTHING, never
// "everything". The fixture holds two real duplicate groups; both must survive.
func TestAuthorDuplicateMerge_EmptyNamesIsNoop(t *testing.T) {
	for _, params := range []string{``, `{}`, `{"names":[]}`, `{"names":null}`, `{"names":[],"dry_run":false}`, `{"names":["  "],"dry_run":false}`} {
		var w authorDupWrites
		runAuthorDup(t, params, &w)
		if w.total() != 0 || len(w.ledger) != 0 {
			t.Fatalf("params %q merged something with no names listed: %+v", params, w)
		}
	}
}

// 🔴 THE ANTI-LAUNDERING GUARD, half two: only the LISTED group moves. The
// Karen Joy Fowler pair is an equally obvious duplicate and must be untouched,
// because the operator did not name it.
func TestAuthorDuplicateMerge_MergesListedGroupOnly(t *testing.T) {
	var w authorDupWrites
	runAuthorDup(t, `{"names":["Raymond L. Weil"],"dry_run":false}`, &w)

	if len(w.deletedAuthors) != 1 || w.deletedAuthors[0] != 2 {
		t.Fatalf("expected only author 2 deleted, got %v", w.deletedAuthors)
	}
	if _, moved := w.setBookAuthors["b3"]; !moved {
		t.Fatalf("book b3 was not relinked onto the canonical row; writes=%v", w.setBookAuthors)
	}
	for _, untouched := range []string{"b4", "b5", "b6"} {
		if _, moved := w.setBookAuthors[untouched]; moved {
			t.Fatalf("book %s belongs to an UNLISTED group and must not be rewritten", untouched)
		}
	}
	// b3's link must now name the canonical author, not the deleted one.
	for _, ba := range w.setBookAuthors["b3"] {
		if ba.AuthorID == 2 {
			t.Fatalf("b3 still links the deleted author row: %+v", w.setBookAuthors["b3"])
		}
	}
}

// A wrong name -- one no live author normalizes to -- touches nothing and is not
// an error. An operator's typo must not become a merge and must not abort a run.
func TestAuthorDuplicateMerge_UnknownNameTouchesNothing(t *testing.T) {
	var w authorDupWrites
	runAuthorDup(t, `{"names":["Raymund Weyl","Nobody At All"],"dry_run":false}`, &w)
	if w.total() != 0 || len(w.ledger) != 0 {
		t.Fatalf("a name matching no live author changed something: %+v", w)
	}
}

// The keeper is the row with the most books, so the merge rewrites as few links
// as possible and keeps the row users have been seeing. Author 1 has two books
// and author 2 has one, so 1 survives even though both ids are live.
func TestAuthorDuplicateMerge_CanonicalIsHighestBookCount(t *testing.T) {
	var w authorDupWrites
	// Normalization is what matches, so the operator's spelling need not equal
	// either row's spelling.
	runAuthorDup(t, `{"names":["  raymond l. weil  "],"dry_run":false}`, &w)
	if len(w.deletedAuthors) != 1 || w.deletedAuthors[0] != 2 {
		t.Fatalf("the row with the most books must survive; deleted=%v", w.deletedAuthors)
	}
}

// 🔴 EVERY MOVED BOOK GETS A LEDGER ROW. A mutation with no journal row is a
// defect: `git revert` restores the code, not the data.
func TestAuthorDuplicateMerge_JournalsOneRowPerMovedBook(t *testing.T) {
	var w authorDupWrites
	runAuthorDup(t, `{"names":["Raymond L. Weil"],"dry_run":false}`, &w)

	rows := w.ledgerFor("b3")
	if len(rows) != 1 {
		t.Fatalf("expected exactly 1 undo-ledger row for the one moved book, got %d (%+v)", len(rows), w.ledger)
	}
	if rows[0].ChangeType != "metadata_update" || rows[0].FieldName != "author_id" {
		t.Fatalf("unexpected ledger row shape: %+v", rows[0])
	}
	if rows[0].OldValue != "2" || rows[0].NewValue != "1" {
		t.Fatalf("ledger row must name the ids it moved between, got old=%q new=%q", rows[0].OldValue, rows[0].NewValue)
	}
	// Plus one row recording the deleted author, whose NAME survives nowhere else.
	var deleteRows int
	for _, c := range w.ledger {
		if c.ChangeType == "author_delete" {
			deleteRows++
			if c.OldValue != "2:RAYMOND L. WEIL" {
				t.Fatalf("author_delete row must preserve the deleted name, got %q", c.OldValue)
			}
		}
	}
	if deleteRows != 1 {
		t.Fatalf("expected 1 author_delete ledger row, got %d", deleteRows)
	}
	// No books were moved for the unlisted group, so no rows exist for them.
	for _, id := range []string{"b4", "b5", "b6"} {
		if got := w.ledgerFor(id); len(got) != 0 {
			t.Fatalf("ledger rows written for untouched book %s: %+v", id, got)
		}
	}
}

// 🔴 THE DELETE GUARD. Author 2 is referenced by three books in the UNFILTERED
// count but the merge can only see one -- the other two are trashed, non-primary,
// or junction-only credits. Deleting the row would strand them behind an id that
// no longer exists, and the name is then unrecoverable. The row must be held back
// entirely: no relink, no delete.
func TestAuthorDuplicateMerge_RefusesDeleteWhileStillReferenced(t *testing.T) {
	authors, books, joins, counts, refs := authorDupFixture()
	refs[2] = 3 // display counter still says 1; the unfiltered truth is 3

	var w authorDupWrites
	p := newAuthorDupPlugin(authors, books, joins, counts, refs, &w)
	if err := p.runAuthorDuplicateMerge(context.Background(),
		json.RawMessage(`{"names":["Raymond L. Weil"],"dry_run":false}`), &fakeReporter{}); err != nil {
		t.Fatalf("runAuthorDuplicateMerge: %v", err)
	}
	if len(w.deletedAuthors) != 0 {
		t.Fatalf("deleted %v while books the merge cannot move still reference it", w.deletedAuthors)
	}
	if w.total() != 0 {
		t.Fatalf("held-back row must not be partially merged either: %+v", w)
	}

	// And the dry run must hold back exactly the same row -- a guard that applied
	// only on the write path would make the dry run a lie.
	w = authorDupWrites{}
	p = newAuthorDupPlugin(authors, books, joins, counts, refs, &w)
	if err := p.runAuthorDuplicateMerge(context.Background(),
		json.RawMessage(`{"names":["Raymond L. Weil"]}`), &fakeReporter{}); err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if w.total() != 0 {
		t.Fatalf("dry run wrote: %+v", w)
	}
}

// The guard fails CLOSED: a store that cannot answer the unfiltered question
// aborts rather than falling back to the display counter, because that fallback
// is exactly the bug it exists to prevent.
func TestAuthorDuplicateMerge_AbortsWhenRefCountUnavailable(t *testing.T) {
	authors, _, _, counts, _ := authorDupFixture()
	store := &database.MockStore{
		GetAllAuthorsFunc:          func() ([]database.Author, error) { return authors, nil },
		GetAllAuthorBookCountsFunc: func() (map[int]int, error) { return counts, nil },
		GetAllAuthorBookRefCountsFunc: func() (map[int]int, error) {
			return nil, fmt.Errorf("scan truncated")
		},
		DeleteAuthorFunc: func(int) error {
			t.Fatal("deleted an author after the unfiltered ref count failed")
			return nil
		},
	}
	p := New(&fakeDeps{store: store})
	err := p.runAuthorDuplicateMerge(context.Background(),
		json.RawMessage(`{"names":["Raymond L. Weil"],"dry_run":false}`), &fakeReporter{})
	if err == nil {
		t.Fatal("expected the op to abort when the unfiltered ref count is unavailable")
	}
}

// Two listed names that normalize to the same group are folded before processing.
// Without the fold the second pass finds a group of one and reports "no duplicate
// found" about the merge that had just succeeded.
func TestAuthorDuplicateMerge_DuplicateListedNamesProcessOnce(t *testing.T) {
	var w authorDupWrites
	runAuthorDup(t, `{"names":["Raymond L. Weil","RAYMOND L. WEIL"],"dry_run":false}`, &w)
	if len(w.deletedAuthors) != 1 {
		t.Fatalf("expected exactly one deletion across two spellings of one group, got %v", w.deletedAuthors)
	}
}

// A listed name with exactly one live row is a no-op, not an error: re-running the
// op after a successful apply is how an operator confirms it is done.
func TestAuthorDuplicateMerge_SingletonGroupIsNoop(t *testing.T) {
	var w authorDupWrites
	runAuthorDup(t, `{"names":["Brandon Sanderson"],"dry_run":false}`, &w)
	if w.total() != 0 || len(w.ledger) != 0 {
		t.Fatalf("a name with no duplicate changed something: %+v", w)
	}
}
