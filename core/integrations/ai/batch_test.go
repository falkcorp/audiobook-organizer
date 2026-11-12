// file: core/integrations/ai/batch_test.go
// version: 1.0.0
// guid: e3b9d6f4-2a71-4c58-9d03-6f1a8e5c7b92
// last-edited: 2026-11-12

package ai_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/falkcorp/mediacore/coretest"
	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/integrations/ai"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/keys"
	"github.com/falkcorp/mediacore/tasks"
)

// applyLog is the module's apply handler: it records every visit, and
// "applies" a row by setting a flag keyed by its custom ID, which is what makes
// a replay harmless (the handler's contract: idempotent).
type applyLog struct {
	mu      sync.Mutex
	calls   int
	seen    map[string]int  // handler visits per custom ID
	effects map[string]bool // the module's idempotent writes
	// failAfter makes the first failRuns runs return an error after that many rows.
	failAfter int
	failRuns  int
}

func newApplyLog() *applyLog {
	return &applyLog{seen: map[string]int{}, effects: map[string]bool{}}
}

func (l *applyLog) handle(_ *tasks.RC, _ ai.BatchJob, results iter.Seq2[ai.BatchResult, error]) (ai.BatchOutcome, error) {
	l.mu.Lock()
	l.calls++
	run := l.calls
	l.mu.Unlock()
	var out ai.BatchOutcome
	for row, err := range results {
		if err != nil {
			return out, err
		}
		l.mu.Lock()
		l.seen[row.CustomID]++
		l.effects[row.CustomID] = true
		l.mu.Unlock()
		out.Applied++
		if run <= l.failRuns && out.Applied >= l.failAfter {
			return out, errors.New("the Batmobile ran out of gas halfway through the batch")
		}
	}
	return out, nil
}

func (l *applyLog) callCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.calls
}

// batchEnv is a harness with one batch capability, its apply task registered,
// and one batch-capable endpoint.
type batchEnv struct {
	*harness
	tt  *testing.T
	c   ai.Capability
	f   *coretest.FakeAI
	ref integrations.Ref
	log *applyLog
	def tasks.Definition
}

const applyKind = "gotham.ai.batch-apply.riddle-batch"

func newBatchEnv(t *testing.T, opts ...harnessOption) *batchEnv {
	return newBatchEnvFor(t, func(f *coretest.FakeAI) ai.Backend { return f }, opts...)
}

// newBatchEnvFor serves the endpoint through wrap(f), so a test can intercept a
// call the FakeAI would otherwise answer.
func newBatchEnvFor(t *testing.T, wrap func(*coretest.FakeAI) ai.Backend, opts ...harnessOption) *batchEnv {
	t.Helper()
	h := newHarness(t, opts...)
	c := h.capability("riddle_batch")
	log := newApplyLog()
	def := registerApply(t, c, log.handle)
	f := fake()
	set := textEndpoint("joker-7b", "gotham.riddle_batch")
	set.Features = []ai.Feature{ai.FeatureBatch}
	ref := h.addEndpoint("arkham", 1, set, wrap(f))
	return &batchEnv{harness: h, tt: t, c: c, f: f, ref: ref, log: log, def: def}
}

// items is n batch items r1..rn.
func items(n int) []ai.BatchItem {
	out := make([]ai.BatchItem, n)
	for i := range out {
		out[i] = ai.BatchItem{CustomID: fmt.Sprintf("r%d", i+1), Request: text(fmt.Sprintf("riddle %d", i+1))}
	}
	return out
}

func (e *batchEnv) submit(n int) ai.BatchJobID {
	e.t.Helper()
	id, err := e.r.SubmitBatch(context.Background(), e.c, items(n), []byte(`{"case":"the Riddler's last riddle"}`))
	if err != nil {
		e.t.Fatalf("SubmitBatch: %v", err)
	}
	return id
}

// complete finishes the remote batch of the job (the only one the fake holds).
func (e *batchEnv) complete() {
	e.t.Helper()
	remote := e.f.RemoteBatches()
	if len(remote) != 1 {
		e.t.Fatalf("%d remote batches, want 1", len(remote))
	}
	e.f.CompleteBatch(remote[0])
}

func (e *batchEnv) poll() (ai.BatchPollResult, error) {
	return e.r.PollBatches(newRC(e.t), ai.BatchPollParams{})
}

func (e *batchEnv) mustPoll() ai.BatchPollResult {
	e.t.Helper()
	res, err := e.poll()
	if err != nil {
		e.t.Fatalf("PollBatches: %v", err)
	}
	return res
}

