// file: internal/plugins/maintenance/series_denumber_apply_test.go
// version: 4.2.2
// guid: 4c93e07a-1d62-4b8e-a5f3-90b7c1de2846
// last-edited: 2026-11-18

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// Fixtures use the Batman catalogue only. The numbered series below are the
// shapes the planner reads as "Base NN": "Court of Owls 05" (zero-padded, high
// confidence) and "Court of Owls Book 5" (a keyword, high confidence).

const (
	denumberOpID   = "op-series-denumber-test"
	denumberAuthor = 3
)

// restoringStore adds the capabilities an apply requires of a store, so a
// MockStore can stand in for the real one: RestoreSeries (what the revert needs),
// GetSeriesBookRefCount (what the delete checks with first) and
// DeleteSeriesIfUnreferenced (the delete itself).
type restoringStore struct {
	*database.MockStore
	refs               func(seriesID int) (int, error)
	deleteUnreferenced func(seriesID int, name string, authorID *int) (bool, error)
}

func (restoringStore) RestoreSeries(int, string, *int) error { return nil }

func (r restoringStore) GetSeriesBookRefCount(id int) (int, error) {
	if r.refs == nil {
		return 0, nil
	}
	return r.refs(id)
}

func (r restoringStore) DeleteSeriesIfUnreferenced(id int, name string, authorID *int) (bool, error) {
	if r.deleteUnreferenced == nil {
		return false, errors.New("restoringStore: no DeleteSeriesIfUnreferenced behaviour wired")
	}
	return r.deleteUnreferenced(id, name, authorID)
}

// opIDReporter (scan_standdown_apply_test.go) gives fakeReporter the OpID() the
// registry's reporter has.

// denumberLib is a stateful library behind a MockStore: book rows really move
// when written, series are really created and deleted, and every write lands in
// events in order, so a test can assert that a journal row came BEFORE the write
// it describes.
type denumberLib struct {
	t  *testing.T
	mu sync.Mutex

	store   *database.MockStore
	series  []database.Series
	deleted map[int]bool
	books   map[string]*database.Book
	// extraRefs are book rows that hold a series but that the membership read
	// cannot see (trashed, non-primary): only the unfiltered count has them.
	extraRefs map[int]int
	// trashed are soft-deleted books: they hold a series (and a position) but the
	// membership read does not list them; ListSoftDeletedBooks does. A test that
	// adds one also adds it to extraRefs, as the unfiltered count has it.
	trashed []*database.Book
	authors []database.Author

	nextSeriesID int
	events       []string
	changes      []database.OperationChange
	created      []database.Series

	bulkReads, perSeriesReads, trashReads int
	titleSeq                              int
	nameLookups                           map[string]int

	// afterMembership runs once the membership read has answered, so a test can
	// change a row between the plan and the apply.
	afterMembership func()
	// appearOnLookup makes a GetSeriesByName for name answer with a row after the
	// first call, as if somebody created the base while the run was deciding.
	appearOnLookup string
	failUpdate     map[string]bool
	// cache counts the cache invalidations of the last run.
	cache *spCacheDeps
	// root is the library root the plugin deps report. A report path is confined to
	// {root}/.reports, so it is a real temp directory, not the fake "/lib".
	root string
	// reportPath is the report an apply writes: run() adds it to every apply that
	// names none, because an apply without one is refused. It lies under
	// {root}/.reports.
	reportPath string
	// refOverride, when set, answers GetSeriesBookRefCount instead of the live count.
	refOverride func(seriesID int) (int, error)
	// refsAtDelete, when set, answers the count the delete itself makes (the
	// store's check-and-delete) instead of refCount, so a test can have a
	// reference appear AFTER the op's own earlier count. It may also act as the
	// other writer: it runs at the moment of the delete.
	refsAtDelete func(seriesID int) (int, error)
	// deps, when set, replaces the plugin deps of run() (a scan controller, say).
	deps func(base *spCacheDeps) ServerDeps
}

func newDenumberLib(t *testing.T) *denumberLib {
	t.Helper()
	l := &denumberLib{
		t:            t,
		root:         t.TempDir(),
		store:        &database.MockStore{},
		deleted:      map[int]bool{},
		books:        map[string]*database.Book{},
		extraRefs:    map[int]int{},
		nextSeriesID: 1000,
		nameLookups:  map[string]int{},
		failUpdate:   map[string]bool{},
		authors:      []database.Author{{ID: denumberAuthor, Name: "Bruce Wayne"}, {ID: denumberAuthor + 1, Name: "Alfred Pennyworth"}},
	}
	l.wire()
	return l
}

func (l *denumberLib) addSeries(id int, name string, authorID int) {
	s := database.Series{ID: id, Name: name}
	if authorID > 0 {
		a := authorID
		s.AuthorID = &a
	}
	l.series = append(l.series, s)
}

// addBook puts a book in a series. seq < 0 means no position; group "" means the
// book is not a version of another.
func (l *denumberLib) addBook(id string, seriesID, seq int, group string) {
	sid := seriesID
	b := &database.Book{ID: id, SeriesID: &sid, Title: l.nextTitle()}
	if seq >= 0 {
		q := seq
		b.SeriesSequence = &q
	}
	if group != "" {
		g := group
		b.VersionGroupID = &g
	}
	l.books[id] = b
}

// nextTitle is a title no other book of the fixture has once its digits and part
// words are taken off (the run check compares titles that way): "Detective Case"
// and a base-26 word. A run needs distinct titles, so a book added without one
// gets a different one each time.
func (l *denumberLib) nextTitle() string {
	l.titleSeq++
	n, word := l.titleSeq, ""
	for ; n > 0; n = (n - 1) / 26 {
		word = string(rune('a'+(n-1)%26)) + word
	}
	return "Detective Case " + word
}

// addBookT is addBook with the book's title, for the tests whose evidence is the
// titles of the books in a run of numbered series.
func (l *denumberLib) addBookT(id string, seriesID, seq int, group, title string) {
	l.addBook(id, seriesID, seq, group)
	l.books[id].Title = title
}

// addRunAround gives the numbered series "base NN" two siblings of the same
// author, each holding one book already placed elsewhere (so the siblings move
// nothing themselves), so that NN sits in a run of three consecutive numbers: the
// library's evidence for a position the book does not carry. firstID and
// firstID+1 are the siblings' series ids.
func (l *denumberLib) addRunAround(base string, author, n, firstID int) {
	nums := []int{n - 1, n + 1}
	if n < 2 {
		nums = []int{n + 1, n + 2}
	}
	for i, k := range nums {
		l.addSeries(firstID+i, fmt.Sprintf("%s %02d", base, k), author)
		l.addBook(fmt.Sprintf("sib-%d", firstID+i), firstID+i, 99, "")
	}
}

func (l *denumberLib) event(format string, args ...any) {
	l.events = append(l.events, fmt.Sprintf(format, args...))
}

func (l *denumberLib) liveSeries() []database.Series {
	out := make([]database.Series, 0, len(l.series))
	for _, s := range l.series {
		if !l.deleted[s.ID] {
			out = append(out, s)
		}
	}
	return out
}

