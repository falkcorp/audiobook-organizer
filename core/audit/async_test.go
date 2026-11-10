// file: core/audit/async_test.go
// version: 1.0.1
// guid: c4f8a1d7-6b32-4e95-8a0d-5f7e3b9c1d28
// last-edited: 2026-11-10

package audit

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"testing/synctest"
	"time"

	"github.com/falkcorp/mediacore/entity"
)

// sinkStore is a store over sink, for the tests that run the writer under
// testing/synctest (its fake clock and timers) and so cannot reach Pebble.
// The keyspace and sequence only satisfy newStore; the sink replaces them.
func sinkStore(t *testing.T, e *env, opts Options, sk sink) *Store {
	t.Helper()
	s, err := newStore(opts, e.deps())
	if err != nil {
		t.Fatal(err)
	}
	s.sink = sk
	return s
}

func denial(route string) Event {
	return Event{
		Action: "core.auth.login", Outcome: Denied, Actor: Actor{ClientAddr: "192.0.2.9", UserAgent: "Batarang/2"},
		Route: route, Permission: "core.role.update", Reason: "scope_escalation",
	}
}

// TestAudit_DenialCoalescing (05-rbac §7.3): identical denials inside the
// window fold into one record whose Coalesced is their count; a denial that
// differs in any part of its key, and an event that is not a denial, are
// ordinary records. A lone denial has Coalesced 0. Runs on the synctest
// clock, so the one-minute window costs no time.
func TestAudit_DenialCoalescing(t *testing.T) {
	e := newEnv(t)
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		sk := &memSink{}
		s := sinkStore(t, e, Options{}, sk)
		if err := s.Start(ctx); err != nil {
			t.Fatal(err)
		}

		for range 5 {
			s.Emit(ctx, denial("POST /api/core/v1/roles/{id}"))
		}
		s.Emit(ctx, denial("DELETE /api/core/v1/roles/{id}")) // another route: its own fold
		other := denial("POST /api/core/v1/roles/{id}")
		other.Actor.ClientAddr = "192.0.2.10" // another caller: its own fold
		s.Emit(ctx, other)
		s.Emit(ctx, Event{Action: "core.auth.login", Outcome: Failure, Actor: bruce()})
		synctest.Wait()

		// Inside the window nothing but the failure is written.
		time.Sleep(10 * time.Second)
		synctest.Wait()
		if got := sk.records(); len(got) != 1 || got[0].Outcome != Failure || got[0].Coalesced != 0 {
			t.Fatalf("inside the window the sink holds %d record(s) %+v, want only the failure", len(got), got)
		}

		// More identical denials, still inside the window, join the open fold.
		for range 3 {
			s.Emit(ctx, denial("POST /api/core/v1/roles/{id}"))
		}
		synctest.Wait()

		// The window ends: each fold becomes one record.
		time.Sleep(time.Minute)
		synctest.Wait()
		got := sk.records()
		if len(got) != 4 {
			t.Fatalf("after the window the sink holds %d records, want 4: %+v", len(got), got)
		}
		counts := map[string]uint32{}
		for _, r := range got[1:] {
			if r.Outcome != Denied {
				t.Errorf("record %+v is not a denial", r)
			}
			counts[r.Route+"|"+r.Actor.ClientAddr] = r.Coalesced
		}
		if c := counts["POST /api/core/v1/roles/{id}|192.0.2.9"]; c != 8 {
			t.Errorf("the 5+3 identical denials folded to Coalesced=%d, want 8", c)
		}
		if c, ok := counts["DELETE /api/core/v1/roles/{id}|192.0.2.9"]; !ok || c != 0 {
			t.Errorf("the lone denial has Coalesced=%d (present %v), want 0", c, ok)
		}
		if c, ok := counts["POST /api/core/v1/roles/{id}|192.0.2.10"]; !ok || c != 0 {
			t.Errorf("the other caller's denial has Coalesced=%d (present %v), want 0", c, ok)
		}

		// After the window a new identical denial opens a new fold.
		s.Emit(ctx, denial("POST /api/core/v1/roles/{id}"))
		synctest.Wait()
		if err := s.Stop(ctx); err != nil {
			t.Fatal(err)
		}
		got = sk.records()
		if len(got) != 5 || got[4].Coalesced != 0 || got[4].Outcome != Denied {
			t.Errorf("after Stop the sink holds %d records, last %+v: want the open fold written as a lone denial", len(got), got[len(got)-1])
		}
		if d := s.ctr.dropped.Load(); d != 0 {
			t.Errorf("dropped = %d, want 0", d)
		}
	})
}

