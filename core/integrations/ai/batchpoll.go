// file: core/integrations/ai/batchpoll.go
// version: 1.0.0
// guid: dadc6b9e-28c7-46cd-825b-259db75f2454
// last-edited: 2026-11-11

// Package ai is the provider-neutral AI contract of 05-ai. Until the
// integrations flip (F7) it holds only the minimal owner registration of the
// kind core.ai.batch-poll: the kind's final TaskSpec over the one-method
// interim backend InterimPoll (05-tasks 5T.8.2). F7 extends BatchPollTask with
// the native body over the job store and deletes InterimPoll.
package ai

import (
	"time"

	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/tasks"
)

// BatchPollKind is the kind's name. It never changes at F5 or F7: the kind's
// stored runs and batch_poller's clock are stored under it (05-tasks 5T.8.2).
const BatchPollKind tasks.Kind = "core.ai.batch-poll"

// BatchPollScheduleKey is the schedule key the kind is bound to. It is declared
// with type tasks.ScheduleConfig in the core.ai section (05-ai section 7,
// default {enabled: true, every: 5m}); F2-04 owns the declaration.
const BatchPollScheduleKey = "core.ai.schedules.batch_poll"

// BatchPollTimeout is the kind's per-attempt timeout.
const BatchPollTimeout = 10 * time.Minute

// BatchPollParams is the kind's params type. It is empty: every poll reads the
// open jobs afresh.
type BatchPollParams struct{}

// BatchPollResult is the kind's result type (05-ai section 4, item 5).
type BatchPollResult struct {
	// Polled counts the open jobs the run polled.
	Polled int `json:"polled"`
	// ApplyStaged counts the jobs whose results were ready and whose apply run
	// was staged through tasks.Stager.StartIn.
	ApplyStaged int `json:"apply_staged"`
	// Failed counts the jobs the run moved to a failed state.
	Failed int `json:"failed"`
}

// InterimPoll is the one-method backend the kind runs over from F5 until F7
// (05-tasks 5T.8.2). Its method has exactly the kind's TaskSpec.Run signature,
// so the registration's Run is the backend's method value and F7's native body
// keeps the types. The backend advances the app's AI jobs and stages each ready
// job's apply as an app run through tasks.Stager.StartIn; it never calls a
// module handler and writes no module entity under the core kind (COR2-11).
// F7 deletes this interface.
type InterimPoll interface {
	PollBatches(rc *tasks.RC, p BatchPollParams) (BatchPollResult, error)
}

// BatchPollTask returns the final definition of core.ai.batch-poll over b.
// The kernel registers it at P6 only when the app supplies a backend. Its
// fields are 05-tasks 5T.8.2's row: Writes on the owner's own core resource,
// ResumeFromZero (each poll re-reads the open jobs and a staged apply run is
// deduped by its job, so a restart from zero is exact), ten minutes,
// core:integrations.manage to start, scheduled live from BatchPollScheduleKey.
// A nil b yields a definition whose Run is nil, which tasks.Problems reports
// ("Task has no Run"), so registration fails closed.
//
//tasklint:constructor the kernel passes the result to Registrar.Tasks at P6 when the app supplies an InterimPoll backend (F5-18); this package registers nothing
func BatchPollTask(b InterimPoll) tasks.Definition {
	var run func(rc *tasks.RC, p BatchPollParams) (BatchPollResult, error)
	if b != nil {
		run = b.PollBatches
	}
	return tasks.Task(BatchPollKind, tasks.TaskSpec[BatchPollParams, BatchPollResult]{
		Title:    "Poll AI batch jobs",
		Help:     "Advances provider batch jobs and stages the apply run of each job whose results are ready.",
		Effects:  tasks.Writes(tasks.Res("core", "ai")),
		Perms:    tasks.Permissions{Start: rbac.Need("core:integrations.manage")},
		Resume:   tasks.ResumeFromZero,
		Timeout:  BatchPollTimeout,
		Schedule: tasks.ScheduleFromConfig(BatchPollScheduleKey).Live(),
		Run:      run,
	})
}
