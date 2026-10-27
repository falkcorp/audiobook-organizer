// file: internal/plugins/maintenance/credits_backfill_rereview_test.go
// version: 1.0.0
// guid: 7d2e9a46-1c8b-4f35-a0d7-93b6e5c41f28
// last-edited: 2026-10-27

package maintenance

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// Tests for the second adversarial review of PR #4195 (credits backfill).
// Fixtures: Batman characters as people, Batman storylines as titles, paths
// under /lib/Gotham (cbFixture.book). Nothing here comes from a real library.

// runEnv runs the scan with an env the caller built (a scan controller, a
// wrapped store or modifier) over this fixture.
func (f *cbFixture) runEnv(ctx context.Context, env creditsBackfillEnv, params creditsBackfillParams, rep *cbReporter) (*creditsBackfillResult, error) {
	f.t.Helper()
	env.opID = rep.id
	if env.workers == 0 {
		env.workers = 1
	}
	return runCreditsBackfillScan(ctx, env, params, rep)
}

func cbRevert(t *testing.T, f *cbFixture) *audiobooks.RevertResult {
	t.Helper()
	res, err := audiobooks.NewRevertService(f.s).RevertOperation(cbTestOpID)
	if err != nil {
		t.Fatalf("revert: %v", err)
	}
	return res
}

// --- stand-down lease -------------------------------------------------------

// cbReadLeaseScan is a ScanController whose lease lapses by READS, not by
// wall-clock time: a renewal fails once more than maxReads store reads
// happened since the last renewal (or the acquire). A real lease is 5 minutes
// against a planning pass of hours; counting reads models "planning took
// longer than the lease" without a timing-dependent test.
type cbReadLeaseScan struct {
	reads    *atomic.Int64
	maxReads int64
	mu       sync.Mutex
	last     int64
	renews   int
	lost     bool
}

func (s *cbReadLeaseScan) AcquireScanStandDown(context.Context, string, string) (func(), error) {
	s.mu.Lock()
	s.last = s.reads.Load()
	s.mu.Unlock()
	return func() {}, nil
}

func (s *cbReadLeaseScan) RenewScanStandDown(string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.renews++
	now := s.reads.Load()
	if s.lost || now-s.last > s.maxReads {
		s.lost = true
		return false
	}
	s.last = now
	return true
}

func (s *cbReadLeaseScan) ScanStandDownValid(string) bool { return true }

// cbReadCountStore counts book reads (one per planned book).
type cbReadCountStore struct {
	*database.PebbleStore
	reads *atomic.Int64
}

func (s cbReadCountStore) GetBookByID(id string) (*database.Book, error) {
	s.reads.Add(1)
	return s.PebbleStore.GetBookByID(id)
}

// Planning is where the time goes in a full-library apply, and the lease is
// shorter than it: the hold must be renewed during planning, not only before
// each write, or the first write finds the lease gone and the run writes
// nothing (and every re-run does the same).
func TestCreditsBackfill_StandDownLeaseIsRenewedDuringPlanning(t *testing.T) {
	f := newCBFixture(t)
	bruce := f.author("Bruce Wayne")
	const books = 10
	for i := 0; i < books; i++ {
		id := fmt.Sprintf("sd-%02d", i)
		f.book(id, "Knightfall "+id, func(b *database.Book) { b.AuthorID = intp(bruce) }, nil, nil)
	}
	reads := &atomic.Int64{}
	scan := &cbReadLeaseScan{reads: reads, maxReads: 3}
	env := creditsBackfillEnv{store: cbReadCountStore{f.s, reads}, mod: f.s, scan: scan}
	res, err := f.runEnv(context.Background(), env, creditsBackfillParams{Apply: true}, &cbReporter{id: cbTestOpID})
	if err != nil {
		t.Fatalf("apply: %v (renews %d, lost %v)", err, scan.renews, scan.lost)
	}
	if got := len(cbClassIDs(res, cbAuthorFillFromAuthorID)); got != books {
		t.Fatalf("written %d of %d books (renews %d)", got, books, scan.renews)
	}
	if scan.renews < books {
		t.Fatalf("renewals = %d, want at least one per planned book (%d)", scan.renews, books)
	}
}