func sameAuthor(a, b *int) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func (l *denumberLib) wire() {
	s := l.store
	s.GetAllSeriesFunc = func() ([]database.Series, error) {
		l.mu.Lock()
		defer l.mu.Unlock()
		return l.liveSeries(), nil
	}
	s.GetAllSeriesBookCountsFunc = func() (map[int]int, error) {
		l.mu.Lock()
		defer l.mu.Unlock()
		out := map[int]int{}
		for _, b := range l.books {
			if b.SeriesID != nil {
				out[*b.SeriesID]++
			}
		}
		return out, nil
	}
	s.GetAllSeriesBookRefCountsFunc = func() (map[int]int, error) {
		l.mu.Lock()
		defer l.mu.Unlock()
		out := map[int]int{}
		for _, b := range l.books {
			if b.SeriesID != nil {
				out[*b.SeriesID]++
			}
		}
		for id, n := range l.extraRefs {
			out[id] += n
		}
		return out, nil
	}
	s.GetBooksBySeriesIDAllVersionsFunc = func(int) ([]database.BookCore, error) {
		l.mu.Lock()
		defer l.mu.Unlock()
		l.perSeriesReads++
		return nil, errors.New("per-series membership read inside the series-denumber loops")
	}
	s.GetBooksBySeriesIDsAllVersionsFunc = func(ids []int) (map[int][]database.BookCore, error) {
		l.mu.Lock()
		l.bulkReads++
		out := map[int][]database.BookCore{}
		for _, id := range ids {
			out[id] = []database.BookCore{}
		}
		bookIDs := make([]string, 0, len(l.books))
		for id := range l.books {
			bookIDs = append(bookIDs, id)
		}
		sort.Strings(bookIDs)
		for _, id := range bookIDs {
			b := l.books[id]
			if b.SeriesID == nil {
				continue
			}
			if _, want := out[*b.SeriesID]; want {
				out[*b.SeriesID] = append(out[*b.SeriesID], database.BookCore{
					ID: id, Title: b.Title, SeriesSequence: b.SeriesSequence, VersionGroupID: b.VersionGroupID,
					ASIN: b.ASIN, ISBN10: b.ISBN10, ISBN13: b.ISBN13,
				})
			}
		}
		hook := l.afterMembership
		l.mu.Unlock()
		if hook != nil {
			hook()
		}
		return out, nil
	}
	s.ListSoftDeletedBooksFunc = func(int, int, *time.Time) ([]database.Book, error) {
		l.mu.Lock()
		defer l.mu.Unlock()
		l.trashReads++
		out := make([]database.Book, 0, len(l.trashed))
		for _, b := range l.trashed {
			out = append(out, *b)
		}
		return out, nil
	}
	s.GetBookByIDFunc = func(id string) (*database.Book, error) {
		l.mu.Lock()
		defer l.mu.Unlock()
		b, ok := l.books[id]
		if !ok {
			return nil, nil
		}
		return database.SnapshotBook(b)
	}
	s.UpdateBookFunc = func(id string, b *database.Book) (*database.Book, error) {
		l.mu.Lock()
		defer l.mu.Unlock()
		if l.failUpdate[id] {
			return nil, errors.New("simulated write failure")
		}
		cp, err := database.SnapshotBook(b)
		if err != nil {
			return nil, err
		}
		l.books[id] = cp
		l.event("update:%s", id)
		return b, nil
	}
	s.GetSeriesByNameFunc = func(name string, authorID *int) (*database.Series, error) {
		l.mu.Lock()
		defer l.mu.Unlock()
		key := strings.ToLower(name)
		l.nameLookups[key]++
		for _, row := range l.liveSeries() {
			if strings.EqualFold(strings.TrimSpace(row.Name), strings.TrimSpace(name)) && sameAuthor(row.AuthorID, authorID) {
				cp := row
				return &cp, nil
			}
		}
		if l.appearOnLookup != "" && strings.EqualFold(l.appearOnLookup, name) && l.nameLookups[key] > 1 {
			return &database.Series{ID: 7777, Name: name, AuthorID: authorID}, nil
		}
		return nil, nil
	}
	s.CreateSeriesFunc = func(name string, authorID *int) (*database.Series, error) {
		l.mu.Lock()
		defer l.mu.Unlock()
		l.nextSeriesID++
		row := database.Series{ID: l.nextSeriesID, Name: name, AuthorID: authorID}
		l.series = append(l.series, row)
		l.created = append(l.created, row)
		l.event("create:%d", row.ID)
		cp := row
		return &cp, nil
	}
	s.DeleteSeriesFunc = func(id int) error {
		l.mu.Lock()
		defer l.mu.Unlock()
		l.deleted[id] = true
		l.event("delete:%d", id)
		return nil
	}
	s.CreateOperationChangeFunc = func(c *database.OperationChange) error {
		l.mu.Lock()
		defer l.mu.Unlock()
		l.changes = append(l.changes, *c)
		target := c.BookID
		if c.SeriesID != nil {
			target = strconv.Itoa(*c.SeriesID)
		}
		l.event("change:%s/%s:%s", c.ChangeType, c.FieldName, target)
		return nil
	}
	s.GetAllAuthorsFunc = func() ([]database.Author, error) { return l.authors, nil }
	s.GetSeriesByIDFunc = func(id int) (*database.Series, error) {
		l.mu.Lock()
		defer l.mu.Unlock()
		for _, row := range l.liveSeries() {
			if row.ID == id {
				cp := row
				return &cp, nil
			}
		}
		return nil, nil
	}
}

// refCount is the unfiltered count of book rows naming a series: the live books
// plus the ones only the unfiltered read sees.
func (l *denumberLib) refCount(id int) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.refOverride != nil {
		return l.refOverride(id)
	}
	n := l.extraRefs[id]
	for _, b := range l.books {
		if b.SeriesID != nil && *b.SeriesID == id {
			n++
		}
	}
	return n, nil
}

// deleteUnreferenced models the store's DeleteSeriesIfUnreferenced for a
// single-threaded fixture, in the real method's terms: the row must exist and
// still be the expected name and author (ErrSeriesChanged), then the references
// are counted (refsAtDelete, when a test sets it, is also the point where a test
// changes the library "at delete time"), a referenced series is kept, a held row
// is refused (ErrSeriesHeld), and the delete goes through the MockStore's
// DeleteSeries (so DeleteSeriesFunc injections and the event log still apply).
// The atomicity itself is the real store's, and is tested there.
func (l *denumberLib) deleteUnreferenced(id int, name string, authorID *int) (bool, error) {
	count := l.refCount
	if l.refsAtDelete != nil {
		count = l.refsAtDelete
	}
	n, err := count(id)
	if err != nil {
		return false, err
	}
	cur, _ := l.store.GetSeriesByID(id)
	if cur == nil {
		return false, database.ErrSeriesNotFound
	}
	if cur.Name != name || !sameAuthorPtr(cur.AuthorID, authorID) {
		return false, database.ErrSeriesChanged
	}
	if n != 0 {
		l.mu.Lock()
		l.event("kept-at-delete:%d", id)
		l.mu.Unlock()
		return false, nil
	}
	if database.SeriesHeld(*cur) {
		return false, fmt.Errorf("%w by %s", database.ErrSeriesHeld, cur.HeldBy)
	}
	if err := l.store.DeleteSeries(id); err != nil {
		return false, err
	}
	return true, nil
}

func (l *denumberLib) restoring() restoringStore {
	return restoringStore{MockStore: l.store, refs: l.refCount, deleteUnreferenced: l.deleteUnreferenced}
}

// withReport adds a reportPath to an apply's params when they name none.
func (l *denumberLib) withReport(params string) string {
	if !strings.Contains(params, `"apply": true`) || strings.Contains(params, `"reportPath"`) {
		return params
	}
	if l.reportPath == "" {
		l.reportPath = filepath.Join(l.root, ".reports", "report.tsv")
	}
	// Inserted as text, so a test of malformed or duplicated keys still reaches the
	// decoder exactly as written.
	return strings.Replace(params, "{", `{"reportPath": `+strconv.Quote(l.reportPath)+`, `, 1)
}

// run executes the op with an operation id and a store that can restore series.
func (l *denumberLib) run(params string) (*fakeReporter, error) {
	l.t.Helper()
	return l.runCtx(context.Background(), params)
}

// runCtx is run under a caller's context (a test that cancels mid-apply).
func (l *denumberLib) runCtx(ctx context.Context, params string) (*fakeReporter, error) {
	l.t.Helper()
	rep := &opIDReporter{id: denumberOpID}
	l.cache = &spCacheDeps{store: l.restoring(), root: l.root}
	var deps ServerDeps = l.cache
	if l.deps != nil {
		deps = l.deps(l.cache)
	}
	p := &Plugin{deps: deps}
	err := p.runSeriesDenumber(ctx, json.RawMessage(l.withReport(params)), rep)
	return &rep.fakeReporter, err
}

func (l *denumberLib) mustRun(params string) *fakeReporter {
	l.t.Helper()
	rep, err := l.run(params)
	if err != nil {
		l.t.Fatalf("runSeriesDenumber(%s): %v", params, err)
	}
	return rep
}

