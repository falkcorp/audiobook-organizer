// file: internal/searchcache/cache_review_test.go
// version: 1.0.0
// guid: 474e41ca-6072-44de-b9c3-da8b78639664
// last-edited: 2026-09-25

package searchcache

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
	"time"
)

// matchCountingEval is fakeEval that counts Match calls.
type matchCountingEval struct {
	*fakeEval
	matches atomic.Int64
}

func (e *matchCountingEval) Match(ctx context.Context, ids []string) ([]string, bool, error) {
	e.matches.Add(1)
	return e.fakeEval.Match(ctx, ids)
}

// Review 2, finding 1: a change set too large to patch cheaply is not
// re-evaluated at all. Before the fix patch() ran Match over the whole changed
// set and only then compared the matches with PatchLimit.
func TestCache_PatchSkipsReevaluationPastMaxPatchChanged(t *testing.T) {
	changes := NewChangeLog(64)
	c := New(changes, Config{PatchLimit: 2, MaxPatchChanged: 4})
	ev := &matchCountingEval{fakeEval: newFake(map[string]string{"b1": "red"}, "red")}
	ctx := context.Background()
	if _, err := c.Lookup(ctx, "k", ev, exactOpts); err != nil {
		t.Fatal(err)
	}
	changes.Record("x1", "x2", "x3", "x4", "x5")
	if _, err := c.Lookup(ctx, "k", ev, webOpts(time.Second)); err != nil {
		t.Fatal(err)
	}
	if n := ev.matches.Load(); n != 0 {
		t.Fatalf("Match ran %d times over a changed set past MaxPatchChanged; want 0", n)
	}
	// A change set within the cap is still patched.
	waitFor(t, func() bool { return c.Stats().Building == 0 })
	ev.set("b2", "red")
	changes.Record("b2")
	res, err := c.Lookup(ctx, "k", ev, exactOpts)
	if err != nil || !reflect.DeepEqual(res.IDs, []string{"b1", "b2"}) {
		t.Fatalf("small patch = %+v, %v", res, err)
	}
	if n := ev.matches.Load(); n != 1 {
		t.Fatalf("Match ran %d times for a small change set; want 1", n)
	}
}

// Review 2, finding 1: patch work holds a build slot, so MaxConcurrentBuilds
// bounds patches and builds together. Before the fix a patch ran its
// re-evaluation while every slot was taken.
func TestCache_PatchWaitsForBuildSlot(t *testing.T) {
	changes := NewChangeLog(64)
	c := New(changes, Config{MaxConcurrentBuilds: 1})
	ctx := context.Background()
	ev := &matchCountingEval{fakeEval: newFake(map[string]string{"b1": "red"}, "red")}
	if _, err := c.Lookup(ctx, "k", ev, exactOpts); err != nil {
		t.Fatal(err)
	}
	// Occupy the only slot with a build of another key.
	blocker := newFake(map[string]string{"z": "blue"}, "blue")
	blocker.block = make(chan struct{})
	var pe *PendingError
	if _, err := c.Lookup(ctx, "other", blocker, webOpts(time.Millisecond)); !errors.As(err, &pe) {
		t.Fatalf("blocker lookup err = %v, want pending", err)
	}
	waitFor(t, func() bool { s, _ := c.Job(pe.SearchID); return s.Status == "running" })

	ev.set("b2", "red")
	changes.Record("b2")
	done := make(chan error, 1)
	go func() {
		res, err := c.Lookup(ctx, "k", ev, exactOpts)
		if err == nil && !reflect.DeepEqual(res.IDs, []string{"b1", "b2"}) {
			err = errors.New("patched list is wrong")
		}
		done <- err
	}()
	time.Sleep(50 * time.Millisecond)
	if n := ev.matches.Load(); n != 0 {
		t.Fatalf("patch re-evaluated %d times while every build slot was taken", n)
	}
	close(blocker.block)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if n := ev.matches.Load(); n != 1 {
		t.Fatalf("Match ran %d times; want 1", n)
	}
}

