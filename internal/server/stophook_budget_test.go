// file: internal/server/stophook_budget_test.go
// version: 1.0.2
// guid: 877b968e-c9c6-4cac-b2e9-06bc892394d0
// last-edited: 2026-11-08

package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/plugin"
	"github.com/falkcorp/audiobook-organizer/internal/serviceregistry"
)

// wedgedStopper is a container service whose Stop ignores its context and
// blocks until the test lets it go, the way the activity Writer's or the
// search index's Stop would on a stuck disk.
type wedgedStopper struct{ release chan struct{} }

func (w wedgedStopper) Stop(context.Context) error {
	<-w.release
	return nil
}

// TestStopHook_WedgedStepsStayWithinTheBudget (R1-15 review m2): a stop step
// whose API takes no deadline (the file I/O pool, the container's Stoppers,
// the scheduler's tasks) cannot hold StopHook past the kernel's shutdown
// budget. Each case wedges one step on a channel the test closes only in
// Cleanup. StopHook must return by the deadline (plus scheduling slack) and
// close Stopped, where it used to wait for the step however long it took.
func TestStopHook_WedgedStepsStayWithinTheBudget(t *testing.T) {
	const budget = 400 * time.Millisecond
	cases := []struct {
		name  string
		wedge func(t *testing.T, s *Server, release chan struct{})
	}{
		{"file I/O pool", func(t *testing.T, s *Server, release chan struct{}) {
			p := NewFileIOPool(1)
			started := make(chan struct{})
			p.SubmitTyped("arkham-transfer", "hold", func() {
				close(started)
				<-release
			})
			<-started
			s.fileIOPool = p
		}},
		{"container", func(t *testing.T, s *Server, release chan struct{}) {
			c := serviceregistry.NewContainerFrom().Override("chromemstore", wedgedStopper{release: release})
			if err := c.Build(context.Background()); err != nil {
				t.Fatal(err)
			}
			s.container = c
		}},
		{"scheduler tasks", func(t *testing.T, s *Server, release chan struct{}) {
			s.life.backgroundWG.Go(func() { <-release })
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &Server{stopped: make(chan struct{})}
			release := make(chan struct{})
			tc.wedge(t, s, release)
			t.Cleanup(func() { close(release) })

			ctx, cancel := context.WithTimeout(context.Background(), budget)
			defer cancel()
			start := time.Now()
			if err := s.StopHook(ctx); err != nil {
				t.Fatalf("StopHook: %v", err)
			}
			if took := time.Since(start); took > budget+time.Second {
				t.Errorf("StopHook took %s with the %s step wedged; the budget was %s", took, tc.name, budget)
			}
			select {
			case <-s.Stopped():
			default:
				t.Error("StopHook returned without closing Stopped")
			}
		})
	}
}

// TestStopWithin_ReportsWhetherTheStepFinished pins the helper's contract: a
// step that returns in time is reported finished, and one still running at
// its share of the budget is reported unfinished as soon as the share ends.
func TestStopWithin_ReportsWhetherTheStepFinished(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if !stopWithin(ctx, time.Second, 0, "quick", func() {}) {
		t.Error("a step that returned at once was reported unfinished")
	}

	release := make(chan struct{})
	defer close(release)
	short, cancelShort := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancelShort()
	start := time.Now()
	if stopWithin(short, time.Minute, 200*time.Millisecond, "wedged", func() { <-release }) {
		t.Error("a wedged step was reported finished")
	}
	if took := time.Since(start); took > 250*time.Millisecond {
		t.Errorf("stopWithin waited %s; its share was the 300ms budget less a 200ms reserve", took)
	}
}

// failingStopper is a container service whose Stop fails at once.
type failingStopper struct{}

func (failingStopper) Stop(context.Context) error { return errors.New("simulated stop failure") }

// timedOutPlugin is a plugin whose Shutdown ran out of its context.
type timedOutPlugin struct{}

func (timedOutPlugin) ID() string                              { return "arkham-notifier" }
func (timedOutPlugin) Name() string                            { return "Arkham notifier" }
func (timedOutPlugin) Version() string                         { return "1.0.0" }
func (timedOutPlugin) Capabilities() []plugin.Capability       { return nil }
func (timedOutPlugin) Init(context.Context, plugin.Deps) error { return nil }
func (timedOutPlugin) Shutdown(context.Context) error          { return context.DeadlineExceeded }
func (timedOutPlugin) HealthCheck() error                      { return nil }

// stopHookCleanStopServer is a minimal Server whose container holds a
// complete HNSW store with one book, persisting to a temp directory. add
// registers more container services before the build.
func stopHookCleanStopServer(t *testing.T, add func(c *serviceregistry.Container)) (*Server, *database.HNSWEmbeddingStore) {
	t.Helper()
	store := database.NewHNSWEmbeddingStore(4)
	if err := store.Upsert(context.Background(), "book", "the-long-halloween", []float32{1, 0, 0, 0}, nil); err != nil {
		t.Fatal(err)
	}
	store.SetComplete(true)
	c := serviceregistry.NewContainerFrom().Override("chromemstore", store)
	if add != nil {
		add(c)
	}
	if err := c.Build(context.Background()); err != nil {
		t.Fatal(err)
	}
	return &Server{stopped: make(chan struct{}), hnswPersistDir: t.TempDir(), container: c}, store
}

