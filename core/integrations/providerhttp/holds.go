// file: core/integrations/providerhttp/holds.go
// version: 1.0.0
// guid: 9458aaad-d25b-4455-b4f8-535c65819b12
// last-edited: 2026-11-11

// Package providerhttp is the instance-scoped provider HTTP layer of
// 05-metadata M2. Until the integrations flip (F7) it holds only the minimal
// owner registration of the kind core.integrations.release-holds: the kind's
// final TaskSpec over the one-method interim backend InterimHolds (05-tasks
// 5T.8.2). F7 extends ReleaseHoldsTask with the native body over the gate
// store and deletes InterimHolds.
package providerhttp

import (
	"time"

	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/tasks"
)

// ReleaseHoldsKind is the kind's name. It never changes at F5 or F7: the
// kind's stored runs and the kind-rename map target it (05-tasks 5T.8.2).
const ReleaseHoldsKind tasks.Kind = "core.integrations.release-holds"

// ReleaseHoldsTimeout is the kind's per-attempt timeout (HEAD's throttle
// reset).
const ReleaseHoldsTimeout = time.Minute

// ReleaseHoldsParams is the kind's params type. It is empty: the run lists
// every active hold of every scope.
type ReleaseHoldsParams struct{}

// ReleaseHoldsResult is the kind's result type (05-metadata M2.4). Each listed
// hold (scope, class, until, reason) is a line of the run's log, not a field
// here.
type ReleaseHoldsResult struct {
	// Listed counts the active holds the run found.
	Listed int `json:"listed"`
	// Released counts the holds a Live run released; a Preview run lists and
	// releases nothing, so it is zero there.
	Released int `json:"released"`
}

// InterimHolds is the one-method backend the kind runs over from F5 until F7
// (05-tasks 5T.8.2). Its method has exactly the kind's TaskSpec.Run signature,
// so the registration's Run is the backend's method value and F7's native body
// keeps the types. The backend is app code over its own state: it lists and,
// in a Live run, releases the app's provider holds, and writes no module
// entity under the core kind (COR2-11). F7 deletes this interface.
type InterimHolds interface {
	ReleaseHolds(rc *tasks.RC, p ReleaseHoldsParams) (ReleaseHoldsResult, error)
}

// ReleaseHoldsTask returns the final definition of core.integrations.release-holds
// over b. The kernel registers it at P6 only when the app supplies a backend.
// Its fields are 05-tasks 5T.8.2's row: Writes on the owner's own core
// resource, ResumeDrop, one minute, core:integrations.manage to start, high
// priority, no schedule. A nil b yields a definition whose Run is nil, which
// tasks.Problems reports ("Task has no Run"), so registration fails closed.
//
//tasklint:constructor the kernel passes the result to Registrar.Tasks at P6 when the app supplies an InterimHolds backend (F5-18); this package registers nothing
func ReleaseHoldsTask(b InterimHolds) tasks.Definition {
	var run func(rc *tasks.RC, p ReleaseHoldsParams) (ReleaseHoldsResult, error)
	if b != nil {
		run = b.ReleaseHolds
	}
	return tasks.Task(ReleaseHoldsKind, tasks.TaskSpec[ReleaseHoldsParams, ReleaseHoldsResult]{
		Title:    "Release provider holds",
		Help:     "Lists every active provider hold and, in a Live run, releases each. Quota counters and breaker state are left as they are.",
		Effects:  tasks.Writes(tasks.Res("core", "providerhttp")),
		Perms:    tasks.Permissions{Start: rbac.Need("core:integrations.manage")},
		Resume:   tasks.ResumeDrop,
		Timeout:  ReleaseHoldsTimeout,
		Priority: tasks.PriorityHigh,
		Run:      run,
	})
}
