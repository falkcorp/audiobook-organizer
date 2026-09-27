// file: internal/repairs/fixer.go
// version: 1.0.0
// guid: 3e8b1c52-7a4d-4f19-9c06-5d2e8a7f1b34
// last-edited: 2026-09-27

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

// Row is one unit a fixer proposes to change. Rows are what the plan op
// stores, the rows endpoint pages and the apply op selects by RowID.
type Row struct {
	// RowID is stable across plans of the same state (a version group id, a
	// book id), so a selection made on one plan names the same thing in a
	// re-plan.
	RowID string `json:"row_id"`
	// BookIDs are every book the row reads or may write. The framework's
	// guards run over all of them, so a fixer lists the whole unit (every
	// member of a group, not just the ones it changes).
	BookIDs []string `json:"book_ids"`
	Title   string   `json:"title"`
	Author  string   `json:"author,omitempty"`
	// Current / Proposed are small display maps: the values the row has now
	// and the values apply would leave. Keys are fixer-defined.
	Current  map[string]string `json:"current,omitempty"`
	Proposed map[string]string `json:"proposed,omitempty"`
	Reason   string            `json:"reason"`
	Risk     string            `json:"risk"`
	// Fingerprint hashes every input the row's decision was made from. Apply
	// re-plans the row and refuses it when the fresh fingerprint differs.
	Fingerprint string `json:"fingerprint"`
	// Skipped is set on a row apply will never write: a framework guard
	// (SkipITunes, SkipOwnerManual) or the fixer's own hold/refusal kind.
	// SkipReason says why in words.
	Skipped    string `json:"skipped,omitempty"`
	SkipReason string `json:"skip_reason,omitempty"`
	// Detail is fixer-private state carried from Replan to Apply inside one
	// apply run. It is never persisted: the stored plan holds only the
	// fields above, and Apply always receives a row fresh from Replan.
	Detail any `json:"-"`
}

// Applicable reports whether apply may write this row.
func (r *Row) Applicable() bool { return r.Skipped == "" }

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
	Plan(ctx context.Context, params json.RawMessage) ([]Row, error)
	// Replan re-reads the state of one planned row and returns the row as a
	// plan made now would produce it (same RowID). An error fails that row,
	// not the run.
	Replan(ctx context.Context, params json.RawMessage, planned Row) (Row, error)
	// Apply writes one fresh row, through w only. Returning
	// ErrChangedSincePlan (wrapped or not) marks the row changed_since_plan.
	Apply(ctx context.Context, w *Writer, fresh Row) error
}
