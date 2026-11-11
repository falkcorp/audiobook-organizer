// file: core/internal/executor/dispatch_test.go
// version: 1.0.1
// guid: f380a3d2-0261-42b8-82c6-72a942b58624
// last-edited: 2026-11-10

package executor

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/tasks"
)

// synctestConfig is the Config of the synctest tests: a slow tick and no
// watchdog, so the fake clock only moves when the test waits.
func synctestConfig(o *Options) {
	o.Config = Config{DispatchTick: time.Second, WatchdogInterval: time.Hour}
}

// TestContinueInPlace_RequiresProgressAndCheckpoint (5T.3.6): a yield is
// honoured only for ResumeContinue and only when the attempt reported
// progress past its starting point and checkpointed; otherwise the run
// fails naming the missing proof.
func TestContinueInPlace_RequiresProgressAndCheckpoint(t *testing.T) {
	cells := []struct {
		name       string
		progress   int64 // -1: none
		checkpoint bool
		policy     tasks.ResumePolicy
		wantErr    []string // empty: requeued, then succeeds
		notErr     string
	}{
		{name: "progress and checkpoint", progress: 3, checkpoint: true, policy: tasks.ResumeContinue},
		{name: "no checkpoint", progress: 3, policy: tasks.ResumeContinue, wantErr: []string{"did not checkpoint"}, notErr: "no progress"},
		{name: "no progress", progress: -1, checkpoint: true, policy: tasks.ResumeContinue, wantErr: []string{"reported no progress"}, notErr: "checkpoint"},
		{name: "progress not past the start", progress: 0, checkpoint: true, policy: tasks.ResumeContinue, wantErr: []string{"reported no progress"}},
		{name: "neither", progress: -1, policy: tasks.ResumeContinue, wantErr: []string{"reported no progress", "did not checkpoint"}},
		{name: "not ResumeContinue", progress: 3, checkpoint: true, policy: tasks.ResumeFromZero, wantErr: []string{"only for ResumeContinue"}},
	}
	for _, c := range cells {
		t.Run(c.name, func(t *testing.T) {
			var calls atomic.Int32
			def := taskDef("gotham.casework", func(rc *tasks.RC, _ noParams) (string, error) {
				if calls.Add(1) > 1 {
					return "case closed", nil
				}
				if c.progress >= 0 {
					rc.Progress().Set(c.progress, tasks.Known(10))
				}
				if c.checkpoint {
					if err := rc.Checkpoint(map[string]int64{"page": c.progress}); err != nil {
						return "", err
					}
				}
				return "", tasks.ErrContinueInPlace
			}, withResume(c.policy))
			h := newHarness(t, []tasks.Definition{def}, nil)
			id := h.start("gotham.casework", tasks.StartRequest{})
			r := h.waitState(id, tasks.StateSucceeded, tasks.StateFailed)
			if len(c.wantErr) == 0 {
				if r.State != tasks.StateSucceeded || r.Attempt != 2 || r.Yields != 1 {
					t.Fatalf("yield with both proofs: %s attempt %d yields %d (%q), want succeeded on attempt 2 after 1 yield", r.State, r.Attempt, r.Yields, r.Error)
				}
				return
			}
			if r.State != tasks.StateFailed || calls.Load() != 1 {
				t.Fatalf("%s: %s after %d attempts, want failed after 1", c.name, r.State, calls.Load())
			}
			for _, w := range c.wantErr {
				if !strings.Contains(r.Error, w) {
					t.Errorf("error %q does not name %q", r.Error, w)
				}
			}
			if c.notErr != "" && strings.Contains(r.Error, c.notErr) {
				t.Errorf("error %q names %q, a proof the attempt gave", r.Error, c.notErr)
			}
		})
	}
}

// TestContinueInPlace_MaxYields (5T.3.6, core.tasks.max_yields; added for
// the yield invariants): a run that keeps yielding fails once it reaches
// max_yields consecutive yields.
func TestContinueInPlace_MaxYields(t *testing.T) {
	var calls atomic.Int64
	def := taskDef("gotham.endlesscase", func(rc *tasks.RC, _ noParams) (string, error) {
		n := calls.Add(1)
		rc.Progress().Set(n*10, tasks.Unknown())
		if err := rc.Checkpoint(n); err != nil {
			return "", err
		}
		return "", tasks.ErrContinueInPlace
	})
	h := newHarness(t, []tasks.Definition{def}, func(o *Options) { o.Config.MaxYields = 2 })
	r := h.waitState(h.start("gotham.endlesscase", tasks.StartRequest{}), tasks.StateFailed, tasks.StateSucceeded)
	if r.State != tasks.StateFailed || r.Reason != "max_yields" || r.Yields != 2 || calls.Load() != 3 {
		t.Fatalf("endless yields: %s/%q yields %d after %d attempts, want failed/max_yields after 2 yields and 3 attempts", r.State, r.Reason, r.Yields, calls.Load())
	}
}

