// file: internal/maintenance/jobs/kinds_test.go
// version: 1.0.1
// guid: 95f5ec58-0c87-4403-b2aa-b306523e06bd
// last-edited: 2026-11-12

package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
	"github.com/falkcorp/mediacore/tasks"
	"github.com/falkcorp/mediacore/tasks/taskstest"
)

// fakeWriter is a distinguishable tasks.Writer: identity is the assertion.
type fakeWriter struct{ name string }

func (*fakeWriter) Do(context.Context, tasks.Write, func(context.Context) ([]tasks.Change, error)) error {
	return nil
}
func (*fakeWriter) MarkRowApplied(context.Context, string)           {}
func (*fakeWriter) RowApplied(context.Context, string) (bool, error) { return false, nil }

// probeStore is a distinguishable Store; its methods are never called.
type probeStore struct {
	Store
	name string
}

// kindHost is a taskstest.FakeRun with the mode and writer a test chooses
// (FakeRun is pinned to ModeLive with no writer) and a record of the progress
// and log lines the run produced.
type kindHost struct {
	*taskstest.FakeRun
	mode tasks.Mode
	w    tasks.Writer

	mu       sync.Mutex
	reported [][2]int64 // done, total (-1 = unknown)
	logs     []string
}

func (h *kindHost) Mode() tasks.Mode     { return h.mode }
func (h *kindHost) Writer() tasks.Writer { return h.w }

func (h *kindHost) ReportProgress(done int64, total tasks.Total) {
	n := int64(-1)
	if v, ok := total.Value(); ok {
		n = v
	}
	h.mu.Lock()
	h.reported = append(h.reported, [2]int64{done, n})
	h.mu.Unlock()
}

func (h *kindHost) Log() *slog.Logger { return slog.New(&recordHandler{h: h}) }

type recordHandler struct{ h *kindHost }

func (r *recordHandler) Enabled(context.Context, slog.Level) bool { return true }
func (r *recordHandler) Handle(_ context.Context, rec slog.Record) error {
	line := rec.Level.String() + " " + rec.Message
	rec.Attrs(func(a slog.Attr) bool { line += " " + a.Key + "=" + a.Value.String(); return true })
	r.h.mu.Lock()
	r.h.logs = append(r.h.logs, line)
	r.h.mu.Unlock()
	return nil
}
func (r *recordHandler) WithAttrs([]slog.Attr) slog.Handler { return r }
func (r *recordHandler) WithGroup(string) slog.Handler      { return r }

func newKindRC(kind tasks.Kind, mode tasks.Mode, w tasks.Writer) (*kindHost, *tasks.RC) {
	h := &kindHost{FakeRun: taskstest.NewFakeRun(context.Background(), kind), mode: mode, w: w}
	return h, tasks.NewRC(h)
}

// runKind runs d's own Run, the function the executor would call, with the
// params it would decode.
func runKind(t *testing.T, d tasks.Definition, rc *tasks.RC, params string) (json.RawMessage, error) {
	t.Helper()
	r, ok := d.(interface {
		ExecutorRunners() (func(json.RawMessage) (any, error), *tasks.TaskSpec[any, json.RawMessage], *tasks.BatchSpec[any, any, json.RawMessage], *tasks.FixerSpec[any, any])
	})
	if !ok {
		t.Fatalf("%T has no ExecutorRunners", d)
	}
	decode, task, _, _ := r.ExecutorRunners()
	if task == nil {
		t.Fatalf("%s is not a Task", d.Kind())
	}
	var raw json.RawMessage
	if params != "" {
		raw = json.RawMessage(params)
	}
	p, err := decode(raw)
	if err != nil {
		return nil, err
	}
	return task.Run(rc, p)
}

// depsProbe records how a shell called NewStore.
type depsProbe struct {
	mu     sync.Mutex
	calls  int
	writer tasks.Writer
	ret    Store
}

func (d *depsProbe) deps() KindDeps {
	return KindDeps{NewStore: func(w tasks.Writer) Store {
		d.mu.Lock()
		defer d.mu.Unlock()
		d.calls++
		d.writer = w
		return d.ret
	}}
}

// probeJob keeps a real job's metadata (ID, name, policy, default params) and
// replaces its Run, so a kind built from it behaves like the real kind without
// running the real body.
type probeJob struct {
	maintenance.MaintenanceJob
	mu     sync.Mutex
	calls  int
	store  maintenance.JobStore
	dryRun bool
	ctx    context.Context
	rep    maintenance.ProgressReporter
	body   func(ctx context.Context, rep maintenance.ProgressReporter) error
}

