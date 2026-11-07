// file: core/kernel/supervisor_test.go
// version: 1.0.2
// guid: e9d797ab-e187-456c-b23a-12fae047e7b9
// last-edited: 2026-11-07

package kernel

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/falkcorp/mediacore/coretest"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/module"
)

// recorder is the ordered event log of one supervised run.
type recorder struct {
	mu     sync.Mutex
	events []string
}

func (r *recorder) add(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, fmt.Sprintf(format, args...))
}

func (r *recorder) list() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.events)
}

// index is the position of event e, -1 if absent.
func (r *recorder) index(e string) int { return slices.Index(r.list(), e) }

// matching is the events with prefix, in order.
func (r *recorder) matching(prefix string) []string {
	return slices.DeleteFunc(r.list(), func(e string) bool { return !strings.HasPrefix(e, prefix) })
}

// notifier records the readiness signals.
type notifier struct{ rec *recorder }

func (n notifier) Ready() error          { n.rec.add("ready"); return nil }
func (n notifier) Status(s string) error { n.rec.add("status:%s", s); return nil }
func (n notifier) Stopping() error       { n.rec.add("stopping"); return nil }

// fakeServer stands in for the listener inside a synctest bubble, where a
// real socket would never be durably blocked.
type fakeServer struct {
	rec     *recorder
	stopped chan struct{}
	once    sync.Once
}

func (s *fakeServer) Addr() net.Addr { return &net.TCPAddr{Port: 8484} }
func (s *fakeServer) Serve() error   { <-s.stopped; return nil }
func (s *fakeServer) Shutdown(context.Context) error {
	s.rec.add("listener:shutdown")
	s.once.Do(func() { close(s.stopped) })
	return nil
}

// supervised is a Config whose listener, notifier, R1Values and exit record
// into rec.
func supervised(t *testing.T, rec *recorder, mods ...module.Module) Config {
	c := testConfig(t, mods...)
	c.Notifier = notifier{rec}
	c.Listen = func(o httpapi.ListenOptions, _ http.Handler, _ *slog.Logger) (Server, error) {
		rec.add("listen:%s", o.Addr)
		return &fakeServer{rec: rec, stopped: make(chan struct{})}, nil
	}
	c.R1Values = func() R1Values { rec.add("r1values"); return R1Values{Listen: "localhost:0"} }
	c.Exit = func(code int) { rec.add("exit:%d", code) }
	return c
}

// hooked is a module with two recording hooks and a recording worker.
func hooked(id string, rec *recorder) testModule {
	return mod(id, func(r *module.Registrar) error {
		for _, name := range []string{"cowl", "cape"} {
			full := id + "." + name
			r.Hook(module.Hook{
				Name:  full,
				Start: func(context.Context) error { rec.add("start:%s", full); return nil },
				Stop:  func(context.Context) error { rec.add("stop:%s", full); return nil },
			})
		}
		r.Worker(module.Worker{Name: "patrol", Policy: module.DegradeOnExit, Run: func(ctx context.Context) error {
			rec.add("worker:%s", id)
			<-ctx.Done()
			rec.add("worker-exit:%s", id)
			return ctx.Err()
		}})
		return nil
	})
}

// run starts k.Run in the bubble and returns its result channel.
func run(ctx context.Context, k *Kernel) <-chan error {
	done := make(chan error, 1)
	go func() { done <- k.Run(ctx) }()
	return done
}

func TestStartOrder(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rec := &recorder{}
		k := mustNew(t, supervised(t, rec, hooked("bane", rec), hooked("alfred", rec)))
		ctx, cancel := context.WithCancel(t.Context())
		done := run(ctx, k)
		synctest.Wait()

		wantStarts := []string{"start:alfred.cowl", "start:alfred.cape", "start:bane.cowl", "start:bane.cape"}
		if got := rec.matching("start:"); !slices.Equal(got, wantStarts) {
			t.Fatalf("hook starts = %v, want module ID order, then registration order: %v", got, wantStarts)
		}
		lastStart := rec.index("start:bane.cape")
		for _, w := range []string{"worker:alfred", "worker:bane"} {
			if i := rec.index(w); i < lastStart {
				t.Errorf("%s at %d ran before the last hook start (%d): a module callback before its Start hook", w, i, lastStart)
			}
		}
		order := []string{"status:starting", "start:alfred.cowl", "r1values", "listen:localhost:0", "ready"}
		for i := 1; i < len(order); i++ {
			if rec.index(order[i-1]) >= rec.index(order[i]) {
				t.Fatalf("%s is not before %s: %v", order[i-1], order[i], rec.list())
			}
		}
		if rec.index("r1values") < lastStart {
			t.Fatal("R1Values was called before the module start hooks (05-config §1.12.1)")
		}
		cancel()
		if err := <-done; err != nil {
			t.Fatalf("Run after ctx cancel = %v, want nil", err)
		}
	})
}

