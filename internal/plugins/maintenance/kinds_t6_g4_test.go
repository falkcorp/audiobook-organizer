// file: internal/plugins/maintenance/kinds_t6_g4_test.go
// version: 1.0.0
// guid: 952f6abf-08d2-41bd-aec8-95b8a3f39bd9
// last-edited: 2026-11-13

package maintenance

import (
	"context"
	"encoding/json"
	"log/slog"
	"slices"
	"sync"
	"testing"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
	"github.com/falkcorp/mediacore/tasks"
	"github.com/falkcorp/mediacore/tasks/taskstest"
)

// ---- recording fakes of the group's two interfaces (rule R10) ----

// t6g4Calls is a concurrency-safe record of what a fake saw, in order.
type t6g4Calls struct {
	mu    sync.Mutex
	calls []string
}

func (c *t6g4Calls) add(s string) {
	c.mu.Lock()
	c.calls = append(c.calls, s)
	c.mu.Unlock()
}

func (c *t6g4Calls) all() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.calls)
}

// fakeT6G4Store answers reads from fixtures and records each one. A method a
// test did not give a fixture for answers empty.
type fakeT6G4Store struct {
	t6g4Store
	rec *t6g4Calls

	bookFilesCore []database.BookFileCore
	identReport   database.IdentIndexReport
}

func (s *fakeT6G4Store) VerifyIdentIndex(context.Context) (database.IdentIndexReport, error) {
	s.rec.add("VerifyIdentIndex")
	return s.identReport, nil
}

func (s *fakeT6G4Store) GetAllBookFilesCore() ([]database.BookFileCore, error) {
	s.rec.add("GetAllBookFilesCore")
	return s.bookFilesCore, nil
}

// fakeT6G4Writer records every write a core makes through it.
type fakeT6G4Writer struct {
	t6g4Writer
	rec *t6g4Calls
	// wrapped is the tasks.Writer NewWriter was handed.
	wrapped tasks.Writer
}

func (w *fakeT6G4Writer) RebuildIdentIndex(context.Context) (database.IdentRebuildStats, error) {
	w.rec.add("RebuildIdentIndex")
	return database.IdentRebuildStats{Books: 3}, nil
}

// t6g4Probe is the recording deps of one test.
type t6g4Probe struct {
	store  *fakeT6G4Store
	writer *fakeT6G4Writer
	mu     sync.Mutex
	// newWriterCalls and handed record how the native shell called NewWriter.
	newWriterCalls int
	handed         tasks.Writer
}

// fakeT6G4Deps is the helper the brief names: deps with a recording store and a
// NewWriter that records the tasks.Writer it was handed.
func fakeT6G4Deps(t *testing.T) (t6g4Deps, *t6g4Probe) {
	t.Helper()
	rec := &t6g4Calls{}
	pr := &t6g4Probe{store: &fakeT6G4Store{rec: rec}, writer: &fakeT6G4Writer{rec: rec}}
	return t6g4Deps{
		Store: pr.store,
		NewWriter: func(w tasks.Writer) t6g4Writer {
			pr.mu.Lock()
			defer pr.mu.Unlock()
			pr.newWriterCalls++
			pr.handed = w
			pr.writer.wrapped = w
			return pr.writer
		},
	}, pr
}

// ---- a run host with the mode and writer a test chooses ----

// t6g4Writer0 is a distinguishable tasks.Writer: identity is the assertion.
type t6g4RunWriter struct{ name string }

func (*t6g4RunWriter) Do(context.Context, tasks.Write, func(context.Context) ([]tasks.Change, error)) error {
	return nil
}
func (*t6g4RunWriter) MarkRowApplied(context.Context, string)           {}
func (*t6g4RunWriter) RowApplied(context.Context, string) (bool, error) { return false, nil }

// t6g4Host is a taskstest.FakeRun pinned to the mode and writer a test sets,
// with a record of progress and log lines.
type t6g4Host struct {
	*taskstest.FakeRun
	mode tasks.Mode
	w    tasks.Writer

	mu       sync.Mutex
	progress [][2]int64
	logs     []string
}