func (j *probeJob) Run(ctx context.Context, store maintenance.JobStore, rep maintenance.ProgressReporter, dryRun bool) error {
	j.mu.Lock()
	j.calls++
	j.store, j.dryRun, j.ctx, j.rep = store, dryRun, ctx, rep
	body := j.body
	j.mu.Unlock()
	if body != nil {
		return body(ctx, rep)
	}
	return nil
}

func realJob(t *testing.T, id string) maintenance.MaintenanceJob {
	t.Helper()
	j, err := maintenance.Get(id)
	if err != nil {
		t.Fatalf("job %s: %v", id, err)
	}
	return j
}

func TestJobKinds_Conformance(t *testing.T) {
	d := &depsProbe{ret: &probeStore{}}
	defs := Kinds(d.deps(), nil)
	jobs := maintenance.All()
	if len(defs) != 34 || len(defs) != len(jobs) {
		t.Fatalf("Kinds returned %d definitions for %d jobs, want 34", len(defs), len(jobs))
	}
	for i, def := range defs {
		if want := tasks.Kind("books.maintenance." + jobs[i].ID()); def.Kind() != want {
			t.Errorf("definition %d is %s, want %s (maintenance.All() order)", i, def.Kind(), want)
		}
		if def.Shape() != tasks.ShapeTask {
			t.Errorf("%s has shape %v, want task", def.Kind(), def.Shape())
		}
	}
	taskstest.Conformance(t, defs...)
}

func TestJobKinds_ModeMapsToDryRun(t *testing.T) {
	d := &depsProbe{ret: &probeStore{}}
	preview := &probeJob{MaintenanceJob: realJob(t, "dedup-books")}  // advertises dry_run=true
	reader := &probeJob{MaintenanceJob: realJob(t, "relink-report")} // advertises dry_run=false: no preview
	pk, rk := kindFor(preview, d.deps(), nil), kindFor(reader, d.deps(), nil)

	t.Run("live writes, preview does not", func(t *testing.T) {
		for _, c := range []struct {
			mode   tasks.Mode
			dryRun bool
		}{{tasks.ModeLive, false}, {tasks.ModePreview, true}, {tasks.Mode(0), true}} {
			preview.calls = 0
			_, rc := newKindRC(pk.Kind(), c.mode, &fakeWriter{})
			if _, err := runKind(t, pk, rc, ""); err != nil {
				t.Fatalf("mode %v: %v", c.mode, err)
			}
			if preview.calls != 1 || preview.dryRun != c.dryRun {
				t.Errorf("mode %v: job ran %d time(s) with dryRun=%v, want once with %v", c.mode, preview.calls, preview.dryRun, c.dryRun)
			}
		}
	})

	t.Run("a job with no preview refuses one and never runs", func(t *testing.T) {
		_, rc := newKindRC(rk.Kind(), tasks.ModePreview, nil)
		_, err := runKind(t, rk, rc, "")
		if err == nil || !strings.Contains(err.Error(), "preview not supported") {
			t.Fatalf("preview of a no-preview job: err = %v, want 'preview not supported'", err)
		}
		if reader.calls != 0 {
			t.Errorf("the job ran %d time(s) under a refused preview", reader.calls)
		}
		_, rc = newKindRC(rk.Kind(), tasks.ModeLive, nil)
		if _, err := runKind(t, rk, rc, ""); err != nil {
			t.Fatal(err)
		}
		if reader.calls != 1 || reader.dryRun {
			t.Errorf("live run of a no-preview job: calls=%d dryRun=%v, want 1 and false", reader.calls, reader.dryRun)
		}
	})

	t.Run("a dry_run key in params is refused in any spelling", func(t *testing.T) {
		for _, params := range []string{
			`{"dry_run":false}`, `{"dryRun":true}`, `{"DRY_RUN":false}`, `{"Dry_Run":true,"force":true}`,
		} {
			preview.calls = 0
			_, rc := newKindRC(pk.Kind(), tasks.ModeLive, &fakeWriter{})
			_, err := runKind(t, pk, rc, params)
			if err == nil || !strings.Contains(err.Error(), "mode comes from the start request") {
				t.Errorf("params %s: err = %v, want the mode-key refusal", params, err)
			}
			if preview.calls != 0 {
				t.Errorf("params %s: the job ran despite the refusal", params)
			}
		}
	})

	t.Run("empty, null and other keys are fine, non-objects are not", func(t *testing.T) {
		for _, params := range []string{"", "null", `{}`, `{"force":true,"keep_count":3}`} {
			_, rc := newKindRC(pk.Kind(), tasks.ModeLive, &fakeWriter{})
			if _, err := runKind(t, pk, rc, params); err != nil {
				t.Errorf("params %q: %v", params, err)
			}
		}
		_, rc := newKindRC(pk.Kind(), tasks.ModeLive, &fakeWriter{})
		if _, err := runKind(t, pk, rc, `[1]`); err == nil {
			t.Error("array params were accepted")
		}
	})
}