// apply runs the module's apply task for the job the way the executor does.
func (e *batchEnv) apply(id ai.BatchJobID) (ai.BatchApplyResult, error) {
	e.t.Helper()
	raw, err := runDefinition(e.tt, e.def, newApplyRC(e.t, e.def.Kind()), fmt.Sprintf(`{"job":%q}`, id))
	var res ai.BatchApplyResult
	if len(raw) > 0 {
		if uerr := json.Unmarshal(raw, &res); uerr != nil {
			e.t.Fatalf("apply result %s: %v", raw, uerr)
		}
	}
	return res, err
}

// spyBackend intercepts SubmitBatch of an embedded FakeAI.
type spyBackend struct {
	*coretest.FakeAI
	onSubmit func(ctx context.Context, tags map[string]string)
}

func (s spyBackend) SubmitBatch(ctx context.Context, model string, it []ai.BatchItem, tags map[string]string) (ai.BatchHandle, error) {
	if s.onSubmit != nil {
		s.onSubmit(ctx, tags)
	}
	return s.FakeAI.SubmitBatch(ctx, model, it, tags)
}

// I4: the job row commits before the remote submit, and the remote batch is
// tagged with the job ID, so a crash in between leaves something to reconcile.
func TestBatch_JobRowBeforeRemote(t *testing.T) {
	t.Parallel()
	var e *batchEnv
	var sawRow ai.StoredJob
	var sawErr error
	e = newBatchEnvFor(t, func(f *coretest.FakeAI) ai.Backend {
		return spyBackend{FakeAI: f, onSubmit: func(ctx context.Context, tags map[string]string) {
			sawRow, sawErr = e.jobs.Get(ctx, ai.BatchJobID(tags[ai.JobTagKey]))
		}}
	})
	id := e.submit(3)

	if sawErr != nil {
		t.Fatalf("the job row was not readable when the remote call was made: %v", sawErr)
	}
	if sawRow.ID != id || sawRow.State != ai.JobPending || sawRow.Endpoint != e.ref || sawRow.Handle != "" {
		t.Errorf("at the remote call the job was %+v, want Pending on the endpoint with no handle", sawRow)
	}
	if string(sawRow.Payload) != `{"case":"the Riddler's last riddle"}` || sawRow.Capability != "gotham.riddle_batch" ||
		sawRow.ApplyKind != applyKind || sawRow.Module != "gotham" {
		t.Errorf("the pre-remote row lacks the payload, capability, module or apply kind: %+v", sawRow)
	}
	remote := e.f.RemoteBatches()
	if len(remote) != 1 || e.f.RemoteBatchTags(remote[0])[ai.JobTagKey] != string(id) {
		t.Fatalf("remote batches %v tags %v; want one tagged with job %s", remote, e.f.RemoteBatchTags(remote[0]), id)
	}
	done := e.mustJob(id)
	if done.State != ai.JobSubmitted || done.Handle != remote[0] {
		t.Errorf("after the submit the job is %+v, want Submitted with handle %s", done, remote[0])
	}
	view, err := e.r.BatchJob(context.Background(), id)
	if err != nil || view.Payload != nil || view.ID != id {
		t.Errorf("BatchJob = %+v, %v; the view omits the payload", view, err)
	}
	for j, err := range e.jobs.Open(context.Background()) {
		if err != nil || j.Payload != nil {
			t.Errorf("Open yielded %+v, %v; payload is for Get only", j, err)
		}
	}
	if _, err := e.r.BatchJob(context.Background(), "no-such-job"); !errors.Is(err, ai.ErrJobNotFound) {
		t.Errorf("an unknown job = %v, want ErrJobNotFound", err)
	}
}

