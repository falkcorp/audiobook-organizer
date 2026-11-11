// file: core/internal/executor/lifecycle_test.go
// version: 1.6.1
// guid: 6c72973b-69ac-4ea7-a160-44fe469187ed
// last-edited: 2026-11-11

package executor

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/lifecycle"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/tasks"
)

// extendLog records ExtendTimeout requests.
type extendLog struct {
	mu   sync.Mutex
	asks []time.Duration
}

func (l *extendLog) extend(d time.Duration) error {
	l.mu.Lock()
	l.asks = append(l.asks, d)
	l.mu.Unlock()
	return nil
}

func (l *extendLog) got() []time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]time.Duration(nil), l.asks...)
}

// TestShutdown_UninterruptibleExtendsOnceOnlyUnderNotify (5T.4 step 4,
// synctest): Shutdown asks systemd for one EXTEND_TIMEOUT_USEC, capped by
// shutdown_extend_max, only when an Uninterruptible run is in flight and
// NOTIFY_SOCKET is set; otherwise the bounded drain is all it waits.
func TestShutdown_UninterruptibleExtendsOnceOnlyUnderNotify(t *testing.T) {
	cells := []struct {
		name     string
		notify   bool
		kinds    []string // fixture kinds in flight
		wantAsks []time.Duration
		want     tasks.RunState // the in-flight runs' state after Shutdown
	}{
		{name: "uninterruptible under notify", notify: true, kinds: []string{"gotham.vault"}, wantAsks: []time.Duration{time.Minute}, want: tasks.StateSucceeded},
		{name: "two uninterruptible runs ask once", notify: true, kinds: []string{"gotham.vault", "arkham.vault"}, wantAsks: []time.Duration{time.Minute}, want: tasks.StateSucceeded},
		{name: "no notify socket", kinds: []string{"gotham.vault"}, want: tasks.StateInterrupted},
		{name: "only a stubborn cooperative run", notify: true, kinds: []string{"gotham.stubborn"}, want: tasks.StateInterrupted},
	}
	for _, c := range cells {
		t.Run(c.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				started := make(chan struct{}, 2)
				work := func(rc *tasks.RC, _ noParams) (string, error) {
					started <- struct{}{}
					time.Sleep(30 * time.Second) // past the 10s drain, inside the extension
					return "vault sealed", nil
				}
				vault := func(c *tasks.Common) { c.Cancel = tasks.Uninterruptible("sealing the vault", 5*time.Minute) }
				defs := []tasks.Definition{
					taskDef("gotham.vault", work, vault),
					taskDef("arkham.vault", work, vault),
					taskDef("gotham.stubborn", work, withCancel(tasks.Cooperative(time.Second))),
				}
				ext := &extendLog{}
				h := newHarness(t, defs, func(o *Options) {
					synctestConfig(o)
					o.Config.ShutdownDrain = 10 * time.Second
					o.Config.ShutdownExtendMax = time.Minute
					o.ExtendTimeout = ext.extend
					o.Getenv = func(k string) string {
						if k == lifecycle.NotifySocketEnv && c.notify {
							return "@batcave-notify"
						}
						return ""
					}
				})
				var ids []tasks.RunID
				for _, k := range c.kinds {
					ids = append(ids, h.start(k, tasks.StartRequest{}))
					<-started
				}
				if err := h.x.Shutdown(context.Background()); err != nil {
					t.Fatal(err)
				}
				if got := ext.got(); !equalDurations(got, c.wantAsks) {
					t.Fatalf("ExtendTimeout requests %v, want %v", got, c.wantAsks)
				}
				for _, id := range ids {
					if r := h.get(id); r.State != c.want {
						t.Fatalf("run %s after Shutdown: %s, want %s", id, r.State, c.want)
					}
				}
				time.Sleep(time.Minute) // let an abandoned body return inside the bubble
			})
		})
	}
}

func equalDurations(a, b []time.Duration) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// heldFixture leaves runs of arkham.* in every unfinished state the held
// rule names, plus a terminal one, on one store, and returns their IDs.
type heldFixture struct {
	queued, interrupted, awaiting, done tasks.RunID
}

// arkhamDefs is the arkham module's kinds; intake and transfer signal
// started when their body runs.
func arkhamDefs(started chan<- tasks.RunID) []tasks.Definition {
	return []tasks.Definition{
		taskDef("arkham.intake", blockUntilCtx(started)),
		taskDef("arkham.transfer", blockUntilCtx(started), withResume(tasks.ResumeAsk)),
		taskDef("arkham.release", okBody),
	}
}

func buildHeldFixture(t *testing.T) (*harness, heldFixture) {
	t.Helper()
	ctx := context.Background()
	var f heldFixture
	// A run's body is waited for, not its stored running state: a claim
	// whose attempt is not live yet when Shutdown runs is released to the
	// queue (5T.4 step 4), and the fixture needs the run interrupted.
	started := make(chan tasks.RunID, 4)
	// Boot 1: a terminal run, and a ResumeAsk run interrupted by shutdown.
	h := newHarness(t, arkhamDefs(started), nil)
	f.done = h.start("arkham.release", tasks.StartRequest{})
	h.waitState(f.done, tasks.StateSucceeded)
	f.awaiting = h.start("arkham.transfer", tasks.StartRequest{})
	<-started
	if err := h.x.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	// Boot 2: the ResumeAsk run becomes awaiting_decision; a run is left
	// interrupted by shutdown and one queued behind the pause marker.
	h = h.reboot(arkhamDefs(started), nil)
	h.waitState(f.awaiting, tasks.StateAwaitingDecision)
	f.interrupted = h.start("arkham.intake", tasks.StartRequest{})
	<-started
	if err := h.x.Pause(ctx, "lockdown"); err != nil {
		t.Fatal(err)
	}
	f.queued = h.start("arkham.release", tasks.StartRequest{})
	if err := h.x.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	return h, f
}