// TestJobKinds_StoreFromRCWriter: for every one of the 34 kinds a live run asks
// the owner for a store with that run's own writer (nil for a ReadOnly kind,
// which core hands no writer), and the job body receives exactly the value it
// returned (05-tasks conversion rule 3), plus the run's own ID and params.
func TestJobKinds_StoreFromRCWriter(t *testing.T) {
	jobs := maintenance.All()
	if len(jobs) != 34 {
		t.Fatalf("%d jobs, want 34", len(jobs))
	}
	for _, real := range jobs {
		t.Run(real.ID(), func(t *testing.T) {
			ret := &probeStore{name: real.ID()}
			d := &depsProbe{ret: ret}
			job := &probeJob{MaintenanceJob: real}
			def := kindFor(job, d.deps(), nil)

			var w tasks.Writer
			if !tasks.CommonOf(def).Effects.IsReadOnly() {
				w = &fakeWriter{name: real.ID()}
			}
			_, rc := newKindRC(def.Kind(), tasks.ModeLive, w)
			const params = `{"path_prefix":"/Gotham"}`
			if _, err := runKind(t, def, rc, params); err != nil {
				t.Fatal(err)
			}
			if d.calls != 1 {
				t.Fatalf("NewStore called %d times, want once", d.calls)
			}
			if d.writer != w {
				t.Errorf("NewStore got writer %v, want this run's writer %v", d.writer, w)
			}
			if got, ok := job.store.(*probeStore); !ok || got != ret {
				t.Errorf("the job got store %v, want exactly what NewStore returned", job.store)
			}
			if id := maintenance.OperationIDFromCtx(job.ctx); id != string(rc.RunID()) {
				t.Errorf("job ctx operation ID = %q, want the run ID %q", id, rc.RunID())
			}
			if raw := maintenance.RawParamsFromCtx(job.ctx); string(raw) != params {
				t.Errorf("job ctx params = %s, want %s", raw, params)
			}
			if ck := checkpointerFrom(job.ctx); ck == nil {
				t.Error("job ctx carries no checkpointer")
			}
		})
	}
}

func TestKindShell_NoStoreFailsBeforeTheJobRuns(t *testing.T) {
	job := &probeJob{MaintenanceJob: realJob(t, "dedup-books")}
	for name, deps := range map[string]KindDeps{
		"nil NewStore": {},
		"nil store":    {NewStore: func(tasks.Writer) Store { return nil }},
	} {
		def := kindFor(job, deps, nil)
		_, rc := newKindRC(def.Kind(), tasks.ModeLive, &fakeWriter{})
		if _, err := runKind(t, def, rc, ""); err == nil {
			t.Errorf("%s: run succeeded", name)
		}
		if job.calls != 0 {
			t.Errorf("%s: the job ran with no store", name)
		}
	}
}

