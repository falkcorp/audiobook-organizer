// file: internal/plugins/maintenance/retired_versions_session.go
// version: 1.0.1
// guid: f6833d27-8720-4289-8af3-fe4fcbe08f83
// last-edited: 2026-10-28

package maintenance

import (
	"context"
	"fmt"

	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
)

// One apply run's state for the retired-versions fixer (repairs.ApplyScoped).
//
// WHY. Every row of an apply is classified twice: by Replan (outside any lock)
// and again by Apply under the process-wide merge lock. Each classification
// used to build a fresh run, and a fresh run read every series (the
// owner-manual check), every user (the listening-state probe) and, for a book
// that still has files, every combine journal. On a 51,000-book trash that is
// whole-library reads per row, and the Apply ones were made while holding the
// merge lock every other writer in the process waits on.
//
// WHAT IS SHARED, AND WHY THAT IS SAFE. The session holds only what no row of
// the run changes and whose staleness cannot make a retire wrong:
//   - the series names and the user listing: both feed guards that are
//     re-read per BOOK on every row (the book's own series id, tags, title and
//     path; each user's state for that book), and a series or user created
//     during one run is not one the planned books can already belong to;
//   - the index of unreversed combine journals: read once, before the first
//     row. A combine journaled during the run is not seen by it, so a book it
//     absorbs is retired as a plain shell. That is not a loss: the combine
//     undo path refuses an absorbed book that is a retired shell
//     (database.RefuseRetiredShell), so undoing the combine can neither revive
//     the retired book nor strand it, and the next run holds it. Everything a
//     row's retire depends on for ITS OWN books (state in the trash, files, the
//     holder, listening state, the path owners) is re-read under the lock by
//     Apply's re-check, as before.
//
// All of it is read in BeginApply, before the first row, so none of it is ever
// read while the merge lock is held. A plan (no session) reads it once for its
// one run, as it always did.
//
// THE FLAG. retire_versions_apply_enabled is read once, in BeginApply, and
// Replan and Apply use that value for the run. The engine reads it itself
// before BeginApply (PlanOnly, NoScanStandDown); a flip between those reads
// and this one makes the run's Writer refuse (ErrNoWritesWriter) or Apply
// refuse, and both are reported retry_later (nothing written, apply again),
// never failed.

// retiredUndo is the index of unreversed combine journals (or why it could not
// be read), read once per apply run.
type retiredUndo struct {
	idx map[string][]string
	err error
}

type retiredSession struct {
	applyOn bool
	shared  *retiredShared
	undo    retiredUndo
	// err is why the run's state could not be read; every row of the run then
	// fails closed with it (as a failed read does in a plan).
	err error
}

type retiredSessionKey struct{}

func retiredSessionFrom(ctx context.Context) *retiredSession {
	s, _ := ctx.Value(retiredSessionKey{}).(*retiredSession)
	return s
}

var _ repairs.ApplyScoped = (*retiredVersionsFixer)(nil)

// BeginApply implements repairs.ApplyScoped.
func (f *retiredVersionsFixer) BeginApply(ctx context.Context, _ bool) (context.Context, func()) {
	s := &retiredSession{applyOn: f.applyOn()}
	if s.shared, s.err = f.loadShared(ctx); s.err == nil {
		s.undo.idx, s.undo.err = merge.UnreversedCombineShells(s.shared.journal)
		if s.undo.err != nil {
			s.undo.err = fmt.Errorf("%w: %w", errRetiredUndoRead, s.undo.err)
		}
	}
	return context.WithValue(ctx, retiredSessionKey{}, s), func() {}
}

// applyOnFor is the apply flag for the run ctx belongs to: the value read when
// the run began, or the live value outside a run.
func (f *retiredVersionsFixer) applyOnFor(ctx context.Context) bool {
	if s := retiredSessionFrom(ctx); s != nil {
		return s.applyOn
	}
	return f.applyOn()
}

// retiredFlagFlipped is the error for a row that finds the apply path closed
// although the engine let the run start: not settled, nothing written.
func retiredFlagFlipped(rowID string) error {
	return fmt.Errorf("%w: %w (row %s); apply again once the flag is on", repairs.ErrRetryLater, errRetiredApplyDisabled, rowID)
}