func TestStopOrder(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rec := &recorder{}
		k := mustNew(t, supervised(t, rec, hooked("alfred", rec), hooked("bane", rec)))
		ctx, cancel := context.WithCancel(t.Context())
		done := run(ctx, k)
		synctest.Wait()
		cancel()
		if err := <-done; err != nil {
			t.Fatalf("Run = %v, want nil", err)
		}
		wantStops := []string{"stop:bane.cape", "stop:bane.cowl", "stop:alfred.cape", "stop:alfred.cowl"}
		if got := rec.matching("stop:"); !slices.Equal(got, wantStops) {
			t.Fatalf("hook stops = %v, want exact reverse start order %v", got, wantStops)
		}
		firstStop := rec.index("stop:bane.cape")
		if rec.index("stopping") > rec.index("listener:shutdown") {
			t.Fatal("STOPPING=1 came after the listener shutdown")
		}
		for _, w := range []string{"worker-exit:alfred", "worker-exit:bane"} {
			if i := rec.index(w); i < rec.index("listener:shutdown") || i > firstStop {
				t.Errorf("%s at %d: want after the listener drain and before the first Stop hook (%d): %v", w, i, firstStop, rec.list())
			}
		}
	})
}

func TestStartRollbackReverseOrder(t *testing.T) {
	failed := errors.New("Bane broke the cowl")
	never := time.Duration(-1) // release only after Run returned
	for _, tc := range []struct {
		name    string
		start   func(ctx context.Context, release <-chan struct{}) error
		release time.Duration // when the test releases a Start that ignores its ctx
		want    error
		took    time.Duration // 0 = not checked
		stops   []string      // every Stop called, once the late Start is released
	}{
		{name: "error", start: func(context.Context, <-chan struct{}) error { return failed }, want: failed,
			stops: []string{"stop:alfred.cape", "stop:alfred.cowl"}},
		{name: "panic", start: func(context.Context, <-chan struct{}) error { panic("venom overdose") },
			stops: []string{"stop:alfred.cape", "stop:alfred.cowl"}},
		{name: "timeout honouring ctx", start: func(ctx context.Context, _ <-chan struct{}) error { <-ctx.Done(); return ctx.Err() },
			want: context.DeadlineExceeded, took: startTimeout, stops: []string{"stop:alfred.cape", "stop:alfred.cowl"}},
		// The review's m1: a Start abandoned at the timeout that returns nil
		// later has started its hook, so the hook is stopped, first.
		{name: "timeout ignoring ctx, starts late", start: func(_ context.Context, release <-chan struct{}) error { <-release; return nil },
			release: startTimeout + time.Second, want: context.DeadlineExceeded, took: startTimeout + time.Second,
			stops: []string{"stop:bane.venom", "stop:alfred.cape", "stop:alfred.cowl"}},
		// Still running when the rollback's budget runs out: like a Stop that
		// holds the budget, it starves the rest, whose Stops are then called
		// unawaited; its own Stop runs in the background if it ever starts.
		{name: "timeout ignoring ctx, never starts in time", start: func(_ context.Context, release <-chan struct{}) error { <-release; return nil },
			release: never, want: context.DeadlineExceeded, took: startTimeout + defaultShutdownBudget,
			stops: []string{"stop:alfred.cape", "stop:alfred.cowl", "stop:bane.venom"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				rec := &recorder{}
				release := make(chan struct{})
				bane := mod("bane", func(r *module.Registrar) error {
					r.Hook(module.Hook{Name: "bane.venom",
						Start: func(ctx context.Context) error { rec.add("start:bane.venom"); return tc.start(ctx, release) },
						Stop:  func(context.Context) error { rec.add("stop:bane.venom"); return nil }})
					return nil
				})
				k := mustNew(t, supervised(t, rec, hooked("alfred", rec), bane))
				var once sync.Once
				free := func() { once.Do(func() { close(release) }) }
				defer free() // a failed check must not leave the Start blocked in the bubble
				if tc.release > 0 {
					time.AfterFunc(tc.release, free)
				}
				begin := time.Now()
				err := k.Run(t.Context())
				took := time.Since(begin)
				if err == nil || (tc.want != nil && !errors.Is(err, tc.want)) || !strings.Contains(err.Error(), "bane.venom") {
					t.Fatalf("Run = %v, want an error naming bane.venom (%v)", err, tc.want)
				}
				if tc.took != 0 && took != tc.took {
					t.Fatalf("Run returned after %s, want %s", took, tc.took)
				}
				// The Stops called unawaited after the budget run now; a Start
				// still running can only be stopped once it returns, so its
				// Stop comes last.
				synctest.Wait()
				free()
				synctest.Wait()
				if got := rec.matching("stop:"); !slices.Equal(got, tc.stops) {
					t.Fatalf("rollback stops = %v, want %v", got, tc.stops)
				}
				for _, never := range []string{"worker:alfred", "r1values", "ready"} {
					if rec.index(never) >= 0 {
						t.Errorf("%s happened after a failed start", never)
					}
				}
			})
		})
	}
}

