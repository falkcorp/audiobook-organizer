// file: internal/repairs/writer_credits.go
// version: 1.2.0
// guid: 2d552bb6-33a3-4168-9c64-c78bec530ec4
// last-edited: 2026-10-01

package repairs

import (
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// A fixer that moves a book's AUTHOR CREDITS (the book_authors junction and
// the primary AuthorID) needs more than Writer.Modify: metadata history holds
// book fields, not the junction, so "undo last apply" cannot put a credit
// back. Every credit write is therefore journaled through the Writer's op
// journal (WithJournal, writer_files.go) under the apply op's id, in a change
// type the op revert (POST /operations/:id/revert) reverses. Being the same
// journal, a credit row gets its resume dedupe, and marks the book so every
// history batch the apply writes for it carries apply_op_journaled.
//
// JOURNAL FIRST, and INSIDE THE LOCK. The row is written inside the
// ModifyBookAuthors callback, from the junction that callback was handed, so
// the "previous" value it records is the one the write replaces -- not a read
// taken before the lock, which a concurrent writer could have made stale (the
// ledger-before-write bug class). A crash between the row and the write
// leaves a row whose credits the book still has; its revert is a no-op or a
// compare-and-set refusal, never an overwrite. A journal failure returns
// ErrNotJournaled and nothing is written.

// CreditStore is the book-credit write primitive.
type CreditStore interface {
	ModifyBookAuthors(bookID string, fn func([]database.BookAuthor) ([]database.BookAuthor, error)) ([]database.BookAuthor, error)
}

// UndoEntry is one operation change row a credit write journals.
type UndoEntry struct {
	ChangeType, Field, Old, New string
}

// WithCredits wires w to write book credits through store. The rows are
// journaled through WithJournal's op journal; without it ModifyCredits and
// RecordChange fail with ErrNotJournaled.
func (w *Writer) WithCredits(store CreditStore) *Writer {
	w.credits = store
	return w
}

// RecordChange journals one undo row under the apply op's id outside a
// credit write (an author row just created, a series link about to be
// written). A failure is ErrNotJournaled: do not make the write.
func (w *Writer) RecordChange(bookID string, e UndoEntry) error {
	return w.Journal(bookID, e.ChangeType, e.Field, e.Old, e.New)
}

// ModifyCredits runs fn on bookID's credit junction under the book's author
// lock. fn returns the next junction and the undo row describing the change;
// the row is journaled BEFORE the junction write commits, and a journal
// failure aborts the write. fn returning database.ErrSkipBookAuthorsWrite
// journals and writes nothing; any other error from fn is returned as is
// (wrap ErrChangedSincePlan for a compare-and-set refusal).
func (w *Writer) ModifyCredits(bookID string, fn func(cur []database.BookAuthor) ([]database.BookAuthor, UndoEntry, error)) ([]database.BookAuthor, error) {
	if w.credits == nil {
		return nil, errors.New("repairs: writer has no credit store")
	}
	// Beat before the author lock is taken (the journal row inside fn beats
	// again under it).
	if err := w.beat("credits of book " + bookID); err != nil {
		return nil, err
	}
	skipped := false
	next, err := w.credits.ModifyBookAuthors(bookID, func(cur []database.BookAuthor) ([]database.BookAuthor, error) {
		out, entry, ferr := fn(cur)
		if ferr != nil {
			skipped = errors.Is(ferr, database.ErrSkipBookAuthorsWrite)
			return nil, ferr
		}
		if jerr := w.RecordChange(bookID, entry); jerr != nil {
			return nil, jerr
		}
		return out, nil
	})
	if err != nil {
		return nil, err
	}
	if !skipped {
		w.writes.Add(1)
	}
	return next, nil
}

// SetPrimaryAuthor rewrites bookID's primary AuthorID from `from` to `to`
// (nil clears it), only while it still names from. It records the
// author_id change in metadata history for the book's history view, marked
// apply_op_journaled: the credit junction moved with it lives only in the op
// journal, so "undo last apply" must refuse this batch rather than put the
// primary back alone. A book this writer journaled nothing for (a caller
// without the credit move, or no journal wired) is marked apply_incomplete,
// which undo-last-apply refuses too. Returns ErrChangedSincePlan when the
// primary no longer names from.
func (w *Writer) SetPrimaryAuthor(bookID string, from int, to *database.Author) error {
	if err := w.beat("primary author of book " + bookID); err != nil {
		return err
	}
	var before, after *int
	written, err := w.store.ModifyBook(bookID, func(b *database.Book) error {
		if b.AuthorID == nil || *b.AuthorID != from {
			return fmt.Errorf("%w: book %s primary author is no longer %d", ErrChangedSincePlan, bookID, from)
		}
		was := from
		before = &was
		b.AuthorID, b.Author = nil, nil
		after = nil
		if to != nil {
			id := to.ID
			a := *to
			b.AuthorID, b.Author = &id, &a
			after = &id
		}
		return nil
	})
	if err != nil {
		return err
	}
	if written == nil {
		return fmt.Errorf("%w: book %s vanished", ErrChangedSincePlan, bookID)
	}
	w.writes.Add(1)
	batchID := w.batchPrefix + ulid.Make().String()
	now := time.Now()
	oldJSON, newJSON := jsonString(intString(before)), jsonString(intString(after))
	if herr := w.history.RecordMetadataChange(&database.MetadataChangeRecord{
		BookID: bookID, Field: "author_id", PreviousValue: &oldJSON, NewValue: &newJSON,
		ChangeType: w.changeType, Source: w.source, ChangedAt: now, BatchID: batchID,
	}); herr != nil {
		w.historyFailed.Add(1)
		w.log.Warn("%s: history row not recorded (the write itself committed): book_id=%s field=author_id err=%s",
			w.source, logger.SanitizeLogValue(bookID), logger.SanitizeLogValue(herr.Error()))
	} else {
		w.historyRows.Add(1)
	}
	marker := ChangeTypeApplyIncomplete
	if w.journaledBook(bookID) {
		marker = ChangeTypeApplyOpJournaled
	}
	if merr := w.history.RecordMetadataChange(&database.MetadataChangeRecord{
		BookID: bookID, Field: "apply", ChangeType: marker,
		Source: w.source, ChangedAt: now, BatchID: batchID,
	}); merr != nil {
		w.log.Error("%s: undo-last-apply marker not recorded: book_id=%s batch_id=%s err=%s",
			w.source, logger.SanitizeLogValue(bookID), batchID, logger.SanitizeLogValue(merr.Error()))
	}
	return nil
}

func intString(p *int) string {
	if p == nil {
		return ""
	}
	return strconv.Itoa(*p)
}