// disabledArkham is the manifest set of a boot with arkham disabled: its
// definitions are not registered and its ledger names its kinds.
func disabledArkham(o *Options) {
	o.Manifests = []Manifest{{Module: "arkham", Enabled: false, TaskLedger: []tasks.Kind{"arkham.intake", "arkham.transfer", "arkham.release"}}}
}

// TestHeld_DisabledModuleRunsUntouchedAndBootSucceeds (5T.3.11): a boot
// with the module disabled succeeds, the resume sweep leaves its runs as
// stored (no drop, no requeue), and reads show them Held.
func TestHeld_DisabledModuleRunsUntouchedAndBootSucceeds(t *testing.T) {
	h, f := buildHeldFixture(t)
	before := map[tasks.RunID]tasks.RunRecord{}
	ids := []tasks.RunID{f.queued, f.interrupted, f.awaiting, f.done}
	for _, id := range ids {
		before[id] = h.get(id)
	}
	held := h.reboot([]tasks.Definition{taskDef("gotham.patrol", okBody)}, disabledArkham)
	if err := held.x.Unpause(context.Background()); err != nil {
		t.Fatal(err)
	}
	held.waitState(held.start("gotham.patrol", tasks.StartRequest{}), tasks.StateSucceeded) // the rest of the system runs
	for _, id := range ids {
		r := held.get(id)
		b := before[id]
		if r.State != b.State || r.Attempt != b.Attempt || r.FenceEpoch != b.FenceEpoch || r.Reason != b.Reason {
			t.Errorf("held run %s moved: %s/%d/%d/%q, was %s/%d/%d/%q", id, r.State, r.Attempt, r.FenceEpoch, r.Reason, b.State, b.Attempt, b.FenceEpoch, b.Reason)
		}
		v, err := held.x.Get(context.Background(), id)
		if err != nil || !v.Held {
			t.Errorf("Get(%s): held %v, err %v; want Held", id, v.Held, err)
		}
	}
	if s := before[f.queued].State; s != tasks.StateQueued {
		t.Fatalf("fixture: queued run is %s", s)
	}
	if s := before[f.interrupted].State; s != tasks.StateInterrupted {
		t.Fatalf("fixture: interrupted run is %s", s)
	}
}

// TestHeld_MutationsRefused409 (5T.3.11): every mutation of a held run is
// refused with ErrHeld (F2-05 maps it to 409 tasks.held), and so is a start
// of a held kind; reads still work.
func TestHeld_MutationsRefused409(t *testing.T) {
	h, f := buildHeldFixture(t)
	held := h.reboot(nil, disabledArkham)
	ctx := context.Background()
	checks := map[string]func() error{
		"Cancel":        func() error { return held.x.Cancel(ctx, f.interrupted) },
		"CancelAs":      func() error { return held.x.CancelAs(ctx, f.queued, tasks.Actor{UserID: "alfred"}) },
		"Decide resume": func() error { return held.x.Decide(ctx, f.awaiting, tasks.DecideResume) },
		"Decide drop":   func() error { return held.x.Decide(ctx, f.awaiting, tasks.DecideDrop) },
		"Approve":       func() error { return held.x.Approve(ctx, f.done, tasks.ApproveRequest{}) },
		"Discard":       func() error { return held.x.Discard(ctx, f.done) },
		"Start the kind": func() error {
			_, err := held.x.Start(ctx, "arkham.release", tasks.StartRequest{Mode: tasks.ModeRequestLive})
			return err
		},
	}
	for name, call := range checks {
		if err := call(); !errors.Is(err, ErrHeld) {
			t.Errorf("%s on a held run: %v, want ErrHeld", name, err)
		}
	}
	if _, err := held.x.Get(ctx, f.awaiting); err != nil {
		t.Fatalf("Get of a held run: %v (reads are allowed)", err)
	}
	if r := held.get(f.awaiting); r.State != tasks.StateAwaitingDecision {
		t.Fatalf("held run moved to %s", r.State)
	}
}

// TestStandDown_MarkerAtBootDefersQuiescedRun (5T.3.7): a stand-down marker
// found at boot means its holder died mid-work; the runs it quiesced stay
// interrupted_quiesced for that boot, the marker is cleared, and the next
// boot re-queues them.
func TestStandDown_MarkerAtBootDefersQuiescedRun(t *testing.T) {
	scanStarted := make(chan tasks.RunID, 2)
	holding := make(chan struct{}, 1)
	scan := func(body func(rc *tasks.RC, p noParams) (string, error)) tasks.Definition {
		return taskDef("gotham.scan", body, func(c *tasks.Common) { c.Yields = true })
	}
	holder := taskDef("gotham.compact", func(rc *tasks.RC, _ noParams) (string, error) {
		if _, err := rc.StandDown("gotham.scan"); err != nil {
			return "", err
		}
		holding <- struct{}{}
		<-rc.Context().Done()
		return "", context.Cause(rc.Context())
	}, withResume(tasks.ResumeDrop), func(c *tasks.Common) { c.Effects = tasks.Writes(tasks.Res("gotham", "store")) })
	h := newHarness(t, []tasks.Definition{scan(blockUntilCtx(scanStarted)), holder}, nil)
	s := h.start("gotham.scan", tasks.StartRequest{})
	<-scanStarted
	h.start("gotham.compact", tasks.StartRequest{})
	<-holding
	h.waitState(s, tasks.StateInterruptedQuiesced)
	// While the hold lives, a new run of the stood-down key waits (the
	// holder's own key and writes are disjoint from it).
	another := h.start("gotham.scan", tasks.StartRequest{Params: mustParams(t, "night")})
	h.stays(another, tasks.StateQueued, 300*time.Millisecond)

	defs := []tasks.Definition{scan(okBody), holder}
	second := h.crashBoot(defs, nil)
	second.stays(s, tasks.StateInterruptedQuiesced, 300*time.Millisecond)
	for m, err := range second.stores.StandDown.List(context.Background()) {
		if err != nil {
			t.Fatal(err)
		}
		t.Fatalf("marker %s survived the boot that found it", m.Key)
	}
	if err := second.x.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	third := second.reboot(defs, nil)
	third.waitState(s, tasks.StateSucceeded)
}

