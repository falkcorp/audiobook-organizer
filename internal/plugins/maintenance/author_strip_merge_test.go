// file: internal/plugins/maintenance/author_strip_merge_test.go
// version: 1.1.0
// guid: 8f5723a5-46b7-409b-901e-e791fdd71228
// last-edited: 2026-09-04

package maintenance

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

type stripMergeCalls struct {
	deleted    []int
	renamed    []int
	tombstones [][2]int
	setAuthors map[string][]database.BookAuthor
	updated    map[string]*database.Book
}

// stripFixture: one real author, one numbered row carrying that real author,
// one pure-junk row that OWNS A BOOK, one publisher row (out of scope), and one
// numbered row whose residue matches nothing and which co-credits a book with
// the real author.
func stripFixture() []database.Author {
	return []database.Author{
		{ID: 1, Name: "Kevin J Anderson"},
		{ID: 2, Name: "001-147 Kevin J Anderson"},
		{ID: 3, Name: "Track 01"},
		{ID: 4, Name: "Penguin Books"},
		{ID: 5, Name: "001_Head of the Dragon"},
	}
}

func newStripPlugin(authors []database.Author, calls *stripMergeCalls) *Plugin {
	calls.setAuthors = map[string][]database.BookAuthor{}
	calls.updated = map[string]*database.Book{}
	junkBookAuthorID := 3
	store := &database.MockStore{
		GetAllAuthorsFunc: func() ([]database.Author, error) { return authors, nil },
		GetAuthorByIDFunc: func(id int) (*database.Author, error) {
			for _, a := range authors {
				if a.ID == id {
					c := a
					return &c, nil
				}
			}
			return nil, nil
		},
		GetBooksByAuthorIDWithRoleFunc: func(authorID int) ([]database.BookCore, error) {
			if authorID == 3 {
				return []database.BookCore{{ID: "bk-junk", Title: "A Book", AuthorID: &junkBookAuthorID}}, nil
			}
			if authorID == 2 {
				id := 2
				return []database.BookCore{{ID: "bk-merge", Title: "Another", AuthorID: &id}}, nil
			}
			if authorID == 5 {
				id := 5
				return []database.BookCore{{ID: "bk-title", Title: "Head of the Dragon", AuthorID: &id}}, nil
			}
			return nil, nil
		},
		GetBookAuthorsFunc: func(bookID string) ([]database.BookAuthor, error) {
			switch bookID {
			case "bk-junk":
				return []database.BookAuthor{{BookID: bookID, AuthorID: 3, Role: "author"}}, nil
			case "bk-merge":
				return []database.BookAuthor{{BookID: bookID, AuthorID: 2, Role: "author"}}, nil
			case "bk-title":
				return []database.BookAuthor{
					{BookID: bookID, AuthorID: 5, Role: "author"},
					{BookID: bookID, AuthorID: 1, Role: "author", Position: 1},
				}, nil
			}
			return nil, nil
		},
		SetBookAuthorsFunc: func(bookID string, as []database.BookAuthor) error {
			calls.setAuthors[bookID] = as
			return nil
		},
		GetBookByIDFunc: func(id string) (*database.Book, error) {
			switch id {
			case "bk-junk":
				a := 3
				return &database.Book{ID: id, AuthorID: &a}, nil
			case "bk-merge":
				a := 2
				return &database.Book{ID: id, AuthorID: &a}, nil
			case "bk-title":
				a := 5
				return &database.Book{ID: id, AuthorID: &a}, nil
			}
			return nil, nil
		},
		UpdateBookFunc: func(id string, b *database.Book) (*database.Book, error) {
			calls.updated[id] = b
			return b, nil
		},
		DeleteAuthorFunc: func(id int) error {
			calls.deleted = append(calls.deleted, id)
			return nil
		},
		UpdateAuthorNameFunc: func(id int, _ string) error {
			calls.renamed = append(calls.renamed, id)
			return nil
		},
		CreateAuthorTombstoneFunc: func(oldID, canonicalID int) error {
			calls.tombstones = append(calls.tombstones, [2]int{oldID, canonicalID})
			return nil
		},
	}
	return &Plugin{deps: &fakeDeps{store: store}}
}

// summaryReporter is a fakeReporter whose logger is captured, so a test can
// read the "author-strip-merge done" summary line the op reports.
type summaryReporter struct {
	fakeReporter
	buf bytes.Buffer
}

