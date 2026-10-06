// file: internal/repairs/writer_tags.go
// version: 1.2.0
// guid: 637b4ad2-17cd-41f7-9b6f-361a057071bf
// last-edited: 2026-10-05

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

// OpID is the apply operation's id ("" without WithJournal): a fixer that
// stamps its writes with the run (franchise.RunSource) reads it here.
func (w *Writer) OpID() string { return w.opID }

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
	if w.noWrites.Load() {
		return false, fmt.Errorf("%w (AddBookTag)", ErrNoWritesWriter)
	}
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
	err = w.journalStep(bookID, UndoEntry{ChangeType: undo.ChangeTypeBookTagAdd, Field: tag, Old: undo.BookTagAbsent, New: source},
		func() error { return w.tags.AddBookTagWithSource(bookID, tag, source) })
	if err != nil {
		return false, err
	}
	w.writes.Add(1)
	return true, nil
}

// ErrTagsOnlyWriter is returned by every Writer method except the tag
// primitive when the apply's fixer declared BookTagsOnly: the framework guard
// was skipped for it on that promise, so the Writer enforces it.
var ErrTagsOnlyWriter = errors.New("repairs: this fixer is book-tags-only; the writer refuses any other write")

// restrictToTags makes every non-tag method of w refuse (ErrTagsOnlyWriter).
// RunApply sets it for a BookTagsOnly fixer before the first Apply.
func (w *Writer) restrictToTags() { w.tagsOnly.Store(true) }

// ErrNoWritesWriter is returned by every Writer write method when the apply's
// fixer declared NoScanStandDown: it runs without the scan stand-down on the
// promise that it writes no library row, so the Writer enforces it.
var ErrNoWritesWriter = errors.New("repairs: this fixer runs without the scan stand-down and may not write library rows; the writer refuses every write")

// restrictToNothing makes every write method of w refuse (ErrNoWritesWriter).
// RunApply sets it for a NoScanStandDown fixer before the first Apply.
func (w *Writer) restrictToNothing() { w.noWrites.Store(true) }

// denyTagsOnly refuses a non-tag write for a BookTagsOnly fixer, and any write
// for a NoScanStandDown one (the tag primitive checks the latter itself).
func (w *Writer) denyTagsOnly(method string) error {
	if w.noWrites.Load() {
		return fmt.Errorf("%w (%s)", ErrNoWritesWriter, method)
	}
	if w.tagsOnly.Load() {
		return fmt.Errorf("%w (%s)", ErrTagsOnlyWriter, method)
	}
	return nil
}
