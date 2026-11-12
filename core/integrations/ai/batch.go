// file: core/integrations/ai/batch.go
// version: 1.0.0
// guid: 0d6b3e58-9a41-4c72-b5f8-3e1a7c9d4b26
// last-edited: 2026-11-12

package ai

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/tasks"
)

// ---- the batch API of the router (05-ai section 4, step 5) -----------------------

// pendingGrace is how long a Pending job whose remote batch cannot be found by
// tag is left alone before the poll fails it: a SubmitBatch that is still
// between its job row and its remote call looks the same.
func pendingGrace() time.Duration { return time.Hour }

// applyKindOf is the registered apply kind of capability id, or "".
func (r *Router) applyKindOf(id string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if e := r.catalog[id]; e != nil {
		return e.applyKind
	}
	return ""
}

// setApplyKind records the apply kind BatchApplyTask minted for id.
func (r *Router) setApplyKind(id, kind string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e := r.catalog[id]; e != nil {
		e.applyKind = kind
	}
}

// newJobID is a time-ordered, collision-resistant job ID: 12 hex digits of the
// millisecond clock and 8 random bytes. Hex only, so it is one safe key segment.
func (r *Router) newJobID() (BatchJobID, error) {
	var rnd [8]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return "", fmt.Errorf("ai: job ID: %w", err)
	}
	return BatchJobID(fmt.Sprintf("%012x%s", r.now().UnixMilli(), hex.EncodeToString(rnd[:]))), nil
}

// commitJob opens a fresh indexed batch over the job store's keyspace, runs
// stage, and commits it with storage.Sync. A failed commit applies nothing.
func (r *Router) commitJob(ctx context.Context, stage func(ctx context.Context, b *storage.Batch) error) error {
	b := r.newBatch()
	defer func() { _ = b.Close() }()
	if err := stage(ctx, b); err != nil {
		return err
	}
	return b.Commit(ctx, storage.Sync)
}

// lostRace reports an error that means another writer moved the job first.
func lostRace(err error) bool {
	return errors.Is(err, ErrJobConflict) || errors.Is(err, storage.ErrConflict)
}

// transitionJob is one compare-and-set of a job: it stages Transition(from ->
// next) in a fresh batch and commits it. The caller re-reads on a lost race.
func (r *Router) transitionJob(ctx context.Context, id BatchJobID, from JobState, next StoredJob) error {
	return r.commitJob(ctx, func(ctx context.Context, b *storage.Batch) error {
		return r.jobs.Transition(ctx, b, id, from, next)
	})
}