func (r *summaryReporter) Logger() *slog.Logger {
	return slog.New(slog.NewTextHandler(&r.buf, nil))
}

func (r *summaryReporter) summary(t *testing.T) string {
	t.Helper()
	for _, line := range strings.Split(r.buf.String(), "\n") {
		if strings.Contains(line, "author-strip-merge done") {
			return line
		}
	}
	t.Fatalf("no summary line logged; log was:\n%s", r.buf.String())
	return ""
}

func runStripMerge(t *testing.T, params string) *stripMergeCalls {
	t.Helper()
	calls, _ := runStripMergeWithSummary(t, params)
	return calls
}

func runStripMergeWithSummary(t *testing.T, params string) (*stripMergeCalls, string) {
	t.Helper()
	calls := &stripMergeCalls{}
	p := newStripPlugin(stripFixture(), calls)
	var raw json.RawMessage
	if params != "" {
		raw = json.RawMessage(params)
	}
	rep := &summaryReporter{}
	if err := p.runAuthorStripMerge(context.Background(), raw, rep); err != nil {
		t.Fatalf("runAuthorStripMerge: %v", err)
	}
	return calls, rep.summary(t)
}

// 🔴 REPORT-ONLY MUST NOT WRITE. Asserts on SILENCE, which is the only thing
// that distinguishes "reported correctly" from "deleted anyway".
func TestAuthorStripMerge_DryRunWritesNothing(t *testing.T) {
	calls := runStripMerge(t, "")
	if len(calls.deleted) != 0 {
		t.Errorf("dry run deleted authors %v", calls.deleted)
	}
	if len(calls.setAuthors) != 0 || len(calls.updated) != 0 {
		t.Errorf("dry run wrote book links: setAuthors=%v updated=%v", calls.setAuthors, calls.updated)
	}
}

func TestAuthorStripMerge_MergesNumberedRowIntoRealAuthor(t *testing.T) {
	calls := runStripMerge(t, `{"apply":true}`)
	if !containsInt(calls.deleted, 2) {
		t.Errorf("numbered row 2 was not merged away; deleted=%v", calls.deleted)
	}
	got := calls.setAuthors["bk-merge"]
	if len(got) != 1 || got[0].AuthorID != 1 {
		t.Errorf("book was not relinked to author 1: %+v", got)
	}
	if len(calls.tombstones) != 1 || calls.tombstones[0] != [2]int{2, 1} {
		t.Errorf("expected tombstone 2->1, got %v", calls.tombstones)
	}
}

// 🔴 THE DANGLING-AuthorID GUARD. store.DeleteAuthor sweeps the junction but
// leaves book.AuthorID pointing at the deleted row -- the exact mechanism that
// stranded ~212 authors' books on 2026-08-24. Deleting a junk author MUST clear
// the denormalized primary too.
func TestAuthorStripMerge_JunkDeleteClearsPrimaryAuthorID(t *testing.T) {
	calls := runStripMerge(t, `{"apply":true}`)
	if !containsInt(calls.deleted, 3) {
		t.Fatalf("junk author 3 was not deleted; deleted=%v", calls.deleted)
	}
	if got, ok := calls.setAuthors["bk-junk"]; !ok || len(got) != 0 {
		t.Errorf("junk credit not removed from book_authors: %+v (present=%v)", got, ok)
	}
	updated, ok := calls.updated["bk-junk"]
	if !ok {
		t.Fatal("book.AuthorID was never rewritten -- it still points at the deleted author")
	}
	if updated.AuthorID != nil {
		t.Errorf("book.AuthorID = %v, want nil after its only author was deleted", *updated.AuthorID)
	}
}

// Publisher shrapnel is a DIFFERENT defect and some of those rows name real
// people. This op must count them and leave them alone.
func TestAuthorStripMerge_LeavesOutOfScopeRowsAlone(t *testing.T) {
	calls := runStripMerge(t, `{"apply":true}`)
	if containsInt(calls.deleted, 4) {
		t.Error("deleted the publisher row 'Penguin Books', which is out of this op's scope")
	}
}

// A stripped name matching no existing author is left alone BY DEFAULT, and
// never renamed: "001_Head of the Dragon" is a book title, and renaming it
// would launder an obviously-corrupt row into a plausible one.
func TestAuthorStripMerge_DoesNotRenameWhenNoTargetExists(t *testing.T) {
	calls := runStripMerge(t, `{"apply":true}`)
	if containsInt(calls.deleted, 5) {
		t.Error("row 5 was deleted; without delete_unmatched it should have been left alone")
	}
	if len(calls.renamed) != 0 {
		t.Errorf("rows %v were renamed; this op must never rename", calls.renamed)
	}
}

