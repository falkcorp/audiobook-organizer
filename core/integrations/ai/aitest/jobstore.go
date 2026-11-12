// file: core/integrations/ai/aitest/jobstore.go
// version: 1.0.0
// guid: 4f8c1d63-a729-4e05-b1d4-9c6e3a5f7b18
// last-edited: 2026-11-12

// Package aitest is the test-support package of integrations/ai (03 section
// 3.3): JobStoreSuite, the commit-time conformance suite of an ai.JobStore, and
// Conformance, the suite every AI adapter passes. It is public so a module's or
// an adapter's own tests can run them; nothing outside tests imports it.
package aitest

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/integrations/ai"
	"github.com/falkcorp/mediacore/storage"
)

// JobStoreFixture is one fresh store under test and the way to open a batch
// over its keyspace. The keyspace must be opened with ai.StorageSpec (or an
// equivalent that Serializes the job families), so storage itself refuses a
// write that carries no Expect; the suite's ordinary operations then prove the
// store stages none.
type JobStoreFixture struct {
	Store    ai.JobStore
	NewBatch func() *storage.Batch // Keyspace.NewIndexedBatch over the store's keyspace
}

// jobID names the Batman-themed jobs of the suite.
func jobID(i int) ai.BatchJobID { return ai.BatchJobID(fmt.Sprintf("riddler-%03d", i)) }

func pending(id ai.BatchJobID) ai.StoredJob {
	now := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	return ai.StoredJob{
		ID: id, Capability: "gotham.riddle_batch", Module: "gotham", ApplyKind: "gotham.ai.batch-apply.riddle-batch",
		Endpoint: integrations.Ref{Kind: integrations.KindAI, Instance: "arkham"},
		State:    ai.JobPending, Payload: []byte(`{"riddle":"what has keys but opens no locks"}`), Created: now, Updated: now,
	}
}

// write stages fn into a fresh batch and commits it. A failed Close is the
// error when nothing else failed, as in core's rbac service.
func write(ctx context.Context, fx JobStoreFixture, fn func(b *storage.Batch) error) (err error) {
	b := fx.NewBatch()
	defer func() {
		if cerr := b.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("close batch: %w", cerr)
		}
	}()
	if err := fn(b); err != nil {
		return err
	}
	return b.Commit(ctx, storage.Sync)
}

func create(ctx context.Context, fx JobStoreFixture, j ai.StoredJob) error {
	return write(ctx, fx, func(b *storage.Batch) error { return fx.Store.Create(ctx, b, j) })
}

func move(ctx context.Context, fx JobStoreFixture, id ai.BatchJobID, from ai.JobState, next ai.StoredJob) error {
	return write(ctx, fx, func(b *storage.Batch) error { return fx.Store.Transition(ctx, b, id, from, next) })
}

// to is a Transition's next value: the fields a writer carries.
func to(j ai.StoredJob, state ai.JobState, attempts int) ai.StoredJob {
	j.State, j.ApplyAttempts, j.Updated = state, attempts, j.Updated.Add(time.Minute)
	j.Payload = nil
	return j
}

// seed creates a job and walks it forward to state.
func seed(ctx context.Context, t *testing.T, fx JobStoreFixture, id ai.BatchJobID, state ai.JobState) ai.StoredJob {
	t.Helper()
	j := pending(id)
	if err := create(ctx, fx, j); err != nil {
		t.Fatalf("create %s: %v", id, err)
	}
	cur := j
	for _, st := range []ai.JobState{ai.JobSubmitted, ai.JobApplying} {
		if cur.State >= state {
			break
		}
		next := to(cur, st, 0)
		next.Handle = "batch_joker"
		if err := move(ctx, fx, id, cur.State, next); err != nil {
			t.Fatalf("move %s to %s: %v", id, st, err)
		}
		cur = next
	}
	return cur
}

func isLoss(err error) bool {
	return errors.Is(err, ai.ErrJobConflict) || errors.Is(err, storage.ErrConflict)
}