func (l *denumberLib) indexOf(event string) int {
	for i, e := range l.events {
		if e == event {
			return i
		}
	}
	return -1
}

func (l *denumberLib) seriesOf(id string) int {
	b := l.books[id]
	if b == nil || b.SeriesID == nil {
		return 0
	}
	return *b.SeriesID
}

func (l *denumberLib) seqOf(id string) string {
	b := l.books[id]
	if b == nil || b.SeriesSequence == nil {
		return "none"
	}
	return strconv.Itoa(*b.SeriesSequence)
}

func (l *denumberLib) createdID(name string) int {
	for _, s := range l.created {
		if s.Name == name {
			return s.ID
		}
	}
	return 0
}

func (l *denumberLib) changesFor(bookID, field string) []database.OperationChange {
	var out []database.OperationChange
	for _, c := range l.changes {
		if c.BookID == bookID && c.FieldName == field {
			out = append(out, c)
		}
	}
	return out
}

func (l *denumberLib) changesOfType(typ string) []database.OperationChange {
	var out []database.OperationChange
	for _, c := range l.changes {
		if c.ChangeType == typ {
			out = append(out, c)
		}
	}
	return out
}

func (l *denumberLib) assertNoWrites() {
	l.t.Helper()
	if len(l.events) != 0 || len(l.created) != 0 || len(l.deleted) != 0 {
		l.t.Fatalf("expected no writes, got events=%v", l.events)
	}
}

func joinLogs(rep *fakeReporter) string {
	rep.mu.Lock()
	defer rep.mu.Unlock()
	return strings.Join(rep.logs, "\n")
}

// ---------------------------------------------------------------------------
// A: reversible apply
// ---------------------------------------------------------------------------

// The apply must move EVERY version of a numbered series' books (the alternate
// rip is invisible to the listing getter), journal each move under the run's
// operation id, and journal the numbered series BEFORE deleting it.
func TestRunSeriesDenumber_ApplyMovesEveryVersionAndJournalsIt(t *testing.T) {
	const baseID, numberedID = 10, 11
	l := newDenumberLib(t)
	l.addSeries(baseID, "Court of Owls", denumberAuthor)
	l.addSeries(numberedID, "Court of Owls 05", denumberAuthor)
	l.addBook("talon-1", baseID, 1, "")
	l.addBook("owls-5-primary", numberedID, -1, "vg-owls-5")
	l.addBook("owls-5-alternate-rip", numberedID, -1, "vg-owls-5")
	l.addRunAround("Court of Owls", denumberAuthor, 5, 20)

	l.mustRun(`{"apply": true}`)

	for _, id := range []string{"owls-5-primary", "owls-5-alternate-rip"} {
		if got := l.seriesOf(id); got != baseID {
			t.Errorf("%s ended in series %d, want %d", id, got, baseID)
		}
		if got := l.seqOf(id); got != "5" {
			t.Errorf("%s position = %s, want 5 (the number the name carried)", id, got)
		}
		link := l.changesFor(id, "series_id")
		if len(link) != 1 || link[0].OldValue != strconv.Itoa(numberedID) || link[0].NewValue != strconv.Itoa(baseID) {
			t.Errorf("%s series_id journal rows = %+v, want one %d -> %d", id, link, numberedID, baseID)
		}
		pos := l.changesFor(id, "series_sequence")
		if len(pos) != 1 || pos[0].OldValue != "" || pos[0].NewValue != "5" {
			t.Errorf("%s series_sequence journal rows = %+v, want one (none) -> 5", id, pos)
		}
		if i, u := l.indexOf("change:metadata_update/series_id:"+id), l.indexOf("update:"+id); i < 0 || u < 0 || i > u {
			t.Errorf("%s: the journal row (event %d) must come before the book write (event %d)", id, i, u)
		}
		if i, j := l.indexOf("change:metadata_update/series_id:"+id), l.indexOf("change:metadata_update/series_sequence:"+id); i < 0 || j < i {
			t.Errorf("%s: the series_id row must be written before the series_sequence row (events %d, %d)", id, i, j)
		}
	}
	if l.seqOf("talon-1") != "1" || l.seriesOf("talon-1") != baseID {
		t.Errorf("a book already in the base was touched: series %d position %s", l.seriesOf("talon-1"), l.seqOf("talon-1"))
	}

	if !l.deleted[numberedID] {
		t.Fatalf("the emptied numbered series was not deleted; events=%v", l.events)
	}
	del := l.changesOfType(undo.ChangeTypeSeriesRowDelete)
	if len(del) != 1 || del[0].SeriesID == nil || *del[0].SeriesID != numberedID {
		t.Fatalf("series_row_delete rows = %+v, want exactly one for series %d", del, numberedID)
	}
	snap, err := undo.DecodeSeriesRowDelete(&del[0])
	if err != nil || snap.ID != numberedID || snap.Name != "Court of Owls 05" || snap.AuthorID == nil || *snap.AuthorID != denumberAuthor {
		t.Errorf("snapshot = %+v (err %v), want series %d \"Court of Owls 05\" by author %d", snap, err, numberedID, denumberAuthor)
	}
	if i, d := l.indexOf("change:"+undo.ChangeTypeSeriesRowDelete+"/:"+strconv.Itoa(numberedID)), l.indexOf("delete:"+strconv.Itoa(numberedID)); i < 0 || d < 0 || i > d {
		t.Errorf("the series_row_delete row (event %d) must be written before DeleteSeries (event %d)", i, d)
	}
	if len(l.changesOfType(undo.ChangeTypeSeriesCreate)) != 0 {
		t.Errorf("a series_create row was written although the base already existed")
	}
	for _, c := range l.changes {
		if c.OperationID != denumberOpID || c.ID == "" {
			t.Errorf("journal row %+v is not stamped with the run's operation id and a row id", c)
		}
	}
}

// A book that already holds the position its name carries moves with its link
// row only: no position row, because none is changed.
func TestRunSeriesDenumber_AMatchingOwnPositionIsKeptAndNotJournaled(t *testing.T) {
	l := newDenumberLib(t)
	l.addSeries(11, "Court of Owls 05", denumberAuthor)
	l.addBook("owls-5", 11, 5, "")
	l.addRunAround("Court of Owls", denumberAuthor, 5, 20)

	l.mustRun(`{"apply": true}`)

	baseID := l.createdID("Court of Owls")
	if baseID == 0 || l.seriesOf("owls-5") != baseID || l.seqOf("owls-5") != "5" {
		t.Fatalf("book in series %d at %s, want the created base %d at 5", l.seriesOf("owls-5"), l.seqOf("owls-5"), baseID)
	}
	if n := len(l.changesFor("owls-5", "series_sequence")); n != 0 {
		t.Errorf("%d series_sequence rows for a book whose position did not change", n)
	}
	if n := len(l.changesFor("owls-5", "series_id")); n != 1 {
		t.Errorf("%d series_id rows, want 1", n)
	}
}

// Dry run is the default: no write of any kind, and it says what an apply would
// do.
func TestRunSeriesDenumber_DryRunIsTheDefaultAndWritesNothing(t *testing.T) {
	l := newDenumberLib(t)
	l.addSeries(11, "Court of Owls 05", denumberAuthor)
	l.addBook("owls-5", 11, -1, "")

	rep := l.mustRun(`{}`)

	l.assertNoWrites()
	if len(l.changes) != 0 || l.seriesOf("owls-5") != 11 {
		t.Fatalf("a dry run moved or journaled something: series %d, changes %v", l.seriesOf("owls-5"), l.changes)
	}
	if !strings.Contains(joinLogs(rep), "dry run") {
		t.Errorf("summary %q does not say it is a dry run", joinLogs(rep))
	}
}

