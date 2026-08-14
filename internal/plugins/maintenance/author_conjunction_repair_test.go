// file: internal/plugins/maintenance/author_conjunction_repair_test.go
// version: 1.0.0
// guid: 8e35b7d2-1c40-4f96-a2e7-5b9d0c68a341
// last-edited: 2026-08-14

package maintenance

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// conjRepairWrites records every mutating call the op makes, so a dry-run test
// can assert on silence rather than on a return value.
type conjRepairWrites struct {
	setBookAuthors  map[string][]database.BookAuthor
	deletedAuthors  []int
	renamedAuthors  map[int]string
	updatedBooks    []string
	getBookAuthorsN int
}

// newConjRepairPlugin wires a MockStore around a fixed author table and a
// book<->author join table.
func newConjRepairPlugin(
	authors []database.Author,
	booksByAuthor map[int][]database.BookCore,
	joins map[string][]database.BookAuthor,
	w *conjRepairWrites,
) *Plugin {
	w.setBookAuthors = map[string][]database.BookAuthor{}
	w.renamedAuthors = map[int]string{}

	store := &database.MockStore{
		GetAllAuthorsFunc: func() ([]database.Author, error) { return authors, nil },
		GetAuthorByNameFunc: func(name string) (*database.Author, error) {
			for i := range authors {
				if authors[i].Name == name {
					return &authors[i], nil
				}
			}
			return nil, nil
		},
		GetBooksByAuthorIDWithRoleFunc: func(authorID int) ([]database.BookCore, error) {
			return booksByAuthor[authorID], nil
		},
		GetBookAuthorsFunc: func(bookID string) ([]database.BookAuthor, error) {
			w.getBookAuthorsN++
			return joins[bookID], nil
		},
		SetBookAuthorsFunc: func(bookID string, ba []database.BookAuthor) error {
			w.setBookAuthors[bookID] = ba
			return nil
		},
		DeleteAuthorFunc: func(id int) error {
			w.deletedAuthors = append(w.deletedAuthors, id)
			return nil
		},
		UpdateAuthorNameFunc: func(id int, name string) error {
			w.renamedAuthors[id] = name
			return nil
		},
	}
	return New(&fakeDeps{store: store})
}

func conjRepairParams(dryRun bool) json.RawMessage {
	b, _ := json.Marshal(authorConjunctionRepairParams{DryRun: &dryRun})
	return b
}

