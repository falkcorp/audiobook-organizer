// file: internal/plugins/maintenance/author_purge_empty_test.go
// version: 1.1.0
// guid: b83c47f1-2065-4ade-9c18-31d70f5b62ea
// last-edited: 2026-08-23

package maintenance

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// newPurgePlugin wires a MockStore over a fixed author table plus book and file
// counts, recording every DeleteAuthor call so a dry-run test can assert on
// SILENCE rather than on a return value — the only assertion that distinguishes
// "reported correctly" from "deleted anyway".
func newPurgePlugin(authors []database.Author, books, files map[int]int, deleted *[]int) *Plugin {
	store := &database.MockStore{
		GetAllAuthorsFunc:          func() ([]database.Author, error) { return authors, nil },
		GetAllAuthorBookCountsFunc: func() (map[int]int, error) { return books, nil },
		GetAllAuthorFileCountsFunc: func() (map[int]int, error) { return files, nil },
		DeleteAuthorFunc: func(id int) error {
			*deleted = append(*deleted, id)
			return nil
		},
	}
	return &Plugin{deps: &fakeDeps{store: store}}
}

// purgeFixture: 1 real author with books, 2 pure-junk (no books, no files), and 1
// zero-book author that HAS files — the ambiguous row the guard exists for.
func purgeFixture() ([]database.Author, map[int]int, map[int]int) {
	authors := []database.Author{
		{ID: 1, Name: "Brandon Sanderson"},
		{ID: 2, Name: "- Edgedancer"},
		{ID: 3, Name: "04 - Heir to the Jedi"},
		{ID: 4, Name: "Has Files But No Books"},
	}
	books := map[int]int{1: 12, 2: 0, 3: 0, 4: 0}
	files := map[int]int{1: 40, 2: 0, 3: 0, 4: 7}
	return authors, books, files
}

func runPurge(t *testing.T, params string, deleted *[]int) {
	t.Helper()
	authors, books, files := purgeFixture()
	p := newPurgePlugin(authors, books, files, deleted)
	var raw json.RawMessage
	if params != "" {
		raw = json.RawMessage(params)
	}
	if err := p.runPurgeEmptyAuthors(context.Background(), raw, &fakeReporter{}); err != nil {
		t.Fatalf("runPurgeEmptyAuthors: %v", err)
	}
}

// 🔴 DRY RUN MUST NOT DELETE. This is the assertion that matters most: the op
// deletes rows from a production library, so the default has to be inert. A test
// that only checked the reported counts would pass even if it deleted everything.
func TestPurgeEmptyAuthors_DryRunDeletesNothing(t *testing.T) {
	var deleted []int
	runPurge(t, "", &deleted)
	if len(deleted) != 0 {
		t.Fatalf("dry run deleted %v — the default must be inert", deleted)
	}
	// And explicitly with apply:false, since a client may send it rather than omit it.
	deleted = nil
	runPurge(t, `{"apply":false}`, &deleted)
	if len(deleted) != 0 {
		t.Fatalf("apply=false deleted %v", deleted)
	}
}

// 🔴 THE GUARD. Author 4 has zero books but seven files — more likely a book that
// lost its junction entry than an empty author. Deleting it makes repairable damage
// permanent, so it must be held back unless explicitly overridden.
func TestPurgeEmptyAuthors_HoldsBackAuthorsWithFiles(t *testing.T) {
	var deleted []int
	runPurge(t, `{"apply":true}`, &deleted)

	got := map[int]bool{}
	for _, id := range deleted {
		got[id] = true
	}
	if got[4] {
		t.Error("deleted author 4, which has 7 files — that is the row the guard exists for")
	}
	if got[1] {
		t.Error("deleted author 1, who has 12 books")
	}
	for _, id := range []int{2, 3} {
		if !got[id] {
			t.Errorf("did NOT delete author %d, which has zero books and zero files", id)
		}
	}
	if len(deleted) != 2 {
		t.Errorf("deleted %v, want exactly authors 2 and 3", deleted)
	}
}

