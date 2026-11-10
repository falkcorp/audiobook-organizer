// file: core/tasks/writer.go
// version: 1.0.1
// guid: 19b8f153-7be6-4fe6-970d-3ad665c17db8
// last-edited: 2026-11-10

package tasks

import (
	"context"
	"errors"

	"github.com/falkcorp/mediacore/entity"
)

// Writer is the run's only write path. Modules wrap it in typed writers
// (books: ModifyBook, ModifyBookFile, RepointBookFile, ModifyBookAuthors —
// the v3 ops package's writer.go:46-92 methods: their contract interface moves to
// internal/books/contract and their implementation to internal/books/writer,
// the opswriter rename; 04 §2.4, §8.6; MOD6-12). Core names no app type (A3).
type Writer interface {
	// Do performs one gated write. It refuses with ErrFenced when the run is
	// canceled, abandoned, timed out or lost its stand-down lease; in Preview
	// it records the intent, does NOT call fn, and returns ErrPreview. In Live
	// it records a write-ahead intent, calls fn, then records the returned
	// changes to the module's ChangeSink (if any) and the run's evidence.
	Do(ctx context.Context, w Write, fn func(ctx context.Context) ([]Change, error)) error
	MarkRowApplied(ctx context.Context, rowID string)           // Fixer evidence (v3 semantics)
	RowApplied(ctx context.Context, rowID string) (bool, error) // via ChangeSink.Stands
}

// Write names one gated write.
type Write struct {
	Name     string     // "books.modify_book"
	Resource entity.Ref // must be inside the def's Effects.Writes
	Subject  entity.Ref // the entity written (ID set)
}

// Change is one field a write changed.
type Change struct {
	Subject  entity.Ref
	Field    string
	Old, New string
}

// ChangeSink is a module's journal (books: the operation-change journal and
// undo, raw/12 §8). Registered via Registrar; optional.
type ChangeSink interface {
	Record(ctx context.Context, run RunID, changes []Change) error
	// Stands reports whether every change a marker names is still in effect
	// (the v3 RowApplied evidence rule, the v3 ops package's writer.go:75-91).
	Stands(ctx context.Context, run RunID, rowID string) (bool, error)
}

var (
	// ErrFenced: write refused, the run no longer holds its permit.
	ErrFenced = errors.New("tasks: run is fenced (canceled, abandoned, timed out or lease lost); write refused")
	// ErrPreview: preview, the intent is recorded and the write not performed.
	ErrPreview = errors.New("tasks: preview mode; write recorded, not performed")
	// ErrChangedSincePlan: a Fixer row re-evaluated differently at apply.
	ErrChangedSincePlan = errors.New("tasks: row changed since the plan; refused")
	// ErrOutsideEffects: Write.Resource is not declared in Effects.Writes (fail-closed).
	ErrOutsideEffects = errors.New("tasks: write outside the definition's declared effects; refused")
)
