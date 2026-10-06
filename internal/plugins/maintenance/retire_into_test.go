// file: internal/plugins/maintenance/retire_into_test.go
// version: 1.9.0
// guid: 90cd2c0f-e6c5-4176-8d2c-bc587eea86cd
// last-edited: 2026-10-06

package maintenance

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary"
)

// journalCountingStore is the ops store with GetBookChanges (the unindexed
// opchange scan) counted.
type journalCountingStore struct {
	OpsStore
	inner bookJournalReader
	scans int
}

func (s *journalCountingStore) GetBookChanges(bookID string) ([]*database.OperationChange, error) {
	s.scans++
	return s.inner.GetBookChanges(bookID)
}

// A version group whose primary carries a nil flag (read as primary by every
// visibility path) has its one primary. Retiring a non-primary copy of it is
// owed no hand-off and must not scan the operation journal for evidence.
// Before the fix the live count took only an explicit true, read the group
// as having none, and sent this first-run retire through GetBookChanges.
func TestRetireInto_NilFlagPrimarySkipsTheJournalScan(t *testing.T) {
	d := newDCFixture(t)
	s, l := d.dune(t)
	gid := "vg-dune"
	no := false
	_, err := d.s.ModifyBook(s, func(b *database.Book) error { b.VersionGroupID, b.IsPrimaryVersion = &gid, nil; return nil })
	require.NoError(t, err)
	_, err = d.s.ModifyBook(l, func(b *database.Book) error { b.VersionGroupID, b.IsPrimaryVersion = &gid, &no; return nil })
	require.NoError(t, err)

	cs := &journalCountingStore{OpsStore: d.s, inner: d.s}
	w := repairs.NewWriter(d.s, d.s, dcFixerID, "bulk_update", "repairs-").WithJournal(d.s, d.s, "op-apply")
	_, err = retireInto(context.Background(), d.p, cs, w, time.Now, dcFixerID, l, s, nil)
	require.NoError(t, err)
	require.False(t, d.live(t, "L"))
	require.Zero(t, cs.scans, "a group with its one (nil-flag) primary owes nothing: no journal scan")
	require.False(t, handoffJournaled(t, d.s, "op-apply", l), "no hand-off made")
	sb, err := d.s.GetBookByID(s)
	require.NoError(t, err)
	require.Nil(t, sb.IsPrimaryVersion, "the nil-flag primary is left as it was")
}

// One retire fixer (fragment consolidation) demotes book L and loses its
// lease before the soft-delete; another (duplicate copies) then retires L.
// The demote is a retire's demote, so the second fixer finishes the owed
// hand-off. Before, it refused because the history Source was the other
// fixer's, on every run, and the group stayed with no primary.
func TestRetireInto_FinishesAnotherRetireFixersOwedHandOff(t *testing.T) {
	d := newDCFixture(t)
	s, l := d.dune(t)
	gid := "vg-dune"
	yes, no := true, false
	_, err := d.s.ModifyBook(s, func(b *database.Book) error { b.VersionGroupID, b.IsPrimaryVersion = &gid, &no; return nil })
	require.NoError(t, err)
	_, err = d.s.ModifyBook(l, func(b *database.Book) error { b.VersionGroupID, b.IsPrimaryVersion = &gid, &yes; return nil })
	require.NoError(t, err)

	// Fixer A's cut-off retire: the demote journaled and written, then nothing.
	a := repairs.NewWriter(d.s, d.s, fragFixerID, "bulk_update", "repairs-").WithJournal(d.s, d.s, "op-a")
	require.NoError(t, a.Step(l, undo.ChangeTypeBookPrimaryDemote, "is_primary_version", "true", "false", func() error {
		_, err := a.Modify(l, func(cur *database.Book) error { cur.IsPrimaryVersion = &no; return nil })
		return err
	}))
	require.Empty(t, d.livePrimaries(t, gid))

	b := repairs.NewWriter(d.s, d.s, dcFixerID, "bulk_update", "repairs-").WithJournal(d.s, d.s, "op-b")
	_, err = retireInto(context.Background(), d.p, d.s, b, time.Now, dcFixerID, l, s, nil)
	require.NoError(t, err)
	require.False(t, d.live(t, "L"))
	require.Equal(t, []string{s}, d.livePrimaries(t, gid))
	require.True(t, handoffJournaled(t, d.s, "op-b", l))
}

// demoteOnly reports whether op opID's journal holds a primary demote of
// bookID and no soft-delete of it: a retire cut off between the two.
func demoteOnly(t *testing.T, s interface {
	GetOperationChanges(string) ([]*database.OperationChange, error)
}, opID, bookID string) bool {
	t.Helper()
	changes, err := s.GetOperationChanges(opID)
	require.NoError(t, err)
	demoted, deleted := false, false
	for _, c := range changes {
		if c.BookID != bookID {
			continue
		}
		switch c.ChangeType {
		case undo.ChangeTypeBookPrimaryDemote:
			demoted = true
		case undo.ChangeTypeBookSoftDelete:
			deleted = true
		}
	}
	return demoted && !deleted
}

// revertBoth reverts the two ops in order and requires exactly one live
// primary in gid after each revert.
func revertBoth(t *testing.T, s *database.PebbleStore, gid string, live func() []string, order ...string) {
	t.Helper()
	for _, op := range order {
		_, err := audiobooks.NewRevertService(s).RevertOperation(op)
		require.NoError(t, err, "revert %s", op)
		require.Len(t, live(), 1, "after reverting %s", op)
	}
}