// TestAudit_CoalescingOffAndDrain: a negative window keeps every denial; Stop
// writes the open folds whatever their window.
func TestAudit_CoalescingOffAndDrain(t *testing.T) {
	e := newEnv(t)
	synctest.Test(t, func(t *testing.T) {
		ctx := context.Background()
		sk := &memSink{}
		s := sinkStore(t, e, Options{CoalesceWindow: -1}, sk)
		if err := s.Start(ctx); err != nil {
			t.Fatal(err)
		}
		for range 3 {
			s.Emit(ctx, denial("GET /api/core/v1/roles"))
		}
		synctest.Wait()
		time.Sleep(time.Second)
		synctest.Wait()
		if got := sk.records(); len(got) != 3 {
			t.Fatalf("coalescing off: %d records, want 3", len(got))
		}
		if err := s.Stop(ctx); err != nil {
			t.Fatal(err)
		}
	})
	synctest.Test(t, func(t *testing.T) {
		ctx := context.Background()
		sk := &memSink{}
		s := sinkStore(t, e, Options{}, sk)
		if err := s.Start(ctx); err != nil {
			t.Fatal(err)
		}
		for range 4 {
			s.Emit(ctx, denial("GET /api/core/v1/roles"))
		}
		synctest.Wait()
		if got := sk.records(); len(got) != 0 {
			t.Fatalf("a fold was written before its window ended: %+v", got)
		}
		if err := s.Stop(ctx); err != nil {
			t.Fatal(err)
		}
		if got := sk.records(); len(got) != 1 || got[0].Coalesced != 4 {
			t.Errorf("Stop wrote %+v, want one record with Coalesced=4", got)
		}
	})
}

// TestAudit_QueueFullDropsWithoutBlocking (05-rbac §7.3): Emit returns at once
// when the queue is full, the writer is stuck or the store is stopped, and
// counts what it did not queue.
func TestAudit_QueueFullDropsWithoutBlocking(t *testing.T) {
	e := newEnv(t)
	ev := Event{Action: "core.auth.login", Outcome: Failure, Actor: bruce()}
	within := func(t *testing.T, what string, f func()) {
		t.Helper()
		done := make(chan struct{})
		go func() { defer close(done); f() }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatalf("%s blocked", what)
		}
	}

	t.Run("writer not started", func(t *testing.T) {
		s := newStoreOn(t, e, Options{AsyncBuffer: 2}, Deps{})
		within(t, "Emit on a full queue", func() {
			for range 5 {
				s.Emit(context.Background(), ev)
			}
		})
		if d := s.ctr.dropped.Load(); d != 3 {
			t.Errorf("dropped = %d, want 3 of 5 into a queue of 2", d)
		}
		if err := s.Start(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err := s.Stop(context.Background()); err != nil {
			t.Fatal(err)
		}
		if got := all(t, s, Filter{}); len(got) != 2 {
			t.Errorf("the two queued events were not written: %d records", len(got))
		}
	})

	t.Run("writer stuck in a write", func(t *testing.T) {
		g := newGate()
		s := sinkStore(t, e, Options{AsyncBuffer: 2, BatchMax: 1}, g)
		ctx := t.Context()
		if err := s.Start(ctx); err != nil {
			t.Fatal(err)
		}
		s.Emit(ctx, ev) // the writer takes it and blocks in write
		<-g.entered
		within(t, "Emit while the writer is stuck", func() {
			for range 10 {
				s.Emit(ctx, ev)
			}
		})
		if d := s.ctr.dropped.Load(); d != 8 {
			t.Errorf("dropped = %d, want 8 (10 emitted, queue of 2)", d)
		}
		close(g.release)
		within(t, "Stop", func() {
			if err := s.Stop(context.Background()); err != nil {
				t.Errorf("Stop: %v", err)
			}
		})
	})

	t.Run("after Stop and for an invalid event", func(t *testing.T) {
		e := newEnv(t) // the other cases wrote records into the shared one
		s := newStoreOn(t, e, Options{}, Deps{})
		if err := s.Start(context.Background()); err != nil {
			t.Fatal(err)
		}
		s.Emit(context.Background(), Event{Action: "nope", Outcome: Failure})
		s.Emit(context.Background(), Event{Action: "core.auth.login", Outcome: "meh"})
		if d := s.ctr.dropped.Load(); d != 2 {
			t.Errorf("dropped = %d after two refused events, want 2", d)
		}
		if err := s.Stop(context.Background()); err != nil {
			t.Fatal(err)
		}
		within(t, "Emit after Stop", func() { s.Emit(context.Background(), ev) })
		if d := s.ctr.dropped.Load(); d != 3 {
			t.Errorf("dropped = %d after an Emit on a stopped store, want 3", d)
		}
		if got := all(t, s, Filter{}); len(got) != 0 {
			t.Errorf("refused or late events were written: %d records", len(got))
		}
	})
}

