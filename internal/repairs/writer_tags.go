// file: internal/repairs/writer_tags.go
// version: 1.0.0
// guid: 637b4ad2-17cd-41f7-9b6f-361a057071bf
// last-edited: 2026-10-04

package repairs

import (
	"errors"
	"fmt"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// A fixer that tags books (maintenance.tag-franchise) writes book_tag rows,
// which metadata history does not hold, so every tag it adds is journaled
// through the Writer's op journal as undo.ChangeTypeBookTagAdd under the
// apply op's id: the op revert (POST /operations/:id/revert) removes the tag
// again, while it still carries the source the apply wrote.
//
// Tags are database rows only. Nothing writes a book tag into an audio file
// or the iTunes library: the organizer, write-back and metadata apply read
// book tags only for policy:* tags (policy.EvaluatePolicy), and the search
// index reads them for search.
//
// A tag the book already carries, from any source, is left alone and not
// journaled: re-adding it would re-source a person's tag to the fixer, and
// the revert would then delete it. The store has no compare-and-set for
// tags, so a person adding the same tag between the read and the write
// leaves it with this fixer's source; the revert of that row removes it.
// The window is one point read wide.

// TagStore is the book_tag surface Writer offers a fixer. It has no remove
// method: an apply only adds tags (the revert removes them).
type TagStore interface {
	GetBookTagsDetailed(bookID string) ([]database.BookTag, error)
	AddBookTagWithSource(bookID, tag, source string) error
}

// WithTags wires w to write book tags through store. Without WithJournal,
// AddBookTag fails with ErrNotJournaled.
func (w *Writer) WithTags(store TagStore) *Writer {
	w.tags = store
	return w
}

// AddBookTag adds tag to bookID with source, journaled first. It reports
// whether it wrote: false, nil when the book already carries the tag from
// any source.
func (w *Writer) AddBookTag(bookID, tag, source string) (bool, error) {
	if w.tags == nil {
		return false, errors.New("repairs: writer has no tag store")
	}
	tag = strings.ToLower(strings.TrimSpace(tag))
	if tag == "" || source == "" {
		return false, fmt.Errorf("repairs: tag and source are required (book %s)", bookID)
	}
	if err := w.beat("tags of book " + bookID); err != nil {
		return false, err
	}
	cur, err := w.tags.GetBookTagsDetailed(bookID)
	if err != nil {
		return false, fmt.Errorf("read tags of %s: %w", bookID, err)
	}
	for _, t := range cur {
		if strings.EqualFold(strings.TrimSpace(t.Tag), tag) {
			return false, nil
		}
	}
	err = w.JournalStep(bookID, UndoEntry{ChangeType: undo.ChangeTypeBookTagAdd, Field: tag, Old: undo.BookTagAbsent, New: source},
		func() error { return w.tags.AddBookTagWithSource(bookID, tag, source) })
	if err != nil {
		return false, err
	}
	w.writes.Add(1)
	return true, nil
}