// The first run's retire demotes fragment G and loses its lease before the
// soft-delete, so op-apply journals the demote and nothing after it; the
// retry (a new op) soft-deletes G and makes the owed hand-off, crowning the
// sibling. Reverting the two ops in EITHER order leaves exactly one live
// primary. Before the fix, reverting op-apply first wrote true on the
// retired G (Crown refused it, the revert still counted it restored), and
// reverting op-retry then brought G back with that true next to the
// sibling: two primaries.
func TestFragmentFixer_DemoteOnlyFirstOpRevertsToOnePrimaryInEitherOrder(t *testing.T) {
	t.Parallel()
	total := handOffProbeRenewals(t)
	ran := 0
	for k := 0; k < total; k++ {
		for _, order := range [][]string{{"op-apply", "op-retry"}, {"op-retry", "op-apply"}} {
			f := newFragFixture(t)
			f.seed(t)
			f.primaryFragG(t)
			f.plan(t, "op-plan")
			scan := f.scan()
			scan.mu.Lock()
			scan.renewsLeft = k
			scan.mu.Unlock()
			_, _ = f.applyErr(t, "op-plan", "op-apply", []string{"copy:" + f.ids["suns"]}, nil)
			scan.mu.Lock()
			scan.renewsLeft = -1
			scan.mu.Unlock()
			if !demoteOnly(t, f.s, "op-apply", f.ids["fragG"]) {
				continue // the cut landed elsewhere
			}
			t.Run(fmt.Sprintf("k%d_%s_first", k, order[0]), func(t *testing.T) {
				ran++
				_, err := f.applyErr(t, "op-plan", "op-retry", []string{"copy:" + f.ids["suns"]}, nil)
				require.NoError(t, err)
				require.Len(t, f.livePrimaries(t, "vg-fragG"), 1)
				revertBoth(t, f.s, "vg-fragG", func() []string { return f.livePrimaries(t, "vg-fragG") }, order...)
			})
		}
	}
	require.Positive(t, ran, "some cut must land between the demote and the soft-delete")
}

// The same, across fixers: fragment consolidation's retire demotes L and
// loses its lease (op-a), duplicate copies then retires L and finishes the
// owed hand-off (op-b). Either revert order ends with one live primary.
func TestRetireInto_CrossFixerDemoteOnlyRevertsToOnePrimaryInEitherOrder(t *testing.T) {
	for _, order := range [][]string{{"op-a", "op-b"}, {"op-b", "op-a"}} {
		t.Run(order[0]+"_first", func(t *testing.T) {
			d := newDCFixture(t)
			s, l := d.dune(t)
			gid := "vg-dune"
			yes, no := true, false
			_, err := d.s.ModifyBook(s, func(b *database.Book) error { b.VersionGroupID, b.IsPrimaryVersion = &gid, &no; return nil })
			require.NoError(t, err)
			_, err = d.s.ModifyBook(l, func(b *database.Book) error { b.VersionGroupID, b.IsPrimaryVersion = &gid, &yes; return nil })
			require.NoError(t, err)
			a := repairs.NewWriter(d.s, d.s, fragFixerID, "bulk_update", "repairs-").WithJournal(d.s, d.s, "op-a")
			require.NoError(t, a.Step(l, undo.ChangeTypeBookPrimaryDemote, "is_primary_version", "true", "false", func() error {
				_, err := a.Modify(l, func(cur *database.Book) error { cur.IsPrimaryVersion = &no; return nil })
				return err
			}))
			b := repairs.NewWriter(d.s, d.s, dcFixerID, "bulk_update", "repairs-").WithJournal(d.s, d.s, "op-b")
			_, err = retireInto(context.Background(), d.p, d.s, b, time.Now, dcFixerID, l, s, nil)
			require.NoError(t, err)
			require.Equal(t, []string{s}, d.livePrimaries(t, gid))
			revertBoth(t, d.s, gid, func() []string { return d.livePrimaries(t, gid) }, order...)
			// Both ops undone: the group is back as it was before either,
			// L its primary, in either order (op-b's revert crowns the book
			// whose hand-off it reverted).
			require.Equal(t, []string{l}, d.livePrimaries(t, gid))
			require.True(t, d.live(t, "L"), "both reverts bring the copy back")
		})
	}
}

// crossFixerDemoteOnly seeds the cross-fixer case: S explicit false, L
// explicit true in one group; op-a (fragment consolidation) demotes L and
// stops; op-b (duplicate copies) retires L and hands the group to S.
func crossFixerDemoteOnly(t *testing.T) (d *dcFixture, s, l, gid string) {
	t.Helper()
	d = newDCFixture(t)
	s, l = d.dune(t)
	gid = "vg-dune"
	yes, no := true, false
	_, err := d.s.ModifyBook(s, func(b *database.Book) error { b.VersionGroupID, b.IsPrimaryVersion = &gid, &no; return nil })
	require.NoError(t, err)
	_, err = d.s.ModifyBook(l, func(b *database.Book) error { b.VersionGroupID, b.IsPrimaryVersion = &gid, &yes; return nil })
	require.NoError(t, err)
	a := repairs.NewWriter(d.s, d.s, fragFixerID, "bulk_update", "repairs-").WithJournal(d.s, d.s, "op-a")
	require.NoError(t, a.Step(l, undo.ChangeTypeBookPrimaryDemote, "is_primary_version", "true", "false", func() error {
		_, err := a.Modify(l, func(cur *database.Book) error { cur.IsPrimaryVersion = &no; return nil })
		return err
	}))
	b := repairs.NewWriter(d.s, d.s, dcFixerID, "bulk_update", "repairs-").WithJournal(d.s, d.s, "op-b")
	_, err = retireInto(context.Background(), d.p, d.s, b, time.Now, dcFixerID, l, s, nil)
	require.NoError(t, err)
	require.Equal(t, []string{s}, d.livePrimaries(t, gid))
	return d, s, l, gid
}