// TestAuthorConjunctionRepair_MergesIntoExistingTwin covers the majority case:
// 31 of the 48 production rows have a correctly-named twin already in the table.
// The stranded row must lose its book links to the twin and then be deleted.
//
// The assertion that matters is on SetBookAuthors, not on Book.AuthorID: every
// stranded row sits at position 1+ of a credit list, so a merge that only
// rewrote the denormalized primary would report success and move nothing.
func TestAuthorConjunctionRepair_MergesIntoExistingTwin(t *testing.T) {
	authors := []database.Author{
		{ID: 43620, Name: "Paul McGann"},
		{ID: 46751, Name: "& Conrad Westmaas"},
		{ID: 44001, Name: "Conrad Westmaas"},
	}
	primary := 43620
	booksByAuthor := map[int][]database.BookCore{
		46751: {{ID: "b1", AuthorID: &primary}},
	}
	joins := map[string][]database.BookAuthor{
		"b1": {
			{BookID: "b1", AuthorID: 43620, Role: "author", Position: 0},
			{BookID: "b1", AuthorID: 46751, Role: "author", Position: 1},
		},
	}

	var w conjRepairWrites
	p := newConjRepairPlugin(authors, booksByAuthor, joins, &w)
	if err := p.runAuthorConjunctionRepair(context.Background(), conjRepairParams(false), &fakeReporter{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, ok := w.setBookAuthors["b1"]
	if !ok {
		t.Fatalf("SetBookAuthors was never called for b1 — the join slice carries the link, so the merge moved nothing")
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 author links on b1, got %d: %+v", len(got), got)
	}
	for _, ba := range got {
		if ba.AuthorID == 46751 {
			t.Errorf("stranded author 46751 still linked to b1: %+v", got)
		}
	}
	foundTwin := false
	for _, ba := range got {
		if ba.AuthorID == 44001 {
			foundTwin = true
			if ba.Role != "author" {
				t.Errorf("role not preserved on relink: got %q, want %q", ba.Role, "author")
			}
		}
	}
	if !foundTwin {
		t.Errorf("twin author 44001 was not linked to b1: %+v", got)
	}

	if len(w.deletedAuthors) != 1 || w.deletedAuthors[0] != 46751 {
		t.Errorf("expected author 46751 deleted, got %v", w.deletedAuthors)
	}
	if len(w.renamedAuthors) != 0 {
		t.Errorf("merge path must not rename anything, got %v", w.renamedAuthors)
	}
}

// TestAuthorConjunctionRepair_RenamesWhenNoTwin covers the other 17 rows. A
// rename keeps the row id, so no book link is touched at all — asserting that
// SetBookAuthors stays silent is the point of the test.
func TestAuthorConjunctionRepair_RenamesWhenNoTwin(t *testing.T) {
	authors := []database.Author{
		{ID: 46751, Name: "& Conrad Westmaas"},
	}
	var w conjRepairWrites
	p := newConjRepairPlugin(authors, nil, nil, &w)
	if err := p.runAuthorConjunctionRepair(context.Background(), conjRepairParams(false), &fakeReporter{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got := w.renamedAuthors[46751]; got != "Conrad Westmaas" {
		t.Errorf("expected rename to %q, got %q", "Conrad Westmaas", got)
	}
	if len(w.deletedAuthors) != 0 {
		t.Errorf("rename path must not delete anything, got %v", w.deletedAuthors)
	}
	if len(w.setBookAuthors) != 0 {
		t.Errorf("rename keeps the row id — no book link should be rewritten, got %v", w.setBookAuthors)
	}
}

// TestAuthorConjunctionRepair_DryRunWritesNothing pins the default. dry_run is
// the zero-value behaviour (nil pointer → true), and the merge path is
// destructive: it deletes an author row.
func TestAuthorConjunctionRepair_DryRunWritesNothing(t *testing.T) {
	authors := []database.Author{
		{ID: 46751, Name: "& Conrad Westmaas"},
		{ID: 44001, Name: "Conrad Westmaas"},
		{ID: 46411, Name: "& Patricia Merrick"}, // no twin → would rename
	}
	booksByAuthor := map[int][]database.BookCore{
		46751: {{ID: "b1"}},
	}
	joins := map[string][]database.BookAuthor{
		"b1": {{BookID: "b1", AuthorID: 46751, Role: "author", Position: 0}},
	}

	for _, tc := range []struct {
		name   string
		params json.RawMessage
	}{
		{"explicit dry_run=true", conjRepairParams(true)},
		{"omitted params default to dry run", nil},
		{"empty object defaults to dry run", json.RawMessage(`{}`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var w conjRepairWrites
			p := newConjRepairPlugin(authors, booksByAuthor, joins, &w)
			if err := p.runAuthorConjunctionRepair(context.Background(), tc.params, &fakeReporter{}); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(w.setBookAuthors) != 0 {
				t.Errorf("dry run wrote book authors: %v", w.setBookAuthors)
			}
			if len(w.deletedAuthors) != 0 {
				t.Errorf("dry run deleted authors: %v", w.deletedAuthors)
			}
			if len(w.renamedAuthors) != 0 {
				t.Errorf("dry run renamed authors: %v", w.renamedAuthors)
			}
			// It must still have done the READ work — a dry run that skips the
			// scan reports "nothing to do" and looks identical to a clean table.
			if w.getBookAuthorsN == 0 {
				t.Errorf("dry run never inspected book authors; it cannot have planned anything")
			}
		})
	}
}

// TestAuthorConjunctionRepair_LeavesNonAmpersandRowsAlone is the guard that
// keeps this op narrower than dedup.NormalizeAuthorName.
//
// The "and " rows are book-title fragments ("So Long, and Thanks for All the
// Fish"), not stranded conjunctions; renaming them yields something that is
// still not an author but no longer looks broken. The "&#169" rows are
// decapitated HTML entities from a copyright string. All are a different defect
// and must survive this op untouched.
func TestAuthorConjunctionRepair_LeavesNonAmpersandRowsAlone(t *testing.T) {
	authors := []database.Author{
		{ID: 46595, Name: "and Thanks for All the Fish"},
		{ID: 46989, Name: "and the Farm Boy (DBY)"},
		{ID: 47193, Name: "and Make Better Decisions"},
		{ID: 46583, Name: "&#169"},
		{ID: 51870, Name: "&#169;2013 by HarperCollinsPublishers"},
		{ID: 38542, Name: "Dan Simmons"},
		{ID: 44444, Name: "Anders Bergman"},
	}
	var w conjRepairWrites
	p := newConjRepairPlugin(authors, nil, nil, &w)
	if err := p.runAuthorConjunctionRepair(context.Background(), conjRepairParams(false), &fakeReporter{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(w.renamedAuthors) != 0 {
		t.Errorf("no row here is a stranded conjunction; got renames %v", w.renamedAuthors)
	}
	if len(w.deletedAuthors) != 0 {
		t.Errorf("no row here should be deleted; got %v", w.deletedAuthors)
	}
	if len(w.setBookAuthors) != 0 {
		t.Errorf("no book link should be rewritten; got %v", w.setBookAuthors)
	}
}