func TestKindShell_ResultAndErrors(t *testing.T) {
	d := &depsProbe{ret: &probeStore{}}
	run := func(body func(ctx context.Context, rep maintenance.ProgressReporter) error) (json.RawMessage, error) {
		job := &probeJob{MaintenanceJob: realJob(t, "scan-chapter-groups"), body: body}
		def := kindFor(job, d.deps(), nil)
		_, rc := newKindRC(def.Kind(), tasks.ModeLive, nil)
		return runKind(t, def, rc, "")
	}

	out, err := run(func(ctx context.Context, _ maintenance.ProgressReporter) error {
		return maintenance.SetResult(ctx, map[string]int{"groups": 2})
	})
	if err != nil || string(out) != `{"groups":2}` {
		t.Errorf("captured result = %s, %v; want {\"groups\":2}", out, err)
	}
	out, err = run(nil)
	if err != nil || string(out) != "null" {
		t.Errorf("no result = %s, %v; want null", out, err)
	}
	boom := errors.New("alfred is out of tea")
	var h *kindHost
	failing := &probeJob{MaintenanceJob: realJob(t, "scan-chapter-groups"), body: func(ctx context.Context, _ maintenance.ProgressReporter) error {
		_ = maintenance.SetResult(ctx, "partial")
		return boom
	}}
	fdef := kindFor(failing, d.deps(), nil)
	var frc *tasks.RC
	h, frc = newKindRC(fdef.Kind(), tasks.ModeLive, nil)
	_, err = runKind(t, fdef, frc, "")
	if !errors.Is(err, boom) {
		t.Errorf("failed run err = %v, want the job's error", err)
	}
	h.mu.Lock()
	logs := slices.Clone(h.logs)
	h.mu.Unlock()
	if len(logs) != 1 || !strings.Contains(logs[0], "partial") || !strings.Contains(logs[0], "WARN") {
		t.Errorf("a failed run's partial result was not kept in the run log: %q", logs)
	}
	_, err = run(func(context.Context, maintenance.ProgressReporter) error {
		return fmt.Errorf("backfill-file-hashes: %w", maintenance.ErrContinueInPlace)
	})
	if !errors.Is(err, tasks.ErrContinueInPlace) || !errors.Is(err, maintenance.ErrContinueInPlace) {
		t.Errorf("ErrContinueInPlace maps to %v, want core's sentinel (and the job's) in the chain", err)
	}
	_, err = run(func(ctx context.Context, _ maintenance.ProgressReporter) error {
		return maintenance.SetResult(ctx, func() {})
	})
	if err == nil {
		t.Error("an unencodable result was dropped silently")
	}
}

func TestKindShell_ProgressIsConcurrencySafe(t *testing.T) {
	h, rc := newKindRC("books.maintenance.dedup-books", tasks.ModeLive, nil)
	p := newRCProgress(rc)
	p.SetTotal(400)
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				p.Increment()
			}
		}()
	}
	wg.Wait()
	h.mu.Lock()
	defer h.mu.Unlock()
	last := h.reported[len(h.reported)-1]
	if last != [2]int64{400, 400} {
		t.Errorf("final report = %v, want done=400 total=400", last)
	}
	prev := int64(-1)
	for _, r := range h.reported {
		if r[0] < prev {
			t.Fatalf("progress went backwards: %v after %d", r, prev)
		}
		prev = r[0]
	}
}

func TestKindShell_ProgressSeedAndUnknownTotal(t *testing.T) {
	h, rc := newKindRC("books.maintenance.dedup-books", tasks.ModeLive, nil)
	p := newRCProgress(rc)
	p.SeedProgress(7)
	p.Increment()
	p.SetTotal(10)
	h.mu.Lock()
	defer h.mu.Unlock()
	want := [][2]int64{{7, -1}, {8, -1}, {8, 10}}
	if !slices.Equal(h.reported, want) {
		t.Errorf("reports = %v, want %v (unknown total until set, seeded position kept)", h.reported, want)
	}
}

func TestKindShell_LogLevels(t *testing.T) {
	h, rc := newKindRC("books.maintenance.dedup-books", tasks.ModeLive, nil)
	p := newRCProgress(rc)
	detail := "two-face flipped"
	p.Log("warn", "careful", &detail)
	p.Log("ERROR", "failed", nil)
	p.Log("debug", "fine detail", nil)
	p.Log("nonsense", "who knows", nil)
	want := []string{"WARN careful details=two-face flipped", "ERROR failed", "DEBUG fine detail", "INFO who knows"}
	h.mu.Lock()
	defer h.mu.Unlock()
	if !slices.Equal(h.logs, want) {
		t.Errorf("logs = %q, want %q", h.logs, want)
	}
}

// permJob is a job that asks for a permission other than settings.manage.
type permJob struct {
	maintenance.MaintenanceJob
	perm string
}

func (j permJob) Permission() string { return j.perm }

func TestKindShell_StartRequirement(t *testing.T) {
	base := realJob(t, "dedup-books")
	if got := startRequirement(base); got.IsZero() || len(got.Permissions()) != len(settingsManageScopes) {
		t.Errorf("default requirement = %v, want AllOf the settings.manage scopes", got)
	}
	if got := startRequirement(permJob{base, "settings.manage"}); got.IsZero() {
		t.Error("an explicit settings.manage lost its requirement")
	}
	// An atom this package has no row for must fail closed: a zero requirement is
	// refused at registration, never replaced by a guess.
	def := kindFor(permJob{base, "library.edit_metadata"}, KindDeps{}, nil)
	if !tasks.CommonOf(def).Perms.Start.IsZero() {
		t.Error("an unmapped permission got a start requirement")
	}
	if len(tasks.Problems(def)) == 0 {
		t.Error("a kind with no start requirement has no registration problem")
	}
}