// SubmitBatch refuses, storing nothing, when the capability has no apply task,
// no endpoint is capable, or the items are not a usable set.
func TestBatch_SubmitWithoutApplyTaskRefused(t *testing.T) {
	t.Parallel()
	e := newBatchEnv(t)
	bare := e.capability("no_apply")
	set := textEndpoint("joker-7b", "gotham.no_apply")
	set.Features = []ai.Feature{ai.FeatureBatch}
	e.addEndpoint("blackgate", 2, set, fake())

	open := func() int {
		n := 0
		for _, err := range e.jobs.Open(context.Background()) {
			if err != nil {
				t.Fatal(err)
			}
			n++
		}
		return n
	}
	before := e.f.BatchCallCount()
	if _, err := e.r.SubmitBatch(context.Background(), bare, items(1), nil); !errors.Is(err, ai.ErrNoBatchApply) {
		t.Fatalf("a capability with no apply task = %v, want ErrNoBatchApply: results nobody can apply would be paid for and dropped", err)
	}
	if open() != 0 {
		t.Error("a refused submit stored a job")
	}
	for name, its := range map[string][]ai.BatchItem{
		"no items":            nil,
		"an empty custom ID":  {{CustomID: "", Request: text("x")}},
		"a duplicate custom":  {{CustomID: "a", Request: text("x")}, {CustomID: "a", Request: text("y")}},
		"a second duplicated": {{CustomID: "a", Request: text("x")}, {CustomID: "b", Request: text("y")}, {CustomID: "b", Request: text("z")}},
	} {
		if _, err := e.r.SubmitBatch(context.Background(), e.c, its, nil); err == nil {
			t.Errorf("%s: SubmitBatch accepted it", name)
		}
	}
	if open() != 0 || e.f.BatchCallCount() != before {
		t.Error("a refused submit stored a job or reached the provider")
	}

	// An endpoint without batch_api is not capable of a batch.
	plain := newHarness(t)
	c := plain.capability("riddle_batch")
	registerApply(t, c, noopHandler)
	f := fake()
	plain.addEndpoint("arkham", 1, textEndpoint("joker-7b", "gotham.riddle_batch"), f)
	if _, err := plain.r.SubmitBatch(context.Background(), c, items(1), nil); !errors.Is(err, ai.ErrNoCapableEndpoint) {
		t.Errorf("an endpoint without batch_api = %v, want ErrNoCapableEndpoint", err)
	}
	if f.BatchCallCount() != 0 {
		t.Error("a batch went to an endpoint without batch_api")
	}

	// BatchApplyTask with a nil handler, or a handle another router minted,
	// registers nothing: the definition has no Run and Problems says so.
	if probs := tasks.Problems(ai.BatchApplyTask(e.c, nil, applyEffects())); len(probs) == 0 {
		t.Error("BatchApplyTask(nil handler) has no problems; registration must fail closed")
	}
	if probs := tasks.Problems(ai.BatchApplyTask(ai.Capability{}, noopHandler, applyEffects())); len(probs) == 0 {
		t.Error("a zero handle produced a registrable task")
	}
	// The apply kind belongs to the router that registered it.
	if other := newHarness(t); other.r.HasApplyKind(applyKind) {
		t.Error("another router knows this router's apply kind")
	}
}

