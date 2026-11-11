// file: core/internal/executor/harness_test.go
// version: 1.0.1
// guid: 55b9c132-7bf2-4ee6-a4eb-7a9f061f9aef
// last-edited: 2026-11-10

package executor

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/health"
	"github.com/falkcorp/mediacore/internal/taskstore"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/tasks"
)

// The test store is opened with storage.Open/Prepare/Migrate over MemFS,
// never coretest.NewStore: core/tasks is in the kernel's import graph
// (W27 rule; the shape of taskstest's storesuite boot).
const storePath = "/batcave/main.pebble"

// bootStores opens storePath on fs with core/tasks registered.
func bootStores(t testing.TB, fs *vfs.MemFS) (tasks.Stores, storage.Keyspace) {
	t.Helper()
	ctx := context.Background()
	o, err := storage.Open(ctx, storage.OpenOptions{
		Path:            storePath,
		Owners:          []storage.OwnerSpec{{Owner: taskstore.Owner, Spec: taskstore.StorageSpec(), Enabled: true}},
		Build:           "coretest+fixture",
		FreeSpaceMargin: 1,
		FS:              storage.FSOf(fs),
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
			t.Errorf("close: %v", err)
		}
	})
	ks, ok := db.Keyspace(taskstore.Owner)
	if !ok {
		t.Fatalf("owner %s has no keyspace", taskstore.Owner)
	}
	return tasks.NativeStores(ks), ks
}

// harness is one executor over one store.
type harness struct {
	t      testing.TB
	fs     *vfs.MemFS
	stores tasks.Stores
	ks     storage.Keyspace
	x      tasks.Executor
	e      *executor
}

// testConfig keeps the clocks short; synctest tests pass their own.
func testConfig() Config {
	return Config{DispatchTick: 20 * time.Millisecond, WatchdogInterval: time.Hour, ProgressInterval: time.Nanosecond,
		MinCheckpointInterval: time.Nanosecond}
}

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// newHarness boots a store and an executor with defs; edit adjusts the
// options; Run is called unless noRun.
func newHarness(t testing.TB, defs []tasks.Definition, edit func(*Options)) *harness {
	t.Helper()
	fs := vfs.NewCrashableMem()
	stores, ks := bootStores(t, fs)
	return onStores(t, fs, stores, ks, defs, edit, true)
}

// onStores builds an executor over existing stores (a second boot).
func onStores(t testing.TB, fs *vfs.MemFS, stores tasks.Stores, ks storage.Keyspace, defs []tasks.Definition, edit func(*Options), run bool) *harness {
	t.Helper()
	o := Options{Stores: stores, Keyspace: &ks, Definitions: defs, InstrumentsOff: true, Config: testConfig(), Logger: quietLogger()}
	if edit != nil {
		edit(&o)
	}
	x, err := New(o)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	h := &harness{t: t, fs: fs, stores: stores, ks: ks, x: x, e: x.(*executor)}
	t.Cleanup(func() { _ = x.Shutdown(context.Background()) })
	if run {
		if err := x.Run(context.Background()); err != nil {
			t.Fatalf("Run: %v", err)
		}
	}
	return h
}

// crashBoot clones the file system as a crash would leave it and boots a
// second executor over the clone. The first executor keeps running on the
// original (its cleanup shuts it down).
func (h *harness) crashBoot(defs []tasks.Definition, edit func(*Options)) *harness {
	h.t.Helper()
	fs := h.fs.CrashClone(vfs.CrashCloneCfg{})
	stores, ks := bootStores(h.t, fs)
	return onStores(h.t, fs, stores, ks, defs, edit, true)
}

// reboot builds a second executor over the same stores (a clean restart:
// the caller has shut the first one down).
func (h *harness) reboot(defs []tasks.Definition, edit func(*Options)) *harness {
	h.t.Helper()
	return onStores(h.t, h.fs, h.stores, h.ks, defs, edit, true)
}

func (h *harness) start(kind string, req tasks.StartRequest) tasks.RunID {
	h.t.Helper()
	if req.Mode == tasks.ModeRequestUnset {
		req.Mode = tasks.ModeRequestLive
	}
	ref, err := h.x.Start(context.Background(), tasks.Kind(kind), req)
	if err != nil {
		h.t.Fatalf("start %s: %v", kind, err)
	}
	return ref.ID
}

