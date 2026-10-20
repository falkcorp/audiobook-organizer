// file: internal/identification/advance_test.go
// version: 1.0.1
// guid: e90a505f-13b3-43db-9f19-8cb48e771f05
// last-edited: 2026-10-20

package identification

import (
	"context"
	"errors"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// TestAdvance_NoRegistryImport: the body must be what the v3 Batch takes,
// so it never imports the v2 runner. Every non-test file of the package is
// parsed and its imports checked.
func TestAdvance_NoRegistryImport(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	fset := token.NewFileSet()
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := parser.ParseFile(fset, f, src, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", f, err)
		}
		checked++
		for _, imp := range parsed.Imports {
			path, _ := strconv.Unquote(imp.Path.Value)
			if strings.Contains(path, "internal/operations") {
				t.Errorf("%s imports %s: the driver body must not import the operations runner (the v3 port is an adapter swap)", f, path)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no source files checked")
	}
}

// fakeStore is an in-memory dirty set and reason table with the store's
// compare-and-delete semantics.
type fakeStore struct {
	mu      sync.Mutex
	epoch   uint64
	marks   map[string]uint64
	reasons map[string][2]string // reason, fp
}

func newFakeStore() *fakeStore {
	return &fakeStore{marks: map[string]uint64{}, reasons: map[string][2]string{}}
}

func (f *fakeStore) mark(id string) uint64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.epoch++
	f.marks[id] = f.epoch
	return f.epoch
}

func (f *fakeStore) has(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.marks[id]
	return ok
}

func (f *fakeStore) ClearIdentDirty(id string, epoch uint64) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if cur, ok := f.marks[id]; ok && cur == epoch {
		delete(f.marks, id)
		return true, nil
	}
	return false, nil
}

func (f *fakeStore) SetIdentReason(id, reason, fp string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if reason == "" {
		delete(f.reasons, id)
		return nil
	}
	f.reasons[id] = [2]string{reason, fp}
	return nil
}

