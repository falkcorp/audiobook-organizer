// file: internal/plugins/maintenance/author_strip_merge_test.go
// version: 1.3.0
// guid: 8f5723a5-46b7-409b-901e-e791fdd71228
// last-edited: 2026-09-25

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
	store.GetBooksByAuthorIDForRelinkFunc = relinkAwareBooks(store.GetBooksByAuthorIDWithRoleFunc, calls.setAuthors, func(id string) (*int, bool) {
		if b, ok := calls.updated[id]; ok {
			return b.AuthorID, true
		}
		return nil, false
	})
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

// placeholderFixture: author 3 ("Unknown") owns bk-junk through the shared
// newStripPlugin book mocks, so the merge into the canonical row is observable
// on a book. 50 is the canonical Unknown Author, 51 a duplicate of it.
func placeholderFixture() []database.Author {
	return []database.Author{
		{ID: 1, Name: "Kevin J Anderson"},
		{ID: 3, Name: "Unknown"},
		{ID: 50, Name: database.UnknownAuthorName},
		{ID: 51, Name: "Unknown Author"},
		{ID: 52, Name: "Various"},
		{ID: 53, Name: "n/a"},
		{ID: 54, Name: "Audiobook"},
		{ID: 55, Name: "None"},
		{ID: 56, Name: "Various Artists"},
		{ID: 60, Name: "Track01"},
		{ID: 61, Name: "Lords of the Sith_418m_07s_"},
		{ID: 62, Name: "14-25"},
		{ID: 63, Name: "Track_07"},
	}
}