// TestResume_RestartGuardFailsCrashLoop (5T.3.6, HEAD worker.go's
// infinite-restart guard; added for the resume invariants): a run that was
// executing at boot after MaxAttempts claims is failed, not resumed again.
func TestResume_RestartGuardFailsCrashLoop(t *testing.T) {
	started := make(chan tasks.RunID, 4)
	defs := []tasks.Definition{taskDef("gotham.crashloop", blockUntilCtx(started))}
	cfg := func(o *Options) { o.Config = testConfig(); o.Config.MaxAttempts = 2 }
	h := newHarness(t, defs, cfg)
	id := h.start("gotham.crashloop", tasks.StartRequest{})
	<-started
	second := h.crashBoot(defs, cfg)
	<-started
	if r := second.get(id); r.Attempt != 2 || r.State != tasks.StateRunning {
		t.Fatalf("after one crash: %s attempt %d, want running attempt 2", r.State, r.Attempt)
	}
	third := second.crashBoot(defs, cfg)
	r := third.waitState(id, tasks.StateFailed, tasks.StateRunning)
	if r.State != tasks.StateFailed || r.Reason != "restart_loop" {
		t.Fatalf("after %d attempts: %s/%q, want failed/restart_loop", r.Attempt, r.State, r.Reason)
	}
}

// TestShutdown_StoppingCancelEndsCanceled (5T.4 step 4; added for the
// shutdown invariants): a run stopping for a cancel when Shutdown abandons
// it ends canceled, not interrupted, so it is never resumed, whichever came
// first. A cancel that lands while Shutdown is draining replaces the
// shutdown's stop (CancelAs: a cancel is final).
func TestShutdown_StoppingCancelEndsCanceled(t *testing.T) {
	for _, order := range []string{"cancel then shutdown", "shutdown then cancel"} {
		t.Run(order, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				started := make(chan struct{}, 1)
				release := make(chan struct{})
				def := taskDef("gotham.interrogation", func(rc *tasks.RC, _ noParams) (string, error) {
					started <- struct{}{}
					<-release
					return "talked", nil
				}, withCancel(tasks.Cooperative(time.Hour)))
				h := newHarness(t, []tasks.Definition{def}, func(o *Options) { synctestConfig(o); o.Config.ShutdownDrain = time.Second })
				id := h.start("gotham.interrogation", tasks.StartRequest{})
				<-started
				cancel := func() {
					if err := h.x.CancelAs(context.Background(), id, tasks.Actor{UserID: "gordon"}); err != nil {
						t.Fatal(err)
					}
				}
				if order == "cancel then shutdown" {
					cancel()
					if err := h.x.Shutdown(context.Background()); err != nil {
						t.Fatal(err)
					}
				} else {
					done := make(chan error, 1)
					go func() { done <- h.x.Shutdown(context.Background()) }()
					synctest.Wait() // Shutdown is draining: the run is stopping for it
					cancel()
					if err := <-done; err != nil {
						t.Fatal(err)
					}
				}
				if r := h.get(id); r.State != tasks.StateCanceled || r.Reason != reasonCancel {
					t.Fatalf("after Shutdown: %s/%q, want canceled/cancel", r.State, r.Reason)
				}
				close(release)
			})
		})
	}
}

// gatedRuns is a run store whose read of the from-zero marker parks until
// the test lets it go. startAttempt makes that read after the claim's
// running commit and before the attempt goes live, so a cancel, a
// shutdown or a stand-down sent while it is parked lands in the window
// startAttempt's go-live decision covers. With kind set, only that kind's
// claims park.
type gatedRuns struct {
	tasks.RunStore
	reached chan<- tasks.RunID
	release <-chan struct{}
	kind    tasks.Kind
}

func (g gatedRuns) Checkpoint(ctx context.Context, id tasks.RunID, name string) ([]byte, bool, error) {
	if name == ckptZero && g.parks(ctx, id) {
		g.reached <- id
		<-g.release
	}
	return g.RunStore.Checkpoint(ctx, id, name)
}

func (g gatedRuns) parks(ctx context.Context, id tasks.RunID) bool {
	if g.kind == "" {
		return true
	}
	r, err := g.RunStore.Get(ctx, id)
	return err == nil && r.Kind == g.kind
}

// releaser returns an idempotent close of a park's release channel. A test
// that parks defers it, so a failed assertion still frees the parked store
// call: otherwise the harness's Shutdown waits on the parked dispatcher
// forever and the failure surfaces as a package timeout, not a test failure.
func releaser(release chan struct{}) func() {
	var once sync.Once
	return func() { once.Do(func() { close(release) }) }
}

// TestCancel_ClaimedNotStartedTakesTheStop (5T.3.6, synctest; added for the
// cancel/claim race the parity kit found, kit cases
// CancelRacingAResumeThatBecameLive and CancelAtTheClaimStillCancelsTheRun):
// a cancel of a run whose claim committed but whose attempt has not started
// puts it in stopping, not canceled, because an attempt is about to go
// live; the attempt takes the stop as it goes live, its body never starts,
// and the run ends canceled.
func TestCancel_ClaimedNotStartedTakesTheStop(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		reached := make(chan tasks.RunID, 1)
		release := make(chan struct{})
		unpark := releaser(release)
		defer unpark()
		var ran atomic.Bool
		def := taskDef("gotham.interrogation", func(rc *tasks.RC, _ noParams) (string, error) {
			ran.Store(true)
			<-rc.Context().Done()
			return "", context.Cause(rc.Context())
		}, withCancel(tasks.Cooperative(time.Minute)))
		h := newHarness(t, []tasks.Definition{def}, func(o *Options) {
			synctestConfig(o)
			o.NewBatch = o.Stores.Runs.(batchOpener).NewBatch
			o.Stores.Runs = gatedRuns{RunStore: o.Stores.Runs, reached: reached, release: release}
		})
		id := h.start("gotham.interrogation", tasks.StartRequest{})
		<-reached
		if r := h.get(id); r.State != tasks.StateRunning {
			t.Fatalf("at the gate: %s, want running (the claim committed)", r.State)
		}
		if err := h.x.CancelAs(context.Background(), id, tasks.Actor{UserID: "gordon"}); err != nil {
			t.Fatal(err)
		}
		if r := h.get(id); r.State != tasks.StateStopping || r.Reason != reasonCancel {
			t.Fatalf("cancel of a claimed run not yet started: %s/%q, want stopping/cancel", r.State, r.Reason)
		}
		unpark()
		if r := h.waitState(id, tasks.StateCanceled, tasks.StateSucceeded, tasks.StateFailed); r.State != tasks.StateCanceled || r.Reason != reasonCancel {
			t.Fatalf("run ended %s/%q, want canceled/cancel", r.State, r.Reason)
		}
		synctest.Wait()
		if ran.Load() {
			t.Fatal("the body started after a cancel that committed before its attempt went live")
		}
	})
}