// An apply that could not be undone must not start, and must not have read or
// written anything first.
func TestRunSeriesDenumber_RefusesToApplyWithoutAnOperationID(t *testing.T) {
	l := newDenumberLib(t)
	l.addSeries(11, "Court of Owls 05", denumberAuthor)
	l.addBook("owls-5", 11, -1, "")
	p := &Plugin{deps: fakeDeps{store: l.restoring()}}

	err := p.runSeriesDenumber(context.Background(), json.RawMessage(l.withReport(`{"apply": true}`)), &fakeReporter{})
	if err == nil || !strings.Contains(err.Error(), "operation id") {
		t.Fatalf("err = %v, want a refusal naming the missing operation id", err)
	}
	l.assertNoWrites()
	if l.bulkReads != 0 {
		t.Errorf("the library was read (%d bulk reads) before the refusal", l.bulkReads)
	}
}

func TestRunSeriesDenumber_RefusesToApplyOnAStoreThatCannotRestoreASeries(t *testing.T) {
	l := newDenumberLib(t)
	l.addSeries(11, "Court of Owls 05", denumberAuthor)
	l.addBook("owls-5", 11, -1, "")
	p := &Plugin{deps: fakeDeps{store: l.store}} // a bare MockStore: no RestoreSeries

	err := p.runSeriesDenumber(context.Background(), json.RawMessage(l.withReport(`{"apply": true}`)), &opIDReporter{id: denumberOpID})
	if err == nil || !strings.Contains(err.Error(), "restore") {
		t.Fatalf("err = %v, want a refusal naming the missing restore capability", err)
	}
	l.assertNoWrites()

	// A dry run needs neither the id nor the capability.
	if err := p.runSeriesDenumber(context.Background(), json.RawMessage(`{}`), &fakeReporter{}); err != nil {
		t.Errorf("dry run refused: %v", err)
	}
}

// A journal that cannot be written must stop the book write it describes.
func TestRunSeriesDenumber_AJournalFailureStopsTheBookWrite(t *testing.T) {
	l := newDenumberLib(t)
	l.addSeries(10, "Court of Owls", denumberAuthor)
	l.addSeries(11, "Court of Owls 05", denumberAuthor)
	l.addBook("owls-5", 11, 5, "")
	l.addRunAround("Court of Owls", denumberAuthor, 5, 20)
	inner := l.store.CreateOperationChangeFunc
	l.store.CreateOperationChangeFunc = func(c *database.OperationChange) error {
		if c.ChangeType == "metadata_update" {
			return errors.New("journal unavailable")
		}
		return inner(c)
	}

	if _, err := l.run(`{"apply": true}`); err == nil {
		t.Errorf("a run whose journal write failed was reported as success")
	}

	if l.seriesOf("owls-5") != 11 || l.indexOf("update:owls-5") >= 0 {
		t.Fatalf("the book was written although its journal row failed: series %d", l.seriesOf("owls-5"))
	}
	if l.deleted[11] || len(l.changesOfType(undo.ChangeTypeSeriesRowDelete)) != 0 {
		t.Errorf("the source series was deleted with its book still in it")
	}
}

// ---------------------------------------------------------------------------
// B: a base series is created only for books that move into it
// ---------------------------------------------------------------------------

func TestRunSeriesDenumber_CreatesTheBaseOnlyWhenABookMovesIntoIt(t *testing.T) {
	l := newDenumberLib(t)
	l.addSeries(11, "Court of Owls 05", denumberAuthor)
	l.addBook("owls-5", 11, -1, "")
	l.addSeries(12, "Dark Knight 03", denumberAuthor) // empty: nothing moves
	l.addRunAround("Court of Owls", denumberAuthor, 5, 20)

	rep := l.mustRun(`{"apply": true}`)

	if len(l.created) != 1 || l.created[0].Name != "Court of Owls" {
		t.Fatalf("created %+v, want only \"Court of Owls\" (no base for the empty \"Dark Knight 03\")", l.created)
	}
	if l.deleted[12] {
		t.Errorf("the empty numbered series was deleted; that is dedup.series-prune's job, not this op's")
	}
	base := l.createdID("Court of Owls")
	if l.seriesOf("owls-5") != base {
		t.Errorf("book in series %d, want the created base %d", l.seriesOf("owls-5"), base)
	}
	cr := l.changesOfType(undo.ChangeTypeSeriesCreate)
	if len(cr) != 1 || cr[0].SeriesID == nil || *cr[0].SeriesID != base {
		t.Fatalf("series_create rows = %+v, want one for series %d", cr, base)
	}
	snap, err := undo.DecodeSeriesCreate(&cr[0])
	if err != nil || snap.ID != base || snap.Name != "Court of Owls" {
		t.Errorf("series_create snapshot = %+v (err %v)", snap, err)
	}
	if c, b := l.indexOf("create:"+strconv.Itoa(base)), l.indexOf("change:"+undo.ChangeTypeSeriesCreate+"/:"+strconv.Itoa(base)); c < 0 || b < c {
		t.Errorf("the series_create row (event %d) must follow the creation (event %d)", b, c)
	}
	if c, m := l.indexOf("change:"+undo.ChangeTypeSeriesCreate+"/:"+strconv.Itoa(base)), l.indexOf("update:owls-5"); c < 0 || m < c {
		t.Errorf("the series_create row (event %d) must be written before the first book moves in (event %d)", c, m)
	}
	if !strings.Contains(joinLogs(rep), "1 base series created") {
		t.Errorf("summary %q does not report the created base", joinLogs(rep))
	}
}

// The empty numbered series are their own category in the preview, and the
// summary names the op that cleans them up.
func TestRunSeriesDenumber_DryRunReportsEmptyNumberedSeriesSeparately(t *testing.T) {
	l := newDenumberLib(t)
	l.addSeries(11, "Court of Owls 05", denumberAuthor)
	l.addSeries(12, "Dark Knight 03", denumberAuthor)
	l.addSeries(13, "Dark Knight 04", denumberAuthor)
	l.addBook("owls-5", 11, -1, "")
	l.extraRefs[13] = 1 // referenced by a trashed row: not empty

	rep := l.mustRun(`{}`)

	logged := joinLogs(rep)
	if !strings.Contains(logged, "1 numbered series have no books at all") || !strings.Contains(logged, "dedup.series-prune") {
		t.Errorf("summary %q does not report exactly 1 empty numbered series and the op that removes it", logged)
	}
	l.assertNoWrites()
}

// ---------------------------------------------------------------------------
// C: position safety
// ---------------------------------------------------------------------------

func TestRunSeriesDenumber_HoldsABookWhoseOwnPositionDiffers(t *testing.T) {
	l := newDenumberLib(t)
	l.addSeries(10, "Court of Owls", denumberAuthor)
	l.addSeries(11, "Court of Owls 05", denumberAuthor)
	l.addBook("owls-9-own", 11, 9, "")
	l.addRunAround("Court of Owls", denumberAuthor, 5, 20)

	l.mustRun(`{"apply": true}`)

	if l.seriesOf("owls-9-own") != 11 || l.seqOf("owls-9-own") != "9" {
		t.Errorf("a book with its own position 9 was changed: series %d at %s", l.seriesOf("owls-9-own"), l.seqOf("owls-9-own"))
	}
	if l.deleted[11] {
		t.Errorf("the numbered series was deleted with a held book still in it")
	}
	if n := len(l.changesFor("owls-9-own", "series_id")) + len(l.changesFor("owls-9-own", "series_sequence")); n != 0 {
		t.Errorf("%d journal rows for a held book", n)
	}
	if report := readFile(t, l.reportPath); !strings.Contains(report, "held: own_position_differs") {
		t.Errorf("the report does not name own_position_differs:\n%s", report)
	}
}

// R1 of the round-3 review: the decision is per SOURCE SERIES. A numbered series
// that holds more than one distinct book is held whole: the book with no position
// is not carried off to the base while its neighbour stays behind.
func TestRunSeriesDenumber_HoldsASourceThatHoldsSeveralBooksWhole(t *testing.T) {
	l := newDenumberLib(t)
	l.addSeries(10, "Court of Owls", denumberAuthor)
	l.addSeries(11, "Court of Owls 05", denumberAuthor)
	l.addBook("owls-5-free", 11, -1, "")
	l.addBook("owls-9-own", 11, 9, "")
	l.addRunAround("Court of Owls", denumberAuthor, 5, 20)

	l.mustRun(`{"apply": true}`)

	if l.seriesOf("owls-5-free") != 11 || l.seqOf("owls-5-free") != "none" {
		t.Errorf("a book of a multi-book series was moved out of it: series %d at %s", l.seriesOf("owls-5-free"), l.seqOf("owls-5-free"))
	}
	if l.deleted[11] || len(l.changes) != 0 {
		t.Errorf("writes happened for a held source: deleted=%v changes=%v", l.deleted, l.changes)
	}
	if report := readFile(t, l.reportPath); !strings.Contains(report, "held: source_holds_multiple_books") {
		t.Errorf("the report does not name source_holds_multiple_books:\n%s", report)
	}
}

