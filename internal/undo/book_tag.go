// file: internal/undo/book_tag.go
// version: 1.0.0
// guid: 7ea2eab6-8661-4291-9ed1-943738848c44
// last-edited: 2026-10-04

package undo

import (
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// ChangeTypeBookTagAdd: a repair added one book_tag row (FieldName is the
// tag, lower-case as stored). OldValue is BookTagAbsent (a repair only adds a
// tag the book did not carry), NewValue the source it wrote
// ("franchise-matcher").
//
// Restorable. The revert removes the tag while the book still carries it
// with exactly that source. A tag that is gone is already restored; one
// whose source changed since (a person claimed it) is theirs and refused.
const ChangeTypeBookTagAdd = "book_tag_add"

// BookTagAbsent is the OldValue of a ChangeTypeBookTagAdd row.
const BookTagAbsent = "absent"

// CheckBookTagAdd is the compare-and-set for a ChangeTypeBookTagAdd row,
// shared by the revert and the preflight: nil while the book carries the tag
// with the row's source (remove it), ErrAlreadyRestored when the tag is
// gone, and a ReasonChangedSince refusal when its source changed.
func CheckBookTagAdd(tags []database.BookTag, c *database.OperationChange) error {
	for _, t := range tags {
		if !strings.EqualFold(strings.TrimSpace(t.Tag), c.FieldName) {
			continue
		}
		if t.Source == c.NewValue {
			return nil
		}
		return refuse(ReasonChangedSince, "book %s tag %q now has source %q, not %q; it is someone else's", c.BookID, c.FieldName, t.Source, c.NewValue)
	}
	return ErrAlreadyRestored
}

func validBookTagAddRow(c *database.OperationChange) bool {
	return c.FieldName != "" && c.OldValue == BookTagAbsent && c.NewValue != ""
}

// bookTagReader is the optional preflight store surface for book tags.
type bookTagReader interface {
	GetBookTagsDetailed(bookID string) ([]database.BookTag, error)
}
