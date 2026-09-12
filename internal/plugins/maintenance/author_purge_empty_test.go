// file: internal/plugins/maintenance/author_purge_empty_test.go
// version: 1.4.0
// guid: b83c47f1-2065-4ade-9c18-31d70f5b62ea
// last-edited: 2026-09-11

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
		// The op reads the UNFILTERED file counter (database.AuthorFileRefCounts).
		// The fixture's `files` map is the same number seen through both, which is
		// what a store with no trashed / non-primary / junction-only rows looks
		// like; the divergence cases get their own fixtures below.
		GetAllAuthorFileRefCountsFunc: func() (map[int]int, error) { return files, nil },
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
		GetAllAuthorsFunc:             func() ([]database.Author, error) { return authors, nil },
		GetAllAuthorBookCountsFunc:    func() (map[int]int, error) { return books, nil },
		GetAllAuthorFileRefCountsFunc: func() (map[int]int, error) { return nil, context.DeadlineExceeded },
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
		GetAllAuthorFileRefCountsFunc: func() (map[int]int, error) {
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

// ---------------------------------------------------------------------------
// TASK-363: the file-safety gate must read the UNFILTERED file count.
//
// GetAllAuthorFileCounts is a DISPLAY counter — primary-version index only,
// soft-deleted books skipped, books mapped to authors through the legacy
// Book.AuthorID field alone. It returns an unconditional 0 for a junction-only
// co-author, for an author whose books are all trashed, and for one whose books
// are all non-primary, in every case while those books' files sit on disk.
// require_zero_files is documented as "🔴 THIS IS THE SAFETY THAT MATTERS" and
// was reading exactly that number.
// ---------------------------------------------------------------------------

// fileRefPurgeStore builds a store whose DISPLAY file count and UNFILTERED file
// count disagree for author 2 — the divergence every one of the three missed
// populations produces.
//
// 🔴 FIXTURE CAVEAT, stated rather than left for a reviewer to reconstruct: the
// unfiltered REF count is stubbed to zero for author 2. In production it would
// be non-zero (the same books that hold the files hold the author), and
// runPurgeEmptyAuthors skips on refCounts before it ever reaches the file gate,
// so this exact state is not producible with both counters intact. The stub
// exercises the FILE gate in isolation; it is not a claim about reachability.
func fileRefPurgeStore(deleted *[]int, displayFiles map[int]int, refFiles func() (map[int]int, error)) *database.MockStore {
	authors := []database.Author{
		{ID: 1, Name: "Has Live Books"},
		{ID: 2, Name: "Looks Empty But Its Files Are Real"},
		{ID: 3, Name: "- Genuinely Junk"},
	}
	return &database.MockStore{
		GetAllAuthorsFunc: func() ([]database.Author, error) { return authors, nil },
		GetAllAuthorBookCountsFunc: func() (map[int]int, error) {
			return map[int]int{1: 12, 2: 0, 3: 0}, nil
		},
		GetAllAuthorBookRefCountsFunc: func() (map[int]int, error) {
			return map[int]int{1: 12}, nil
		},
		GetAllAuthorFileCountsFunc:    func() (map[int]int, error) { return displayFiles, nil },
		GetAllAuthorFileRefCountsFunc: refFiles,
		DeleteAuthorFunc: func(id int) error {
			*deleted = append(*deleted, id)
			return nil
		},
	}
}

// 🔴 THE BUG. Author 2's books are trashed / non-primary / credit it only
// through the junction, so the display counter says it has zero files and the
// gate waved it through. Its files are still on disk, and an author's name
// lives only in the row the purge deletes.
func TestPurgeEmptyAuthors_FileGateReadsTheUnfilteredCount(t *testing.T) {
	var deleted []int
	display := map[int]int{1: 40, 2: 0, 3: 0}
	unfiltered := map[int]int{1: 40, 2: 9}
	store := fileRefPurgeStore(&deleted, display,
		func() (map[int]int, error) { return unfiltered, nil })

	p := &Plugin{deps: &fakeDeps{store: store}}
	if err := p.runPurgeEmptyAuthors(context.Background(), json.RawMessage(`{"apply":true}`), &fakeReporter{}); err != nil {
		t.Fatalf("runPurgeEmptyAuthors: %v", err)
	}

	for _, id := range deleted {
		if id == 2 {
			t.Fatalf("deleted author 2, which has 9 files the display counter cannot see (deleted=%v)", deleted)
		}
	}
	if len(deleted) != 1 || deleted[0] != 3 {
		t.Fatalf("deleted %v, want exactly the genuinely-junk author 3", deleted)
	}
}

// 🔴 A MISSING SIGNAL IS NOT PERMISSION, second edition. The unfiltered counter
// refuses on a memdb known to be short rather than falling through to a scan
// that would read the files back out of that same short projection. The op must
// carry the refusal, not treat it as "zero files".
func TestPurgeEmptyAuthors_ShortMemdbFileCountAbortsRatherThanDeleting(t *testing.T) {
	var deleted []int
	store := fileRefPurgeStore(&deleted, map[int]int{1: 40, 2: 0, 3: 0},
		func() (map[int]int, error) { return nil, database.ErrMemdbIncomplete })

	p := &Plugin{deps: &fakeDeps{store: store}}
	err := p.runPurgeEmptyAuthors(context.Background(), json.RawMessage(`{"apply":true}`), &fakeReporter{})
	if err == nil {
		t.Fatal("a short memdb did not abort the op")
	}
	if !errors.Is(err, database.ErrMemdbIncomplete) {
		t.Errorf("error %v does not wrap ErrMemdbIncomplete — the cause must survive to the operator", err)
	}
	if len(deleted) != 0 {
		t.Fatalf("deleted %v despite being unable to evaluate the file-safety gate", deleted)
	}
}

// ---------------------------------------------------------------------------
// TASK-075: the held-back (zero books, has files) population gets a SAMPLE, not
// just a count, so a human can look at who they are before require_zero_files
// is ever flipped.
//
// 🔴 FIXTURE CAVEAT, same as fileRefPurgeStore above: refCounts is left empty
// for the held-back authors. In production AuthorFileRefCounts attributes files
// only through books that reference the author, so a non-zero file count
// implies a non-zero ref count and such an author lands in HeldByRefs first.
// These fixtures isolate the file gate's reporting; they are not a claim that
// the state is common in a live library.
// ---------------------------------------------------------------------------

// Author 4 (zero books, 7 files) is held back and must appear in the sample
// with its ID, name and file count; authors 2 and 3 (zero books, zero files)
// are eligible junk and must NOT appear in it; author 1 has books.
func TestPurgeEmptyAuthors_HeldBackSample_Populated(t *testing.T) {
	authors, books, files := purgeFixture()
	report, eligible := classifyEmptyAuthors(authors, books, nil, files, true)

	want := []heldBackAuthor{{AuthorID: 4, Name: "Has Files But No Books", FileCount: 7}}
	if len(report.HeldBackSample) != len(want) || report.HeldBackSample[0] != want[0] {
		t.Fatalf("HeldBackSample = %+v, want %+v", report.HeldBackSample, want)
	}
	if report.ZeroBooksWithFiles != 1 {
		t.Errorf("ZeroBooksWithFiles = %d, want 1", report.ZeroBooksWithFiles)
	}
	// Report-only: the eligible set and its sample are unchanged by the addition.
	if len(eligible) != 2 || eligible[0] != 2 || eligible[1] != 3 {
		t.Errorf("eligible = %v, want [2 3] — the sample must not change what is deleted", eligible)
	}
	for _, name := range report.Sample {
		if name == "Has Files But No Books" {
			t.Error("the held-back author leaked into the ELIGIBLE sample")
		}
	}
}

// Zero held-back authors leaves the sample empty; require_zero_files=false
// means nothing is held back on file grounds, so nothing is sampled either.
func TestPurgeEmptyAuthors_HeldBackSample_EmptyWhenNothingHeldBack(t *testing.T) {
	authors, books, files := purgeFixture()
	report, eligible := classifyEmptyAuthors(authors, books, nil, files, false)
	if len(report.HeldBackSample) != 0 || report.ZeroBooksWithFiles != 0 {
		t.Fatalf("require_zero_files=false: HeldBackSample=%+v ZeroBooksWithFiles=%d, want empty/0",
			report.HeldBackSample, report.ZeroBooksWithFiles)
	}
	if len(eligible) != 3 {
		t.Errorf("eligible = %v, want authors 2, 3 and 4", eligible)
	}

	// Still-referenced authors are HeldByRefs, not held back on files.
	report, _ = classifyEmptyAuthors(authors, books, map[int]int{4: 1}, files, true)
	if len(report.HeldBackSample) != 0 || report.HeldByRefs != 1 {
		t.Fatalf("referenced author: HeldBackSample=%+v HeldByRefs=%d, want empty/1",
			report.HeldBackSample, report.HeldByRefs)
	}
}

// The PRODUCTION shape the fixture caveat above describes: an author with files
// also has references, so it is claimed by the refCounts guard first. It must
// show up in HeldByRefsSample with both counts, leave HeldBackSample empty, and
// that sample must be capped while HeldByRefs stays uncapped. Without this
// sample the 822-author population would again be only a number.
func TestPurgeEmptyAuthors_HeldByRefsSample_ProductionShape(t *testing.T) {
	authors, books, files := purgeFixture()
	report, eligible := classifyEmptyAuthors(authors, books, map[int]int{4: 3}, files, true)

	want := heldBackAuthor{AuthorID: 4, Name: "Has Files But No Books", FileCount: 7, RefCount: 3}
	if len(report.HeldByRefsSample) != 1 || report.HeldByRefsSample[0] != want {
		t.Fatalf("HeldByRefsSample = %+v, want [%+v]", report.HeldByRefsSample, want)
	}
	if len(report.HeldBackSample) != 0 || report.ZeroBooksWithFiles != 0 {
		t.Errorf("HeldBackSample=%+v ZeroBooksWithFiles=%d, want empty/0: a referenced author is never file-held",
			report.HeldBackSample, report.ZeroBooksWithFiles)
	}
	if len(eligible) != 2 || eligible[0] != 2 || eligible[1] != 3 {
		t.Errorf("eligible = %v, want [2 3]", eligible)
	}

	// Cap: more referenced authors than the limit → sample capped, count not.
	const held = emptyAuthorSampleLimit + 5
	var many []database.Author
	refs := map[int]int{}
	for i := 1; i <= held; i++ {
		many = append(many, database.Author{ID: i, Name: fmt.Sprintf("Ref %d", i)})
		refs[i] = 1
	}
	report, _ = classifyEmptyAuthors(many, map[int]int{}, refs, map[int]int{}, true)
	if report.HeldByRefs != held || len(report.HeldByRefsSample) != emptyAuthorSampleLimit {
		t.Fatalf("HeldByRefs=%d sample=%d, want %d/%d", report.HeldByRefs, len(report.HeldByRefsSample), held, emptyAuthorSampleLimit)
	}
}

// 🔴 THE CAP. More than emptyAuthorSampleLimit held-back authors must produce
// a sample of exactly emptyAuthorSampleLimit entries while the COUNT stays
// uncapped. Without this a refactor could drop the cap and turn a report meant
// for eyeballing into another 822-line dump — or cap the counter too and hide
// the size of the problem.
func TestPurgeEmptyAuthors_HeldBackSample_CapAtLimit(t *testing.T) {
	const heldBack = emptyAuthorSampleLimit + 10
	var authors []database.Author
	books := map[int]int{}
	files := map[int]int{}
	for id := 1; id <= heldBack; id++ {
		authors = append(authors, database.Author{ID: id, Name: fmt.Sprintf("held %d", id)})
		books[id] = 0
		files[id] = id // distinct, non-zero
	}
	// Plus one genuinely empty author, which must still be eligible.
	junk := heldBack + 1
	authors = append(authors, database.Author{ID: junk, Name: "- junk"})

	report, eligible := classifyEmptyAuthors(authors, books, nil, files, true)
	if len(report.HeldBackSample) != emptyAuthorSampleLimit {
		t.Fatalf("len(HeldBackSample) = %d, want the cap %d", len(report.HeldBackSample), emptyAuthorSampleLimit)
	}
	if report.ZeroBooksWithFiles != heldBack {
		t.Errorf("ZeroBooksWithFiles = %d, want %d — the counter must not be capped with the sample",
			report.ZeroBooksWithFiles, heldBack)
	}
	for _, h := range report.HeldBackSample {
		if h.AuthorID < 1 || h.AuthorID > heldBack || h.FileCount != files[h.AuthorID] {
			t.Errorf("sample entry %+v is not a held-back author with its own file count", h)
		}
	}
	if len(eligible) != 1 || eligible[0] != junk {
		t.Errorf("eligible = %v, want only the junk author %d", eligible, junk)
	}
}

// End to end through the op: the dry run still deletes nothing with a
// held-back author present, and the new log line does not disturb the run.
func TestPurgeEmptyAuthors_HeldBackSample_DryRunStillInert(t *testing.T) {
	var deleted []int
	runPurge(t, `{"apply":false}`, &deleted)
	if len(deleted) != 0 {
		t.Fatalf("dry run deleted %v", deleted)
	}
}