// TestShutdown_ReleasesClaimNotYetLive (5T.4 step 4, synctest; added for
// the shutdown invariants): a run whose claim committed but whose attempt
// is not live when Shutdown takes its view of the live attempts is released
// to the queue and its body never starts. Before the fix the claim checked
// for a shutdown before the attempt went live, not in the same decision, so
// an attempt that went live just after Shutdown's view ran on past
// Shutdown: never canceled, never stamped, its fence intact.
func TestShutdown_ReleasesClaimNotYetLive(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		reached := make(chan tasks.RunID, 1)
		release := make(chan struct{})
		unpark := releaser(release)
		defer unpark()
		var ran atomic.Bool
		def := taskDef("gotham.interrogation", func(rc *tasks.RC, _ noParams) (string, error) {
			ran.Store(true)
			<-rc.Context().Done()
			return "", context.Cause(rc.Context())
		})
		h := newHarness(t, []tasks.Definition{def}, func(o *Options) {
			synctestConfig(o)
			o.NewBatch = o.Stores.Runs.(batchOpener).NewBatch
			o.Stores.Runs = gatedRuns{RunStore: o.Stores.Runs, reached: reached, release: release}
		})
		id := h.start("gotham.interrogation", tasks.StartRequest{})
		<-reached
		done := make(chan error, 1)
		go func() { done <- h.x.Shutdown(context.Background()) }()
		synctest.Wait() // Shutdown has taken its view and waits for the parked dispatcher
		unpark()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if ran.Load() {
			t.Fatal("the body started after Shutdown took its view of the live attempts")
		}
		if r := h.get(id); r.State != tasks.StateQueued || r.Attempt != 0 {
			t.Fatalf("after Shutdown: %s attempt %d, want queued attempt 0 (a claim not yet started is released)", r.State, r.Attempt)
		}
	})
}

// TestStandDown_ClaimInFlightIsDeferred (5T.3.7, synctest; review of
// #4586): a run whose claim committed but whose attempt is not live when
// another run takes a stand-down on its key does not start beside the
// hold. It is released to the queue, deferred while the hold lives, and
// runs once the hold is gone. Before the fix the stand-down's victims were
// only live runs, so such a claim went live beside the hold.
func TestStandDown_ClaimInFlightIsDeferred(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		reached := make(chan tasks.RunID, 1)
		release := make(chan struct{})
		unpark := releaser(release)
		defer unpark()
		takeHold, holding, free := make(chan struct{}), make(chan struct{}), make(chan struct{})
		var ran atomic.Bool
		scan := taskDef("gotham.scan", func(rc *tasks.RC, _ noParams) (string, error) {
			ran.Store(true)
			return "case closed", nil
		}, func(c *tasks.Common) { c.Yields = true })
		holder := taskDef("gotham.compact", func(rc *tasks.RC, _ noParams) (string, error) {
			<-takeHold
			if _, err := rc.StandDown("gotham.scan"); err != nil {
				return "", err
			}
			close(holding)
			<-free
			return "compacted", nil
		}, func(c *tasks.Common) { c.Effects = tasks.Writes(tasks.Res("gotham", "lab")) })
		h := newHarness(t, []tasks.Definition{scan, holder}, func(o *Options) {
			synctestConfig(o)
			o.NewBatch = o.Stores.Runs.(batchOpener).NewBatch
			o.Stores.Runs = gatedRuns{RunStore: o.Stores.Runs, reached: reached, release: release, kind: "gotham.scan"}
		})
		hid := h.start("gotham.compact", tasks.StartRequest{})
		h.waitState(hid, tasks.StateRunning)
		id := h.start("gotham.scan", tasks.StartRequest{})
		<-reached
		close(takeHold)
		<-holding
		unpark()
		synctest.Wait()
		if ran.Load() {
			t.Fatal("the scan started beside a stand-down hold on its key")
		}
		if r := h.get(id); r.State != tasks.StateQueued || r.Attempt != 0 {
			t.Fatalf("while the hold lives: %s attempt %d, want queued attempt 0 (released, deferred)", r.State, r.Attempt)
		}
		close(free)
		if r := h.waitState(id, tasks.StateSucceeded, tasks.StateFailed); r.State != tasks.StateSucceeded || !ran.Load() {
			t.Fatalf("after the hold: %s (%q), ran %v; want succeeded", r.State, r.Error, ran.Load())
		}
	})
}

