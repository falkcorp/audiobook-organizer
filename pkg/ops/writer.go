// file: pkg/ops/writer.go
// version: 1.1.0
// guid: dc4d264d-6ac3-4ecd-bd11-247d5300dd3c
// last-edited: 2026-10-13

package ops

import (
	"context"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// Writer is the only write path an op gets (internal/operations/opswriter
// implements it). Every method, in order:
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
