// file: internal/undo/author_rename.go
// version: 1.0.0
// guid: 5e2c8a14-7b39-4f60-9d1a-8c3e6f0b2d75
// last-edited: 2026-10-15

package undo

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// ChangeTypeAuthorRename: an author row renamed in place (author-conjunction-
// repair's "& Name" row with no twin). OldValue and NewValue are AuthorName
// JSON: the author and its name before and after. The revert renames it back,
// only while it still carries exactly the name the rename wrote.
const ChangeTypeAuthorRename = "author_rename"

// ChangeTypeAuthorIDRepoint: a book's primary AuthorID repointed off an id that
// names no author (author-id-repair phase a). OldValue and NewValue are the ids
// before and after. The revert puts the old id back, only while the book still
// carries exactly the new one. A metadata_update row cannot carry this:
// "author_id" is deliberately not a revertable book field (see
// revertableBookFields), because other ops' author_id rows record a scalar
// write whose credit is not journaled with it.
const ChangeTypeAuthorIDRepoint = "author_id_repoint"

// AuthorName is one side of an author_rename row.
type AuthorName struct {
	AuthorID int    `json:"author_id"`
	Name     string `json:"name"`
}

// NewAuthorRenameRow builds the row for renaming authorID from to to.
func NewAuthorRenameRow(authorID int, from, to string) (*database.OperationChange, error) {
	old, err := json.Marshal(AuthorName{AuthorID: authorID, Name: from})
	if err != nil {
		return nil, err
	}
	nv, err := json.Marshal(AuthorName{AuthorID: authorID, Name: to})
	if err != nil {
		return nil, err
	}
	return &database.OperationChange{ChangeType: ChangeTypeAuthorRename, FieldName: "author_name",
		OldValue: string(old), NewValue: string(nv)}, nil
}

// DecodeAuthorRename reads an author_rename row.
func DecodeAuthorRename(c *database.OperationChange) (authorID int, from, to string, err error) {
	var o, n AuthorName
	if err := json.Unmarshal([]byte(c.OldValue), &o); err != nil {
		return 0, "", "", refuse(ReasonOldValueUnparsable, "author rename row: %v", err)
	}
	if err := json.Unmarshal([]byte(c.NewValue), &n); err != nil {
		return 0, "", "", refuse(ReasonOldValueUnparsable, "author rename row: %v", err)
	}
	if o.AuthorID <= 0 || o.AuthorID != n.AuthorID || o.Name == "" {
		return 0, "", "", refuse(ReasonOldValueUnparsable, "author rename row names no author or no old name")
	}
	return o.AuthorID, o.Name, n.Name, nil
}

// CheckAuthorRename is the revert's compare-and-set: the author must be live
// under its own id (GetAuthorByID follows a redirect, so another id means it
// was merged away) and still carry exactly to. Already back at from is
// ErrAlreadyRestored.
func CheckAuthorRename(cur *database.Author, authorID int, from, to string) error {
	if cur == nil || cur.ID != authorID {
		return refuse(ReasonChangedSince, "author %d was deleted or merged since the rename", authorID)
	}
	switch cur.Name {
	case to:
		return nil
	case from:
		return ErrAlreadyRestored
	}
	return refuse(ReasonChangedSince, "author %d was renamed since (now %q)", authorID, cur.Name)
}

// NewAuthorIDRepointRow builds the row for repointing bookID's primary from
// from to to.
func NewAuthorIDRepointRow(bookID string, from, to int) *database.OperationChange {
	return &database.OperationChange{BookID: bookID, ChangeType: ChangeTypeAuthorIDRepoint, FieldName: "author_id",
		OldValue: strconv.Itoa(from), NewValue: strconv.Itoa(to)}
}

// DecodeAuthorIDRepoint reads an author_id_repoint row.
func DecodeAuthorIDRepoint(c *database.OperationChange) (from, to int, err error) {
	from, ferr := strconv.Atoi(c.OldValue)
	to, terr := strconv.Atoi(c.NewValue)
	if ferr != nil || terr != nil || c.BookID == "" {
		return 0, 0, refuse(ReasonOldValueUnparsable, "author id repoint row: old %q new %q book %q", c.OldValue, c.NewValue, c.BookID)
	}
	return from, to, nil
}

// CheckAuthorIDRepoint is the revert's compare-and-set on the book's primary:
// it must still be exactly to. Already back at from is ErrAlreadyRestored.
func CheckAuthorIDRepoint(bookID string, current *int, from, to int) error {
	switch {
	case current != nil && *current == to:
		return nil
	case current != nil && *current == from:
		return ErrAlreadyRestored
	}
	return refuse(ReasonChangedSince, "book %s primary author changed since the author id repair", bookID)
}

// String forms used by NotRestorableLabel.
func authorRenameLabel(c *database.OperationChange) string {
	if _, _, _, err := DecodeAuthorRename(c); err != nil {
		return fmt.Sprintf("%s:(unparsable)", ChangeTypeAuthorRename)
	}
	return ""
}

func authorIDRepointLabel(c *database.OperationChange) string {
	if _, _, err := DecodeAuthorIDRepoint(c); err != nil {
		return fmt.Sprintf("%s:(unparsable)", ChangeTypeAuthorIDRepoint)
	}
	return ""
}