// TestStartAttempt_OvertakenClaimEndsByItsCommittedStop (5T.3.6, 5T.4 step
// 4, synctest; re-verify of #4586): a run is claimed, a cancel commits while
// its attempt is not live, and then a stand-down hold on its key is taken or
// Shutdown begins, so startAttempt releases the claim. The release must end
// the run by the cancel, without its body. Before the fix the release's edit
// wanted running while the record was stopping{cancel}: the release failed
// and the run sat in stopping with no attempt until the next boot, while a
// repeat Cancel answered nil. The last cell commits the cancel's record
// without handing the attempt its stop, the window between stop's move and
// its setting stopCause under e.mu, which is why the release (not a check of
// the attempt's pending stop) is where the fix lives.
func TestStartAttempt_OvertakenClaimEndsByItsCommittedStop(t *testing.T) {
	for _, c := range []struct {
		name      string
		cancel    func(h *harness, id tasks.RunID)
		overtaker string // "hold" or "shutdown"
	}{
		{name: "a stand-down hold on its key", overtaker: "hold", cancel: cancelAsGordon},
		{name: "shutdown", overtaker: "shutdown", cancel: cancelAsGordon},
		{name: "a cancel committed but not yet handed to the attempt", overtaker: "hold", cancel: func(h *harness, id tasks.RunID) {
			h.t.Helper()
			if _, err := h.e.move(context.Background(), id, func(cur tasks.RunRecord) (tasks.RunRecord, tasks.RunEvent, error) {
				next := cur
				next.State, next.Reason = tasks.StateStopping, reasonCancel
				return next, tasks.RunEvent{Event: "cancel", Reason: reasonCancel}, nil
			}, nil); err != nil {
				h.t.Fatal(err)
			}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				reached := make(chan tasks.RunID, 1)
				release := make(chan struct{})
				unpark := releaser(release)
				defer unpark()
				takeHold, holding, free := make(chan struct{}), make(chan struct{}), make(chan struct{})
				defer close(free)
				var ran atomic.Bool
				scan := taskDef("gotham.scan", func(rc *tasks.RC, _ noParams) (string, error) {
					ran.Store(true)
					<-rc.Context().Done()
					return "", context.Cause(rc.Context())
				}, func(c *tasks.Common) { c.Yields = true })
				holder := taskDef("gotham.compact", func(rc *tasks.RC, _ noParams) (string, error) {
					<-takeHold
					if _, err := rc.StandDown("gotham.scan"); err != nil {
						return "", err
					}
					close(holding)
					<-free
					return "compacted", nil
				}, func(c *tasks.Common) { c.Effects = tasks.Writes(tasks.Res("gotham", "lab")) })
				h := newHarness(t, []tasks.Definition{scan, holder}, func(o *Options) {
					synctestConfig(o)
					o.NewBatch = o.Stores.Runs.(batchOpener).NewBatch
					o.Stores.Runs = gatedRuns{RunStore: o.Stores.Runs, reached: reached, release: release, kind: "gotham.scan"}
				})
				if c.overtaker == "hold" {
					h.waitState(h.start("gotham.compact", tasks.StartRequest{}), tasks.StateRunning)
				}
				id := h.start("gotham.scan", tasks.StartRequest{})
				<-reached
				c.cancel(h, id)
				if r := h.get(id); r.State != tasks.StateStopping || r.Reason != reasonCancel {
					t.Fatalf("after the cancel: %s/%q, want stopping/cancel", r.State, r.Reason)
				}
				var shut chan error
				switch c.overtaker {
				case "hold":
					close(takeHold)
					<-holding
				case "shutdown":
					shut = make(chan error, 1)
					go func() { shut <- h.x.Shutdown(context.Background()) }()
					for !h.shutting() {
						time.Sleep(time.Millisecond)
					}
				}
				unpark()
				if shut != nil {
					if err := <-shut; err != nil {
						t.Fatalf("Shutdown: %v", err)
					}
				}
				synctest.Wait()
				if r := h.get(id); r.State != tasks.StateCanceled || r.Reason != reasonCancel {
					t.Fatalf("once the attempt was overtaken: %s/%q, want canceled/cancel at once (not stopping until the next boot)", r.State, r.Reason)
				}
				if ran.Load() {
					t.Fatal("the body started although a cancel had committed")
				}
				if c.overtaker == "hold" {
					if err := h.x.CancelAs(context.Background(), id, tasks.Actor{UserID: "gordon"}); !isErr(err, tasks.ErrRunNotFound) {
						t.Fatalf("a repeat cancel: %v, want ErrRunNotFound (the run is terminal)", err)
					}
				}
			})
		})
	}
}

// cancelAsGordon cancels id through the executor's own cancel path.
func cancelAsGordon(h *harness, id tasks.RunID) {
	h.t.Helper()
	if err := h.x.CancelAs(context.Background(), id, tasks.Actor{UserID: "gordon"}); err != nil {
		h.t.Fatal(err)
	}
}

// shutting reports whether Shutdown has begun.
func (h *harness) shutting() bool {
	h.e.mu.Lock()
	defer h.e.mu.Unlock()
	return h.e.shutting
}

// TestLedger_StaleAttemptWritesNothing (5T.7, synctest; review of #4586):
// a second executor over the same store (the first one's process presumed
// dead) resumes a run the first is still executing. The first executor's
// attempt does not know it was superseded, so its in-memory fence is
// intact; its late checkpoint is refused (ErrFenced) by the ledger's check
// of the stored attempt and fence, and the second attempt restores the
// value written before the takeover, not the stale one.
func TestLedger_StaleAttemptWritesNothing(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		started := make(chan int32, 2)
		poke := make(chan chan error)
		check := make(chan struct{})
		restored := make(chan int, 1)
		var calls atomic.Int32
		def := taskDef("gotham.patrol", func(rc *tasks.RC, _ noParams) (string, error) {
			n := calls.Add(1)
			if n > 1 {
				started <- n
				<-check
				var v int
				_, err := rc.Restore(&v)
				restored <- v
				return "second attempt", err
			}
			if err := rc.Checkpoint(1); err != nil {
				return "", err
			}
			started <- n
			reply := <-poke
			time.Sleep(time.Second) // past min_checkpoint_interval, so the checkpoint flushes now
			reply <- rc.Checkpoint(99)
			<-rc.Context().Done()
			return "", context.Cause(rc.Context())
		})
		defs := []tasks.Definition{def}
		cfg := func(o *Options) {
			o.Config = Config{DispatchTick: time.Second, WatchdogInterval: time.Hour, MinCheckpointInterval: time.Nanosecond}
		}
		h := newHarness(t, defs, cfg)
		id := h.start("gotham.patrol", tasks.StartRequest{})
		<-started
		next := onStores(t, h.fs, h.stores, h.ks, defs, cfg, true)
		<-started
		if r := next.get(id); r.Attempt != 2 || r.State != tasks.StateRunning {
			t.Fatalf("after the takeover: %s attempt %d, want running attempt 2", r.State, r.Attempt)
		}
		reply := make(chan error, 1)
		poke <- reply
		if err := <-reply; !isErr(err, tasks.ErrFenced) {
			t.Fatalf("the superseded attempt's checkpoint: %v, want ErrFenced", err)
		}
		close(check)
		if v := <-restored; v != 1 {
			t.Fatalf("the second attempt restored %d, want 1 (the superseded attempt's 99 must not land)", v)
		}
		next.waitState(id, tasks.StateSucceeded)
	})
}

