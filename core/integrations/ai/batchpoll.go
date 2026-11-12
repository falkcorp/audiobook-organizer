// file: core/integrations/ai/batchpoll.go
// version: 1.1.0
// guid: dadc6b9e-28c7-46cd-825b-259db75f2454
// last-edited: 2026-11-12

// Package ai is the provider-neutral AI contract of 05-ai: the capability
// catalog, the per-App Router with its routing, failover, health and slots,
// structured output, the batch job store and apply tasks, the core.ai.batch-poll
// kind and the section 8 metrics. Provider adapters live in subpackages
// (integrations/ai/openai), and no other package may import a provider SDK.
//
// The kind core.ai.batch-poll is registered over InterimPoll (05-tasks 5T.8.2),
// the one-method backend an app supplies until it moves onto the native job
// store; *Router implements it, so BatchPollTask(router) is the native poll.
package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
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

// ---- the native poll -------------------------------------------------------------

// errSkipJob ends a stage closure without staging anything: the job moved while
// the poll was deciding (a cancel, another poll), so the poll leaves it for its
// next pass.
var errSkipJob = errors.New("ai: job moved; skipped this pass")

// batchCallTimeout bounds one provider call made by the poll.
func batchCallTimeout() time.Duration { return 2 * time.Minute }

// setOpenCounts records the open-state counts of this poll's snapshot for the
// batch_jobs gauge.
func (r *Router) setOpenCounts(open []StoredJob) {
	counts := map[JobState]int{}
	for _, j := range open {
		counts[j.State]++
	}
	r.pollMu.Lock()
	r.openJobs = counts
	r.pollMu.Unlock()
}

// PollBatches is the native body of core.ai.batch-poll (05-ai section 4, step
// 5). It reads every open job and advances each one, at most
// core.ai.batch_parallelism at a time:
//
//   - Pending: look the remote batch up by its job tag (the orphan reconcile)
//     and record the handle; a job with no remote batch after the grace period
//     is failed (its items are not stored, so it cannot be resubmitted).
//   - Submitted: ask the provider. Completed: in ONE commit move the job to
//     Applying and stage the module's apply run through tasks.Stager.StartIn;
//     Failed, Expired or Canceled at the provider: fail the job.
//   - Applying: stage another apply run (a failed run leaves the job Applying),
//     deduped by job, with a same-state write so a concurrent cancel conflicts.
//
// It never calls a module handler, and it never raises a job's attempt count
// (the apply run does, so one writer owns it). Every job write is a
// compare-and-set from the state the poll read; a job that moved meanwhile is
// skipped until the next pass. While core.integrations.disabled is set it makes
// no remote call and changes no job. A job store that cannot be read fails the
// poll with nothing staged.
func (r *Router) PollBatches(rc *tasks.RC, _ BatchPollParams) (BatchPollResult, error) {
	var res BatchPollResult
	if r.isDisabled() {
		return res, nil
	}
	if r.jobs == nil {
		return res, ErrNoJobStore
	}
	ctx := rc.Context()
	var open []StoredJob
	for j, err := range r.jobs.Open(ctx) {
		if err != nil {
			return res, fmt.Errorf("ai: batch poll: list open jobs: %w", err)
		}
		open = append(open, j)
	}
	r.setOpenCounts(open)
	cfg := r.config()

	var polled, staged, failed atomic.Int64
	var mu sync.Mutex
	var errs []error
	_, err := tasks.RunItems(rc, tasks.Fixed(cfg.BatchParallelism, "provider batch API calls"), open,
		func(ic *tasks.ItemRC, j StoredJob) error {
			polled.Add(1)
			s, f, err := r.pollOne(ic.Context(), j)
			if s {
				staged.Add(1)
			}
			if f {
				failed.Add(1)
			}
			if err != nil {
				mu.Lock()
				errs = append(errs, fmt.Errorf("job %s: %w", j.ID, err))
				mu.Unlock()
			}
			return err
		}, tasks.ItemsOptions[StoredJob]{OnError: tasks.Collect})
	res = BatchPollResult{Polled: int(polled.Load()), ApplyStaged: int(staged.Load()), Failed: int(failed.Load())}
	if err != nil {
		return res, err
	}
	mu.Lock()
	defer mu.Unlock()
	return res, errors.Join(errs...)
}

// pollOne advances one open job; it reports whether an apply run was staged and
// whether the job was moved to Failed. The error is a store or stager failure;
// a provider that does not answer is logged and retried next pass.
func (r *Router) pollOne(ctx context.Context, j StoredJob) (staged, failed bool, err error) {
	switch j.State {
	case JobPending:
		return r.pollPending(ctx, j)
	case JobSubmitted:
		return r.pollSubmitted(ctx, j)
	case JobApplying:
		s, err := r.stageApply(ctx, j, JobApplying)
		return s, false, err
	}
	return false, false, nil
}

// batchBackend leases the job's endpoint as a BatchBackend.
func (r *Router) batchBackend(ctx context.Context, ref integrations.Ref) (BatchBackend, func(), error) {
	b, release, err := integrations.Lease[Backend](ctx, r.reg, ref)
	if err != nil {
		return nil, nil, err
	}
	bb, ok := b.(BatchBackend)
	if !ok {
		release()
		return nil, nil, fmt.Errorf("ai: the adapter of %s has no batch API", ref)
	}
	return bb, release, nil
}

