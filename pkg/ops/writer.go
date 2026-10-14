// file: pkg/ops/writer.go
// version: 1.5.1
// guid: dc4d264d-6ac3-4ecd-bd11-247d5300dd3c
// last-edited: 2026-10-14

package ops

import (
	"context"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// Writer is the only write path an op gets (internal/operations/opswriter
// implements it). Every write method, in order:
//  1. checks the run's fence (canceled, abandoned or lost stand-down lease
//     -> ErrFenced);
//  2. in Live mode writes an INTENT row before touching the store; in
//     Preview records the intent as a plan line and returns ErrPreview;
//  3. performs the write inside the store's critical section, capturing the
//     previous value there, never from the caller's earlier snapshot;
//  4. writes the HISTORY row only after the write committed;
//  5. clears the intent.
//
// A crash between 2 and 5 leaves an intent: recovery lists that subject as
// "may be partially applied".
//
// MarkRowApplied and RowApplied are a Fixer apply's bookkeeping, not
// writes to the library: they take no intent and record no history, and
// only the Fixer's applyRow calls them. Every write method also reports the
// history it recorded on the item (NoteHistory), so the row's marker can
// name it.
//
// ctx, on every method, must be the item's context (ItemRC.Context()) or one
// derived from it, from inside the item, before the item returns. A write
// the Writer refuses because the run is fenced or lost its lease is then
// flagged on that item's outcome, so its chunk is not recorded done even if
// the item swallows the error. A refused write on any other context (the
// run's RC, context.Background(), a goroutine that outlived its item) cannot
// be pinned on a chunk, so it fails the whole run closed instead.
//
// There is deliberately no delete method (standing rule: never delete
// book_file rows; repoint them). ModifyCredits, ModifyTags and LockFields of
// the first sketch are not here yet: the store has no atomic modify for them
// (todo.d follow-ups).
type Writer interface {
	// ModifyBook runs fn on the book row inside the store's critical
	// section. ctx: the item's context or derived from it (see above).
	ModifyBook(ctx context.Context, bookID string, fn func(*database.Book) error) (Changed, error)
	// ModifyBookFile runs fn on one book_file row inside the store's
	// critical section. ctx: the item's context or derived from it.
	ModifyBookFile(ctx context.Context, bookID, fileID string, fn func(*database.BookFile) error) (Changed, error)
	// RepointBookFile points a book_file row at to while it still holds
	// expect (compare-and-set; nothing on disk moves). ctx: the item's
	// context or derived from it.
	RepointBookFile(ctx context.Context, bookID, fileID string, expect, to database.BookFileLocation) error
	// ModifyBookAuthors runs fn on a book's credit list inside the store's
	// critical section. ctx: the item's context or derived from it.
	ModifyBookAuthors(ctx context.Context, bookID string, fn func([]database.BookAuthor) ([]database.BookAuthor, error)) (Changed, error)
	// Effect wraps a write the typed surface does not cover (a file
	// reflink, a settings key, an index rebuild): the Writer applies steps
	// 1, 2 and 5 around fn and records the history rows fn returns after it
	// returns. ctx: the item's context or derived from it.
	Effect(ctx context.Context, e EffectSpec, fn func() ([]HistoryRow, error)) error
	// MarkRowApplied records, under the run's op id, that the Fixer row
	// rowID's Apply returned nil, naming the history the writes Apply made
	// with ctx recorded (NoteHistory on ctx's item). It is the evidence
	// RowApplied reads on a resume. Gated like a write (a fenced run or a
	// canceled ctx records nothing); in Preview it records nothing. It
	// returns nothing: the row's writes already committed, so a marker
	// that cannot be recorded is logged and costs only that evidence -- a
	// resume then skips the row as changed since plan, and never writes it
	// twice. ctx: the item's context, after Apply returned.
	MarkRowApplied(ctx context.Context, rowID string)
	// RowApplied reports whether some attempt of this run marked the row
	// rowID applied (MarkRowApplied) and all of the history that marker
	// names still stands: no operation journal row of it voided or
	// reverted, no metadata history batch of it missing or undone, no
	// book_file it wrote moved off (or deleted from) its book, and no
	// write it names followed, on what it wrote, by a later write of its
	// book recorded in either store (metadata history or the operation
	// journal), whatever wrote it and whatever it is named there; a name
	// the check does not know counts as a later write to everything.
	// Evidence is per row: another row's writes, even to the same book,
	// never count. A Fixer's resumed apply requires it before it
	// reports a row as applied by an earlier attempt (applyRow). False on
	// a Preview Writer and when the store keeps no markers -- never an
	// error for those; a failed read is an error. ctx: the item's context
	// or derived from it; a canceled context or a revoked fence is an
	// error.
	RowApplied(ctx context.Context, rowID string) (bool, error)
}

// Changed lists the tracked fields a write changed, in
// database.TrackedBookFields order for a book.
type Changed struct{ Fields []string }

// Any reports whether anything changed.
func (c Changed) Any() bool { return len(c.Fields) > 0 }

// EffectSpec names an Effect write and its subject.
type EffectSpec struct {
	Name     string
	Subject  Subject
	Resource Resource
}

// HistoryRow is one recorded change. BookID is the book it belongs to.
type HistoryRow struct{ BookID, Field, Old, New string }

// SubjectType is the kind of thing a Subject names.
type SubjectType string

// Subject types.
const (
	SubjectBook     SubjectType = "book"
	SubjectBookFile SubjectType = "book_file"
)

// Subject is one book or book file a row reads or may write.
type Subject struct {
	Type SubjectType
	ID   string
}

// Books returns book subjects.
func Books(ids ...string) []Subject { return subjects(SubjectBook, ids) }

// BookFiles returns book_file subjects.
func BookFiles(ids ...string) []Subject { return subjects(SubjectBookFile, ids) }

func subjects(t SubjectType, ids []string) []Subject {
	out := make([]Subject, len(ids))
	for i, id := range ids {
		out[i] = Subject{Type: t, ID: id}
	}
	return out
}

// Risk grades a Fixer row.
type Risk int

// Risks.
const (
	RiskLow Risk = iota + 1
	RiskMedium
	RiskHigh
)