func runPlaceholderStripMerge(t *testing.T, params string, canonical *database.Author) (*stripMergeCalls, string) {
	t.Helper()
	calls := &stripMergeCalls{}
	p := newStripPlugin(placeholderFixture(), calls)
	store := p.deps.(*fakeDeps).store.(*database.MockStore)
	store.GetAuthorByNameFunc = func(name string) (*database.Author, error) {
		if name == database.UnknownAuthorName && canonical != nil {
			c := *canonical
			return &c, nil
		}
		return nil, nil
	}
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

// 🔴 THE CANONICAL UNKNOWN AUTHOR IS NEVER DELETED. The 2026-09-25 dry run
// planned to delete it, and every placeholder with it, leaving 244 books
// authorless. Placeholders are merged INTO the canonical row instead.
func TestAuthorStripMerge_KeepsCanonicalUnknownAndMergesPlaceholders(t *testing.T) {
	canonical := &database.Author{ID: 50, Name: database.UnknownAuthorName}
	calls, summary := runPlaceholderStripMerge(t, `{"apply":true}`, canonical)
	if containsInt(calls.deleted, 50) {
		t.Fatalf("the canonical Unknown Author row 50 was deleted; deleted=%v", calls.deleted)
	}
	for _, id := range []int{3, 51, 52, 53, 54, 55, 56} {
		if !containsInt(calls.deleted, id) {
			t.Errorf("placeholder row %d was not merged away; deleted=%v", id, calls.deleted)
		}
		found := false
		for _, ts := range calls.tombstones {
			if ts == [2]int{id, 50} {
				found = true
			}
		}
		if !found {
			t.Errorf("placeholder row %d was not MERGED into 50 (no tombstone %d->50); tombstones=%v", id, id, calls.tombstones)
		}
	}
	got := calls.setAuthors["bk-junk"]
	if len(got) != 1 || got[0].AuthorID != 50 {
		t.Errorf("book credited to placeholder 3 should be relinked to the canonical row 50: %+v", got)
	}
	if b := calls.updated["bk-junk"]; b == nil || b.AuthorID == nil || *b.AuthorID != 50 {
		t.Errorf("bk-junk primary should move to the canonical row 50: %+v", b)
	}
	if !strings.Contains(summary, "placeholders=7") || !strings.Contains(summary, "canonical-unknown-id=50") {
		t.Errorf("summary should count 7 placeholders and name the canonical row: %s", summary)
	}
	if !strings.Contains(summary, "books-left-authorless=0") {
		t.Errorf("merging placeholders must leave no book authorless: %s", summary)
	}
}

// With no canonical row, placeholders are left alone: this op does not create
// author rows, and deleting them is the defect being fixed.
func TestAuthorStripMerge_NoCanonicalLeavesPlaceholdersAlone(t *testing.T) {
	calls, summary := runPlaceholderStripMerge(t, `{"apply":true}`, nil)
	for _, id := range []int{3, 50, 51, 52, 53, 54, 55, 56} {
		if containsInt(calls.deleted, id) {
			t.Errorf("placeholder row %d was deleted with no canonical row to merge into", id)
		}
	}
	if !strings.Contains(summary, "placeholders-no-canonical=8") {
		t.Errorf("summary should count 8 untouched placeholders: %s", summary)
	}
}

// Track and timecode numbering that CleanAuthorNameForCreation accepts
// ("Track01", "..._418m_07s_") is in scope as junk, as is bare "NN-NN".
func TestAuthorStripMerge_TrackAndTimecodeRowsAreJunk(t *testing.T) {
	canonical := &database.Author{ID: 50, Name: database.UnknownAuthorName}
	calls, summary := runPlaceholderStripMerge(t, `{"apply":true}`, canonical)
	for _, id := range []int{60, 61, 62, 63} {
		if !containsInt(calls.deleted, id) {
			t.Errorf("track/timecode row %d was not deleted as junk; deleted=%v", id, calls.deleted)
		}
	}
	if containsInt(calls.deleted, 1) {
		t.Error("the real author row was deleted")
	}
	if !strings.Contains(summary, "junk=4") {
		t.Errorf("summary should count 4 junk rows: %s", summary)
	}
	for _, name := range []string{"Kevin J Anderson", "Track Palin", "50 Cent", "Homer"} {
		if isTrackOrTimecodeArtifact(name) {
			t.Errorf("isTrackOrTimecodeArtifact(%q) = true; a person's name must not match", name)
		}
	}
}

// The placeholder dry run writes nothing.
func TestAuthorStripMerge_PlaceholderDryRunWritesNothing(t *testing.T) {
	canonical := &database.Author{ID: 50, Name: database.UnknownAuthorName}
	calls, summary := runPlaceholderStripMerge(t, ``, canonical)
	if len(calls.deleted) != 0 || len(calls.setAuthors) != 0 || len(calls.updated) != 0 || len(calls.tombstones) != 0 {
		t.Errorf("dry run wrote: deleted=%v setAuthors=%v updated=%v tombstones=%v",
			calls.deleted, calls.setAuthors, calls.updated, calls.tombstones)
	}
	if !strings.Contains(summary, "placeholders=7") {
		t.Errorf("dry run should still count the placeholders: %s", summary)
	}
}

// --- title-as-author (TODO JUNK-TITLE-AUTHORS) ---

func titleAsAuthorIntPtr(n int) *int { return &n }

// TestNormalizeForTitleAuthorCompare covers the TODO's exact normalization
// rule: case, punctuation, '_'->space, whitespace.
func TestNormalizeForTitleAuthorCompare(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"already normal", "arcane chef 2", "arcane chef 2"},
		{"case folds", "Arcane Chef 2", "arcane chef 2"},
		{"underscore becomes space", "Arcane_Chef_2", "arcane chef 2"},
		{"punctuation dropped", "Arcane Chef 2: A LitRPG Adventure", "arcane chef 2 a litrpg adventure"},
		{"apostrophe becomes a space, then collapses", "O'Brien", "o brien"},
		{"whitespace collapses", "Arcane   Chef\t2", "arcane chef 2"},
		{"empty stays empty", "", ""},
		{"mixed separators", "Arcane-Chef_2!!", "arcane chef 2"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := normalizeForTitleAuthorCompare(c.in); got != c.want {
				t.Errorf("normalizeForTitleAuthorCompare(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// TestBookTitleNamesAuthor covers the three candidate shapes: the whole
// title, the leading segment before ':' or ' - ', and "<series> <position>".
func TestBookTitleNamesAuthor(t *testing.T) {
	seriesByID := map[int]database.Series{
		1: {ID: 1, Name: "Broken Circle"},
	}
	cases := []struct {
		name   string
		author string
		book   database.BookCore
		want   bool
	}{
		{
			name:   "whole title matches",
			author: "Arcane Chef 2",
			book:   database.BookCore{Title: "Arcane Chef 2"},
			want:   true,
		},
		{
			name:   "leading colon segment matches",
			author: "Arcane Chef 2",
			book:   database.BookCore{Title: "Arcane Chef 2: A LitRPG Adventure"},
			want:   true,
		},
		{
			name:   "leading ' - ' segment matches",
			author: "Arcane Chef 2",
			book:   database.BookCore{Title: "Arcane Chef 2 - A LitRPG Adventure"},
			want:   true,
		},
		{
			name:   "series + position matches",
			author: "Broken Circle 3",
			book: database.BookCore{
				Title:          "Prologue",
				SeriesID:       titleAsAuthorIntPtr(1),
				SeriesSequence: titleAsAuthorIntPtr(3),
			},
			want: true,
		},
		{
			name:   "series + raw position string matches",
			author: "Broken Circle 3",
			book: database.BookCore{
				Title:             "Prologue",
				SeriesID:          titleAsAuthorIntPtr(1),
				SeriesPositionRaw: strPtr("3"),
			},
			want: true,
		},
		{
			name:   "no match, different title",
			author: "Arcane Chef 2",
			book:   database.BookCore{Title: "The Stand"},
			want:   false,
		},
		{
			name:   "no match, colon segment differs",
			author: "Arcane Chef 2",
			book:   database.BookCore{Title: "Something Else: Arcane Chef 2"},
			want:   false,
		},
		{
			name:   "empty author name never matches",
			author: "",
			book:   database.BookCore{Title: ""},
			want:   false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := bookTitleNamesAuthor(c.author, c.book, seriesByID); got != c.want {
				t.Errorf("bookTitleNamesAuthor(%q, %+v) = %v, want %v", c.author, c.book, got, c.want)
			}
		})
	}
}

// TestClassifyTitleAsAuthor covers the "no OTHER books whose titles differ"
// guard: an author is flagged only when every LIVE book it credits matches,
// and never flagged when it has zero live books.
func TestClassifyTitleAsAuthor(t *testing.T) {
	trashed := true
	cases := []struct {
		name  string
		books []database.BookCore
		want  bool
	}{
		{
			name:  "single self-titled book is junk",
			books: []database.BookCore{{ID: "b1", Title: "Arcane Chef 2: A LitRPG Adventure"}},
			want:  true,
		},
		{
			name: "two self-titled books, both match, is junk",
			books: []database.BookCore{
				{ID: "b1", Title: "Arcane Chef 2: A LitRPG Adventure"},
				{ID: "b2", Title: "Arcane Chef 2"},
			},
			want: true,
		},
		{
			name: "a differently-titled book protects the author",
			books: []database.BookCore{
				{ID: "b1", Title: "Arcane Chef 2: A LitRPG Adventure"},
				{ID: "b2", Title: "Arcane Chef 3: The Sequel"},
			},
			want: false,
		},
		{
			name:  "no books at all is not flagged",
			books: nil,
			want:  false,
		},
		{
			name: "only a soft-deleted matching book is not flagged",
			books: []database.BookCore{
				{ID: "b1", Title: "Arcane Chef 2", MarkedForDeletion: &trashed},
			},
			want: false,
		},
		{
			name: "soft-deleted mismatch is ignored, live match still counts",
			books: []database.BookCore{
				{ID: "b1", Title: "Arcane Chef 2"},
				{ID: "b2", Title: "Completely Different", MarkedForDeletion: &trashed},
			},
			want: true,
		},
	}
	seriesByID := map[int]database.Series{}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			author := database.Author{ID: 100, Name: "Arcane Chef 2"}
			if got := classifyTitleAsAuthor(author, c.books, seriesByID); got != c.want {
				t.Errorf("classifyTitleAsAuthor(...) = %v, want %v", got, c.want)
			}
		})
	}
}