// TestResume_UnreadableZeroMarkerVoidsCheckpoints (5T.3.6; added for
// zeroFrom): a resumed attempt restores the checkpoint an earlier attempt
// wrote, unless the run's from-zero marker cannot be decoded. Such a marker
// voids every checkpoint before the attempt it is read for, so the attempt
// starts from nothing (and is not Resumed) rather than from state a
// ResumeFromZero requeue may have voided.
func TestResume_UnreadableZeroMarkerVoidsCheckpoints(t *testing.T) {
	type seen struct {
		resumed, found bool
		n              int
	}
	for _, c := range []struct {
		name   string
		marker []byte
		want   seen
	}{
		{name: "no marker", want: seen{resumed: true, found: true, n: 3}},
		{name: "undecodable marker", marker: []byte("the Riddler's cipher"), want: seen{}},
	} {
		t.Run(c.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				started := make(chan tasks.RunID, 1)
				second := make(chan seen, 1)
				var calls atomic.Int32
				def := taskDef("gotham.patrol", func(rc *tasks.RC, _ noParams) (string, error) {
					if calls.Add(1) > 1 {
						var got seen
						found, err := rc.Restore(&got.n)
						if err != nil {
							return "", err
						}
						got.resumed, got.found = rc.Resumed(), found
						second <- got
						return "second attempt", nil
					}
					if err := rc.Checkpoint(3); err != nil {
						return "", err
					}
					started <- rc.RunID()
					<-rc.Context().Done()
					return "", context.Cause(rc.Context())
				})
				defs := []tasks.Definition{def}
				cfg := func(o *Options) {
					o.Config = Config{DispatchTick: time.Second, WatchdogInterval: time.Hour, MinCheckpointInterval: time.Nanosecond}
				}
				h := newHarness(t, defs, cfg)
				id := h.start("gotham.patrol", tasks.StartRequest{})
				<-started
				if c.marker != nil {
					if err := h.e.write(context.Background(), func(ctx context.Context, b *storage.Batch) error {
						return h.stores.Runs.SaveCheckpoint(ctx, b, id, ckptZero, c.marker)
					}); err != nil {
						t.Fatalf("plant the marker: %v", err)
					}
				}
				next := h.crashBoot(defs, cfg)
				if got := <-second; got != c.want {
					t.Fatalf("second attempt saw %+v, want %+v", got, c.want)
				}
				next.waitState(id, tasks.StateSucceeded)
			})
		})
	}
}

// TestUnknownKindRun_DroppedAtResume (5T.3.6, D10): at boot, an unfinished
// run of a kind no definition registers (and no manifest holds) is
// interrupted_dropped with reason unknown_kind; boot succeeds and the
// known kinds resume.
func TestUnknownKindRun_DroppedAtResume(t *testing.T) {
	started := make(chan tasks.RunID, 2)
	lab := func(c *tasks.Common) { c.Effects = tasks.Writes(tasks.Res("gotham", "lab")) } // disjoint from gotham.retired's writes
	defs := []tasks.Definition{taskDef("gotham.retired", blockUntilCtx(started)), taskDef("gotham.patrol", blockUntilCtx(started), lab)}
	h := newHarness(t, defs, nil)
	running := h.start("gotham.retired", tasks.StartRequest{})
	<-started
	known := h.start("gotham.patrol", tasks.StartRequest{})
	<-started
	if err := h.x.Pause(context.Background(), "inventory"); err != nil {
		t.Fatal(err)
	}
	queued := h.start("gotham.retired", tasks.StartRequest{Params: mustParams(t, "second")})
	next := h.crashBoot([]tasks.Definition{taskDef("gotham.patrol", okBody, lab)}, nil)
	for _, id := range []tasks.RunID{running, queued} {
		if r := next.waitState(id, tasks.StateInterruptedDropped); r.Reason != reasonUnknownKind {
			t.Fatalf("run %s of the unknown kind: reason %q, want %q", id, r.Reason, reasonUnknownKind)
		}
	}
	if err := next.x.Unpause(context.Background()); err != nil {
		t.Fatal(err)
	}
	next.waitState(known, tasks.StateSucceeded)
}