// Revert op-a while L is still retired, then S goes to the trash, then
// revert op-b. L comes back with no live incumbent to yield to and must end
// the group's one primary. Before the fix op-a's demote revert was
// superseded on the retired L (its flag left false) and L came back false
// next to a trashed S: no live primary at all.
func TestRetireInto_RevertWithTheIncumbentGoneLeavesOnePrimary(t *testing.T) {
	// Not parallel: crossFixerDemoteOnly builds a dcFixture, which swaps the
	// global library root.
	d, s, l, gid := crossFixerDemoteOnly(t)
	_, err := audiobooks.NewRevertService(d.s).RevertOperation("op-a")
	require.NoError(t, err)
	yes := true
	now := time.Now().UTC()
	_, err = d.s.ModifyBook(s, func(b *database.Book) error { b.MarkedForDeletion, b.MarkedForDeletionAt = &yes, &now; return nil })
	require.NoError(t, err)
	_, err = audiobooks.NewRevertService(d.s).RevertOperation("op-b")
	require.NoError(t, err)
	require.Equal(t, []string{l}, d.livePrimaries(t, gid))
}

// groupReadFailsOnce fails one version-group read of group: the nth (1 when
// zero), by default the soft-delete revert's incumbent read.
type groupReadFailsOnce struct {
	*database.PebbleStore
	group string
	nth   int32
	armed atomic.Bool
	reads atomic.Int32
}

func (g *groupReadFailsOnce) GetBooksByVersionGroup(groupID string) ([]database.Book, error) {
	if groupID == g.group {
		n := g.nth
		if n == 0 {
			n = 1
		}
		if g.reads.Add(1) == n && g.armed.CompareAndSwap(true, false) {
			return nil, errors.New("version group read failed")
		}
	}
	return g.PebbleStore.GetBooksByVersionGroup(groupID)
}

// A duplicate-copies retire of primary L journals no book_file move, so the
// revert plan had no file dependency to gate L's rows on. A soft-delete
// revert that fails once (a transient group read) must refuse L's other
// rows in that pass; the second pass then restores L whole and its demote
// revert re-crowns it. Before the fix the first pass restored L's
// merged-into and path onto the still-retired book and reverted the demote
// against it, and the second pass returned no error with L live but
// non-primary and S still primary.
func TestRetireInto_FailedSoftDeleteRevertRefusesTheBooksOtherRows(t *testing.T) {
	d := newDCFixture(t)
	s, l := d.dune(t)
	gid := "vg-dune"
	yes, no := true, false
	_, err := d.s.ModifyBook(s, func(b *database.Book) error { b.VersionGroupID, b.IsPrimaryVersion = &gid, &no; return nil })
	require.NoError(t, err)
	_, err = d.s.ModifyBook(l, func(b *database.Book) error { b.VersionGroupID, b.IsPrimaryVersion = &gid, &yes; return nil })
	require.NoError(t, err)
	w := repairs.NewWriter(d.s, d.s, dcFixerID, "bulk_update", "repairs-").WithJournal(d.s, d.s, "op-b")
	_, err = retireInto(context.Background(), d.p, d.s, w, time.Now, dcFixerID, l, s, nil)
	require.NoError(t, err)
	require.Equal(t, []string{s}, d.livePrimaries(t, gid))

	failing := &groupReadFailsOnce{PebbleStore: d.s, group: gid}
	failing.armed.Store(true)
	_, err = audiobooks.NewRevertService(failing).RevertOperation("op-b")
	require.Error(t, err, "the soft-delete revert fails on the group read")
	require.False(t, failing.armed.Load())
	require.False(t, d.live(t, "L"))

	_, err = audiobooks.NewRevertService(d.s).RevertOperation("op-b")
	require.NoError(t, err)
	require.True(t, d.live(t, "L"))
	require.Equal(t, []string{l}, d.livePrimaries(t, gid))
}

// retiredInOp soft-deletes book id with a stamp journaled in op (as a
// retire's last step), merged into mergedInto when it is not "".
func retiredInOp(t *testing.T, d *dcFixture, op, id, mergedInto string) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Microsecond)
	w := repairs.NewWriter(d.s, d.s, dcFixerID, "bulk_update", "repairs-").WithJournal(d.s, d.s, op)
	require.NoError(t, w.Journal(id, undo.ChangeTypeBookSoftDelete, "marked_for_deletion", "", undo.SoftDeleteStamp(now)))
	yes := true
	_, err := d.s.ModifyBook(id, func(b *database.Book) error {
		b.MarkedForDeletion, b.MarkedForDeletionAt = &yes, &now
		if mergedInto != "" {
			b.MergedIntoBookID = &mergedInto
		}
		return nil
	})
	require.NoError(t, err)
}

// groupOf puts the books in group gid with the given flags.
func groupOf(t *testing.T, d *dcFixture, gid string, flags map[string]*bool) {
	t.Helper()
	for id, v := range flags {
		_, err := d.s.ModifyBook(id, func(b *database.Book) error { b.VersionGroupID, b.IsPrimaryVersion = &gid, v; return nil })
		require.NoError(t, err)
	}
}

// A retired book comes back with a stale explicit false, its group has no
// live primary (the other member is in the trash) and no demote row of the
// op follows to crown it. The soft-delete revert must hand the group off
// itself (EnsureSinglePrimary), as a trash restore does, or the group is
// left with no primary at all.
func TestRevert_SoftDeleteWithNoIncumbentHandsTheGroupOff(t *testing.T) {
	d := newDCFixture(t)
	s, l := d.dune(t)
	no := false
	groupOf(t, d, "vg-dune", map[string]*bool{s: &no, l: &no})
	retiredInOp(t, d, "op-old", s, "")
	retiredInOp(t, d, "op-x", l, "")
	res, err := audiobooks.NewRevertService(d.s).RevertOperation("op-x")
	require.NoError(t, err)
	require.Empty(t, res.HandOffFailed)
	require.Equal(t, []string{l}, d.livePrimaries(t, "vg-dune"))
}