// Submission failover, and what an ambiguous failure leaves behind.
func TestBatch_SubmitFailoverAndAmbiguity(t *testing.T) {
	t.Parallel()
	rate := &integrations.Error{Class: integrations.ClassRateLimited, Err: errors.New("429")}
	t.Run("a definite refusal fails over and the job follows", func(t *testing.T) {
		t.Parallel()
		e := newBatchEnv(t)
		e.f.FailSubmit(rate)
		second := fake()
		set := textEndpoint("joker-7b", "gotham.riddle_batch")
		set.Features = []ai.Feature{ai.FeatureBatch}
		ref2 := e.addEndpoint("blackgate", 2, set, second)
		id := e.submit(2)
		j := e.mustJob(id)
		if j.Endpoint != ref2 || j.State != ai.JobSubmitted || len(second.RemoteBatches()) != 1 {
			t.Errorf("job = %+v; want Submitted on blackgate with its batch there", j)
		}
		if len(e.f.RemoteBatches()) != 0 {
			t.Error("the refused endpoint holds a batch")
		}
	})
	t.Run("every endpoint refusing fails the job", func(t *testing.T) {
		t.Parallel()
		e := newBatchEnv(t)
		e.f.FailSubmit(rate)
		_, err := e.r.SubmitBatch(context.Background(), e.c, items(1), nil)
		if _, ok := errors.AsType[*ai.ExhaustedError](err); !ok {
			t.Fatalf("err = %v, want ExhaustedError", err)
		}
		for j, jerr := range e.jobs.Open(context.Background()) {
			t.Errorf("open job %+v %v after a definite failure", j, jerr)
		}
	})
	t.Run("a rejected batch is returned as is and fails the job", func(t *testing.T) {
		t.Parallel()
		e := newBatchEnv(t)
		e.f.FailSubmit(&integrations.Error{Class: integrations.ClassInvalid, Err: errors.New("400")})
		second := fake()
		set := textEndpoint("joker-7b", "gotham.riddle_batch")
		set.Features = []ai.Feature{ai.FeatureBatch}
		e.addEndpoint("blackgate", 2, set, second)
		_, err := e.r.SubmitBatch(context.Background(), e.c, items(1), nil)
		if integrations.ClassOf(context.Background(), err) != integrations.ClassInvalid {
			t.Fatalf("err = %v, want the Invalid refusal as is", err)
		}
		if second.BatchCallCount() != 0 {
			t.Error("an Invalid batch went to a second endpoint")
		}
	})
	t.Run("an ambiguous failure leaves the job and says do not resubmit", func(t *testing.T) {
		t.Parallel()
		e := newBatchEnv(t)
		e.f.FailSubmit(&integrations.Error{Class: integrations.ClassTransient, Err: errors.New("connection reset")})
		second := fake()
		set := textEndpoint("joker-7b", "gotham.riddle_batch")
		set.Features = []ai.Feature{ai.FeatureBatch}
		e.addEndpoint("blackgate", 2, set, second)
		id, err := e.r.SubmitBatch(context.Background(), e.c, items(1), nil)
		if !errors.Is(err, ai.ErrSubmitUnconfirmed) || id == "" {
			t.Fatalf("id=%q err=%v, want the job ID with ErrSubmitUnconfirmed", id, err)
		}
		if second.BatchCallCount() != 0 {
			t.Error("an ambiguous failure failed over; the provider may already hold the batch, so a second one could be paid for twice")
		}
		if j := e.mustJob(id); j.State != ai.JobPending {
			t.Errorf("job = %+v, want Pending for the poll's reconcile", j)
		}
	})
	// BadOutput on a create looks like a refusal and is not one: a 2xx the
	// adapter could not decode, or one with no batch ID, is a provider that
	// said yes in words nobody could read, so the batch may exist.
	for name, class := range map[string]integrations.Class{
		"an unreadable 2xx":   integrations.ClassBadOutput,
		"an unclassified one": integrations.ClassUnknown,
	} {
		t.Run(name+" leaves the job Pending for the tag reconcile", func(t *testing.T) {
			t.Parallel()
			e := newBatchEnv(t)
			e.f.FailSubmit(&integrations.Error{Class: class, Err: errors.New("the answer made no sense")})
			second := fake()
			set := textEndpoint("joker-7b", "gotham.riddle_batch")
			set.Features = []ai.Feature{ai.FeatureBatch}
			e.addEndpoint("blackgate", 2, set, second)
			id, err := e.r.SubmitBatch(context.Background(), e.c, items(1), nil)
			if !errors.Is(err, ai.ErrSubmitUnconfirmed) || id == "" {
				t.Fatalf("id=%q err=%v, want the job ID with ErrSubmitUnconfirmed", id, err)
			}
			if second.BatchCallCount() != 0 {
				t.Error("an unreadable answer failed over; the provider may already hold the batch")
			}
			if j := e.mustJob(id); j.State != ai.JobPending {
				t.Errorf("job = %+v, want Pending: failing it would orphan a batch the provider accepted", j)
			}
		})
	}
}

// NotAccepted is the one predicate for "the provider did not create the
// batch", shared by the router (may the Pending job be failed?) and the adapter
// (is the uploaded input garbage?). Only a refusal or a request never sent
// counts; every class that can follow an accepted request does not.
func TestNotAccepted(t *testing.T) {
	t.Parallel()
	for c, want := range map[integrations.Class]bool{
		integrations.ClassRefusedLocal: true,
		integrations.ClassRateLimited:  true,
		integrations.ClassQuota:        true,
		integrations.ClassAuth:         true,
		integrations.ClassUnsupported:  true,
		integrations.ClassInvalid:      true,
		integrations.ClassNotFound:     true,
		integrations.ClassTransient:    false,
		integrations.ClassBadOutput:    false,
		integrations.ClassUnknown:      false,
		integrations.ClassCanceled:     false,
	} {
		if got := ai.NotAccepted(c); got != want {
			t.Errorf("NotAccepted(%s) = %v, want %v", c, got, want)
		}
	}
}