// SubmitBatch stores a job for items, then submits them to the best endpoint
// that has FeatureBatch.
//
// The job row (JobPending, with payload) is committed BEFORE every remote call
// and the remote batch is tagged with the job ID, so a crash between the remote
// accepting the batch and the handle being recorded leaves a job the poll can
// reconcile by tag. It refuses, storing nothing, when integrations are
// disabled, no endpoint is capable, or the capability has no BatchApplyTask
// (ErrNoBatchApply: results nobody can apply would be paid for and dropped).
//
// An error that wraps ErrSubmitUnconfirmed comes with the job's ID: the
// provider may hold the batch, so do not resubmit. Any other error means
// nothing was accepted and the job, if one was stored, is Failed.
func (r *Router) SubmitBatch(ctx context.Context, c Capability, items []BatchItem, payload []byte, opts ...CallOption) (BatchJobID, error) {
	e, err := r.resolve(c)
	if err != nil {
		return "", err
	}
	applyKind := r.applyKindOf(c.id)
	if applyKind == "" {
		return "", fmt.Errorf("%w: %s", ErrNoBatchApply, c.id)
	}
	if r.jobs == nil {
		return "", ErrNoJobStore
	}
	if len(items) == 0 {
		return "", errors.New("ai: SubmitBatch needs at least one item")
	}
	seen := make(map[string]bool, len(items))
	for _, it := range items {
		if it.CustomID == "" || seen[it.CustomID] {
			return "", fmt.Errorf("ai: batch item custom IDs must be non-empty and unique (%q)", oneLine(it.CustomID))
		}
		seen[it.CustomID] = true
	}
	co := newCallOptions(opts)
	co.requireFeatures = append(slices.Clone(co.requireFeatures), FeatureBatch)
	cfg := r.config()
	cands, refusals := r.selectEndpoints(ctx, e.spec, co, cfg)
	if len(cands) == 0 {
		r.m.refused(ctx, c.id)
		err := &NoCapableEndpointError{Capability: c.id, Refusals: refusals}
		r.logNoCapable(err, cfg.LogNoCapableEvery)
		return "", err
	}
	id, err := r.newJobID()
	if err != nil {
		return "", err
	}
	timeout := co.attemptTimeout
	if timeout <= 0 {
		timeout = e.spec.AttemptTimeout
	}

	created := false
	var current integrations.Ref
	var failed []AttemptError
	for _, ep := range cands {
		err = nil
		// The job row first: created on the first endpoint, re-pointed (a
		// same-state Transition) when failover moves to another.
		now := r.now()
		switch {
		case !created:
			err = r.commitJob(ctx, func(ctx context.Context, b *storage.Batch) error {
				return r.jobs.Create(ctx, b, StoredJob{
					ID: id, Capability: c.id, Module: e.module, ApplyKind: applyKind, Endpoint: ep.ref(),
					State: JobPending, Payload: slices.Clone(payload), Created: now, Updated: now,
				})
			})
			created = err == nil
		case current != ep.ref():
			err = r.transitionJob(ctx, id, JobPending, StoredJob{Endpoint: ep.ref(), State: JobPending, Updated: now})
		}
		if err != nil {
			return "", fmt.Errorf("ai: store job %s before its remote call: %w", id, err)
		}
		current = ep.ref()

		handle, class, serr := r.submitTo(ctx, ep, e.spec, id, items, timeout)
		if serr == nil {
			return id, r.recordHandle(ctx, id, ep, handle)
		}
		switch {
		case class == integrations.ClassCanceled || !definite(class):
			// The provider may have accepted it: leave the job Pending for the
			// poll's orphan reconcile and tell the caller not to resubmit.
			return id, fmt.Errorf("%w: job %s: %w", ErrSubmitUnconfirmed, id, serr)
		case failsOver(class):
			failed = append(failed, AttemptError{Endpoint: ep.ref(), Class: class, Err: serr})
			r.m.failedOver(ctx, c.id, ep.ref(), class)
		default:
			// Invalid, BadOutput, NotFound: the provider rejected the batch, and
			// another would too.
			r.failPending(ctx, id)
			return "", serr
		}
	}
	r.failPending(ctx, id)
	return "", &ExhaustedError{Capability: c.id, Attempts: failed}
}

// definite reports a class that says the provider did NOT accept the request:
// nothing was sent (RefusedLocal), or it answered with a refusal. Transient and
// Unknown are ambiguous: the request may have been accepted before the failure.
func definite(c integrations.Class) bool {
	switch c {
	case integrations.ClassRefusedLocal, integrations.ClassRateLimited, integrations.ClassQuota, integrations.ClassAuth,
		integrations.ClassUnsupported, integrations.ClassInvalid, integrations.ClassBadOutput, integrations.ClassNotFound:
		return true
	}
	return false
}