// D: a soft-delete-only op (no demote, no hand-off) whose group settle
// fails: the failure is reported (HandOffFailed, Partial) with the row
// itself restored and marked, the settle is recorded as owed, and the next
// revert of the op retries it although every row is already reverted.
func TestRevert_FailedHandOffIsReported(t *testing.T) {
	d := newDCFixture(t)
	s, l := d.dune(t)
	no := false
	groupOf(t, d, "vg-dune", map[string]*bool{s: &no, l: &no})
	retiredInOp(t, d, "op-old", s, "")
	retiredInOp(t, d, "op-x", l, "")
	failing := &groupReadFailsOnce{PebbleStore: d.s, group: "vg-dune", nth: 2} // 1: incumbent, 2: settle
	failing.armed.Store(true)
	res, err := audiobooks.NewRevertService(failing).RevertOperation("op-x")
	require.Error(t, err)
	require.False(t, failing.armed.Load(), "the settle's group read failed")
	require.Equal(t, 1, res.Restored, "the row is restored and marked")
	require.True(t, res.Partial())
	require.Len(t, res.HandOffFailed, 1, "%+v", res)
	require.Contains(t, res.HandOffFailed[0], "vg-dune")
	require.Contains(t, res.HandOffFailed[0], "left to retry")
	require.Contains(t, res.Summary(), "not settled to one primary")
	require.True(t, d.live(t, "L"))
	require.Empty(t, d.livePrimaries(t, "vg-dune"))

	res, err = audiobooks.NewRevertService(d.s).RevertOperation("op-x")
	require.NoError(t, err)
	require.Empty(t, res.HandOffFailed)
	require.Equal(t, []string{l}, d.livePrimaries(t, "vg-dune"), "the re-run retried the owed settle")
	_, err = audiobooks.NewRevertService(d.s).RevertOperation("op-x")
	require.Error(t, err, "nothing is owed any more: the op is fully reverted")
}

// A row of a retired book journaled AFTER its soft-delete (fs-regroup-xml
// moves external ids off the shell after retiring it; a title here) must
// wait for the soft-delete revert and be refused when it fails. Before, the
// plan reverted it first (newest first), and it landed on a book that
// stayed retired.
func TestRevert_RowsAfterAFailedSoftDeleteAreRefused(t *testing.T) {
	d := newDCFixture(t)
	s, l := d.dune(t)
	no, yes := false, true
	groupOf(t, d, "vg-dune", map[string]*bool{s: &no, l: &yes})
	w := repairs.NewWriter(d.s, d.s, dcFixerID, "bulk_update", "repairs-").WithJournal(d.s, d.s, "op-b")
	_, err := retireInto(context.Background(), d.p, d.s, w, time.Now, dcFixerID, l, s, nil)
	require.NoError(t, err)
	lb, err := d.s.GetBookByID(l)
	require.NoError(t, err)
	was := lb.Title
	require.NoError(t, w.Step(l, "metadata_update", "title", was, "Retired shell", func() error {
		_, err := d.s.ModifyBook(l, func(b *database.Book) error { b.Title = "Retired shell"; return nil })
		return err
	}))

	failing := &groupReadFailsOnce{PebbleStore: d.s, group: "vg-dune"}
	failing.armed.Store(true)
	_, err = audiobooks.NewRevertService(failing).RevertOperation("op-b")
	require.Error(t, err)
	require.False(t, d.live(t, "L"))
	lb, err = d.s.GetBookByID(l)
	require.NoError(t, err)
	require.Equal(t, "Retired shell", lb.Title, "the later row waits for the book's soft-delete revert")

	_, err = audiobooks.NewRevertService(d.s).RevertOperation("op-b")
	require.NoError(t, err)
	lb, err = d.s.GetBookByID(l)
	require.NoError(t, err)
	require.Equal(t, was, lb.Title)
	require.Equal(t, []string{l}, d.livePrimaries(t, "vg-dune"))
}

// fs-regroup-xml's nil-shell shape: the shell L had no flag (primary by
// every visibility path), the retire journaled its demote as OldValue "",
// soft-deleted it and handed the group to S, noting the hand-off. Reverting
// the op must bring L back as the group's primary, as for an explicit true.
// Before, the soft-delete revert yielded L to S, the demote revert wrote nil
// and the hand-off kept S and wrote L false: one primary, the wrong one,
// with the row reported restored.
func TestRevert_NilDemoteRestoreLeavesOnePrimary(t *testing.T) {
	d := newDCFixture(t)
	s, l := d.dune(t)
	no, yes := false, true
	groupOf(t, d, "vg-dune", map[string]*bool{s: &no, l: nil})
	w := repairs.NewWriter(d.s, d.s, dcFixerID, "bulk_update", "repairs-").WithJournal(d.s, d.s, "op-y")
	require.NoError(t, w.Step(l, undo.ChangeTypeBookPrimaryDemote, "is_primary_version", "", "false", func() error {
		_, err := d.s.ModifyBook(l, func(b *database.Book) error { b.IsPrimaryVersion = &no; return nil })
		return err
	}))
	now := time.Now().UTC().Truncate(time.Microsecond)
	require.NoError(t, w.Step(l, undo.ChangeTypeBookSoftDelete, "marked_for_deletion", "", undo.SoftDeleteStamp(now), func() error {
		_, err := d.s.ModifyBook(l, func(b *database.Book) error { b.MarkedForDeletion, b.MarkedForDeletionAt = &yes, &now; return nil })
		return err
	}))
	_, err := versionprimary.Crown(fragEnsureStore{OpsStore: d.s, chapters: d.s}, "vg-dune", s)
	require.NoError(t, err)
	require.NoError(t, w.Journal(l, undo.ChangeTypeBookPrimaryHandoff, "version_group_id", "", "vg-dune"))
	require.Equal(t, []string{s}, d.livePrimaries(t, "vg-dune"))

	_, err = audiobooks.NewRevertService(d.s).RevertOperation("op-y")
	require.NoError(t, err)
	require.Equal(t, []string{l}, d.livePrimaries(t, "vg-dune"))
}