func (h *t6g4Host) Mode() tasks.Mode     { return h.mode }
func (h *t6g4Host) Writer() tasks.Writer { return h.w }

func (h *t6g4Host) ReportProgress(done int64, total tasks.Total) {
	n := int64(-1)
	if v, ok := total.Value(); ok {
		n = v
	}
	h.mu.Lock()
	h.progress = append(h.progress, [2]int64{done, n})
	h.mu.Unlock()
}

func (h *t6g4Host) Log() *slog.Logger { return slog.New(&t6g4Handler{h: h}) }

type t6g4Handler struct{ h *t6g4Host }

func (r *t6g4Handler) Enabled(context.Context, slog.Level) bool { return true }
func (r *t6g4Handler) Handle(_ context.Context, rec slog.Record) error {
	line := rec.Level.String() + " " + rec.Message
	rec.Attrs(func(a slog.Attr) bool { line += " " + a.Key + "=" + a.Value.String(); return true })
	r.h.mu.Lock()
	r.h.logs = append(r.h.logs, line)
	r.h.mu.Unlock()
	return nil
}
func (r *t6g4Handler) WithAttrs([]slog.Attr) slog.Handler { return r }
func (r *t6g4Handler) WithGroup(string) slog.Handler      { return r }

func newT6G4RC(kind tasks.Kind, mode tasks.Mode, w tasks.Writer) (*t6g4Host, *tasks.RC) {
	h := &t6g4Host{FakeRun: taskstest.NewFakeRun(context.Background(), kind), mode: mode, w: w}
	return h, tasks.NewRC(h)
}