// submitTo makes the remote call to one endpoint, records the attempt, and
// benches the endpoint on a failure that says it is unhealthy.
func (r *Router) submitTo(ctx context.Context, ep *endpoint, spec CapabilitySpec, id BatchJobID,
	items []BatchItem, timeout time.Duration) (BatchHandle, integrations.Class, error) {
	start := r.now()
	var handle BatchHandle
	var err error
	deadline := false
	func() {
		backend, release, lerr := integrations.Lease[Backend](ctx, r.reg, ep.ref())
		if lerr != nil {
			err = lerr
			return
		}
		defer release()
		bb, ok := backend.(BatchBackend)
		if !ok {
			err = unsupported(Target{Endpoint: ep.ref()}, "batch submit")
			return
		}
		attemptCtx, cancel := ctx, context.CancelFunc(func() {})
		if timeout > 0 {
			attemptCtx, cancel = context.WithTimeout(ctx, timeout)
		}
		defer cancel()
		handle, err = bb.SubmitBatch(attemptCtx, ep.model, items, map[string]string{JobTagKey: string(id)})
		deadline = err != nil && errors.Is(attemptCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil
	}()
	if err == nil && handle == "" {
		err = Quality(errors.New("ai: the provider accepted the batch but returned no handle"))
	}
	out := classify(ctx, err, deadline)
	class := classOK
	if err != nil {
		class = out.class.String()
	}
	r.m.attempt(ctx, spec.ID, spec.Kind, ep.ref(), r.modelLabel(ep.model), class, r.now().Sub(start))
	switch {
	case err == nil:
		r.bench.markSuccess(ep.ref())
		return handle, integrations.ClassUnknown, nil
	case out.class == integrations.ClassQuota:
		r.bench.markQuota(ep.ref(), r.config().QuotaCooldown)
	case benches(out.class):
		cfg := r.config()
		r.bench.markFailure(ep.ref(), cfg.CooldownBase, cfg.CooldownMax)
	}
	r.log.Warn("ai: batch submit failed", "job", string(id), "endpoint", oneLine(ep.ref().String()),
		"class", out.class.String(), "err", oneLine(err.Error()))
	return "", out.class, err
}

// recordHandle moves the job from Pending to Submitted with the remote handle.
// It re-reads and retries a lost race while the job is still Pending; a job
// that was canceled meanwhile gets its remote batch canceled and the caller an
// ErrJobConflict. A commit that fails otherwise leaves the job Pending for the
// poll's orphan reconcile (ErrSubmitUnconfirmed).
func (r *Router) recordHandle(ctx context.Context, id BatchJobID, ep *endpoint, h BatchHandle) error {
	var err error
	for range 3 {
		err = r.transitionJob(ctx, id, JobPending, StoredJob{Endpoint: ep.ref(), Handle: h, State: JobSubmitted, Updated: r.now()})
		if err == nil {
			return nil
		}
		if !lostRace(err) {
			break
		}
		cur, gerr := r.jobs.Get(ctx, id)
		switch {
		case gerr != nil:
			err = gerr
		case cur.State == JobSubmitted && cur.Handle == h:
			return nil // the poll's reconcile recorded it first
		case cur.State != JobPending:
			// Canceled (or failed) while the provider was accepting it: the
			// remote batch is an orphan nobody will poll.
			if cerr := r.cancelRemote(context.WithoutCancel(ctx), StoredJob{ID: id, Endpoint: ep.ref(), Handle: h}); cerr != nil {
				r.log.Warn("ai: could not cancel an orphaned remote batch", "job", string(id), "err", oneLine(cerr.Error()))
			}
			return fmt.Errorf("%w: job %s is %s; its remote batch was canceled", ErrJobConflict, id, cur.State)
		default:
			continue
		}
		break
	}
	return fmt.Errorf("%w: job %s: the provider accepted the batch but its handle was not recorded: %w", ErrSubmitUnconfirmed, id, err)
}

// failPending marks a job whose submission definitely failed as Failed. A lost
// race (the job was canceled) is fine; any other failure is logged, and the
// poll's grace period eventually fails the job.
func (r *Router) failPending(ctx context.Context, id BatchJobID) {
	err := r.transitionJob(context.WithoutCancel(ctx), id, JobPending, StoredJob{State: JobFailed, Updated: r.now()})
	if err != nil && !lostRace(err) {
		r.log.Warn("ai: could not mark a rejected batch job failed", "job", string(id), "err", oneLine(err.Error()))
	}
}

// BatchJob returns the stored job without its payload.
func (r *Router) BatchJob(ctx context.Context, id BatchJobID) (BatchJob, error) {
	if r.jobs == nil {
		return BatchJob{}, ErrNoJobStore
	}
	j, err := r.jobs.Get(ctx, id)
	if err != nil {
		return BatchJob{}, err
	}
	j.Payload = nil
	return j, nil
}

// CancelBatch cancels a non-terminal job. The cancel is one compare-and-set from
// the state it read: when it loses a race it re-reads ONCE and cancels from the
// new state while the job is non-terminal, else it returns an ErrJobConflict
// (the HTTP surface's 409). The remote cancel is sent only after the
// JobCanceled transition commits, so a poll that read the job as Submitted can
// never stage an apply run for it. A job whose results are already complete
// (Applying) is canceled without a remote call.
func (r *Router) CancelBatch(ctx context.Context, id BatchJobID) error {
	if r.jobs == nil {
		return ErrNoJobStore
	}
	var canceled StoredJob
	var lost error
	for range 2 {
		cur, err := r.jobs.Get(ctx, id)
		if err != nil {
			return err
		}
		if cur.State.Terminal() {
			return fmt.Errorf("%w: job %s is already %s", ErrJobConflict, id, cur.State)
		}
		next := cur
		next.State, next.Updated = JobCanceled, r.now()
		err = r.transitionJob(ctx, id, cur.State, next)
		if err == nil {
			canceled = cur
			lost = nil
			break
		}
		if !lostRace(err) {
			return err
		}
		lost = err
	}
	if lost != nil {
		return fmt.Errorf("%w: job %s changed twice while it was being canceled: %w", ErrJobConflict, id, lost)
	}
	if canceled.State == JobApplying {
		return nil
	}
	return r.cancelRemote(context.WithoutCancel(ctx), canceled)
}

// cancelRemote cancels the provider's batch of a job the store already shows
// canceled. A job with no recorded handle may still have a batch at the
// provider (the submit that was in flight), found by its tag.
func (r *Router) cancelRemote(ctx context.Context, j StoredJob) error {
	backend, release, err := integrations.Lease[Backend](ctx, r.reg, j.Endpoint)
	if err != nil {
		return fmt.Errorf("ai: job %s is canceled but its endpoint is unavailable for the remote cancel: %w", j.ID, err)
	}
	defer release()
	bb, ok := backend.(BatchBackend)
	if !ok {
		return nil
	}
	handles := []BatchHandle{j.Handle}
	if j.Handle == "" {
		if handles, err = bb.FindBatches(ctx, map[string]string{JobTagKey: string(j.ID)}); err != nil {
			return fmt.Errorf("ai: job %s is canceled but its remote batch could not be looked up: %w", j.ID, err)
		}
	}
	var errs []error
	for _, h := range handles {
		if err := bb.CancelBatch(ctx, h); err != nil {
			errs = append(errs, err)
		}
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("ai: job %s is canceled but the remote cancel failed: %w", j.ID, err)
	}
	return nil
}

// ---- the module's apply task --------------------------------------------------------

// BatchHandler applies results. It runs inside a MODULE task run
// (BatchApplyTask) and writes module entities only through rc.Writer(), under
// the Effects the module declared (COR2-11). It MUST be idempotent: a crash
// between the apply and the job's completed mark replays it, and an apply run
// that loses a race to a cancel has already committed its writes.
type BatchHandler func(rc *tasks.RC, job BatchJob, results iter.Seq2[BatchResult, error]) (BatchOutcome, error)

// BatchApplyParams is the apply task's params: the job to apply.
type BatchApplyParams struct {
	Job BatchJobID `json:"job"`
}

// BatchApplyResult is the apply task's result.
type BatchApplyResult struct {
	Applied  int    `json:"applied"`
	Skipped  int    `json:"skipped"`
	Failed   int    `json:"failed"`
	JobState string `json:"job_state"` // the job's state when the run ended
}

// applyTimeout bounds one apply run attempt.
func applyTimeout() time.Duration { return 30 * time.Minute }

// applyKindName is the apply kind of a capability ID "<module>.<name>":
// "<module>.ai.batch-apply.<name>" with underscores written as hyphens, the
// task Kind grammar's alphabet (books.filename_parse is
// books.ai.batch-apply.filename-parse).
func applyKindName(capabilityID string) string {
	module, name, _ := strings.Cut(capabilityID, ".")
	dash := strings.NewReplacer("_", "-")
	return dash.Replace(module) + ".ai.batch-apply." + dash.Replace(name)
}

// BatchApplyTask returns the module task kind that applies c's finished batch
// jobs: "<module>.ai.batch-apply.<name>" (the capability name with "_" -> "-",
// e.g. books.ai.batch-apply.author-dedup-batch). The module registers it with
// r.Tasks(...); effects must name the module's own resources (05-tasks
// registration rule). The run streams the results to h, adds their token counts
// to the usage metrics (mode=batch), and marks the job completed. A capability
// with no registered apply task cannot SubmitBatch (ErrNoBatchApply).
//
// The run raises the job's apply-attempt count when it starts (a same-state
// transition) and fails the job when the count would pass
// core.ai.batch_max_apply_attempts; a run whose job is terminal ends without
// applying anything.
//
// A zero or foreign handle, or a nil h, yields a definition whose Run is nil,
// which tasks.Problems reports ("Task has no Run"), so registration fails
// closed.
//
//tasklint:constructor the module passes the result to Registrar.Tasks in its Register; this package registers nothing
func BatchApplyTask(c Capability, h BatchHandler, effects tasks.Effects) tasks.Definition {
	kind := tasks.Kind("core.ai.batch-apply.unregistered")
	var run func(rc *tasks.RC, p BatchApplyParams) (BatchApplyResult, error)
	if c.r != nil && c.id != "" && c.r.HasCapability(c.id) {
		name := applyKindName(c.id)
		kind = tasks.Kind(name)
		c.r.setApplyKind(c.id, name)
		if h != nil {
			r := c.r
			run = func(rc *tasks.RC, p BatchApplyParams) (BatchApplyResult, error) { return r.applyRun(rc, c, h, p) }
		}
	}
	return tasks.Task(kind, tasks.TaskSpec[BatchApplyParams, BatchApplyResult]{
		Title:   "Apply AI batch results",
		Help:    "Applies the finished provider batch of one job through the module's handler and marks the job completed.",
		Effects: effects,
		Perms:   tasks.Permissions{Start: rbac.Need("core:integrations.manage")},
		Resume:  tasks.ResumeFromZero,
		Timeout: applyTimeout(),
		// A poll that finds the job still Applying stages another run; a
		// start that finds one QUEUED for the same job folds into it. A
		// running run is never folded into, so the new one queues behind it.
		Dedupe: tasks.DedupePolicy{Key: func(params json.RawMessage) (string, error) {
			var p BatchApplyParams
			if err := json.Unmarshal(params, &p); err != nil {
				return "", err
			}
			return string(p.Job), nil
		}},
		Run: run,
	})
}

// usageSum totals the token usage of the results a handler consumed.
type usageSum struct {
	mu sync.Mutex
	u  Usage
}

func (s *usageSum) add(res BatchResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if res.Text != nil {
		s.u = addUsage(s.u, res.Text.Usage)
	}
	if res.Embed != nil {
		s.u = addUsage(s.u, res.Embed.Usage)
	}
}

func (s *usageSum) total() Usage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.u
}

