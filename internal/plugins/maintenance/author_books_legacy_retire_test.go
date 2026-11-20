// file: internal/plugins/maintenance/author_books_legacy_retire_test.go
// version: 0.1.0
// guid: 7b3e9d15-2c84-4a6f-b1d7-0e5a9c2f8b64
// last-edited: 2026-11-20

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
)

// legacyAuthorBooksKey is the retired family's key for (author, book), as
// builds before F5-18j wrote it.
func legacyAuthorBooksKey(authorID int, bookID string) string {
	return "author_books_legacy:" + strconv.Itoa(authorID) + ":" + bookID
}

// legacyRetireFixture is a store whose one book is credited to its AuthorID,
// with two legacy rows an older build left (one for the book, one for a book
// since deleted). uncredited adds a second book whose AuthorID has no credit
// row: the legacy row is the last index entry naming that author for it, so a
// live run must refuse. The op runs through a Store-embedding decorator, as
// production does.
func legacyRetireFixture(t *testing.T, uncredited bool) (p *Plugin, inner *database.PebbleStore, legacy []string, blocker string) {
	t.Helper()
	inner, err := database.NewPebbleStoreInMemory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = inner.Close() })
	inner.WaitForWarmup()
	bruce, err := inner.CreateAuthor("Bruce Wayne")
	if err != nil {
		t.Fatal(err)
	}
	yes := true
	yearOne, err := inner.CreateBook(&database.Book{Title: "Batman: Year One", FilePath: "/lib/batman/year-one", AuthorID: &bruce.ID, IsPrimaryVersion: &yes})
	if err != nil {
		t.Fatal(err)
	}
	if err := inner.SetBookAuthors(yearOne.ID, []database.BookAuthor{{BookID: yearOne.ID, AuthorID: bruce.ID, Role: "author"}}); err != nil {
		t.Fatal(err)
	}
	legacy = []string{legacyAuthorBooksKey(bruce.ID, yearOne.ID), legacyAuthorBooksKey(bruce.ID, "01GONEBOOKGONEBOOKGONEBOOK")}
	if uncredited {
		joker, err := inner.CreateAuthor("The Joker")
		if err != nil {
			t.Fatal(err)
		}
		killingJoke, err := inner.CreateBook(&database.Book{Title: "Batman: The Killing Joke", FilePath: "/lib/batman/killing-joke", AuthorID: &joker.ID, IsPrimaryVersion: &yes})
		if err != nil {
			t.Fatal(err)
		}
		blocker = killingJoke.ID
		legacy = append(legacy, legacyAuthorBooksKey(joker.ID, killingJoke.ID))
	}
	for _, k := range legacy {
		if err := inner.SetRaw(k, []byte{}); err != nil {
			t.Fatal(err)
		}
	}
	return New(fakeDeps{store: &opChangeDecorator{Store: inner}}), inner, legacy, blocker
}

func legacyRowsLeft(t *testing.T, s *database.PebbleStore, keys []string) int {
	t.Helper()
	n := 0
	for _, k := range keys {
		if keyPresent(t, s, k) {
			n++
		}
	}
	return n
}

