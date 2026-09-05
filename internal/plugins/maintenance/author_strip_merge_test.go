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
// one pure-junk row that OWNS A BOOK, one publisher row (out of scope), one
// numbered row whose residue matches nothing and which co-credits a book with
// the real author, a second unmatched row that co-credits ANOTHER book with the
// first (so both of that book's credits are doomed), and an ambiguous row whose
// residue names two existing authors.
func stripFixture() []database.Author {
	return []database.Author{
		{ID: 1, Name: "Kevin J Anderson"},
		{ID: 2, Name: "001-147 Kevin J Anderson"},
		{ID: 3, Name: "Track 01"},
		{ID: 4, Name: "Penguin Books"},
		{ID: 5, Name: "001_Head of the Dragon"},
		{ID: 6, Name: "002_Head of the Dragon"},
		{ID: 7, Name: "Jane Roe"},
		{ID: 8, Name: "Jane Roe"},
		{ID: 9, Name: "003_Jane Roe"},
	}
}

func newStripPlugin(authors []database.Author, calls *stripMergeCalls) *Plugin {
	calls.setAuthors = map[string][]database.BookAuthor{}
	calls.updated = map[string]*database.Book{}
	junkBookAuthorID := 3
	// currentPrimary mirrors the real store: once the op has rewritten a
	// book's primary, the BookCore projection a later author lookup returns
	// carries the NEW primary, not the fixture's seed.
	currentPrimary := func(bookID string, seed int) *int {
		if b, ok := calls.updated[bookID]; ok {
			return b.AuthorID
		}
		return &seed
	}
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
				return []database.BookCore{
					{ID: "bk-title", Title: "Head of the Dragon", AuthorID: &id},
					{ID: "bk-twice", Title: "Head of the Dragon 2", AuthorID: currentPrimary("bk-twice", 5)},
				}, nil
			}
			if authorID == 6 {
				return []database.BookCore{{ID: "bk-twice", Title: "Head of the Dragon 2", AuthorID: currentPrimary("bk-twice", 5)}}, nil
			}
			return nil, nil
		},
		GetBookAuthorsFunc: func(bookID string) ([]database.BookAuthor, error) {
			// Stateful: once the op has written a book's credits, later reads
			// see that write, as the real store does. Without this a second
			// doomed co-credit looks identical in apply and dry-run mode and
			// the parity test would pass for the wrong reason.
			if written, ok := calls.setAuthors[bookID]; ok {
				return written, nil
			}
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
			case "bk-twice":
				return []database.BookAuthor{
					{BookID: bookID, AuthorID: 5, Role: "author"},
					{BookID: bookID, AuthorID: 6, Role: "author", Position: 1},
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
			case "bk-twice":
				if b, ok := calls.updated[id]; ok {
					c := *b
					return &c, nil
				}
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
	// Still in scope: the publisher row stays, the merge still happens, and
	// the ambiguous row (two "Jane Roe" targets) is neither deleted nor merged.
	if containsInt(calls.deleted, 4) {
		t.Error("delete_unmatched deleted the out-of-scope publisher row")
	}
	if !containsInt(calls.deleted, 2) {
		t.Error("delete_unmatched suppressed the merge")
	}
	if containsInt(calls.deleted, 9) {
		t.Error("delete_unmatched deleted the AMBIGUOUS row 9; its residue names real authors")
	}
	if !strings.Contains(summary, "ambiguous=1") {
		t.Errorf("summary should still count the ambiguous row: %s", summary)
	}
	// bk-junk loses its only credit (row 3); bk-twice loses both (rows 5 and
	// 6); bk-title keeps author 1. Two books, counted once each.
	if !strings.Contains(summary, "books-left-authorless=2") {
		t.Errorf("summary should report exactly two books left authorless: %s", summary)
	}
	// bk-twice ends with no credits and no primary, whichever order rows 5
	// and 6 were processed in.
	if got := calls.setAuthors["bk-twice"]; len(got) != 0 {
		t.Errorf("bk-twice still has credits after both doomed rows went: %+v", got)
	}
	if b := calls.updated["bk-twice"]; b == nil || b.AuthorID != nil {
		t.Errorf("bk-twice primary should be cleared once both doomed rows are gone: %+v", b)
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
	if !strings.Contains(summary, "stripped-no-target=2") {
		t.Errorf("summary should still count the bucket: %s", summary)
	}
	// 🔴 PARITY WITH THE APPLY. bk-twice is credited by two doomed rows. The
	// apply sees row 5's removal before it judges row 6; the dry run never
	// does. Both must still report the book, or the operator's number is low
	// in the unsafe direction.
	if !strings.Contains(summary, "books-left-authorless=2") {
		t.Errorf("dry run should predict the same authorless count as the apply (2): %s", summary)
	}
	if !strings.Contains(summary, "deleted=0") || !strings.Contains(summary, "books-touched=0") {
		t.Errorf("dry run must report nothing applied: %s", summary)
	}
}

// 🔴 A FAILED PRIMARY REWRITE MUST ABORT THE DELETE. The junction is already
// rewritten; deleting the author row anyway leaves book.AuthorID pointing at
// nothing while the summary says failed=0 — the 2026-08-24 incident with a
// green report on top.
func TestAuthorStripMerge_PrimaryRewriteFailureKeepsAuthorRow(t *testing.T) {
	calls := &stripMergeCalls{}
	p := newStripPlugin(stripFixture(), calls)
	store := p.deps.OpsStore().(*database.MockStore)
	store.UpdateBookFunc = func(id string, b *database.Book) (*database.Book, error) {
		if id == "bk-junk" {
			return nil, context.DeadlineExceeded
		}
		calls.updated[id] = b
		return b, nil
	}
	rep := &summaryReporter{}
	if err := p.runAuthorStripMerge(context.Background(), json.RawMessage(`{"apply":true}`), rep); err != nil {
		t.Fatalf("runAuthorStripMerge: %v", err)
	}
	if containsInt(calls.deleted, 3) {
		t.Fatal("junk author 3 was deleted although its book's primary rewrite failed")
	}
	summary := rep.summary(t)
	if !strings.Contains(summary, "failed=1") {
		t.Errorf("the failed rewrite must be counted: %s", summary)
	}
	// The other rows are unaffected: the merge still lands.
	if !containsInt(calls.deleted, 2) {
		t.Error("an unrelated failure suppressed the merge")
	}
}

// 🔴 PARTIAL WORK IS STILL WORK. Row 5 rewrites bk-title, then fails on
// bk-twice. That first rewrite happened and must be in books-touched; the
// report must not shrink because the row later errored.
func TestAuthorStripMerge_PartialWorkOnErroringRowIsReported(t *testing.T) {
	calls := &stripMergeCalls{}
	p := newStripPlugin(stripFixture(), calls)
	store := p.deps.OpsStore().(*database.MockStore)
	store.UpdateBookFunc = func(id string, b *database.Book) (*database.Book, error) {
		if id == "bk-twice" && !containsInt(calls.deleted, 5) {
			return nil, context.DeadlineExceeded
		}
		calls.updated[id] = b
		return b, nil
	}
	rep := &summaryReporter{}
	if err := p.runAuthorStripMerge(context.Background(), json.RawMessage(`{"apply":true,"delete_unmatched":true}`), rep); err != nil {
		t.Fatalf("runAuthorStripMerge: %v", err)
	}
	if containsInt(calls.deleted, 5) {
		t.Fatal("row 5 was deleted although its second book's rewrite failed")
	}
	summary := rep.summary(t)
	// bk-merge (merge) + bk-junk (junk) + bk-title (row 5, before the
	// failure) + bk-twice (row 6) = 4 books rewritten; row 5 counts as failed.
	if !strings.Contains(summary, "books-touched=4") || !strings.Contains(summary, "failed=1") {
		t.Errorf("want books-touched=4 failed=1, got: %s", summary)
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