// The override must actually override — otherwise the escape hatch is decorative
// and the 822 real rows can never be cleaned up.
func TestPurgeEmptyAuthors_RequireZeroFilesFalseIncludesThem(t *testing.T) {
	var deleted []int
	runPurge(t, `{"apply":true,"require_zero_files":false}`, &deleted)

	got := map[int]bool{}
	for _, id := range deleted {
		got[id] = true
	}
	if !got[4] {
		t.Error("require_zero_files=false did not include author 4 — the override does nothing")
	}
	if got[1] {
		t.Error("deleted author 1, who has 12 books — no flag should ever allow that")
	}
	if len(deleted) != 3 {
		t.Errorf("deleted %v, want authors 2, 3 and 4", deleted)
	}
}

// A book count of zero is the ONLY thing that makes a row eligible. Pinned
// separately because it is the invariant a future refactor is most likely to break
// while all the flag tests keep passing.
func TestPurgeEmptyAuthors_NeverDeletesAnAuthorWithBooks(t *testing.T) {
	authors := []database.Author{
		{ID: 1, Name: "One Book"},
		{ID: 2, Name: "Zero Books"},
	}
	var deleted []int
	p := newPurgePlugin(authors, map[int]int{1: 1, 2: 0}, map[int]int{1: 0, 2: 0}, &deleted)
	if err := p.runPurgeEmptyAuthors(context.Background(),
		json.RawMessage(`{"apply":true,"require_zero_files":false}`), &fakeReporter{}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(deleted) != 1 || deleted[0] != 2 {
		t.Fatalf("deleted %v, want only author 2 — a single book must protect a row", deleted)
	}
}

// Limit exists so a first apply can be run small and inspected. Sorted order makes
// the slice deterministic, so limit=1 takes the same row every time.
func TestPurgeEmptyAuthors_LimitCapsTheDeletion(t *testing.T) {
	var deleted []int
	runPurge(t, `{"apply":true,"limit":1}`, &deleted)
	if len(deleted) != 1 {
		t.Fatalf("limit=1 deleted %v, want exactly one", deleted)
	}
	if deleted[0] != 2 {
		t.Errorf("limit=1 deleted author %d, want the lowest eligible id (2) — order must be deterministic", deleted[0])
	}
}

// 🔴 A MISSING SIGNAL IS NOT PERMISSION. If the file counts cannot be read, the
// guard cannot be evaluated, and treating that as "zero files" would delete exactly
// the rows the guard protects. The op must fail instead.
func TestPurgeEmptyAuthors_FileCountFailureAbortsRatherThanDeleting(t *testing.T) {
	authors, books, _ := purgeFixture()
	var deleted []int
	store := &database.MockStore{
		GetAllAuthorsFunc:          func() ([]database.Author, error) { return authors, nil },
		GetAllAuthorBookCountsFunc: func() (map[int]int, error) { return books, nil },
		GetAllAuthorFileCountsFunc: func() (map[int]int, error) { return nil, context.DeadlineExceeded },
		DeleteAuthorFunc: func(id int) error {
			deleted = append(deleted, id)
			return nil
		},
	}
	p := &Plugin{deps: &fakeDeps{store: store}}
	err := p.runPurgeEmptyAuthors(context.Background(), json.RawMessage(`{"apply":true}`), &fakeReporter{})
	if err == nil {
		t.Fatal("file-count failure did not abort the op")
	}
	if len(deleted) != 0 {
		t.Fatalf("deleted %v despite being unable to evaluate the guard", deleted)
	}
}

// ---------------------------------------------------------------------------
// TASK-028: the delete guard must use the UNFILTERED reference count.
//
// Every fixture below creates a DIVERGENCE — the filtered counter says zero
// while the unfiltered one does not — so none of them can pass against the old
// `bookCounts[a.ID] != 0` guard. `refDivergenceFixture` asserts that divergence
// explicitly rather than leaving it implied.
// ---------------------------------------------------------------------------

// refPurgeStore builds a store whose FILTERED author counts and UNFILTERED
// reference counts disagree, which is the entire bug: author 2 looks empty to
// the display counter (all its books are trashed / non-primary / junction-only)
// but is still referenced by two of them.
func refPurgeStore(deleted *[]int, refCounts map[int]int, refErr error) *database.MockStore {
	authors := []database.Author{
		{ID: 1, Name: "Has Live Books"},
		{ID: 2, Name: "Looks Empty But Is Referenced"},
		{ID: 3, Name: "- Genuinely Junk"},
	}
	return &database.MockStore{
		GetAllAuthorsFunc: func() ([]database.Author, error) { return authors, nil },
		// FILTERED: 2 and 3 both read as zero.
		GetAllAuthorBookCountsFunc: func() (map[int]int, error) {
			return map[int]int{1: 12, 2: 0, 3: 0}, nil
		},
		GetAllAuthorFileCountsFunc: func() (map[int]int, error) {
			return map[int]int{1: 40, 2: 0, 3: 0}, nil
		},
		// UNFILTERED: 2 is still held by two books.
		GetAllAuthorBookRefCountsFunc: func() (map[int]int, error) {
			if refErr != nil {
				return nil, refErr
			}
			return refCounts, nil
		},
		DeleteAuthorFunc: func(id int) error {
			*deleted = append(*deleted, id)
			return nil
		},
	}
}

// divergentRefCounts is the reference view matching refPurgeStore: author 2 is
// referenced by 2 books that the filtered counter cannot see.
func divergentRefCounts() map[int]int { return map[int]int{1: 12, 2: 2} }

// 🔴 THE BUG. Author 2 has zero books by the display counter and zero files, so
// the old guard deleted it — while two trashed/non-primary/junction-only books
// still held its author_id. An author's name exists only in that row, so the
// reference is unrecoverable afterwards.
func TestPurgeEmptyAuthors_HoldsBackStillReferencedAuthor(t *testing.T) {
	var deleted []int
	store := refPurgeStore(&deleted, divergentRefCounts(), nil)

	// Precondition: the FILTERED counter really does report zero for author 2.
	// Without this the test could pass for the wrong reason.
	filtered, err := store.GetAllAuthorBookCounts()
	if err != nil {
		t.Fatalf("filtered counts: %v", err)
	}
	if filtered[2] != 0 {
		t.Fatalf("fixture is not divergent: filtered count for author 2 is %d, want 0", filtered[2])
	}

	p := &Plugin{deps: &fakeDeps{store: store}}
	if err := p.runPurgeEmptyAuthors(context.Background(),
		json.RawMessage(`{"apply":true,"require_zero_files":false}`), &fakeReporter{}); err != nil {
		t.Fatalf("run: %v", err)
	}

	for _, id := range deleted {
		if id == 2 {
			t.Fatal("deleted author 2, which is still referenced by 2 books the display counter cannot see")
		}
	}
	// POSITIVE CONTROL: the guard must not suppress everything. Author 3 is
	// genuinely unreferenced and must STILL be purged, or this op is dead.
	if len(deleted) != 1 || deleted[0] != 3 {
		t.Fatalf("deleted %v, want exactly author 3 — the genuinely empty row must still be purged", deleted)
	}
}

// opaquePurgeStore satisfies database.Store but neither implements
// AuthorBookRefStore nor opts into unwrapping — the shape a capability lookup
// must refuse rather than guess at.
type opaquePurgeStore struct{ database.Store }

// unwrappablePurgeStore is the PRODUCTION shape: a decorator (Bleve's
// indexedStore) that does not implement the capability itself but opts into
// having it resolved against the store it wraps.
type unwrappablePurgeStore struct{ database.Store }

func (u unwrappablePurgeStore) Unwrap() database.Store { return u.Store }

// 🔴 FAIL CLOSED. A store that cannot answer the unfiltered question must make
// the op REFUSE. Falling back to the filtered count is precisely the bug, and it
// would delete thousands of rows while reporting success.
func TestPurgeEmptyAuthors_FailsClosedWhenStoreLacksRefCounter(t *testing.T) {
	var deleted []int
	inner := refPurgeStore(&deleted, divergentRefCounts(), nil)
	p := &Plugin{deps: &fakeDeps{store: opaquePurgeStore{Store: inner}}}

	err := p.runPurgeEmptyAuthors(context.Background(),
		json.RawMessage(`{"apply":true,"require_zero_files":false}`), &fakeReporter{})
	if err == nil {
		t.Fatal("op did not fail when the store cannot count unfiltered author references")
	}
	if len(deleted) != 0 {
		t.Fatalf("deleted %v despite being unable to evaluate the guard", deleted)
	}
}

// 🔴 THE DECORATOR. In production the live store is wrapped by the Bleve
// search-index decorator, so a lookup that does not walk the chain returns nil
// exactly where the guard matters — and this op would then fail closed on every
// single run, which is a different outage. This pins that it resolves THROUGH.
func TestPurgeEmptyAuthors_ResolvesRefCounterThroughDecorator(t *testing.T) {
	var deleted []int
	inner := refPurgeStore(&deleted, divergentRefCounts(), nil)
	p := &Plugin{deps: &fakeDeps{store: unwrappablePurgeStore{Store: inner}}}

	if err := p.runPurgeEmptyAuthors(context.Background(),
		json.RawMessage(`{"apply":true,"require_zero_files":false}`), &fakeReporter{}); err != nil {
		t.Fatalf("run through decorator: %v", err)
	}
	// The guard resolved (author 2 held back) AND the op still works (3 purged).
	if len(deleted) != 1 || deleted[0] != 3 {
		t.Fatalf("through the decorator deleted %v, want exactly author 3", deleted)
	}
}

// A missing signal is not permission — same contract as the file-count failure
// above, applied to the reference count.
func TestPurgeEmptyAuthors_RefCountFailureAbortsRatherThanDeleting(t *testing.T) {
	var deleted []int
	store := refPurgeStore(&deleted, nil, context.DeadlineExceeded)
	p := &Plugin{deps: &fakeDeps{store: store}}

	err := p.runPurgeEmptyAuthors(context.Background(),
		json.RawMessage(`{"apply":true,"require_zero_files":false}`), &fakeReporter{})
	if err == nil {
		t.Fatal("reference-count failure did not abort the op")
	}
	if len(deleted) != 0 {
		t.Fatalf("deleted %v despite being unable to evaluate the guard", deleted)
	}
}

// 🔴 THE DRY RUN MUST NOT LIE. The guard is applied before the apply/dry-run
// branch, so both paths see an identical eligible set. If the check ever moves
// after that branch, a dry run would report a set the apply run does not delete
// — the worst possible failure for an op whose safety story IS "inspect the dry
// run first".
func TestPurgeEmptyAuthors_DryRunAndApplyAgreeOnEligibleSet(t *testing.T) {
	var dryDeleted []int
	dryStore := refPurgeStore(&dryDeleted, divergentRefCounts(), nil)
	dryReporter := &fakeReporter{}
	pDry := &Plugin{deps: &fakeDeps{store: dryStore}}
	if err := pDry.runPurgeEmptyAuthors(context.Background(),
		json.RawMessage(`{"apply":false,"require_zero_files":false}`), dryReporter); err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if len(dryDeleted) != 0 {
		t.Fatalf("dry run deleted %v", dryDeleted)
	}

	var applyDeleted []int
	applyStore := refPurgeStore(&applyDeleted, divergentRefCounts(), nil)
	pApply := &Plugin{deps: &fakeDeps{store: applyStore}}
	if err := pApply.runPurgeEmptyAuthors(context.Background(),
		json.RawMessage(`{"apply":true,"require_zero_files":false}`), &fakeReporter{}); err != nil {
		t.Fatalf("apply run: %v", err)
	}

	// The dry run reported 1 eligible; the apply run deleted exactly that row.
	if len(applyDeleted) != 1 || applyDeleted[0] != 3 {
		t.Fatalf("apply deleted %v, want exactly author 3 — the set the dry run promised", applyDeleted)
	}
}
