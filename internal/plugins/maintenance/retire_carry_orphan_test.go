// file: internal/plugins/maintenance/retire_carry_orphan_test.go
// version: 1.0.1
// guid: 5a1e8c34-7d92-4f06-b3a8-0e6d29c4f175
// last-edited: 2026-10-30

package maintenance

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// singleRowJournal is an op journal that writes one row at a time: no
// repairs.ChangeBatchJournal.
type singleRowJournal struct{ s *database.PebbleStore }

func (j singleRowJournal) CreateOperationChange(c *database.OperationChange) error {
	return j.s.CreateOperationChange(c)
}

func (j singleRowJournal) GetOperationChanges(op string) ([]*database.OperationChange, error) {
	return j.s.GetOperationChanges(op)
}

// A journal that cannot write a retire's rows in one commit refuses the retire
// BEFORE its first write, like a store without the list capabilities: written
// one by one, a failure between a carry's marker and its row would leave a
// marker standing on nothing. Nothing moves, nothing is journaled, the book
// stays live.
func TestRetireCarry_AJournalWithoutBatchesRefusesBeforeAnyWrite(t *testing.T) {
	d := newDCFixture(t)
	s, l := d.dune(t)
	d.rcRate(t, "L", database.UpdateBookRatingRequest{Overall: rcFloat(4)})
	col := d.rcCollection(t, "Favourites", l)
	w := repairs.NewWriter(d.s, d.s, dcFixerID, "bulk_update", "repairs-").WithJournal(d.s, singleRowJournal{d.s}, "op-apply")
	_, err := retireInto(context.Background(), d.p, d.s, w, time.Now, dcFixerID, l, s, nil)
	require.ErrorIs(t, err, errCarryJournalNotBatched)
	require.ErrorContains(t, err, "nothing was written")
	changes, err := d.s.GetOperationChanges("op-apply")
	require.NoError(t, err)
	require.Empty(t, changes, "nothing journaled")
	require.True(t, d.live(t, "L"), "the retired book is still live")
	sb, err := d.s.GetBookByID(s)
	require.NoError(t, err)
	require.Nil(t, sb.UserRatingOverall, "the survivor took nothing")
	require.Equal(t, []string{l}, d.rcMembers(t, col))
}

// An orphan survivor marker end to end. l0 = [a B c X]. op1 retires B into S (S
// takes B's slot). op2 began retiring X into S (S listed: a held marker) and its
// row never made it into the journal (an older build committed marker and row
// apart, and died between), or its void failed; its list write never ran.
// Believed, the marker borrows S (op1's took marker stands), so reverting op1
// left S in the list for good. Whatever the revert order, the list ends exactly
// as it began, S out, and the orphan is named in Kept and counted
// (RevertResult.OrphanMarkers), never silently.
func TestRetireCarry_AnOrphanMarkerNeverKeepsTheSurvivorListed(t *testing.T) {
	t.Parallel()
	for _, voidedRow := range []bool{false, true} {
		for _, order := range [][]string{{"op1", "op2"}, {"op2", "op1"}} {
			c := newChainFixture(t, []string{"a", "B", "c", "X", "S"}, []string{"a", "B", "c", "X"})
			c.carry("op1", "B", "S")
			require.Equal(t, []string{"a", "S", "c", "X"}, c.members())
			_, rec, ok := undo.CarryMembership(c.f.rcMembers(t, c.col), c.id["X"], c.id["S"])
			require.True(t, ok)
			require.True(t, rec.SurvivorWasMember)
			val, err := undo.EncodeMembership(rec)
			require.NoError(t, err)
			marker := &database.OperationChange{ID: ulid.Make().String(), OperationID: "op2", BookID: c.id["S"],
				ChangeType: undo.ChangeTypeUserContentFollow, FieldName: undo.UserContentField(undo.UserContentCollectionHeld, c.col), OldValue: val, NewValue: c.id["X"]}
			require.NoError(t, c.f.s.CreateOperationChange(marker))
			if voidedRow {
				now := time.Now()
				require.NoError(t, c.f.s.CreateOperationChange(&database.OperationChange{ID: ulid.Make().String(), OperationID: "op2", BookID: c.id["X"],
					ChangeType: undo.ChangeTypeUserContentFollow, FieldName: undo.UserContentField(undo.UserContentCollection, c.col), OldValue: val, NewValue: c.id["S"],
					RevertedAt: &now, Voided: true}))
			}
			var named int
			var kept []string
			for _, op := range order {
				rr, err := audiobooks.NewRevertService(c.st).RevertOperation(op)
				require.NoError(t, err, "voidedRow=%v order=%v %s", voidedRow, order, op)
				require.Zero(t, rr.Failed, "voidedRow=%v order=%v %s", voidedRow, order, op)
				named += rr.OrphanMarkers
				kept = append(kept, keptTexts(rr)...)
				// Who names it. The orphan is reported by whichever revert meets it
				// standing: op1's (it changes the list's decisions: the row of B
				// reads the list's journal) when op1 goes first, op2's own (its
				// post-pass stamps it, accounting for nothing) when op2 goes first.
				// Once stamped it is not named again.
				switch {
				case op == "op1" && order[0] == "op1", op == "op2":
					require.Equal(t, 1, rr.OrphanMarkers, "voidedRow=%v order=%v %s must name the standing orphan once: %v", voidedRow, order, op, rr.Kept)
					require.Contains(t, strings.Join(keptTexts(rr), "\n"), marker.ID)
				default:
					require.Zero(t, rr.OrphanMarkers, "voidedRow=%v order=%v %s: a stamped orphan is not named again: %v", voidedRow, order, op, rr.Kept)
				}
				if rr.OrphanMarkers > 0 {
					require.Contains(t, rr.Summary(), "survivor marker(s) with no live row", "the summary says so")
				}
			}
			require.Equal(t, []string{"a", "B", "c", "X"}, c.members(), "voidedRow=%v order=%v: S must leave with op1, the only retire that stands on it", voidedRow, order)
			require.GreaterOrEqual(t, named, 1, "voidedRow=%v order=%v: the orphan is never ignored silently", voidedRow, order)
			require.True(t, strings.Contains(strings.Join(kept, "\n"), marker.ID), "voidedRow=%v order=%v: Kept names the marker; kept=%v", voidedRow, order, kept)
		}
	}
}

// keptTexts is the text of each line of a revert's Kept list.
func keptTexts(rr *audiobooks.RevertResult) []string {
	out := make([]string, len(rr.Kept))
	for i, n := range rr.Kept {
		out[i] = n.Text
	}
	return out
}