// cbWriteLeaseScan loses the lease once afterWrites books have been written.
type cbWriteLeaseScan struct {
	writes      *atomic.Int64
	afterWrites int64
}

func (s *cbWriteLeaseScan) AcquireScanStandDown(context.Context, string, string) (func(), error) {
	return func() {}, nil
}
func (s *cbWriteLeaseScan) RenewScanStandDown(string) bool { return s.writes.Load() < s.afterWrites }
func (s *cbWriteLeaseScan) ScanStandDownValid(string) bool { return true }

type cbWriteCountMod struct {
	*database.PebbleStore
	writes *atomic.Int64
}

func (m cbWriteCountMod) ModifyBookCredits(id string, fn func(*database.Book, *database.BookCreditsEdit) error) (*database.Book, database.BookCreditsEdit, error) {
	b, e, err := m.PebbleStore.ModifyBookCredits(id, fn)
	if err == nil && b != nil {
		m.writes.Add(1)
	}
	return b, e, err
}

// A lease lost partway through the writes stops the next write, and the
// operator is told the real reason once: not "context canceled", and not one
// copy of the error per remaining book.
func TestCreditsBackfill_LostStandDownIsReportedAsTheCause(t *testing.T) {
	f := newCBFixture(t)
	bruce := f.author("Bruce Wayne")
	const books = 6
	for i := 0; i < books; i++ {
		id := fmt.Sprintf("sl-%02d", i)
		f.book(id, "Hush "+id, func(b *database.Book) { b.AuthorID = intp(bruce) }, nil, nil)
	}
	writes := &atomic.Int64{}
	env := creditsBackfillEnv{
		store: f.s,
		mod:   cbWriteCountMod{f.s, writes},
		scan:  &cbWriteLeaseScan{writes: writes, afterWrites: 2},
	}
	res, err := f.runEnv(context.Background(), env, creditsBackfillParams{Apply: true}, &cbReporter{id: cbTestOpID})
	if !errors.Is(err, registry.ErrScanStandDownLost) {
		t.Fatalf("err = %v, want it to wrap registry.ErrScanStandDownLost", err)
	}
	if strings.Contains(err.Error(), "context canceled") {
		t.Fatalf("err names the symptom, not the cause: %v", err)
	}
	if n := strings.Count(err.Error(), "scan stand-down lease lost"); n != 1 {
		t.Fatalf("the cause appears %d times in %q, want once", n, err)
	}
	if res == nil || res.Complete || !strings.Contains(res.Error, "stand-down") {
		t.Fatalf("result = %+v, want an incomplete result carrying the stand-down error", res)
	}
	if got := writes.Load(); got != 2 {
		t.Fatalf("%d books written after the lease was lost at 2", got)
	}
}

// --- empty-join snapshot conflict -------------------------------------------