// TestOnTerminal_NotificationStagedInTerminalBatch (5T.4 step 3, COR2-3):
// OnTerminal stages its write in the terminal transition's own batch, so
// both commit together. When the hook fails, nothing it staged commits, but
// the run still reaches its terminal state, with a notify_failed event, an
// error line in its log and an error in the executor's log.
func TestOnTerminal_NotificationStagedInTerminalBatch(t *testing.T) {
	subject := entity.Ref{Module: "gotham", Kind: "cases", ID: "ace"}
	for _, fail := range []bool{false, true} {
		name := map[bool]string{false: "commits with the transition", true: "an error still ends the run"}[fail]
		t.Run(name, func(t *testing.T) {
			var h *harness
			var calls int
			var mu sync.Mutex
			logs := &lockedBuffer{}
			h = newHarness(t, []tasks.Definition{taskDef("gotham.closecase", okBody)}, func(o *Options) {
				o.Logger = slog.New(slog.NewTextHandler(logs, nil))
				o.OnTerminal = func(ctx context.Context, b *storage.Batch, v tasks.RunView) error {
					mu.Lock()
					calls++
					mu.Unlock()
					if !v.State.Terminal() {
						return errors.New("OnTerminal called for a non-terminal state")
					}
					if err := o.Stores.Runs.Stamp(ctx, b, subject, v.Kind, time.Unix(1_000_000, 0)); err != nil {
						return err
					}
					if fail {
						return errors.New("the signal is down")
					}
					return nil
				}
			})
			id := h.start("gotham.closecase", tasks.StartRequest{})
			r := h.waitState(id, tasks.StateSucceeded)
			_, stamped, err := h.stores.Runs.Stamped(context.Background(), subject, "gotham.closecase")
			if err != nil {
				t.Fatal(err)
			}
			events := h.eventsOf(id)
			if !fail {
				if !stamped {
					t.Fatal("run succeeded without the stamp OnTerminal staged in its batch")
				}
				if slices.Contains(events, eventNotifyFailed) {
					t.Fatalf("events %v record a notification failure that did not happen", events)
				}
				return
			}
			if stamped {
				t.Fatal("OnTerminal failed but the write it staged committed")
			}
			mu.Lock()
			n := calls
			mu.Unlock()
			if n != 1 {
				t.Fatalf("OnTerminal called %d times, want once (the second staging skips it)", n)
			}
			if events[len(events)-1] != eventNotifyFailed || r.Seq.Event != uint64(len(events)) {
				t.Fatalf("events %v (seq %d), want notify_failed last, after the terminal transition", events, r.Seq.Event)
			}
			var line string
			for l, err := range h.x.Log(context.Background(), id, tasks.PageQuery{}) {
				if err != nil {
					t.Fatal(err)
				}
				if l.Level == "error" {
					line = l.Message
				}
			}
			if !strings.Contains(line, "the signal is down") {
				t.Fatalf("run log error line %q, want the hook's error", line)
			}
			if !strings.Contains(logs.String(), "the signal is down") {
				t.Fatalf("executor log %q does not name the hook's error", logs.String())
			}
		})
	}
}

// eventsOf lists a run's event names in order.
func (h *harness) eventsOf(id tasks.RunID) []string {
	h.t.Helper()
	var out []string
	for p, err := range h.stores.Runs.Pages(context.Background(), id, tasks.PageQuery{Page: tasks.PageEvents, Limit: 1000}) {
		if err != nil {
			h.t.Fatalf("events: %v", err)
		}
		out = append(out, p.Event.Event)
	}
	return out
}

// lockedBuffer is an io.Writer safe for a logger's concurrent writes.
type lockedBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// TestModuleSystemActor_CannotStartUndeclaredKind (SEC2-15, F4-09): a
// module's system actor may start only its own kinds and the kinds its
// manifest lists in TaskStarts, and never one that needs core:admin or an
// AuthRequired scope.
func TestModuleSystemActor_CannotStartUndeclaredKind(t *testing.T) {
	gothamSystem := tasks.Actor{AuthMethod: systemMethod, System: "gotham"}
	purge := func(c *tasks.Common) { c.Perms.Start = rbac.Need("core:system.manage") } // AuthRequired
	defs := []tasks.Definition{
		taskDef("gotham.dispatch", okBody),
		taskDef("arkham.cleanup", okBody),
		taskDef("arkham.purge", okBody, purge),
		taskDef("arkham.transfer", okBody),
	}
	cells := []struct {
		kind    tasks.Kind
		starts  []tasks.Kind
		allowed bool
	}{
		{kind: "gotham.dispatch", allowed: true},
		{kind: "arkham.cleanup"},
		{kind: "arkham.cleanup", starts: []tasks.Kind{"arkham.cleanup"}, allowed: true},
		{kind: "arkham.transfer", starts: []tasks.Kind{"arkham.cleanup"}},
		{kind: "arkham.purge", starts: []tasks.Kind{"arkham.purge"}},
	}
	for _, c := range cells {
		t.Run(string(c.kind)+"/"+strings.Join(kindStrings(c.starts), ","), func(t *testing.T) {
			h := newHarness(t, defs, func(o *Options) {
				o.Catalog = catalogWith(t, "gotham:run", "arkham:run")
				o.Manifests = []Manifest{{Module: "gotham", Enabled: true, TaskStarts: c.starts}}
			})
			_, err := h.x.Start(context.Background(), c.kind, tasks.StartRequest{Mode: tasks.ModeRequestLive, Actor: gothamSystem})
			if c.allowed && err != nil {
				t.Fatalf("gotham's system actor starting %s: %v, want allowed", c.kind, err)
			}
			if !c.allowed && !errors.Is(err, ErrNotPermitted) {
				t.Fatalf("gotham's system actor starting %s: %v, want ErrNotPermitted", c.kind, err)
			}
		})
	}
}

func kindStrings(ks []tasks.Kind) []string {
	out := make([]string, len(ks))
	for i, k := range ks {
		out[i] = string(k)
	}
	return out
}

// children lists the runs whose parent is id.
func (h *harness) children(id tasks.RunID) []tasks.RunID {
	h.t.Helper()
	var out []tasks.RunID
	for v, err := range h.x.Runs(context.Background(), tasks.RunQuery{Parent: id}) {
		if err != nil {
			h.t.Fatalf("children of %s: %v", id, err)
		}
		out = append(out, v.ID)
	}
	return out
}