func TestRunSeriesDenumber_HoldsABookWhosePositionIsTakenInTheBase(t *testing.T) {
	l := newDenumberLib(t)
	l.addSeries(10, "Court of Owls", denumberAuthor)
	l.addSeries(11, "Court of Owls 05", denumberAuthor)
	l.addBook("talon-5", 10, 5, "") // already holds position 5 of the base
	l.addBook("owls-5", 11, -1, "")
	l.addRunAround("Court of Owls", denumberAuthor, 5, 20) // so the collision, not a lack of evidence, holds it

	l.mustRun(`{"apply": true}`)

	if l.seriesOf("owls-5") != 11 || l.seqOf("talon-5") != "5" || l.seriesOf("talon-5") != 10 {
		t.Fatalf("books moved onto an occupied position: owls-5 in %d, talon-5 in %d at %s",
			l.seriesOf("owls-5"), l.seriesOf("talon-5"), l.seqOf("talon-5"))
	}
	if l.deleted[11] || len(l.changes) != 0 {
		t.Errorf("writes happened for a held plan: deleted=%v changes=%v", l.deleted, l.changes)
	}
}

// Two numbered series of one base that name the same position: the run they
// would form is rejected, so neither moves (and neither is a lucky winner).
func TestRunSeriesDenumber_TwoSeriesNamingOnePositionRejectTheRun(t *testing.T) {
	l := newDenumberLib(t)
	l.addSeries(11, "Court of Owls 05", denumberAuthor)
	l.addSeries(12, "Court of Owls Book 5", denumberAuthor)
	l.addBook("owls-5-a", 11, -1, "")
	l.addBook("owls-5-b", 12, -1, "")
	// The neighbours that would make 4, 5, 6 a run.
	l.addSeries(13, "Court of Owls 04", denumberAuthor)
	l.addBook("owls-4", 13, -1, "")
	l.addSeries(14, "Court of Owls 06", denumberAuthor)
	l.addBook("owls-6", 14, -1, "")

	l.mustRun(`{"apply": true}`)

	if l.seriesOf("owls-5-a") != 11 || l.seriesOf("owls-5-b") != 12 {
		t.Errorf("colliding books moved: a in %d, b in %d", l.seriesOf("owls-5-a"), l.seriesOf("owls-5-b"))
	}
	if l.seriesOf("owls-4") != 13 || l.seriesOf("owls-6") != 14 {
		t.Errorf("a book of the rejected run moved: owls-4 in %d, owls-6 in %d", l.seriesOf("owls-4"), l.seriesOf("owls-6"))
	}
	l.assertNoWrites()
	if report := readFile(t, l.reportPath); !strings.Contains(report, "held: sibling_run_rejected") {
		t.Errorf("the report does not name sibling_run_rejected:\n%s", report)
	}
}

// Versions of one book hold one position: they are not a collision with each
// other.
func TestRunSeriesDenumber_VersionsOfOneBookDoNotCollide(t *testing.T) {
	l := newDenumberLib(t)
	l.addSeries(11, "Court of Owls 05", denumberAuthor)
	l.addBook("owls-5-primary", 11, -1, "vg")
	l.addBook("owls-5-alt", 11, -1, "vg")
	l.addRunAround("Court of Owls", denumberAuthor, 5, 20)

	l.mustRun(`{"apply": true}`)

	base := l.createdID("Court of Owls")
	if base == 0 || l.seriesOf("owls-5-primary") != base || l.seriesOf("owls-5-alt") != base {
		t.Fatalf("versions did not move together: %d and %d, base %d", l.seriesOf("owls-5-primary"), l.seriesOf("owls-5-alt"), base)
	}
}

// A book that changes between the plan and the write is decided by the row the
// write sees, not the one the plan saw.
func TestRunSeriesDenumber_TheFreshRowDecidesAtWriteTime(t *testing.T) {
	cases := []struct {
		name   string
		drift  func(b *database.Book)
		series int
		seq    string
	}{
		{"given its own position meanwhile", func(b *database.Book) { q := 9; b.SeriesSequence = &q }, 11, "9"},
		{"moved to another series meanwhile", func(b *database.Book) { s := 55; b.SeriesID = &s }, 55, "none"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l := newDenumberLib(t)
			l.addSeries(10, "Court of Owls", denumberAuthor)
			l.addSeries(11, "Court of Owls 05", denumberAuthor)
			l.addBook("owls-5", 11, -1, "")
			l.addRunAround("Court of Owls", denumberAuthor, 5, 20)
			l.afterMembership = func() {
				l.mu.Lock()
				defer l.mu.Unlock()
				tc.drift(l.books["owls-5"])
			}

			l.mustRun(`{"apply": true}`)

			if l.seriesOf("owls-5") != tc.series || l.seqOf("owls-5") != tc.seq {
				t.Errorf("book ended in series %d at %s, want %d at %s", l.seriesOf("owls-5"), l.seqOf("owls-5"), tc.series, tc.seq)
			}
			if len(l.changesFor("owls-5", "series_id")) != 0 || l.deleted[11] {
				t.Errorf("rows journaled or the series deleted for a book that was not moved")
			}
		})
	}
}

// A base somebody created while the run was deciding was never compared with the
// movers, so its plan is skipped.
func TestRunSeriesDenumber_SkipsAPlanWhoseBaseAppearedMidRun(t *testing.T) {
	l := newDenumberLib(t)
	l.addSeries(11, "Court of Owls 05", denumberAuthor)
	l.addBook("owls-5", 11, 5, "")
	l.addRunAround("Court of Owls", denumberAuthor, 5, 20)
	l.appearOnLookup = "Court of Owls"

	rep := l.mustRun(`{"apply": true}`)

	if l.seriesOf("owls-5") != 11 || len(l.created) != 0 || len(l.changes) != 0 {
		t.Errorf("a plan whose base appeared was applied: series %d, created %v", l.seriesOf("owls-5"), l.created)
	}
	if !strings.Contains(joinLogs(rep), "1 plans skipped because the base appeared mid-run") {
		t.Errorf("summary %q does not report the skipped plan", joinLogs(rep))
	}
}

// A base name that resolves to a numbered series this run folds elsewhere takes
// no books: they would be stranded when that series is merged away. The grammar
// keeps a digit out of every base, so no name the planner reads resolves this
// way today; the store, which matches names its own way, is simulated here, and
// the decision must still hold both plans.
func TestRunSeriesDenumber_AChainedBaseTakesNoBooksThisRun(t *testing.T) {
	l := newDenumberLib(t)
	l.addSeries(2, "Court of Owls 04", denumberAuthor)
	l.addSeries(3, "Court of Owls 05", denumberAuthor)
	l.addBook("x1", 2, 4, "")
	l.addBook("y1", 3, 5, "")
	inner := l.store.GetSeriesByNameFunc
	l.store.GetSeriesByNameFunc = func(name string, authorID *int) (*database.Series, error) {
		if strings.EqualFold(name, "Court of Owls") { // the base "resolves" onto numbered series 2
			a := denumberAuthor
			return &database.Series{ID: 2, Name: "Court of Owls 04", AuthorID: &a}, nil
		}
		return inner(name, authorID)
	}

	rep := l.mustRun(`{"apply": true}`)

	for id, want := range map[string]int{"x1": 2, "y1": 3} {
		if got := l.seriesOf(id); got != want || l.deleted[want] {
			t.Errorf("%s moved into a series this run deletes (series %d, deleted=%v)", id, got, l.deleted[want])
		}
	}
	l.assertNoWrites()
	if !strings.Contains(joinLogs(rep), "nothing to apply") {
		t.Errorf("summary %q should say there is nothing to apply", joinLogs(rep))
	}
}

