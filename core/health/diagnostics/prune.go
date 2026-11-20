// file: core/health/diagnostics/prune.go
// version: 0.1.0
// guid: 1f600abf-08d1-4d69-8985-8243ecfd9ef1
// last-edited: 2026-11-20

package diagnostics

import (
	"fmt"
	"os"
	"time"

	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/tasks"
)

// PruneKind is the bundle retention kind (05-health 5H.3.5): it deletes the
// bundles in core.diagnostics.dir once they are past core.diagnostics.retention.
const PruneKind tasks.Kind = "core.diagnostics.prune"

// PruneScheduleKey is core.diagnostics.schedules.prune (05-health 5H.3.5,
// R2-7): the kernel's core.diagnostics section declares it, default every 1 h.
// This file only passes it to tasks.ScheduleFromConfig.
const PruneScheduleKey = "core.diagnostics.schedules.prune"

// PruneTimeout bounds one pass. A pass lists one directory and removes at
// most a few hundred files, so ten minutes is far past any real pass.
const PruneTimeout = 10 * time.Minute

// PruneParams is empty: each pass reads the retention afresh.
type PruneParams struct{}

// PruneResult is what a pass did.
type PruneResult struct {
	// Removed counts the bundles and abandoned partials deleted.
	Removed int `json:"removed"`
	// Retention is the retention the pass applied, after the default rule
	// (a zero or negative setting is DefaultRetention), in Go duration text.
	Retention string `json:"retention"`
}

// PruneTask returns the definition of core.diagnostics.prune over root, the
// os.Root on core.diagnostics.dir that the exporter and the download route
// use. retention reads core.diagnostics.retention at the start of each run,
// so a changed value applies to the next pass; a value of zero or less is
// DefaultRetention (Prune's rule, the same as OpenBundle's, so the prune
// never deletes a bundle the download route would still serve). now is the
// clock (nil = time.Now). A nil root or retention yields a definition whose
// Run is nil, which tasks.Problems reports, so registration fails closed.
//
// Perms.Start is core:tasks.manage for the reason core.tasks.prune's is: the
// scheduler starts the kind as core's system actor, which holds no
// AuthRequired scope (SEC2-15), and core:system.manage is AuthRequired.
//
//tasklint:constructor the kernel passes the result to Registrar.Tasks at P6 when it wires core.diagnostics (F8-04); this package registers nothing
func PruneTask(root *os.Root, retention func() time.Duration, now func() time.Time) tasks.Definition {
	var run func(rc *tasks.RC, _ PruneParams) (PruneResult, error)
	if root != nil && retention != nil {
		if now == nil {
			now = time.Now
		}
		run = func(rc *tasks.RC, _ PruneParams) (PruneResult, error) {
			return prunePass(rc, root, retention(), now())
		}
	}
	return tasks.Task(PruneKind, tasks.TaskSpec[PruneParams, PruneResult]{
		Title:    "Prune diagnostics bundles",
		Help:     "Deletes diagnostics bundles, and partial bundles a stopped export left behind, once they are older than the diagnostics retention.",
		Effects:  tasks.Writes(tasks.Res("core", "diagnostics")),
		Perms:    tasks.Permissions{Start: rbac.Need("core:tasks.manage")},
		Resume:   tasks.ResumeFromZero,
		Priority: tasks.PriorityLow,
		Timeout:  PruneTimeout,
		Schedule: tasks.ScheduleFromConfig(PruneScheduleKey).Live(),
		Run:      run,
	})
}

// prunePass is one run: the retention after the default rule, then Prune.
func prunePass(rc *tasks.RC, root *os.Root, keep time.Duration, now time.Time) (PruneResult, error) {
	if keep <= 0 {
		keep = DefaultRetention
	}
	n, err := Prune(root, keep, now)
	out := PruneResult{Removed: n, Retention: keep.String()}
	// The executor stores a result only for a run that succeeded, so the
	// status line is where a failed pass's count survives.
	rc.Progress().Note(fmt.Sprintf("removed %d diagnostics bundles older than %s", n, keep))
	if err != nil {
		return out, fmt.Errorf("diagnostics: prune: %w", err)
	}
	return out, nil
}