// TestContinueInPlace_SameRunIDFreshTimeout (5T.3.6, synctest): a honoured
// yield re-queues the same run, and the next attempt's Timeout starts
// afresh: two attempts of 9 minutes each finish under a 10-minute Timeout.
func TestContinueInPlace_SameRunIDFreshTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		var secondID tasks.RunID
		var secondBudget time.Duration
		def := taskDef("gotham.longcase", func(rc *tasks.RC, _ noParams) (string, error) {
			if calls.Add(1) == 1 {
				rc.Progress().Set(5, tasks.Known(10))
				if err := rc.Checkpoint(map[string]int{"page": 5}); err != nil {
					return "", err
				}
				time.Sleep(9 * time.Minute)
				return "", tasks.ErrContinueInPlace
			}
			secondID, secondBudget = rc.RunID(), time.Until(rc.Deadline())
			time.Sleep(9 * time.Minute)
			return "case closed", nil
		}, withTimeout(10*time.Minute))
		h := newHarness(t, []tasks.Definition{def}, synctestConfig)
		id := h.start("gotham.longcase", tasks.StartRequest{})
		time.Sleep(19 * time.Minute) // both attempts; past one Timeout
		synctest.Wait()
		r := h.waitState(id, tasks.StateSucceeded, tasks.StateFailed, tasks.StateTimedOut)
		if r.State != tasks.StateSucceeded || r.Attempt != 2 || r.Yields != 1 {
			t.Fatalf("after the yield: %s attempt %d yields %d (%q), want succeeded, attempt 2, 1 yield", r.State, r.Attempt, r.Yields, r.Error)
		}
		if secondID != id {
			t.Fatalf("second attempt ran as %s, want the same run %s", secondID, id)
		}
		if secondBudget < 9*time.Minute {
			t.Fatalf("second attempt started with %s left, want a fresh ~10m Timeout", secondBudget)
		}
	})
}

// TestZombieCapBlocksModule (5T.4, core.tasks.zombie_cap): once a module
// has zombie_cap abandoned attempts still alive, its new runs wait; other
// modules dispatch; the module dispatches again when a zombie exits.
func TestZombieCapBlocksModule(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		started := make(chan struct{}, 1)
		release := make(chan struct{})
		stubborn := taskDef("gotham.interrogation", func(rc *tasks.RC, _ noParams) (string, error) {
			started <- struct{}{}
			<-release // ignores the cancel: becomes a zombie after its grace
			return "talked", nil
		}, withCancel(tasks.Cooperative(time.Second)))
		h := newHarness(t, []tasks.Definition{stubborn, taskDef("gotham.paperwork", okBody), taskDef("arkham.paperwork", okBody)}, func(o *Options) {
			synctestConfig(o)
			o.Config.ZombieCap = 1
		})
		z := h.start("gotham.interrogation", tasks.StartRequest{})
		<-started
		if err := h.x.Cancel(context.Background(), z); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Second)
		synctest.Wait()
		if r := h.get(z); r.State != tasks.StateCanceled {
			t.Fatalf("after the grace: %s, want canceled (abandoned, goroutine still alive)", r.State)
		}
		same := h.start("gotham.paperwork", tasks.StartRequest{})
		other := h.start("arkham.paperwork", tasks.StartRequest{})
		h.waitState(other, tasks.StateSucceeded)
		h.stays(same, tasks.StateQueued, 5*time.Second)
		close(release)
		h.waitState(same, tasks.StateSucceeded)
	})
}

// caseSubjects is a per-run write set from params: "" none, "*" every case,
// otherwise the one case named.
func caseSubjects(module string) tasks.SubjectsFunc {
	return func(raw json.RawMessage) ([]entity.Ref, error) {
		var p noParams
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		switch p.Case {
		case "":
			return nil, nil
		case "*":
			return []entity.Ref{tasks.Res(module, "cases")}, nil
		}
		return []entity.Ref{tasks.ResID(module, "cases", p.Case)}, nil
	}
}

