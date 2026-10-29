// file: internal/plugins/maintenance/credits_backfill_gaps_test.go
// version: 1.0.0
// guid: 812b813b-fe22-4b23-bf28-6de81d6c94be
// last-edited: 2026-10-29

package maintenance

import (
	"context"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/falkcorp/audiobook-organizer/internal/database"
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

// --- leftovers of OTHER operations ------------------------------------------

const cbCrashedOp = "01CRASHEDOP"

// cbSeedOtherOp inserts an operation row of the given definition and status and
// journals a narrator_create_intent row for "Renee Montoya" under it, an hour
// ago (the record the fixture made is newer).
func cbSeedOtherOp(t *testing.T, f *cbFixture, opID, defID, status string) {
	t.Helper()
	queued := time.Now().Add(-2 * time.Hour).UTC()
	if err := f.s.InsertOperationV2(database.OperationV2Row{
		ID: opID, DefID: defID, Plugin: "maintenance", Status: status, QueuedAt: queued, StartedAt: &queued,
	}); err != nil {
		t.Fatalf("insert operation %s: %v", opID, err)
	}
	at := time.Now().Add(-time.Hour)
	if err := f.s.CreateOperationChange(&database.OperationChange{
		ID:          ulid.MustNew(ulid.Timestamp(at), ulid.DefaultEntropy()).String(),
		OperationID: opID,
		ChangeType:  undo.ChangeTypeNarratorCreateIntent, FieldName: "narrator", NewValue: "Renee Montoya",
	}); err != nil {
		t.Fatalf("seed intent: %v", err)
	}
}

// An operation crashed after CreateNarrator and was never resumed. A NEW
// operation, which splits a book naming the person, must not take the record
// for a preexisting one: nobody creates her, so the split book goes to review,
// and the record, which nothing links, is deleted under the new operation.
func TestCreditsBackfill_RecordACrashedOperationLeftIsNotPreexisting(t *testing.T) {
	for _, status := range []string{"failed", "canceled", "interrupted_quiesced", "running"} {
		t.Run(status, func(t *testing.T) {
			f, _ := cbLeftoverFixture(t, false)
			cbSeedOtherOp(t, f, cbCrashedOp, creditsBackfillOpID, status)
			cbWantLeftoverDeleted(t, f, cbLeftoverRun(t, f))
		})
	}
}

// With a creator in the library the record the crashed operation left is created
// again by the new one (the store returns the same record), both books link to
// it, and it is kept.
func TestCreditsBackfill_RecordACrashedOperationLeftIsKeptWhenACreatorCreditsIt(t *testing.T) {
	f, rec := cbLeftoverFixture(t, true)
	cbSeedOtherOp(t, f, cbCrashedOp, creditsBackfillOpID, "failed")
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
			cbSeedOtherOp(t, f, cbCrashedOp, tc.def, tc.status)
			res := cbLeftoverRun(t, f)
			if st := f.state("a-split"); len(st.Narrators) == 0 || st.Narrators[0].NarratorID != rec.ID {
				t.Errorf("a-split narrators = %v, want it linked to the preexisting record %d", st.Narrators, rec.ID)
			}
			if res.UnusedNarratorsDeleted != 0 {
				t.Errorf("deleted %d record(s), want the record kept", res.UnusedNarratorsDeleted)
			}
		})
	}
}

// A crashed operation's record that a book links now is credited, not a
// leftover.
func TestCreditsBackfill_LinkedRecordOfACrashedOperationIsNotALeftover(t *testing.T) {
	f, rec := cbLeftoverFixture(t, false)
	cbSeedOtherOp(t, f, cbCrashedOp, creditsBackfillOpID, "failed")
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

// cbNoListerStore hides everything but the backfill store's own surface, so the
// store cannot list operations.
type cbNoListerStore struct{ creditsBackfillStore }

// An apply that cannot find the other operations' leftovers does not guess: it
// refuses to start, before anything is written.
func TestCreditsBackfill_ApplyFailsClosedWhenOperationsCannotBeListed(t *testing.T) {
	f, _ := cbLeftoverFixture(t, true)
	env := creditsBackfillEnv{store: cbNoListerStore{f.s}, mod: f.s, opID: cbLeftoverOp, workers: 2}
	_, err := f.runEnv(context.Background(), env, creditsBackfillParams{Apply: true}, &cbReporter{id: cbLeftoverOp})
	if err == nil || !strings.Contains(err.Error(), "cannot list operations") {
		t.Fatalf("err = %v, want the apply refused for a store that cannot list operations", err)
	}
	rows, rerr := f.s.GetOperationChanges(cbLeftoverOp)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if len(rows) != 0 {
		t.Errorf("%d journal rows written by a refused apply", len(rows))
	}
}
