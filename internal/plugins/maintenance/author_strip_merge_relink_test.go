// file: internal/plugins/maintenance/author_strip_merge_relink_test.go
// version: 1.3.0
// guid: 9444cf3d-482c-4380-9243-4bcfd66af5ee
// last-edited: 2026-09-26

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/dedup"
)

// relinkFixture is the Arcane Chef shape from the owner's example: T 100
// "Arcane Chef 2" credits only its self-titled book, twin S 200
// "01 Arcane Chef 2" strips to T's name, and the real author T.J. Ward (300)
// already has a row. Evidence per book is set by each test.
type relinkFixture struct {
	books   []database.BookCore
	files   map[string][]database.BookFile
	fetched map[string]string // book ID -> provider author_name
	created []string
	extra   []database.Author
	// journal, events and journalErr observe the undo journal: every
	// journal row, the order of journal rows vs writes, and a forced
	// journal failure.
	journal    []database.OperationChange
	events     []string
	journalErr error
	// coCredits adds junction credits to a book beside its primary.
	coCredits map[string][]database.BookAuthor
	store     *database.MockStore
}

func newRelinkFixture() *relinkFixture {
	return &relinkFixture{
		books: []database.BookCore{
			{ID: "bk-t", Title: "Arcane Chef 2: A LitRPG Adventure", AuthorID: titleAsAuthorIntPtr(100), FilePath: "/lib/Arcane Chef 2/Arcane Chef 2"},
			{ID: "bk-s", Title: "Arcane Chef 2", AuthorID: titleAsAuthorIntPtr(200), FilePath: "/lib/01 Arcane Chef 2/Arcane Chef 2"},
			{ID: "bk-ward", Title: "Arcane Chef", AuthorID: titleAsAuthorIntPtr(300), FilePath: "/lib/T.J. Ward/Arcane Chef"},
		},
		files:   map[string][]database.BookFile{},
		fetched: map[string]string{},
	}
}

func (f *relinkFixture) run(t *testing.T, params string) (*stripMergeCalls, string) {
	t.Helper()
	calls := &stripMergeCalls{}
	authors := []database.Author{
		{ID: 100, Name: "Arcane Chef 2"},
		{ID: 200, Name: "01 Arcane Chef 2"},
		{ID: 300, Name: "T.J. Ward"},
	}
	authors = append(authors, f.extra...)
	p := newTitleAsAuthorPluginWith(calls, authors, f.books, nil)
	store := p.deps.(*fakeDeps).store.(*database.MockStore)
	store.GetBookFilesFunc = func(bookID string) ([]database.BookFile, error) { return f.files[bookID], nil }
	store.GetMetadataFieldStatesFunc = func(bookID string) ([]database.MetadataFieldState, error) {
		v, ok := f.fetched[bookID]
		if !ok {
			return nil, nil
		}
		raw, _ := json.Marshal(v)
		s := string(raw)
		return []database.MetadataFieldState{{BookID: bookID, Field: "author_name", FetchedValue: &s}}, nil
	}
	store.GetAuthorByNameFunc = func(name string) (*database.Author, error) {
		for _, a := range authors {
			if dedup.NormalizeAuthorName(a.Name) == dedup.NormalizeAuthorName(name) {
				c := a
				return &c, nil
			}
		}
		return nil, nil
	}
	if len(f.coCredits) > 0 {
		inner := store.GetBookAuthorsFunc
		store.GetBookAuthorsFunc = func(bookID string) ([]database.BookAuthor, error) {
			if w, ok := calls.setAuthors[bookID]; ok {
				return w, nil
			}
			base, err := inner(bookID)
			if err != nil {
				return nil, err
			}
			return append(base, f.coCredits[bookID]...), nil
		}
	}
	innerSet := store.SetBookAuthorsFunc
	store.SetBookAuthorsFunc = func(bookID string, as []database.BookAuthor) error {
		f.events = append(f.events, "write:"+bookID)
		return innerSet(bookID, as)
	}
	store.CreateOperationChangeFunc = func(c *database.OperationChange) error {
		if f.journalErr != nil {
			return f.journalErr
		}
		f.events = append(f.events, "journal:"+c.ChangeType+":"+c.BookID)
		f.journal = append(f.journal, *c)
		return nil
	}
	f.store = store
	store.CreateAuthorFunc = func(name string) (*database.Author, error) {
		f.events = append(f.events, "create:"+name)
		f.created = append(f.created, name)
		a := database.Author{ID: 900 + len(f.created), Name: name}
		authors = append(authors, a)
		return &a, nil
	}
	rep := &summaryReporter{}
	if err := p.runAuthorStripMerge(context.Background(), json.RawMessage(params), rep); err != nil {
		t.Fatalf("runAuthorStripMerge: %v", err)
	}
	return calls, rep.summary(t)
}