// A submit whose answer was lost leaves a Pending job and a tagged remote
// batch; the poll finds it by the tag and records the handle. A Pending job
// with no remote batch is left alone for the grace period and failed after.
func TestBatch_OrphanReconcileByTag(t *testing.T) {
	t.Parallel()
	e := newBatchEnv(t)
	e.f.LoseSubmitResponse(true)
	id, err := e.r.SubmitBatch(context.Background(), e.c, items(2), []byte(`{}`))
	if !errors.Is(err, ai.ErrSubmitUnconfirmed) || id == "" {
		t.Fatalf("id=%q err=%v, want ErrSubmitUnconfirmed with the job ID", id, err)
	}
	if j := e.mustJob(id); j.State != ai.JobPending || j.Handle != "" {
		t.Fatalf("job = %+v, want Pending with no handle", j)
	}
	remote := e.f.RemoteBatches()
	if len(remote) != 1 {
		t.Fatalf("%d remote batches; the provider kept the one whose answer was lost", len(remote))
	}

	res := e.mustPoll()
	if res.Polled != 1 || res.Failed != 0 {
		t.Errorf("poll = %+v", res)
	}
	j := e.mustJob(id)
	if j.State != ai.JobSubmitted || j.Handle != remote[0] {
		t.Fatalf("after the poll: %+v, want Submitted with the orphan's handle %s", j, remote[0])
	}
	// And the reconciled job runs to completion like any other.
	e.f.LoseSubmitResponse(false)
	e.complete()
	if res := e.mustPoll(); res.ApplyStaged != 1 {
		t.Errorf("poll after completion = %+v, want one apply run staged", res)
	}

	// No remote batch at all: inside the grace period nothing happens, after it
	// the job fails (its items are not stored, so it cannot be resubmitted).
	lone := newBatchEnv(t)
	lone.f.FailSubmit(&integrations.Error{Class: integrations.ClassTransient, Err: errors.New("reset")})
	lid, err := lone.r.SubmitBatch(context.Background(), lone.c, items(1), nil)
	if !errors.Is(err, ai.ErrSubmitUnconfirmed) {
		t.Fatal(err)
	}
	lone.mustPoll()
	if j := lone.mustJob(lid); j.State != ai.JobPending {
		t.Fatalf("inside the grace period the job is %s, want it left Pending", j.State)
	}
	lone.clk.Advance(59 * time.Minute)
	lone.mustPoll()
	if j := lone.mustJob(lid); j.State != ai.JobPending {
		t.Fatalf("at 59 minutes the job is %s, want it still Pending", j.State)
	}
	lone.clk.Advance(2 * time.Minute)
	res = lone.mustPoll()
	if j := lone.mustJob(lid); j.State != ai.JobFailed || res.Failed != 1 {
		t.Errorf("after the grace period the job is %s (poll %+v), want Failed", j.State, res)
	}
}

// I4: the poll moves a finished job to Applying and stages the module's apply
// run in the same commit; it never calls the handler and never raises the
// attempt count. A job already Applying gets another run staged (a failed run
// leaves it there).
func TestBatch_PollNeverCallsHandler(t *testing.T) {
	t.Parallel()
	e := newBatchEnv(t)
	id := e.submit(3)

	// Still in progress at the provider: nothing staged.
	if res := e.mustPoll(); res.Polled != 1 || res.ApplyStaged != 0 {
		t.Errorf("poll of a running batch = %+v", res)
	}
	if len(e.stager.Started()) != 0 {
		t.Fatal("a run was staged for a batch the provider has not finished")
	}

	e.complete()
	res := e.mustPoll()
	if res.Polled != 1 || res.ApplyStaged != 1 || res.Failed != 0 {
		t.Errorf("poll = %+v, want one apply run staged", res)
	}
	j := e.mustJob(id)
	if j.State != ai.JobApplying || j.ApplyAttempts != 0 {
		t.Errorf("job = %+v, want Applying with the attempt count untouched (the apply run raises it)", j)
	}
	runs := e.stager.Started()
	if len(runs) != 1 || runs[0].Kind != applyKind || runs[0].Params.Job != id {
		t.Fatalf("staged runs = %+v, want one %s run for job %s", runs, applyKind, id)
	}

	// Applying: another run is staged, still without a handler call.
	e.mustPoll()
	if got := len(e.stager.Started()); got != 2 {
		t.Errorf("%d staged runs after polling an Applying job, want 2", got)
	}
	if j := e.mustJob(id); j.State != ai.JobApplying || j.ApplyAttempts != 0 {
		t.Errorf("job = %+v after the second poll", j)
	}
	if n := e.log.callCount(); n != 0 {
		t.Errorf("the handler ran %d time(s) under the poll; only the module's apply run may call it", n)
	}

	// A batch the provider failed, expired or canceled fails the job.
	for name, fail := range map[string]func(f *coretest.FakeAI, h ai.BatchHandle){
		"failed":   func(f *coretest.FakeAI, h ai.BatchHandle) { f.FailBatch(h) },
		"canceled": func(f *coretest.FakeAI, h ai.BatchHandle) { _ = f.CancelBatch(context.Background(), h) },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			x := newBatchEnv(t)
			xid := x.submit(1)
			fail(x.f, x.f.RemoteBatches()[0])
			res := x.mustPoll()
			if got := x.mustJob(xid).State; got != ai.JobFailed || res.Failed != 1 {
				t.Errorf("a %s remote batch left the job %s (poll %+v), want Failed", name, got, res)
			}
			if len(x.stager.Started()) != 0 {
				t.Error("a run was staged for a failed batch")
			}
		})
	}
}