func (f *fakeStore) IdentDirtyPage(after string, limit int) ([]database.IdentDirtyEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var ids []string
	for id := range f.marks {
		if id > after {
			ids = append(ids, id)
		}
	}
	sortStrings(ids)
	if len(ids) > limit {
		ids = ids[:limit]
	}
	out := make([]database.IdentDirtyEntry, len(ids))
	for i, id := range ids {
		out[i] = database.IdentDirtyEntry{BookID: id, Epoch: f.marks[id]}
	}
	return out, nil
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

func primaryBook(id string) *database.Book {
	author := 7
	return &database.Book{ID: id, Title: "Title " + id, AuthorID: &author}
}

// fixture builds an Item over a fake store and a fake library.
type fixture struct {
	store   *fakeStore
	books   map[string]*database.Book
	fetches map[string]int
	result  FetchResult
	onFetch func(book *database.Book)
	mu      sync.Mutex
}

func newFixture() *fixture {
	return &fixture{store: newFakeStore(), books: map[string]*database.Book{}, fetches: map[string]int{},
		result: FetchResult{Outcome: OutcomeFetched}}
}

func (fx *fixture) item() ItemFunc {
	return NewItem(ItemDeps{
		LoadBook: func(id string) (*database.Book, error) { return fx.books[id], nil },
		Store:    fx.store,
		FetchOne: func(_ context.Context, b *database.Book) FetchResult {
			fx.mu.Lock()
			fx.fetches[b.ID]++
			fx.mu.Unlock()
			if fx.onFetch != nil {
				fx.onFetch(b)
			}
			return fx.result
		},
	})
}

// A writer re-marks the book while its Item runs: the compare-and-delete on
// the epoch read at start leaves the newer mark in place.
func TestItem_ClearsMarkOnlyWhenEpochMatches(t *testing.T) {
	fx := newFixture()
	fx.books["b1"] = primaryBook("b1")
	e := fx.store.mark("b1")
	fx.onFetch = func(*database.Book) { fx.store.mark("b1") }
	o, err := fx.item()(context.Background(), Mark{BookID: "b1", Epoch: e})
	if err != nil || o != OutcomeFetched {
		t.Fatalf("got %q, %v", o, err)
	}
	if !fx.store.has("b1") {
		t.Fatal("a mark re-written during Item was deleted")
	}

	// Without a re-mark the same flow clears it.
	fx.onFetch = nil
	page, _ := fx.store.IdentDirtyPage("", PageSize)
	if _, err := fx.item()(context.Background(), Mark{BookID: "b1", Epoch: page[0].Epoch}); err != nil {
		t.Fatal(err)
	}
	if fx.store.has("b1") {
		t.Fatal("the mark survived an Item that read its current epoch")
	}
	if fx.fetches["b1"] != 2 {
		t.Fatalf("fetches = %d, want 2 (the re-marked book is processed again)", fx.fetches["b1"])
	}
}

// An applied (or held, gone, non-primary, quarantined) book is not fetched;
// its mark is cleared.
func TestItem_AppliedBookClearsMarkWithoutFetch(t *testing.T) {
	cases := map[string]func(*database.Book) *database.Book{
		"applied":     func(b *database.Book) *database.Book { b.MetadataReviewStatus = new("matched"); return b },
		"held":        func(b *database.Book) *database.Book { b.MetadataReviewStatus = new("no_match"); return b },
		"gone":        func(*database.Book) *database.Book { return nil },
		"not_primary": func(b *database.Book) *database.Book { f := false; b.IsPrimaryVersion = &f; return b },
		"quarantined": func(b *database.Book) *database.Book { now := time.Now(); b.QuarantinedAt = &now; return b },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			fx := newFixture()
			if b := mutate(primaryBook("b1")); b != nil {
				fx.books["b1"] = b
			}
			_ = fx.store.SetIdentReason("b1", database.IdentReasonFragment, "fp")
			e := fx.store.mark("b1")
			o, err := fx.item()(context.Background(), Mark{BookID: "b1", Epoch: e})
			if err != nil || o != OutcomeSkippedDone {
				t.Fatalf("got %q, %v", o, err)
			}
			if fx.fetches["b1"] != 0 {
				t.Fatalf("FetchOne called %d times", fx.fetches["b1"])
			}
			if fx.store.has("b1") {
				t.Fatal("mark not cleared")
			}
			if _, kept := fx.store.reasons["b1"]; (name == "gone") == kept {
				t.Fatalf("reason kept=%v (only a gone book's orphan reason is dropped)", kept)
			}
		})
	}
}

// Each reason FetchOne reports is recorded under the book's question
// fingerprint, and a later fetch with no reason clears it.
func TestItem_RecordsIdentReason(t *testing.T) {
	cases := []struct {
		reason  string
		outcome Outcome
	}{
		{database.IdentReasonNoUsableTitle, OutcomeSkippedDone},
		{database.IdentReasonFragment, OutcomeSkippedDone},
		{database.IdentReasonManualOnly, OutcomeFetched},
		{database.IdentReasonQuotaDeferred, OutcomeDeferred},
	}
	for _, c := range cases {
		t.Run(c.reason, func(t *testing.T) {
			fx := newFixture()
			b := primaryBook("b1")
			fx.books["b1"] = b
			fx.result = FetchResult{Outcome: c.outcome, Reason: c.reason}
			o, err := fx.item()(context.Background(), Mark{BookID: "b1", Epoch: fx.store.mark("b1")})
			if err != nil || o != c.outcome {
				t.Fatalf("got %q, %v", o, err)
			}
			got := fx.store.reasons["b1"]
			if got[0] != c.reason || got[1] != database.IdentQuestionFP(b) {
				t.Fatalf("reason = %v, want %q under fp %q", got, c.reason, database.IdentQuestionFP(b))
			}
			fx.result = FetchResult{Outcome: OutcomeFetched}
			if _, err := fx.item()(context.Background(), Mark{BookID: "b1", Epoch: fx.store.mark("b1")}); err != nil {
				t.Fatal(err)
			}
			if _, kept := fx.store.reasons["b1"]; kept {
				t.Fatal("a reason that no longer applies was kept")
			}
		})
	}
}

