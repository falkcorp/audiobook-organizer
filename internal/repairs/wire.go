// file: internal/repairs/wire.go
// version: 1.0.0
// guid: 4c7d2e91-5b0a-4e38-8f16-a93d6b1c0e57
// last-edited: 2026-10-18

package repairs

// This file holds every type the repairs endpoints serialise to the SPA, and
// nothing else. tygo generates web/src/types/generated/repairs.ts from it
// (tygo.yaml include_files), so a type that is not a wire shape (a Deps
// struct, an interface, a func type) must not be added here: it would land in
// the generated file with references the frontend cannot resolve.

import (
	"encoding/json"
	"time"
)

// PlanParams are the params of repairs.plan.
type PlanParams struct {
	FixerID string          `json:"fixer_id"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// PlanResult is the stored result of repairs.plan: every row, uncapped.
type PlanResult struct {
	FixerID    string          `json:"fixer_id"`
	Params     json.RawMessage `json:"params,omitempty"`
	PlannedAt  time.Time       `json:"planned_at"`
	Total      int             `json:"total"`
	Applicable int             `json:"applicable"`
	// SkippedByKind counts the rows apply will not write, by Row.Skipped.
	SkippedByKind map[string]int `json:"skipped_by_kind"`
	// ByClass counts every row (applicable and skipped) by Row.Class, for a
	// fixer that sets one. Each count is a filter of the rows endpoint.
	ByClass map[string]int `json:"by_class,omitempty"`
	// OwnerApplicable counts the skipped rows the owner may apply himself
	// (Row.OwnerApplicable); the owner_applicable filter pages them.
	OwnerApplicable int   `json:"owner_applicable,omitempty"`
	Rows            []Row `json:"rows"`
}

// RowsPage is one page of a stored plan.
type RowsPage struct {
	PlanOpID   string    `json:"plan_op_id"`
	FixerID    string    `json:"fixer_id"`
	PlannedAt  time.Time `json:"planned_at"`
	Filter     string    `json:"filter,omitempty"`
	Class      string    `json:"class,omitempty"`
	Offset     int       `json:"offset"`
	Limit      int       `json:"limit"`
	Total      int       `json:"total"` // rows matching the filter
	Applicable int       `json:"applicable"`
	// SkippedByKind is the whole plan's tally, whatever the filter.
	SkippedByKind map[string]int `json:"skipped_by_kind"`
	// OwnerApplicable is the whole plan's owner-row count, whatever the
	// filter.
	OwnerApplicable int `json:"owner_applicable,omitempty"`
	// ByClass is the whole plan's per-class tally, whatever the filter.
	ByClass map[string]int `json:"by_class,omitempty"`
	// ByClassInFilter tallies by class only the rows matching filter (class
	// aside): exactly the rows a class chip on this tab lists.
	ByClassInFilter map[string]int `json:"by_class_in_filter,omitempty"`
	// SkippedByKindInClass is the Skipped tally of the rows of Class,
	// whatever the filter: under a selected class the skip-kind chips count
	// exactly the rows "skipped:<kind>" pages. Set only when Class is (then
	// never nil, so an empty tally reads as {}, not as absent); null
	// without a class.
	SkippedByKindInClass map[string]int `json:"skipped_by_kind_in_class"`
	// ApplicableInClass counts the applicable rows of Class, whatever the
	// filter: under a selected class the Applicable tab counts exactly the
	// rows "applicable" pages. Set only when Class is (then present even at
	// zero).
	ApplicableInClass *int  `json:"applicable_in_class,omitempty"`
	Rows              []Row `json:"rows"`
}

// ApplyParams are the params of repairs.apply. Only an explicit
// dry_run:false writes (owner rule 2026-09-25: an omitted mode is a preview).
type ApplyParams struct {
	FixerID  string   `json:"fixer_id"`
	PlanOpID string   `json:"plan_op_id"`
	RowIDs   []string `json:"row_ids"`
	DryRun   *bool    `json:"dry_run,omitempty"`
	DryRunC  *bool    `json:"dryRun,omitempty"`
	// Resume is the checkpoint of an interrupted run (set by the op through
	// the registry's checkpoint merge, never by a caller): the rows it had
	// already settled. A resumed run keeps those results and does not
	// re-apply them.
	Resume *ApplyCheckpoint `json:"resume,omitempty"`
	// OwnerApplyRowIDs are owner rows (Row.OwnerApplicable) the owner chose
	// to apply, and OwnerGrant the one-shot grant the Repairs handler minted
	// for them (owner.go). Neither is trusted on its own: the op takes the
	// grant from the process's grant store and refuses every owner row
	// without one (ResolveOwnerApproval).
	OwnerApplyRowIDs []string `json:"owner_apply_row_ids,omitempty"`
	OwnerGrant       string   `json:"owner_grant,omitempty"`
}

// ApplyCheckpoint is what repairs.apply checkpoints while it runs: every row
// settled so far, with its outcome. Merged back into the op's params as
// "resume" when the server restarts mid-apply (ResumeRestart).
type ApplyCheckpoint struct {
	Settled []RowResult `json:"settled"`
}

// RowResult is the apply outcome of one requested row.
type RowResult struct {
	RowID   string `json:"row_id"`
	Outcome string `json:"outcome"`
	// Skipped is the guard kind (skipped_guard), the plan row's skip kind
	// (not_applicable), or the re-plan's skip kind (changed_since_plan, with
	// its reason in Error).
	Skipped string `json:"skipped,omitempty"`
	Error   string `json:"error,omitempty"`
	// OwnerUserID is set on an owner row (owner.go): the user whose grant
	// the row ran under.
	OwnerUserID string `json:"owner_user_id,omitempty"`
}

// ApplyResult is the stored result of repairs.apply.
type ApplyResult struct {
	FixerID          string         `json:"fixer_id"`
	PlanOpID         string         `json:"plan_op_id"`
	DryRun           bool           `json:"dry_run"`
	Requested        int            `json:"requested"`
	NotInPlan        []string       `json:"not_in_plan,omitempty"`
	ByOutcome        map[string]int `json:"by_outcome"`
	Applied          int            `json:"applied"`
	ChangedSincePlan int            `json:"changed_since_plan"`
	Partial          int            `json:"partially_applied"`
	Failed           int            `json:"failed"`
	// RetryLater counts rows refused with ErrRetryLater (nothing written).
	RetryLater    int `json:"retry_later"`
	BookWrites    int `json:"book_writes"`
	HistoryRows   int `json:"history_rows"`
	HistoryFailed int `json:"history_rows_failed"`
	// JournalRows counts the operation-journal rows the apply recorded
	// (book_file steps and credit writes alike); the op revert replays them.
	JournalRows int `json:"journal_rows,omitempty"`
	// StandDownHeld: the library-scan stand-down was held for the writes.
	StandDownHeld bool `json:"standdown_held"`
	// FollowUp is what an AfterApplier fixer started for the applied books
	// (an operation id); FollowUpError is why it could not. Neither changes
	// any row's outcome: the writes were already made.
	FollowUp      string `json:"follow_up,omitempty"`
	FollowUpError string `json:"follow_up_error,omitempty"`
	Aborted       string `json:"aborted,omitempty"`
	// OwnerRows / OwnerUserID: the owner rows the run was asked for and the
	// user whose grant they ran under ("" when refused).
	OwnerRows   int         `json:"owner_rows,omitempty"`
	OwnerUserID string      `json:"owner_user_id,omitempty"`
	Rows        []RowResult `json:"rows"`
}

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
	// OwnerITunesDatabaseOnly are books of an owner row whose files sit
	// under books/itunes/** and whose DATABASE rows the owner's apply of the
	// row writes (never a file, never an iTunes id): the fragment
	// consolidation's iTunes twins, retired into the parent beside their
	// library copy (docs/plans/2026-10-07-stale-itunes-path.md D7). The
	// framework guard lifts its books/itunes path check for exactly these
	// books, and only while the row is owner-applicable (plan) or applied
	// under the owner's grant for it (apply); every other check (Doctor Who
	// / Big Finish / Torchwood by path, title, credits, transcription, tags;
	// an unreadable path) still runs for them. An applicable row, or a plain
	// apply, gets no exception from it. Every id must also be in BookIDs.
	OwnerITunesDatabaseOnly []string `json:"owner_itunes_database_only,omitempty"`
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