// The apply task is the module's: it carries the module's declared Effects and
// kind name, runs the module's handler, and a router with no stager fails the
// poll closed instead of dropping the job.
func TestBatch_ApplyRunUsesModuleEffects(t *testing.T) {
	t.Parallel()
	e := newBatchEnv(t)
	if got := e.def.Kind(); got != applyKind {
		t.Errorf("kind = %q, want %q (capability name with _ written as -)", got, applyKind)
	}
	if probs := tasks.Problems(e.def); len(probs) != 0 {
		t.Errorf("the apply task has registration problems: %v", probs)
	}
	if got := tasks.CommonOf(e.def).Effects; !reflect.DeepEqual(got, applyEffects()) {
		t.Errorf("Effects = %+v, want the module's own %+v (never a core resource)", got, applyEffects())
	}
	if !e.r.HasApplyKind(applyKind) || e.r.HasApplyKind("gotham.ai.batch-apply.other") {
		t.Error("the catalog does not report exactly the registered apply kind")
	}

	id := e.submit(3)
	e.complete()
	e.mustPoll()
	res, err := e.apply(id)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if res.Applied != 3 || res.JobState != "completed" {
		t.Errorf("apply result = %+v, want 3 applied and the job completed", res)
	}
	if got := e.mustJob(id); got.State != ai.JobCompleted || got.ApplyAttempts != 1 {
		t.Errorf("job = %+v, want Completed after one apply attempt", got)
	}
	if n := e.log.callCount(); n != 1 || len(e.log.effects) != 3 {
		t.Errorf("handler ran %d time(s) with %d effects, want 1 and 3", n, len(e.log.effects))
	}
}

// A router built without a Stager cannot stage the apply run of a finished job,
// so its poll fails closed: the job is neither dropped nor failed, and a router
// that has one picks it up.
func TestBatch_PollWithoutStagerFailsClosed(t *testing.T) {
	t.Parallel()
	e := newBatchEnv(t, withoutStager())
	id := e.submit(2)
	e.complete()
	if _, err := e.poll(); err == nil {
		t.Fatal("a poll with nothing to stage the run through reported success")
	}
	if j := e.mustJob(id); j.State != ai.JobSubmitted {
		t.Errorf("job = %+v, want it left Submitted for a poll that can stage", j)
	}
}

// The handler may run more than once for a job (a crash between its writes and
// the job's completed mark; an apply run that loses a race to a cancel): the
// router replays it until the job completes, and a finished job is never
// applied again.
func TestBatch_HandlerReplayIdempotent(t *testing.T) {
	t.Parallel()
	e := newBatchEnv(t)
	e.log.failAfter, e.log.failRuns = 2, 1
	id := e.submit(4)
	e.complete()
	e.mustPoll()

	_, err := e.apply(id)
	if err == nil {
		t.Fatal("the first run should have failed halfway")
	}
	if j := e.mustJob(id); j.State != ai.JobApplying || j.ApplyAttempts != 1 {
		t.Fatalf("after a failed run the job is %+v, want Applying with 1 attempt", j)
	}
	if n := e.log.callCount(); n != 1 {
		t.Errorf("the handler ran %d time(s) in the first run", n)
	}

	// The next poll stages another run; the replayed handler sees every row again.
	before := len(e.stager.Started())
	e.mustPoll()
	if len(e.stager.Started()) != before+1 {
		t.Fatalf("the poll did not stage a replay run")
	}
	res, err := e.apply(id)
	if err != nil {
		t.Fatalf("the replay run: %v", err)
	}
	if res.JobState != "completed" || res.Applied != 4 {
		t.Errorf("replay result = %+v", res)
	}
	if j := e.mustJob(id); j.State != ai.JobCompleted || j.ApplyAttempts != 2 {
		t.Errorf("job = %+v, want Completed with 2 attempts", j)
	}
	e.log.mu.Lock()
	seen1, seen3, nEffects := e.log.seen["r1"], e.log.seen["r3"], len(e.log.effects)
	e.log.mu.Unlock()
	if seen1 != 2 || seen3 != 1 || nEffects != 4 {
		t.Errorf("visits r1=%d r3=%d, effects=%d; want the replay to revisit r1 (2), r3 once, and every row applied exactly as a set (4)", seen1, seen3, nEffects)
	}

	// Completed is final: another run does nothing and the handler stays quiet.
	calls := e.log.callCount()
	res, err = e.apply(id)
	if err != nil || res.JobState != "completed" || res.Applied != 0 {
		t.Errorf("a run on a completed job = %+v, %v", res, err)
	}
	if e.log.callCount() != calls {
		t.Error("the handler ran again for a completed job")
	}
}