// Preview is the default: no params, {} and dry_run=true count the rows and
// delete nothing; only dry_run=false deletes them. The next live run finds
// nothing and says so, tagged no-op (idempotent).
func TestAuthorBooksLegacyRetire_PreviewDeletesNothingAndOnlyDryRunFalseDeletes(t *testing.T) {
	for _, raw := range []string{``, `{}`, `{"dry_run":true}`, `{"dryRun":true}`} {
		t.Run("preview "+raw, func(t *testing.T) {
			p, inner, legacy, _ := legacyRetireFixture(t, false)
			rep := &p6AttrReporter{}
			if err := p.runAuthorBooksLegacyRetire(context.Background(), json.RawMessage(raw), rep); err != nil {
				t.Fatalf("preview: %v", err)
			}
			if left := legacyRowsLeft(t, inner, legacy); left != len(legacy) {
				t.Fatalf("params %q deleted %d legacy rows: a preview must delete nothing", raw, len(legacy)-left)
			}
			joined := joinLines(rep)
			if !strings.Contains(joined, "0 blocking") || !strings.Contains(joined, "2 legacy rows") ||
				!strings.Contains(joined, "preview, nothing deleted (run with dry_run=false") {
				t.Fatalf("the preview must report the counts; log:\n%s", joined)
			}
		})
	}

	t.Run("dry_run=false deletes, then is a tagged no-op", func(t *testing.T) {
		p, inner, legacy, _ := legacyRetireFixture(t, false)
		rep := &p6AttrReporter{}
		if err := p.runAuthorBooksLegacyRetire(context.Background(), json.RawMessage(`{"dry_run":false}`), rep); err != nil {
			t.Fatalf("retire: %v", err)
		}
		if left := legacyRowsLeft(t, inner, legacy); left != 0 {
			t.Fatalf("dry_run=false left %d legacy rows", left)
		}
		if !strings.Contains(joinLines(rep), "2 legacy rows; 2 deleted") {
			t.Fatalf("the run must report what it deleted; log:\n%s", joinLines(rep))
		}
		if rep.noOpLines() != 0 {
			t.Fatal("a run that deleted rows was tagged no-op")
		}

		rep = &p6AttrReporter{}
		if err := p.runAuthorBooksLegacyRetire(context.Background(), json.RawMessage(`{"dry_run":false}`), rep); err != nil {
			t.Fatalf("second retire: %v", err)
		}
		if rep.noOpLines() != 1 {
			t.Fatalf("a re-run over a retired store found nothing and must say so, tagged: %+v", rep.lines)
		}
	})
}

// A book whose AuthorID is not in its credits blocks the delete: the live run
// deletes nothing, fails with ErrAuthorBooksLegacyRetireRefused and names the
// book; the preview over the same store succeeds and says a live run would
// refuse.
func TestAuthorBooksLegacyRetire_RefusesAndReportsUncreditedBooks(t *testing.T) {
	p, inner, legacy, blocker := legacyRetireFixture(t, true)

	rep := &p6AttrReporter{}
	err := p.runAuthorBooksLegacyRetire(context.Background(), json.RawMessage(`{"dry_run":false}`), rep)
	if !errors.Is(err, database.ErrAuthorBooksLegacyRetireRefused) {
		t.Fatalf("live run over an uncredited book: err = %v, want ErrAuthorBooksLegacyRetireRefused", err)
	}
	if left := legacyRowsLeft(t, inner, legacy); left != len(legacy) {
		t.Fatalf("a refused run deleted %d legacy rows; it must delete none", len(legacy)-left)
	}
	joined := joinLines(rep)
	if !strings.Contains(joined, "1 blocking (no credit row 1") || !strings.Contains(joined, "refused, nothing deleted") {
		t.Fatalf("the refusal must report the counts; log:\n%s", joined)
	}
	if !strings.Contains(joined, "blocking: ") || !strings.Contains(joined, blocker) {
		t.Fatalf("the refusal must name the blocking book %s; log:\n%s", blocker, joined)
	}

	rep = &p6AttrReporter{}
	if err := p.runAuthorBooksLegacyRetire(context.Background(), json.RawMessage(`{}`), rep); err != nil {
		t.Fatalf("a preview that finds blocking books reports them and succeeds: %v", err)
	}
	joined = joinLines(rep)
	if !strings.Contains(joined, "a live run would refuse") || !strings.Contains(joined, blocker) {
		t.Fatalf("the preview must say a live run would refuse, naming the book; log:\n%s", joined)
	}
	if left := legacyRowsLeft(t, inner, legacy); left != len(legacy) {
		t.Fatal("the preview deleted legacy rows")
	}
}

// A store without the capability (any store but Pebble) is refused up front,
// not run as an empty success.
func TestAuthorBooksLegacyRetire_RefusesAStoreWithoutTheCapability(t *testing.T) {
	p := New(fakeDeps{store: &opChangeDecorator{}})
	err := p.runAuthorBooksLegacyRetire(context.Background(), json.RawMessage(`{"dry_run":false}`), &p6AttrReporter{})
	if err == nil || !strings.Contains(err.Error(), "no author_books_legacy: rows to retire") {
		t.Fatalf("err = %v, want the unsupported-store refusal", err)
	}
}
