// file: internal/plugins/maintenance/fragment_lease_test.go
// version: 1.8.1
// guid: 6b1e8d42-3c7f-4a95-b2d6-9f0a4e7c1d38
// last-edited: 2026-10-03

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary"
)

// fragApplyErr is fragFixture.apply without requiring success.
func (f *fragFixture) applyErr(t *testing.T, planOpID, opID string, rowIDs []string, resume *repairs.ApplyCheckpoint) (*repairs.ApplyResult, error) {
	t.Helper()
	no := false
	params, err := json.Marshal(repairs.ApplyParams{FixerID: fragFixerID, PlanOpID: planOpID, RowIDs: rowIDs, DryRun: &no, Resume: resume})
	require.NoError(t, err)
	f.applyOp(opID, fragFixerID)
	rep := &repairsOpReporter{id: opID}
	runErr := f.p.runRepairsApply(context.Background(), params, rep)
	res, _ := rep.result.(*repairs.ApplyResult)
	return res, runErr
}

func (f *fragFixture) scan() *scriptedScan { return f.p.deps.(scanDeps).scan }

// holdMergeLockPastTheLease holds merge.LockMergeRMW while start's apply
// waits for it, for six lease lengths of the scan's clock, and releases it.
// start launches the apply in a goroutine and returns; the caller collects
// the apply's result after this returns, pass or fail.
//
// No wall-clock margin decides the outcome. The scan's lease clock moves only
// here, 3/4 of a lease at a time, and only after a renewal has landed since
// the last move (scriptedScan.advanceAfterRenewal), so the lock is held until
// the test has OBSERVED the renewals, however slowly the runner schedules
// them. Before the lock is released the apply can renew three times without
// the wait's own renewals (RunApply's two per-row beats and LockWaiting's
// beat before it blocks); the eight moves need eight, so at least five come
// from the wait. An apply that blocks without renewing never gets the clock
// moved again and fails the test on the liveness deadline below, which only bounds a
// run that is already failing. The clock stops before the lock is released,
// so the row's own writes are never raced against the lease: how long one
// store write takes on a busy runner is not what these tests are about.
//
// (They used a 400ms wall-clock lease and a 1.5s sleep. The wait renewed
// correctly, and then the lease lapsed between two of the row's WRITES on a
// slow CI runner, where one write took longer than 400ms.)
func holdMergeLockPastTheLease(t *testing.T, scan *scriptedScan, wait *repairs.WaitOptions, start func()) {
	t.Helper()
	const (
		ttl   = 5 * time.Minute // the registry's lease
		moves = 8               // x 3/4 ttl = six leases
	)
	scan.mu.Lock()
	scan.ttl = ttl
	scan.mu.Unlock()
	wait.LockRenewEvery = 2 * time.Millisecond

	merge.LockMergeRMW()
	// Released on every path, the failing one included: the caller then
	// collects the apply's result before the fixture's store closes.
	defer merge.UnlockMergeRMW()
	start()
	deadline := time.Now().Add(time.Minute)
	for moved := 0; moved < moves; {
		if scan.advanceAfterRenewal(ttl * 3 / 4) {
			moved++
			continue
		}
		if time.Now().After(deadline) {
			t.Errorf("the apply stopped renewing the lease while it waited for the merge lock: %d of %d renewals seen", moved, moves)
			return
		}
		time.Sleep(time.Millisecond)
	}
}

// An outside holder keeps merge.LockMergeRMW (a dedup merge, say) for longer
// than the stand-down lease while the fragment row waits for it. The wait
// renews the lease, so the row applies once the lock is free. Before the fix
// the fixer blocked in LockMergeRMW with no renewal, the lease lapsed, and
// the row's first write was refused.
//
// Not parallel: it holds the process-wide merge lock.
func TestFragmentFixer_MergeLockWaitKeepsTheLease(t *testing.T) {
	f := newFragFixture(t)
	f.seed(t)
	f.plan(t, "op-plan")
	type out struct {
		res *repairs.ApplyResult
		err error
	}
	done := make(chan out, 1)
	holdMergeLockPastTheLease(t, f.scan(), &f.p.standDownWait, func() {
		go func() {
			res, err := f.applyErr(t, "op-plan", "op-apply", []string{"moved:" + f.ids["parent"]}, nil)
			done <- out{res, err}
		}()
	})
	o := <-done
	require.NoError(t, o.err, "%+v", o.res)
	require.Equal(t, 1, o.res.Applied, "%+v", o.res.Rows)
	require.False(t, f.live(t, "fragF"))
}