// TestAudit_StartStop: the lifecycle. A store starts once and stops for good,
// Stop is repeatable, and Start's context ends the writer as Stop does.
func TestAudit_StartStop(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)

	t.Run("lifecycle", func(t *testing.T) {
		s := newStoreOn(t, e, Options{BatchWait: time.Millisecond}, Deps{})
		if err := s.Start(ctx); err != nil {
			t.Fatal(err)
		}
		if err := s.Start(ctx); !errors.Is(err, ErrStarted) {
			t.Errorf("second Start = %v, want ErrStarted", err)
		}
		for range 7 {
			s.Emit(ctx, Event{Action: "core.auth.login", Outcome: Failure, Actor: bruce(), Target: entity.Ref{Module: "core", Kind: "user", ID: "oracle"}})
		}
		if err := s.Stop(ctx); err != nil {
			t.Fatal(err)
		}
		if err := s.Stop(ctx); err != nil {
			t.Errorf("second Stop = %v", err)
		}
		if err := s.Start(ctx); !errors.Is(err, ErrStopped) {
			t.Errorf("Start after Stop = %v, want ErrStopped", err)
		}
		if got := all(t, s, Filter{Target: entity.Ref{Module: "core", Kind: "user", ID: "oracle"}}); len(got) != 7 {
			t.Errorf("Stop drained %d of 7 queued events", len(got))
		}
		// Record still works on a stopped store: it is the synchronous path.
		if err := s.Record(ctx, success("core.auth.logout")); err != nil {
			t.Errorf("Record after Stop: %v", err)
		}
	})

	t.Run("start context ends the writer", func(t *testing.T) {
		s := newStoreOn(t, e, Options{}, Deps{})
		app, cancel := context.WithCancel(ctx)
		if err := s.Start(app); err != nil {
			t.Fatal(err)
		}
		s.Emit(ctx, Event{Action: "core.auth.setup", Outcome: Success, Actor: bruce()})
		cancel()
		select {
		case <-s.done:
		case <-time.After(10 * time.Second):
			t.Fatal("the writer outlived its start context")
		}
		if got := all(t, s, Filter{ActionPrefix: "core.auth.setup"}); len(got) != 1 {
			t.Errorf("the queue was not drained on cancel: %d records", len(got))
		}
		if err := s.Stop(ctx); err != nil {
			t.Errorf("Stop after the context ended = %v", err)
		}
	})

	t.Run("stop without start counts the queue as dropped", func(t *testing.T) {
		s := newStoreOn(t, e, Options{}, Deps{})
		s.Emit(ctx, Event{Action: "core.auth.setup", Outcome: Success, Actor: bruce()})
		s.Emit(ctx, Event{Action: "core.auth.setup", Outcome: Success, Actor: bruce()})
		if err := s.Stop(ctx); err != nil {
			t.Fatal(err)
		}
		if d := s.ctr.dropped.Load(); d != 2 {
			t.Errorf("dropped = %d, want 2", d)
		}
	})

	t.Run("stop gives up when its context ends", func(t *testing.T) {
		g := newGate()
		s := sinkStore(t, e, Options{BatchMax: 1}, g)
		if err := s.Start(ctx); err != nil {
			t.Fatal(err)
		}
		s.Emit(ctx, Event{Action: "core.auth.login", Outcome: Failure})
		s.Emit(ctx, Event{Action: "core.auth.login", Outcome: Failure})
		<-g.entered
		short, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
		defer cancel()
		if err := s.Stop(short); !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("Stop = %v, want the context's deadline error", err)
		}
		select {
		case <-s.done: // the abandoned write fails, the writer drops the rest and exits
		case <-time.After(10 * time.Second):
			t.Fatal("the writer never exited after an abandoned Stop")
		}
		if d := s.ctr.dropped.Load(); d != 2 {
			t.Errorf("dropped = %d, want 2 (the stuck batch and the queued event)", d)
		}
	})

	t.Run("a failed batch is counted and the writer carries on", func(t *testing.T) {
		sk := &memSink{fail: errors.New("the Batcave is flooded")}
		s := sinkStore(t, e, Options{BatchWait: time.Millisecond, BatchMax: 1}, sk)
		if err := s.Start(ctx); err != nil {
			t.Fatal(err)
		}
		s.Emit(ctx, Event{Action: "core.auth.login", Outcome: Failure})
		time.Sleep(50 * time.Millisecond)
		s.Emit(ctx, Event{Action: "core.auth.logout", Outcome: Success})
		if err := s.Stop(ctx); err != nil {
			t.Fatal(err)
		}
		got := sk.records()
		if len(got) != 1 || got[0].Action != "core.auth.logout" {
			t.Errorf("records after one failed batch = %+v, want only the logout", got)
		}
		if s.ctr.asyncErrs.Load() != 1 || s.ctr.dropped.Load() != 1 {
			t.Errorf("async write errors = %d, dropped = %d, want 1 and 1", s.ctr.asyncErrs.Load(), s.ctr.dropped.Load())
		}
	})
}

