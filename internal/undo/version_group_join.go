// file: internal/undo/version_group_join.go
// version: 1.0.1
// guid: 03d9635a-205f-4a87-a5bf-f49a1396f7bd
// last-edited: 2026-11-09

package undo

import (
	"encoding/json"
	"fmt"
	"strings"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
)

// ChangeTypeRepairVersionGroupJoin: a Repairs fixer put a live book that had
// no version group into a new one (the folder-books fixer's owner apply, when
// an orphan file's title and author already name a book that is not in any
// group: that book becomes the primary of a group the new, non-primary
// version joins). The row is journaled on the book that joined, BEFORE the
// write. OldValue and NewValue are VersionGroupJoinValue JSON: the group and
// primary flag the book carried (pointer shape included: nil is not "") and
// the ones the join wrote.
//
// Restorable. The revert writes the old group and flag back only while the
// book is still exactly what the join left (in the minted group, explicit
// primary) and no other live book is in that group. A version the same
// operation added is hidden by its own repair_book_create revert first (it is
// journaled after this row, and the revert runs newest first); the preflight
// reads that order too (VersionGroupJoinVerdict). A join whose write never
// happened leaves the book as it was, which counts as already restored.
const ChangeTypeRepairVersionGroupJoin = "repair_version_group_join"

// VersionGroupState is a book's group and primary flag, each exactly as
// stored: a nil pointer is "never set", which a Group of "" is not.
type VersionGroupState struct {
	Group   *string `json:"group"`
	Primary *bool   `json:"primary"`
}

// VersionGroupJoinValue is a repair_version_group_join row's value (both
// OldValue and NewValue; NewValue's Group is the minted id).
type VersionGroupJoinValue = VersionGroupState

// StateOfBook reads b's group and primary flag, copied.
func StateOfBook(b *database.Book) VersionGroupState {
	var s VersionGroupState
	if b.VersionGroupID != nil {
		g := *b.VersionGroupID
		s.Group = &g
	}
	if b.IsPrimaryVersion != nil {
		p := *b.IsPrimaryVersion
		s.Primary = &p
	}
	return s
}

// Apply writes s into b (a copy of each pointer).
func (s VersionGroupState) Apply(b *database.Book) {
	b.VersionGroupID, b.IsPrimaryVersion = nil, nil
	if s.Group != nil {
		g := *s.Group
		b.VersionGroupID = &g
	}
	if s.Primary != nil {
		p := *s.Primary
		b.IsPrimaryVersion = &p
	}
}

// Encode is the value as a row stores it.
func (s VersionGroupState) Encode() (string, error) {
	b, err := json.Marshal(s)
	if err != nil {
		return "", fmt.Errorf("encode version group state: %w", err)
	}
	return string(b), nil
}

// GroupID is the trimmed group id ("" for none or unset).
func (s VersionGroupState) GroupID() string {
	if s.Group == nil {
		return ""
	}
	return strings.TrimSpace(*s.Group)
}

func sameFlag(a, b *bool) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// Same reports whether two states hold the same group (trimmed) and the same
// primary flag, nil and false being different.
func (s VersionGroupState) Same(o VersionGroupState) bool {
	return s.GroupID() == o.GroupID() && sameFlag(s.Primary, o.Primary)
}

// DecodeVersionGroupJoin reads a row's two values. A row that is not a join
// of a group-less book into a group (an old group, a new group that is
// empty, or a new flag that is not an explicit true) is malformed: the revert
// would write something the join never did.
func DecodeVersionGroupJoin(c *database.OperationChange) (old, now VersionGroupState, err error) {
	if err = json.Unmarshal([]byte(c.OldValue), &old); err != nil {
		return old, now, fmt.Errorf("%s old value: %w", ChangeTypeRepairVersionGroupJoin, err)
	}
	if err = json.Unmarshal([]byte(c.NewValue), &now); err != nil {
		return old, now, fmt.Errorf("%s new value: %w", ChangeTypeRepairVersionGroupJoin, err)
	}
	if old.GroupID() != "" || now.GroupID() == "" || now.Primary == nil || !*now.Primary {
		return old, now, fmt.Errorf("%s row %s does not join a group-less book into a group as its primary", ChangeTypeRepairVersionGroupJoin, c.ID)
	}
	return old, now, nil
}

// CheckVersionGroupJoinCurrent is the revert's compare-and-set on the book:
//
//   - the book already holds the old state (the join never wrote, or was put
//     back): ErrAlreadyRestored. A book with no group whose primary flag is
//     NOT the old one is refused: someone changed it since.
//   - the book is not in the minted group, or no longer its explicit primary,
//     or another live book (not in ignore: the books the same revert hides
//     first) is in that group: refused (ReasonChangedSince).
//
// members is the group's current members (the minted group's, as the store
// lists them, soft-deleted ones included; they are skipped here).
func CheckVersionGroupJoinCurrent(book *database.Book, members []database.Book, ignore map[string]bool, c *database.OperationChange) error {
	old, now, err := DecodeVersionGroupJoin(c)
	if err != nil {
		return refuse(ReasonOldValueUnparsable, "%v", err)
	}
	cur := StateOfBook(book)
	if cur.GroupID() == "" {
		if sameFlag(cur.Primary, old.Primary) {
			return ErrAlreadyRestored
		}
		return refuse(ReasonChangedSince, "book %s has no version group but its primary flag was changed since the join", book.ID)
	}
	if cur.GroupID() != now.GroupID() {
		return refuse(ReasonChangedSince, "book %s was moved from the group the operation made (%s) to %s since", book.ID, now.GroupID(), cur.GroupID())
	}
	if cur.Primary == nil || !*cur.Primary {
		return refuse(ReasonChangedSince, "book %s is no longer the primary of the group the operation made (%s)", book.ID, now.GroupID())
	}
	for i := range members {
		m := members[i]
		if m.ID == book.ID || m.IsSoftDeleted() || ignore[m.ID] {
			continue
		}
		return refuse(ReasonChangedSince, "book %s joined the group the operation made (%s) since", m.ID, now.GroupID())
	}
	return nil
}