// titleAsAuthorFixture: author 100 "Arcane Chef 2" credits one book titled
// after it (the TODO's motivating example, fixed by hand on 2026-09-25 as
// author id 64477). Author 101 "Stephen King" credits a self-titled book
// AND "The Stand" — a real person who must never be flagged just because one
// of their books shares their name. Author 102 "Solo Title" has exactly one
// book, also self-titled. Author 103 "Broken Circle 3" is matched only
// through its series name + position, on a book titled "Prologue".
func titleAsAuthorFixture() []database.Author {
	return []database.Author{
		{ID: 100, Name: "Arcane Chef 2"},
		{ID: 101, Name: "Stephen King"},
		{ID: 102, Name: "Solo Title"},
		{ID: 103, Name: "Broken Circle 3"},
	}
}

func newTitleAsAuthorPlugin(calls *stripMergeCalls) *Plugin {
	authors := titleAsAuthorFixture()
	p := newStripPlugin(authors, calls)
	store := p.deps.(*fakeDeps).store.(*database.MockStore)

	books := []database.BookCore{
		{ID: "bk-arcane", Title: "Arcane Chef 2: A LitRPG Adventure", AuthorID: titleAsAuthorIntPtr(100)},
		{ID: "bk-king-self", Title: "Stephen King", AuthorID: titleAsAuthorIntPtr(101)},
		{ID: "bk-king-stand", Title: "The Stand", AuthorID: titleAsAuthorIntPtr(101)},
		{ID: "bk-solo", Title: "Solo Title", AuthorID: titleAsAuthorIntPtr(102)},
		{ID: "bk-broken-circle", Title: "Prologue", AuthorID: titleAsAuthorIntPtr(103), SeriesID: titleAsAuthorIntPtr(1), SeriesSequence: titleAsAuthorIntPtr(3)},
	}
	store.GetAllBooksCoreFunc = func(limit, offset int) ([]database.BookCore, error) { return books, nil }
	store.GetAllSeriesFunc = func() ([]database.Series, error) {
		return []database.Series{{ID: 1, Name: "Broken Circle"}}, nil
	}

	byID := map[string]database.BookCore{}
	for _, b := range books {
		byID[b.ID] = b
	}
	byAuthor := map[int][]database.BookCore{}
	for _, b := range books {
		byAuthor[*b.AuthorID] = append(byAuthor[*b.AuthorID], b)
	}
	// relinkAwareBooks makes the post-delete VerifyAuthorUnlinked re-read see
	// the SetBookAuthors/UpdateBook writes this run makes, exactly as
	// newStripPlugin's own fixture does — a static byAuthor snapshot would
	// always see the pre-unlink credit and VerifyAuthorUnlinked would refuse
	// every delete.
	store.GetBooksByAuthorIDForRelinkFunc = relinkAwareBooks(
		func(authorID int) ([]database.BookCore, error) { return byAuthor[authorID], nil },
		calls.setAuthors,
		func(id string) (*int, bool) {
			if b, ok := calls.updated[id]; ok {
				return b.AuthorID, true
			}
			return nil, false
		},
	)
	store.GetBookAuthorsFunc = func(bookID string) ([]database.BookAuthor, error) {
		if written, ok := calls.setAuthors[bookID]; ok {
			return written, nil
		}
		b, ok := byID[bookID]
		if !ok || b.AuthorID == nil {
			return nil, nil
		}
		return []database.BookAuthor{{BookID: bookID, AuthorID: *b.AuthorID, Role: "author"}}, nil
	}
	store.GetBookByIDFunc = func(id string) (*database.Book, error) {
		if b, ok := calls.updated[id]; ok {
			c := *b
			return &c, nil
		}
		b, ok := byID[id]
		if !ok {
			return nil, nil
		}
		return &database.Book{ID: b.ID, AuthorID: b.AuthorID}, nil
	}
	return p
}