// A retired book merged into a survivor OUTSIDE its group, the group's only
// member, comes back in two steps: the soft-delete revert (first, while it
// still points at the survivor, so it is not Electable and no hand-off can
// pick it) and the merged-into revert. The hand-off after the merged-into
// revert makes it the group's primary. Before, nothing ran after that
// revert and the group was left with no primary.
func TestRevert_MergedIntoRevertHandsTheGroupOff(t *testing.T) {
	d := newDCFixture(t)
	s, l := d.dune(t)
	no := false
	groupOf(t, d, "vg-l", map[string]*bool{l: &no})
	w := repairs.NewWriter(d.s, d.s, dcFixerID, "bulk_update", "repairs-").WithJournal(d.s, d.s, "op-m")
	require.NoError(t, w.Journal(l, undo.ChangeTypeBookMergedInto, "merged_into_book_id", "", s))
	retiredInOp(t, d, "op-m", l, s)
	_, err := audiobooks.NewRevertService(d.s).RevertOperation("op-m")
	require.NoError(t, err)
	lb, err := d.s.GetBookByID(l)
	require.NoError(t, err)
	require.Nil(t, lb.MergedIntoBookID)
	require.Equal(t, []string{l}, d.livePrimaries(t, "vg-l"))
}

// A retired book carrying a true (written by a demote revert while it was
// in the trash) comes back still merged into a live survivor that is not in
// its group, so there is no incumbent to yield to and Crown never reaches
// it. It must come back explicit false: ABS does not read MergedIntoBookID
// and would list it as a primary next to the survivor.
func TestRevert_RestoredMergeLoserComesBackNonPrimary(t *testing.T) {
	d := newDCFixture(t)
	s, l := d.dune(t)
	yes := true
	groupOf(t, d, "vg-l", map[string]*bool{l: &yes})
	retiredInOp(t, d, "op-x", l, s)
	_, err := audiobooks.NewRevertService(d.s).RevertOperation("op-x")
	require.NoError(t, err)
	lb, err := d.s.GetBookByID(l)
	require.NoError(t, err)
	require.False(t, lb.IsSoftDeleted())
	require.NotNil(t, lb.IsPrimaryVersion)
	require.False(t, *lb.IsPrimaryVersion, "a live merge loser is never a primary")
}

// nilShellRetired journals fs-regroup-xml's retire of a never-set (nil, so
// primary) shell L in op: the demote as OldValue "", then the soft-delete.
// The caller decides what happened to the rest of the group.
func nilShellRetired(t *testing.T, d *dcFixture, op, l string) *repairs.Writer {
	t.Helper()
	no, yes := false, true
	w := repairs.NewWriter(d.s, d.s, dcFixerID, "bulk_update", "repairs-").WithJournal(d.s, d.s, op)
	require.NoError(t, w.Step(l, undo.ChangeTypeBookPrimaryDemote, "is_primary_version", "", "false", func() error {
		_, err := d.s.ModifyBook(l, func(b *database.Book) error { b.IsPrimaryVersion = &no; return nil })
		return err
	}))
	now := time.Now().UTC().Truncate(time.Microsecond)
	require.NoError(t, w.Step(l, undo.ChangeTypeBookSoftDelete, "marked_for_deletion", "", undo.SoftDeleteStamp(now), func() error {
		_, err := d.s.ModifyBook(l, func(b *database.Book) error { b.MarkedForDeletion, b.MarkedForDeletionAt = &yes, &now; return nil })
		return err
	}))
	return w
}

// A: the nil-shell retire handed the group to S but its hand-off note was
// never journaled (fs-regroup-xml before 2026-09-29, or a note whose
// journal write failed). Reverting it must still bring L back as the one
// primary. Before, the demote revert wrote nil, nothing crowned L, and the
// group was left with L's nil (read as primary) beside S's true.
func TestRevert_NilShellWithoutAHandOffNoteComesBackPrimary(t *testing.T) {
	d := newDCFixture(t)
	s, l := d.dune(t)
	no := false
	groupOf(t, d, "vg-dune", map[string]*bool{s: &no, l: nil})
	nilShellRetired(t, d, "op-y", l)
	_, err := versionprimary.Crown(fragEnsureStore{OpsStore: d.s, chapters: d.s}, "vg-dune", s)
	require.NoError(t, err)
	require.Equal(t, []string{s}, d.livePrimaries(t, "vg-dune"))

	_, err = audiobooks.NewRevertService(d.s).RevertOperation("op-y")
	require.NoError(t, err)
	require.Equal(t, []string{l}, d.livePrimaries(t, "vg-dune"))
}

