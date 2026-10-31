// file: internal/database/pebble_store_opchange_verify_test.go
// version: 1.0.0
// guid: 3d0f6a52-8c1e-4b7a-9e44-2a5b7c91d0e6
// last-edited: 2026-10-30

package database

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"
)

// verifyTestWindow makes a verify checkpoint every n rows for the test.
func verifyTestWindow(t *testing.T, n int) {
	t.Helper()
	old := opChangeVerifyProgressRows
	opChangeVerifyProgressRows = n
	t.Cleanup(func() { opChangeVerifyProgressRows = old })
}

// seedGotham writes n journal rows through the store (so each has its entry)
// under one operation whose id sorts at opID, books alternating between two
// Batman characters.
func seedGotham(t *testing.T, p *PebbleStore, opID string, from, n int) {
	t.Helper()
	books := []string{"batman", "robin"}
	for i := from; i < from+n; i++ {
		c := &OperationChange{ID: fmt.Sprintf("c%04d", i), OperationID: opID, BookID: books[i%2], ChangeType: "metadata_update"}
		if err := p.CreateOperationChange(c); err != nil {
			t.Fatal(err)
		}
	}
}

// bootAgain forgets this process's trust, as a restart does; the store's
// persisted keys stay.
func bootAgain(p *PebbleStore) {
	p.opChangeByBookTrustedAt.Store(0)
	p.publishOpChangeTrust()
}

func verifyCursor(t *testing.T, p *PebbleStore) (opChangeVerifyState, bool) {
	t.Helper()
	st, ok, err := p.readOpChangeVerifyState()
	if err != nil {
		t.Fatal(err)
	}
	return st, ok
}

func verifiedSentinel(t *testing.T, p *PebbleStore) bool {
	t.Helper()
	ok, err := p.opChangeVerifiedSentinel()
	if err != nil {
		t.Fatal(err)
	}
	return ok
}

// yieldNow is a deadline far enough away that the run is not cut, with a margin
// larger than the time left, so the verify is "due" at its first checkpoint.
func yieldNow(t *testing.T) (context.Context, OpChangeEnsureOptions) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	t.Cleanup(cancel)
	return ctx, OpChangeEnsureOptions{YieldMargin: time.Hour}
}