// primaryFragG puts the copy row's fragment G in a version group as its
// explicit primary, with an organized sibling, so retiring G hands the
// group's primary to the sibling (handOff).
func (f *fragFixture) primaryFragG(t *testing.T) (sib string) {
	t.Helper()
	group := "vg-fragG"
	yes, no := true, false
	_, err := f.s.ModifyBook(f.ids["fragG"], func(b *database.Book) error {
		b.VersionGroupID = &group
		b.IsPrimaryVersion = &yes
		return nil
	})
	require.NoError(t, err)
	sibPath := f.file(t, "lib/SunsSibling/Suns edition.m4b", 6000)
	sib = f.book(t, "sibling", "Scattered Suns, another edition", sibPath, nil)
	f.row(t, "sib", sib, sibPath, "Suns edition.m4b", 6000, 36000, 0)
	f.organized(t, sib)
	_, err = f.s.ModifyBook(sib, func(b *database.Book) error {
		b.VersionGroupID = &group
		b.IsPrimaryVersion = &no
		return nil
	})
	require.NoError(t, err)
	return sib
}

func handoffJournaled(t *testing.T, s interface {
	GetOperationChanges(string) ([]*database.OperationChange, error)
}, opID, bookID string) bool {
	t.Helper()
	changes, err := s.GetOperationChanges(opID)
	require.NoError(t, err)
	for _, c := range changes {
		if c.BookID == bookID && c.ChangeType == undo.ChangeTypeBookPrimaryHandoff && c.RevertedAt == nil {
			return true
		}
	}
	return false
}

// The lease lapses inside the copy row's last retire, at the primary
// hand-off (the row's last beats). Before the fix handOff logged the refused
// Journal and returned nothing, retire returned nil and the row reported
// applied, with the hand-off journal row missing. Now the row is aborted and
// the next run finishes the hand-off, whether it resumes the same op from
// its checkpoint (a restart) or is a NEW op with the same rows (POST
// /operations/v2/:id/retry of the failed apply re-enqueues its params as a
// new run, with no checkpoint): the owed hand-off is found from the
// fragment's state and this fixer's history, not from the op journal.
func TestFragmentFixer_LeaseLostAtHandOffAbortsAndResumes(t *testing.T) {
	total := handOffProbeRenewals(t)
	// back=1 refuses the hand-off's journal (EnsureSinglePrimary already
	// crowned the sibling: nothing is owed after); back=2 refuses the beat
	// before EnsureSinglePrimary (nothing of the hand-off written: owed).
	for _, back := range []int{1, 2} {
		for _, retry := range []string{"op-apply", "op-retry"} {
			t.Run(fmt.Sprintf("refuse_beat_%d_from_the_end/next_run_%s", back, retry), func(t *testing.T) {
				f, _, rows := leaseLostAtHandOff(t, total-back)
				f.finishAfterLostHandOff(t, retry, back == 2, rows)
			})
		}
	}
}