// runT6G4Kind runs d's own Run, the function the executor would call, with the
// params it would decode, and returns the result as JSON.
func runT6G4Kind(t *testing.T, d tasks.Definition, rc *tasks.RC, params string) (json.RawMessage, error) {
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

func t6g4KindByName(t *testing.T, defs []tasks.Definition, kind tasks.Kind) tasks.Definition {
	t.Helper()
	for _, d := range defs {
		if d.Kind() == kind {
			return d
		}
	}
	t.Fatalf("no native kind %s among %d definitions", kind, len(defs))
	return nil
}

// ---- the three tests of the brief ----

// TestT6G4Kinds_Conformance: every native kind of the group passes the shared
// conformance suite (05-tasks 5T.9 last row).
func TestT6G4Kinds_Conformance(t *testing.T) {
	deps, _ := fakeT6G4Deps(t)
	defs := (&Plugin{}).t6g4Kinds(deps, nil)
	if got, want := len(defs), len(t6g4Expected); got != want {
		t.Fatalf("t6g4Kinds returned %d definitions, want %d", got, want)
	}
	for i, def := range defs {
		if def.Kind() != t6g4Expected[i].kind {
			t.Errorf("definition %d is %s, want %s (file order)", i, def.Kind(), t6g4Expected[i].kind)
		}
		if def.Shape() != tasks.ShapeTask {
			t.Errorf("%s has shape %v, want task", def.Kind(), def.Shape())
		}
	}
	taskstest.Conformance(t, defs...)
}

// t6g4Want is one row of the HEAD-parity table (rule R8).
type t6g4Want struct {
	kind tasks.Kind
	head func(p *Plugin) sdk.OperationDef
	// resume is rule R5's mapping of HEAD's ResumePolicy.
	resume tasks.ResumePolicy
	// par is rule R4's Parallelism.String() of the converted in-body loop, "" for
	// a definition with no loop. HEAD's call sites decide it (comments in each
	// file).
	par string
	// writes is rule R7's native write set, as books resource kinds; nil with
	// readOnly true means "Declared ReadOnly".
	writes   []string
	readOnly bool
	// liveOnly: HEAD runs this only in an explicit live mode (rule R7).
	liveOnly bool
}

var t6g4Expected = []t6g4Want{
	{
		kind: identIndexKind, head: func(p *Plugin) sdk.OperationDef { return p.identIndexRebuildDef() },
		resume: tasks.ResumeDrop, writes: []string{"books"},
	},
	{
		kind: integrityCheckKind, head: func(p *Plugin) sdk.OperationDef { return p.integrityCheckDef() },
		resume: tasks.ResumeDrop, readOnly: true,
	},
}

// TestT6G4Kinds_MatchHEADDefs: each native kind equals "books." + HEAD's ID, and
// its Title, Resume, Priority, Timeout, key, effects class and parallelism are
// rule R5's, R4's and R7's mapping of the HEAD definition built in the same test,
// so a drift in either fails.
func TestT6G4Kinds_MatchHEADDefs(t *testing.T) {
	deps, _ := fakeT6G4Deps(t)
	p := &Plugin{}
	defs := p.t6g4Kinds(deps, nil)
	for _, w := range t6g4Expected {
		t.Run(string(w.kind), func(t *testing.T) {
			head := w.head(p)
			def := t6g4KindByName(t, defs, w.kind)
			c := tasks.CommonOf(def)
			if want := tasks.Kind("books." + head.ID); def.Kind() != want {
				t.Errorf("kind = %s, want %s", def.Kind(), want)
			}
			if c.Title != head.DisplayName {
				t.Errorf("Title = %q, want HEAD's DisplayName %q", c.Title, head.DisplayName)
			}
			if c.Help != head.Description {
				t.Errorf("Help differs from HEAD's Description")
			}
			if c.Resume != w.resume {
				t.Errorf("Resume = %v, want %v", c.Resume, w.resume)
			}
			if c.Timeout != head.Timeout || c.ProgressTimeout != head.ProgressTimeout {
				t.Errorf("Timeout/ProgressTimeout = %v/%v, want %v/%v", c.Timeout, c.ProgressTimeout, head.Timeout, head.ProgressTimeout)
			}
			if wantKey := tasks.Key("books." + head.ConcurrencyKey); head.ConcurrencyKey != "" && c.ConcurrencyKey != wantKey {
				t.Errorf("ConcurrencyKey = %q, want %q", c.ConcurrencyKey, wantKey)
			}
			wantPrio := map[sdk.Priority]tasks.Priority{sdk.PriorityLow: tasks.PriorityLow, sdk.PriorityNormal: tasks.PriorityNormal, sdk.PriorityHigh: tasks.PriorityHigh}[head.DefaultPriority]
			if c.Priority != wantPrio {
				t.Errorf("Priority = %v, want %v", c.Priority, wantPrio)
			}
			if got := t6g4WriteKinds(c.Effects); w.readOnly != c.Effects.IsReadOnly() || !slices.Equal(got, w.writes) {
				t.Errorf("Effects: readOnly=%v writes=%v, want readOnly=%v writes=%v", c.Effects.IsReadOnly(), got, w.readOnly, w.writes)
			}
			if got := t6g4ParFor(def.Kind()); got != w.par {
				t.Errorf("parallelism = %q, want %q", got, w.par)
			}
		})
	}
}

// t6g4WriteKinds lists the books resource kinds an Effects writes, sorted.
func t6g4WriteKinds(e tasks.Effects) []string {
	var out []string
	for _, r := range e.WriteSet() {
		out = append(out, r.Kind)
	}
	slices.Sort(out)
	return out
}

// t6g4ParFor is the Parallelism.String() of a kind's in-body loop.
func t6g4ParFor(kind tasks.Kind) string {
	if p, ok := t6g4Parallelism[kind]; ok {
		return p.String()
	}
	return ""
}

// TestT6G4Kinds_WritesThroughShell: for every kind whose Effects declare writes,
// one Live run shows NewWriter called with that run's rc.Writer() and every write
// the run makes reaching the recording t6g4Writer (rule R10).
func TestT6G4Kinds_WritesThroughShell(t *testing.T) {
	for _, w := range t6g4Expected {
		if w.readOnly {
			continue
		}
		t.Run(string(w.kind), func(t *testing.T) {
			scenario, ok := t6g4WriteScenarios[w.kind]
			if !ok {
				t.Fatalf("no write scenario for %s", w.kind)
			}
			scenario(t)
		})
	}
}

// t6g4WriteScenarios holds one Live-run scenario per writing kind; each
// definition's section below adds its own.
var t6g4WriteScenarios = map[tasks.Kind]func(t *testing.T){}

// ---- file-integrity-check ----

func TestIntegrityCheckKind_ReadsOnlyThroughEnvStore(t *testing.T) {
	deps, pr := fakeT6G4Deps(t)
	pr.store.bookFilesCore = []database.BookFileCore{
		{ID: "f1", FileHash: "aa", OriginalFileHash: "bb", OriginalFileHashKind: database.FileHashKindSampled},
		{ID: "f2", FileHash: "cc", OriginalFileHash: "cc", OriginalFileHashKind: database.FileHashKindSampled},
	}
	def := (&Plugin{}).integrityCheckKind(deps, nil)
	_, rc := newT6G4RC(integrityCheckKind, tasks.ModeLive, &t6g4RunWriter{name: "run"})
	out, err := runT6G4Kind(t, def, rc, "")
	if err != nil {
		t.Fatal(err)
	}
	var res integrityCheckResult
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatal(err)
	}
	if res.Flagged != 1 || res.TotalFiles != 2 {
		t.Errorf("result = %+v, want 1 flagged of 2", res)
	}
	if pr.newWriterCalls != 0 {
		t.Errorf("a ReadOnly kind built a writer %d time(s)", pr.newWriterCalls)
	}
	if got := pr.store.rec.all(); !slices.Equal(got, []string{"GetAllBookFilesCore"}) {
		t.Errorf("store reads = %v", got)
	}
}