// Review 2, finding 2: a lookup that arrives while a queued build is being
// abandoned must not join it and be handed errAbandoned. Before the fix run()
// decided "idle" under the lock, released it, and removed the job from
// c.building only later, so a lookup in that gap joined a job that was about
// to end with errAbandoned.
func TestCache_AbandonedBuildIsNeverJoined(t *testing.T) {
	changes := NewChangeLog(64)
	c := New(changes, Config{MaxConcurrentBuilds: 1, AbandonAfter: time.Millisecond})
	ctx := context.Background()
	evB := newFake(map[string]string{"b1": "red"}, "red")

	got := make(chan error, 1)
	testHookAbandoned = func(key string) {
		if key != "b" {
			return
		}
		go func() {
			res, err := c.Lookup(ctx, "b", evB, exactOpts)
			if err == nil && !reflect.DeepEqual(res.IDs, []string{"b1"}) {
				err = errors.New("wrong ids")
			}
			got <- err
		}()
		// Hold the abandoning run here until the lookup has registered on
		// whatever job it found, which is where the race lived.
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			c.mu.Lock()
			j := c.building["b"]
			joined := j != nil && j.waiters > 0
			c.mu.Unlock()
			if joined {
				return
			}
			time.Sleep(time.Millisecond)
		}
	}
	t.Cleanup(func() { testHookAbandoned = nil })

	blocker := newFake(map[string]string{"z": "blue"}, "blue")
	blocker.block = make(chan struct{})
	var pe *PendingError
	if _, err := c.Lookup(ctx, "a", blocker, webOpts(time.Millisecond)); !errors.As(err, &pe) {
		t.Fatalf("blocker lookup err = %v, want pending", err)
	}
	waitFor(t, func() bool { s, _ := c.Job(pe.SearchID); return s.Status == "running" })
	c.queueRebuild("b", evB) // queued behind the blocker, nobody waiting
	time.Sleep(20 * time.Millisecond)
	close(blocker.block)

	select {
	case err := <-got:
		if err != nil {
			t.Fatalf("lookup during abandonment: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("abandonment hook never ran")
	}
}

// Review 2, finding 4: the drift-correcting rebuild a patch asks for must
// reflect the patched generation. When it joins a build already running from
// an older generation, that build's result is refused by store (the patched
// entry is newer), so a fresh build must follow it. Before the fix none did,
// and the patched list kept its build-time relevance order.
func TestCache_DriftRebuildIsNotLostToOlderBuild(t *testing.T) {
	changes := NewChangeLog(64)
	c := New(changes, Config{})
	fe := newFake(map[string]string{"b1": "red", "b3": "red"}, "red")
	fe.block = make(chan struct{}, 8) // one token per build allowed to finish
	ev := &driftEval{fakeEval: fe}
	ctx := context.Background()

	fe.block <- struct{}{}
	if _, err := c.Lookup(ctx, "k", ev, exactOpts); err != nil {
		t.Fatal(err)
	}
	// Patch 1 stores generation G1 and starts drift build B1, which blocks.
	fe.set("b2", "red")
	changes.Record("b2")
	if _, err := c.Lookup(ctx, "k", ev, exactOpts); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return fe.builds.Load() == 2 })
	// Patch 2 stores G2 > G1 and asks for a drift build again: B1 is still
	// running from G1.
	fe.set("b4", "red")
	changes.Record("b4")
	g2 := changes.Generation()
	res, err := c.Lookup(ctx, "k", ev, exactOpts)
	if err != nil || res.Gen < g2 {
		t.Fatalf("patch 2 = %+v, %v", res, err)
	}
	// Let every build finish: B1 (refused) and the follow-up.
	for i := 0; i < 4; i++ {
		fe.block <- struct{}{}
	}
	waitFor(t, func() bool { return fe.builds.Load() >= 3 && c.Stats().Building == 0 })
	c.mu.Lock()
	e := c.entries["k"]
	c.mu.Unlock()
	if e == nil || e.gen < g2 || !reflect.DeepEqual(e.ids, []string{"b1", "b2", "b3", "b4"}) {
		t.Fatalf("entry after follow-up rebuild = %+v", e)
	}
}
