// file: internal/plugins/metafetch/asin_backfill_test.go
// version: 1.0.0
// guid: 2b8f4c71-6e05-4d39-a1c7-9e3d0f5a8b26
// last-edited: 2026-10-01

package metafetch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

// asinTestReporter records what the op reports. SetCurrentItem (fed by the
// RunItems Label closure, which runs inside the workers) and Checkpoint are
// mutex-guarded.
type asinTestReporter struct {
	stubReporter
	mu          sync.Mutex
	labels      int
	checkpoints []any
	result      any
}

func (r *asinTestReporter) SetCurrentItem(string) {
	r.mu.Lock()
	r.labels++
	r.mu.Unlock()
}

func (r *asinTestReporter) Checkpoint(v any) error {
	r.mu.Lock()
	r.checkpoints = append(r.checkpoints, v)
	r.mu.Unlock()
	return nil
}

func (r *asinTestReporter) SetResult(v any) error {
	r.mu.Lock()
	r.result = v
	r.mu.Unlock()
	return nil
}

func (r *asinTestReporter) res(t *testing.T) asinBackfillResult {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	res, ok := r.result.(asinBackfillResult)
	if !ok {
		t.Fatalf("result = %#v, want asinBackfillResult", r.result)
	}
	return res
}

// fakeAudible answers by title (lowercased) and by keyword, and counts calls.
type fakeAudible struct {
	mu      sync.Mutex
	byTitle map[string][]metadata.AudibleIdentity
	byKW    map[string][]metadata.AudibleIdentity
	err     error
	calls   atomic.Int64
	delay   time.Duration
}