func TestStopTimeoutAbandons(t *testing.T) {
	for _, tc := range []struct {
		name   string
		budget time.Duration
		want   time.Duration
	}{
		{"default budget", 0, defaultShutdownBudget},
		{"R1Values budget", 5 * time.Second, 5 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				rec := &recorder{}
				release := make(chan struct{})
				capture := coretest.NewLogCapture(t)
				joker := mod("joker", func(r *module.Registrar) error {
					r.Hook(module.Hook{Name: "joker.laugh", Stop: func(context.Context) error {
						rec.add("stop:joker.laugh")
						<-release // ignores its ctx: outlives the budget
						return nil
					}})
					return nil
				})
				c := supervised(t, rec, hooked("alfred", rec), joker)
				c.Logger = capture.Logger()
				c.R1Values = func() R1Values { return R1Values{ShutdownBudget: tc.budget} }
				k := mustNew(t, c)
				ctx, cancel := context.WithCancel(t.Context())
				done := run(ctx, k)
				synctest.Wait()
				begin := time.Now()
				cancel()
				err := <-done
				took := time.Since(begin)
				close(release)
				synctest.Wait() // the hooks reached after the budget are stopped unawaited
				if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "joker.laugh") {
					t.Fatalf("Run = %v, want the abandoned joker.laugh", err)
				}
				// The stop order continued past the abandoned hook, in order.
				if got, want := rec.matching("stop:"), []string{"stop:joker.laugh", "stop:alfred.cape", "stop:alfred.cowl"}; !slices.Equal(got, want) {
					t.Fatalf("stops = %v, want %v", got, want)
				}
				// The joker hook held the budget; the rest add nothing.
				if took < tc.want || took > tc.want+lateStopFloor {
					t.Fatalf("stop took %s, want the budget %s", took, tc.want)
				}
				if !slices.ContainsFunc(capture.Records(), func(r slog.Record) bool {
					return r.Level == slog.LevelError && strings.Contains(r.Message, "hook stop abandoned")
				}) {
					t.Fatalf("no ERROR naming the abandoned hook: %v", capture.Messages())
				}
			})
		})
	}
}

