// file: internal/repairs/fixer.go
// version: 1.9.1
// guid: 3e8b1c52-7a4d-4f19-9c06-5d2e8a7f1b34
// last-edited: 2026-10-18

// Package repairs is the shared framework behind the Repairs lane of /review:
// one contract every library fixer implements, and one engine that plans,
// pages and applies them with the same guards.
//
// A fixer only decides WHAT a row is and HOW one row is written. The engine
// owns everything that must not differ between fixers:
//   - plans run as an operation (repairs.plan) and every row is stored in the
//     op result, so a large plan survives a restart and pages over HTTP;
//   - apply (repairs.apply) takes explicit row ids from a stored plan,
//     re-plans each selected row and refuses one whose fingerprint changed
//     (changed_since_plan);
//   - rows touching books/itunes/** or Doctor Who / Big Finish / Torchwood
//     are skipped at plan time and refused again, freshly read, at apply time;
//   - writes go through Writer, which records a metadata-history row for
//     every changed field and offers no delete primitive (no book_file row,
//     and no book, can be deleted through it);
//   - apply holds the library-scan stand-down instead of refusing while a
//     scan runs (owner ruling 2026-09-27), and hard-aborts on a lost lease.
//
// Design: .claude/notes/repairs-tab-plan-2026-09-27.md.
package repairs

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// Risk values of a Row.
const (
	// RiskLow: the change is mechanical and reversible from history.
	RiskLow = "low"
	// RiskReview: the owner should read the row before applying it.
	RiskReview = "review"
)

// ErrChangedSincePlan is returned (wrapped or bare) by a fixer's Apply when
// its own under-lock check finds the row no longer matches what was planned.
// The engine also produces this outcome itself when a re-plan's fingerprint
// differs from the stored plan's.
var ErrChangedSincePlan = errors.New("repairs: row changed since it was planned")

// ErrRetryLater is returned (wrapped) by a fixer's Apply that wrote nothing
// because a check it must make cannot be answered right now without slow
// work it may not do there (for the version twin fixer: memdb still warming
// up, so the record-hash check under the write lock would need a full scan).
// The row is reported retry_later, not changed_since_plan: nothing about the
// row changed, and the same plan can be applied again once the condition
// clears. It is checked before ErrChangedSincePlan.
var ErrRetryLater = errors.New("repairs: row cannot be checked right now; retry later")

// ErrPartiallyApplied is returned (wrapped) by a fixer's Apply that wrote
// part of a row before finding a change. It is checked before
// ErrChangedSincePlan, so the row reports partially_applied, not unchanged.
var ErrPartiallyApplied = errors.New("repairs: row partially applied")

// Applicable reports whether apply may write this row.
func (r Row) Applicable() bool { return r.Skipped == "" }

// Fixer is one library repair offered in the Repairs lane.
//
// Plan and Replan are read-only; they run during a library.scan with no
// stand-down. Apply is called only by the engine, for a row fresh from Replan
// whose fingerprint matched the stored plan, under the scan stand-down, with
// the framework guards already passed.
type Fixer interface {
	// ID is the stable id used in the URL and in the op params.
	ID() string
	Title() string
	Description() string
	// Plan returns every row of the whole-library plan, applicable and
	// skipped alike, with no cap: the rows endpoint pages them. params are
	// the fixer's own (the plan op passes them through).
	Plan(ctx context.Context, params json.RawMessage, reporter registry.Reporter) ([]Row, error)
	// Replan re-reads the state of one planned row and returns the row as a
	// plan made now would produce it (same RowID). An error fails that row,
	// not the run.
	Replan(ctx context.Context, params json.RawMessage, planned Row, reporter registry.Reporter) (Row, error)
	// Apply writes one fresh row, through w only. Returning
	// ErrChangedSincePlan (wrapped or not) marks the row changed_since_plan.
	Apply(ctx context.Context, w *Writer, fresh Row) error
}

// ApplyScoped is implemented by a fixer that keeps state for the length of
// one apply run (RunApply), such as one whole-library snapshot shared by
// every row's re-check instead of one listing per row. BeginApply is called
// once, before the first row, with whether the run is a dry run; the context
// it returns is the one every Replan and Apply of the run receives, and end
// is called when RunApply returns, so nothing the fixer hangs on that context
// outlives the run.
type ApplyScoped interface {
	BeginApply(ctx context.Context, dryRun bool) (scoped context.Context, end func())
}

// DefaultPermission is what starting a fixer's plan or apply requires when
// the fixer does not say otherwise (PermissionScoped): the lane's routes are
// library.edit_metadata.
const DefaultPermission = auth.PermLibraryEditMetadata

// PermissionScoped is implemented by a fixer whose writes need more than the
// lane's default permission (owner decision 2026-10-14). The HTTP routes that
// start its plan or apply check every permission it returns, on top of the
// route's own gate.
type PermissionScoped interface {
	RequiredPermissions() []auth.Permission
}

// RequiredPermissions is what starting f's plan or apply requires: f's own
// list when it is PermissionScoped and non-empty, else DefaultPermission.
func RequiredPermissions(f Fixer) []auth.Permission {
	if ps, ok := f.(PermissionScoped); ok {
		if perms := ps.RequiredPermissions(); len(perms) > 0 {
			return perms
		}
	}
	return []auth.Permission{DefaultPermission}
}