// TestWriteSet_ResourceOverlapMatrix (5T.3.4): runs whose write sets
// overlap never run at once; disjoint write sets, reads and other resource
// kinds do not wait.
func TestWriteSet_ResourceOverlapMatrix(t *testing.T) {
	subjects := func(c *tasks.Common) { c.Subjects = caseSubjects("gotham") }
	cells := []struct {
		name       string
		holder     string // gotham.evidence's case
		kind       string
		contender  string
		concurrent bool
	}{
		{name: "same case", holder: "ace", kind: "gotham.forgery", contender: "ace"},
		{name: "different cases", holder: "ace", kind: "gotham.forgery", contender: "bane", concurrent: true},
		{name: "kind-wide holder", holder: "*", kind: "gotham.forgery", contender: "bane"},
		{name: "kind-wide contender", holder: "ace", kind: "gotham.forgery", contender: "*"},
		{name: "reader of the same resource", holder: "*", kind: "gotham.audit", concurrent: true},
		{name: "writer of another resource kind", holder: "*", kind: "gotham.lab", concurrent: true},
	}
	for _, c := range cells {
		t.Run(c.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				started := make(chan tasks.RunID, 1)
				defs := []tasks.Definition{
					taskDef("gotham.evidence", blockUntilCtx(started), subjects),
					taskDef("gotham.forgery", okBody, subjects),
					taskDef("gotham.audit", okBody, readOnly("gotham.audit")),
					taskDef("gotham.lab", okBody, func(c *tasks.Common) { c.Effects = tasks.Writes(tasks.Res("gotham", "lab")) }),
				}
				h := newHarness(t, defs, synctestConfig)
				holder := h.start("gotham.evidence", tasks.StartRequest{Params: mustParams(t, c.holder)})
				<-started
				contender := h.start(c.kind, tasks.StartRequest{Params: mustParams(t, c.contender)})
				if c.concurrent {
					h.waitState(contender, tasks.StateSucceeded)
					if r := h.get(holder); r.State != tasks.StateRunning {
						t.Fatalf("holder is %s, want still running", r.State)
					}
					return
				}
				h.stays(contender, tasks.StateQueued, 5*time.Second)
				if err := h.x.Cancel(context.Background(), holder); err != nil {
					t.Fatal(err)
				}
				h.waitState(contender, tasks.StateSucceeded)
			})
		})
	}
}

func mustParams(t testing.TB, c string) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(noParams{Case: c})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestConcurrencyKey_NamespaceEnforced (5T.3.4): a ConcurrencyKey is the
// module's own or core's; another module's key is refused at New. Kinds
// sharing a key never run at once.
func TestConcurrencyKey_NamespaceEnforced(t *testing.T) {
	key := func(k tasks.Key) func(*tasks.Common) { return func(c *tasks.Common) { c.ConcurrencyKey = k } }
	stores, ks := bootStores(t, vfs.NewCrashableMem())
	for _, c := range []struct {
		key tasks.Key
		ok  bool
	}{{"gotham.signal", true}, {"core.storage", true}, {"arkham.signal", false}} {
		x, err := New(Options{Stores: stores, Keyspace: &ks, Definitions: []tasks.Definition{taskDef("gotham.watch", okBody, key(c.key))}, InstrumentsOff: true, Logger: quietLogger()})
		switch {
		case c.ok && err != nil:
			t.Errorf("ConcurrencyKey %q: New: %v, want accepted", c.key, err)
		case !c.ok && (err == nil || !strings.Contains(err.Error(), "namespace")):
			t.Errorf("ConcurrencyKey %q: New: %v, want refused as outside the namespace", c.key, err)
		}
		if x != nil {
			_ = x.Shutdown(context.Background())
		}
	}
	synctest.Test(t, func(t *testing.T) {
		started := make(chan tasks.RunID, 1)
		h := newHarness(t, []tasks.Definition{
			taskDef("gotham.watch", blockUntilCtx(started), key("gotham.signal")),
			taskDef("gotham.beacon", okBody, key("gotham.signal")),
			taskDef("gotham.lantern", okBody, func(c *tasks.Common) { c.Effects = tasks.Writes(tasks.Res("gotham", "lab")) }),
		}, synctestConfig)
		w := h.start("gotham.watch", tasks.StartRequest{})
		<-started
		b := h.start("gotham.beacon", tasks.StartRequest{})
		free := h.start("gotham.lantern", tasks.StartRequest{})
		h.waitState(free, tasks.StateSucceeded)
		h.stays(b, tasks.StateQueued, 5*time.Second)
		if err := h.x.Cancel(context.Background(), w); err != nil {
			t.Fatal(err)
		}
		h.waitState(b, tasks.StateSucceeded)
	})
}