// ---------------------------------------------------------------------------
// D: shapes that are not positions
// ---------------------------------------------------------------------------

func TestRunSeriesDenumber_NeverAppliesAWrongShape(t *testing.T) {
	// Every book holds the position its name carries, so the library corroborates
	// each number and only the shape check can hold it: the report must name it.
	cases := []struct {
		name, series string
		seq          int
		why          string
	}{
		{"file part marker", "Court of Owls Part 3", 3, "part_or_format_word"},
		{"file part marker, glued", "Court of Owls-Part03", 3, "no_separator"},
		{"position of 100 or more", "Court of Owls Book 150", 50, "number_out_of_range"},
		{"base equal to the author's name", "Bruce Wayne 05", 5, "base_is_author_name"},
		{"base equal to another author's name", "Alfred Pennyworth 05", 5, "base_is_author_name"},
		{"base with a leftover trailing digit", "Court of Owls 2 05", 5, "base_has_digit"},
		{"two letter base", "Ab 05", 5, "base_too_short"},
		{"chapter base", "Chap-12 03", 3, "part_or_format_word"},
		{"digit-leading base", "9 Lives 05", 5, "base_has_digit"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l := newDenumberLib(t)
			l.addSeries(11, tc.series, denumberAuthor)
			l.addBook("owls", 11, tc.seq, "")

			rep := l.mustRun(`{"apply": true}`)

			l.assertNoWrites()
			if l.seriesOf("owls") != 11 || len(l.changes) != 0 {
				t.Fatalf("%q was applied: book in series %d", tc.series, l.seriesOf("owls"))
			}
			if !strings.Contains(joinLogs(rep), "nothing to apply") {
				t.Errorf("summary %q should say there is nothing to apply", joinLogs(rep))
			}
			if report := readFile(t, l.reportPath); !strings.Contains(report, tc.why) {
				t.Errorf("the report does not name %q as the reason:\n%s", tc.why, report)
			}
		})
	}
}

func TestRunSeriesDenumber_NeverAppliesASeriesWithNoAuthor(t *testing.T) {
	l := newDenumberLib(t)
	l.addSeries(11, "Court of Owls 05", 0)
	l.addBook("owls-5", 11, 5, "")

	l.mustRun(`{"apply": true}`)

	l.assertNoWrites()
	if report := readFile(t, l.reportPath); !strings.Contains(report, "no_author") {
		t.Errorf("the report does not name no_author:\n%s", report)
	}
}

// An author row that stands for "no known author" holds like author id 0.
func TestRunSeriesDenumber_NeverAppliesASeriesOfAPlaceholderAuthor(t *testing.T) {
	for _, name := range []string{"Unknown", "Various Authors", "Various", "N/A", "Audiobook"} {
		t.Run(name, func(t *testing.T) {
			l := newDenumberLib(t)
			l.authors = append(l.authors, database.Author{ID: 40, Name: name})
			l.addSeries(11, "Court of Owls 05", 40)
			l.addBook("owls-5", 11, 5, "")

			l.mustRun(`{"apply": true}`)

			l.assertNoWrites()
			if report := readFile(t, l.reportPath); !strings.Contains(report, "placeholder_author") {
				t.Errorf("the report does not name placeholder_author:\n%s", report)
			}
		})
	}
}

// The shapes that must keep applying: a keyword the name vouches for.
func TestRunSeriesDenumber_StillAppliesTheKeywordShapes(t *testing.T) {
	l := newDenumberLib(t)
	l.addSeries(11, "Court of Owls: Book 11", denumberAuthor)
	l.addSeries(12, "Court of Owls, Volume 3", denumberAuthor)
	l.addBook("owls-11", 11, 11, "")
	l.addBook("owls-3", 12, 3, "")
	l.addRunAround("Court of Owls", denumberAuthor, 11, 20) // 10, 11, 12
	l.addRunAround("Court of Owls", denumberAuthor, 3, 30)  // 2, 3, 4

	l.mustRun(`{"apply": true}`)

	base := l.createdID("Court of Owls")
	if base == 0 {
		t.Fatalf("no base created; events=%v", l.events)
	}
	if l.seriesOf("owls-11") != base || l.seqOf("owls-11") != "11" || l.seriesOf("owls-3") != base || l.seqOf("owls-3") != "3" {
		t.Errorf("owls-11 in %d at %s, owls-3 in %d at %s, want both in %d at 11 and 3",
			l.seriesOf("owls-11"), l.seqOf("owls-11"), l.seriesOf("owls-3"), l.seqOf("owls-3"), base)
	}
}

// A base that exists for one author is that author's. A second author's numbered
// series is held rather than given a second base of the same name beside it.
func TestRunSeriesDenumber_ABaseOfAnotherAuthorIsNeverDuplicated(t *testing.T) {
	l := newDenumberLib(t)
	l.addSeries(11, "Court of Owls 05", denumberAuthor)
	l.addSeries(12, "Court of Owls 05", denumberAuthor+1)
	l.addSeries(10, "Court of Owls", denumberAuthor) // exists for the first author only
	l.addBook("bruce-owls", 11, 5, "")
	l.addBook("alfred-owls", 12, 5, "")
	l.addRunAround("Court of Owls", denumberAuthor, 5, 20)
	l.addRunAround("Court of Owls", denumberAuthor+1, 5, 30)

	l.mustRun(`{"apply": true}`)

	if l.seriesOf("bruce-owls") != 10 {
		t.Errorf("the first author's book is in series %d, want their own base 10", l.seriesOf("bruce-owls"))
	}
	if len(l.created) != 0 {
		t.Fatalf("created %+v: a second base of the same name was made under another author row", l.created)
	}
	if l.seriesOf("alfred-owls") != 12 || l.deleted[12] {
		t.Errorf("the second author's book moved or its series was deleted: series %d", l.seriesOf("alfred-owls"))
	}
	if report := readFile(t, l.reportPath); !strings.Contains(report, "base_under_another_author") {
		t.Errorf("the report does not name base_under_another_author:\n%s", report)
	}
}

// With no base for either author, each author's numbered series folds into a
// base of their own. Merged by name alone the two Court of Owls 05 books would
// share one position of one base and both be held.
func TestRunSeriesDenumber_ASharedBaseNameGetsOneBasePerAuthorWhenNoneExists(t *testing.T) {
	l := newDenumberLib(t)
	l.addSeries(11, "Court of Owls 05", denumberAuthor)
	l.addSeries(12, "Court of Owls 05", denumberAuthor+1)
	l.addBook("bruce-owls", 11, 5, "")
	l.addBook("alfred-owls", 12, 5, "")
	l.addRunAround("Court of Owls", denumberAuthor, 5, 20)
	l.addRunAround("Court of Owls", denumberAuthor+1, 5, 30)

	l.mustRun(`{"apply": true}`)

	if len(l.created) != 2 {
		t.Fatalf("created %+v, want one base per author", l.created)
	}
	for _, id := range []string{"bruce-owls", "alfred-owls"} {
		if l.seriesOf(id) == 11 || l.seriesOf(id) == 12 || l.seqOf(id) != "5" {
			t.Errorf("%s was not folded: series %d at %s", id, l.seriesOf(id), l.seqOf(id))
		}
	}
	if l.seriesOf("bruce-owls") == l.seriesOf("alfred-owls") {
		t.Errorf("both authors' books landed in series %d", l.seriesOf("bruce-owls"))
	}
}

// A plan whose only book is held moves nothing, so it must not create a base.
func TestRunSeriesDenumber_ANumberedSeriesWhoseBooksAreAllHeldCreatesNoBase(t *testing.T) {
	l := newDenumberLib(t)
	l.addSeries(11, "Court of Owls 05", denumberAuthor)
	l.addBook("owls-9-own", 11, 9, "")

	l.mustRun(`{"apply": true}`)

	if len(l.created) != 0 {
		t.Errorf("created %+v for a plan that moves nothing", l.created)
	}
	if l.seriesOf("owls-9-own") != 11 || l.seqOf("owls-9-own") != "9" || l.deleted[11] {
		t.Errorf("the held book or its series changed: series %d at %s, deleted=%v", l.seriesOf("owls-9-own"), l.seqOf("owls-9-own"), l.deleted[11])
	}
}

