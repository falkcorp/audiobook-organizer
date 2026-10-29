// file: internal/plugins/maintenance/credits_backfill_gaps_test.go
// version: 1.1.0
// guid: 812b813b-fe22-4b23-bf28-6de81d6c94be
// last-edited: 2026-10-29

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
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// Tests for the two gaps left after PR #4200 and #4235 (credits backfill): an
// apply that was canceled (or lost the scan stand-down) filed the books it never
// reached under no class, and a narrator record left by an operation that
// crashed and was never resumed was taken for a preexisting one by every later
// operation.

// --- canceled_not_written ---------------------------------------------------

// cbCancelAfterWrites cancels the run's context once n ModifyBookCredits calls
// have returned (one worker: the n-th book is the last one written).
type cbCancelAfterWrites struct {
	database.BookCreditsModifier
	cancel context.CancelFunc
	n      int64
	seen   atomic.Int64
}

func (m *cbCancelAfterWrites) ModifyBookCredits(id string, fn func(*database.Book, *database.BookCreditsEdit) error) (*database.Book, database.BookCreditsEdit, error) {
	b, e, err := m.BookCreditsModifier.ModifyBookCredits(id, fn)
	if m.seen.Add(1) == m.n {
		m.cancel()
	}
	return b, e, err
}

// cbDispositions are the classes that say what became of a book that has
// something to write. A book is in exactly one of them (it may also carry
// informational classes such as filing_author_changes).
var cbDispositions = []string{
	cbAuthorFillFromAuthorID, cbNarratorFillFromColumn, cbNarratorFillFromNarratorsJSON,
	cbCanceledNotWritten, cbLimitReached, cbNarratorWaitingOnCreator,
	cbNarratorNewFromSplit, cbReadError, cbChangedSincePlan, cbWriteFailed,
	cbHeldOwnerManual, cbHeldITunes, cbHeldUserLocked, cbHeldNarratorLocked,
}

// cbAssertOneDisposition fails unless every id is in exactly one disposition
// class of res, and returns the class by id.
func cbAssertOneDisposition(t *testing.T, res *creditsBackfillResult, ids []string) map[string]string {
	t.Helper()
	got := map[string]string{}
	for _, id := range ids {
		var in []string
		for _, key := range cbDispositions {
			if slices.Contains(cbClassIDs(res, key), id) {
				in = append(in, key)
			}
		}
		if len(in) != 1 {
			t.Errorf("%s is in %d disposition classes %v, want exactly 1", id, len(in), in)
			continue
		}
		got[id] = in[0]
	}
	return got
}

func cbAuthorFillFixture(t *testing.T, n int) (*cbFixture, []string) {
	t.Helper()
	f := newCBFixture(t)
	bruce := f.author("Bruce Wayne")
	var ids []string
	for i := range n {
		id := "ab-" + string(rune('a'+i))
		f.book(id, "Batman: Year One "+id, func(b *database.Book) { b.AuthorID = intp(bruce) }, nil, nil)
		ids = append(ids, id)
	}
	return f, ids
}