func runTitleAsAuthorStripMerge(t *testing.T, params string) (*stripMergeCalls, string) {
	t.Helper()
	calls := &stripMergeCalls{}
	p := newTitleAsAuthorPlugin(calls)
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

// 🔴 THE ARCANE CHEF CASE. Author 100 "Arcane Chef 2" credits only a book
// titled "Arcane Chef 2: A LitRPG Adventure" — the exact pattern fixed by
// hand on 2026-09-25 (author id 64477). It, and the series-matched author
// 103, must be deleted as junk; the legitimate Stephen King row must survive.
func TestAuthorStripMerge_TitleAsAuthorDeletesSelfTitledRow(t *testing.T) {
	calls, summary := runTitleAsAuthorStripMerge(t, `{"apply":true}`)
	for _, id := range []int{100, 102, 103} {
		if !containsInt(calls.deleted, id) {
			t.Errorf("title-as-author row %d was not deleted; deleted=%v", id, calls.deleted)
		}
	}
	if containsInt(calls.deleted, 101) {
		t.Errorf("Stephen King (row 101) was deleted, but it credits a differently-titled book too")
	}
	if !strings.Contains(summary, "title-as-author=3") {
		t.Errorf("summary should count 3 title-as-author rows: %s", summary)
	}
}

// A legitimate author who happens to share a title with one of their books
// (Stephen King crediting both "Stephen King" and "The Stand") must never be
// flagged: the differing title is exactly the protection the TODO calls for.
func TestAuthorStripMerge_LegitimateSameTitleAuthorIsNotFlagged(t *testing.T) {
	calls, summary := runTitleAsAuthorStripMerge(t, `{"apply":true}`)
	if containsInt(calls.deleted, 101) {
		t.Fatalf("Stephen King (row 101) was deleted; deleted=%v", calls.deleted)
	}
	if len(calls.setAuthors["bk-king-self"]) != 0 || len(calls.setAuthors["bk-king-stand"]) != 0 {
		t.Errorf("Stephen King's books should not have been touched: %v / %v",
			calls.setAuthors["bk-king-self"], calls.setAuthors["bk-king-stand"])
	}
	_ = summary
}

// Report-only mode must count but not write, same as every other bucket in
// this op.
func TestAuthorStripMerge_TitleAsAuthorDryRunWritesNothing(t *testing.T) {
	calls, summary := runTitleAsAuthorStripMerge(t, ``)
	if len(calls.deleted) != 0 || len(calls.setAuthors) != 0 || len(calls.updated) != 0 {
		t.Errorf("dry run wrote: deleted=%v setAuthors=%v updated=%v", calls.deleted, calls.setAuthors, calls.updated)
	}
	if !strings.Contains(summary, "title-as-author=3") {
		t.Errorf("dry run should still count the title-as-author rows: %s", summary)
	}
}

// delete_junk=false must hold back title-as-author deletes the same way it
// holds back the numbering-junk bucket — it is the same gate, not a new one.
func TestAuthorStripMerge_TitleAsAuthorRespectsDeleteJunkFalse(t *testing.T) {
	calls, summary := runTitleAsAuthorStripMerge(t, `{"apply":true,"delete_junk":false}`)
	for _, id := range []int{100, 102, 103} {
		if containsInt(calls.deleted, id) {
			t.Errorf("row %d was deleted despite delete_junk=false; deleted=%v", id, calls.deleted)
		}
	}
	if !strings.Contains(summary, "title-as-author=3") {
		t.Errorf("summary should still count the 3 rows even when not deleting: %s", summary)
	}
}