// B: the nil-shell retire's hand-off failed at op time: no hand-off row and
// no live primary in the group. The revert ends with L the one primary and
// is NOT partial. Before, the hand-off after the soft-delete row ran before
// L's demote row: crowning L made that row find true where it expected the
// false the op wrote (changed since: partial for good), crowning S left L's
// nil beside it.
func TestRevert_NilShellWithAFailedHandOffRevertsWhole(t *testing.T) {
	d := newDCFixture(t)
	s, l := d.dune(t)
	no := false
	groupOf(t, d, "vg-dune", map[string]*bool{s: &no, l: nil})
	nilShellRetired(t, d, "op-y", l)
	require.Empty(t, d.livePrimaries(t, "vg-dune"))

	res, err := audiobooks.NewRevertService(d.s).RevertOperation("op-y")
	require.NoError(t, err)
	require.False(t, res.Partial(), "%+v", res)
	require.Equal(t, []string{l}, d.livePrimaries(t, "vg-dune"))
}

// C: the revert's group settle fails; a user then makes another member, M,
// the primary. The re-run retries the owed settle but finds M made primary
// since the failure, so it writes nothing and drops the record: the user's
// pick stays. Before, the demote row was left unmarked and the re-run
// re-applied it (any current false read as revertable) and crowned L over
// the user's pick.
func TestRevert_OwedSettleYieldsToALaterUserPick(t *testing.T) {
	d := newDCFixture(t)
	s, l := d.dune(t)
	m := d.copyBook(t, "M", "Dune", "lib/Dune third", dcRow{track: 1, dur: 600, hash: "m1"})
	no, yes := false, true
	groupOf(t, d, "vg-dune", map[string]*bool{s: &no, l: &yes, m: &no})
	w := repairs.NewWriter(d.s, d.s, dcFixerID, "bulk_update", "repairs-").WithJournal(d.s, d.s, "op-c")
	require.NoError(t, w.Step(l, undo.ChangeTypeBookPrimaryDemote, "is_primary_version", "true", "false", func() error {
		_, err := d.s.ModifyBook(l, func(b *database.Book) error { b.IsPrimaryVersion = &no; return nil })
		return err
	}))
	now := time.Now().UTC().Truncate(time.Microsecond)
	require.NoError(t, w.Step(l, undo.ChangeTypeBookSoftDelete, "marked_for_deletion", "", undo.SoftDeleteStamp(now), func() error {
		_, err := d.s.ModifyBook(l, func(b *database.Book) error { b.MarkedForDeletion, b.MarkedForDeletionAt = &yes, &now; return nil })
		return err
	}))
	_, err := versionprimary.Crown(fragEnsureStore{OpsStore: d.s, chapters: d.s}, "vg-dune", s)
	require.NoError(t, err)
	require.NoError(t, w.Journal(l, undo.ChangeTypeBookPrimaryHandoff, "version_group_id", "", "vg-dune"))

	failing := &groupReadFailsOnce{PebbleStore: d.s, group: "vg-dune", nth: 3} // 1: incumbent, 2: settle, 3: Crown
	failing.armed.Store(true)
	_, err = audiobooks.NewRevertService(failing).RevertOperation("op-c")
	require.Error(t, err)
	require.False(t, failing.armed.Load())

	// The user picks M.
	_, err = d.s.ModifyBook(l, func(b *database.Book) error { b.IsPrimaryVersion = &no; return nil })
	require.NoError(t, err)
	_, err = d.s.ModifyBook(s, func(b *database.Book) error { b.IsPrimaryVersion = &no; return nil })
	require.NoError(t, err)
	_, err = d.s.ModifyBook(m, func(b *database.Book) error { b.IsPrimaryVersion = &yes; return nil })
	require.NoError(t, err)

	_, _ = audiobooks.NewRevertService(d.s).RevertOperation("op-c")
	require.Equal(t, []string{m}, d.livePrimaries(t, "vg-dune"), "the user's pick stays")
	mb, err := d.s.GetBookByID(m)
	require.NoError(t, err)
	require.True(t, mb.IsPrimaryVersion != nil && *mb.IsPrimaryVersion)
}

// markFailsOnce fails the first MarkOperationChangesReverted.
type markFailsOnce struct {
	*database.PebbleStore
	armed atomic.Bool
}

func (m *markFailsOnce) MarkOperationChangesReverted(op string, ids []string) error {
	if m.armed.CompareAndSwap(true, false) {
		return errors.New("mark failed")
	}
	return m.PebbleStore.MarkOperationChangesReverted(op, ids)
}

// groupReadPanics panics on the nth version-group read of group: a revert
// that dies mid-run.
type groupReadPanics struct {
	*database.PebbleStore
	group string
	nth   int32
	reads atomic.Int32
}

func (g *groupReadPanics) GetBooksByVersionGroup(groupID string) ([]database.Book, error) {
	if groupID == g.group && g.reads.Add(1) == g.nth {
		panic("revert died")
	}
	return g.PebbleStore.GetBooksByVersionGroup(groupID)
}

// nilShellHandedTo journals the nil-shell retire of l in op and hands its
// group to s, noting the hand-off with s as the member crowned.
func nilShellHandedTo(t *testing.T, d *dcFixture, op, l, s string) {
	t.Helper()
	w := nilShellRetired(t, d, op, l)
	_, err := versionprimary.Crown(fragEnsureStore{OpsStore: d.s, chapters: d.s}, "vg-dune", s)
	require.NoError(t, err)
	require.NoError(t, w.Journal(l, undo.ChangeTypeBookPrimaryHandoff, "version_group_id", undo.HandOffCrownedValue(s), "vg-dune"))
}

