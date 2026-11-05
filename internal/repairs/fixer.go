// file: internal/repairs/fixer.go
// version: 1.11.0
// guid: 3e8b1c52-7a4d-4f19-9c06-5d2e8a7f1b34
// last-edited: 2026-11-05

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
	// ID is the stable id used in the URL and in the op params. It is never
	// shown to the owner and never renamed: stored op history, per-fixer
	// settings and ?fixer= links key on it.
	ID() string
	// Title is the name the lane shows: "<Verb> <what it fixes>" in plain
	// language (see Meta). It is fixed text: a state such as "preview only"
	// is a badge from PlanOnly, never a suffix.
	Title() string
	// Description is the full text shown in the fixer's header, safety
	// warnings included. The rail shows Meta.Summary instead.
	Description() string
	// Meta says where the fixer sits in the lane and how the rail describes it.
	// It is required, not optional, so a fixer cannot be added without a group
	// and a summary.
	Meta() Meta
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

// Meta is how the Repairs lane lists a fixer: the group header it sits under,
// its place inside that group, the one-line summary shown in the rail, and
// whether its plan can be started from the lane at all.
type Meta struct {
	// Group is the id of one of Groups(). The lane lists the groups in that
	// order, each under its own header.
	Group GroupID
	// Rank orders the fixer inside its group, 1 first. It is a fixed number,
	// not a runtime sort by usage, so the list does not reshuffle after each
	// trial; it was set from how often each fixer was applied (the 2026-11-05
	// naming proposal) and is worth re-measuring by hand now and then.
	Rank int
	// Summary is one plain sentence for the rail. The long Description, with
	// its safety warnings, stays in the fixer's header.
	Summary string
	// APIOnly, when not empty, says why the fixer's plan needs params the lane
	// cannot collect (an uploaded export, ids from an outside list). The lane
	// then shows an "API only" badge and a disabled "Run trial" button with
	// this text as the explanation, because a trial started with no params
	// can only fail. Empty: the fixer plans with no params.
	APIOnly string
}

// GroupID names one of the lane's group headers.
type GroupID string

// The lane's groups, in display order (see Groups).
const (
	GroupDuplicates GroupID = "duplicates"
	GroupTitles     GroupID = "titles"
	GroupAuthors    GroupID = "authors"
	GroupSeries     GroupID = "series"
	GroupMetadata   GroupID = "metadata"
	GroupFiles      GroupID = "files"
	GroupVersions   GroupID = "versions"
	GroupImports    GroupID = "imports"
)

// Group is one header of the Repairs lane.
type Group struct {
	ID    GroupID
	Title string
}

// groups is the one place the lane's group order lives. It follows how often
// each group's fixers were applied (owner decision 2026-11-05); the imports
// and one-shot tools group is always last, because those fixers are run from
// the API.
var groups = []Group{
	{GroupDuplicates, "Duplicates & split books"},
	{GroupTitles, "Titles"},
	{GroupAuthors, "Authors"},
	{GroupSeries, "Series"},
	{GroupMetadata, "Metadata & tags"},
	{GroupFiles, "Files & iTunes paths"},
	{GroupVersions, "Versions"},
	{GroupImports, "Imports & one-shot tools"},
}

// Groups returns the lane's groups in display order. The slice is a copy.
func Groups() []Group { return append([]Group(nil), groups...) }

// groupIndex is the position of a group id in the display order. An id that is
// not a group sorts after every real one, so a fixer with a missing or
// mistyped group is listed last instead of being dropped.
func groupIndex(id GroupID) int {
	for i, g := range groups {
		if g.ID == id {
			return i
		}
	}
	return len(groups)
}

// GroupTitle is the header text of a group id, or "" when it is not one.
func GroupTitle(id GroupID) string {
	for _, g := range groups {
		if g.ID == id {
			return g.Title
		}
	}
	return ""
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

// PlanOnly is implemented by a fixer that only previews: its Plan and Replan
// classify rows, and it has no Apply yet (the retired-versions fixer until its
// apply lands). The apply routes refuse it (ErrPlanOnly) and the Repairs lane
// hides its Apply controls, so no run is ever queued for it. The fixer's own
// Apply must still refuse: this is the front door, not the only lock.
type PlanOnly interface {
	PlanOnly() bool
}

// ErrPlanOnly is what starting an apply of a PlanOnly fixer answers.
var ErrPlanOnly = errors.New("repairs: this fixer only previews; it cannot be applied")

// IsPlanOnly reports whether f only previews (PlanOnly).
func IsPlanOnly(f Fixer) bool {
	x, ok := f.(PlanOnly)
	return ok && x.PlanOnly()
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