func TestIntegrityCheckKind_RefusesWithoutStore(t *testing.T) {
	def := (&Plugin{}).integrityCheckKind(t6g4Deps{}, nil)
	_, rc := newT6G4RC(integrityCheckKind, tasks.ModeLive, nil)
	if _, err := runT6G4Kind(t, def, rc, ""); err == nil {
		t.Fatal("a run with no t6g4Deps.Store succeeded")
	}
}

// ---- ident-index-rebuild ----

func init() {
	t6g4WriteScenarios[identIndexKind] = func(t *testing.T) {
		deps, pr := fakeT6G4Deps(t)
		def := (&Plugin{}).identIndexKind(deps, nil)
		run := &t6g4RunWriter{name: "run"}
		_, rc := newT6G4RC(identIndexKind, tasks.ModeLive, run)
		if _, err := runT6G4Kind(t, def, rc, ""); err != nil {
			t.Fatal(err)
		}
		if pr.newWriterCalls != 1 || pr.handed != tasks.Writer(run) {
			t.Errorf("NewWriter called %d time(s) with %v, want once with this run's rc.Writer()", pr.newWriterCalls, pr.handed)
		}
		if got := pr.store.rec.all(); !slices.Equal(got, []string{"RebuildIdentIndex"}) {
			t.Errorf("calls = %v, want the rebuild through the writer only", got)
		}
	}
}

func TestIdentIndexKind_PreviewVerifiesAndNeverRebuilds(t *testing.T) {
	deps, pr := fakeT6G4Deps(t)
	def := (&Plugin{}).identIndexKind(deps, nil)
	_, rc := newT6G4RC(identIndexKind, tasks.ModePreview, &t6g4RunWriter{})
	if _, err := runT6G4Kind(t, def, rc, ""); err != nil {
		t.Fatal(err)
	}
	if got := pr.store.rec.all(); !slices.Equal(got, []string{"VerifyIdentIndex"}) {
		t.Errorf("calls = %v, want one verify and no rebuild", got)
	}
}