// applyRun is the body of the module's apply task.
func (r *Router) applyRun(rc *tasks.RC, c Capability, h BatchHandler, p BatchApplyParams) (BatchApplyResult, error) {
	ctx := rc.Context()
	if r.jobs == nil {
		return BatchApplyResult{}, ErrNoJobStore
	}
	cfg := r.config()
	job, err := r.jobs.Get(ctx, p.Job)
	if err != nil {
		return BatchApplyResult{}, err
	}
	if job.Capability != c.id {
		return BatchApplyResult{}, fmt.Errorf("ai: job %s is a %s job, not %s", p.Job, oneLine(job.Capability), c.id)
	}
	res := BatchApplyResult{JobState: job.State.String()}
	if job.State.Terminal() {
		return res, nil // a completed job is never applied twice; a canceled one never
	}
	if job.State != JobApplying {
		return res, fmt.Errorf("ai: job %s is %s, not applying", p.Job, job.State)
	}

	job, done, err := r.raiseAttempts(ctx, job, cfg)
	res.JobState = job.State.String()
	if done || err != nil {
		return res, err
	}

	backend, release, err := integrations.Lease[Backend](ctx, r.reg, job.Endpoint)
	if err != nil {
		return res, fmt.Errorf("ai: job %s: its endpoint is unavailable for the results: %w", job.ID, err)
	}
	defer release()
	bb, ok := backend.(BatchBackend)
	if !ok {
		return res, fmt.Errorf("ai: job %s: the adapter of %s has no batch API", job.ID, job.Endpoint)
	}
	results, err := bb.BatchResults(ctx, job.Handle)
	if err != nil {
		return res, fmt.Errorf("ai: job %s: fetch the batch results: %w", job.ID, err)
	}
	var sum usageSum
	counted := func(yield func(BatchResult, error) bool) {
		for row, rerr := range results {
			if rerr == nil {
				sum.add(row)
			}
			if !yield(row, rerr) {
				return
			}
		}
	}
	outcome, err := h(rc, job, counted)
	res.Applied, res.Skipped, res.Failed = outcome.Applied, outcome.Skipped, outcome.Failed
	if err != nil {
		return res, err // the job stays Applying; the next poll starts a new run
	}

	completed, state, err := r.completeJob(ctx, job.ID)
	res.JobState = state.String()
	if err != nil {
		return res, err
	}
	if completed {
		r.m.usage(ctx, job.Capability, job.Endpoint, r.modelLabel(r.endpointModel(job)), modeBatch, sum.total())
	}
	return res, nil
}