// Reverting the failed first op and the retry that finished its hand-off,
// in either order, leaves the group with exactly one primary after each
// revert: the hand-off row is a note, and the first op's demote revert
// re-crowns the fragment with Crown, which demotes the sibling the retry
// crowned. Reverting the retry alone leaves the sibling it crowned.
func TestFragmentFixer_RevertFirstOpAndRetryInEitherOrder(t *testing.T) {
	total := handOffProbeRenewals(t)
	for _, order := range [][]string{{"op-apply", "op-retry"}, {"op-retry", "op-apply"}} {
		t.Run(order[0]+"_then_"+order[1], func(t *testing.T) {
			f, sib, rows := leaseLostAtHandOff(t, total-2)
			f.finishAfterLostHandOff(t, "op-retry", true, rows)
			require.Equal(t, []string{sib}, f.livePrimaries(t, "vg-fragG"))
			for _, op := range order {
				_, err := audiobooks.NewRevertService(f.s).RevertOperation(op)
				require.NoError(t, err, "revert %s", op)
				require.Len(t, f.livePrimaries(t, "vg-fragG"), 1, "after reverting %s", op)
			}
			require.True(t, f.live(t, "fragG"), "the first op's revert restores the fragment")
			require.Equal(t, []string{f.ids["fragG"]}, f.livePrimaries(t, "vg-fragG"))
		})
	}
}

// A fragment whose flag someone else changed after this fixer's demote is
// not guessed at: the retry refuses the row.
func TestFragmentFixer_OwedHandOffRefusesAFlagChangedByAnotherWriter(t *testing.T) {
	total := handOffProbeRenewals(t)
	f, _, _ := leaseLostAtHandOff(t, total-2)
	fr := f.ids["fragG"]
	yes := true
	_, err := f.s.ModifyBook(fr, func(b *database.Book) error { b.IsPrimaryVersion = &yes; return nil })
	require.NoError(t, err)
	src := "manual"
	tv, fv := `"true"`, `"false"`
	require.NoError(t, f.s.RecordMetadataChange(&database.MetadataChangeRecord{BookID: fr, Field: "is_primary_version",
		PreviousValue: &fv, NewValue: &tv, ChangeType: "override", Source: src, ChangedAt: time.Now()}))
	res, err := f.applyErr(t, "op-plan", "op-retry", []string{"copy:" + f.ids["suns"]}, nil)
	require.NoError(t, err)
	require.Equal(t, 1, res.ChangedSincePlan, "%+v", res.Rows)
	require.Contains(t, res.Rows[0].Error, "last changed by \"manual\"", "refused by the owed-hand-off check")
	require.Empty(t, f.livePrimaries(t, "vg-fragG"), "nothing crowned on a guess")
}

func handOffProbeRenewals(t *testing.T) int {
	t.Helper()
	probe := newFragFixture(t)
	probe.seed(t)
	probe.primaryFragG(t)
	probe.plan(t, "op-plan")
	out := probe.apply(t, "op-plan", "op-apply", []string{"copy:" + probe.ids["suns"]}, nil)
	require.Equal(t, 1, out.Applied, "%+v", out.Rows)
	require.True(t, handoffJournaled(t, probe.s, "op-apply", probe.ids["fragG"]))
	return probe.scan().renews
}

// livePrimaries lists the group's members ABS counts as primary: Electable
// (live, and not the loser of a live merge survivor) and primary by
// database.EffectiveIsPrimaryVersion, so a nil flag counts.
func (f *fragFixture) livePrimaries(t *testing.T, gid string) []string {
	t.Helper()
	members, err := f.s.GetBooksByVersionGroup(gid)
	require.NoError(t, err)
	alive := func(id string) bool {
		b, err := f.s.GetBookByID(id)
		require.NoError(t, err)
		return b != nil && !b.IsSoftDeleted()
	}
	var out []string
	for i := range members {
		m := &members[i]
		if versionprimary.Electable(m, alive) && database.EffectiveIsPrimaryVersion(m.IsPrimaryVersion) {
			out = append(out, m.ID)
		}
	}
	return out
}