// With an empty join the organizer files a book under its Book.Author
// snapshot. When the snapshot names a different live author than AuthorID (a
// rescan reverted AuthorID from file tags), filling the join from AuthorID
// would flip the filing author and the snapshot refresh would destroy the
// evidence: the book is held for review and left exactly as it was.
func TestCreditsBackfill_EmptyJoinSnapshotNamingAnotherAuthorIsHeldForReview(t *testing.T) {
	f := newCBFixture(t)
	jason := f.author("Jason Todd")
	dick := f.author("Dick Grayson")
	f.book("ej-conflict", "Under the Red Hood", func(b *database.Book) {
		b.AuthorID = intp(jason)
		b.Author = &database.Author{ID: dick, Name: "Dick Grayson"}
	}, nil, nil)
	// Control: the snapshot names the SAME author (under an old name): the
	// plan's fill and snapshot refresh stand.
	f.book("ej-stale", "Batman: Year One", func(b *database.Book) {
		b.AuthorID = intp(jason)
		b.Author = &database.Author{ID: jason, Name: "Jay Todd"}
	}, nil, nil)
	idx, err := buildCreditCensusIndex(f.s)
	if err != nil {
		t.Fatal(err)
	}
	filing := func(id string) string {
		st := f.state(id)
		name, err := filingAuthorName(idx, st.Authors, st.Author, st.AuthorID)
		if err != nil {
			t.Fatal(err)
		}
		return name
	}
	before := f.state("ej-conflict")
	pre := filing("ej-conflict")
	if pre != "Dick Grayson" {
		t.Fatalf("fixture: the organizer files ej-conflict under %q, want the snapshot's Dick Grayson", pre)
	}

	for _, apply := range []bool{false, true} {
		res, err := f.run(creditsBackfillParams{Apply: apply}, 1)
		if err != nil {
			t.Fatalf("apply=%v: %v", apply, err)
		}
		if got := cbClassIDs(res, cbAuthorIDSnapshotConflict); !slices.Equal(got, []string{"ej-conflict"}) {
			t.Fatalf("apply=%v: %s = %v, want [ej-conflict]", apply, cbAuthorIDSnapshotConflict, got)
		}
		for _, class := range []string{cbAuthorFillFromAuthorID, cbAuthorSnapshotRefreshed, cbFilingAuthorChanges} {
			if cbHasID(res, class, "ej-conflict") {
				t.Fatalf("apply=%v: ej-conflict is in %s: it must be held, not written", apply, class)
			}
		}
		if !cbHasID(res, cbAuthorFillFromAuthorID, "ej-stale") || !cbHasID(res, cbAuthorSnapshotRefreshed, "ej-stale") {
			t.Fatalf("apply=%v: the control book (snapshot names AuthorID's own author) was not planned for a fill and a refresh", apply)
		}
	}
	got := f.state("ej-conflict")
	if got.Author == nil || got.Author.ID != dick || got.Author.Name != "Dick Grayson" {
		t.Fatalf("snapshot after the apply = %+v, want it untouched (Dick Grayson)", got.Author)
	}
	if len(got.Authors) != 0 || got.AuthorID == nil || *got.AuthorID != jason {
		t.Fatalf("ej-conflict was written: join %v author_id %v (before %v)", cbAuthorIDs(got.Authors), got.AuthorID, before.AuthorID)
	}
	if post := filing("ej-conflict"); post != pre {
		t.Fatalf("filing author flipped %q -> %q", pre, post)
	}
	if st := f.state("ej-stale"); len(st.Authors) != 1 || st.Author == nil || st.Author.Name != "Jason Todd" {
		t.Fatalf("control book not filled and refreshed: join %v snapshot %+v", cbAuthorIDs(st.Authors), st.Author)
	}
}

// --- fold-linked narrator ---------------------------------------------------