// Running Item twice for the same state is harmless: same reason, no mark,
// no error.
func TestItem_Idempotent(t *testing.T) {
	fx := newFixture()
	fx.books["b1"] = primaryBook("b1")
	fx.result = FetchResult{Outcome: OutcomeFetched, Reason: database.IdentReasonManualOnly}
	m := Mark{BookID: "b1", Epoch: fx.store.mark("b1")}
	for i := 0; i < 2; i++ {
		o, err := fx.item()(context.Background(), m)
		if err != nil || o != OutcomeFetched {
			t.Fatalf("run %d: got %q, %v", i, o, err)
		}
		if fx.store.has("b1") || fx.store.reasons["b1"][0] != database.IdentReasonManualOnly {
			t.Fatalf("run %d: mark=%v reason=%v", i, fx.store.has("b1"), fx.store.reasons["b1"])
		}
	}
}

// A cancel during FetchOne keeps the mark: the rerun processes the book.
func TestItem_CancelKeepsMark(t *testing.T) {
	fx := newFixture()
	fx.books["b1"] = primaryBook("b1")
	ctx, cancel := context.WithCancel(context.Background())
	fx.onFetch = func(*database.Book) { cancel() }
	o, err := fx.item()(ctx, Mark{BookID: "b1", Epoch: fx.store.mark("b1")})
	if !errors.Is(err, context.Canceled) || o != "" {
		t.Fatalf("got %q, %v", o, err)
	}
	if !fx.store.has("b1") {
		t.Fatal("a cancelled item cleared its mark")
	}
}

// A pipeline error is reported and its mark cleared: the safety net, not the
// 5-minute tick, retries it.
func TestItem_ErrorClearsMark(t *testing.T) {
	fx := newFixture()
	fx.books["b1"] = primaryBook("b1")
	fx.result = FetchResult{Outcome: OutcomeError, Err: errors.New("provider down")}
	o, err := fx.item()(context.Background(), Mark{BookID: "b1", Epoch: fx.store.mark("b1")})
	if err == nil || o != OutcomeError {
		t.Fatalf("got %q, %v", o, err)
	}
	if fx.store.has("b1") {
		t.Fatal("an errored item kept its mark")
	}
}

// Pages streams the set in key order with the last key as the cursor, and
// Finish's counters add up.
func TestDirtySet_PagesAndTally(t *testing.T) {
	st := newFakeStore()
	for i := 0; i < 5; i++ {
		st.mark("b" + strconv.Itoa(i))
	}
	src := DirtySet{Store: st}
	var got []string
	after := ""
	for {
		page, next, err := src.Pages(context.Background(), after, 2)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			break
		}
		for _, m := range page {
			got = append(got, m.BookID)
		}
		after = next
	}
	if strings.Join(got, ",") != "b0,b1,b2,b3,b4" {
		t.Fatalf("pages = %v", got)
	}

	var tally Tally
	for _, o := range []Outcome{OutcomeFetched, OutcomeFetched, OutcomeSkippedDone, OutcomeDeferred, OutcomeError, ""} {
		tally.Record(o)
	}
	var sum Summary
	adv := New(src, nil, func(s Summary) { sum = s }, 4, "test")
	adv.Finish(tally.Summary(false))
	if sum.Done != 5 || sum.Counters[string(OutcomeFetched)] != 2 {
		t.Fatalf("summary = %+v", sum)
	}
	if adv.PartitionBy(Mark{BookID: "b9"}) != "b9" || adv.Key(Mark{BookID: "b9"}) != "b9" || adv.Order != Unordered {
		t.Fatal("PartitionBy/Key must be the book id and Order Unordered")
	}
	if PageSize != 256 {
		t.Fatalf("PageSize = %d, want 256 (D28a's chunk)", PageSize)
	}
}