// An apply run is bound to its capability: a run of one capability's task
// refuses a job another capability submitted.
func TestBatch_ApplyRefusesAnotherCapabilitysJob(t *testing.T) {
	t.Parallel()
	e := newBatchEnv(t)
	other := e.capability("cipher_batch")
	otherLog := newApplyLog()
	otherDef := registerApply(t, other, otherLog.handle)
	id := e.submit(1)
	e.complete()
	e.mustPoll()

	_, err := runDefinition(t, otherDef, newApplyRC(t, otherDef.Kind()), fmt.Sprintf(`{"job":%q}`, id))
	if err == nil {
		t.Fatal("another capability's apply run accepted the job")
	}
	if otherLog.callCount() != 0 {
		t.Error("another capability's handler saw the job's results")
	}
	if j := e.mustJob(id); j.State != ai.JobApplying || j.ApplyAttempts != 0 {
		t.Errorf("job = %+v; a refused run must not touch it", j)
	}
}

// The apply-attempt limit is enforced by the apply run, not the poll: past
// core.ai.batch_max_apply_attempts the run fails the job.
func TestBatch_ApplyAttemptLimit(t *testing.T) {
	t.Parallel()
	e := newBatchEnv(t)
	e.setConfig(ai.Config{BatchMaxApplyAttempts: 2})
	e.log.failAfter, e.log.failRuns = 1, 100
	id := e.submit(2)
	e.complete()
	e.mustPoll()
	for i := 1; i <= 2; i++ {
		if _, err := e.apply(id); err == nil {
			t.Fatalf("run %d succeeded", i)
		}
		if j := e.mustJob(id); j.State != ai.JobApplying || j.ApplyAttempts != i {
			t.Fatalf("after run %d: %+v", i, j)
		}
	}
	calls := e.log.callCount()
	if _, err := e.apply(id); err == nil {
		t.Fatal("the run past the limit succeeded")
	}
	if j := e.mustJob(id); j.State != ai.JobFailed {
		t.Errorf("job = %+v, want Failed", j)
	}
	if e.log.callCount() != calls {
		t.Error("the handler ran for a run that exhausted the attempts")
	}
	// A Failed job gets no more runs from the poll.
	staged := len(e.stager.Started())
	e.mustPoll()
	if len(e.stager.Started()) != staged {
		t.Error("the poll staged a run for a failed job")
	}
}