func (f *fakeAudible) SearchIdentities(ctx context.Context, q metadata.AudibleIdentityQuery) ([]metadata.AudibleIdentity, error) {
	f.calls.Add(1)
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	if f.err != nil {
		return nil, f.err
	}
	if q.Author == "" && q.Keywords == "" {
		return nil, errors.New("test: title search without an author")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if q.Keywords != "" {
		return f.byKW[q.Keywords], nil
	}
	return f.byTitle[strings.ToLower(q.Title)], nil
}

func newASINTestPlugin(t *testing.T, fa *fakeAudible) (*Plugin, *database.PebbleStore) {
	t.Helper()
	store, err := database.NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	p := New(store, nil)
	p.newAudible = func() audibleIdentitySearcher { return fa }
	return p, store
}

func mkAuthor(t *testing.T, s *database.PebbleStore, name string) int {
	t.Helper()
	a, err := s.CreateAuthor(name)
	if err != nil {
		t.Fatal(err)
	}
	return a.ID
}

func mkBook(t *testing.T, s *database.PebbleStore, b *database.Book) *database.Book {
	t.Helper()
	got, err := s.CreateBook(b)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func runASIN(t *testing.T, p *Plugin, params string) *asinTestReporter {
	t.Helper()
	rep := &asinTestReporter{}
	if err := p.runASINBackfill(context.Background(), json.RawMessage(params), rep); err != nil {
		t.Fatalf("runASINBackfill(%s): %v", params, err)
	}
	return rep
}

func asinOf(t *testing.T, s *database.PebbleStore, id string) string {
	t.Helper()
	b, err := s.GetBookByID(id)
	if err != nil || b == nil {
		t.Fatalf("GetBookByID(%s): %v", id, err)
	}
	if b.ASIN == nil {
		return ""
	}
	return *b.ASIN
}

// rrProduct is the Audible product for "Red Rising" by Pierce Brown, ISBN-
// corroborated against isbnRR.
const isbnRR = "9781470380281"

func rrProduct() metadata.AudibleIdentity {
	return metadata.AudibleIdentity{ASIN: "B00I2VWW5U", Title: "Red Rising", Authors: []string{"Pierce Brown"}, ISBN: isbnRR, RuntimeMin: 972}
}

func rrFake() *fakeAudible {
	return &fakeAudible{
		byTitle: map[string][]metadata.AudibleIdentity{"red rising": {rrProduct()}},
		byKW:    map[string][]metadata.AudibleIdentity{isbnRR: {rrProduct()}},
	}
}

func TestASINBackfill_WritesWithHistory(t *testing.T) {
	fa := rrFake()
	p, s := newASINTestPlugin(t, fa)
	aid := mkAuthor(t, s, "Pierce Brown")
	b := mkBook(t, s, &database.Book{Title: "Red Rising", AuthorID: &aid, ISBN13: new(isbnRR), FilePath: "/x/rr.m4b"})

	rep := runASIN(t, p, `{"dry_run":false}`)
	if got := asinOf(t, s, b.ID); got != "B00I2VWW5U" {
		t.Fatalf("asin = %q, want B00I2VWW5U", got)
	}
	res := rep.res(t)
	if res.MatchedWritten != 1 || res.Searched != 1 || res.DryRun {
		t.Fatalf("result = %+v", res)
	}
	if res.Evidence[evidenceISBN] != 1 {
		t.Fatalf("evidence = %v, want isbn", res.Evidence)
	}
	hist, err := s.GetBookChangeHistory(b.ID, 100)
	if err != nil {
		t.Fatal(err)
	}
	var asinRows int
	for _, h := range hist {
		if h.Field != "asin" {
			t.Fatalf("unexpected history field %q (%s): only asin may change", h.Field, h.ChangeType)
		}
		if h.ChangeType != asinHistoryChangeType || h.Source != asinHistorySource || h.BatchID == "" {
			t.Fatalf("history row = %+v", h)
		}
		asinRows++
	}
	if asinRows != 1 {
		t.Fatalf("asin history rows = %d, want 1", asinRows)
	}
}

func TestASINBackfill_DefaultIsDryRunAndWritesNothing(t *testing.T) {
	fa := rrFake()
	fa.byTitle["dune"] = nil // a no-match book: a dry run must not leave a marker either
	p, s := newASINTestPlugin(t, fa)
	aid := mkAuthor(t, s, "Pierce Brown")
	b := mkBook(t, s, &database.Book{Title: "Red Rising", AuthorID: &aid, ISBN13: new(isbnRR)})
	miss := mkBook(t, s, &database.Book{Title: "Dune", AuthorID: &aid})

	rep := runASIN(t, p, `{}`)
	if got := asinOf(t, s, b.ID); got != "" {
		t.Fatalf("dry run wrote asin %q", got)
	}
	res := rep.res(t)
	if !res.DryRun || res.MatchedWritten != 1 || res.NoMatch != 1 {
		t.Fatalf("result = %+v", res)
	}
	if raw, _ := s.GetRaw(asinMissKey(miss.ID)); raw != nil {
		t.Fatalf("dry run wrote a no-match marker: %s", raw)
	}
	if hist, _ := s.GetBookChangeHistory(b.ID, 100); len(hist) != 0 {
		t.Fatalf("dry run recorded history: %+v", hist)
	}
}

func TestASINBackfill_SkipsLockedHasASINNoAuthorDeleted(t *testing.T) {
	fa := rrFake()
	p, s := newASINTestPlugin(t, fa)
	aid := mkAuthor(t, s, "Pierce Brown")
	locked := mkBook(t, s, &database.Book{Title: "Red Rising", AuthorID: &aid, ISBN13: new(isbnRR)})
	if err := s.UpsertMetadataFieldState(&database.MetadataFieldState{
		BookID: locked.ID, Field: database.FieldKeyASIN, OverrideValue: new(`""`), OverrideLocked: true,
	}); err != nil {
		t.Fatal(err)
	}
	has := mkBook(t, s, &database.Book{Title: "Red Rising", AuthorID: &aid, ASIN: new("B000KEEPME")})
	noAuthor := mkBook(t, s, &database.Book{Title: "Red Rising"})
	deleted := mkBook(t, s, &database.Book{Title: "Red Rising", AuthorID: &aid, MarkedForDeletion: new(true)})

	rep := runASIN(t, p, `{"dry_run":false}`)
	res := rep.res(t)
	// The library walk never lists a soft-deleted book (the store filters it).
	if res.SkippedLocked != 1 || res.SkippedHasASIN != 1 || res.SkippedNoAuthor != 1 || res.Scanned != 3 {
		t.Fatalf("result = %+v", res)
	}
	// Named explicitly, it is read by ID and must still be refused.
	if res := runASIN(t, p, `{"dry_run":false,"book_ids":["`+deleted.ID+`"]}`).res(t); res.SkippedDeleted != 1 {
		t.Fatalf("book_ids result = %+v, want the deleted book skipped", res)
	}
	if fa.calls.Load() != 0 {
		t.Fatalf("Audible called %d times for books that must all be skipped", fa.calls.Load())
	}
	if asinOf(t, s, locked.ID) != "" || asinOf(t, s, noAuthor.ID) != "" || asinOf(t, s, deleted.ID) != "" {
		t.Fatal("a skipped book was written")
	}
	if got := asinOf(t, s, has.ID); got != "B000KEEPME" {
		t.Fatalf("existing asin changed to %q", got)
	}
}

func TestASINBackfill_AmbiguousWritesNothingAndIsRemembered(t *testing.T) {
	twin := rrProduct()
	twin.ASIN = "B0TWIN0001"
	fa := &fakeAudible{byTitle: map[string][]metadata.AudibleIdentity{"red rising": {rrProduct(), twin}}}
	p, s := newASINTestPlugin(t, fa)
	aid := mkAuthor(t, s, "Pierce Brown")
	b := mkBook(t, s, &database.Book{Title: "Red Rising", AuthorID: &aid, ISBN13: new(isbnRR)})

	res := runASIN(t, p, `{"dry_run":false}`).res(t)
	if res.Ambiguous != 1 || res.MatchedWritten != 0 {
		t.Fatalf("result = %+v", res)
	}
	if got := asinOf(t, s, b.ID); got != "" {
		t.Fatalf("ambiguous book written: %q", got)
	}
	raw, err := s.GetRaw(asinMissKey(b.ID))
	if err != nil || raw == nil {
		t.Fatalf("no-match marker missing (err %v)", err)
	}

	calls := fa.calls.Load()
	res = runASIN(t, p, `{"dry_run":false}`).res(t)
	if res.SkippedRecentMiss != 1 || fa.calls.Load() != calls {
		t.Fatalf("second run re-searched a remembered miss: %+v, calls %d -> %d", res, calls, fa.calls.Load())
	}

	res = runASIN(t, p, `{"dry_run":false,"retry_after_days":0}`).res(t)
	if res.SkippedRecentMiss != 0 || res.Ambiguous != 1 {
		t.Fatalf("retry_after_days=0 did not re-search: %+v", res)
	}
}

func TestASINBackfill_ExpiredMissIsSearchedAgain(t *testing.T) {
	fa := rrFake()
	p, s := newASINTestPlugin(t, fa)
	aid := mkAuthor(t, s, "Pierce Brown")
	b := mkBook(t, s, &database.Book{Title: "Red Rising", AuthorID: &aid, ISBN13: new(isbnRR)})
	old, _ := json.Marshal(asinMissMarker{At: time.Now().Add(-31 * 24 * time.Hour), Outcome: asinOutcomeNoMatch})
	if err := s.SetRaw(asinMissKey(b.ID), old); err != nil {
		t.Fatal(err)
	}
	res := runASIN(t, p, `{"dry_run":false}`).res(t)
	if res.MatchedWritten != 1 {
		t.Fatalf("31-day-old miss was not searched again: %+v", res)
	}
}

func TestASINBackfill_SearchErrorIsCountedNotMarkedAndTrips(t *testing.T) {
	fa := &fakeAudible{err: errors.New("503 from audible")}
	p, s := newASINTestPlugin(t, fa)
	aid := mkAuthor(t, s, "Pierce Brown")
	first := mkBook(t, s, &database.Book{Title: "Red Rising", AuthorID: &aid})

	res := runASIN(t, p, `{"dry_run":false}`).res(t)
	if res.Errored != 1 || res.NoMatch != 0 {
		t.Fatalf("result = %+v", res)
	}
	if raw, _ := s.GetRaw(asinMissKey(first.ID)); raw != nil {
		t.Fatal("a failed search wrote a no-match marker")
	}

	for i := range asinBackfillMaxConsecutiveErrors + 5 {
		mkBook(t, s, &database.Book{Title: fmt.Sprintf("Book %d", i), AuthorID: &aid})
	}
	rep := &asinTestReporter{}
	err := p.runASINBackfill(context.Background(), json.RawMessage(`{"dry_run":false,"workers":1}`), rep)
	if err == nil || !strings.Contains(err.Error(), "in a row failed") {
		t.Fatalf("run err = %v, want the consecutive-error trip", err)
	}
}

func TestASINBackfill_LimitCapsSearches(t *testing.T) {
	fa := rrFake()
	p, s := newASINTestPlugin(t, fa)
	aid := mkAuthor(t, s, "Pierce Brown")
	for range 5 {
		mkBook(t, s, &database.Book{Title: "Red Rising", AuthorID: &aid, ISBN13: new(isbnRR)})
	}
	res := runASIN(t, p, `{"dry_run":true,"limit":2}`).res(t)
	if res.Searched != 2 || res.SkippedLimit != 3 {
		t.Fatalf("result = %+v", res)
	}
}

// TestASINBackfill_ConcurrentRun drives a multi-page library through a 4-worker
// pool, so the counters, the Label closure (which reads them inside the
// workers) and the writer are all exercised concurrently. Run with -race.
func TestASINBackfill_ConcurrentRun(t *testing.T) {
	const n = asinBackfillPageSize + 37 // two pages
	fa := &fakeAudible{byTitle: map[string][]metadata.AudibleIdentity{}, byKW: map[string][]metadata.AudibleIdentity{}, delay: time.Millisecond}
	p, s := newASINTestPlugin(t, fa)
	aid := mkAuthor(t, s, "Pierce Brown")
	ids := make([]string, 0, n)
	for i := range n {
		title := fmt.Sprintf("Title Number %c%c", 'a'+rune(i/26), 'a'+rune(i%26))
		isbn := fmt.Sprintf("978000000%04d", i)
		prod := metadata.AudibleIdentity{ASIN: fmt.Sprintf("B0%08d", i), Title: title, Authors: []string{"Pierce Brown"}, ISBN: isbn}
		fa.byTitle[strings.ToLower(title)] = []metadata.AudibleIdentity{prod}
		fa.byKW[isbn] = []metadata.AudibleIdentity{prod}
		b := mkBook(t, s, &database.Book{Title: title, AuthorID: &aid, ISBN13: new(isbn)})
		ids = append(ids, b.ID)
	}

	rep := runASIN(t, p, `{"dry_run":false,"workers":4}`)
	res := rep.res(t)
	if res.MatchedWritten != n || res.Scanned != n || res.Errored != 0 {
		t.Fatalf("result = %+v, want %d written", res, n)
	}
	for i, id := range ids {
		if got := asinOf(t, s, id); got != fmt.Sprintf("B0%08d", i) {
			t.Fatalf("book %d asin = %q", i, got)
		}
	}
	rep.mu.Lock()
	defer rep.mu.Unlock()
	if rep.labels != n {
		t.Fatalf("labels = %d, want %d (one per item)", rep.labels, n)
	}
	if len(rep.checkpoints) != 2 {
		t.Fatalf("checkpoints = %d, want one per page (2)", len(rep.checkpoints))
	}
}

// TestASINBackfill_ITunesBookFileUntouched: an ASIN written onto a book whose
// file lives under books/itunes/ changes the DB row only -- the file is not
// opened for write (bytes and mtime unchanged) and no write-back history row
// appears.
func TestASINBackfill_ITunesBookFileUntouched(t *testing.T) {
	fa := rrFake()
	p, s := newASINTestPlugin(t, fa)
	aid := mkAuthor(t, s, "Pierce Brown")
	dir := filepath.Join(t.TempDir(), "books", "itunes", "Pierce Brown", "Red Rising")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "Red Rising.m4b")
	content := []byte("not really audio")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-48 * time.Hour).Truncate(time.Second)
	if err := os.Chtimes(path, past, past); err != nil {
		t.Fatal(err)
	}
	b := mkBook(t, s, &database.Book{Title: "Red Rising", AuthorID: &aid, ISBN13: new(isbnRR), FilePath: path})

	runASIN(t, p, `{"dry_run":false}`)
	if got := asinOf(t, s, b.ID); got != "B00I2VWW5U" {
		t.Fatalf("asin = %q", got)
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, content) {
		t.Fatalf("file changed (err %v)", err)
	}
	st, err := os.Stat(path)
	if err != nil || !st.ModTime().Equal(past) {
		t.Fatalf("file mtime changed: %v (err %v)", st.ModTime(), err)
	}
	hist, _ := s.GetBookChangeHistory(b.ID, 100)
	for _, h := range hist {
		if h.ChangeType == "write-back" || h.Field != "asin" {
			t.Fatalf("unexpected history row %+v", h)
		}
	}
}

func TestASINBackfill_DefRegistered(t *testing.T) {
	var found bool
	for _, d := range (&Plugin{}).OperationDefs() {
		if d.ID != asinBackfillOpID {
			continue
		}
		found = true
		if d.ConcurrencyKey != asinBackfillOpID || !d.Cancellable || d.Run == nil {
			t.Fatalf("def = %+v", d)
		}
	}
	if !found {
		t.Fatalf("%s not in OperationDefs", asinBackfillOpID)
	}
}