func TestKindShell_DefinitionFields(t *testing.T) {
	merge := realJob(t, "merge-chapter-groups") // the one job with its own concurrency key
	plain := realJob(t, "dedup-books")
	var asked []tasks.Kind
	sched := func(k tasks.Kind) *tasks.Schedule {
		asked = append(asked, k)
		return nil
	}
	mk, pk := kindFor(merge, KindDeps{}, sched), kindFor(plain, KindDeps{}, nil)
	if c := tasks.CommonOf(mk); c.ConcurrencyKey != "books.library.scan" {
		t.Errorf("merge-chapter-groups key = %q, want books.library.scan", c.ConcurrencyKey)
	}
	if c := tasks.CommonOf(pk); c.ConcurrencyKey != "" {
		t.Errorf("dedup-books key = %q, want the zero key (Key(Kind))", c.ConcurrencyKey)
	}
	if len(asked) != 1 || asked[0] != mk.Kind() {
		t.Errorf("schedule hook asked for %v, want exactly %s", asked, mk.Kind())
	}
	for _, d := range []tasks.Definition{mk, pk} {
		c := tasks.CommonOf(d)
		if c.Priority != tasks.PriorityNormal || !slices.Equal(c.Ready, []tasks.ReadySignal{"books.memdb"}) || c.Perms.StartableByViewers != "" {
			t.Errorf("%s: priority=%v ready=%v viewers=%q", d.Kind(), c.Priority, c.Ready, c.Perms.StartableByViewers)
		}
	}
}

// TestKindShell_EffectsTable: every job has a declared write set, no row is
// stale, and ReadOnly is exactly the jobs HEAD gives no preview.
func TestKindShell_EffectsTable(t *testing.T) {
	seen := map[string]bool{}
	var readOnly []string
	for _, job := range maintenance.All() {
		seen[job.ID()] = true
		ws, ok := jobWrites[job.ID()]
		if !ok {
			t.Errorf("%s has no jobWrites row", job.ID())
			continue
		}
		fx := effectsFor(job.ID())
		if !fx.Declared() {
			t.Errorf("%s: effects are not declared", job.ID())
		}
		if fx.IsReadOnly() != (len(ws) == 0) {
			t.Errorf("%s: IsReadOnly=%v with %d write kinds", job.ID(), fx.IsReadOnly(), len(ws))
		}
		if fx.IsReadOnly() == AdvertisedDryRunDefault(job) {
			t.Errorf("%s: ReadOnly=%v but AdvertisedDryRunDefault=%v; HEAD gives a preview to exactly the writers", job.ID(), fx.IsReadOnly(), AdvertisedDryRunDefault(job))
		}
		if fx.IsReadOnly() {
			readOnly = append(readOnly, job.ID())
		}
		if got := len(fx.WriteSet()); got != len(ws) {
			t.Errorf("%s: write set has %d refs, want %d", job.ID(), got, len(ws))
		}
	}
	for id := range jobWrites {
		if !seen[id] {
			t.Errorf("jobWrites has a row for %q, which is no registered job", id)
		}
	}
	slices.Sort(readOnly)
	want := []string{"relink-report", "scan-chapter-groups", "scan-duplicate-files", "scan-duration-mismatch", "scan-metadata-hash-dups"}
	if !slices.Equal(readOnly, want) {
		t.Errorf("ReadOnly jobs = %v, want %v", readOnly, want)
	}
	if fx := effectsFor("not-a-job"); fx.Declared() {
		t.Error("an unknown job got declared effects")
	}
}

func TestKindShell_ResumeMapping(t *testing.T) {
	for id, want := range map[string]tasks.ResumePolicy{
		"backfill-file-hashes": tasks.ResumeContinue, // HEAD ResumeRestart
		"dedup-books":          tasks.ResumeDrop,     // HEAD ResumeDrop
	} {
		if got := tasks.CommonOf(kindFor(realJob(t, id), KindDeps{}, nil)).Resume; got != want {
			t.Errorf("%s resume = %v, want %v", id, got, want)
		}
	}
	if got := resumeFor(0); got != 0 {
		t.Errorf("an unspecified policy mapped to %v, want it left zero for registration to refuse", got)
	}
}