// The rows' mark fails after their writes: the settle must already have
// run, so the re-run (which finds every row already restored) leaves the
// original the one primary. Before, the rows were marked first and the
// settle came after: a failed mark returned before it, nothing was owed,
// and the re-run settled nothing, leaving L's nil beside S's true.
func TestRevert_FailedMarkStillSettlesTheGroup(t *testing.T) {
	d := newDCFixture(t)
	s, l := d.dune(t)
	no := false
	groupOf(t, d, "vg-dune", map[string]*bool{s: &no, l: nil})
	nilShellRetired(t, d, "op-y", l)
	_, err := versionprimary.Crown(fragEnsureStore{OpsStore: d.s, chapters: d.s}, "vg-dune", s)
	require.NoError(t, err)

	failing := &markFailsOnce{PebbleStore: d.s}
	failing.armed.Store(true)
	_, err = audiobooks.NewRevertService(failing).RevertOperation("op-y")
	require.Error(t, err)
	require.False(t, failing.armed.Load())
	_, _ = audiobooks.NewRevertService(d.s).RevertOperation("op-y")
	require.Equal(t, []string{l}, d.livePrimaries(t, "vg-dune"))
}

// A revert that dies after its rows and before its settle: the intent it
// wrote before the rows makes the re-run count those rows, found already
// restored, as evidence, and settle the group. Before, nothing was recorded
// and the re-run settled nothing.
func TestRevert_RunDyingBeforeTheSettleIsFinishedByTheNext(t *testing.T) {
	d := newDCFixture(t)
	s, l := d.dune(t)
	no := false
	groupOf(t, d, "vg-dune", map[string]*bool{s: &no, l: nil})
	nilShellRetired(t, d, "op-y", l)
	_, err := versionprimary.Crown(fragEnsureStore{OpsStore: d.s, chapters: d.s}, "vg-dune", s)
	require.NoError(t, err)

	dying := &groupReadPanics{PebbleStore: d.s, group: "vg-dune", nth: 2} // 1: incumbent, 2: settle
	func() {
		defer func() { require.NotNil(t, recover(), "the first run died") }()
		_, _ = audiobooks.NewRevertService(dying).RevertOperation("op-y")
	}()
	require.NotEqual(t, []string{l}, d.livePrimaries(t, "vg-dune"), "the dead run did not settle")

	_, err = audiobooks.NewRevertService(d.s).RevertOperation("op-y")
	require.NoError(t, err)
	require.Equal(t, []string{l}, d.livePrimaries(t, "vg-dune"))
}

// The op's hand-off crowned S and recorded it; a user then made M the
// primary. Reverting the op brings L back non-primary: M, picked after the
// op, stays. Before, the settle crowned L over the user's pick.
func TestRevert_SettleKeepsAPrimaryPickedAfterTheOp(t *testing.T) {
	d := newDCFixture(t)
	s, l := d.dune(t)
	m := d.copyBook(t, "M", "Dune", "lib/Dune third", dcRow{track: 1, dur: 600, hash: "m1"})
	no := false
	groupOf(t, d, "vg-dune", map[string]*bool{s: &no, l: nil, m: &no})
	nilShellHandedTo(t, d, "op-y", l, s)
	_, err := versionprimary.Crown(fragEnsureStore{OpsStore: d.s, chapters: d.s}, "vg-dune", m)
	require.NoError(t, err)

	_, err = audiobooks.NewRevertService(d.s).RevertOperation("op-y")
	require.NoError(t, err)
	require.Equal(t, []string{m}, d.livePrimaries(t, "vg-dune"))
	lb, err := d.s.GetBookByID(l)
	require.NoError(t, err)
	require.False(t, lb.IsSoftDeleted())
	require.NotNil(t, lb.IsPrimaryVersion)
	require.False(t, *lb.IsPrimaryVersion, "L yields explicit false, not a nil read as primary")
}

// A retired book whose own demote row is refused (made primary while it
// was in the trash) still comes back, and the group ends with one primary.
// This pins the outcome only: the settle no longer counts such a book an
// original (refusedDemote), but when the demote is refused the book is
// already explicit true, so crowning it again writes nothing observable
// here.
func TestRevert_RefusedDemoteLeavesOnePrimary(t *testing.T) {
	d := newDCFixture(t)
	s, l := d.dune(t)
	no, yes := false, true
	groupOf(t, d, "vg-dune", map[string]*bool{s: &no, l: nil})
	nilShellRetired(t, d, "op-r", l)
	_, err := d.s.ModifyBook(l, func(b *database.Book) error { b.IsPrimaryVersion = &yes; return nil })
	require.NoError(t, err)

	res, err := audiobooks.NewRevertService(d.s).RevertOperation("op-r")
	require.Error(t, err, "the demote row is refused")
	require.Equal(t, 1, res.Failed, "%+v", res)
	require.Equal(t, []string{l}, d.livePrimaries(t, "vg-dune"))
}

// A run that dies in its settle leaves a pending record, but only for the
// groups whose rows it actually wrote. Group vg-b holds a retire cut off
// before it wrote (its demote is journaled, X is still primary): the re-run
// finds that row already restored and must leave vg-b alone. Before, the
// intent was written op-wide before any row ran, so the re-run counted
// vg-b's already-restored demote as evidence and crowned X, writing an
// explicit false over Y's never-set flag.
func TestRevert_LeftoverPendingRecordLeavesACutOffGroupAlone(t *testing.T) {
	d := newDCFixture(t)
	s, l := d.dune(t)
	x := d.copyBook(t, "X", "Arrakis", "lib/Arrakis one", dcRow{track: 1, dur: 600, hash: "x1"})
	y := d.copyBook(t, "Y", "Arrakis", "lib/Arrakis two", dcRow{track: 1, dur: 600, hash: "y1"})
	no, yes := false, true
	groupOf(t, d, "vg-dune", map[string]*bool{s: &no, l: nil})
	groupOf(t, d, "vg-b", map[string]*bool{x: &yes, y: nil})
	w := nilShellRetired(t, d, "op-y", l)
	_, err := versionprimary.Crown(fragEnsureStore{OpsStore: d.s, chapters: d.s}, "vg-dune", s)
	require.NoError(t, err)
	// The cut-off retire of X: journaled, never written.
	require.NoError(t, w.Journal(x, undo.ChangeTypeBookPrimaryDemote, "is_primary_version", "true", "false"))

	dying := &groupReadPanics{PebbleStore: d.s, group: "vg-dune", nth: 2} // 1: incumbent, 2: settle
	func() {
		defer func() { require.NotNil(t, recover(), "the first run died") }()
		_, _ = audiobooks.NewRevertService(dying).RevertOperation("op-y")
	}()
	_, _ = audiobooks.NewRevertService(d.s).RevertOperation("op-y")

	yb, err := d.s.GetBookByID(y)
	require.NoError(t, err)
	require.Nil(t, yb.IsPrimaryVersion, "vg-b, which the op never changed, is not written")
	require.Equal(t, []string{l}, d.livePrimaries(t, "vg-dune"), "the group the op did change is settled")
}