// TestStopHook_WritesTheCleanStopMarkerLast (R1-15 follow-up review, MAJOR-2
// and MINOR-2): StopHook itself, with every step finished, leaves the marker
// (M7), and leaves it after the stop-time export: the graph changed after
// the periodic snapshot, so a marker taken before that export would be
// refused (M16).
func TestStopHook_WritesTheCleanStopMarkerLast(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(map[bool]string{false: "graph unchanged since the periodic snapshot", true: "graph changed since the periodic snapshot"}[changed], func(t *testing.T) {
			s, store := stopHookCleanStopServer(t, nil)
			s.exportHNSWSnapshot("periodic")
			if changed {
				if err := store.Upsert(context.Background(), "book", "dark-victory", []float32{0, 1, 0, 0}, nil); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			if err := s.StopHook(ctx); err != nil {
				t.Fatalf("StopHook: %v", err)
			}
			if !cleanStopMarkerExists(t, s.hnswPersistDir) {
				t.Error("a stop whose every step finished, with a snapshot of the graph on disk, must leave the marker")
			}
		})
	}
}

// wedgedOpRegistry is an operations registry running one op that ignores its
// context until release closes, so Shutdown's drain times out.
func wedgedOpRegistry(t *testing.T, release chan struct{}) *opsregistry.Registry {
	t.Helper()
	store, err := database.NewPebbleStoreInMemory(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("NewPebbleStoreInMemory: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	reg := opsregistry.New(store, slog.New(slog.DiscardHandler), 1, nil)
	started := make(chan struct{})
	if err := reg.RegisterOp(opsregistry.OperationDef{
		ID:           "test.wedged-op",
		Liveness:     opsregistry.LivenessManual,
		Plugin:       "test",
		DisplayName:  "wedged op",
		ResumePolicy: opsregistry.ResumeDrop,
		Run: func(context.Context, json.RawMessage, opsregistry.Reporter) error {
			close(started)
			<-release
			return nil
		},
	}); err != nil {
		t.Fatalf("RegisterOp: %v", err)
	}
	reg.Start(t.Context())
	if _, err := reg.EnqueueOp(context.Background(), "test.wedged-op", map[string]any{}); err != nil {
		t.Fatalf("EnqueueOp: %v", err)
	}
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("the wedged op never started")
	}
	return reg
}

// TestStopHook_NoCleanStopMarkerAfterAnAbandonedStep (MAJOR-2): a stop step
// left running at its share of the budget, or failed, may leave a writer
// running, so the marker is not written although the snapshot on disk is the
// graph. Each case abandons one step; the budget leaves every other step a
// positive share, so only that step can stop the marker.
func TestStopHook_NoCleanStopMarkerAfterAnAbandonedStep(t *testing.T) {
	const budget = 6 * time.Second
	cases := []struct {
		name string
		add  func(c *serviceregistry.Container, release chan struct{})
		set  func(t *testing.T, s *Server, release chan struct{})
	}{
		{name: "ops registry", set: func(t *testing.T, s *Server, release chan struct{}) {
			s.opRegistry = wedgedOpRegistry(t, release)
		}},
		{name: "background goroutines", set: func(t *testing.T, s *Server, release chan struct{}) {
			s.bgWG.Go("arkham-backfill", func() { <-release })
		}},
		{name: "file I/O pool", set: func(t *testing.T, s *Server, release chan struct{}) {
			p := NewFileIOPool(1)
			started := make(chan struct{})
			p.SubmitTyped("arkham-transfer", "hold", func() {
				close(started)
				<-release
			})
			<-started
			s.fileIOPool = p
		}},
		{name: "container left running", add: func(c *serviceregistry.Container, release chan struct{}) {
			c.Override("metricsstore", wedgedStopper{release: release})
		}},
		{name: "container stop failed", add: func(c *serviceregistry.Container, _ chan struct{}) {
			c.Override("metricsstore", failingStopper{})
		}},
		{name: "scheduler tasks", set: func(t *testing.T, s *Server, release chan struct{}) {
			s.life.backgroundWG.Go(func() { <-release })
		}},
		{name: "plugins", set: func(t *testing.T, s *Server, _ chan struct{}) {
			r := plugin.NewRegistry()
			r.Register(timedOutPlugin{})
			r.Enable(timedOutPlugin{}.ID())
			if err := r.InitAll(context.Background(), plugin.Deps{}); err != nil {
				t.Fatal(err)
			}
			s.pluginRegistry = r
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			release := make(chan struct{})
			var add func(c *serviceregistry.Container)
			if tc.add != nil {
				add = func(c *serviceregistry.Container) { tc.add(c, release) }
			}
			s, _ := stopHookCleanStopServer(t, add)
			s.exportHNSWSnapshot("periodic")
			if tc.set != nil {
				tc.set(t, s, release)
			}
			t.Cleanup(func() { close(release) })

			ctx, cancel := context.WithTimeout(context.Background(), budget)
			defer cancel()
			if err := s.StopHook(ctx); err != nil {
				t.Fatalf("StopHook: %v", err)
			}
			if cleanStopMarkerExists(t, s.hnswPersistDir) {
				t.Errorf("the %s step did not finish, and the stop still left the clean-stop marker", tc.name)
			}
		})
	}
}