func credits(calls *stripMergeCalls, bookID string) []int {
	var out []int
	for _, ba := range calls.setAuthors[bookID] {
		out = append(out, ba.AuthorID)
	}
	return out
}

func wantSummary(t *testing.T, summary string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(summary, w) {
			t.Errorf("summary missing %q: %s", w, summary)
		}
	}
}

// One source per book, each agreeing with nothing else: T's book is named by
// its provider value, S's by a file tag. Both books are relinked to T.J. Ward
// (the existing row, no creation), T is deleted as title-as-author and S as a
// twin whose every book was relinked, and no book is left authorless.
func TestAuthorStripMerge_RelinkOneAgreeingSource(t *testing.T) {
	f := newRelinkFixture()
	f.fetched["bk-t"] = "T.J. Ward"
	f.files["bk-s"] = []database.BookFile{{ID: "f1", BookID: "bk-s", FilePath: "/lib/01 Arcane Chef 2/Arcane Chef 2/01.m4b", RawTags: map[string]string{"ARTIST": "T.J. Ward"}}}
	calls, summary := f.run(t, `{"apply":true,"relink_title_as_author":true,"delete_title_as_author":true}`)

	for _, id := range []string{"bk-t", "bk-s"} {
		got := credits(calls, id)
		if !containsInt(got, 300) || containsInt(got, 100) || containsInt(got, 200) {
			t.Errorf("%s credits = %v, want T.J. Ward (300) only", id, got)
		}
	}
	for _, id := range []string{"bk-t", "bk-s"} {
		b, ok := calls.updated[id]
		if !ok || b.AuthorID == nil || *b.AuthorID != 300 {
			t.Errorf("%s primary AuthorID not moved to 300: %+v", id, b)
		}
	}
	if !containsInt(calls.deleted, 100) || !containsInt(calls.deleted, 200) {
		t.Errorf("junk rows 100 and 200 should be deleted; deleted=%v", calls.deleted)
	}
	if len(f.created) != 0 {
		t.Errorf("an existing author was re-created: %v", f.created)
	}
	wantSummary(t, summary, "relink-planned=2 ", "relinked=2 ", "twin-deletes=1", "books-left-authorless=0 ",
		"relink-conflict=0 ", "relink-no-candidate=0 ", "relink-new-authors=0 ", "target-is-junk=1 ", "mergeable=0 ")
}

// Two sources that name different people: the book is left alone and
// reported, and the junk row is NOT reported as fully relinked.
func TestAuthorStripMerge_RelinkConflictingSourcesSkip(t *testing.T) {
	f := newRelinkFixture()
	f.fetched["bk-t"] = "T.J. Ward"
	f.files["bk-t"] = []database.BookFile{{ID: "f1", BookID: "bk-t", FilePath: "/lib/Arcane Chef 2/Arcane Chef 2/01.m4b", RawTags: map[string]string{"album_artist": "Someone Else"}}}
	calls, summary := f.run(t, `{"apply":true,"relink_title_as_author":true}`)
	if got := credits(calls, "bk-t"); got != nil {
		t.Errorf("conflicting book was rewritten: %v", got)
	}
	if _, ok := calls.updated["bk-t"]; ok {
		t.Errorf("conflicting book's primary was rewritten")
	}
	wantSummary(t, summary, "relink-conflict=1 ", "relinked=0 ")
}

// No evidence at all: the book is left alone and reported. A file tag that
// only repeats the junk name, or names the narrator, is not evidence.
func TestAuthorStripMerge_RelinkNoSourceSkips(t *testing.T) {
	f := newRelinkFixture()
	narrator := "Some Reader"
	f.books[0].Narrator = &narrator
	f.files["bk-t"] = []database.BookFile{{ID: "f1", BookID: "bk-t", FilePath: "/lib/Arcane Chef 2/Arcane Chef 2/01.m4b",
		RawTags: map[string]string{"artist": "Arcane Chef 2", "album_artist": "Some Reader"}}}
	calls, summary := f.run(t, `{"apply":true,"relink_title_as_author":true}`)
	if got := credits(calls, "bk-t"); got != nil {
		t.Errorf("book with no evidence was rewritten: %v", got)
	}
	wantSummary(t, summary, "relink-no-candidate=2 ", "relinked=0 ", "relink-planned=0 ")
}

