// file: internal/books/writer/gate.go
// version: 1.0.1
// guid: 5b7d2e91-3c46-4f08-a1d9-80e6f2b43c17
// last-edited: 2026-11-19

// Package writer is the books module's gated write path (F5-18b): one call to
// the module's store becomes ONE tasks.Writer.Do carrying every resource the
// call can write, so the run-time write fence refuses an under-declared
// Effects when the write is attempted, not after the fact.
//
// The fence is a run-time check by owner decision (F5-18 prep split, section 6,
// question 1): there are no compile-time narrow writer types per kind. A kind's
// Effects, the method-to-resource table (internal/maintenance/resources) and
// the store's real key writes are three things that must agree, and the fence
// is what makes a disagreement an error at the write instead of a silent one.
//
// This package is dark: nothing calls it from a live path until F5-18 wires the
// kinds. Each group's writer (F5-18c) is built on Gate with its own view of the
// rows.
package writer

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/tasks"

	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// Module is the books module's name in a resource ref.
const Module = "books"

// Rows maps a store method (or a qualified method, "SetRaw/cover_text") to the
// resources one call of it can write. A method with no entry is unknown; an
// entry with no resources is a method that writes no Effects resource (see
// Gate.Do for what that lets through).
type Rows map[string][]ops.Resource

// ErrNoRow refuses a write whose method has no row: the gate cannot say what it
// writes, so it does not let it through. It is returned wrapped together with
// tasks.ErrOutsideEffects, since an unknown write is a write outside every
// declared effect.
var ErrNoRow = errors.New("books writer: method has no resource row")

// Gate is a run's books write path over its tasks.Writer. It is safe for
// concurrent use when the Writer is (the executor's is).
type Gate struct {
	w    tasks.Writer
	rows Rows
}

// New builds a Gate over the run's writer and a row table. The table is read,
// never copied: a caller passes its own view (F5-18c: one per group).
func New(w tasks.Writer, rows Rows) *Gate { return &Gate{w: w, rows: rows} }

// Stamp is the tasks.Write for one call of method on subject: the whole row,
// sorted so the recorded intent does not depend on the row's literal order.
// ok is false when the method has no row. A method with an empty row stamps no
// resource, and Do does not call Writer.Do for it; it asks the writer only
// whether the run may still write (tasks.CheckWritable).
func (g *Gate) Stamp(method string, subject entity.Ref) (w tasks.Write, ok bool) {
	row, ok := g.rows[method]
	if !ok {
		return tasks.Write{}, false
	}
	return tasks.Write{Name: Module + "." + method, Resources: refs(row), Subject: subject}, true
}

// Do performs one gated store call. It refuses with ErrNoRow (and
// tasks.ErrOutsideEffects) for a method with no row, without calling fn.
//
// A method whose row is empty writes no Effects resource: fn runs with no Preview
// check and no write-ahead intent, but NOT past a stopped run: the gate first
// asks the run's writer whether it may still write (tasks.CheckWritable: the
// executor's own permit and stand-down lease check, the one Do makes), and a
// refusal leaves fn uncalled. A writer with no such check (a test double, or
// the no-run writer of a ReadOnly kind) lets it through, as before. That is
// not "writes nothing". Of the seven empty rows of the shared table
// (internal/maintenance/resources), three write nothing themselves
// (InvalidateAuthorsCache drops an in-memory cache, NewUserStateProbe builds a
// probe, HardDeleteWithoutUserState wraps a closure whose writes are their own
// calls) and four do write: the system:backfill: sentinel that
// MarkBookAggregatesBackfillDone puts, and the op rows of ReplaceOpRowsPrefix,
// SaveClassLists and ClearClassLists. Those are ledger and store-maintained
// writes that belong to no Effects resource, so they carry no stamp (no intent,
// no epoch) and the CALLER owns the mode (for example
// internal/maintenance/jobs/recompute_book_aggregates.go checks the mode before
// it calls the store); the stopped-run refusal is the gate's. No
// metafetch.Store method has an empty row.
//
// Every other method is ONE Writer.Do stamped with the whole row; a refusal
// (fenced, preview, outside Effects) leaves fn uncalled.
func (g *Gate) Do(ctx context.Context, method string, subject entity.Ref, fn func(context.Context) ([]tasks.Change, error)) error {
	w, ok := g.Stamp(method, subject)
	if !ok {
		return fmt.Errorf("%w: %w: %s", tasks.ErrOutsideEffects, ErrNoRow, method)
	}
	if len(w.Resources) == 0 {
		// No resource to stamp: Writer.Do would refuse, so fn runs outside it, but
		// not past a stopped run. Liveness only: no intent and no epoch, which stay
		// row-only (F5-18 commit 1).
		if err := tasks.CheckWritable(g.w); err != nil {
			return err
		}
		_, err := fn(ctx)
		return err
	}
	return g.w.Do(ctx, w, fn)
}

// refs is the row as books resource refs, sorted and without repeats.
func refs(row []ops.Resource) []entity.Ref {
	out := make([]entity.Ref, 0, len(row))
	for _, r := range row {
		out = append(out, tasks.Res(Module, string(r)))
	}
	slices.SortFunc(out, func(a, b entity.Ref) int {
		switch {
		case a.Kind < b.Kind:
			return -1
		case a.Kind > b.Kind:
			return 1
		}
		return 0
	})
	return slices.Compact(out)
}