func TestWorkerPolicies(t *testing.T) {
	escaped := errors.New("Joker escaped Arkham")
	worker := func(policy module.WorkerPolicy, run func(context.Context) error) testModule {
		return mod("arkham", func(r *module.Registrar) error {
			r.Hook(module.Hook{Name: "arkham.gate", Stop: func(context.Context) error { return nil }})
			r.Worker(module.Worker{Name: "inmate", Policy: policy, Run: run})
			return nil
		})
	}
	for _, tc := range []struct {
		name string
		mod  testModule
		want string // "" = the app keeps serving and stops cleanly
	}{
		{"degrade on error", worker(module.DegradeOnExit, func(context.Context) error { return escaped }), ""},
		{"degrade on panic", worker(module.DegradeOnExit, func(context.Context) error { panic("Harley") }), ""},
		{"degrade on nil", worker(module.DegradeOnExit, func(context.Context) error { return nil }), ""},
		{"stop app on error", worker(module.StopAppOnExit, func(context.Context) error { return escaped }), escaped.Error()},
		{"stop app on panic", worker(module.StopAppOnExit, func(context.Context) error { panic("Harley") }), "panic: Harley"},
		{"stop app on nil", worker(module.StopAppOnExit, func(context.Context) error { return nil }), "exited early"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				rec := &recorder{}
				k := mustNew(t, supervised(t, rec, tc.mod))
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				done := run(ctx, k)
				synctest.Wait()
				if tc.want == "" {
					select {
					case err := <-done:
						t.Fatalf("Run returned %v after a DegradeOnExit worker exit; the app must keep serving", err)
					default:
					}
					cancel()
					if err := <-done; err != nil {
						t.Fatalf("Run = %v, want nil", err)
					}
					return
				}
				err := <-done // no cancel: the worker's exit stops the app
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("Run = %v, want the worker's exit (%q)", err, tc.want)
				}
				if tc.want == escaped.Error() && !errors.Is(err, escaped) {
					t.Fatalf("Run = %v does not wrap the worker's error", err)
				}
				if rec.index("listener:shutdown") < 0 {
					t.Fatal("StopAppOnExit did not run the stop order")
				}
			})
		})
	}
}

func TestRestartRequest_ExitsRestartCode(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rec := &recorder{}
		k := mustNew(t, supervised(t, rec, hooked("alfred", rec)))
		done := run(t.Context(), k)
		synctest.Wait()
		k.lifecycle.RequestRestart("Bruce staged a restore")
		k.lifecycle.RequestRestart("a second request is a no-op")
		err := <-done
		if !errors.Is(err, errRestartRequested) || !strings.Contains(err.Error(), "Bruce staged a restore") {
			t.Fatalf("Run = %v, want errRestartRequested with the first reason", err)
		}
		ev := rec.list()
		if got := rec.matching("exit:"); !slices.Equal(got, []string{"exit:75"}) {
			t.Fatalf("exits = %v, want exactly one exit(75), never exit(0)", got)
		}
		if ev[len(ev)-1] != "exit:75" || rec.index("stop:alfred.cowl") > rec.index("exit:75") {
			t.Fatalf("exit(75) is not after the stop order: %v", ev)
		}
	})
}

func TestNewCleansUpCoreGoroutines(t *testing.T) {
	// synctest.Test fails if any goroutine started in the bubble is still
	// alive when the function returns: a boot failing in P7 must leave none.
	synctest.Test(t, func(t *testing.T) {
		spa := func(r *module.Registrar) error {
			r.MountLegacy(httpapi.LegacyMount{Handler: http.NotFoundHandler(), Auth: httpapi.LegacyAuthCore, ServesSPA: true})
			return nil
		}
		k, err := New(t.Context(), testConfig(t, mod("batcave", spa), mod("arkham", spa)))
		if k != nil || !hasKind(err, module.ProblemSPAOwner) {
			t.Fatalf("New = %v, %v; want a P7 ProblemSPAOwner", k, err)
		}
	})
	synctest.Test(t, func(t *testing.T) {
		// The same with the kernel's own log.Logging, not a capture.
		c := Config{AppID: "gotham", LogOutput: io.Discard, Modules: []module.Module{mod("batcave", func(r *module.Registrar) error {
			r.MountLegacy(httpapi.LegacyMount{Handler: http.NotFoundHandler(), Auth: httpapi.LegacyAuthCore, TopSegments: []string{"arkham"}})
			return nil
		})}}
		if _, err := New(t.Context(), c); !hasKind(err, module.ProblemRouteOutsidePrefix) {
			t.Fatalf("New = %v, want a P7 ProblemRouteOutsidePrefix", err)
		}
	})
}

func TestRunTwice(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rec := &recorder{}
		k := mustNew(t, supervised(t, rec))
		ctx, cancel := context.WithCancel(t.Context())
		done := run(ctx, k)
		synctest.Wait()
		if err := k.Run(ctx); !errors.Is(err, errRunTwice) {
			t.Fatalf("second Run = %v, want errRunTwice", err)
		}
		cancel()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	})
}

