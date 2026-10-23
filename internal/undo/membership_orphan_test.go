// file: internal/undo/membership_orphan_test.go
// version: 1.0.0
// guid: 9e4b27c1-5a38-4d60-b1f7-a83c0d6e2f54
// last-edited: 2026-10-22

package undo

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// orphanFixture is the reviewer's scenario: l0 = [a B c X]. op1 retires B into
// S (S takes B's slot: a took marker and B's row). op2 starts retiring X into S
// (S is listed: a held marker) and its row is never journaled or is voided; its
// list write never ran. Reverting op1 must take S out again whatever op2's
// orphan marker says.
type orphanFixture struct {
	t1, r1, h2 *database.OperationChange
	rec1       MembershipRecord
	extra      []*database.OperationChange
}

func newOrphanFixture(voidedRow bool) orphanFixture {
	base := time.Unix(1_800_000_000, 0)
	enc := func(r MembershipRecord) string { s, _ := EncodeMembership(r); return s }
	f := orphanFixture{rec1: MembershipRecord{Index: 1, Anchored: true, Prev: "a", Next: "c"}}
	f.t1 = &database.OperationChange{ID: "01-op1-S", OperationID: "op1", BookID: "S", ChangeType: ChangeTypeUserContentFollow,
		FieldName: UserContentField(UserContentCollectionTook, "L"), OldValue: enc(f.rec1), NewValue: "B", CreatedAt: base, CreatedSeq: base.UnixNano()}
	f.r1 = &database.OperationChange{ID: "02-op1-B", OperationID: "op1", BookID: "B", ChangeType: ChangeTypeUserContentFollow,
		FieldName: UserContentField(UserContentCollection, "L"), OldValue: enc(f.rec1), NewValue: "S", CreatedAt: base.Add(time.Millisecond), CreatedSeq: base.UnixNano() + 1}
	rec2 := MembershipRecord{Index: 3, SurvivorWasMember: true, Anchored: true, Prev: "c"}
	f.h2 = &database.OperationChange{ID: "03-op2-S", OperationID: "op2", BookID: "S", ChangeType: ChangeTypeUserContentFollow,
		FieldName: UserContentField(UserContentCollectionHeld, "L"), OldValue: enc(rec2), NewValue: "X", CreatedAt: base.Add(time.Second), CreatedSeq: base.UnixNano() + 2}
	if voidedRow {
		v := base.Add(3 * time.Second)
		f.extra = append(f.extra, &database.OperationChange{ID: "04-op2-X", OperationID: "op2", BookID: "X", ChangeType: ChangeTypeUserContentFollow,
			FieldName: UserContentField(UserContentCollection, "L"), OldValue: enc(rec2), NewValue: "S", CreatedAt: base.Add(2 * time.Second), CreatedSeq: base.UnixNano() + 3,
			RevertedAt: &v, RevertedSeq: base.UnixNano() + 4, Voided: true})
	}
	return f
}

func (f orphanFixture) rows() []*database.OperationChange {
	return append([]*database.OperationChange{f.t1, f.r1, f.h2}, f.extra...)
}

// revertB reverts op1's row of B against the list [a S c X], the way the revert
// does (MemberRoleOf and the graph from the list's whole journal).
func (f orphanFixture) revertB(t *testing.T, rows []*database.OperationChange) ([]string, MemberRole) {
	t.Helper()
	role := MemberRoleOf(rows, f.r1, UserContentCollection, "L")
	g := NewMembershipGraph("S", UserContentCollection, "L")
	g.SetRow(f.r1)
	g.AddRows(rows)
	next, _, err := RestoreMember([]string{"a", "S", "c", "X"}, "B", "S", f.rec1, role, g)
	require.NoError(t, err)
	return next, role
}

