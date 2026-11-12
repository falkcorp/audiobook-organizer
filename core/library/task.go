// file: core/library/task.go
// version: 1.0.2
// guid: 7c1ca96d-27c7-48fa-9e0e-837a3808fcc9
// last-edited: 2026-11-11

package library

import (
	"errors"
	"time"

	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/tasks"
)

// TransferRecoverKind is the kind's name (05-library 5L.3.3).
const TransferRecoverKind tasks.Kind = "core.library.transfer-recover"

// TransferRecoverScheduleKey is the schedule key the kind is bound to. It is
// declared with type tasks.ScheduleConfig in the core.library section (05-library
// 5L.7, default {enabled: true, every: 24h}); F2-04 owns the declaration.
const TransferRecoverScheduleKey = "core.library.schedules.transfer_recover"

// TransferRecoverTimeout is the kind's per-attempt timeout: a pass hashes the
// files of the few interrupted transfers, never the library.
const TransferRecoverTimeout = 30 * time.Minute

// TransferRecoverParams is the kind's params type. It is empty: each pass reads
// the journal afresh.
type TransferRecoverParams struct{}

// TransferRecoverResult is the kind's result type: Recover's RecoverReport.
type TransferRecoverResult struct {
	Completed  int `json:"completed"`
	RolledBack int `json:"rolled_back"`
	Left       int `json:"left"`
	Blocked    int `json:"blocked"`
	Conflicts  int `json:"conflicts"`
	Failed     int `json:"failed"`
}

// TransferRecoverTask returns the definition of core.library.transfer-recover
// over m, which must implement Recoverer (the value New returns does). The
// kernel registers it when the library is wired (F2-16, F7); it runs once at
// start (OnStart) and then on the key's interval, and finds nothing to do when
// no transfer was interrupted. Writes on core's own library resource;
// Live (a scheduled writer states its mode, D8), ResumeFromZero, because a pass is idempotent (every row is decided from the
// journal and from what is on disk); core:library.manage to start. A manager
// that cannot recover yields a definition whose Run is nil, which
// tasks.Problems reports, so registration fails closed.
//
//tasklint:constructor the kernel passes the result to Registrar.Tasks when core.library is wired (F2-16, F7); this package registers nothing
func TransferRecoverTask(m Manager) tasks.Definition {
	var run func(rc *tasks.RC, p TransferRecoverParams) (TransferRecoverResult, error)
	if rec, ok := m.(Recoverer); ok {
		run = func(rc *tasks.RC, _ TransferRecoverParams) (TransferRecoverResult, error) {
			rep, err := rec.Recover(rc.Context())
			res := TransferRecoverResult(rep)
			// The executor stores a result only for a run that succeeded, so
			// the status line is where a failed pass's counts survive.
			rc.Progress().Note(rep.String())
			if err != nil {
				return res, errors.Join(errors.New("library: transfer recovery"), err)
			}
			return res, nil
		}
	}
	return tasks.Task(TransferRecoverKind, tasks.TaskSpec[TransferRecoverParams, TransferRecoverResult]{
		Title:    "Recover interrupted file transfers",
		Help:     "Completes or rolls back every file transfer and quarantine step a crash interrupted, from its last durable journal step.",
		Effects:  tasks.Writes(tasks.Res("core", "library")),
		Perms:    tasks.Permissions{Start: rbac.Need("core:library.manage")},
		Resume:   tasks.ResumeFromZero,
		Timeout:  TransferRecoverTimeout,
		Schedule: tasks.ScheduleFromConfig(TransferRecoverScheduleKey).OnStart().Live(),
		Run:      run,
	})
}