// TestAudit_OpenFoldsAreBounded: the open denial folds never exceed
// AsyncBuffer, however many distinct keys arrive inside one window. At the
// cap the oldest fold is written early, so nothing is lost and nothing is
// dropped; memory is bounded by the queue, the folds and one batch.
func TestAudit_OpenFoldsAreBounded(t *testing.T) {
	e := newEnv(t)
	synctest.Test(t, func(t *testing.T) {
		ctx := context.Background()
		sk := &memSink{}
		const bufferSize, distinct = 4, 13
		s := sinkStore(t, e, Options{AsyncBuffer: bufferSize, BatchMax: 3}, sk)
		if err := s.Start(ctx); err != nil {
			t.Fatal(err)
		}
		// One denial each from many addresses, all inside the one-minute window.
		for i := range distinct {
			ev := denial("POST /api/core/v1/roles/{id}")
			ev.Actor.ClientAddr = fmt.Sprintf("192.0.2.%d", 10+i)
			s.Emit(ctx, ev)
			synctest.Wait() // the writer has taken it: the queue never fills
		}
		// Let the batch timer flush what the early endings left ready.
		time.Sleep(DefaultBatchWait)
		synctest.Wait()
		if got := len(sk.records()); got != distinct-bufferSize {
			t.Fatalf("inside the window %d records are written, want %d: all but the %d open folds", got, distinct-bufferSize, bufferSize)
		}
		for _, r := range sk.records() {
			if r.Outcome != Denied || r.Coalesced != 0 {
				t.Errorf("early record %+v: want a lone denial", r)
			}
		}
		if d := s.ctr.dropped.Load(); d != 0 {
			t.Errorf("dropped = %d, want 0: an early ending writes the fold, it does not drop it", d)
		}

		// A repeat of a key that is still open folds into it; one whose fold
		// ended early opens a new fold.
		again := denial("POST /api/core/v1/roles/{id}")
		again.Actor.ClientAddr = fmt.Sprintf("192.0.2.%d", 10+distinct-1) // the newest, still open
		s.Emit(ctx, again)
		synctest.Wait()

		// The window ends: the open folds are written, and every denial is accounted for.
		time.Sleep(2 * time.Minute)
		synctest.Wait()
		var total uint32
		for _, r := range sk.records() {
			total += max(r.Coalesced, 1)
		}
		if total != distinct+1 {
			t.Errorf("records stand for %d denials, want %d (every one emitted)", total, distinct+1)
		}
		if got := len(sk.records()); got != distinct {
			t.Errorf("%d records, want %d (the repeat folded into its open fold)", got, distinct)
		}
		if err := s.Stop(ctx); err != nil {
			t.Fatal(err)
		}
	})
}