// The sync and batch token counters: a sync call counts mode=sync at its
// attempt, a finished apply run counts the results' tokens mode=batch exactly
// once, and a run that does not complete the job counts none.
func TestBatch_UsageMetricsRecorded(t *testing.T) {
	t.Parallel()
	e := newBatchEnv(t, withMetrics())
	g := e.prov.Gatherer()
	tokens := func(mode, dir string) float64 {
		return series(t, g, "mediacore_ai_tokens_total", map[string]string{"mode": mode, "direction": dir}, true)
	}

	// Sync usage.
	e.f.ScriptText(coretest.FakeAIStep{Text: "ok", Usage: ai.Usage{Requests: 1, InputTokens: 100, OutputTokens: 40, CachedInputTokens: 25}})
	plain := e.capability("riddle")
	e.edit(e.ref, ai.EndpointSettings{Concurrency: 1, Capabilities: []string{"gotham.riddle_batch", "gotham.riddle"}, TextModel: "joker-7b", Features: []ai.Feature{ai.FeatureBatch}})
	if _, err := e.r.Generate(context.Background(), plain, text("who?")); err != nil {
		t.Fatal(err)
	}
	if got := tokens("sync", "input"); got != 100 {
		t.Errorf("sync input tokens = %v, want 100", got)
	}
	if got := tokens("sync", "output"); got != 40 {
		t.Errorf("sync output tokens = %v, want 40", got)
	}
	if got := tokens("sync", "cached_input"); got != 25 {
		t.Errorf("sync cached input tokens = %v, want 25", got)
	}
	if got := tokens("batch", "input"); got != 0 {
		t.Errorf("batch tokens = %v before any batch finished", got)
	}

	// Batch usage: FakeAI answers each item with 10 in / 5 out.
	id := e.submit(3)
	e.complete()
	e.mustPoll()
	e.log.failAfter, e.log.failRuns = 1, 1
	if _, err := e.apply(id); err == nil {
		t.Fatal("the first run should fail")
	}
	if got := tokens("batch", "input"); got != 0 {
		t.Errorf("a run that did not complete the job counted %v batch tokens", got)
	}
	if _, err := e.apply(id); err != nil {
		t.Fatal(err)
	}
	if got := tokens("batch", "input"); got != 30 {
		t.Errorf("batch input tokens = %v, want 30 (3 rows x 10)", got)
	}
	if got := tokens("batch", "output"); got != 15 {
		t.Errorf("batch output tokens = %v, want 15 (3 rows x 5)", got)
	}
	// A third run on the completed job counts nothing more.
	if _, err := e.apply(id); err != nil {
		t.Fatal(err)
	}
	if got := tokens("batch", "input"); got != 30 {
		t.Errorf("a replay on a completed job counted tokens again: %v", got)
	}
}

// series reads one sample of family whose labels include want; with zeroOK a
// missing series reads 0.
func series(t *testing.T, g prometheus.Gatherer, family string, want map[string]string, zeroOK bool) float64 {
	t.Helper()
	mfs, err := g.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		if mf.GetName() != family {
			continue
		}
	next:
		for _, m := range mf.GetMetric() {
			labels := map[string]string{}
			for _, lp := range m.GetLabel() {
				labels[lp.GetName()] = lp.GetValue()
			}
			for k, v := range want {
				if labels[k] != v {
					continue next
				}
			}
			switch {
			case m.GetCounter() != nil:
				return m.GetCounter().GetValue()
			case m.GetGauge() != nil:
				return m.GetGauge().GetValue()
			}
			return m.GetUntyped().GetValue()
		}
	}
	if !zeroOK {
		t.Fatalf("no series %s%v", family, want)
	}
	return 0
}

// A job row the store cannot read ends the open listing there, after the jobs
// before it. The poll advances those jobs and then returns the error: before,
// one damaged row meant nothing was polled at all, so every job listed ahead of
// it stopped advancing too. Every write is a compare-and-set from the state the
// poll read, so acting on the partial list is safe.
func TestBatchPoll_AdvancesTheJobsListedBeforeADamagedRow(t *testing.T) {
	t.Parallel()
	e := newBatchEnv(t)
	ids := []ai.BatchJobID{e.submit(1), e.submit(1), e.submit(1)}
	slices.Sort(ids) // Open lists in ID order
	for _, h := range e.f.RemoteBatches() {
		e.f.CompleteBatch(h)
	}

	ctx := context.Background()
	row := keys.Join(keys.Str("jobs"), keys.Str(string(ids[1])))
	err := commit(t, e.ks, func(b *storage.Batch) error {
		b.Bind(e.ks)
		if _, err := b.GetForUpdate(ctx, e.ks, row); err != nil {
			return err
		}
		return b.In(e.ks).Set(row, []byte("{not json"))
	})
	if err != nil {
		t.Fatalf("damage the second job's row: %v", err)
	}

	res, err := e.poll()
	if err == nil || !strings.Contains(err.Error(), "list open jobs") {
		t.Fatalf("PollBatches = %+v, %v; want the listing error returned", res, err)
	}
	if res.Polled != 1 || res.ApplyStaged != 1 {
		t.Errorf("PollBatches = %+v, want the one job listed before the damage polled and its apply run staged", res)
	}
	if j := e.mustJob(ids[0]); j.State != ai.JobApplying {
		t.Errorf("the job listed before the damaged row is %s, want Applying: it must not wait on the damage", j.State)
	}
}