// endpointModel is the model job's endpoint uses for its capability today, or
// "" when the endpoint is gone.
func (r *Router) endpointModel(job StoredJob) string {
	cfg, ok := r.reg.Config(job.Endpoint)
	if !ok {
		return ""
	}
	set, err := decodeSettings(cfg.Settings)
	if err != nil {
		return ""
	}
	e := r.entry(job.Capability)
	if e == nil {
		return ""
	}
	m, _ := modelFor(set, e.spec)
	return m
}

// raiseAttempts is an apply run's own compare-and-set: it counts this run in
// ApplyAttempts, and fails the job when that would pass the limit. It returns
// the job as it now stands; done is true when the run must stop (the job is
// terminal now, or this run just failed it). A lost race re-reads.
func (r *Router) raiseAttempts(ctx context.Context, job StoredJob, cfg Config) (StoredJob, bool, error) {
	for range 3 {
		if job.State.Terminal() {
			return job, true, nil
		}
		next := job
		next.Updated = r.now()
		exhausted := job.ApplyAttempts+1 > cfg.BatchMaxApplyAttempts
		if exhausted {
			next.State = JobFailed
		} else {
			next.ApplyAttempts++
		}
		err := r.transitionJob(ctx, job.ID, job.State, next)
		switch {
		case err == nil && exhausted:
			next.Payload = nil
			return next, true, fmt.Errorf("ai: job %s failed: %d apply attempts did not complete it", job.ID, cfg.BatchMaxApplyAttempts)
		case err == nil:
			return next, false, nil
		case !lostRace(err):
			return job, true, err
		}
		cur, gerr := r.jobs.Get(ctx, job.ID)
		if gerr != nil {
			return job, true, gerr
		}
		job = cur
	}
	return job, true, fmt.Errorf("%w: job %s kept changing under the apply run", ErrJobConflict, job.ID)
}

// completeJob marks the job Completed in the run's own compare-and-set. It
// checks the run's context first (a canceled run must not mark anything), and a
// job that is terminal by now (canceled while the handler ran) is left as it is:
// the handler's writes are idempotent and stay. It reports whether this call
// completed the job, and the job's state when it stopped.
func (r *Router) completeJob(ctx context.Context, id BatchJobID) (bool, JobState, error) {
	state := JobApplying
	for range 3 {
		if err := ctx.Err(); err != nil {
			return false, state, err
		}
		cur, err := r.jobs.Get(ctx, id)
		if err != nil {
			return false, state, err
		}
		state = cur.State
		if cur.State.Terminal() {
			return false, state, nil
		}
		next := cur
		next.State, next.Updated = JobCompleted, r.now()
		err = r.transitionJob(ctx, id, cur.State, next)
		switch {
		case err == nil:
			return true, JobCompleted, nil
		case !lostRace(err):
			return false, state, err
		}
	}
	return false, state, fmt.Errorf("%w: job %s kept changing while it was being completed", ErrJobConflict, id)
}