// The preview (apply=false) writes nothing: no credit, no primary, no author
// row created, no delete. It reports the same plan the apply does, including
// the author row it would create.
func TestAuthorStripMerge_RelinkPreviewWritesNothing(t *testing.T) {
	previewF := newRelinkFixture()
	previewF.fetched["bk-t"] = "Brand New Writer"
	previewF.fetched["bk-s"] = "T.J. Ward"
	calls, preview := previewF.run(t, `{"relink_title_as_author":true,"delete_title_as_author":true}`)
	if len(calls.setAuthors) != 0 || len(calls.updated) != 0 || len(calls.deleted) != 0 || len(previewF.created) != 0 {
		t.Fatalf("preview wrote: setAuthors=%v updated=%v deleted=%v created=%v", calls.setAuthors, calls.updated, calls.deleted, previewF.created)
	}
	wantSummary(t, preview, "relink-planned=2 ", "relink-new-authors=1 ", "relinked=0 ")

	applyF := newRelinkFixture()
	applyF.fetched["bk-t"] = "Brand New Writer"
	applyF.fetched["bk-s"] = "T.J. Ward"
	applyCalls, applied := applyF.run(t, `{"apply":true,"relink_title_as_author":true,"delete_title_as_author":true}`)
	if len(applyF.created) != 1 || applyF.created[0] != "Brand New Writer" {
		t.Errorf("apply should create exactly the previewed author, created %v", applyF.created)
	}
	if got := credits(applyCalls, "bk-t"); len(got) != 1 || got[0] != 901 {
		t.Errorf("bk-t credits = %v, want the created row 901", got)
	}
	strip := func(s string) string {
		var keep []string
		for _, f := range strings.Fields(planOnlySummary(s)) {
			if !strings.HasPrefix(f, "relinked=") && !strings.HasPrefix(f, "relink-journal-rows=") {
				keep = append(keep, f)
			}
		}
		return strings.Join(keep, " ")
	}
	if strip(preview) != strip(applied) {
		t.Errorf("preview plan differs from apply plan:\n preview: %s\n apply:   %s", strip(preview), strip(applied))
	}
}

// books/itunes/** and Doctor Who / Big Finish / Torchwood are never touched,
// even with agreeing evidence; the file rows are checked, not only
// book.file_path.
func TestAuthorStripMerge_RelinkNeverTouchesITunesOrOwnerManual(t *testing.T) {
	f := newRelinkFixture()
	f.fetched["bk-t"] = "T.J. Ward"
	f.fetched["bk-s"] = "T.J. Ward"
	f.files["bk-t"] = []database.BookFile{{ID: "f1", BookID: "bk-t", FilePath: "/mnt/bigdata/books/itunes/iTunes Media/Audiobooks/x.m4b"}}
	f.books[1].FilePath = "/lib/Big Finish/Arcane Chef 2"
	calls, summary := f.run(t, `{"apply":true,"relink_title_as_author":true,"delete_title_as_author":true}`)
	if len(calls.setAuthors["bk-t"]) != 0 || len(calls.setAuthors["bk-s"]) != 0 {
		t.Errorf("excluded books were rewritten: %v", calls.setAuthors)
	}
	if containsInt(calls.deleted, 200) {
		t.Errorf("twin 200 deleted although its book was never relinked; deleted=%v", calls.deleted)
	}
	wantSummary(t, summary, "relink-skipped-itunes=1 ", "relink-skipped-owner-manual=1 ", "relinked=0 ", "twin-deletes=0")
}

// Two existing rows carry the chosen name (this library has duplicate author
// rows). Picking one would be a guess: the book is left alone and reported as
// ambiguous, the same rule the op applies to merges.
func TestAuthorStripMerge_RelinkDuplicateAuthorRowsIsAmbiguous(t *testing.T) {
	f := newRelinkFixture()
	f.extra = []database.Author{{ID: 301, Name: "T.J. Ward"}}
	f.fetched["bk-t"] = "T.J. Ward"
	calls, summary := f.run(t, `{"apply":true,"relink_title_as_author":true}`)
	if len(calls.setAuthors) != 0 || len(calls.updated) != 0 || len(f.created) != 0 {
		t.Errorf("ambiguous relink wrote: setAuthors=%v updated=%v created=%v", calls.setAuthors, calls.updated, f.created)
	}
	wantSummary(t, summary, "relink-ambiguous=1 ", "relinked=0 ")
}

// limit caps the relinks: with limit=1 only the first book (junk row 100's
// bk-t) is written, the other is deferred, and the twin keeps its book, so it
// is not deleted.
func TestAuthorStripMerge_RelinkHonoursLimit(t *testing.T) {
	f := newRelinkFixture()
	f.fetched["bk-t"] = "T.J. Ward"
	f.fetched["bk-s"] = "T.J. Ward"
	calls, summary := f.run(t, `{"apply":true,"relink_title_as_author":true,"delete_title_as_author":true,"limit":1}`)
	if got := credits(calls, "bk-t"); len(got) != 1 || got[0] != 300 {
		t.Errorf("bk-t credits = %v, want [300]", got)
	}
	if got := credits(calls, "bk-s"); got != nil {
		t.Errorf("bk-s past the limit was rewritten: %v", got)
	}
	if containsInt(calls.deleted, 200) {
		t.Errorf("twin deleted although its book was deferred; deleted=%v", calls.deleted)
	}
	wantSummary(t, summary, "relinked=1 ", "relink-deferred=1 ", "twin-deletes=0")
}

