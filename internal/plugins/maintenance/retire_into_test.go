// file: internal/plugins/maintenance/retire_into_test.go
// version: 1.2.0
// guid: 90cd2c0f-e6c5-4176-8d2c-bc587eea86cd
// last-edited: 2026-10-02

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
)

// journalCountingStore is the ops store with GetBookChanges (the unindexed
// opchange scan) counted, and the history read resumeHandOff asserts passed
// through.
type journalCountingStore struct {
	OpsStore
	inner interface {
		bookJournalReader
		bookHistoryReader
	}
	scans int
}

func (s *journalCountingStore) GetBookChanges(bookID string) ([]*database.OperationChange, error) {
	s.scans++
	return s.inner.GetBookChanges(bookID)
}

func (s *journalCountingStore) GetBookChangeHistory(bookID string, limit int) ([]database.MetadataChangeRecord, error) {
	return s.inner.GetBookChangeHistory(bookID, limit)
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
			if order[0] == "op-a" {
				// S was crowned by op-b's hand-off and is still live when L
				// comes back: L yields to it.
				require.Equal(t, []string{s}, d.livePrimaries(t, gid))
			}
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

// groupReadFailsOnce fails the first version-group read of group: the
// soft-delete revert's incumbent read.
type groupReadFailsOnce struct {
	*database.PebbleStore
	group string
	armed atomic.Bool
}

func (g *groupReadFailsOnce) GetBooksByVersionGroup(groupID string) ([]database.Book, error) {
	if groupID == g.group && g.armed.CompareAndSwap(true, false) {
		return nil, errors.New("version group read failed")
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