// 🔴 delete_unmatched IS A DELETE, NOT A RENAME. The row goes away through the
// dangling-AuthorID-safe path: its credit is removed, the surviving co-author
// is promoted to the book's primary, and nothing is ever renamed.
func TestAuthorStripMerge_DeleteUnmatchedDeletesNoTargetRow(t *testing.T) {
	calls, summary := runStripMergeWithSummary(t, `{"apply":true,"delete_unmatched":true}`)
	if !containsInt(calls.deleted, 5) {
		t.Fatalf("row 5 was not deleted under delete_unmatched; deleted=%v", calls.deleted)
	}
	if len(calls.renamed) != 0 {
		t.Errorf("rows %v were renamed; delete_unmatched must delete, never rename", calls.renamed)
	}
	if got := calls.setAuthors["bk-title"]; len(got) != 1 || got[0].AuthorID != 1 || got[0].Position != 0 {
		t.Errorf("bk-title credits after unlink = %+v, want only author 1 at position 0", got)
	}
	updated, ok := calls.updated["bk-title"]
	if !ok || updated.AuthorID == nil || *updated.AuthorID != 1 {
		t.Errorf("bk-title primary was not promoted to the surviving author 1: %+v (present=%v)", updated, ok)
	}
	// Still in scope: the publisher row stays, the merge still happens.
	if containsInt(calls.deleted, 4) {
		t.Error("delete_unmatched deleted the out-of-scope publisher row")
	}
	if !containsInt(calls.deleted, 2) {
		t.Error("delete_unmatched suppressed the merge")
	}
	// bk-junk loses its only credit (row 3); bk-title keeps author 1.
	if !strings.Contains(summary, "books-left-authorless=1") {
		t.Errorf("summary should report exactly one book left authorless: %s", summary)
	}
}

// 🔴 A REPORT-ONLY RUN WITH delete_unmatched MUST NOT WRITE — and must still
// report the same authorless count the apply would, because it is the number
// the operator reads before deciding.
func TestAuthorStripMerge_DeleteUnmatchedDryRunReportsWithoutWriting(t *testing.T) {
	calls, summary := runStripMergeWithSummary(t, `{"delete_unmatched":true}`)
	if len(calls.deleted) != 0 || len(calls.renamed) != 0 {
		t.Errorf("dry run wrote authors: deleted=%v renamed=%v", calls.deleted, calls.renamed)
	}
	if len(calls.setAuthors) != 0 || len(calls.updated) != 0 {
		t.Errorf("dry run wrote book links: setAuthors=%v updated=%v", calls.setAuthors, calls.updated)
	}
	if !strings.Contains(summary, "stripped-no-target=1") {
		t.Errorf("summary should still count the bucket: %s", summary)
	}
	if !strings.Contains(summary, "books-left-authorless=1") {
		t.Errorf("dry run should predict the same authorless count as the apply: %s", summary)
	}
	if !strings.Contains(summary, "deleted=0") || !strings.Contains(summary, "books-touched=0") {
		t.Errorf("dry run must report nothing applied: %s", summary)
	}
}

// delete_unmatched composes with delete_junk=false: only the unmatched rows go.
func TestAuthorStripMerge_DeleteUnmatchedWithoutJunk(t *testing.T) {
	calls := runStripMerge(t, `{"apply":true,"delete_junk":false,"delete_unmatched":true}`)
	if containsInt(calls.deleted, 3) {
		t.Error("delete_junk=false still deleted the junk row")
	}
	if !containsInt(calls.deleted, 5) {
		t.Error("delete_unmatched did not delete the unmatched row when delete_junk=false")
	}
}

func TestAuthorStripMerge_DeleteJunkFalseKeepsJunk(t *testing.T) {
	calls := runStripMerge(t, `{"apply":true,"delete_junk":false}`)
	if containsInt(calls.deleted, 3) {
		t.Error("delete_junk=false still deleted the junk row")
	}
	if !containsInt(calls.deleted, 2) {
		t.Error("delete_junk=false suppressed the merge as well; it should only stop deletions")
	}
}

func containsInt(xs []int, v int) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}