// A series a repair holds (Series.HeldBy) is never deleted, even when this run
// moves every book out of it: that repair's own undo needs the row.
func TestRunSeriesDenumber_ASeriesAnotherRepairHoldsIsMergedButNotDeleted(t *testing.T) {
	l := newDenumberLib(t)
	l.addSeries(10, "Court of Owls", denumberAuthor)
	l.addSeries(11, "Court of Owls 05", denumberAuthor)
	l.series[len(l.series)-1].HeldBy = "series-prune-fixer"
	l.addBook("owls-5", 11, 5, "")
	l.addRunAround("Court of Owls", denumberAuthor, 5, 20)

	l.mustRun(`{"apply": true}`)

	if l.seriesOf("owls-5") != 10 {
		t.Fatalf("the book did not move: series %d", l.seriesOf("owls-5"))
	}
	if l.deleted[11] {
		t.Errorf("a held series was deleted")
	}
	if rows := l.changesOfType(undo.ChangeTypeSeriesRowDelete); len(rows) != 0 {
		t.Errorf("%d series_row_delete rows for a delete that must not happen", len(rows))
	}
}

// The series list is cached for a day, so an apply that changed it must drop the
// cache; a dry run and an apply that changed nothing must not.
func TestRunSeriesDenumber_AnApplyThatChangedSeriesDropsTheSeriesCache(t *testing.T) {
	l := newDenumberLib(t)
	l.addSeries(11, "Court of Owls 05", denumberAuthor)
	l.addBook("owls-5", 11, 5, "")
	l.addRunAround("Court of Owls", denumberAuthor, 5, 20)
	l.mustRun(`{}`)
	if n := l.cache.series.Load(); n != 0 {
		t.Errorf("a dry run dropped the series cache %d times", n)
	}
	l.mustRun(`{"apply": true}`)
	if n := l.cache.series.Load(); n < 1 {
		t.Errorf("an apply that moved a book and created a base never dropped the series cache")
	}

	held := newDenumberLib(t)
	held.addSeries(11, "Court of Owls 05", denumberAuthor)
	held.addBook("owls-9-own", 11, 9, "")
	held.mustRun(`{"apply": true}`)
	if n := held.cache.series.Load(); n != 0 {
		t.Errorf("an apply that changed nothing dropped the series cache %d times", n)
	}
}

// ---------------------------------------------------------------------------
// E: canary controls
// ---------------------------------------------------------------------------

func canaryLib(t *testing.T) *denumberLib {
	t.Helper()
	l := newDenumberLib(t)
	// Sorted by base name then position, the plans are:
	//   Arkham Cell 01 (empty) | 02, 03, 04 (one book each: a run) | Court of Owls 05 (2 books:
	//   held whole) | Dark Knight 03 (1 book, with neighbours 02 and 04 that hold their own positions)
	l.addSeries(21, "Arkham Cell 01", denumberAuthor)
	l.addSeries(22, "Arkham Cell 02", denumberAuthor)
	l.addSeries(25, "Arkham Cell 03", denumberAuthor)
	l.addSeries(26, "Arkham Cell 04", denumberAuthor)
	l.addSeries(23, "Court of Owls 05", denumberAuthor)
	l.addSeries(24, "Dark Knight 03", denumberAuthor)
	l.addBook("cell-2", 22, 2, "")
	l.addBook("cell-3", 25, 3, "")
	l.addBook("cell-4", 26, 4, "")
	l.addBook("owls-5-a", 23, 5, "vg-a")
	l.addBook("owls-5-b", 23, 5, "vg-b") // two distinct books in one series: held whole
	l.addBook("dark-3", 24, 3, "")
	l.addRunAround("Dark Knight", denumberAuthor, 3, 40)
	return l
}

func movedBooks(l *denumberLib) []string {
	var out []string
	for _, c := range l.changes {
		if c.FieldName == "series_id" {
			out = append(out, c.BookID)
		}
	}
	sort.Strings(out)
	return out
}

// Plans that move nothing never take a canary slot: limit 1 must move the first
// plan that carries a book, not stop at the empty series sorted before it.
func TestRunSeriesDenumber_LimitCountsOnlyPlansThatCarryBooks(t *testing.T) {
	l := canaryLib(t)

	l.mustRun(`{"apply": true, "limit": 1}`)

	if got := movedBooks(l); len(got) != 1 || got[0] != "cell-2" {
		t.Fatalf("limit 1 moved %v, want only cell-2 (the first plan that carries a book)", got)
	}
}

func TestRunSeriesDenumber_MinBooksAndBaseNameScopeTheApply(t *testing.T) {
	l := canaryLib(t)
	l.mustRun(`{"apply": true, "baseName": "dark knight"}`)
	if got := movedBooks(l); len(got) != 1 || got[0] != "dark-3" {
		t.Fatalf("baseName moved %v, want only dark-3 (case-insensitive exact match)", got)
	}

	l2 := canaryLib(t)
	l2.mustRun(`{"apply": true, "minBooks": 2}`)
	if got := movedBooks(l2); len(got) != 0 {
		t.Fatalf("minBooks 2 moved %v: every plan here moves one book or none", got)
	}
}

func TestRunSeriesDenumber_TheReportCoversEveryPlanWhateverTheCanarySelects(t *testing.T) {
	l := canaryLib(t)
	path := filepath.Join(l.root, ".reports", "report.tsv")

	l.mustRun(fmt.Sprintf(`{"apply": true, "limit": 1, "reportPath": %q}`, path))

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("report not written: %v", err)
	}
	report := string(raw)
	for _, name := range []string{"Arkham Cell 01", "Arkham Cell 02", "Court of Owls 05", "Dark Knight 03"} {
		if !strings.Contains(report, name) {
			t.Errorf("the report does not list %q although the canary left it out", name)
		}
	}
	if !strings.Contains(report, "not_selected") || !strings.Contains(report, "held: source_holds_multiple_books") {
		t.Errorf("the report does not mark unselected plans and held books:\n%s", report)
	}
	applied, err := os.ReadFile(path + ".applied")
	if err != nil || !strings.Contains(string(applied), "created_series") || !strings.Contains(string(applied), "moved") {
		t.Errorf("outcome report missing or incomplete (err %v):\n%s", err, applied)
	}
}