// Every write is preceded by its journal row: the author creation by a
// title_relink_author_create row, the credit move by a title_relink_credits
// row whose OldValue holds the credits read just before it.
func TestAuthorStripMerge_RelinkJournalsBeforeEachWrite(t *testing.T) {
	f := newRelinkFixture()
	f.fetched["bk-t"] = "Brand New Writer"
	_, summary := f.run(t, `{"apply":true,"relink_title_as_author":true}`)
	want := []string{
		"journal:" + ChangeTypeTitleRelinkAuthorCreate + ":bk-t",
		"create:Brand New Writer",
		"journal:" + ChangeTypeTitleRelinkCredits + ":bk-t",
		"write:bk-t",
	}
	if strings.Join(f.events, " | ") != strings.Join(want, " | ") {
		t.Errorf("event order:\n got  %v\n want %v", f.events, want)
	}
	wantSummary(t, summary, "relinked=1 ", "relink-journal-rows=2 ")
}

// A journal write that fails skips the book: no author row is created and
// no credit is moved.
func TestAuthorStripMerge_RelinkJournalFailureSkipsBook(t *testing.T) {
	for name, fetched := range map[string]string{"existing author": "T.J. Ward", "new author": "Brand New Writer"} {
		t.Run(name, func(t *testing.T) {
			f := newRelinkFixture()
			f.fetched["bk-t"] = fetched
			f.journalErr = errors.New("journal down")
			calls, summary := f.run(t, `{"apply":true,"relink_title_as_author":true}`)
			if len(calls.setAuthors) != 0 || len(calls.updated) != 0 || len(f.created) != 0 {
				t.Errorf("wrote without a journal: setAuthors=%v updated=%v created=%v", calls.setAuthors, calls.updated, f.created)
			}
			wantSummary(t, summary, "relinked=0 ", "relink-failed=1 ", "relink-journal-rows=0 ")
		})
	}
}

// The journal's old value replays (through audiobooks.RevertService) to the original credits: order, roles and
// the co-author included, and the primary.
func TestAuthorStripMerge_RelinkJournalReplaysOriginalCredits(t *testing.T) {
	f := newRelinkFixture()
	f.fetched["bk-t"] = "T.J. Ward"
	f.coCredits = map[string][]database.BookAuthor{
		"bk-t": {{BookID: "bk-t", AuthorID: 400, Role: "co-author", Position: 1}},
	}
	original := []database.BookAuthor{
		{BookID: "bk-t", AuthorID: 100, Role: "author"},
		{BookID: "bk-t", AuthorID: 400, Role: "co-author", Position: 1},
	}
	calls, _ := f.run(t, `{"apply":true,"relink_title_as_author":true}`)
	if got := credits(calls, "bk-t"); !containsInt(got, 300) || containsInt(got, 100) || !containsInt(got, 400) {
		t.Fatalf("relink did not move the credit while keeping the co-author: %v", got)
	}
	var row *database.OperationChange
	for i := range f.journal {
		if f.journal[i].ChangeType == ChangeTypeTitleRelinkCredits && f.journal[i].BookID == "bk-t" {
			row = &f.journal[i]
		}
	}
	if row == nil {
		t.Fatalf("no credits journal row; journal=%v", f.journal)
	}
	// Replay through the normal undo path.
	var rows []*database.OperationChange
	for i := range f.journal {
		rows = append(rows, &f.journal[i])
	}
	f.store.GetOperationChangesFunc = func(string) ([]*database.OperationChange, error) { return rows, nil }
	f.store.MarkOperationChangesRevertedFunc = func(string, []string) error { return nil }
	res, err := audiobooks.NewRevertService(f.store).RevertOperation("op")
	if err != nil {
		t.Fatalf("revert: %v", err)
	}
	if res.Restored != 1 {
		t.Errorf("restored %d rows, want 1: %+v", res.Restored, res)
	}
	if got := calls.setAuthors["bk-t"]; !reflect.DeepEqual(got, original) {
		t.Errorf("replayed credits = %+v, want %+v", got, original)
	}
	if b := calls.updated["bk-t"]; b == nil || b.AuthorID == nil || *b.AuthorID != 100 {
		t.Errorf("replayed primary = %+v, want 100", b)
	}
}