// The verify yields at the deadline with a durable cursor, and the next run
// continues from that cursor instead of from the first row.
func TestOpchangeVerify_YieldsAtDeadlineWithCursorAndResumesFromIt(t *testing.T) {
	verifyTestWindow(t, 10)
	p := newOpChangeTestStore(t)
	seedGotham(t, p, "op-year-one", 0, 40)

	ctx, opts := yieldNow(t)
	var totals []int
	opts.VerifyTotal = func(n int) { totals = append(totals, n) }
	res, err := p.EnsureOpChangeByBookIndexWith(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Yielded || res.Trusted || p.opChangeByBookIndexTrusted() {
		t.Fatalf("first run = %+v, want Yielded, not trusted", res)
	}
	if res.Verify.Rows != 10 || res.Verify.VerifiedRows != 10 || res.Verify.Complete || !res.Verify.Yielded {
		t.Fatalf("first verify = %+v, want 10 rows read, incomplete, yielded", res.Verify)
	}
	st, ok := verifyCursor(t, p)
	if !ok || st.Rows != 10 || st.Cursor != "opchange:op-year-one:c0009" {
		t.Fatalf("cursor after the yield = %+v (present %t), want rows=10 cursor=opchange:op-year-one:c0009", st, ok)
	}
	if verifiedSentinel(t, p) {
		t.Fatal("verified sentinel set by a verify that did not reach the end of the journal")
	}
	if !equalInts(totals, []int{40}) {
		t.Fatalf("VerifyTotal calls = %v, want one call with the whole pass (40)", totals)
	}

	// The same operation, run again with no deadline pressure.
	var positions []int
	res, err = p.EnsureOpChangeByBookIndexWith(context.Background(), OpChangeEnsureOptions{
		Progress: func(phase string, rows int) {
			if phase == "verify" {
				positions = append(positions, rows)
			}
		},
		VerifyTotal: func(n int) { totals = append(totals, n) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Backfill.Skipped {
		t.Fatalf("resumed run re-ran the backfill: %+v", res.Backfill)
	}
	v := res.Verify
	if v.ResumedAfter != "opchange:op-year-one:c0009" || v.Rows != 30 || v.VerifiedRows != 40 || !v.Complete {
		t.Fatalf("resumed verify = %+v, want resumed_after c0009, 30 rows this run, 40 verified, complete", v)
	}
	if !res.Trusted || !p.opChangeByBookIndexTrusted() || !verifiedSentinel(t, p) {
		t.Fatalf("resumed run = %+v, want trusted with the verified sentinel set", res)
	}
	if len(positions) == 0 {
		t.Fatal("the resumed verify reported no position")
	}
	for _, pos := range positions {
		if pos <= 10 {
			t.Fatalf("progress positions %v: a resumed verify must report an absolute position (> 10), not restart at 0", positions)
		}
	}
	if !equalInts(totals, []int{40, 40}) {
		t.Fatalf("VerifyTotal calls = %v, want 40 on both runs (the total counts the rows already checked)", totals)
	}
}

// A run with no deadline, or no margin, never yields; and a deadline that is
// further away than the margin does not either.
func TestOpchangeVerify_YieldNeedsADeadlineInsideTheMargin(t *testing.T) {
	verifyTestWindow(t, 10)
	p := newOpChangeTestStore(t)
	seedGotham(t, p, "op-year-one", 0, 40)

	res, err := p.EnsureOpChangeByBookIndexWith(context.Background(), OpChangeEnsureOptions{YieldMargin: time.Hour})
	if err != nil || res.Yielded || !res.Trusted {
		t.Fatalf("no deadline: res=%+v err=%v, want a normal trusted run", res, err)
	}
	bootAgain(p)
	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	res, err = p.EnsureOpChangeByBookIndexWith(ctx, OpChangeEnsureOptions{YieldMargin: time.Second})
	if err != nil || res.Yielded || !res.Trusted {
		t.Fatalf("deadline outside the margin: res=%+v err=%v, want a normal trusted run", res, err)
	}
}

// The trusted state is set only once a pass has covered the whole journal: not
// by a cut pass, not by a yielded one, not by a cursor alone.
func TestOpchangeVerify_TrustedSentinelOnlyAfterFullCoverage(t *testing.T) {
	verifyTestWindow(t, 10)
	p := newOpChangeTestStore(t)
	seedGotham(t, p, "op-year-one", 0, 40)

	// Cut by a cancel inside the verify: the cursor is durable, nothing trusted.
	ctx, cancel := context.WithCancel(context.Background())
	_, err := p.EnsureOpChangeByBookIndexWith(ctx, OpChangeEnsureOptions{Progress: func(phase string, rows int) {
		if phase == "verify" && rows >= 20 {
			cancel()
		}
	}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if p.opChangeByBookIndexTrusted() || verifiedSentinel(t, p) {
		t.Fatal("a cancelled verify trusted the index or set the verified sentinel")
	}
	if st, ok := verifyCursor(t, p); !ok || st.Rows < 20 {
		t.Fatalf("cursor after the cancel = %+v (present %t), want it to have advanced to at least 20 rows", st, ok)
	}
	// A cursor with the sentinel unset does not let a reader use the index.
	if p.OpChangeByBookIndexUsable() {
		t.Fatal("GetBookChanges would read the index on the strength of a verify cursor")
	}

	res, err := p.EnsureOpChangeByBookIndexWith(context.Background(), OpChangeEnsureOptions{})
	if err != nil || !res.Trusted || !verifiedSentinel(t, p) || !p.OpChangeByBookIndexUsable() {
		t.Fatalf("completing run: res=%+v err=%v sentinel=%t, want trusted with the sentinel set", res, err, verifiedSentinel(t, p))
	}
	if res.Verify.ResumedAfter == "" || res.Verify.Rows >= 40 {
		t.Fatalf("completing run read %d rows after %q, want it to resume past the cut", res.Verify.Rows, res.Verify.ResumedAfter)
	}
}

// After a trusted verify, rows appended later are covered by the next boot's
// run, and only they are read.
func TestOpchangeVerify_AppendedRowsCoveredWithoutReWalking(t *testing.T) {
	verifyTestWindow(t, 10)
	p := newOpChangeTestStore(t)
	seedGotham(t, p, "op-year-one", 0, 40)
	mustTrustOpChange(t, p)
	before, _ := verifyCursor(t, p)

	// Rows appended by this binary (with entries).
	seedGotham(t, p, "op-year-two", 0, 3)
	bootAgain(p)
	res, err := p.EnsureOpChangeByBookIndexWith(context.Background(), OpChangeEnsureOptions{})
	if err != nil {
		t.Fatal(err)
	}
	v := res.Verify
	if !res.Trusted || !v.PriorCoverage || v.Rows != 3 || v.ResumedAfter != before.Cursor || v.VerifiedRows != 43 {
		t.Fatalf("tail verify = %+v (trusted %t), want a trusted run that read the 3 appended rows after %q", v, res.Trusted, before.Cursor)
	}
	if st, _ := verifyCursor(t, p); st.Cursor != "opchange:op-year-two:c0002" || st.Rows != 43 {
		t.Fatalf("cursor after the tail verify = %+v, want it moved to the last appended row", st)
	}

	// A row an older binary appended: no entry. The tail verify finds it and
	// the ensure rebuilds.
	rawPutOpChange(t, p, &OperationChange{ID: "c0000", OperationID: "op-year-zodiac", BookID: "batman"})
	bootAgain(p)
	res, err = p.EnsureOpChangeByBookIndexWith(context.Background(), OpChangeEnsureOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Verify.Rows != 1 || res.Verify.MissingEntries != 1 || !res.Rebuilt || !res.Trusted {
		t.Fatalf("tail verify of a row with no entry = %+v rebuilt=%t trusted=%t, want 1 row read, 1 missing, rebuilt, trusted",
			res.Verify, res.Rebuilt, res.Trusted)
	}
	if got := opChangeEntries(t, p, "op-year-zodiac", "c0000"); len(got) != 1 || got[0] != "batman" {
		t.Fatalf("entries of the appended row after the rebuild = %v, want [batman]", got)
	}
}

// A rebuild resets the cursor and the sentinel when it starts; a cut rebuild
// leaves neither, and a finished one records its own coverage.
func TestOpchangeVerify_RebuildResetsCursorAndSentinel(t *testing.T) {
	verifyTestWindow(t, 10)
	oldChunk := opChangeByBookBackfillChunk
	opChangeByBookBackfillChunk = 10
	t.Cleanup(func() { opChangeByBookBackfillChunk = oldChunk })
	p := newOpChangeTestStore(t)
	seedGotham(t, p, "op-year-one", 0, 40)
	mustTrustOpChange(t, p)
	if _, ok := verifyCursor(t, p); !ok || !verifiedSentinel(t, p) {
		t.Fatal("setup: the trusted ensure left no cursor and sentinel")
	}

	cut := errors.New("cut")
	opChangeByBookBackfillAfterChunk = func(commits int) error {
		if commits == 2 {
			return cut
		}
		return nil
	}
	_, err := p.RebuildOpChangeByBookIndex(context.Background(), nil)
	opChangeByBookBackfillAfterChunk = nil
	if !errors.Is(err, cut) {
		t.Fatalf("rebuild err = %v, want the synthetic cut", err)
	}
	if _, ok := verifyCursor(t, p); ok || verifiedSentinel(t, p) {
		t.Fatal("a cut rebuild left the verify cursor or the verified sentinel behind")
	}
	if p.opChangeByBookIndexTrusted() {
		t.Fatal("a cut rebuild left the index trusted")
	}

	// The boot ensure resumes the cut rebuild's backfill and then verifies the
	// whole journal from the first row: the cursor is not carried over.
	res := mustTrustOpChange(t, p)
	if res.Backfill.ResumedAfter == "" || res.Verify.ResumedAfter != "" || res.Verify.Rows != 40 {
		t.Fatalf("ensure after a cut rebuild = backfill %+v verify %+v, want the backfill resumed and a verify of all 40 rows from the start",
			res.Backfill, res.Verify)
	}

	// A finished rebuild covered every row itself: its last row is the cursor.
	if _, err := p.RebuildOpChangeByBookIndex(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	st, ok := verifyCursor(t, p)
	if !ok || st.Rows != 40 || st.Cursor != "opchange:op-year-one:c0039" || !verifiedSentinel(t, p) {
		t.Fatalf("state after a finished rebuild = %+v (present %t), want the rebuild's coverage (40 rows, last c0039)", st, ok)
	}
	bootAgain(p)
	res = mustTrustOpChange(t, p)
	if res.Verify.Rows != 0 || res.Verify.ResumedAfter != "opchange:op-year-one:c0039" {
		t.Fatalf("ensure after a finished rebuild = %+v, want only the tail (nothing) read", res.Verify)
	}
}

// A dirty verify stops at its next window instead of walking the rest, and the
// rebuild it forces leaves the index trusted.
func TestOpchangeVerify_DirtyStopsEarlyAndRebuilds(t *testing.T) {
	verifyTestWindow(t, 10)
	p := newOpChangeTestStore(t)
	mustBackfillOpChange(t, p)
	for i := range 40 {
		rawPutOpChange(t, p, &OperationChange{ID: fmt.Sprintf("c%04d", i), OperationID: "op-year-one", BookID: "joker"})
	}
	res, err := p.EnsureOpChangeByBookIndexWith(context.Background(), OpChangeEnsureOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Verify.Rows != 10 || res.Verify.MissingEntries != 10 || !res.Rebuilt || !res.Trusted {
		t.Fatalf("ensure = verify %+v rebuilt=%t trusted=%t, want the verify to stop after its first 10-row window and the rebuild to trust",
			res.Verify, res.Rebuilt, res.Trusted)
	}
	if got := countOpChangeIndexKeys(t, p); got != 40 {
		t.Fatalf("index entries after the rebuild = %d, want 40", got)
	}
}

// The documented limit of the tail verify: a row an older binary wrote BELOW
// the cursor (a smaller opID prefix) is not read by it, and is found by the
// periodic full walk.
func TestOpchangeVerify_BelowCursorRowIsFoundByTheFullWalk(t *testing.T) {
	verifyTestWindow(t, 10)
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	oldNow, oldEvery := opChangeVerifyNow, opChangeVerifyFullEvery
	opChangeVerifyNow = func() time.Time { return now }
	t.Cleanup(func() { opChangeVerifyNow, opChangeVerifyFullEvery = oldNow, oldEvery })

	p := newOpChangeTestStore(t)
	seedGotham(t, p, "op-year-one", 0, 20)
	mustTrustOpChange(t, p)

	rawPutOpChange(t, p, &OperationChange{ID: "c0000", OperationID: "arkham-batch", BookID: "joker"}) // sorts before "op-"
	bootAgain(p)
	res := mustTrustOpChange(t, p)
	if res.Verify.Rows != 0 || res.Rebuilt {
		t.Fatalf("tail verify = %+v rebuilt=%t, want it to read nothing: the row sits below the cursor", res.Verify, res.Rebuilt)
	}

	now = now.Add(opChangeVerifyFullEvery + time.Hour)
	bootAgain(p)
	res, err := p.EnsureOpChangeByBookIndexWith(context.Background(), OpChangeEnsureOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// The stray sorts first, so the walk finds it in its first window and stops.
	if res.Verify.ResumedAfter != "" || res.Verify.Rows != 10 || res.Verify.MissingEntries != 1 || !res.Rebuilt || !res.Trusted {
		t.Fatalf("full walk = %+v rebuilt=%t trusted=%t, want a walk from the first row that finds the stray in its first window, rebuilt",
			res.Verify, res.Rebuilt, res.Trusted)
	}
}

// A cursor left behind by an index that no longer exists (a Reset wiped the
// sentinel, then a racing verify wrote a cursor into the new keyspace) must not
// make the next verify skip rows.
func TestOpchangeVerify_StaleCursorWithoutSentinelIsDropped(t *testing.T) {
	verifyTestWindow(t, 10)
	p := newOpChangeTestStore(t)
	seedGotham(t, p, "op-year-one", 0, 20)
	if err := p.saveOpChangeVerifyState(p.opChangeByBookGen.Load(), opChangeVerifyState{
		Cursor: "opchange:op-year-one:c0018", Rows: 19, Started: time.Now(),
	}, true); err != nil {
		t.Fatal(err)
	}
	res := mustTrustOpChange(t, p) // no backfill sentinel: the backfill runs
	if res.Backfill.Skipped || res.Verify.ResumedAfter != "" || res.Verify.Rows != 20 {
		t.Fatalf("ensure = backfill %+v verify %+v, want the backfill to run and the verify to read all 20 rows", res.Backfill, res.Verify)
	}
}

// The read-only verify (the preview) is a full walk that neither reads nor
// writes the resume point.
func TestOpchangeVerify_PreviewWalksEverythingAndWritesNothing(t *testing.T) {
	verifyTestWindow(t, 10)
	p := newOpChangeTestStore(t)
	seedGotham(t, p, "op-year-one", 0, 30)
	mustTrustOpChange(t, p)
	seedGotham(t, p, "op-year-two", 0, 5)

	rep, err := p.VerifyOpChangeByBookIndex(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Rows != 35 || rep.VerifiedRows != 35 || rep.ResumedAfter != "" || !rep.Complete {
		t.Fatalf("preview = %+v, want a full walk of 35 rows", rep)
	}
	if st, _ := verifyCursor(t, p); st.Rows != 30 {
		t.Fatalf("the preview moved the cursor: %+v", st)
	}
}

// A tail verify reads only the rows after the cursor, so the undecodable rows
// before it are counted from the marker family, and a repaired one stops
// counting.
func TestOpchangeVerify_UndecodableCountSurvivesATailVerify(t *testing.T) {
	verifyTestWindow(t, 10)
	p := newOpChangeTestStore(t)
	seedGotham(t, p, "op-year-one", 0, 5)
	if err := p.db.Set(opChangeKey("op-year-one", "c9999"), []byte("{not json"), pebble.Sync); err != nil {
		t.Fatal(err)
	}
	res := mustTrustOpChange(t, p)
	if res.Verify.Undecodable != 1 {
		t.Fatalf("first verify undecodable = %d, want 1", res.Verify.Undecodable)
	}
	seedGotham(t, p, "op-year-two", 0, 2)
	bootAgain(p)
	res = mustTrustOpChange(t, p)
	if res.Verify.Rows != 2 || res.Verify.Undecodable != 1 {
		t.Fatalf("tail verify = %+v, want 2 rows read and the earlier undecodable row still counted", res.Verify)
	}
	if err := p.db.Delete(opChangeKey("op-year-one", "c9999"), pebble.Sync); err != nil {
		t.Fatal(err)
	}
	bootAgain(p)
	res = mustTrustOpChange(t, p)
	if res.Verify.Undecodable != 0 {
		t.Fatalf("after the bad row was removed, undecodable = %d, want 0", res.Verify.Undecodable)
	}
}

// A Reset during the pass bumps the generation; the verify must not write a
// cursor into the wiped keyspace.
func TestOpchangeVerify_NoCursorWrittenAfterAReset(t *testing.T) {
	verifyTestWindow(t, 10)
	p := newOpChangeTestStore(t)
	seedGotham(t, p, "op-year-one", 0, 40)
	_, err := p.EnsureOpChangeByBookIndexWith(context.Background(), OpChangeEnsureOptions{Progress: func(phase string, rows int) {
		if phase == "verify" && rows == 10 {
			p.opChangeByBookGen.Add(1)
		}
	}})
	if !errors.Is(err, errOpChangeStoreReset) {
		t.Fatalf("err = %v, want errOpChangeStoreReset", err)
	}
	if p.opChangeByBookIndexTrusted() {
		t.Fatal("a verify of a reset store trusted the index")
	}
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