// leaseLostAtHandOff runs the copy row as op-apply with renewals beats
// allowed; the refusal lands in the hand-off, so the op fails and the row is
// aborted with the fragment already retired.
func leaseLostAtHandOff(t *testing.T, renewals int) (*fragFixture, string, []repairs.RowResult) {
	t.Helper()
	f := newFragFixture(t)
	f.seed(t)
	sib := f.primaryFragG(t)
	f.plan(t, "op-plan")
	scan := f.scan()
	scan.mu.Lock()
	scan.renewsLeft = renewals
	scan.mu.Unlock()
	res, err := f.applyErr(t, "op-plan", "op-apply", []string{"copy:" + f.ids["suns"]}, nil)
	require.ErrorIs(t, err, repairs.ErrStandDownLost)
	require.NotNil(t, res)
	require.Len(t, res.Rows, 1)
	require.Equal(t, repairs.OutcomeAborted, res.Rows[0].Outcome,
		"a refused hand-off must not report the row applied: %+v", res.Rows[0])
	require.Contains(t, res.Rows[0].Error, "hand-off")
	require.False(t, f.live(t, "fragG"), "the retire itself was written")
	require.False(t, handoffJournaled(t, f.s, "op-apply", f.ids["fragG"]))
	scan.mu.Lock()
	scan.renewsLeft = -1
	scan.mu.Unlock()
	return f, sib, res.Rows
}

// finishAfterLostHandOff runs the row again: as op-apply from its
// checkpoint (a restart) or as a new op with no checkpoint (a retry). The
// row applies and the group ends with exactly one live primary; owed says
// whether this run had to make (and journal) the hand-off itself.
func (f *fragFixture) finishAfterLostHandOff(t *testing.T, opID string, owed bool, first []repairs.RowResult) {
	t.Helper()
	var resume *repairs.ApplyCheckpoint
	if opID == "op-apply" {
		resume = &repairs.ApplyCheckpoint{Settled: first}
	}
	res, err := f.applyErr(t, "op-plan", opID, []string{"copy:" + f.ids["suns"]}, resume)
	require.NoError(t, err)
	require.Equal(t, 1, res.Applied, "%+v", res.Rows)
	require.Len(t, f.livePrimaries(t, "vg-fragG"), 1, "exactly one primary after the next run")
	require.Equal(t, owed, handoffJournaled(t, f.s, opID, f.ids["fragG"]),
		"the run that made the owed hand-off journals it in its own op")
}

// A create decision whose lease lapses at the first write never mints the
// author: the renewal before MintAuthor (a direct store write) is refused,
// so nothing is created and nothing has to be taken back. Before, MintAuthor
// ran on the lapsed lease, the journal row was refused after it, and the
// row's error was the take-back's "journal created author".
func TestJunkAuthorFixer_LeaseLostBeforeMintCreatesNothing(t *testing.T) {
	f := newJunkFixture(t)
	bk := f.book(junkBookSpec{title: "Assassin's Apprentice", path: "/lib/bk-create", author: "GraphicAudio",
		tags: map[string]string{"artist": "Robin Hobb"}})
	plan := f.plan()
	id := junkAuthorRowID(f.authors["GraphicAudio"], bk)
	require.Equal(t, junkAuthorDecCreate, rowByID(plan, id).Proposed["decision"])
	all, err := f.s.GetAllSeries()
	require.NoError(t, err)
	w := repairs.NewWriter(f.s, f.s, f.fixer.ID(), "bulk_update", "repairs-").WithJournal(f.s, f.s, junkTestOpID).WithCredits(f.s)
	scan := &scriptedScan{renewsLeft: 2} // row start and pre-Apply; the first write's beat is refused
	res, err := repairs.RunApply(context.Background(), f.fixer, plan, "op-junk-plan", []string{id}, false,
		repairs.ApplyDeps{Guard: f.s, Series: repairs.SeriesNamesFrom(all), Writer: w, OpID: junkTestOpID,
			StandDown: scan, Wait: noWait}, &fakeReporter{})
	require.ErrorIs(t, err, repairs.ErrStandDownLost)
	require.Len(t, res.Rows, 1)
	require.Equal(t, repairs.OutcomeAborted, res.Rows[0].Outcome, "%+v", res.Rows[0])
	require.NotContains(t, res.Rows[0].Error, "journal created author", "refused before the mint, not after")
	got, err := f.s.GetAuthorByName("Robin Hobb")
	require.NoError(t, err)
	require.Nil(t, got)
	require.Equal(t, []int{f.authors["GraphicAudio"]}, f.credits(bk))
}