// An op that demoted TWO primaries of a group (as folder-books does for
// each primary folder-book) and handed the group to S; a user then made M
// the primary. Reverting the op restores both originals, and every one of
// them yields to M. Before, only the first yielded, so the other came back
// true beside M and Elect could pick it over the user's pick.
func TestRevert_EveryOriginalYieldsToALaterPick(t *testing.T) {
	d := newDCFixture(t)
	s, l := d.dune(t)
	l2 := d.copyBook(t, "L2", "Dune", "lib/Dune second", dcRow{track: 1, dur: 600, hash: "l2"})
	m := d.copyBook(t, "M", "Dune", "lib/Dune third", dcRow{track: 1, dur: 600, hash: "m1"})
	no, yes := false, true
	groupOf(t, d, "vg-dune", map[string]*bool{s: &no, l: &yes, l2: &yes, m: &no})
	w := repairs.NewWriter(d.s, d.s, dcFixerID, "bulk_update", "repairs-").WithJournal(d.s, d.s, "op-f")
	for _, id := range []string{l, l2} {
		id := id
		require.NoError(t, w.Step(id, undo.ChangeTypeBookPrimaryDemote, "is_primary_version", "true", "false", func() error {
			_, err := d.s.ModifyBook(id, func(b *database.Book) error { b.IsPrimaryVersion = &no; return nil })
			return err
		}))
	}
	_, err := versionprimary.Crown(fragEnsureStore{OpsStore: d.s, chapters: d.s}, "vg-dune", s)
	require.NoError(t, err)
	require.NoError(t, w.Journal(l, undo.ChangeTypeBookPrimaryHandoff, "version_group_id", undo.HandOffCrownedValue(s), "vg-dune"))
	_, err = versionprimary.Crown(fragEnsureStore{OpsStore: d.s, chapters: d.s}, "vg-dune", m)
	require.NoError(t, err)

	_, err = audiobooks.NewRevertService(d.s).RevertOperation("op-f")
	require.NoError(t, err)
	require.Equal(t, []string{m}, d.livePrimaries(t, "vg-dune"), "the user's pick stays")
	for _, id := range []string{l, l2} {
		b, err := d.s.GetBookByID(id)
		require.NoError(t, err)
		require.NotNil(t, b.IsPrimaryVersion)
		require.False(t, *b.IsPrimaryVersion, "%s yields explicit false", id)
	}
}

// Every retire's hand-off carries the iTunes guard (itunesguard.MayWrite),
// not only the consolidation-leftovers one: a duplicate-copies or
// fragment-consolidation retire whose group holds a nil-flag iTunes copy
// refuses the hand-off rather than demote it. Nothing in the group is
// written by the hand-off, the refusal is journaled, and the row's error
// names the copy.
func TestRetireInto_HandOffNeverWritesAnITunesMember(t *testing.T) {
	for _, fixerID := range []string{dcFixerID, fragFixerID} {
		t.Run(fixerID, func(t *testing.T) {
			d := newDCFixture(t)
			s, l := d.dune(t)
			gid := "vg-dune"
			yes := true
			pid := "0123456789ABCDEF"
			_, err := d.s.ModifyBook(s, func(b *database.Book) error { b.VersionGroupID, b.IsPrimaryVersion = &gid, &yes; return nil })
			require.NoError(t, err)
			_, err = d.s.ModifyBook(l, func(b *database.Book) error { b.VersionGroupID, b.IsPrimaryVersion = &gid, &yes; return nil })
			require.NoError(t, err)
			it, err := d.s.CreateBook(&database.Book{Title: "Dune (iTunes)", FilePath: "/x/it.m4b", VersionGroupID: &gid, ITunesPersistentID: &pid})
			require.NoError(t, err)

			w := repairs.NewWriter(d.s, d.s, fixerID, "bulk_update", "repairs-").WithJournal(d.s, d.s, "op-apply")
			_, err = retireInto(context.Background(), d.p, d.s, w, time.Now, fixerID, l, s, nil)
			require.ErrorIs(t, err, versionprimary.ErrWriteRefused)
			require.Contains(t, err.Error(), "iTunes copy "+it.ID)
			ib, err := d.s.GetBookByID(it.ID)
			require.NoError(t, err)
			require.Nil(t, ib.IsPrimaryVersion, "the iTunes copy's flag was never written")
			sb, err := d.s.GetBookByID(s)
			require.NoError(t, err)
			require.True(t, *sb.IsPrimaryVersion)
			require.False(t, handoffJournaled(t, d.s, "op-apply", l), "no hand-off made")
			changes, err := d.s.GetOperationChanges("op-apply")
			require.NoError(t, err)
			refused := false
			for _, c := range changes {
				refused = refused || (c.BookID == l && c.ChangeType == undo.ChangeTypeBookPrimaryHandoffRefused)
			}
			require.True(t, refused, "the refusal is journaled for the revert")
		})
	}
}
