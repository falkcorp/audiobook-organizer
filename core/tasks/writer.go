// file: core/tasks/writer.go
// version: 1.1.0
// guid: 19b8f153-7be6-4fe6-970d-3ad665c17db8
// last-edited: 2026-11-19

package tasks

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

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

// Write names one gated write: one call to the module's store, however many
// resources that call can touch.
//
// A call whose method writes several resources (a book write also stamps the
// metadata cache and stages the identity-index rows) names all of them: Resource
// and Resources together are the write's stamps (Stamps), and the writer
// admits the write only when the definition's Effects.Writes declare every one.
// One Do per resource is not an option: a method is one store call, and
// splitting it would record it as several writes with a fence able to land
// between them.
type Write struct {
	Name      string       // "books.modify_book"
	Resource  entity.Ref   // one stamp, inside the def's Effects.Writes
	Resources []entity.Ref `json:",omitempty"` // more stamps; see Stamps
	Subject   entity.Ref   // the entity written (ID set)
}

// Stamps is every resource the write names: Resource when it is set, then each
// entry of Resources in order, with repeats dropped. An entry of Resources is
// kept as given, a zero ref included, so a malformed stamp is refused by
// CheckWrite rather than skipped; only an unset Resource is left out (it is
// the single-stamp shorthand, and a write that names its stamps in Resources
// leaves it zero).
func (w Write) Stamps() []entity.Ref {
	out := make([]entity.Ref, 0, 1+len(w.Resources))
	if w.Resource != (entity.Ref{}) {
		out = append(out, w.Resource)
	}
	for _, r := range w.Resources {
		if !slices.Contains(out, r) {
			out = append(out, r)
		}
	}
	return out
}

// Covers reports whether the declared write set covers r: a declared ref with
// no ID covers every ref of its module and kind, one with an ID covers that
// ID. This is the one declared-set predicate: the executor's start-time subject
// check and the run-time fence (CheckWrite) both use it.
func Covers(declared []entity.Ref, r entity.Ref) bool {
	for _, d := range declared {
		if d.Module == r.Module && d.Kind == r.Kind && (d.ID == "" || d.ID == r.ID) {
			return true
		}
	}
	return false
}

// CheckWrite is the run-time write fence's predicate: nil when the write names
// at least one resource and the declared write set covers every one,
// ErrOutsideEffects (wrapped, naming the kind and every refused stamp)
// otherwise. A write that names no resource is refused too: the fence fails
// closed on what it cannot see. The executor's writer calls it before the
// write-ahead intent and before fn, in Preview as in Live; a test double that
// stands in for the run's writer calls the same function, so no copy of the
// rule exists to drift.
func CheckWrite(kind Kind, declared []entity.Ref, w Write) error {
	stamps := w.Stamps()
	if len(stamps) == 0 {
		return fmt.Errorf("%w: %s: write %q names no resource", ErrOutsideEffects, kind, w.Name)
	}
	var refused []string
	for _, s := range stamps {
		if !Covers(declared, s) {
			refused = append(refused, FormatRef(s))
		}
	}
	if len(refused) > 0 {
		return fmt.Errorf("%w: %s writes %s", ErrOutsideEffects, kind, strings.Join(refused, ", "))
	}
	return nil
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
	// ErrHoldLost: a stand-down hold's lease lapsed without renewal, or the hold was
	// released; Hold.Renew returns it and the run's next write is refused with
	// ErrFenced (5T.3.7).
	ErrHoldLost = errors.New("tasks: stand-down hold lost: its lease lapsed or it was released")
	// ErrPreview: preview, the intent is recorded and the write not performed.
	ErrPreview = errors.New("tasks: preview mode; write recorded, not performed")
	// ErrChangedSincePlan: a Fixer row re-evaluated differently at apply.
	ErrChangedSincePlan = errors.New("tasks: row changed since the plan; refused")
	// ErrOutsideEffects: a stamp of the Write (Resource or Resources) is not declared in
	// Effects.Writes, or the Write names none (fail-closed).
	ErrOutsideEffects = errors.New("tasks: write outside the definition's declared effects; refused")
)
