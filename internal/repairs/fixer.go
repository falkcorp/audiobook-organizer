// file: internal/repairs/fixer.go
// version: 1.7.0
// guid: 3e8b1c52-7a4d-4f19-9c06-5d2e8a7f1b34
// last-edited: 2026-10-07

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
	// Class is an optional fixer-defined kind of row ("moved", "copy", ...).
	// The plan tallies rows by it (PlanResult.ByClass) and the rows endpoint
	// filters on it, so every per-class count in the lane lists its rows.
	Class string `json:"class,omitempty"`
	// Members optionally describes every book of the row (its role in the
	// row and its file counts), so the lane can list and link each of them
	// rather than show a bare "N books" count.
	Members []RowMember `json:"members,omitempty"`
	// Evidence optionally lists what the row's decision was made from, in
	// words ("import path equals parent row path", "hash equal").
	Evidence []string `json:"evidence,omitempty"`
	// Fingerprint hashes every input the row's decision was made from. Apply
	// re-plans the row and refuses it when the fresh fingerprint differs.
	Fingerprint string `json:"fingerprint"`
	// Skipped is set on a row apply will never write: a framework guard
	// (SkipITunes, SkipOwnerManual) or the fixer's own hold/refusal kind.
	// SkipReason says why in words.
	Skipped    string `json:"skipped,omitempty"`
	SkipReason string `json:"skip_reason,omitempty"`
	// RetryLater marks a skip (Skipped set) that is transient: a condition
	// outside the row that clears on its own, such as an index not built yet,
	// not anything about the row itself. A re-plan returning such a row is
	// reported retry_later (OutcomeRetryLater, not settled), not
	// changed_since_plan, so the same plan can be applied again once the
	// condition clears -- but only when its RetryFingerprint equals the
	// plan's. The re-plan-time counterpart of ErrRetryLater.
	RetryLater bool `json:"retry_later,omitempty"`
	// RetryFingerprint, on a RetryLater row, is the fingerprint the row would
	// have once the transient condition clears with nothing else changed:
	// the fixer computes it from the same inputs as Fingerprint, leaving out
	// only the transient hold. The engine reports retry_later only when it
	// equals the planned row's Fingerprint; otherwise some other input
	// changed too and the row is changed_since_plan. Empty (a fixer that sets
	// RetryLater without it) never matches, so such a row is
	// changed_since_plan: fail closed.
	RetryFingerprint string `json:"retry_fingerprint,omitempty"`
	// OwnerApplicable marks a skipped row the fixer decided, at plan time,
	// the owner may apply himself by clicking Apply on it (owner.go). It
	// stays skipped: no bulk selection, scheduled run or plain apply writes
	// it; only an apply holding a consumed owner grant naming it does.
	// OwnerApplyReason says why the row qualifies and what that apply writes.
	OwnerApplicable  bool   `json:"owner_applicable,omitempty"`
	OwnerApplyReason string `json:"owner_apply_reason,omitempty"`
	// OwnerWrites are the books an owner apply of the row writes (the rest
	// of BookIDs are only read). The engine journals the owner's audit note
	// on these, so a book the row only reads (a parent) gets none.
	OwnerWrites []string `json:"owner_writes,omitempty"`
	// State is fixer-private state stored WITH the plan: what Replan needs
	// from plan time that the row as it is now cannot tell it (the ids of the
	// rows a group was planned over, the values a compare-and-set must find).
	// Replan receives it back on the planned row. The framework never reads
	// it.
	State json.RawMessage `json:"state,omitempty"`
	// Detail is fixer-private state carried from Replan to Apply inside one
	// apply run. It is never persisted: the stored plan holds only the
	// fields above, and Apply always receives a row fresh from Replan.
	Detail any `json:"-"`
}

// RowMember is one book of a Row, for display.
type RowMember struct {
	BookID string `json:"book_id"`
	Title  string `json:"title,omitempty"`
	// Role is fixer-defined ("parent", "fragment", "survivor").
	Role string `json:"role,omitempty"`
	// Files / MissingFiles count the book's book_file rows at plan time.
	Files        int `json:"files"`
	MissingFiles int `json:"missing_files,omitempty"`
}

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