// The lease is refused at the renewal before FollowAbsorbedJournaled, whose
// pending-repair record and progress moves are direct store writes that run
// before any journaled record (and a fragment nobody listened to journals
// none). The row stops there, before anything of the retire is written.
// Beats before it: row start, pre-Apply, and LockWaiting's two (before
// waiting, after acquiring).
func TestFragmentFixer_LeaseLostBeforeTheUserStateFollow(t *testing.T) {
	f := newFragFixture(t)
	f.seed(t)
	f.plan(t, "op-plan")
	scan := f.scan()
	scan.mu.Lock()
	scan.renewsLeft = 4
	scan.mu.Unlock()
	res, err := f.applyErr(t, "op-plan", "op-apply", []string{"copy:" + f.ids["suns"]}, nil)
	require.ErrorIs(t, err, repairs.ErrStandDownLost)
	require.Len(t, res.Rows, 1)
	require.Equal(t, repairs.OutcomeAborted, res.Rows[0].Outcome, "%+v", res.Rows[0])
	require.Contains(t, res.Rows[0].Error, "user state of "+f.ids["fragG"], "refused before the follow, not at a later write")
	require.True(t, f.live(t, "fragG"))
	require.Zero(t, res.JournalRows)
}

// The folder-books claim loop re-indexes each created row with a direct
// ModifyBookFile; the lease lapsing just before it refuses the claim there
// rather than at some later Writer write.
func TestFolderBooksFixer_LeaseLostAtThePathKeyClaim(t *testing.T) {
	f := newFragFixture(t)
	f.seedWolfe(t, fbITunes, "citadel")
	row := f.fbSingleRow(t, "op-plan")
	scan := f.scan()
	fbCrashHook = func(stage string, n int) error {
		if stage == "claim" && n == 1 {
			scan.mu.Lock()
			scan.renewsLeft = 0
			scan.mu.Unlock()
		}
		return nil
	}
	t.Cleanup(func() { fbCrashHook = nil })
	no := false
	params, err := json.Marshal(repairs.ApplyParams{FixerID: fbFixerID, PlanOpID: "op-plan", RowIDs: []string{row.RowID}, DryRun: &no})
	require.NoError(t, err)
	rep := &repairsOpReporter{id: "op-apply"}
	runErr := f.p.runRepairsApply(context.Background(), params, rep)
	require.ErrorIs(t, runErr, repairs.ErrStandDownLost)
	res, ok := rep.result.(*repairs.ApplyResult)
	require.True(t, ok)
	require.Equal(t, repairs.OutcomeAborted, res.Rows[0].Outcome, "%+v", res.Rows[0])
	require.Contains(t, res.Rows[0].Error, "path key of row", "refused at the claim, not at a later write")
	require.True(t, f.live(t, "fb"), "the folder-book is not retired")
}

// The duplicate-copies fixer waits for merge.LockMergeRMW like the fragment
// fixer does: an outside holder keeps it past the lease, and the wait renews
// the lease so the row applies once the lock is free. Before the fix its
// Apply blocked in LockMergeRMW with no renewal and the row's first write was
// refused on the lapsed lease.
//
// Not parallel: it holds the process-wide merge lock.
func TestDuplicateCopies_MergeLockWaitKeepsTheLease(t *testing.T) {
	d := newDCFixture(t)
	s, l := d.dune(t)
	d.planFor(t, dcFixerID, "op-plan", nil)
	type out struct {
		res *repairs.ApplyResult
		err error
	}
	done := make(chan out, 1)
	apply := func() {
		no := false
		params, err := json.Marshal(repairs.ApplyParams{FixerID: dcFixerID, PlanOpID: "op-plan", RowIDs: []string{dupRowID(s, l)}, DryRun: &no})
		if err != nil {
			done <- out{nil, err}
			return
		}
		rep := &repairsOpReporter{id: "op-apply"}
		runErr := d.p.runRepairsApply(context.Background(), params, rep)
		res, _ := rep.result.(*repairs.ApplyResult)
		done <- out{res, runErr}
	}
	holdMergeLockPastTheLease(t, d.scan(), &d.p.standDownWait, func() { go apply() })
	o := <-done
	require.NoError(t, o.err, "%+v", o.res)
	require.Equal(t, 1, o.res.Applied, "%+v", o.res.Rows)
	require.False(t, d.live(t, "L"))
}

