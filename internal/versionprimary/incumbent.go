// file: internal/versionprimary/incumbent.go
// version: 1.1.0
// guid: a10a4338-0d70-498e-aa2f-5f8db63bf26d
// last-edited: 2026-10-01

package versionprimary

import (
	"fmt"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// Incumbent returns the member of a version group that currently acts as its
// primary, read-only, or nil when the group has none or it cannot be told
// apart without electing. It never elects: a caller that needs a primary
// written uses EnsureSinglePrimary.
//
// The rule, in order:
//
//  1. An Electable member whose flag is explicitly true. The first one in
//     members order wins when there are several (a doubled group; the
//     hand-off is what repairs that, not this read).
//  2. Otherwise, the ONE Electable member whose flag is nil. Every visibility
//     path reads a nil flag as primary (database.EffectiveIsPrimaryVersion),
//     so that member is the copy the user sees. Two or more nil members are
//     ambiguous and return nil rather than guessing.
//
// A soft-deleted member, and a merge loser whose survivor is alive, are never
// the incumbent (Electable). alive answers for merge survivors; a member of
// the group is answered from members itself.
func Incumbent(members []database.Book, alive func(id string) bool) *database.Book {
	inGroup := make(map[string]bool, len(members))
	for i := range members {
		inGroup[members[i].ID] = !members[i].IsSoftDeleted()
	}
	isAlive := func(id string) bool {
		if live, ok := inGroup[id]; ok {
			return live
		}
		return alive(id)
	}

	var nilFlag *database.Book
	nilCount := 0
	for i := range members {
		m := &members[i]
		if !Electable(m, isAlive) {
			continue
		}
		if explicitTrue(m) {
			return m
		}
		if m.IsPrimaryVersion == nil {
			nilCount++
			nilFlag = m
		}
	}
	if nilCount == 1 {
		return nilFlag
	}
	return nil
}

// IncumbentExcept reads group gid and returns the ID of its Incumbent among
// the members other than exceptID, or "" when there is none (or gid is
// empty). A restore reads it BEFORE writing, so a restored row that still
// carries the explicit true it had when it was trashed does not take the
// group back from the member that has been its primary since.
func IncumbentExcept(store EnsureStore, gid, exceptID string) (string, error) {
	if gid == "" {
		return "", nil
	}
	members, err := store.GetBooksByVersionGroup(gid)
	if err != nil {
		return "", fmt.Errorf("read version group %s: %w", gid, err)
	}
	others := make([]database.Book, 0, len(members))
	for i := range members {
		if members[i].ID != exceptID {
			others = append(others, members[i])
		}
	}
	if inc := Incumbent(others, storeAlive(store)); inc != nil {
		return inc.ID, nil
	}
	return "", nil
}

// YieldToIncumbent demotes a row being restored from the trash when its group
// has had another primary since (incumbentID, from IncumbentExcept read
// before the restore) and the row still carries an explicit true. The
// restored row comes back as a non-primary member, the same rule the combine
// undo applies, and the hand-off that follows finds the group healthy.
func YieldToIncumbent(row *database.Book, incumbentID string) {
	if row == nil || incumbentID == "" || incumbentID == row.ID || !explicitTrue(row) {
		return
	}
	f := false
	row.IsPrimaryVersion = &f
}