// A marker with no live row of its retire accounts for nothing: believed, the
// held marker of op2 (which borrows S, because op1's took marker stands) would
// keep S listed for good after op1 is reverted.
func TestOrphanMarker_AMarkerWithoutALiveRowDoesNotKeepTheSurvivorListed(t *testing.T) {
	t.Parallel()
	for _, voided := range []bool{false, true} {
		f := newOrphanFixture(voided)
		next, role := f.revertB(t, f.rows())
		require.Equal(t, []string{"a", "B", "c", "X"}, next, "voidedRow=%v: S must leave with the only retire that stands on it (role %+v)", voided, role)
		require.True(t, role.Borrowed)
		require.Nil(t, PairedRow(f.rows(), f.h2), "the orphan has no live paired row")
	}
}

// The ignored marker is reported by every entry point: the lister, and the graph.
func TestOrphanMarker_IsListedForTheReport(t *testing.T) {
	t.Parallel()
	for _, voided := range []bool{false, true} {
		f := newOrphanFixture(voided)
		got := IgnoredMarkers(f.rows(), UserContentCollection, "L")
		require.Len(t, got, 1, "voidedRow=%v", voided)
		require.Equal(t, IgnoredMarker{ID: f.h2.ID, OperationID: "op2", Survivor: "S", Retired: "X", Field: f.h2.FieldName, Standing: true, Reason: IgnoredNoRow}, got[0])
		require.Contains(t, got[0].Detail(), f.h2.ID)
		g := NewMembershipGraph("S", UserContentCollection, "L")
		g.SetRow(f.r1)
		g.AddRows(f.rows())
		require.Equal(t, got, g.Ignored())
	}
	// A list with no orphan lists none.
	f := newOrphanFixture(false)
	require.Empty(t, IgnoredMarkers([]*database.OperationChange{f.t1, f.r1}, UserContentCollection, "L"))
}

// A marker already stamped reverted is ignored as well (it never accounted for a
// live retire) but is not reported: it can change no decision now, and the
// revert that stamped it said so.
func TestOrphanMarker_AStampedOrphanIsIgnoredQuietly(t *testing.T) {
	t.Parallel()
	f := newOrphanFixture(false)
	at := time.Unix(1_800_000_100, 0)
	f.h2.RevertedAt, f.h2.RevertedSeq = &at, at.UnixNano()
	got := IgnoredMarkers(f.rows(), UserContentCollection, "L")
	require.Len(t, got, 1)
	require.False(t, got[0].Standing)
	next, _ := f.revertB(t, f.rows())
	require.Equal(t, []string{"a", "B", "c", "X"}, next)
}

// A took marker with no row ends no epoch and starts none: the survivor is an
// original member as far as the live retires say.
func TestOrphanMarker_ATookOrphanStartsNoEpoch(t *testing.T) {
	t.Parallel()
	f := newOrphanFixture(false)
	f.h2.FieldName = UserContentField(UserContentCollectionTook, "L")
	rec := MembershipRecord{Index: 3, Anchored: true, Prev: "c"}
	f.h2.OldValue, _ = EncodeMembership(rec)
	next, _ := f.revertB(t, f.rows())
	require.Equal(t, []string{"a", "B", "c", "X"}, next)
	require.False(t, MarkerSuperseded(f.rows(), f.r1, UserContentCollection, "L"), "an orphan took marker must not supersede op1's epoch")
}