// A duplicate-copies loser is demoted by Crown inside the hand-off, journaled
// as a demote and then a hand-off note, with no Writer history row (Crown
// writes the store directly). A retry that meets the loser already retired
// must see the newer hand-off note and owe nothing. Before the shared
// resumeHandOff checked the hand-off note first, it demanded a history row
// from the fixer and refused the row as changed since plan.
func TestRetireInto_RetiredLoserHandedOffByCrownOwesNothing(t *testing.T) {
	d := newDCFixture(t)
	s, l := d.dune(t)
	gid := "vg-dune"
	yes := true
	_, err := d.s.ModifyBook(s, func(b *database.Book) error { b.VersionGroupID = &gid; return nil })
	require.NoError(t, err)
	_, err = d.s.ModifyBook(l, func(b *database.Book) error { b.VersionGroupID, b.IsPrimaryVersion = &gid, &yes; return nil })
	require.NoError(t, err)

	// The cut-off run: demote journaled, Crown, hand-off noted, loser retired.
	w := repairs.NewWriter(d.s, d.s, dcFixerID, "bulk_update", "repairs-").WithJournal(d.s, d.s, "op-cut")
	require.NoError(t, w.Journal(l, undo.ChangeTypeBookPrimaryDemote, "is_primary_version", "true", "false"))
	cr, err := versionprimary.Crown(fragEnsureStore{OpsStore: d.s, chapters: d.s}, gid, s)
	require.NoError(t, err)
	require.Equal(t, s, cr.PrimaryID)
	require.NoError(t, w.Journal(l, undo.ChangeTypeBookPrimaryHandoff, "version_group_id", "", gid))
	now := time.Now().UTC()
	_, err = d.s.ModifyBook(l, func(b *database.Book) error {
		b.MarkedForDeletion, b.MarkedForDeletionAt, b.MergedIntoBookID = &yes, &now, &s
		return nil
	})
	require.NoError(t, err)

	retry := repairs.NewWriter(d.s, d.s, dcFixerID, "bulk_update", "repairs-").WithJournal(d.s, d.s, "op-retry")
	n, err := retireInto(context.Background(), d.p, d.s, retry, time.Now, dcFixerID, l, s, nil)
	require.NoError(t, err)
	require.Zero(t, n)
	require.Zero(t, retry.Journaled(), "nothing owed, nothing journaled")
}

// The folder-books create credits the group's author with a direct
// SetBookAuthors, which on a resumed apply has no journal row in front of
// it. The lease lapsing just after the book is created refuses the row at
// the credit, before it is written, not at the next book_file journal row.
func TestFolderBooksFixer_LeaseLostBeforeTheAuthorCredit(t *testing.T) {
	f := newFragFixture(t)
	f.seedWolfe(t, fbITunes, "citadel")
	row := f.fbSingleRow(t, "op-plan")
	scan := f.scan()
	fbCrashHook = func(stage string, n int) error {
		if stage == "book" {
			scan.mu.Lock()
			scan.renewsLeft = 0
			scan.mu.Unlock()
		}
		return nil
	}
	t.Cleanup(func() { fbCrashHook = nil })
	no := false
	params, err := json.Marshal(repairs.ApplyParams{FixerID: fbFixerID, PlanOpID: "op-plan", RowIDs: []string{row.RowID}, DryRun: &no})
	require.NoError(t, err)
	rep := &repairsOpReporter{id: "op-apply"}
	runErr := f.p.runRepairsApply(context.Background(), params, rep)
	require.ErrorIs(t, runErr, repairs.ErrStandDownLost)
	res, ok := rep.result.(*repairs.ApplyResult)
	require.True(t, ok)
	require.Equal(t, repairs.OutcomeAborted, res.Rows[0].Outcome, "%+v", res.Rows[0])
	require.Contains(t, res.Rows[0].Error, "author credit of", "refused at the credit, not at a later write")
}