// JobStoreSuite runs the store conformance of 05-ai section 9 against a store
// built by factory. It asserts the commit-time preconditions: Create of an
// existing ID and a Transition from a state the job is not in both fail; two
// transitions whose read-to-commit windows overlap never both commit (a
// same-state Transition and a cancel included, so a cancel is never reverted);
// free-running writers serialize (at most one of them changes the state, and a
// write that read the job after another committed is a valid later write, so
// "exactly one" is asserted only where the overlap is forced); a batch built
// from a stale read loses at commit; and every write the store stages is
// accepted by a keyspace that refuses a write without its Expect.
func JobStoreSuite(t *testing.T, factory func(t *testing.T) JobStoreFixture) {
	t.Helper()
	ctx := context.Background()

	t.Run("create_get_open", func(t *testing.T) {
		fx := factory(t)
		j := pending("riddler-001")
		if err := create(ctx, fx, j); err != nil {
			t.Fatal(err)
		}
		got, err := fx.Store.Get(ctx, j.ID)
		if err != nil {
			t.Fatal(err)
		}
		if string(got.Payload) != string(j.Payload) || got.State != ai.JobPending || got.Capability != j.Capability ||
			got.Module != j.Module || got.ApplyKind != j.ApplyKind || got.Endpoint != j.Endpoint {
			t.Fatalf("Get = %+v, want the created job %+v", got, j)
		}
		n := 0
		for o, err := range fx.Store.Open(ctx) {
			if err != nil {
				t.Fatal(err)
			}
			n++
			if o.ID != j.ID || len(o.Payload) != 0 {
				t.Fatalf("Open yielded %+v: want the job without its payload", o)
			}
		}
		if n != 1 {
			t.Fatalf("Open yielded %d jobs, want 1", n)
		}
		if _, err := fx.Store.Get(ctx, "riddler-404"); !errors.Is(err, ai.ErrJobNotFound) {
			t.Fatalf("Get of an unknown job = %v, want ErrJobNotFound", err)
		}
	})

	t.Run("terminal_job_leaves_open", func(t *testing.T) {
		fx := factory(t)
		cur := seed(ctx, t, fx, "riddler-001", ai.JobApplying)
		if err := move(ctx, fx, cur.ID, ai.JobApplying, to(cur, ai.JobCompleted, 1)); err != nil {
			t.Fatal(err)
		}
		for o, err := range fx.Store.Open(ctx) {
			t.Fatalf("Open yielded %+v (%v) after the job completed", o, err)
		}
		got, err := fx.Store.Get(ctx, cur.ID)
		if err != nil || got.State != ai.JobCompleted || got.ApplyAttempts != 1 {
			t.Fatalf("Get = %+v, %v: want Completed with 1 attempt", got, err)
		}
	})

	t.Run("create_existing_fails", func(t *testing.T) {
		fx := factory(t)
		j := pending("riddler-001")
		if err := create(ctx, fx, j); err != nil {
			t.Fatal(err)
		}
		other := j
		other.Payload = []byte(`{"riddle":"another"}`)
		if err := create(ctx, fx, other); err == nil {
			t.Fatal("Create of an existing ID committed")
		}
		got, err := fx.Store.Get(ctx, j.ID)
		if err != nil || string(got.Payload) != string(j.Payload) {
			t.Fatalf("the first job was changed: %+v, %v", got, err)
		}
	})

	t.Run("create_refuses_non_pending", func(t *testing.T) {
		fx := factory(t)
		j := pending("riddler-001")
		j.State = ai.JobCompleted
		if err := create(ctx, fx, j); err == nil {
			t.Fatal("Create took a Completed job")
		}
	})

	t.Run("transition_wrong_state_fails", func(t *testing.T) {
		fx := factory(t)
		cur := seed(ctx, t, fx, "riddler-001", ai.JobPending)
		err := move(ctx, fx, cur.ID, ai.JobSubmitted, to(cur, ai.JobApplying, 0))
		if !errors.Is(err, ai.ErrJobConflict) {
			t.Fatalf("Transition from the wrong state = %v, want ErrJobConflict", err)
		}
		got, err := fx.Store.Get(ctx, cur.ID)
		if err != nil {
			t.Fatalf("Get after a failed Transition: %v", err)
		}
		if got.State != ai.JobPending {
			t.Fatalf("a failed Transition changed the job to %s", got.State)
		}
		if err := move(ctx, fx, "riddler-404", ai.JobPending, to(cur, ai.JobSubmitted, 0)); err == nil {
			t.Fatal("Transition of an unknown job committed")
		}
	})

	t.Run("illegal_edges_refused", func(t *testing.T) {
		fx := factory(t)
		cur := seed(ctx, t, fx, "riddler-001", ai.JobPending)
		if err := move(ctx, fx, cur.ID, ai.JobPending, to(cur, ai.JobCompleted, 0)); err == nil {
			t.Fatal("Pending -> Completed committed")
		}
		cancel := to(cur, ai.JobCanceled, 0)
		if err := move(ctx, fx, cur.ID, ai.JobPending, cancel); err != nil {
			t.Fatal(err)
		}
		if err := move(ctx, fx, cur.ID, ai.JobCanceled, to(cur, ai.JobPending, 0)); err == nil {
			t.Fatal("a canceled job moved again")
		}
	})

	t.Run("same_state_transition_commits", func(t *testing.T) {
		fx := factory(t)
		cur := seed(ctx, t, fx, "riddler-001", ai.JobApplying)
		if err := move(ctx, fx, cur.ID, ai.JobApplying, to(cur, ai.JobApplying, 3)); err != nil {
			t.Fatal(err)
		}
		got, err := fx.Store.Get(ctx, cur.ID)
		if err != nil {
			t.Fatalf("Get after the Transition: %v", err)
		}
		if got.State != ai.JobApplying || got.ApplyAttempts != 3 {
			t.Fatalf("Get = %+v, want Applying with 3 attempts", got)
		}
	})

	t.Run("stale_batch_loses_at_commit", func(t *testing.T) {
		fx := factory(t)
		cur := seed(ctx, t, fx, "riddler-001", ai.JobSubmitted)
		stale := fx.NewBatch()
		defer func() {
			if err := stale.Close(); err != nil {
				t.Errorf("close the stale batch: %v", err)
			}
		}()
		if err := fx.Store.Transition(ctx, stale, cur.ID, ai.JobSubmitted, to(cur, ai.JobApplying, 0)); err != nil {
			t.Fatal(err)
		}
		if err := move(ctx, fx, cur.ID, ai.JobSubmitted, to(cur, ai.JobCanceled, 0)); err != nil {
			t.Fatal(err)
		}
		if err := stale.Commit(ctx, storage.Sync); !errors.Is(err, storage.ErrConflict) {
			t.Fatalf("a batch built from a read another writer superseded: Commit = %v, want storage.ErrConflict", err)
		}
		got, err := fx.Store.Get(ctx, cur.ID)
		if err != nil {
			t.Fatalf("Get after the stale commit lost: %v", err)
		}
		if got.State != ai.JobCanceled {
			t.Fatalf("the stale batch reverted the cancel: state %s", got.State)
		}
	})

	t.Run("overlapping_windows_one_wins", func(t *testing.T) {
		// Both writers read the job before either commits, so neither can have
		// seen the other: whichever commits first, the second must lose at
		// commit and nothing of its batch may land. Every ordered pair of
		// (state-keeping, state-changing) writers, from every open state.
		fx := factory(t)
		edges := []struct {
			from ai.JobState
			outs []ai.JobState // every legal next state, the state-keeping one first
		}{
			{ai.JobSubmitted, []ai.JobState{ai.JobSubmitted, ai.JobApplying, ai.JobCanceled, ai.JobFailed}},
			{ai.JobApplying, []ai.JobState{ai.JobApplying, ai.JobCompleted, ai.JobCanceled, ai.JobFailed}},
		}
		n := 0
		for _, e := range edges {
			for _, first := range e.outs {
				for _, second := range e.outs {
					n++
					cur := seed(ctx, t, fx, jobID(n), e.from)
					ba, bb := fx.NewBatch(), fx.NewBatch()
					if err := fx.Store.Transition(ctx, ba, cur.ID, e.from, to(cur, first, 1)); err != nil {
						t.Fatalf("%s: stage %s -> %s: %v", cur.ID, e.from, first, err)
					}
					if err := fx.Store.Transition(ctx, bb, cur.ID, e.from, to(cur, second, 2)); err != nil {
						t.Fatalf("%s: stage %s -> %s: %v", cur.ID, e.from, second, err)
					}
					if err := ba.Commit(ctx, storage.Sync); err != nil {
						t.Fatalf("%s: the first commit (%s -> %s): %v", cur.ID, e.from, first, err)
					}
					if err := bb.Commit(ctx, storage.Sync); !errors.Is(err, storage.ErrConflict) {
						t.Fatalf("%s: the second writer (%s -> %s) overlapped the first (-> %s): Commit = %v, want storage.ErrConflict", cur.ID, e.from, second, first, err)
					}
					if err := ba.Close(); err != nil {
						t.Fatalf("%s: close the first batch: %v", cur.ID, err)
					}
					if err := bb.Close(); err != nil {
						t.Fatalf("%s: close the second batch: %v", cur.ID, err)
					}
					got, err := fx.Store.Get(ctx, cur.ID)
					if err != nil || got.State != first || got.ApplyAttempts != 1 {
						t.Fatalf("%s: stored %+v (%v), want the first writer's %s with 1 attempt: the loser's batch must not land", cur.ID, got, err, first)
					}
				}
			}
		}
	})

	t.Run("concurrent_transitions_serialize", func(t *testing.T) {
		// Writers released together do not necessarily overlap: one may commit
		// before another reads, and that one then reads the committed row, finds
		// its own from state still true (a same-state write keeps it) and
		// commits after it. That is a valid serial order, so the asserted
		// invariants are the serializable ones: some writer commits, at most
		// one of them changes the state, and the stored state is that writer's.
		fx := factory(t)
		for round := range 20 {
			cur := seed(ctx, t, fx, jobID(round), ai.JobSubmitted)
			targets := []ai.StoredJob{
				to(cur, ai.JobApplying, 0), to(cur, ai.JobCanceled, 0), to(cur, ai.JobFailed, 0),
				to(cur, ai.JobSubmitted, 0), to(cur, ai.JobApplying, 0), to(cur, ai.JobCanceled, 0),
			}
			wins := race(ctx, fx, cur.ID, ai.JobSubmitted, targets)
			changed := -1
			for _, w := range wins {
				if targets[w].State == ai.JobSubmitted {
					continue
				}
				if changed >= 0 {
					t.Fatalf("round %d: writers %d and %d both changed the state and committed (%v)", round, changed, w, wins)
				}
				changed = w
			}
			if len(wins) == 0 {
				t.Fatalf("round %d: no writer committed; the first to commit cannot lose", round)
			}
			want := ai.JobSubmitted
			if changed >= 0 {
				want = targets[changed].State
			}
			got, err := fx.Store.Get(ctx, cur.ID)
			if err != nil || got.State != want {
				t.Fatalf("round %d: stored %s (%v), want %s (committed writers %v)", round, got.State, err, want, wins)
			}
		}
	})

	t.Run("same_state_write_never_reverts_cancel", func(t *testing.T) {
		// A same-state write that commits before the cancel reads is followed by
		// the cancel (both commit, the job ends Canceled); one that read first
		// loses at commit if the cancel got in between. In no order may the
		// job end anywhere but where the last committed writer left it, and a
		// cancel that committed is never undone.
		fx := factory(t)
		for round := range 20 {
			cur := seed(ctx, t, fx, jobID(round), ai.JobApplying)
			targets := []ai.StoredJob{to(cur, ai.JobApplying, 2), to(cur, ai.JobCanceled, 0)}
			wins := race(ctx, fx, cur.ID, ai.JobApplying, targets)
			if len(wins) == 0 {
				t.Fatalf("round %d: no writer committed", round)
			}
			canceled := false
			for _, w := range wins {
				canceled = canceled || w == 1
			}
			got, err := fx.Store.Get(ctx, cur.ID)
			if err != nil {
				t.Fatalf("%s: Get after the race: %v", cur.ID, err)
			}
			switch {
			case canceled && got.State != ai.JobCanceled:
				t.Fatalf("round %d: the cancel committed (%v) but the job is %s: a same-state write reverted it", round, wins, got.State)
			case !canceled && (got.State != ai.JobApplying || got.ApplyAttempts != 2):
				t.Fatalf("round %d: only the same-state write committed but the job is %+v", round, got)
			}
		}
	})
}

// race commits every target as a Transition of one job from the same state, all
// released together, and returns the indexes whose commit succeeded. A writer
// that lost must have lost to a race, not failed otherwise.
func race(ctx context.Context, fx JobStoreFixture, id ai.BatchJobID, from ai.JobState, targets []ai.StoredJob) []int {
	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		wins  []int
		other []error
	)
	start := make(chan struct{})
	for i, next := range targets {
		wg.Go(func() {
			<-start
			err := move(ctx, fx, id, from, next)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				wins = append(wins, i)
			case !isLoss(err):
				other = append(other, err)
			}
		})
	}
	close(start)
	wg.Wait()
	if len(other) > 0 {
		panic(fmt.Sprintf("aitest: a transition failed for a reason other than losing the race: %v", other[0]))
	}
	return wins
}