func (r *Router) warnJob(msg string, j StoredJob, err error) {
	r.log.Warn(msg, "job", string(j.ID), "endpoint", oneLine(j.Endpoint.String()), "err", oneLine(err.Error()))
}

// failJob moves j to Failed from the state the poll read it in. A lost race is
// not a failure of the poll: the job moved on its own.
func (r *Router) failJob(ctx context.Context, j StoredJob) (bool, error) {
	next := j
	next.State, next.Updated = JobFailed, r.now()
	err := r.transitionJob(ctx, j.ID, j.State, next)
	switch {
	case err == nil:
		return true, nil
	case lostRace(err):
		return false, nil
	}
	return false, err
}

func (r *Router) pollPending(ctx context.Context, j StoredJob) (bool, bool, error) {
	stale := r.now().Sub(j.Created) >= pendingGrace()
	bb, release, err := r.batchBackend(ctx, j.Endpoint)
	if err != nil {
		r.warnJob("ai: batch poll: cannot reach the endpoint of a pending job", j, err)
		if stale {
			f, ferr := r.failJob(ctx, j)
			return false, f, ferr
		}
		return false, false, nil
	}
	defer release()
	cctx, cancel := context.WithTimeout(ctx, batchCallTimeout())
	defer cancel()
	handles, err := bb.FindBatches(cctx, map[string]string{JobTagKey: string(j.ID)})
	if err != nil {
		r.warnJob("ai: batch poll: orphan lookup failed", j, err)
		return false, false, nil
	}
	if len(handles) == 0 {
		if stale {
			f, ferr := r.failJob(ctx, j)
			return false, f, ferr
		}
		return false, false, nil
	}
	if len(handles) > 1 {
		r.warnJob("ai: batch poll: several remote batches carry one job tag; recording the first", j, fmt.Errorf("%d batches", len(handles)))
	}
	next := j
	next.Handle, next.State, next.Updated = handles[0], JobSubmitted, r.now()
	if err := r.transitionJob(ctx, j.ID, JobPending, next); err != nil && !lostRace(err) {
		return false, false, err
	}
	return false, false, nil
}

func (r *Router) pollSubmitted(ctx context.Context, j StoredJob) (bool, bool, error) {
	bb, release, err := r.batchBackend(ctx, j.Endpoint)
	if err != nil {
		r.warnJob("ai: batch poll: cannot reach the endpoint of a submitted job", j, err)
		return false, false, nil
	}
	defer release()
	cctx, cancel := context.WithTimeout(ctx, batchCallTimeout())
	defer cancel()
	st, err := bb.BatchStatus(cctx, j.Handle)
	if err != nil {
		r.warnJob("ai: batch poll: status failed", j, err)
		return false, false, nil
	}
	switch st.Phase {
	case BatchCompleted:
		s, err := r.stageApply(ctx, j, JobSubmitted)
		return s, false, err
	case BatchFailed, BatchExpired, BatchCanceled:
		f, err := r.failJob(ctx, j)
		return false, f, err
	}
	return false, false, nil
}

// stageApply moves the job to Applying (or, from Applying, rewrites it in
// place) and stages its module's apply run in the SAME commit, through
// tasks.Stager.WriteRuns, which re-runs the closure from fresh reads on a
// conflict. The closure re-reads the job each attempt and stages nothing when
// the job is no longer in from: a canceled job never gets a run, and a job
// another poll already moved is not staged twice. The run starts ModeLive, and
// the kernel stamps the owning module's actor; a start that finds a run queued
// for the job folds into it (the apply task's dedupe key).
func (r *Router) stageApply(ctx context.Context, j StoredJob, from JobState) (bool, error) {
	if r.stager == nil {
		return false, errors.New("ai: the router has no Stager; the apply run of a ready job cannot be staged")
	}
	if !r.HasApplyKind(j.ApplyKind) {
		r.warnJob("ai: batch poll: a ready job names an apply kind no module registered; leaving it", j, errors.New(oneLine(j.ApplyKind)))
		return false, nil
	}
	params, err := json.Marshal(BatchApplyParams{Job: j.ID})
	if err != nil {
		return false, err
	}
	staged := false
	err = r.stager.WriteRuns(ctx, r.newBatch, func(ctx context.Context, b *storage.Batch) error {
		staged = false
		cur, err := r.jobs.Get(ctx, j.ID)
		if err != nil {
			return err
		}
		if cur.State != from {
			return errSkipJob
		}
		next := cur
		next.State, next.Updated = JobApplying, r.now()
		if err := r.jobs.Transition(ctx, b, j.ID, from, next); err != nil {
			return err
		}
		if _, err := r.stager.StartIn(ctx, b, tasks.Kind(cur.ApplyKind), tasks.StartRequest{Mode: tasks.ModeRequestLive, Params: params}); err != nil {
			return err
		}
		staged = true
		return nil
	})
	switch {
	case err == nil:
		return staged, nil
	case lostRace(err), errors.Is(err, errSkipJob):
		return false, nil
	}
	return false, err
}