// The lease lapses at every beat of the copy row in turn, and the row is then
// run again, as a resume of op-apply from its checkpoint or as a new op (a
// retry). The group must end with exactly one live primary every time. Before
// the fix the retire read wasPrimary from the CURRENT flag: a first run that
// demoted fragment G (step 3) and lost the lease before its soft-delete left
// G explicit false, the next run skipped the hand-off, soft-deleted G and
// reported applied with no live primary in the group.
func TestFragmentFixer_LeaseLostAtEveryBeatLeavesOnePrimary(t *testing.T) {
	total := handOffProbeRenewals(t)
	for k := 0; k < total; k++ {
		for _, retry := range []string{"op-apply", "op-retry"} {
			t.Run(fmt.Sprintf("k%d_%s", k, retry), func(t *testing.T) {
				f := newFragFixture(t)
				f.seed(t)
				f.primaryFragG(t)
				f.plan(t, "op-plan")
				scan := f.scan()
				scan.mu.Lock()
				scan.renewsLeft = k
				scan.mu.Unlock()
				res, _ := f.applyErr(t, "op-plan", "op-apply", []string{"copy:" + f.ids["suns"]}, nil)
				scan.mu.Lock()
				scan.renewsLeft = -1
				scan.mu.Unlock()
				var resume *repairs.ApplyCheckpoint
				if retry == "op-apply" && res != nil {
					resume = &repairs.ApplyCheckpoint{Settled: res.Rows}
				}
				_, err := f.applyErr(t, "op-plan", retry, []string{"copy:" + f.ids["suns"]}, resume)
				require.NoError(t, err)
				require.Len(t, f.livePrimaries(t, "vg-fragG"), 1)
			})
		}
	}
}

// A duplicate-copies loser demoted by Crown whose hand-off note was cut off
// (the lease lost after Crown, before the note): the demote is journaled,
// no hand-off row and no Writer history row exist, but the group has its
// one live primary, the heir Crown made. A retry owes nothing. Before the
// fix resumeHandOff validated history before counting primaries and refused
// the row as changed since plan on every retry.
func TestRetireInto_RetiredLoserCrownedWithoutANoteOwesNothing(t *testing.T) {
	d := newDCFixture(t)
	s, l := d.dune(t)
	gid := "vg-dune"
	yes := true
	_, err := d.s.ModifyBook(s, func(b *database.Book) error { b.VersionGroupID = &gid; return nil })
	require.NoError(t, err)
	_, err = d.s.ModifyBook(l, func(b *database.Book) error { b.VersionGroupID, b.IsPrimaryVersion = &gid, &yes; return nil })
	require.NoError(t, err)

	w := repairs.NewWriter(d.s, d.s, dcFixerID, "bulk_update", "repairs-").WithJournal(d.s, d.s, "op-cut")
	require.NoError(t, w.Journal(l, undo.ChangeTypeBookPrimaryDemote, "is_primary_version", "true", "false"))
	cr, err := versionprimary.Crown(fragEnsureStore{OpsStore: d.s, chapters: d.s}, gid, s)
	require.NoError(t, err)
	require.Equal(t, s, cr.PrimaryID)
	now := time.Now().UTC()
	_, err = d.s.ModifyBook(l, func(b *database.Book) error {
		b.MarkedForDeletion, b.MarkedForDeletionAt, b.MergedIntoBookID = &yes, &now, &s
		return nil
	})
	require.NoError(t, err)

	retry := repairs.NewWriter(d.s, d.s, dcFixerID, "bulk_update", "repairs-").WithJournal(d.s, d.s, "op-retry")
	n, err := retireInto(context.Background(), d.p, d.s, retry, time.Now, dcFixerID, l, s, nil)
	require.NoError(t, err)
	require.Zero(t, n)
	require.Zero(t, retry.Journaled(), "nothing owed, nothing journaled")
}