// Canceled in phase 3 with a limit: every planned book ends up in exactly one
// class. Two are written, the cohort's other three were never reached, and the
// one the limit left out stays limit_reached.
func TestCreditsBackfill_CanceledApplyFilesTheCohortBooksItNeverReached(t *testing.T) {
	f, ids := cbAuthorFillFixture(t, 6)
	dry, err := f.run(creditsBackfillParams{}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got := cbClassIDs(dry, cbAuthorFillFromAuthorID); !slices.Equal(got, ids) {
		t.Fatalf("dry run plans %v, want %v", got, ids)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mod := &cbCancelAfterWrites{BookCreditsModifier: f.s, cancel: cancel, n: 2}
	env := creditsBackfillEnv{store: f.s, mod: mod, workers: 1}
	res, err := f.runEnv(ctx, env, creditsBackfillParams{Apply: true, Limit: 5}, &cbReporter{id: cbTestOpID})
	if err == nil || res.Complete {
		t.Fatalf("a canceled apply must be incomplete: err=%v", err)
	}
	disp := cbAssertOneDisposition(t, res, ids)
	want := map[string]string{
		"ab-a": cbAuthorFillFromAuthorID, "ab-b": cbAuthorFillFromAuthorID,
		"ab-c": cbCanceledNotWritten, "ab-d": cbCanceledNotWritten, "ab-e": cbCanceledNotWritten,
		"ab-f": cbLimitReached,
	}
	for id, w := range want {
		if disp[id] != w {
			t.Errorf("%s is under %q, want %q", id, disp[id], w)
		}
	}
	if res.Counts[cbCanceledNotWritten] != 3 {
		t.Errorf("counts[%s] = %d, want 3", cbCanceledNotWritten, res.Counts[cbCanceledNotWritten])
	}
	// Unreached means unchanged.
	for _, id := range []string{"ab-c", "ab-d", "ab-e", "ab-f"} {
		if st := f.state(id); len(st.Authors) != 0 {
			t.Errorf("%s was written: %v", id, st.Authors)
		}
	}
}

// Canceled in the replan (phase 1b) of an APPLY: the cohort is never picked, so
// every planned book, the plain one and the split ones alike, is unwritten and
// must be filed.
func TestCreditsBackfill_ApplyCanceledInTheReplanFilesEveryPlannedBook(t *testing.T) {
	f := newCBFixture(t)
	bruce := f.author("Bruce Wayne")
	f.narrator("Harvey Bullock")
	f.book("c-solo", "Batman: The Long Halloween", func(b *database.Book) {
		b.AuthorID = intp(bruce)
		b.Narrator = strp("Selina Kyle")
	}, ba("c-solo", [2]int{bruce, 0}), nil)
	ids := []string{"a-split-1", "a-split-2", "c-solo"}
	for _, id := range ids[:2] {
		f.book(id, "Batman: Dark Victory "+id, func(b *database.Book) {
			b.AuthorID = intp(bruce)
			b.Narrator = strp("Selina Kyle and Harvey Bullock")
		}, ba(id, [2]int{bruce, 0}), nil)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rep := &cbCancelOnReplanReporter{cbReporter: cbReporter{id: cbTestOpID}, cancel: cancel}
	res, err := runCreditsBackfillScan(ctx, creditsBackfillEnv{store: f.s, mod: f.s, opID: cbTestOpID, workers: 1}, creditsBackfillParams{Apply: true}, rep)
	if err == nil || res.Complete {
		t.Fatalf("a canceled apply must be incomplete: err=%v", err)
	}
	// a-split-2 was never reached by the replan, which filed it under review as
	// phase 1 left it; it has nothing else to write, so it is not planned. The
	// other two have a narrator write the canceled apply never started.
	disp := cbAssertOneDisposition(t, res, ids)
	want := map[string]string{"a-split-1": cbCanceledNotWritten, "a-split-2": cbNarratorNewFromSplit, "c-solo": cbCanceledNotWritten}
	for id, w := range want {
		if disp[id] != w {
			t.Errorf("%s is under %q, want %q", id, disp[id], w)
		}
	}
	for _, key := range []string{cbNarratorFillFromColumn, cbAuthorFillFromAuthorID} {
		if got := cbClassIDs(res, key); len(got) != 0 {
			t.Errorf("%s = %v, want none: nothing was written", key, got)
		}
	}
	if rows := cbBookWriteRows(f); len(rows) != 0 {
		t.Errorf("%d journal rows, want none", len(rows))
	}
}

// A run that was not stopped files nothing under the class, and neither does a
// dry run.
func TestCreditsBackfill_CanceledNotWrittenIsEmptyWhenNothingStopped(t *testing.T) {
	f, ids := cbAuthorFillFixture(t, 3)
	for _, apply := range []bool{false, true} {
		res, err := f.run(creditsBackfillParams{Apply: apply}, 2)
		if err != nil {
			t.Fatal(err)
		}
		if got := cbClassIDs(res, cbCanceledNotWritten); len(got) != 0 {
			t.Errorf("apply=%t: %s = %v, want none", apply, cbCanceledNotWritten, got)
		}
		if apply {
			cbAssertOneDisposition(t, res, ids)
		}
	}
}

// --- stops with more than one worker, and a lost stand-down ----------------

// Two workers: the cancel lands while a second book may be in flight, so the
// count of written books is not fixed, but the partition is: every planned book
// is written, unreached, or left out by the limit, and none is two of them.
// (Run under -race: the attempted set is written by the workers.)
func TestCreditsBackfill_CanceledApplyWithTwoWorkersFilesEveryBookOnce(t *testing.T) {
	f, ids := cbAuthorFillFixture(t, 10)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mod := &cbCancelAfterWrites{BookCreditsModifier: f.s, cancel: cancel, n: 2}
	env := creditsBackfillEnv{store: f.s, mod: mod, workers: 2}
	res, err := f.runEnv(ctx, env, creditsBackfillParams{Apply: true, Limit: 8}, &cbReporter{id: cbTestOpID})
	if err == nil || res.Complete {
		t.Fatalf("a canceled apply must be incomplete: err=%v", err)
	}
	disp := cbAssertOneDisposition(t, res, ids)
	counts := map[string]int{}
	for _, c := range disp {
		counts[c]++
	}
	if counts[cbLimitReached] != 2 {
		t.Errorf("limit_reached = %d, want 2 (limit 8 of 10)", counts[cbLimitReached])
	}
	if got := counts[cbAuthorFillFromAuthorID] + counts[cbCanceledNotWritten]; got != 8 {
		t.Errorf("written %d + unreached %d = %d, want the cohort of 8", counts[cbAuthorFillFromAuthorID], counts[cbCanceledNotWritten], got)
	}
	if counts[cbAuthorFillFromAuthorID] < 2 || counts[cbCanceledNotWritten] == 0 {
		t.Errorf("written %d, unreached %d: want at least the 2 written before the cancel and some unreached", counts[cbAuthorFillFromAuthorID], counts[cbCanceledNotWritten])
	}
	for id, c := range disp {
		if c == cbCanceledNotWritten {
			if st := f.state(id); len(st.Authors) != 0 {
				t.Errorf("%s is unreached but was written: %v", id, st.Authors)
			}
		}
	}
}

// A lapsed scan stand-down stops the apply the same way a cancel does: the
// books it never wrote are filed, and the error is the lost stand-down.
func TestCreditsBackfill_LostStandDownFilesTheBooksItNeverWrote(t *testing.T) {
	f, ids := cbAuthorFillFixture(t, 6)
	writes := &atomic.Int64{}
	env := creditsBackfillEnv{
		store: f.s,
		mod:   cbWriteCountMod{f.s, writes},
		scan:  &cbWriteLeaseScan{writes: writes, afterWrites: 2},
	}
	res, err := f.runEnv(context.Background(), env, creditsBackfillParams{Apply: true}, &cbReporter{id: cbTestOpID})
	if !errors.Is(err, registry.ErrScanStandDownLost) {
		t.Fatalf("err = %v, want the lost stand-down", err)
	}
	disp := cbAssertOneDisposition(t, res, ids)
	var written, unreached int
	for _, c := range disp {
		switch c {
		case cbAuthorFillFromAuthorID:
			written++
		case cbCanceledNotWritten:
			unreached++
		}
	}
	if written != 2 || unreached != 4 {
		t.Errorf("written %d, unreached %d, want 2 and 4", written, unreached)
	}
}

// --- leftovers of OTHER operations ------------------------------------------

const cbCrashedOp = "01CRASHEDOP"

// cbInsertOtherOp inserts an operation row of the given definition and status.
func cbInsertOtherOp(t *testing.T, f *cbFixture, opID, defID, status string, completedAt *time.Time) {
	t.Helper()
	queued := time.Now().Add(-2 * time.Hour).UTC()
	if err := f.s.InsertOperationV2(database.OperationV2Row{
		ID: opID, DefID: defID, Plugin: "maintenance", Status: status, QueuedAt: queued, StartedAt: &queued, CompletedAt: completedAt,
	}); err != nil {
		t.Fatalf("insert operation %s: %v", opID, err)
	}
}

// cbSeedOtherOp inserts an operation row of the given definition and status and
// journals a narrator_create_intent row for "Renee Montoya" under it one second
// before rec was created, the way CreateNarrator follows its intent.
func cbSeedOtherOp(t *testing.T, f *cbFixture, opID, defID, status string, rec *database.Narrator) {
	t.Helper()
	cbInsertOtherOp(t, f, opID, defID, status, nil)
	cbSeedRow(t, f, rec.CreatedAt.Add(-time.Second), database.OperationChange{
		OperationID: opID,
		ChangeType:  undo.ChangeTypeNarratorCreateIntent, FieldName: "narrator", NewValue: "Renee Montoya",
	})
}

// An operation crashed after CreateNarrator and was never resumed. A NEW
// operation, which splits a book naming the person, must not take the record
// for a preexisting one: nobody creates her, so the split book goes to review,
// and the record, which nothing links, is deleted under the new operation.
func TestCreditsBackfill_RecordACrashedOperationLeftIsNotPreexisting(t *testing.T) {
	for _, status := range []string{"failed", "canceled", "interrupted", "interrupted_quiesced", "running", "queued", "waiting_deps"} {
		t.Run(status, func(t *testing.T) {
			f, rec := cbLeftoverFixture(t, false)
			cbSeedOtherOp(t, f, cbCrashedOp, creditsBackfillOpID, status, rec)
			res := cbLeftoverRun(t, f)
			cbWantLeftoverDeleted(t, f, res)
			// The delete row says whose journal named the record.
			rows, err := f.s.GetOperationChanges(cbLeftoverOp)
			if err != nil {
				t.Fatal(err)
			}
			for _, c := range rows {
				if c.ChangeType == "narrator_delete" && !strings.Contains(c.NewValue, "source_op="+cbCrashedOp) {
					t.Errorf("narrator_delete NewValue = %q, want it to name %s", c.NewValue, cbCrashedOp)
				}
			}
		})
	}
}

// With a creator in the library the record the crashed operation left is created
// again by the new one (the store returns the same record), both books link to
// it, and it is kept.
func TestCreditsBackfill_RecordACrashedOperationLeftIsKeptWhenACreatorCreditsIt(t *testing.T) {
	f, rec := cbLeftoverFixture(t, true)
	cbSeedOtherOp(t, f, cbCrashedOp, creditsBackfillOpID, "failed", rec)
	res := cbLeftoverRun(t, f)
	if res.NarratorsCreated != 1 || res.UnusedNarratorsDeleted != 0 {
		t.Errorf("created=%d deleted=%d, want 1/0", res.NarratorsCreated, res.UnusedNarratorsDeleted)
	}
	for _, id := range []string{"z-solo", "a-split"} {
		st := f.state(id)
		if len(st.Narrators) == 0 || st.Narrators[0].NarratorID != rec.ID {
			t.Errorf("%s narrators = %v, want the record %d first", id, st.Narrators, rec.ID)
		}
	}
}

// cbWantPreexisting is the control outcome: the split book links to the record
// and nothing is deleted.
func cbWantPreexisting(t *testing.T, f *cbFixture, rec *database.Narrator, res *creditsBackfillResult) {
	t.Helper()
	if st := f.state("a-split"); len(st.Narrators) == 0 || st.Narrators[0].NarratorID != rec.ID {
		t.Errorf("a-split narrators = %v, want it linked to the preexisting record %d", st.Narrators, rec.ID)
	}
	if res.UnusedNarratorsDeleted != 0 {
		t.Errorf("deleted %d record(s), want the record kept", res.UnusedNarratorsDeleted)
	}
	if got := cbRecordNames(t, f); !slices.Equal(got, []string{"Harvey Bullock", "Renee Montoya"}) {
		t.Errorf("narrator records = %v, want the record kept", got)
	}
}

// Controls: an operation that completed cleaned up after itself, and another
// kind of operation's journal is not this op's business, so the record stays an
// ordinary preexisting one the split book links to.
func TestCreditsBackfill_RecordOfACompletedOrForeignOperationStaysPreexisting(t *testing.T) {
	for _, tc := range []struct{ name, def, status string }{
		{"completed", creditsBackfillOpID, "completed"},
		{"other definition", "maintenance.credit-census", "failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, rec := cbLeftoverFixture(t, false)
			cbSeedOtherOp(t, f, cbCrashedOp, tc.def, tc.status, rec)
			cbWantPreexisting(t, f, rec, cbLeftoverRun(t, f))
		})
	}
}

// A failed operation's intent row is read at every later apply, and must not
// condemn a record of the same name made LATER by something else (the narrator
// sync for an imported book, say): the record was created long after the
// operation's row and after the operation ended.
func TestCreditsBackfill_RecordMadeAfterTheCrashedOperationEndedStaysPreexisting(t *testing.T) {
	t.Run("long after its intent row", func(t *testing.T) {
		f, rec := cbLeftoverFixture(t, false)
		cbInsertOtherOp(t, f, cbCrashedOp, creditsBackfillOpID, "failed", nil)
		cbSeedRow(t, f, rec.CreatedAt.Add(-time.Hour), database.OperationChange{
			OperationID: cbCrashedOp, ChangeType: undo.ChangeTypeNarratorCreateIntent, FieldName: "narrator", NewValue: "Renee Montoya",
		})
		cbWantPreexisting(t, f, rec, cbLeftoverRun(t, f))
	})
	t.Run("after the operation row says it ended", func(t *testing.T) {
		f, rec := cbLeftoverFixture(t, false)
		ended := rec.CreatedAt.Add(-time.Hour)
		cbInsertOtherOp(t, f, cbCrashedOp, creditsBackfillOpID, "failed", &ended)
		// The intent row is just before the record (inside the row window), but
		// the operation ended an hour before the record existed.
		cbSeedRow(t, f, rec.CreatedAt.Add(-time.Second), database.OperationChange{
			OperationID: cbCrashedOp, ChangeType: undo.ChangeTypeNarratorCreateIntent, FieldName: "narrator", NewValue: "Renee Montoya",
		})
		cbWantPreexisting(t, f, rec, cbLeftoverRun(t, f))
	})
	t.Run("own operation's old intent row", func(t *testing.T) {
		f, rec := cbLeftoverFixture(t, false)
		cbSeedIntent(t, f, rec.CreatedAt.Add(-time.Hour))
		cbWantPreexisting(t, f, rec, cbLeftoverRun(t, f))
	})
}

// A record that predates the other operation's first journal row was never that
// operation's, whatever its rows say, on the intent path and on the
// CreatedNarrators path.
func TestCreditsBackfill_RecordOlderThanTheCrashedOperationStaysPreexisting(t *testing.T) {
	t.Run("intent", func(t *testing.T) {
		f, rec := cbLeftoverFixture(t, false)
		cbInsertOtherOp(t, f, cbCrashedOp, creditsBackfillOpID, "failed", nil)
		cbSeedRow(t, f, rec.CreatedAt.Add(time.Hour), database.OperationChange{
			OperationID: cbCrashedOp, ChangeType: undo.ChangeTypeNarratorCreateIntent, FieldName: "narrator", NewValue: "Renee Montoya",
		})
		cbWantPreexisting(t, f, rec, cbLeftoverRun(t, f))
	})
	t.Run("created_narrators", func(t *testing.T) {
		f, rec := cbLeftoverFixture(t, false)
		cbInsertOtherOp(t, f, cbCrashedOp, creditsBackfillOpID, "failed", nil)
		cbSeedCreatedRow(t, f, cbCrashedOp, rec.CreatedAt.Add(time.Hour), rec, true)
		cbWantPreexisting(t, f, rec, cbLeftoverRun(t, f))
	})
}

// The CreatedNarrators id path of another operation: a voided credits_backfill
// row (a refused write) naming the record, after the operation's first row and
// right after the record was made, makes it a leftover; a row that is older than
// the record by more than the window, or an operation that ended before the
// record existed, does not.
func TestCreditsBackfill_CreatedNarratorsRowOfACrashedOperation(t *testing.T) {
	t.Run("leftover", func(t *testing.T) {
		f, rec := cbLeftoverFixture(t, false)
		cbInsertOtherOp(t, f, cbCrashedOp, creditsBackfillOpID, "failed", nil)
		cbSeedFirstRow(t, f, cbCrashedOp, rec.CreatedAt.Add(-time.Hour))
		cbSeedCreatedRow(t, f, cbCrashedOp, rec.CreatedAt.Add(time.Second), rec, true)
		cbWantLeftoverDeleted(t, f, cbLeftoverRun(t, f))
	})
	t.Run("row long before the record", func(t *testing.T) {
		f, rec := cbLeftoverFixture(t, false)
		cbInsertOtherOp(t, f, cbCrashedOp, creditsBackfillOpID, "failed", nil)
		cbSeedFirstRow(t, f, cbCrashedOp, rec.CreatedAt.Add(-2*time.Hour))
		cbSeedCreatedRow(t, f, cbCrashedOp, rec.CreatedAt.Add(-time.Hour), rec, true)
		cbWantPreexisting(t, f, rec, cbLeftoverRun(t, f))
	})
	t.Run("operation ended before the record", func(t *testing.T) {
		f, rec := cbLeftoverFixture(t, false)
		ended := rec.CreatedAt.Add(-time.Hour)
		cbInsertOtherOp(t, f, cbCrashedOp, creditsBackfillOpID, "failed", &ended)
		cbSeedFirstRow(t, f, cbCrashedOp, rec.CreatedAt.Add(-2*time.Hour))
		cbSeedCreatedRow(t, f, cbCrashedOp, rec.CreatedAt.Add(time.Second), rec, true)
		cbWantPreexisting(t, f, rec, cbLeftoverRun(t, f))
	})
}

// A crashed operation's record that a book links now is credited, not a
// leftover.
func TestCreditsBackfill_LinkedRecordOfACrashedOperationIsNotALeftover(t *testing.T) {
	f, rec := cbLeftoverFixture(t, false)
	cbSeedOtherOp(t, f, cbCrashedOp, creditsBackfillOpID, "failed", rec)
	alfred := f.author("Alfred Pennyworth")
	f.narrators[rec.ID] = true
	f.book("bystander", "Gotham Central: Soft Targets", func(b *database.Book) { b.AuthorID = intp(alfred) }, ba("bystander", [2]int{alfred, 0}), nil)
	if err := f.s.SetBookNarrators("bystander", []database.BookNarrator{{BookID: "bystander", NarratorID: rec.ID, Role: "narrator"}}); err != nil {
		t.Fatal(err)
	}
	res := cbLeftoverRun(t, f)
	if st := f.state("a-split"); len(st.Narrators) == 0 || st.Narrators[0].NarratorID != rec.ID {
		t.Errorf("a-split narrators = %v, want it linked to the credited record %d", st.Narrators, rec.ID)
	}
	if res.UnusedNarratorsDeleted != 0 {
		t.Errorf("deleted %d record(s), want the linked record kept", res.UnusedNarratorsDeleted)
	}
}

// --- fail closed -------------------------------------------------------------

// cbNoListerStore hides everything but the backfill store's own surface, so the
// store cannot list operations.
type cbNoListerStore struct{ creditsBackfillStore }

// cbFaultStore wraps the store and fails one read, so the apply must refuse
// rather than decide without it. Unwrap lets the capabilities (link counts,
// row sets) through.
type cbFaultStore struct {
	creditsBackfillStore
	listErr    error
	changesOp  string // GetOperationChanges fails for this operation id
	changesErr error
	nameErr    error // the FIRST GetNarratorByName fails: the leftover search's lookup, not a later one
	nameCalls  *atomic.Int64
	reads      *sync.Map // operation id -> *atomic.Int64 of GetOperationChanges calls
}

func (s cbFaultStore) Unwrap() database.Store {
	st, _ := s.creditsBackfillStore.(database.Store)
	return st
}

func (s cbFaultStore) ListOperationsV2Since(since time.Time, limit int) ([]database.OperationV2Row, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	return s.creditsBackfillStore.(database.OperationsV2SinceLister).ListOperationsV2Since(since, limit)
}

func (s cbFaultStore) GetOperationChanges(opID string) ([]*database.OperationChange, error) {
	if s.reads != nil {
		n, _ := s.reads.LoadOrStore(opID, new(atomic.Int64))
		n.(*atomic.Int64).Add(1)
	}
	if s.changesErr != nil && opID == s.changesOp {
		return nil, s.changesErr
	}
	return s.creditsBackfillStore.GetOperationChanges(opID)
}

func (s cbFaultStore) GetNarratorByName(name string) (*database.Narrator, error) {
	if s.nameErr != nil && s.nameCalls.Add(1) == 1 {
		return nil, s.nameErr
	}
	return s.creditsBackfillStore.(narratorByNameReader).GetNarratorByName(name)
}

// cbRefusedApply runs an apply on store and checks that it refused to start: an
// error containing want, and no journal row of the operation (nothing written).
func cbRefusedApply(t *testing.T, f *cbFixture, store creditsBackfillStore, want string) {
	t.Helper()
	env := creditsBackfillEnv{store: store, mod: f.s, opID: cbLeftoverOp, workers: 2}
	_, err := f.runEnv(context.Background(), env, creditsBackfillParams{Apply: true}, &cbReporter{id: cbLeftoverOp})
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("err = %v, want the apply refused with %q", err, want)
	}
	rows, rerr := f.s.GetOperationChanges(cbLeftoverOp)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if len(rows) != 0 {
		t.Errorf("%d journal rows written by a refused apply", len(rows))
	}
}

// An apply that cannot find the other operations' leftovers does not guess: it
// refuses to start, before anything is written.
func TestCreditsBackfill_ApplyFailsClosedWhenOperationsCannotBeListed(t *testing.T) {
	f, _ := cbLeftoverFixture(t, true)
	cbRefusedApply(t, f, cbNoListerStore{f.s}, "cannot list operations")
	cbRefusedApply(t, f, cbFaultStore{creditsBackfillStore: f.s, listErr: errors.New("injected list failure")}, "injected list failure")
}

func TestCreditsBackfill_ApplyFailsClosedWhenAnotherOperationsJournalCannotBeRead(t *testing.T) {
	f, rec := cbLeftoverFixture(t, true)
	cbSeedOtherOp(t, f, cbCrashedOp, creditsBackfillOpID, "failed", rec)
	cbRefusedApply(t, f, cbFaultStore{creditsBackfillStore: f.s, changesOp: cbCrashedOp, changesErr: errors.New("injected journal failure")}, "injected journal failure")
}

func TestCreditsBackfill_ApplyFailsClosedWhenAnIntentNameCannotBeLookedUp(t *testing.T) {
	f, rec := cbLeftoverFixture(t, true)
	cbSeedOtherOp(t, f, cbCrashedOp, creditsBackfillOpID, "failed", rec)
	cbRefusedApply(t, f, cbFaultStore{creditsBackfillStore: f.s, nameErr: errors.New("injected lookup failure"), nameCalls: &atomic.Int64{}}, "injected lookup failure")
}

func TestCreditsBackfill_ApplyFailsClosedOnARowItCannotRead(t *testing.T) {
	t.Run("credits_backfill row that does not decode", func(t *testing.T) {
		f, rec := cbLeftoverFixture(t, true)
		cbInsertOtherOp(t, f, cbCrashedOp, creditsBackfillOpID, "failed", nil)
		cbSeedRow(t, f, rec.CreatedAt, database.OperationChange{
			OperationID: cbCrashedOp, BookID: "z-solo", ChangeType: undo.ChangeTypeCreditsBackfill, FieldName: "book_credits",
			OldValue: "{not json", NewValue: "{not json",
		})
		cbRefusedApply(t, f, f.s, cbCrashedOp)
	})
	t.Run("row whose id is not a ULID", func(t *testing.T) {
		f, rec := cbLeftoverFixture(t, true)
		cbInsertOtherOp(t, f, cbCrashedOp, creditsBackfillOpID, "failed", nil)
		if err := f.s.CreateOperationChange(&database.OperationChange{
			ID: "not-a-ulid", OperationID: cbCrashedOp, ChangeType: undo.ChangeTypeNarratorCreateIntent, FieldName: "narrator", NewValue: rec.Name,
		}); err != nil {
			t.Fatal(err)
		}
		cbRefusedApply(t, f, f.s, "not a ULID")
	})
}

// --- the judged marker -------------------------------------------------------

func cbJournalReads(reads *sync.Map, opID string) int64 {
	n, ok := reads.Load(opID)
	if !ok {
		return 0
	}
	return n.(*atomic.Int64).Load()
}

// Once an apply has judged a crashed operation's leftovers, its journal is not
// read again while its row is unchanged; an operation that is resumed afterwards
// (its row changes) is read again.
func TestCreditsBackfill_JudgedOperationIsNotReadAgainUntilItChanges(t *testing.T) {
	f, rec := cbLeftoverFixture(t, false)
	cbSeedOtherOp(t, f, cbCrashedOp, creditsBackfillOpID, "failed", rec)
	reads := &sync.Map{}
	run := func(opID string) {
		t.Helper()
		env := creditsBackfillEnv{store: cbFaultStore{creditsBackfillStore: f.s, reads: reads}, mod: f.s, opID: opID, workers: 2}
		if _, err := f.runEnv(context.Background(), env, creditsBackfillParams{Apply: true}, &cbReporter{id: opID}); err != nil {
			t.Fatalf("apply %s: %v", opID, err)
		}
	}
	run("01SWEEPONE")
	if got := cbJournalReads(reads, cbCrashedOp); got != 1 {
		t.Fatalf("first apply read the crashed journal %d times, want 1", got)
	}
	if got := cbRecordNames(t, f); !slices.Equal(got, []string{"Harvey Bullock"}) {
		t.Fatalf("narrator records = %v, want the leftover deleted by the first apply", got)
	}
	run("01SWEEPTWO")
	if got := cbJournalReads(reads, cbCrashedOp); got != 1 {
		t.Errorf("second apply read the judged journal again (%d reads), want it skipped", got)
	}
	// The operation is resumed and cut off again: its row changed.
	if err := f.s.ResetOperationV2ForResume(cbCrashedOp); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := f.s.UpdateOperationV2Status(cbCrashedOp, "failed", nil, &now, nil); err != nil {
		t.Fatal(err)
	}
	run("01SWEEPTHREE")
	if got := cbJournalReads(reads, cbCrashedOp); got != 2 {
		t.Errorf("third apply read the changed operation's journal %d times in all, want 2", got)
	}
}

// An apply whose cleanup could not settle a leftover (the stand-down lapsed)
// does not mark the operation judged, so the next apply reads it again.
func TestCreditsBackfill_LostStandDownLeavesTheOperationUnjudged(t *testing.T) {
	f, rec := cbLeftoverFixture(t, false)
	cbSeedOtherOp(t, f, cbCrashedOp, creditsBackfillOpID, "failed", rec)
	// Give the run books to write so the lease can be lost after the first.
	bruce := f.author("Bruce Wayne")
	f.narrators[rec.ID] = true // f.book drops every record the fixture has not registered
	for i := range 4 {
		id := fmt.Sprintf("sd-%d", i)
		f.book(id, "Hush "+id, func(b *database.Book) { b.AuthorID = intp(bruce) }, nil, nil)
	}
	reads := &sync.Map{}
	writes := &atomic.Int64{}
	env := creditsBackfillEnv{
		store:   cbFaultStore{creditsBackfillStore: f.s, reads: reads},
		mod:     cbWriteCountMod{f.s, writes},
		scan:    &cbWriteLeaseScan{writes: writes, afterWrites: 1},
		workers: 1,
	}
	if _, err := f.runEnv(context.Background(), env, creditsBackfillParams{Apply: true}, &cbReporter{id: "01LOSTONE"}); !errors.Is(err, registry.ErrScanStandDownLost) {
		t.Fatalf("err = %v, want the lost stand-down", err)
	}
	env = creditsBackfillEnv{store: cbFaultStore{creditsBackfillStore: f.s, reads: reads}, mod: f.s, workers: 1}
	if _, err := f.runEnv(context.Background(), env, creditsBackfillParams{Apply: true}, &cbReporter{id: "01LOSTTWO"}); err != nil {
		t.Fatal(err)
	}
	if got := cbJournalReads(reads, cbCrashedOp); got != 2 {
		t.Errorf("the crashed journal was read %d times, want 2 (the lost stand-down left it unjudged)", got)
	}
}