// TestMode_OmittedIsPreviewForWriters (5T.3.7 rule 2): an omitted mode is
// Preview for a writer and Live for a ReadOnly kind on Start; StartIn
// refuses it; a child enqueued without a mode takes its parent's.
func TestMode_OmittedIsPreviewForWriters(t *testing.T) {
	childModes := make(chan tasks.Mode, 2)
	// The child writes another resource: a parent awaiting a child whose
	// write set overlaps its own would wait forever (the write-set gate has
	// no parent exemption, as HEAD's Gate 3b).
	child := taskDef("gotham.child", func(rc *tasks.RC, _ noParams) (string, error) {
		childModes <- rc.Mode()
		return "", nil
	}, func(c *tasks.Common) { c.Effects = tasks.Writes(tasks.Res("gotham", "lab")) })
	parent := taskDef("gotham.parent", func(rc *tasks.RC, _ noParams) (string, error) {
		ref, err := rc.Enqueue("gotham.child", noParams{})
		if err != nil {
			return "", err
		}
		_, err = rc.Await(ref)
		return "", err
	})
	h := newHarness(t, []tasks.Definition{taskDef("gotham.writer", okBody), taskDef("gotham.reader", okBody, readOnly("gotham.reader")), child, parent},
		func(o *Options) { o.Catalog = catalogWith(t, "gotham:run") }) // gotham's system actor may start its kinds
	ctx := context.Background()
	for kind, want := range map[tasks.Kind]tasks.Mode{"gotham.writer": tasks.ModePreview, "gotham.reader": tasks.ModeLive} {
		ref, err := h.x.Start(ctx, kind, tasks.StartRequest{})
		if err != nil {
			t.Fatal(err)
		}
		if r := h.get(ref.ID); r.Mode != want {
			t.Errorf("%s with no mode: %v, want %v", kind, r.Mode, want)
		}
	}
	b := h.ks.NewIndexedBatch()
	defer func() { _ = b.Close() }()
	if _, err := h.x.StartIn(ctx, b, "gotham.writer", tasks.StartRequest{}); !errors.Is(err, tasks.ErrModeUnset) {
		t.Errorf("StartIn with no mode: %v, want ErrModeUnset", err)
	}
	for _, mode := range []tasks.ModeRequest{tasks.ModeRequestLive, tasks.ModeRequestPreview} {
		ref, err := h.x.Start(ctx, "gotham.parent", tasks.StartRequest{Mode: mode})
		if err != nil {
			t.Fatal(err)
		}
		h.waitState(ref.ID, tasks.StateSucceeded)
		want, _ := mode.Mode()
		if got := <-childModes; got != want {
			t.Errorf("child of a %v parent ran %v, want the parent's mode", want, got)
		}
	}
}

// TestPreview_WriterRecordsIntentOnly (5T.3.7): in Preview, Writer.Do
// records the intent, never calls fn and returns ErrPreview; in Live it
// records the intent and calls fn. A write outside Effects.Writes is
// refused in both.
func TestPreview_WriterRecordsIntentOnly(t *testing.T) {
	type outcome struct {
		doErr, outsideErr error
		called            bool
	}
	got := make(chan outcome, 1)
	ace := tasks.ResID("gotham", "cases", "ace")
	def := taskDef("gotham.closecase", func(rc *tasks.RC, _ noParams) (string, error) {
		var o outcome
		w := rc.Writer()
		o.doErr = w.Do(rc.Context(), tasks.Write{Name: "gotham.close_case", Resource: tasks.Res("gotham", "cases"), Subject: ace},
			func(context.Context) ([]tasks.Change, error) {
				o.called = true
				return []tasks.Change{{Subject: ace, Field: "status", Old: "open", New: "closed"}}, nil
			})
		o.outsideErr = w.Do(rc.Context(), tasks.Write{Name: "gotham.burn_files", Resource: tasks.Res("arkham", "files"), Subject: ace},
			func(context.Context) ([]tasks.Change, error) { return nil, nil })
		got <- o
		return "filed", nil
	})
	h := newHarness(t, []tasks.Definition{def}, nil)
	for _, mode := range []tasks.ModeRequest{tasks.ModeRequestPreview, tasks.ModeRequestLive} {
		id := h.start("gotham.closecase", tasks.StartRequest{Mode: mode})
		h.waitState(id, tasks.StateSucceeded)
		o := <-got
		preview := mode == tasks.ModeRequestPreview
		if preview && (!errors.Is(o.doErr, tasks.ErrPreview) || o.called) {
			t.Errorf("Preview: Do returned %v, fn called %v; want ErrPreview and not called", o.doErr, o.called)
		}
		if !preview && (o.doErr != nil || !o.called) {
			t.Errorf("Live: Do returned %v, fn called %v; want nil and called", o.doErr, o.called)
		}
		if !errors.Is(o.outsideErr, tasks.ErrOutsideEffects) {
			t.Errorf("%v: write outside Effects.Writes: %v, want ErrOutsideEffects", mode, o.outsideErr)
		}
		var intents []tasks.PageEntry
		for p, err := range h.stores.Runs.Pages(context.Background(), id, tasks.PageQuery{Page: tasks.PageIntents}) {
			if err != nil {
				t.Fatal(err)
			}
			intents = append(intents, p)
		}
		if len(intents) != 1 || intents[0].Write.Name != "gotham.close_case" || intents[0].Write.Subject != ace {
			t.Fatalf("%v: intents %+v, want exactly the one close_case intent", mode, intents)
		}
	}
}
