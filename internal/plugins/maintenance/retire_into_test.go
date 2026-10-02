// file: internal/plugins/maintenance/retire_into_test.go
// version: 1.0.0
// guid: 90cd2c0f-e6c5-4176-8d2c-bc587eea86cd
// last-edited: 2026-10-02

package maintenance

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

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