// TestStopBudget_LateHooksNotAwaited is the review's M1: with the budget
// spent by the first Stop, the hooks after it get no time of their own (a
// floor each would overrun the budget by one floor per hook). Their Stops
// are still called, in reverse order, with an expired ctx.
func TestStopBudget_LateHooksNotAwaited(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rec := &recorder{}
		const budget = 2 * time.Second
		gcpd := mod("gcpd", func(r *module.Registrar) error {
			for _, name := range []string{"gordon", "bullock", "montoya", "allen"} {
				r.Hook(module.Hook{Name: "gcpd." + name, Stop: func(ctx context.Context) error {
					rec.add("stop:%s", name)
					<-ctx.Done() // honours its ctx, and blocks until it is done
					return ctx.Err()
				}})
			}
			return nil
		})
		c := supervised(t, rec, gcpd)
		c.R1Values = func() R1Values { return R1Values{ShutdownBudget: budget} }
		k := mustNew(t, c)
		ctx, cancel := context.WithCancel(t.Context())
		done := run(ctx, k)
		synctest.Wait()
		begin := time.Now()
		cancel()
		err := <-done
		took := time.Since(begin)
		synctest.Wait()
		if took > budget+lateStopFloor {
			t.Fatalf("stop took %s with 4 blocking hooks, want at most the budget %s plus one floor %s", took, budget, lateStopFloor)
		}
		if got, want := rec.matching("stop:"), []string{"stop:allen", "stop:montoya", "stop:bullock", "stop:gordon"}; !slices.Equal(got, want) {
			t.Fatalf("stops = %v, want every hook told to stop, in reverse order: %v", got, want)
		}
		if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "gcpd.montoya, gcpd.bullock, gcpd.gordon") {
			t.Fatalf("Run = %v, want the hooks reached after the budget named", err)
		}
	})
}

// TestR1ValuesPanic_StopsInOrder is the review's m7: R1Values is module-free
// app code, so a panic in it is an error like any other start failure, and
// the hooks and workers already started are stopped.
func TestR1ValuesPanic_StopsInOrder(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rec := &recorder{}
		c := supervised(t, rec, hooked("alfred", rec))
		c.R1Values = func() R1Values { panic("Riddler swapped the config") }
		k := mustNew(t, c)
		err := k.Run(t.Context())
		if err == nil || !strings.Contains(err.Error(), "panic: Riddler swapped the config") {
			t.Fatalf("Run = %v, want the R1Values panic as an error", err)
		}
		if got, want := rec.matching("stop:"), []string{"stop:alfred.cape", "stop:alfred.cowl"}; !slices.Equal(got, want) {
			t.Fatalf("stops = %v, want the started hooks stopped in reverse: %v", got, want)
		}
		if rec.index("worker-exit:alfred") < 0 {
			t.Fatal("the started worker was not stopped")
		}
		for _, never := range rec.list() {
			if strings.HasPrefix(never, "listen:") || never == "ready" {
				t.Fatalf("%s happened after R1Values panicked: %v", never, rec.list())
			}
		}
	})
}

// TestDepsRequestRestart is Deps.RequestRestart: a module that asks for a
// restart through its Deps leaves Run through the restart path, exit 75.
func TestDepsRequestRestart(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rec := &recorder{}
		lucius := mod("lucius", func(r *module.Registrar) error {
			d := r.Deps()
			r.Worker(module.Worker{Name: "swap", Policy: module.DegradeOnExit, Run: func(ctx context.Context) error {
				d.RequestRestart("Lucius staged a swap")
				<-ctx.Done()
				return ctx.Err()
			}})
			return nil
		})
		k := mustNew(t, supervised(t, rec, hooked("alfred", rec), lucius))
		err := k.Run(t.Context())
		if !errors.Is(err, errRestartRequested) || !strings.Contains(err.Error(), "Lucius staged a swap") {
			t.Fatalf("Run = %v, want errRestartRequested with the module's reason", err)
		}
		if got := rec.matching("exit:"); !slices.Equal(got, []string{"exit:75"}) {
			t.Fatalf("exits = %v, want exactly one exit(75)", got)
		}
		if rec.index("stop:alfred.cowl") < 0 || rec.index("stop:alfred.cowl") > rec.index("exit:75") {
			t.Fatalf("exit(75) is not after the stop order: %v", rec.list())
		}
	})
}