// A narrator name that links to a record only without its accents ("Renee"
// for the record "Renée") is credited under the record's spelling, so the
// Narrator column must be rewritten to it in the same write: the census
// compares column and junction by normalized spelling and would otherwise
// count the written book as narrator_column_differs_junction. The journal
// keeps the old column, so the revert restores it.
func TestCreditsBackfill_FoldLinkedNarratorLeavesColumnAndJunctionAgreeing(t *testing.T) {
	f := newCBFixture(t)
	bruce := f.author("Bruce Wayne")
	renee := f.narrator("Renée Montoya")
	f.book("fl-col", "Gotham Central: In the Line of Duty", func(b *database.Book) {
		b.AuthorID = intp(bruce)
		b.Narrator = strp("Renee Montoya")
	}, ba("fl-col", [2]int{bruce, 0}), nil)

	res, err := f.run(creditsBackfillParams{Apply: true}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !cbHasID(res, cbNarratorFillFromColumn, "fl-col") {
		t.Fatalf("fl-col not written: counts %v", res.Counts)
	}
	st := f.state("fl-col")
	if ids := cbNarratorIDs(st.Narrators); !slices.Equal(ids, []int{renee}) {
		t.Fatalf("junction = %v, want the existing record %d (no second record)", ids, renee)
	}
	if st.Narrator == nil || *st.Narrator != "Renée Montoya" {
		t.Fatalf("column = %v, want the record's spelling", st.Narrator)
	}
	assertPlansClean(t, f, "fl-col")

	// Revert: the column and the junction come back as they were.
	if r := cbRevert(t, f); r.Restored != 1 {
		t.Fatalf("revert = %+v, want 1 restored", r)
	}
	st = f.state("fl-col")
	if st.Narrator == nil || *st.Narrator != "Renee Montoya" || len(st.Narrators) != 0 {
		t.Fatalf("after the revert: column %v junction %v, want the pre-image", st.Narrator, cbNarratorIDs(st.Narrators))
	}
}

// Two books naming one new person under spellings of one fold key share one
// record (the first one created); the second book's column follows that
// record's spelling, as a linked record's does.
func TestCreditsBackfill_NewNarratorSharedByFoldKeyFollowsTheCreatedSpelling(t *testing.T) {
	f := newCBFixture(t)
	bruce := f.author("Bruce Wayne")
	f.book("fn-1", "Gotham Central: Half a Life", func(b *database.Book) {
		b.AuthorID = intp(bruce)
		b.Narrator = strp("Renee Montoya")
	}, ba("fn-1", [2]int{bruce, 0}), nil)
	f.book("fn-2", "Gotham Central: Corrigan", func(b *database.Book) {
		b.AuthorID = intp(bruce)
		b.Narrator = strp("Renée Montoya")
	}, ba("fn-2", [2]int{bruce, 0}), nil)

	if _, err := f.run(creditsBackfillParams{Apply: true}, 1); err != nil {
		t.Fatal(err)
	}
	a, b := f.state("fn-1"), f.state("fn-2")
	if len(a.Narrators) != 1 || len(b.Narrators) != 1 || a.Narrators[0].NarratorID != b.Narrators[0].NarratorID {
		t.Fatalf("junctions %v / %v, want one shared record", cbNarratorIDs(a.Narrators), cbNarratorIDs(b.Narrators))
	}
	if a.Narrator == nil || b.Narrator == nil || *a.Narrator != *b.Narrator {
		t.Fatalf("columns %v / %v, want both rewritten to the one record's spelling", a.Narrator, b.Narrator)
	}
	assertPlansClean(t, f, "fn-1", "fn-2")
}

// NarratorsJSON keeps the old spelling, and the op does not rewrite it: a
// fold-linked name from NarratorsJSON would leave the JSON matching neither
// the column nor the junction, so the book is listed for review instead of
// written into a state the census flags.
func TestCreditsBackfill_FoldLinkedNarratorsJSONIsListedForReview(t *testing.T) {
	f := newCBFixture(t)
	bruce := f.author("Bruce Wayne")
	f.narrator("Renée Montoya")
	f.book("fj-1", "Gotham Central: Soft Targets", func(b *database.Book) {
		b.AuthorID = intp(bruce)
		b.NarratorsJSON = strp(`["Renee Montoya"]`)
	}, ba("fj-1", [2]int{bruce, 0}), nil)

	res, err := f.run(creditsBackfillParams{Apply: true}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !cbHasID(res, ccNarratorsJSONDisagrees, "fj-1") || cbHasID(res, cbNarratorFillFromNarratorsJSON, "fj-1") {
		t.Fatalf("fj-1 not held for review under %s: counts %v", ccNarratorsJSONDisagrees, res.Counts)
	}
	if st := f.state("fj-1"); len(st.Narrators) != 0 || st.Narrator != nil {
		t.Fatalf("fj-1 was written: junction %v column %v", cbNarratorIDs(st.Narrators), st.Narrator)
	}
}

// assertPlansClean fails when the credit census still counts any of the books
// in a narrator or author review class, or a re-run of the backfill finds
// anything left to write or review on them.
func assertPlansClean(t *testing.T, f *cbFixture, ids ...string) {
	t.Helper()
	census, err := runCreditCensusScan(context.Background(), f.s, &fakeReporter{}, 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range census.Classes {
		if c.Kind != creditKindDisagreement {
			continue
		}
		for _, id := range ids {
			if slices.Contains(c.BookIDs, id) {
				t.Errorf("census class %s still lists %s after the write", c.Key, id)
			}
		}
	}
	again, err := f.run(creditsBackfillParams{}, 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range again.Classes {
		if c.Kind != cbKindWrite && c.Kind != cbKindReview {
			continue
		}
		for _, id := range ids {
			if slices.Contains(c.BookIDs, id) {
				t.Errorf("a re-run lists %s under %s (%s): it does not plan clean", id, c.Key, c.Kind)
			}
		}
	}
}

// --- field locks ------------------------------------------------------------

// lockingMod sets a user lock on the book right before the write reaches the
// store: a person locking the field after the plan.
type lockingMod struct {
	*database.PebbleStore
	f     *cbFixture
	field string
}

func (m lockingMod) ModifyBookCredits(id string, fn func(*database.Book, *database.BookCreditsEdit) error) (*database.Book, database.BookCreditsEdit, error) {
	m.f.lock(id, m.field)
	return m.PebbleStore.ModifyBookCredits(id, fn)
}

// A lock a person sets between the plan and the write holds the book: it is
// not written, its journal row is voided, it is listed as held, and the run is
// not failed. A re-run then plans it with the lock in place.
func TestCreditsBackfill_LockAppearingAfterThePlanHoldsTheBook(t *testing.T) {
	for _, tc := range []struct {
		name  string
		field string
		class string
	}{
		{"author", database.FieldKeyAuthorName, cbHeldUserLocked},
		{"narrator", database.FieldKeyNarrator, cbHeldNarratorLocked},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newCBFixture(t)
			bruce := f.author("Bruce Wayne")
			f.narrator("Oswald Cobblepot")
			f.book("lk-1", "The Court of Owls", func(b *database.Book) {
				b.AuthorID = intp(bruce)
				b.Narrator = strp("Oswald Cobblepot")
			}, nil, nil)
			before := f.state("lk-1")

			env := creditsBackfillEnv{store: f.s, mod: lockingMod{f.s, f, tc.field}}
			res, err := f.runEnv(context.Background(), env, creditsBackfillParams{Apply: true}, &cbReporter{id: cbTestOpID})
			if err != nil {
				t.Fatalf("apply: %v", err)
			}
			if !res.Complete || res.Counts[cbWriteFailed] != 0 || res.Counts[cbChangedSincePlan] != 0 {
				t.Fatalf("a held book failed the run: complete=%v counts=%v", res.Complete, res.Counts)
			}
			if !cbHasID(res, tc.class, "lk-1") {
				t.Fatalf("lk-1 not listed under %s: counts %v", tc.class, res.Counts)
			}
			if res.BooksToWrite != 0 {
				t.Fatalf("books_to_write = %d, want 0", res.BooksToWrite)
			}
			if got := f.state("lk-1"); !undo.SameCreditsBackfillState(got, before) {
				t.Fatalf("a locked book was written: %+v -> %+v", before, got)
			}
			rows, err := f.s.GetOperationChanges(cbTestOpID)
			if err != nil {
				t.Fatal(err)
			}
			for _, row := range rows {
				if !row.Voided {
					t.Fatalf("journal row %s for the locked book stands: it must be voided", row.ID)
				}
			}
		})
	}
}

// cbLockOnWriteReporter sets a user lock on a book the moment the write pass
// starts its item (before the apply's own lock check).
type cbLockOnWriteReporter struct {
	cbReporter
	f *cbFixture
	// books are locked, by the "writing n/total" label's item index (the
	// cohort is written in sorted id order), with field.
	books []string
	field string
}

func (r *cbLockOnWriteReporter) SetCurrentItem(label string) {
	var n, total int
	if _, err := fmt.Sscanf(label, "Credits backfill: writing %d/%d", &n, &total); err == nil && n >= 1 && n <= len(r.books) {
		r.f.lock(r.books[n-1], r.field)
	}
}

// The check before anything is created or journaled: a lock set after the
// plan keeps the book from getting a journal row at all.
func TestCreditsBackfill_LockAppearingBeforeTheWriteLeavesNoJournalRow(t *testing.T) {
	f := newCBFixture(t)
	bruce := f.author("Bruce Wayne")
	f.book("lj-1", "Year One", func(b *database.Book) { b.AuthorID = intp(bruce) }, nil, nil)
	f.book("lj-2", "Year Two", func(b *database.Book) { b.AuthorID = intp(bruce) }, nil, nil)
	rep := &cbLockOnWriteReporter{cbReporter: cbReporter{id: cbTestOpID}, f: f, books: []string{"lj-1", "lj-2"}, field: database.FieldKeyAuthorName}
	res, err := runCreditsBackfillScan(context.Background(), creditsBackfillEnv{store: f.s, mod: f.s, opID: cbTestOpID, workers: 1}, creditsBackfillParams{Apply: true}, rep)
	if err != nil {
		t.Fatal(err)
	}
	if got := cbClassIDs(res, cbHeldUserLocked); !slices.Equal(got, []string{"lj-1", "lj-2"}) {
		t.Fatalf("%s = %v, want both books", cbHeldUserLocked, got)
	}
	rows, err := f.s.GetOperationChanges(cbTestOpID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("%d journal rows for books held before their write", len(rows))
	}
}

// --- limit cohort -----------------------------------------------------------

// failingReadStore fails the read of one book.
type failingReadStore struct {
	*database.PebbleStore
	bad string
}

func (s failingReadStore) GetBookByID(id string) (*database.Book, error) {
	if id == s.bad {
		return nil, errors.New("injected read failure")
	}
	return s.PebbleStore.GetBookByID(id)
}

// A book whose read fails in phase 1 may have been eligible; it keeps its
// place in a limited cohort instead of the next eligible book taking its
// slot, so the cohort does not depend on a transient read error.
func TestCreditsBackfill_UnreadableBookKeepsItsPlaceInTheLimitCohort(t *testing.T) {
	f := newCBFixture(t)
	bruce := f.author("Bruce Wayne")
	for _, id := range []string{"lm-01", "lm-02", "lm-03", "lm-04"} {
		f.book(id, "Arkham "+id, func(b *database.Book) { b.AuthorID = intp(bruce) }, nil, nil)
	}
	env := creditsBackfillEnv{store: failingReadStore{f.s, "lm-02"}, mod: f.s}
	res, err := f.runEnv(context.Background(), env, creditsBackfillParams{Apply: true, Limit: 2}, &cbReporter{id: cbTestOpID})
	if err == nil || res.Complete {
		t.Fatalf("a run with an unreadable book must be incomplete: err=%v", err)
	}
	if got := cbClassIDs(res, cbAuthorFillFromAuthorID); !slices.Equal(got, []string{"lm-01"}) {
		t.Fatalf("written = %v, want only lm-01 (lm-02 holds the second slot unwritten)", got)
	}
	if got := cbClassIDs(res, cbLimitReached); !slices.Equal(got, []string{"lm-03", "lm-04"}) {
		t.Fatalf("%s = %v, want [lm-03 lm-04]", cbLimitReached, got)
	}
	if got := cbClassIDs(res, cbReadError); !slices.Equal(got, []string{"lm-02"}) {
		t.Fatalf("%s = %v, want [lm-02]", cbReadError, got)
	}

	// The re-run reads it: one slot is left (lm-01 counts), and lm-02 takes it.
	res2, err := f.run(creditsBackfillParams{Apply: true, Limit: 2}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got := cbClassIDs(res2, cbAuthorFillFromAuthorID); !slices.Equal(got, []string{"lm-01", "lm-02"}) {
		t.Fatalf("after the re-run written = %v, want [lm-01 lm-02]", got)
	}
}

// --- revert of a write that never landed ------------------------------------

// A journal row whose write never landed (the run died between the journal
// and ModifyBookCredits) on a book whose stored lists are not canonical: the
// revert finds the book at its raw pre-image, which is already restored. It is
// not refused as changed-since, and it is not rewritten: the book keeps the
// exact rows it had.
func TestCreditsBackfill_RevertOfAWriteThatNeverLandedIsNotRefused(t *testing.T) {
	f := newCBFixture(t)
	bruce := f.author("Bruce Wayne")
	selina := f.author("Selina Kyle")
	raw := ba("sr-1", [2]int{bruce, 0}, [2]int{selina, 0})
	f.book("sr-1", "Hush", func(b *database.Book) { b.AuthorID = intp(bruce) }, raw, nil)
	if _, err := f.run(creditsBackfillParams{Apply: true}, 1); err != nil {
		t.Fatal(err)
	}
	// Put the book back exactly as it was before the write.
	f.raw("book_authors:sr-1", raw)
	before := f.state("sr-1")
	res, err := audiobooks.NewRevertService(f.s).RevertOperation(cbTestOpID)
	if err != nil {
		t.Fatalf("revert: %v (%+v)", err, res)
	}
	if res.ChangedSince != 0 || res.Failed != 0 || res.Restored != 1 {
		t.Fatalf("revert = %+v, want the row counted restored with no changed-since refusal", res)
	}
	if got := f.state("sr-1"); !undo.SameCreditsBackfillState(got, before) {
		t.Fatalf("the revert rewrote a book already at its pre-image: %+v -> %+v", before, got)
	}
}
