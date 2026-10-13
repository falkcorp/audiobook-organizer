// file: pkg/ops/writer.go
// version: 1.0.1
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
// There is deliberately no delete method (standing rule: never delete
// book_file rows; repoint them). ModifyCredits, ModifyTags and LockFields of
// the first sketch are not here yet: the store has no atomic modify for them
// (todo.d follow-ups).
type Writer interface {
	ModifyBook(ctx context.Context, bookID string, fn func(*database.Book) error) (Changed, error)
	ModifyBookFile(ctx context.Context, bookID, fileID string, fn func(*database.BookFile) error) (Changed, error)
	RepointBookFile(ctx context.Context, bookID, fileID string, expect, to database.BookFileLocation) error
	ModifyBookAuthors(ctx context.Context, bookID string, fn func([]database.BookAuthor) ([]database.BookAuthor, error)) (Changed, error)
	// Effect wraps a write the typed surface does not cover (a file
	// reflink, a settings key, an index rebuild): the Writer applies steps
	// 1, 2 and 5 around fn and records the history rows fn returns after it
	// returns.
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
