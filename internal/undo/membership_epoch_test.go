// file: internal/undo/membership_epoch_test.go
// version: 1.1.0
// guid: 6b3e8d12-4a97-4c05-9f21-e7d0a5c18b34
// last-edited: 2026-10-22

package undo

import (
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// The epoch rule: a survivor is in a list on a retire's account from a "took"
// marker until the last retire of that epoch is reverted, and a later "took"
// marker ends every earlier epoch, so a standing marker of an earlier epoch
// (the survivor left the list by hand since) has no say over the survivor.
func TestSurvivorStand_EpochRule(t *testing.T) {
	t.Parallel()
	base := time.Unix(1_700_000_000, 0)
	at := func(sec int) time.Time { return base.Add(time.Duration(sec) * time.Second) }
	tp := func(sec int) *time.Time { v := at(sec); return &v }
	took := UserContentField(UserContentCollectionTook, "c1")
	held := UserContentField(UserContentCollectionHeld, "c1")
	mk := func(op, retired, field string, created int, reverted *time.Time) *database.OperationChange {
		return &database.OperationChange{ID: op + "/" + retired, ChangeType: ChangeTypeUserContentFollow, OperationID: op, BookID: "s",
			FieldName: field, NewValue: retired, CreatedAt: at(created), RevertedAt: reverted}
	}
	row := func(op, retired string) *database.OperationChange {
		return &database.OperationChange{ChangeType: ChangeTypeUserContentFollow, OperationID: op, BookID: retired, NewValue: "s"}
	}
	cases := []struct {
		name         string
		rows         []*database.OperationChange
		row          *database.OperationChange
		want         SurvivorStand
		wantBorrowed bool
	}{
		{"a standing row of a superseded epoch is not the survivor's: it neither borrows nor keeps it",
			[]*database.OperationChange{mk("op0", "a", took, 1, nil), mk("op1", "b", took, 3, nil)}, row("op0", "a"), SurvivorDefault, false},
		{"the row of the epoch that stands still borrows",
			[]*database.OperationChange{mk("op0", "a", took, 1, nil), mk("op1", "b", took, 3, nil)}, row("op1", "b"), SurvivorDefault, true},
		{"a held marker after the survivor was listed again by hand does not borrow through the old epoch",
			[]*database.OperationChange{mk("op0", "a", took, 1, nil), mk("op1", "b", took, 3, tp(5)), mk("op2", "c", held, 6, nil)}, row("op2", "c"), SurvivorDefault, false},
		{"a superseded epoch's standing row does not keep the survivor for the epoch that stands",
			[]*database.OperationChange{mk("op0", "a", took, 1, nil), mk("op1", "b", took, 3, nil), mk("op2", "c", held, 4, nil)}, row("op1", "b"), SurvivorStays, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			stand, borrowed := SurvivorStandFor(pairRows(tc.rows), tc.row, UserContentCollection, "c1")
			if stand != tc.want || borrowed != tc.wantBorrowed {
				t.Fatalf("got stand %v borrowed %v, want %v %v", stand, borrowed, tc.want, tc.wantBorrowed)
			}
		})
	}
}
