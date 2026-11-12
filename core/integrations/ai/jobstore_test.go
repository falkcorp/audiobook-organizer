// file: core/integrations/ai/jobstore_test.go
// version: 1.0.0
// guid: 5f8c2a7d-9e14-4b63-a1d5-7c0e3b9f6a28
// last-edited: 2026-11-12

package ai_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/integrations/ai"
	"github.com/falkcorp/mediacore/integrations/ai/aitest"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/keys"
)

// openAIKeyspace boots an in-memory store with only the c/ai/ owner and returns
// its keyspace.
func openAIKeyspace(t testing.TB) storage.Keyspace {
	t.Helper()
	ctx := context.Background()
	o, err := storage.Open(ctx, storage.OpenOptions{
		Path:            filepath.Join(t.TempDir(), "main.pebble"),
		Owners:          []storage.OwnerSpec{{Owner: ai.Owner, Spec: ai.StorageSpec(), Enabled: true}},
		Build:           "coretest+fixture",
		FreeSpaceMargin: 1,
		FS:              storage.FSOf(vfs.NewMem()),
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	db, err := o.Prepare(ctx)
	if err == nil {
		err = db.Migrate(ctx)
	}
	if err != nil {
		_ = o.Close()
		t.Fatalf("boot: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	return keyspaceOf(t, db, ai.Owner)
}

// aitest.JobStoreSuite against core's one implementation: every write is a
// compare-and-set the keyspace itself enforces (it Serializes the job families,
// so a write without its Expect is refused).
func TestNativeJobStore_JobStoreSuite(t *testing.T) {
	t.Parallel()
	aitest.JobStoreSuite(t, func(t *testing.T) aitest.JobStoreFixture {
		ks := openAIKeyspace(t)
		return aitest.JobStoreFixture{Store: ai.NativeJobStore(ks), NewBatch: ks.NewIndexedBatch}
	})
}

func commit(t testing.TB, ks storage.Keyspace, fn func(b *storage.Batch) error) error {
	t.Helper()
	b := ks.NewIndexedBatch()
	defer func() { _ = b.Close() }()
	if err := fn(b); err != nil {
		return err
	}
	return b.Commit(context.Background(), storage.Sync)
}

func newJob(id string) ai.StoredJob {
	now := time.Date(2039, 4, 1, 22, 0, 0, 0, time.UTC)
	return ai.StoredJob{
		ID: ai.BatchJobID(id), Capability: "gotham.riddle_batch", Module: "gotham", ApplyKind: applyKind,
		Endpoint: integrations.Ref{Kind: integrations.KindAI, Instance: "arkham"},
		State:    ai.JobPending, Payload: []byte(`{"riddle":"what falls but never breaks"}`), Created: now, Updated: now,
	}
}

// A job round-trips through Create, Transition and Get with every field, the
// open index follows the job in and out of the open states, and Open lists the
// open jobs in ID order without their payload.
func TestNativeJobStore_RoundTrip(t *testing.T) {
	t.Parallel()
	ks := openAIKeyspace(t)
	st := ai.NativeJobStore(ks)
	ctx := context.Background()
	for _, id := range []string{"b-02", "a-01", "c-03"} {
		if err := commit(t, ks, func(b *storage.Batch) error { return st.Create(ctx, b, newJob(id)) }); err != nil {
			t.Fatal(err)
		}
	}
	var order []ai.BatchJobID
	for j, err := range st.Open(ctx) {
		if err != nil {
			t.Fatal(err)
		}
		order = append(order, j.ID)
		if j.Payload != nil {
			t.Errorf("Open yielded the payload of %s", j.ID)
		}
	}
	if fmt.Sprint(order) != "[a-01 b-02 c-03]" {
		t.Errorf("Open order = %v, want ID order", order)
	}

	want := newJob("a-01")
	got, err := st.Get(ctx, "a-01")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Created.Equal(want.Created) || !got.Updated.Equal(want.Updated) || got.Endpoint != want.Endpoint ||
		string(got.Payload) != string(want.Payload) || got.Capability != want.Capability || got.Module != want.Module ||
		got.ApplyKind != want.ApplyKind || got.State != ai.JobPending || got.ApplyAttempts != 0 || got.Handle != "" {
		t.Errorf("Get = %+v, want %+v", got, want)
	}

	// Walk a-01 to Completed: the handle and attempts are kept, the payload
	// stays readable by Get, and the job leaves the open index at the end.
	step := func(from, to ai.JobState, mut func(*ai.StoredJob)) {
		t.Helper()
		cur, err := st.Get(ctx, "a-01")
		if err != nil {
			t.Fatal(err)
		}
		next := cur
		next.State, next.Updated = to, cur.Updated.Add(time.Minute)
		if mut != nil {
			mut(&next)
		}
		if err := commit(t, ks, func(b *storage.Batch) error { return st.Transition(ctx, b, "a-01", from, next) }); err != nil {
			t.Fatalf("%s -> %s: %v", from, to, err)
		}
	}
	step(ai.JobPending, ai.JobSubmitted, func(j *ai.StoredJob) { j.Handle = "batch_7" })
	step(ai.JobSubmitted, ai.JobApplying, nil)
	step(ai.JobApplying, ai.JobApplying, func(j *ai.StoredJob) { j.ApplyAttempts = 2 })
	step(ai.JobApplying, ai.JobCompleted, nil)
	got, _ = st.Get(ctx, "a-01")
	if got.State != ai.JobCompleted || got.Handle != "batch_7" || got.ApplyAttempts != 2 || string(got.Payload) != string(want.Payload) {
		t.Errorf("after the walk: %+v", got)
	}
	if !got.Created.Equal(want.Created) {
		t.Errorf("Created changed: %v", got.Created)
	}
	order = nil
	for j, err := range st.Open(ctx) {
		if err != nil {
			t.Fatal(err)
		}
		order = append(order, j.ID)
	}
	if fmt.Sprint(order) != "[b-02 c-03]" {
		t.Errorf("Open after completing a-01 = %v", order)
	}

	// A terminal job never moves, and the graph is forward only.
	cur, _ := st.Get(ctx, "a-01")
	next := cur
	next.State = ai.JobApplying
	if err := commit(t, ks, func(b *storage.Batch) error { return st.Transition(ctx, b, "a-01", ai.JobCompleted, next) }); err == nil {
		t.Error("a Completed job moved back to Applying")
	}
	bad := cur
	bad.State = ai.JobApplying
	if err := commit(t, ks, func(b *storage.Batch) error { return st.Transition(ctx, b, "b-02", ai.JobPending, bad) }); err == nil {
		t.Error("a Pending job jumped to Applying")
	}
	// Identity fields in next are ignored: the stored ones stand.
	forged := cur
	forged.State, forged.Capability, forged.Module, forged.ApplyKind = ai.JobApplying, "evil.cap", "evil", "evil.kind"
	b02, _ := st.Get(ctx, "b-02")
	forged.ID = "b-02"
	forged.State = ai.JobSubmitted
	if err := commit(t, ks, func(b *storage.Batch) error { return st.Transition(ctx, b, "b-02", ai.JobPending, forged) }); err != nil {
		t.Fatal(err)
	}
	if after, _ := st.Get(ctx, "b-02"); after.Capability != b02.Capability || after.Module != b02.Module || after.ApplyKind != b02.ApplyKind {
		t.Errorf("a Transition rewrote the job's identity: %+v", after)
	}
	if _, err := st.Get(ctx, "z-404"); !errors.Is(err, ai.ErrJobNotFound) {
		t.Errorf("Get of an unknown job = %v", err)
	}
	if err := commit(t, ks, func(b *storage.Batch) error { return st.Transition(ctx, b, "z-404", ai.JobPending, next) }); !errors.Is(err, ai.ErrJobNotFound) {
		t.Errorf("Transition of an unknown job = %v", err)
	}
}

// Open lists the open jobs it could read, then ends with the error: a poll that
// cannot read a job must not treat it as absent, and must not lose the jobs
// before it either. The damage is a real write that carries its Expect (the job
// row is read for update first), so the keyspace's own rules stay on and only
// the row's content is wrong.
func TestNativeJobStore_OpenYieldsReadableJobsThenTheError(t *testing.T) {
	t.Parallel()
	row := keys.Join(keys.Str("jobs"), keys.Str("b-02"))
	for _, tc := range []struct {
		name, wantErr string
		damage        func(in storage.BatchWriter) error
	}{
		{"an index row whose job row is missing", "b-02", func(in storage.BatchWriter) error { return in.Delete(row) }},
		{"a job row that does not decode", "decode a job row", func(in storage.BatchWriter) error { return in.Set(row, []byte("{not json")) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ks := openAIKeyspace(t)
			st := ai.NativeJobStore(ks)
			ctx := context.Background()
			for _, id := range []string{"a-01", "b-02", "c-03"} {
				if err := commit(t, ks, func(b *storage.Batch) error { return st.Create(ctx, b, newJob(id)) }); err != nil {
					t.Fatal(err)
				}
			}
			damage := func(b *storage.Batch) error {
				b.Bind(ks)
				if _, err := b.GetForUpdate(ctx, ks, row); err != nil {
					return err
				}
				return tc.damage(b.In(ks))
			}
			if err := commit(t, ks, damage); err != nil {
				t.Fatalf("damage the store: %v", err)
			}

			var ids []ai.BatchJobID
			var failure error
			for j, err := range st.Open(ctx) {
				switch {
				case failure != nil:
					t.Fatalf("Open yielded (%s, %v) after its error %v", j.ID, err, failure)
				case err != nil:
					failure = err
				default:
					ids = append(ids, j.ID)
				}
			}
			if fmt.Sprint(ids) != "[a-01]" {
				t.Errorf("Open yielded %v before the error, want [a-01]: the job read before the damage must not be lost", ids)
			}
			if failure == nil || !strings.Contains(failure.Error(), tc.wantErr) {
				t.Errorf("Open ended with %v, want an error naming %q", failure, tc.wantErr)
			}
		})
	}
}

// I4: there is no blind write. The job families are Serialized, so a raw write
// to one without an Expect is refused by the keyspace; the store's own writes
// are all accepted (the suite proves that), and it has no other writing method.
func TestNativeJobStore_BlindWriteRefused(t *testing.T) {
	t.Parallel()
	ks := openAIKeyspace(t)
	st := ai.NativeJobStore(ks)
	ctx := context.Background()
	job := keys.Join(keys.Str("jobs"), keys.Str("riddler-1"))
	openRow := keys.Join(keys.Str("ix"), keys.Str("open"), keys.Str("riddler-1"))

	for name, key := range map[string][]byte{"the job row": job, "the open index row": openRow} {
		err := commit(t, ks, func(b *storage.Batch) error { b.Bind(ks); return b.In(ks).Set(key, []byte("{}")) })
		if !errors.Is(err, storage.ErrBlindWrite) {
			t.Errorf("a raw write of %s = %v, want ErrBlindWrite", name, err)
		}
	}
	// A raw delete of an existing job row is as blind.
	if err := commit(t, ks, func(b *storage.Batch) error { return st.Create(ctx, b, newJob("riddler-1")) }); err != nil {
		t.Fatal(err)
	}
	err := commit(t, ks, func(b *storage.Batch) error { b.Bind(ks); return b.In(ks).Delete(job) })
	if !errors.Is(err, storage.ErrBlindWrite) {
		t.Errorf("a raw delete of the job row = %v, want ErrBlindWrite", err)
	}
	if _, err := st.Get(ctx, "riddler-1"); err != nil {
		t.Errorf("the refused delete removed the job: %v", err)
	}
	// A stale Transition (built from a read another commit then invalidated) is
	// dropped at commit, never applied over the newer row.
	b := ks.NewIndexedBatch()
	defer func() { _ = b.Close() }()
	cur, _ := st.Get(ctx, "riddler-1")
	next := cur
	next.State, next.Handle = ai.JobSubmitted, "batch_stale"
	if err := st.Transition(ctx, b, "riddler-1", ai.JobPending, next); err != nil {
		t.Fatal(err)
	}
	winner := cur
	winner.State = ai.JobCanceled
	if err := commit(t, ks, func(b2 *storage.Batch) error { return st.Transition(ctx, b2, "riddler-1", ai.JobPending, winner) }); err != nil {
		t.Fatal(err)
	}
	if err := b.Commit(ctx, storage.Sync); !errors.Is(err, storage.ErrConflict) {
		t.Errorf("the stale commit = %v, want ErrConflict", err)
	}
	if got, _ := st.Get(ctx, "riddler-1"); got.State != ai.JobCanceled || got.Handle != "" {
		t.Errorf("job = %+v; the stale write must not have landed", got)
	}
	// Create of an existing ID is a conflict, and Create refuses any state but Pending.
	if err := commit(t, ks, func(b *storage.Batch) error { return st.Create(ctx, b, newJob("riddler-1")) }); !errors.Is(err, ai.ErrJobConflict) {
		t.Errorf("a second Create = %v, want ErrJobConflict", err)
	}
	notPending := newJob("riddler-2")
	notPending.State = ai.JobSubmitted
	if err := commit(t, ks, func(b *storage.Batch) error { return st.Create(ctx, b, notPending) }); err == nil {
		t.Error("Create accepted a Submitted job")
	}
}

// The importer writes a job in ANY state, terminal ones included, with its
// payload and its open-index row when it is open, and every write carries the
// expectation that the job is absent: a job that appears between its read and
// its commit makes the commit fail instead of being overwritten.
func TestImportJob_AnyStateExpectsAbsent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, state := range []ai.JobState{ai.JobPending, ai.JobSubmitted, ai.JobApplying, ai.JobCompleted, ai.JobFailed, ai.JobCanceled} {
		t.Run(state.String(), func(t *testing.T) {
			t.Parallel()
			ks := openAIKeyspace(t)
			imp, st := ai.NativeImporter(ks), ai.NativeJobStore(ks)
			j := newJob("imported-" + state.String())
			j.State, j.Handle, j.ApplyAttempts = state, "batch_head", 3
			if err := commit(t, ks, func(b *storage.Batch) error { return imp.ImportJob(ctx, b, j) }); err != nil {
				t.Fatalf("import: %v", err)
			}
			got, err := st.Get(ctx, j.ID)
			if err != nil || got.State != state || got.Handle != "batch_head" || got.ApplyAttempts != 3 || string(got.Payload) != string(j.Payload) {
				t.Fatalf("Get = %+v, %v", got, err)
			}
			open := false
			for o, err := range st.Open(ctx) {
				if err != nil {
					t.Fatal(err)
				}
				open = open || o.ID == j.ID
			}
			if open != !state.Terminal() {
				t.Errorf("open = %v for a %s job", open, state)
			}
			// Re-running the step that committed is a no-op; other content is a conflict.
			if err := commit(t, ks, func(b *storage.Batch) error { return imp.ImportJob(ctx, b, j) }); err != nil {
				t.Errorf("re-import of identical content: %v", err)
			}
			other := j
			other.Handle = "batch_other"
			if err := commit(t, ks, func(b *storage.Batch) error { return imp.ImportJob(ctx, b, other) }); !errors.Is(err, storage.ErrImportConflict) {
				t.Errorf("re-import with other content = %v, want ErrImportConflict", err)
			}
		})
	}

	t.Run("the absence is expected at commit", func(t *testing.T) {
		t.Parallel()
		ks := openAIKeyspace(t)
		imp, st := ai.NativeImporter(ks), ai.NativeJobStore(ks)
		j := newJob("racing-import")
		b := ks.NewIndexedBatch()
		defer func() { _ = b.Close() }()
		if err := imp.ImportJob(ctx, b, j); err != nil {
			t.Fatal(err)
		}
		// A SubmitBatch creates the same ID after the import read it absent.
		if err := commit(t, ks, func(b2 *storage.Batch) error { return st.Create(ctx, b2, newJob("racing-import")) }); err != nil {
			t.Fatal(err)
		}
		if err := b.Commit(ctx, storage.Sync); !errors.Is(err, storage.ErrConflict) {
			t.Fatalf("the import commit = %v, want ErrConflict: it expected the job absent", err)
		}
	})

	t.Run("a job naming nothing registered is imported as failed", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		c := h.capability("riddle_batch")
		registerApply(t, c, noopHandler)
		imp := ai.NativeImporter(h.ks, ai.ImportWithCatalog(h.r))
		good := newJob("known")
		good.State = ai.JobApplying
		unknownCap := newJob("unknown-cap")
		unknownCap.Capability, unknownCap.State = "ghost.capability", ai.JobSubmitted
		unknownKind := newJob("unknown-kind")
		unknownKind.ApplyKind, unknownKind.State = "ghost.ai.batch-apply.x", ai.JobApplying
		for _, j := range []ai.StoredJob{good, unknownCap, unknownKind} {
			if err := commit(t, h.ks, func(b *storage.Batch) error { return imp.ImportJob(ctx, b, j) }); err != nil {
				t.Fatalf("import %s: %v", j.ID, err)
			}
		}
		if got := h.mustJob("known").State; got != ai.JobApplying {
			t.Errorf("a registered job = %s, want it imported as given", got)
		}
		for _, id := range []ai.BatchJobID{"unknown-cap", "unknown-kind"} {
			if got := h.mustJob(id).State; got != ai.JobFailed {
				t.Errorf("%s = %s, want Failed: never dropped, never left open for a poll that cannot apply it", id, got)
			}
		}
		// The poll leaves the registered Applying job's run to the stager and
		// does not touch the failed ones.
		if _, err := h.r.PollBatches(newRC(t), ai.BatchPollParams{}); err != nil {
			t.Fatal(err)
		}
		if runs := h.stager.Started(); len(runs) != 1 || runs[0].Params.Job != "known" {
			t.Errorf("staged runs = %+v, want one for the registered job", runs)
		}
	})
}

// A poll that finds an open job naming an apply kind no module registered (a
// module absent from this binary) leaves it as it is: not staged, not failed.
func TestBatch_PollLeavesJobOfUnregisteredApplyKind(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	ctx := context.Background()
	ghost := newJob("ghost-job")
	ghost.State, ghost.ApplyKind = ai.JobApplying, "ghost.ai.batch-apply.x"
	// Imported without a catalog view: the caller vouches for its IDs.
	if err := commit(t, h.ks, func(b *storage.Batch) error { return ai.NativeImporter(h.ks).ImportJob(ctx, b, ghost) }); err != nil {
		t.Fatal(err)
	}
	res, err := h.r.PollBatches(newRC(t), ai.BatchPollParams{})
	if err != nil {
		t.Fatalf("PollBatches: %v", err)
	}
	if res.ApplyStaged != 0 || res.Failed != 0 || len(h.stager.Started()) != 0 {
		t.Errorf("poll = %+v, runs %v; want the job left alone", res, h.stager.Started())
	}
	if got := h.mustJob("ghost-job").State; got != ai.JobApplying {
		t.Errorf("job = %s, want it left Applying until its module is back", got)
	}
}

// ---- the one-winner races (I4) ---------------------------------------------------------

// raceEnv is a batch env with one job at the given state.
func (e *batchEnv) jobAt(state ai.JobState) ai.BatchJobID {
	e.t.Helper()
	id := e.submit(2)
	if state >= ai.JobApplying {
		e.f.CompleteBatch(e.mustJob(id).Handle)
		e.mustPoll()
	} else if state == ai.JobSubmitted {
		e.f.CompleteBatch(e.mustJob(id).Handle)
	}
	return id
}

// once returns a hook that runs fn the first time cond holds.
func once(cond func(from ai.JobState, next ai.StoredJob) bool, fn func(id ai.BatchJobID)) func(ai.BatchJobID, ai.JobState, ai.StoredJob) {
	var done sync.Once
	return func(id ai.BatchJobID, from ai.JobState, next ai.StoredJob) {
		if cond(from, next) {
			done.Do(func() { fn(id) })
		}
	}
}

// Poll vs cancel, in the exact window: the poll has read the job Submitted and
// staged Applying plus the run, and a cancel commits before the poll does. The
// poll's commit loses, re-reads, finds the job canceled and stages nothing.
func TestAIJobPollVsCancelOneWins(t *testing.T) {
	t.Parallel()
	t.Run("a cancel commits inside the poll's window", func(t *testing.T) {
		t.Parallel()
		e := newBatchEnv(t)
		id := e.jobAt(ai.JobSubmitted)
		e.hooks.onTransition(once(func(from ai.JobState, n ai.StoredJob) bool {
			return from == ai.JobSubmitted && n.State == ai.JobApplying
		},
			func(id ai.BatchJobID) { e.competing(id, ai.JobSubmitted, ai.JobCanceled) }))

		res, err := e.poll()
		if err != nil {
			t.Fatalf("poll: %v", err)
		}
		if res.ApplyStaged != 0 || len(e.stager.Started()) != 0 {
			t.Errorf("poll = %+v, runs %v: a run was staged for a job that was canceled first", res, e.stager.Started())
		}
		if got := e.mustJob(id).State; got != ai.JobCanceled {
			t.Errorf("job = %s, want Canceled: the poll must not overwrite a cancel", got)
		}
		e.hooks.onTransition(nil)
		e.mustPoll()
		if got := e.mustJob(id).State; got != ai.JobCanceled || len(e.stager.Started()) != 0 {
			t.Errorf("a later poll moved a canceled job: %s, runs %v", got, e.stager.Started())
		}
	})
	t.Run("the cancel loses its window and cancels from the new state", func(t *testing.T) {
		t.Parallel()
		e := newBatchEnv(t)
		id := e.jobAt(ai.JobSubmitted)
		// The poll commits Applying between the cancel's read and its commit.
		e.hooks.onTransition(once(func(from ai.JobState, n ai.StoredJob) bool {
			return from == ai.JobSubmitted && n.State == ai.JobCanceled
		},
			func(id ai.BatchJobID) { e.competing(id, ai.JobSubmitted, ai.JobApplying) }))
		if err := e.r.CancelBatch(context.Background(), id); err != nil {
			t.Fatalf("CancelBatch: %v", err)
		}
		if got := e.mustJob(id).State; got != ai.JobCanceled {
			t.Errorf("job = %s, want Canceled: the cancel re-reads once and cancels from Applying", got)
		}
		if len(e.f.CancelCalls()) != 0 {
			t.Error("a job canceled from Applying (results already complete) made a remote cancel")
		}
	})
	t.Run("concurrently", func(t *testing.T) {
		t.Parallel()
		e := newBatchEnv(t)
		const rounds = 25
		var canceledFirst, polledFirst int
		for range rounds {
			id := e.jobAt(ai.JobSubmitted)
			before := len(e.stager.Started())
			var start sync.WaitGroup
			start.Add(1)
			var wg sync.WaitGroup
			var cancelErr error
			wg.Add(2)
			go func() { defer wg.Done(); start.Wait(); _, _ = e.poll() }()
			go func() { defer wg.Done(); start.Wait(); cancelErr = e.r.CancelBatch(context.Background(), id) }()
			start.Done()
			wg.Wait()
			if cancelErr != nil {
				t.Fatalf("CancelBatch: %v", cancelErr)
			}
			if got := e.mustJob(id).State; got != ai.JobCanceled {
				t.Fatalf("job = %s; a cancel that returned nil must stand", got)
			}
			if staged := len(e.stager.Started()) - before; staged == 0 {
				canceledFirst++
			} else {
				polledFirst++
				if staged != 1 {
					t.Fatalf("%d runs staged for one job", staged)
				}
			}
			// Whatever was staged is inert: the run finds the job terminal.
			if _, err := e.apply(id); err != nil {
				t.Fatalf("apply of a canceled job: %v", err)
			}
		}
		if e.log.callCount() != 0 {
			t.Errorf("the handler ran %d time(s) for canceled jobs", e.log.callCount())
		}
		t.Logf("cancel won %d, poll won %d of %d", canceledFirst, polledFirst, rounds)
	})
}

// Apply vs cancel, in the exact window: the apply run's handler is done and it
// has staged Completed; a cancel commits first. The completion loses, re-reads
// and leaves the cancel alone, and no usage is recorded for a job that did not
// complete. (The handler's writes stay: it is idempotent.)
func TestAIJobApplyVsCancelOneWins(t *testing.T) {
	t.Parallel()
	t.Run("a cancel commits inside the completion's window", func(t *testing.T) {
		t.Parallel()
		e := newBatchEnv(t, withMetrics())
		id := e.jobAt(ai.JobApplying)
		e.hooks.onTransition(once(func(from ai.JobState, n ai.StoredJob) bool {
			return from == ai.JobApplying && n.State == ai.JobCompleted
		},
			func(id ai.BatchJobID) { e.competing(id, ai.JobApplying, ai.JobCanceled) }))

		res, err := e.apply(id)
		if err != nil {
			t.Fatalf("apply: %v", err)
		}
		if res.JobState != "canceled" {
			t.Errorf("result = %+v, want the run to report the job canceled", res)
		}
		if got := e.mustJob(id).State; got != ai.JobCanceled {
			t.Errorf("job = %s, want Canceled: the completion must not overwrite a cancel", got)
		}
		if n := e.log.callCount(); n != 1 {
			t.Errorf("handler ran %d time(s); it ran before the cancel and its writes stand", n)
		}
		if got := series(t, e.prov.Gatherer(), "mediacore_ai_tokens_total", map[string]string{"mode": "batch", "direction": "input"}, true); got != 0 {
			t.Errorf("a canceled job's batch tokens were counted: %v", got)
		}
	})
	t.Run("the cancel loses its window to the completion", func(t *testing.T) {
		t.Parallel()
		e := newBatchEnv(t)
		id := e.jobAt(ai.JobApplying)
		e.hooks.onTransition(once(func(from ai.JobState, n ai.StoredJob) bool {
			return from == ai.JobApplying && n.State == ai.JobCanceled
		},
			func(id ai.BatchJobID) { e.competing(id, ai.JobApplying, ai.JobCompleted) }))
		err := e.r.CancelBatch(context.Background(), id)
		if !errors.Is(err, ai.ErrJobConflict) {
			t.Fatalf("CancelBatch of a job that completed first = %v, want ErrJobConflict (the HTTP 409)", err)
		}
		if got := e.mustJob(id).State; got != ai.JobCompleted {
			t.Errorf("job = %s, want Completed: a lost cancel must not revert it", got)
		}
	})
	t.Run("concurrently", func(t *testing.T) {
		t.Parallel()
		e := newBatchEnv(t)
		const rounds = 25
		var completed, canceled int
		for range rounds {
			id := e.jobAt(ai.JobApplying)
			var start sync.WaitGroup
			start.Add(1)
			var wg sync.WaitGroup
			var cancelErr error
			wg.Add(2)
			go func() { defer wg.Done(); start.Wait(); _, _ = e.apply(id) }()
			go func() { defer wg.Done(); start.Wait(); cancelErr = e.r.CancelBatch(context.Background(), id) }()
			start.Done()
			wg.Wait()
			final := e.mustJob(id).State
			switch {
			case cancelErr == nil && final == ai.JobCanceled:
				canceled++
			case errors.Is(cancelErr, ai.ErrJobConflict) && final == ai.JobCompleted:
				completed++
			default:
				t.Fatalf("cancel err = %v with the job %s: exactly one of the two must have won", cancelErr, final)
			}
		}
		t.Logf("cancel won %d, apply won %d of %d", canceled, completed, rounds)
	})
}

// A same-state write is a compare-and-set like any other: the apply run's
// raised attempt count must not revert a cancel that committed after the run
// read the job.
func TestAIJobSameStateWriteVsCancelOneWins(t *testing.T) {
	t.Parallel()
	e := newBatchEnv(t)
	id := e.jobAt(ai.JobApplying)
	e.hooks.onTransition(once(func(from ai.JobState, n ai.StoredJob) bool {
		return from == ai.JobApplying && n.State == ai.JobApplying
	},
		func(id ai.BatchJobID) { e.competing(id, ai.JobApplying, ai.JobCanceled) }))

	res, err := e.apply(id)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if res.JobState != "canceled" || res.Applied != 0 {
		t.Errorf("result = %+v, want the run to stop on a canceled job without applying", res)
	}
	got := e.mustJob(id)
	if got.State != ai.JobCanceled {
		t.Fatalf("job = %s, want Canceled: the same-state write must not revert it", got.State)
	}
	if got.ApplyAttempts != 0 {
		t.Errorf("attempts = %d, want 0: the lost same-state write must not have landed", got.ApplyAttempts)
	}
	if e.log.callCount() != 0 {
		t.Error("the handler ran for a job canceled before the run began applying")
	}
	// The write that lost leaves no trace after a re-read either.
	e.hooks.onTransition(nil)
	if _, err := e.apply(id); err != nil {
		t.Fatal(err)
	}
	if again := e.mustJob(id); again.State != ai.JobCanceled || again.ApplyAttempts != 0 {
		t.Errorf("job = %+v after a second run", again)
	}
}