// TestAudit_FoldKey (05-rbac §7.3): denials fold on actor, route, permission,
// outcome, target and reason, so two that differ by any one of them are two
// records, and identical denials still fold. A route is a pattern, so without
// the target a caller walking many targets through one route would show only
// the first.
func TestAudit_FoldKey(t *testing.T) {
	e := newEnv(t)
	robin := entity.Ref{Module: "core", Kind: "role", ID: "robin"}
	cases := map[string]func(Event) Event{
		"credential":  func(e Event) Event { e.Actor.CredentialID = "sess_arkham"; return e },
		"user":        func(e Event) Event { e.Actor.UserID = "harvey_dent"; return e },
		"address":     func(e Event) Event { e.Actor.ClientAddr = "192.0.2.77"; return e },
		"permission":  func(e Event) Event { e.Permission = "core.role.delete"; return e },
		"route":       func(e Event) Event { e.Route = "GET /api/core/v1/roles"; return e },
		"action":      func(e Event) Event { e.Action = "core.permission.deny"; return e },
		"reason":      func(e Event) Event { e.Reason = "dominance_required"; return e },
		"target":      func(e Event) Event { e.Target = robin; return e },
		"target kind": func(e Event) Event { e.Target = entity.Ref{Module: "core", Kind: "user", ID: "robin"}; return e },
		"target id":   func(e Event) Event { e.Target = entity.Ref{Module: "core", Kind: "role", ID: "nightwing"}; return e },
	}
	for name, mutate := range cases {
		t.Run("differs by "+name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx := context.Background()
				sk := &memSink{}
				s := sinkStore(t, e, Options{}, sk)
				if err := s.Start(ctx); err != nil {
					t.Fatal(err)
				}
				base := denial("POST /api/core/v1/roles/{id}")
				base.Target = entity.Ref{Module: "core", Kind: "role", ID: "oracle"}
				s.Emit(ctx, base)
				s.Emit(ctx, mutate(base))
				if err := s.Stop(ctx); err != nil {
					t.Fatal(err)
				}
				got := sk.records()
				if len(got) != 2 {
					t.Fatalf("%d records, want 2: the denials differ by %s", len(got), name)
				}
				for _, r := range got {
					if r.Coalesced != 0 {
						t.Errorf("record %+v is a fold, want two lone denials", r)
					}
				}
			})
		})
	}

	t.Run("identical denials fold", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			ctx := context.Background()
			sk := &memSink{}
			s := sinkStore(t, e, Options{}, sk)
			if err := s.Start(ctx); err != nil {
				t.Fatal(err)
			}
			ev := denial("POST /api/core/v1/roles/{id}")
			ev.Target = robin
			for range 3 {
				s.Emit(ctx, ev)
			}
			if err := s.Stop(ctx); err != nil {
				t.Fatal(err)
			}
			got := sk.records()
			if len(got) != 1 || got[0].Coalesced != 3 || got[0].Target != robin || got[0].Reason != "scope_escalation" {
				t.Errorf("records = %+v, want one record, Coalesced=3, target robin, reason scope_escalation", got)
			}
		})
	})
}