func (h *harness) get(id tasks.RunID) tasks.RunRecord {
	h.t.Helper()
	r, err := h.stores.Runs.Get(context.Background(), id)
	if err != nil {
		h.t.Fatalf("get %s: %v", id, err)
	}
	return r
}

// waitState polls until the run is in one of states (10s of the test's
// clock; inside a bubble that clock is fake).
func (h *harness) waitState(id tasks.RunID, states ...tasks.RunState) tasks.RunRecord {
	h.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		r := h.get(id)
		for _, s := range states {
			if r.State == s {
				return r
			}
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("run %s is %s (reason %q, err %q), want one of %v", id, r.State, r.Reason, r.Error, states)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// stays asserts the run keeps state for d.
func (h *harness) stays(id tasks.RunID, state tasks.RunState, d time.Duration) {
	h.t.Helper()
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		if r := h.get(id); r.State != state {
			h.t.Fatalf("run %s left %s for %s (reason %q)", id, state, r.State, r.Reason)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Fixture definitions: Batman kinds only (public repo).

type noParams struct {
	Case string `json:"case,omitempty"`
}

// perm is a kind's module start scope.
func perm(kind string) rbac.Requirement {
	m, _, _ := strings.Cut(kind, ".")
	return rbac.Need(rbac.Permission(m + ":run"))
}

// casesRef is the resource the fixture kinds write.
func casesRef(kind string) tasks.Resource {
	m, _, _ := strings.Cut(kind, ".")
	return tasks.Res(m, "cases")
}

func common(kind string, edits ...func(*tasks.Common)) tasks.Common {
	c := tasks.Common{
		Title:   "Batcave " + kind,
		Effects: tasks.Writes(casesRef(kind)),
		Perms:   tasks.Permissions{Start: perm(kind)},
		Resume:  tasks.ResumeContinue,
	}
	for _, e := range edits {
		e(&c)
	}
	return c
}

func taskDef(kind string, run func(rc *tasks.RC, p noParams) (string, error), edits ...func(*tasks.Common)) tasks.Definition {
	return tasks.Task(tasks.Kind(kind), tasks.TaskSpec[noParams, string]{Common: common(kind, edits...), Run: run})
}

func withResume(p tasks.ResumePolicy) func(*tasks.Common) {
	return func(c *tasks.Common) { c.Resume = p }
}

func withTimeout(d time.Duration) func(*tasks.Common) { return func(c *tasks.Common) { c.Timeout = d } }

func withCancel(cc tasks.CancelContract) func(*tasks.Common) {
	return func(c *tasks.Common) { c.Cancel = cc }
}

func readOnly(kind string) func(*tasks.Common) {
	return func(c *tasks.Common) { c.Effects = tasks.ReadOnly(casesRef(kind)) }
}

// blockUntilCtx is a cooperative body: it signals started, then returns the
// run context's cause when it ends.
func blockUntilCtx(started chan<- tasks.RunID) func(rc *tasks.RC, p noParams) (string, error) {
	return func(rc *tasks.RC, _ noParams) (string, error) {
		if started != nil {
			started <- rc.RunID()
		}
		<-rc.Context().Done()
		return "", context.Cause(rc.Context())
	}
}

func okBody(rc *tasks.RC, _ noParams) (string, error) { return "case closed", nil }

// readyRegistry is a health registry with core.storage Up (every
// definition's default Ready signal).
func readyRegistry(t testing.TB) *health.Registry {
	t.Helper()
	reg := health.NewRegistry()
	rep, err := reg.Register(health.Check{ID: health.ComponentID(tasks.SignalStorage), Title: "Storage", Readiness: true})
	if err != nil {
		t.Fatalf("register storage: %v", err)
	}
	rep.Report(health.Result{Status: health.StatusUp})
	return reg
}

func isErr(err, target error) bool { return errors.Is(err, target) }

// resultOf decodes a fixture Task run's string result.
func resultOf(t testing.TB, r tasks.RunRecord) string {
	t.Helper()
	var s string
	if err := json.Unmarshal(r.Result, &s); err != nil {
		t.Fatalf("result of %s: %v (%s)", r.ID, err, r.Result)
	}
	return s
}