// A resumed run that journaled the same retire again with another record leaves
// two markers of one retire; only the latest counts, the earlier is reported.
func TestOrphanMarker_AReplacedMarkerOfTheSameRetireIsIgnored(t *testing.T) {
	t.Parallel()
	f := newOrphanFixture(false)
	enc := func(r MembershipRecord) string { s, _ := EncodeMembership(r); return s }
	// op1 again, later: S was listed by then, so the record is a held one.
	rec := MembershipRecord{Index: 1, SurvivorWasMember: true, Anchored: true, Prev: "a", Next: "c"}
	m2 := &database.OperationChange{ID: "05-op1-S", OperationID: "op1", BookID: "S", ChangeType: ChangeTypeUserContentFollow,
		FieldName: UserContentField(UserContentCollectionHeld, "L"), OldValue: enc(rec), NewValue: "B", CreatedAt: f.h2.CreatedAt.Add(time.Hour), CreatedSeq: f.h2.CreatedSeq + 10}
	r2 := &database.OperationChange{ID: "06-op1-B", OperationID: "op1", BookID: "B", ChangeType: ChangeTypeUserContentFollow,
		FieldName: UserContentField(UserContentCollection, "L"), OldValue: enc(rec), NewValue: "S", CreatedAt: m2.CreatedAt.Add(time.Millisecond), CreatedSeq: m2.CreatedSeq + 1}
	rows := []*database.OperationChange{f.t1, f.r1, m2, r2}
	got := IgnoredMarkers(rows, UserContentCollection, "L")
	require.Len(t, got, 1)
	require.Equal(t, f.t1.ID, got[0].ID)
	require.Equal(t, IgnoredReplaced, got[0].Reason)
	// Only the later marker counts: a held marker with no borrowing marker
	// before it borrows nothing.
	stand, borrowed := SurvivorStandFor(rows, r2, UserContentCollection, "L")
	require.Equal(t, SurvivorDefault, stand)
	require.False(t, borrowed)
}

// The decisions follow the journal's sequence, not the wall time: a wall clock
// that stepped back must not reorder a took marker and the held marker written
// after it.
func TestJournalOrder_SequenceBeatsAWallClockThatSteppedBack(t *testing.T) {
	t.Parallel()
	base := time.Unix(1_800_000_000, 0)
	enc := func(r MembershipRecord) string { s, _ := EncodeMembership(r); return s }
	mk := func(id, op, book, field, retired string, wall time.Time, seq int64) *database.OperationChange {
		return &database.OperationChange{ID: id, OperationID: op, BookID: book, ChangeType: ChangeTypeUserContentFollow,
			FieldName: field, OldValue: enc(MembershipRecord{Index: 1}), NewValue: retired, CreatedAt: wall, CreatedSeq: seq}
	}
	took := UserContentField(UserContentCollectionTook, "L")
	held := UserContentField(UserContentCollectionHeld, "L")
	main := UserContentField(UserContentCollection, "L")
	// op2's marker was written after op1's (sequence), but the wall clock read an
	// hour earlier for it.
	m1 := mk("m1", "op1", "S", took, "A", base, 100)
	r1 := mk("r1", "op1", "A", main, "S", base, 101)
	m2 := mk("m2", "op2", "S", held, "B", base.Add(-time.Hour), 200)
	r2 := mk("r2", "op2", "B", main, "S", base.Add(-time.Hour), 201)
	rows := []*database.OperationChange{m1, r1, m2, r2}
	stand, borrowed := SurvivorStandFor(rows, r2, UserContentCollection, "L")
	require.True(t, borrowed, "m2 was written while m1 stood: it borrows")
	require.Equal(t, SurvivorStays, stand, "m1, a borrower, still stands")
	// PairedRow and MarkerOf take the later of two rows by sequence as well.
	late := *r2
	late.ID, late.CreatedAt, late.CreatedSeq = "r2b", base.Add(-2*time.Hour), 300
	require.Equal(t, "r2b", PairedRow([]*database.OperationChange{m2, r2, &late}, m2).ID)
	// A marker stamped by a mark whose wall time is before a later row's creation
	// but whose sequence is after it still stood when that row was written.
	m1.RevertedAt, m1.RevertedSeq = ptr(base.Add(-3*time.Hour)), 250
	require.Equal(t, int64(250), RevertedKey(m1))
	require.Equal(t, int64(0), RevertedKey(m2))
	require.Equal(t, int64(100), CreatedKey(m1))
	legacy := &database.OperationChange{CreatedAt: base}
	require.Equal(t, base.UnixNano(), CreatedKey(legacy))
	legacy.RevertedAt = ptr(base.Add(time.Second))
	require.Equal(t, base.Add(time.Second).UnixNano(), RevertedKey(legacy))
}

func ptr[T any](v T) *T { return &v }