// enqueueOnce boots defs plus gotham.dispatch, whose body makes one
// rc.Enqueue of kind with opts and hands back its error, starts the parent
// as Gordon (who holds gotham:run and nothing else) and returns the
// harness, the parent's ID and the enqueue's error once the parent ended.
func enqueueOnce(t *testing.T, defs []tasks.Definition, starts []tasks.Kind, kind tasks.Kind, opts ...tasks.StartOption) (*harness, tasks.RunID, error) {
	t.Helper()
	got := make(chan error, 1)
	parent := taskDef("gotham.dispatch", func(rc *tasks.RC, _ noParams) (string, error) {
		_, err := rc.Enqueue(kind, noParams{}, opts...)
		got <- err
		return "", nil
	})
	h := newHarness(t, append([]tasks.Definition{parent}, defs...), func(o *Options) {
		withPrincipal(o)
		o.Catalog = catalogWith(t, "gotham:run", "arkham:run")
		o.Manifests = []Manifest{{Module: "gotham", Enabled: true, TaskStarts: starts}}
	})
	ref, err := h.x.Start(asUser("gordon", "gotham:run"), "gotham.dispatch", tasks.StartRequest{Mode: tasks.ModeRequestLive})
	if err != nil {
		t.Fatalf("start the parent: %v", err)
	}
	err = <-got
	h.waitState(ref.ID, tasks.StateSucceeded)
	return h, ref.ID, err
}

var gordon = tasks.Actor{UserID: "gordon", AuthMethod: "session"}

// TestEnqueue_ChildBoundedByItsModule (SEC2-15, COR-21): a child start is
// module code, so it is bounded by the reach of the module whose body makes
// it (its own kinds plus its manifest's TaskStarts, never a kind needing
// core:admin or an AuthRequired scope), whoever started the parent. The
// parent here is a user's, the start that once ran no check at all: no
// request principal reaches a body. The user's own grants are not what
// bounds it either way: Gordon holds no arkham scope, yet a listed arkham
// kind is enqueued. A refused enqueue creates no run.
func TestEnqueue_ChildBoundedByItsModule(t *testing.T) {
	purge := func(c *tasks.Common) { c.Perms.Start = rbac.Need("core:system.manage") } // AuthRequired
	defs := []tasks.Definition{
		taskDef("gotham.forensics", okBody),
		taskDef("arkham.cleanup", okBody),
		taskDef("arkham.purge", okBody, purge),
		taskDef("arkham.transfer", okBody),
	}
	cells := []struct {
		kind    tasks.Kind
		starts  []tasks.Kind
		allowed bool
	}{
		{kind: "gotham.forensics", allowed: true},
		{kind: "arkham.cleanup"},
		{kind: "arkham.cleanup", starts: []tasks.Kind{"arkham.cleanup"}, allowed: true},
		{kind: "arkham.transfer", starts: []tasks.Kind{"arkham.cleanup"}},
		{kind: "arkham.purge", starts: []tasks.Kind{"arkham.purge"}},
	}
	for _, c := range cells {
		t.Run(string(c.kind)+"/"+strings.Join(kindStrings(c.starts), ","), func(t *testing.T) {
			h, pid, err := enqueueOnce(t, defs, c.starts, c.kind)
			kids := h.children(pid)
			if !c.allowed {
				if !errors.Is(err, ErrNotPermitted) {
					t.Fatalf("enqueue %s: %v, want ErrNotPermitted", c.kind, err)
				}
				if len(kids) != 0 {
					t.Fatalf("a refused enqueue created runs %v", kids)
				}
				return
			}
			if err != nil {
				t.Fatalf("enqueue %s: %v, want allowed", c.kind, err)
			}
			if len(kids) != 1 {
				t.Fatalf("children %v, want one", kids)
			}
			if r := h.get(kids[0]); r.Kind != c.kind || r.Actor != gordon || r.Trigger != tasks.TriggerParent {
				t.Fatalf("child %s actor %+v trigger %v, want %s as %+v by parent", r.Kind, r.Actor, r.Trigger, c.kind, gordon)
			}
		})
	}
}

// TestEnqueue_OptionCannotRestampTheChild (COR-21): a StartOption adjusts a
// child's mode, params or priority. One that sets the child's actor, parent,
// trigger or idempotency key is refused and creates no run, so a body
// cannot start a child as another user or as a system actor (whose reach is
// another rule), unlinked from its parent, or folded into another run.
func TestEnqueue_OptionCannotRestampTheChild(t *testing.T) {
	defs := []tasks.Definition{taskDef("gotham.forensics", okBody)}
	cells := []struct {
		name    string
		opt     tasks.StartOption
		allowed bool
	}{
		{name: "priority", opt: func(r *tasks.StartRequest) { r.Priority = tasks.PriorityHigh }, allowed: true},
		{name: "another user", opt: func(r *tasks.StartRequest) { r.Actor = tasks.Actor{UserID: "joker", AuthMethod: "session"} }},
		{name: "a system actor", opt: func(r *tasks.StartRequest) { r.Actor = tasks.Actor{AuthMethod: systemMethod, System: "gotham"} }},
		{name: "no parent", opt: func(r *tasks.StartRequest) { r.Parent = "" }},
		{name: "manual trigger", opt: func(r *tasks.StartRequest) { r.Trigger = tasks.TriggerManual }},
		{name: "idempotency key", opt: func(r *tasks.StartRequest) { r.IdempotencyKey = "bat-signal" }},
	}
	for _, c := range cells {
		t.Run(c.name, func(t *testing.T) {
			h, pid, err := enqueueOnce(t, defs, nil, "gotham.forensics", c.opt)
			if !c.allowed {
				if !errors.Is(err, ErrNotPermitted) {
					t.Fatalf("enqueue with %s: %v, want ErrNotPermitted", c.name, err)
				}
				var n int
				for _, err := range h.x.Runs(context.Background(), tasks.RunQuery{Kinds: []tasks.Kind{"gotham.forensics"}}) {
					if err != nil {
						t.Fatal(err)
					}
					n++
				}
				if n != 0 {
					t.Fatalf("a refused enqueue created %d runs", n)
				}
				return
			}
			if err != nil {
				t.Fatalf("enqueue with %s: %v, want allowed", c.name, err)
			}
			kids := h.children(pid)
			if len(kids) != 1 {
				t.Fatalf("children %v, want one", kids)
			}
			if r := h.get(kids[0]); r.Priority != tasks.PriorityHigh || r.Actor != gordon {
				t.Fatalf("child priority %v actor %+v, want high as %+v", r.Priority, r.Actor, gordon)
			}
		})
	}
}
