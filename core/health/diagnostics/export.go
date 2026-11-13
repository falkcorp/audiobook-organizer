// file: core/health/diagnostics/export.go
// version: 1.1.1
// guid: 7102b260-8f02-42cc-80dd-78ab03c55cec
// last-edited: 2026-11-13

// Package diagnostics is the diagnostics bundle of 05-health 5H.3.5. Until the
// health flip (F8) it holds only the minimal owner registration of the kind
// core.diagnostics.export: the kind's final TaskSpec over the one-method
// interim backend InterimExport (05-tasks 5T.8.2). F8 extends ExportTask with
// the native body and deletes InterimExport.
package diagnostics

import (
	"time"

	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/tasks"
)

// ExportKind is the kind's name. It never changes at F5 or F8: the kind's
// stored runs are stored under it (05-tasks 5T.8.2).
const ExportKind tasks.Kind = "core.diagnostics.export"

// ExportTimeout is the kind's per-attempt timeout.
const ExportTimeout = 30 * time.Minute

// ExportParams is the kind's params type (05-health 5H.3.5): the start
// request's options and the start-time Viewer's actor. The run outlives the
// request, so the params are plain data; the bundle's permission view is
// resolved from ActorID when the run starts.
type ExportParams struct {
	// ActorID is the start-time Viewer's actor (diagnostics.Viewer.ActorID).
	ActorID string `json:"actor_id"`
	// Perms are the actor's concrete permissions as resolved when the request
	// was made (PermsOf), so the bundle holds what the actor could read then
	// and not what a later role change leaves them (05-health 5H.3.5). The
	// start route always sets it, an empty list when the actor holds nothing;
	// nil means no snapshot was taken. The run always resolves the actor's
	// permissions when it starts and a snapshot only NARROWS that set (a
	// permission is read only if the actor holds it now and the snapshot lists
	// it), because task params are client-supplied and must never grant.
	// It is deliberately not omitempty: an empty snapshot must not read back
	// as none.
	Perms []string `json:"perms"`
	// Sections, when not empty, names the sections to write
	// ("<moduleID>.<name>"); empty means every section the viewer may read. The
	// spec names "the start request's options" without listing them, so this
	// is the one option F2 declares; F8 may add more.
	Sections []string `json:"sections,omitempty"`
}

// ExportResult is the kind's result type (05-health 5H.3.5).
type ExportResult struct {
	// File names the zip, "<run-id>.zip", inside core.diagnostics.dir.
	File string `json:"file"`
	// Redactions is the manifest's redaction counts by layer: "typed",
	// "pattern" and "permission" (the mediacore_diagnostics_redactions_total
	// layers).
	Redactions map[string]int `json:"redactions,omitempty"`
}

// InterimExport is the one-method backend the kind runs over from F5 until F8
// (05-tasks 5T.8.2). Its method has exactly the kind's TaskSpec.Run signature,
// so the registration's Run is the backend's method value and F8's native body
// keeps the types. The backend writes the app's bundle into the run's zip and
// reports progress per section. F8 deletes this interface.
type InterimExport interface {
	Export(rc *tasks.RC, p ExportParams) (ExportResult, error)
}

// ExportTask returns the final definition of core.diagnostics.export over b.
// The kernel registers it at P6 only when the app supplies a backend. Its
// fields are 05-tasks 5T.8.2's row: ReadOnly, ResumeDrop, thirty minutes,
// core:system.manage to start, no schedule. A nil b yields a definition whose
// Run is nil, which tasks.Problems reports ("Task has no Run"), so
// registration fails closed.
//
//tasklint:constructor the kernel passes the result to Registrar.Tasks at P6 when the app supplies an InterimExport backend (F5-18); this package registers nothing
func ExportTask(b InterimExport) tasks.Definition {
	var run func(rc *tasks.RC, p ExportParams) (ExportResult, error)
	if b != nil {
		run = b.Export
	}
	return tasks.Task(ExportKind, tasks.TaskSpec[ExportParams, ExportResult]{
		Title:   "Export diagnostics",
		Help:    "Writes a redacted diagnostics bundle into a zip, section by section, limited to what the person who asked may read.",
		Effects: tasks.ReadOnly(),
		Perms:   tasks.Permissions{Start: rbac.Need("core:system.manage")},
		Resume:  tasks.ResumeDrop,
		Timeout: ExportTimeout,
		Run:     run,
	})
}