// An apply nobody can audit does not start.
func TestRunSeriesDenumber_AnUnwritableReportStopsTheApply(t *testing.T) {
	l := canaryLib(t)
	dir := filepath.Join(l.root, ".reports")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	blocker := filepath.Join(dir, "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := l.run(fmt.Sprintf(`{"apply": true, "reportPath": %q}`, filepath.Join(blocker, "report.tsv")))
	if err == nil || !strings.Contains(err.Error(), "report") {
		t.Fatalf("err = %v, want a failure naming the report", err)
	}
	l.assertNoWrites()
}

// ---------------------------------------------------------------------------
// G: fail-closed parameters
// ---------------------------------------------------------------------------

func TestRunSeriesDenumber_RejectsUnknownAndInvalidParams(t *testing.T) {
	for _, raw := range []string{
		`{"apply": true, "baseNme": "Court of Owls"}`,
		`{"aply": true}`,
		`{"apply": true, "limit": -1}`,
		`{"apply": true, "minBooks": -2}`,
		`{"apply": true, "apply": false}`,
	} {
		l := newDenumberLib(t)
		l.addSeries(11, "Court of Owls 05", denumberAuthor)
		l.addBook("owls-5", 11, -1, "")
		if _, err := l.run(raw); err == nil {
			t.Errorf("%s: accepted; an op that writes must reject what it does not understand", raw)
		}
		l.assertNoWrites()
	}
}

// ---------------------------------------------------------------------------
// The unfiltered reference guard and the membership reads
// ---------------------------------------------------------------------------

// Rows the membership read cannot see (trashed) still hold the numbered series:
// the books it can see move, and the series is NOT deleted.
func TestRunSeriesDenumber_KeepsASeriesThatRowsOutsideTheMembershipReadStillHold(t *testing.T) {
	l := newDenumberLib(t)
	l.addSeries(10, "Court of Owls", denumberAuthor)
	l.addSeries(11, "Court of Owls 05", denumberAuthor)
	l.addBook("owls-5", 11, 5, "")
	l.addRunAround("Court of Owls", denumberAuthor, 5, 20)
	l.extraRefs[11] = 1

	l.mustRun(`{"apply": true}`)

	if l.seriesOf("owls-5") != 10 {
		t.Errorf("the visible book did not move: series %d", l.seriesOf("owls-5"))
	}
	if l.deleted[11] || len(l.changesOfType(undo.ChangeTypeSeriesRowDelete)) != 0 {
		t.Errorf("series 11 was deleted while a trashed row still references it")
	}
}

// A series whose only members are trashed has no books to move: it is not a plan
// to apply, and is not deleted.
func TestRunSeriesDenumber_DoesNotDeleteASeriesWithOnlyTrashedMembers(t *testing.T) {
	l := newDenumberLib(t)
	l.addSeries(10, "Court of Owls", denumberAuthor)
	l.addSeries(11, "Court of Owls 05", denumberAuthor)
	l.extraRefs[11] = 2

	l.mustRun(`{"apply": true}`)

	l.assertNoWrites()
}

func TestRunSeriesDenumber_FailsClosedWhenGuardCannotAnswer(t *testing.T) {
	for _, apply := range []bool{false, true} {
		l := newDenumberLib(t)
		l.addSeries(10, "Court of Owls", denumberAuthor)
		l.addSeries(11, "Court of Owls 05", denumberAuthor)
		l.addBook("owls-5", 11, -1, "")
		l.store.GetAllSeriesBookRefCountsFunc = func() (map[int]int, error) {
			return nil, errors.New("boom: store cannot compute unfiltered series references")
		}
		if _, err := l.run(fmt.Sprintf(`{"apply": %v}`, apply)); err == nil {
			t.Errorf("apply=%v: succeeded without the unfiltered reference counts", apply)
		}
		l.assertNoWrites()
	}
}

func TestRunSeriesDenumber_FailsClosedWithoutBookCounts(t *testing.T) {
	for _, apply := range []bool{false, true} {
		l := newDenumberLib(t)
		l.addSeries(11, "Court of Owls 05", denumberAuthor)
		l.addBook("owls-5", 11, -1, "")
		l.store.GetAllSeriesBookCountsFunc = func() (map[int]int, error) { return nil, errors.New("counts unavailable") }
		if _, err := l.run(fmt.Sprintf(`{"apply": %v}`, apply)); err == nil {
			t.Errorf("apply=%v: ran without the book counts", apply)
		}
		l.assertNoWrites()
	}
}

func TestRunSeriesDenumber_FailsClosedWithoutMembership(t *testing.T) {
	for _, apply := range []bool{false, true} {
		l := newDenumberLib(t)
		l.addSeries(10, "Court of Owls", denumberAuthor)
		l.addSeries(11, "Court of Owls 05", denumberAuthor)
		l.addBook("owls-5", 11, -1, "")
		l.store.GetBooksBySeriesIDsAllVersionsFunc = func([]int) (map[int][]database.BookCore, error) {
			return nil, errors.New("scan failed")
		}
		_, err := l.run(fmt.Sprintf(`{"apply": %v}`, apply))
		if err == nil || !strings.Contains(err.Error(), "series membership") {
			t.Errorf("apply=%v: err = %v, want a failure naming the membership read", apply, err)
		}
		l.assertNoWrites()
	}
}

// The membership read happens once per run, for every source and target, and
// never per series (a per-series read is a full book scan once the cache is
// tainted).
func TestRunSeriesDenumber_ReadsMembershipOncePerRun(t *testing.T) {
	for _, apply := range []bool{false, true} {
		l := canaryLib(t)
		l.mustRun(fmt.Sprintf(`{"apply": %v}`, apply))
		if l.bulkReads != 1 || l.perSeriesReads != 0 {
			t.Errorf("apply=%v: bulk reads %d, per-series reads %d, want 1 and 0", apply, l.bulkReads, l.perSeriesReads)
		}
	}
}

// The dry run reports the plans the guard would keep from deleting.
func TestRunSeriesDenumber_DryRunReportsGuardHeldBack(t *testing.T) {
	l := newDenumberLib(t)
	l.addSeries(10, "Court of Owls", denumberAuthor)
	l.addSeries(11, "Court of Owls 05", denumberAuthor)
	l.addBook("owls-5", 11, 5, "")
	l.addRunAround("Court of Owls", denumberAuthor, 5, 20)
	l.extraRefs[11] = 2

	rep := l.mustRun(`{}`)

	logged := joinLogs(rep)
	if !strings.Contains(logged, "1 would be merged but NOT deleted") {
		t.Errorf("dry-run summary %q does not report the guard-held plan", logged)
	}
	l.assertNoWrites()
}

// m1: a book's versions move together or not at all. The second version's write
// fails after the first landed; the first is taken back, so the book is not split
// across the numbered series and the base, its journal rows are voided, and the
// series is kept.
func TestRunSeriesDenumber_AFailedWriteKeepsTheSeriesAndTheVersionsTogether(t *testing.T) {
	l := newDenumberLib(t)
	l.addSeries(10, "Court of Owls", denumberAuthor)
	l.addSeries(11, "Court of Owls 05", denumberAuthor)
	l.addBook("owls-5-primary", 11, -1, "vg")
	l.addBook("owls-5-alt", 11, -1, "vg")
	l.addRunAround("Court of Owls", denumberAuthor, 5, 20)
	// The versions are written in id order: the alternate first, the primary second.
	l.failUpdate["owls-5-primary"] = true

	if _, err := l.run(`{"apply": true}`); err == nil {
		t.Errorf("a run with a failed write was reported as success")
	}

	if l.seriesOf("owls-5-alt") != 11 || l.seriesOf("owls-5-primary") != 11 {
		t.Fatalf("the book's versions were split: alt in %d, primary in %d (both must be back in 11)", l.seriesOf("owls-5-alt"), l.seriesOf("owls-5-primary"))
	}
	if l.seqOf("owls-5-alt") != "none" {
		t.Errorf("the version taken back kept the position %s it was given", l.seqOf("owls-5-alt"))
	}
	if l.deleted[11] {
		t.Errorf("the series was deleted while a book still points at it")
	}
	if len(l.changesOfType(undo.ChangeTypeSeriesRowDelete)) != 0 {
		t.Errorf("a series_row_delete row was written for a series that was kept")
	}
	// The rows of the move that was taken back are voided: a revert never acts on
	// them.
	voided := 0
	for _, c := range l.changesFor("owls-5-alt", "series_id") {
		if c.Voided {
			voided++
		}
	}
	if voided < 1 {
		t.Errorf("no voided series_id row for the version that was taken back: %+v", l.changesFor("owls-5-alt", "series_id"))
	}
}

// A delete that fails is counted as a failure in the summary, and the series
// stays (its journal row is harmless: the revert finds the series live).
func TestRunSeriesDenumber_AFailedDeleteIsCounted(t *testing.T) {
	l := newDenumberLib(t)
	l.addSeries(10, "Court of Owls", denumberAuthor)
	l.addSeries(11, "Court of Owls 05", denumberAuthor)
	l.addBook("owls-5", 11, 5, "")
	l.addRunAround("Court of Owls", denumberAuthor, 5, 20)
	l.store.DeleteSeriesFunc = func(int) error { return errors.New("delete refused") }

	rep, err := l.run(`{"apply": true}`)
	if err == nil {
		t.Errorf("a run with a failed delete was reported as success")
	}

	if l.seriesOf("owls-5") != 10 || l.deleted[11] {
		t.Fatalf("fixture: book in %d, deleted=%v", l.seriesOf("owls-5"), l.deleted)
	}
	if !strings.Contains(joinLogs(rep), "failed 1)") {
		t.Errorf("summary %q does not count the failed delete", joinLogs(rep))
	}
}
