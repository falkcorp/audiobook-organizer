// file: internal/plugins/metafetch/kinds_test.go
// version: 1.0.1
// guid: 5b1d7e92-0c4a-4f68-a3d1-8e27c9f40b15
// last-edited: 2026-11-19

package metafetch

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/tasks"
	"github.com/falkcorp/mediacore/tasks/taskstest"
	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// F5-05 metafetch test kit. The three shared tests below are exactly the three
// top-level TestMetafetchKinds_ functions the brief's acceptance counts; every
// other test in the package's kinds files uses another prefix.

// kindRow is one definition's expected shape, spelled out literally (rule R8):
// the native kind is compared with this row and HEAD's definition is compared
// with the same row, so a drift of either fails.
type kindRow struct {
	// HeadID is the HEAD def ID; the native kind is "books." + HeadID (rule R3).
	HeadID string
	// Title is HEAD's DisplayName.
	Title string
	// HeadResume is HEAD's ResumePolicy; Resume the native one (rule R5 and the
	// F5-11 ruling: Restart maps to Continue only if HEAD reads its checkpoint on
	// restart).
	HeadResume sdk.ResumePolicy
	Resume     tasks.ResumePolicy
	// Priority is the native priority; HEAD's DefaultPriority must map to it.
	Priority tasks.Priority
	// Timeout is HEAD's Timeout, carried unchanged.
	Timeout time.Duration
	// Writes is the native write set as pkg/ops constants; ReadOnly is
	// tasks.ReadOnly (no write set).
	Writes   []ops.Resource
	ReadOnly bool
	// Par is the parallelism String() of each pool the body runs (rule R4).
	Par []string
	// HeadKey is HEAD's ConcurrencyKey; the native key is "books." + HeadKey.
	HeadKey string
	// NotWhile is the native NotWhile list, from HEAD's DependsOn.
	NotWhile []tasks.Kind
}

// metafetchRows is rule R8's table, written out by hand.
func metafetchRows() []kindRow {
	return []kindRow{
		{
			HeadID: "metafetch.calibrate-scoring", Title: "Calibrate metadata scoring (report only)",
			HeadResume: sdk.ResumeDrop, Resume: tasks.ResumeDrop,
			Priority: tasks.PriorityNormal, Timeout: 30 * time.Minute,
			ReadOnly: true,
			Par:      []string{"cpu"},
			HeadKey:  "metafetch.calibrate-scoring",
		},
		{
			HeadID: "metafetch.asin-backfill", Title: "Backfill missing ASINs and ISBNs from Audible",
			HeadResume: sdk.ResumeRestart, Resume: tasks.ResumeContinue,
			Priority: tasks.PriorityLow, Timeout: 24 * time.Hour,
			// HEAD declares {books}; widened (coordinator ruling): the body also
			// writes authors, metadata_cache and indexes through ModifyBook's row,
			// and the no-match markers (asin_misses).
			Writes:   []ops.Resource{ops.ResBooks, ops.ResAuthors, ops.ResMetadataCache, ops.ResIndexes, ops.ResASINMisses},
			Par:      []string{"fixed(4): Audible's token bucket is the throttle; workers param, default 4, clamped 1..8"},
			HeadKey:  "metafetch.asin-backfill",
			NotWhile: []tasks.Kind{"books.catalog.harvest-authors", "books.identification.advance"},
		},
		{
			HeadID: "metafetch.pcache-sweep", Title: "Sweep the shared metadata answer cache",
			HeadResume: sdk.ResumeDrop, Resume: tasks.ResumeDrop,
			Priority: tasks.PriorityLow,
			Writes:   []ops.Resource{ops.ResMetadataCache},
			Par:      []string{"fixed(8): store-bound point-gets; the metadata-cache reaper's resolve figure"},
			HeadKey:  "metafetch.pcache-sweep",
		},
	}
}

// kindHeadPriority maps HEAD's three priorities onto the native ones.
func kindHeadPriority(p sdk.Priority) tasks.Priority {
	switch p {
	case sdk.PriorityHigh:
		return tasks.PriorityHigh
	case sdk.PriorityNormal:
		return tasks.PriorityNormal
	default: // sdk.PriorityLow is the zero value
		return tasks.PriorityLow
	}
}

// byKind finds the definition of a kind in defs.
func byKind(t *testing.T, defs []tasks.Definition, kind tasks.Kind) tasks.Definition {
	t.Helper()
	for _, d := range defs {
		if d.Kind() == kind {
			return d
		}
	}
	t.Fatalf("no definition of kind %s", kind)
	return nil
}

func refStrings(rs []ops.Resource) []string {
	out := make([]string, 0, len(rs))
	for _, r := range rs {
		out = append(out, "books/"+string(r))
	}
	sort.Strings(out)
	return out
}

func refStrings2(refs []entity.Ref) []string {
	out := make([]string, 0, len(refs))
	for _, r := range refs {
		out = append(out, tasks.FormatRef(r))
	}
	sort.Strings(out)
	return out
}

// parGot is the parallelism of each pool the body runs, read from the code's own
// parallelism functions (so a change of either the row or the code fails).
func parGot(headID string) []tasks.Parallelism {
	switch headID {
	case "metafetch.calibrate-scoring":
		return []tasks.Parallelism{tasks.CPU()}
	case "metafetch.asin-backfill":
		return []tasks.Parallelism{asinBackfillParallelism(asinBackfillWorkers(asinBackfillParams{}))}
	default:
		return []tasks.Parallelism{tasks.Fixed(pcacheSweepResolveConcurrency, "store-bound point-gets; the metadata-cache reaper's resolve figure")}
	}
}

// kindHost is a taskstest.FakeRun with the mode and writer a test chooses
// (FakeRun is pinned to ModeLive with no writer).
type kindHost struct {
	*taskstest.FakeRun
	mode tasks.Mode
	w    tasks.Writer

	mu    sync.Mutex
	notes []string
	logs  bytes.Buffer
	bar   []barFrame
	// declared is every worker count a pool of the run declared (CapWorkers).
	declared []int
}

// barFrame is one progress-bar report: a run's rc.Progress().Set.
type barFrame struct {
	Done, Total int64
	Known       bool
}

func (h *kindHost) ReportProgress(done int64, total tasks.Total) {
	n, known := total.Value()
	h.mu.Lock()
	h.bar = append(h.bar, barFrame{Done: done, Total: n, Known: known})
	h.mu.Unlock()
}

func (h *kindHost) bars() []barFrame {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]barFrame(nil), h.bar...)
}

// Log is a logger over the host's own buffer, so a test can read it.
func (h *kindHost) Log() *slog.Logger {
	return slog.New(slog.NewTextHandler(&lockedWriter{mu: &h.mu, w: &h.logs}, nil))
}

type lockedWriter struct {
	mu *sync.Mutex
	w  *bytes.Buffer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

func (h *kindHost) Note(s string) {
	h.mu.Lock()
	h.notes = append(h.notes, s)
	h.mu.Unlock()
}

func (h *kindHost) noted() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.notes...)
}

// CapWorkers records the declared count and applies the fake's own cap.
func (h *kindHost) CapWorkers(declared int) int {
	h.mu.Lock()
	h.declared = append(h.declared, declared)
	h.mu.Unlock()
	return h.FakeRun.CapWorkers(declared)
}

func (h *kindHost) declaredWorkers() []int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]int(nil), h.declared...)
}

func (h *kindHost) Mode() tasks.Mode     { return h.mode }
func (h *kindHost) Writer() tasks.Writer { return h.w }

// newKindHost is a host in mode with writer w, over a context.
func newKindHost(ctx context.Context, d tasks.Definition, mode tasks.Mode, w tasks.Writer) *kindHost {
	return &kindHost{FakeRun: taskstest.NewFakeRun(ctx, d.Kind()), mode: mode, w: w}
}

// runKind runs d's own Run, the function the executor would call, on a fresh
// host. It returns the host, the result document the run returned and its error.
func runKind(t *testing.T, d tasks.Definition, mode tasks.Mode, w tasks.Writer, params string) (*kindHost, json.RawMessage, error) {
	t.Helper()
	h := newKindHost(context.Background(), d, mode, w)
	out, err := runKindOn(t, d, h, params)
	return h, out, err
}

// runKindOn runs d on h.
func runKindOn(t *testing.T, d tasks.Definition, h *kindHost, params string) (json.RawMessage, error) {
	t.Helper()
	r, ok := d.(interface {
		ExecutorRunners() (func(json.RawMessage) (any, error), *tasks.TaskSpec[any, json.RawMessage], *tasks.BatchSpec[any, any, json.RawMessage], *tasks.FixerSpec[any, any])
	})
	require.True(t, ok, "%T has no ExecutorRunners", d)
	decode, task, _, _ := r.ExecutorRunners()
	require.NotNil(t, task, "%s is not a Task", d.Kind())
	var raw json.RawMessage
	if params != "" {
		raw = json.RawMessage(params)
	}
	p, err := decode(raw)
	if err != nil {
		return nil, err
	}
	return task.Run(tasks.NewRC(h), p)
}

// sentinelWriter is a tasks.Writer only its identity matters for: a probe asserts
// that the writer the deps factory saw is the very writer the run's rc.Writer()
// returned.
type sentinelWriter struct{ tasks.Writer }

// writeOp is one write a core made through its kindWriter.
type writeOp struct {
	Method string
	Key    string
}

// recWriter is the kindWriter fakes use: it records every write and applies it
// to a real store, so a test also sees the effect.
type recWriter struct {
	mu    sync.Mutex
	store *database.PebbleStore
	ops   []writeOp
	// failDelete, when set, fails DeleteRawBatch.
	failDelete error
	// failRecord, when set, fails RecordMetadataChange (the call is still recorded).
	failRecord error
}

func (w *recWriter) rec(method, key string) {
	w.mu.Lock()
	w.ops = append(w.ops, writeOp{method, key})
	w.mu.Unlock()
}

func (w *recWriter) log() []writeOp {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]writeOp(nil), w.ops...)
}

// methods is the sorted distinct set of methods called.
func (w *recWriter) methods() []string {
	seen := map[string]bool{}
	for _, o := range w.log() {
		seen[o.Method] = true
	}
	var out []string
	for m := range seen {
		out = append(out, m)
	}
	sort.Strings(out)
	return out
}

// keysOf is the sorted keys written by method.
func (w *recWriter) keysOf(method string) []string {
	var out []string
	for _, o := range w.log() {
		if o.Method == method {
			out = append(out, o.Key)
		}
	}
	sort.Strings(out)
	return out
}

func (w *recWriter) DeleteRawBatch(keys []string) error {
	for _, k := range keys {
		w.rec("DeleteRawBatch", k)
	}
	if w.failDelete != nil {
		return w.failDelete
	}
	return w.store.DeleteRawBatch(keys)
}

func (w *recWriter) ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error) {
	w.rec("ModifyBook", id)
	return w.store.ModifyBook(id, fn)
}

func (w *recWriter) RecordMetadataChange(rec *database.MetadataChangeRecord) error {
	w.rec("RecordMetadataChange", rec.BookID)
	if w.failRecord != nil {
		return w.failRecord
	}
	return w.store.RecordMetadataChange(rec)
}

func (w *recWriter) SetRawASINMiss(key string, value []byte) error {
	w.rec("SetRawASINMiss", key)
	return w.store.SetRaw(key, value)
}

func (w *recWriter) SetRawPCache(key string, value []byte) error {
	w.rec("SetRawPCache", key)
	return w.store.SetRaw(key, value)
}

var _ kindWriter = (*recWriter)(nil)

// guardStore serves a core's reads from the embedded store and fails the test on
// any write-ish method: the kinds' stores are read-only interfaces, but a core
// that type-asserts a wider store, or a future edit that does, would reach these.
// It satisfies asinBackfillStore and pcacheSweepStore (the HEAD write shapes), so
// such an assertion would succeed and trip the guard.
type guardStore struct {
	kindStore
	t      *testing.T
	writes atomic.Int64
}

func (g *guardStore) trip(method string) error {
	g.writes.Add(1)
	g.t.Errorf("a write bypassed env.Writer: store.%s was called", method)
	return errGuardWrite
}

var errGuardWrite = &guardError{}

type guardError struct{}

func (*guardError) Error() string { return "guard store written" }

func (g *guardStore) ModifyBook(string, func(*database.Book) error) (*database.Book, error) {
	return nil, g.trip("ModifyBook")
}
func (g *guardStore) RecordMetadataChange(*database.MetadataChangeRecord) error {
	return g.trip("RecordMetadataChange")
}
func (g *guardStore) SetRaw(string, []byte) error   { return g.trip("SetRaw") }
func (g *guardStore) DeleteRaw(string) error        { return g.trip("DeleteRaw") }
func (g *guardStore) DeleteRawBatch([]string) error { return g.trip("DeleteRawBatch") }
func (g *guardStore) UpdateBook(string, *database.Book) (*database.Book, error) {
	return nil, g.trip("UpdateBook")
}
func (g *guardStore) CreateBook(*database.Book) (*database.Book, error) {
	return nil, g.trip("CreateBook")
}
func (g *guardStore) DeleteBook(string) error { return g.trip("DeleteBook") }

// fakeKindDeps is the deps the shared tests build the kinds over: nothing in it
// is used by the shape tests, so every field is a recording or fatal fake.
func fakeKindDeps(t *testing.T) KindDeps {
	t.Helper()
	return KindDeps{
		Store: &guardStore{t: t},
		NewWriter: func(tasks.Writer) kindWriter {
			t.Error("NewWriter called by a shape test")
			return nil
		},
		Candidates: nilCandidates{},
		Scoring:    func() config.MetadataScoringConfig { return config.MetadataScoringConfig{} },
		NewAudible: func() audibleIdentitySearcher { return nil },
	}
}

// TestMetafetchKinds_Conformance: taskstest.Conformance over every native kind
// (05-tasks 5T.9 last row).
func TestMetafetchKinds_Conformance(t *testing.T) {
	taskstest.Conformance(t, (&Plugin{}).Kinds(fakeKindDeps(t), nil)...)
}

// TestMetafetchKinds_MatchHEADDefs: every row compares one native kind and the
// HEAD definition it came from with the same literal row; the rows and the list
// are the same set (rule R8).
func TestMetafetchKinds_MatchHEADDefs(t *testing.T) {
	p := &Plugin{}
	defs := p.Kinds(fakeKindDeps(t), nil)
	heads := map[string]sdk.OperationDef{}
	for _, h := range p.OperationDefs() {
		heads[h.ID] = h
	}
	rows := metafetchRows()

	seen := map[tasks.Kind]bool{}
	for _, r := range rows {
		t.Run(r.HeadID, func(t *testing.T) {
			kind := tasks.Kind("books." + r.HeadID)
			require.False(t, seen[kind], "two rows for %s", kind)
			seen[kind] = true
			head, ok := heads[r.HeadID]
			require.True(t, ok, "HEAD has no definition %s", r.HeadID)
			c := tasks.CommonOf(byKind(t, defs, kind))
			_, err := tasks.ParseKind(string(kind))
			require.NoError(t, err)

			require.Equal(t, head.DisplayName, r.Title, "row title is HEAD's DisplayName")
			require.Equal(t, head.Description, c.Help, "Help is HEAD's Description verbatim")
			require.NotEmpty(t, c.Help)
			require.Equal(t, r.Title, c.Title)
			require.Equal(t, r.HeadResume, head.ResumePolicy, "row's HEAD resume is HEAD's")
			require.Equal(t, r.Resume, c.Resume)
			require.Equal(t, r.Priority, c.Priority)
			require.Equal(t, kindHeadPriority(head.DefaultPriority), c.Priority, "HEAD's priority maps to the native one")
			require.Equal(t, head.Timeout, c.Timeout, "timeout is carried unchanged")
			require.Equal(t, r.Timeout, c.Timeout)
			require.False(t, head.NoWarmupWait, "no metafetch definition skips the warmup wait")
			require.Equal(t, []tasks.ReadySignal{"books.memdb"}, c.Ready)
			require.Equal(t, r.HeadKey, head.ConcurrencyKey)
			require.Equal(t, tasks.Key("books."+r.HeadKey), c.ConcurrencyKey)
			require.Equal(t, r.NotWhile, c.NotWhile)
			require.Empty(t, head.Permissions, "no metafetch definition declares Permissions: settings.manage")
			require.Equal(t, rbac.KindAllOf, c.Perms.Start.Kind(), "every metafetch definition is settings.manage")
			require.Equal(t, rbac.Requirement(kindStartRequirement()), c.Perms.Start)
			// The scopes spelled out (internal/books/atoms.go settings.manage), so a
			// drift of the shared list fails here and not only against itself.
			require.Equal(t, rbac.Requirement(rbac.AllOf(
				"core:settings.manage", "books:settings.manage", "core:tasks.manage", "core:system.view",
				"core:system.manage", "core:library.manage", "core:library.browse", "core:integrations.manage",
			)), c.Perms.Start)
			require.Empty(t, c.Perms.StartableByViewers)
			require.Nil(t, c.Schedule, "sched is nil in the test; no conversion declares a schedule itself")

			require.Equal(t, r.ReadOnly, c.Effects.IsReadOnly())
			require.Equal(t, refStrings(r.Writes), refStrings2(c.Effects.WriteSet()), "write set")
			require.Empty(t, c.Effects.ReadSet(), "no declared reads")
			require.Empty(t, c.Effects.DeleteSet(), "no AlsoDeletes: tasklint wants an owner review for each")

			var par []string
			for _, p := range parGot(r.HeadID) {
				par = append(par, p.String())
			}
			require.Equal(t, r.Par, par, "parallelism of the body's pools (rule R4)")
		})
	}
	// The list and the rows are one set.
	var got, want []string
	for _, d := range defs {
		got = append(got, string(d.Kind()))
	}
	for k := range seen {
		want = append(want, string(k))
	}
	sort.Strings(got)
	sort.Strings(want)
	require.Equal(t, want, got, "every native kind has a row and every row a kind")
	require.Len(t, defs, len(rows))
	require.Len(t, p.OperationDefs(), len(rows), "every HEAD definition is converted (3/3)")
}

// TestMetafetchKinds_WritesThroughShell: a Live run of each writing kind hands
// NewWriter that run's rc.Writer(), reaches every write through that writer and
// never through the store (rule R10); a Preview run builds no writer and writes
// nothing; the read-only report never asks for a writer in either mode.
func TestMetafetchKinds_WritesThroughShell(t *testing.T) {
	t.Run("pcache-sweep live", func(t *testing.T) {
		f := newSweepFixture(t)
		g := &guardStore{kindStore: f.wrap, t: t}
		w := &recWriter{store: f.store}
		var seen tasks.Writer
		sent := &sentinelWriter{}
		defs := (&Plugin{}).Kinds(KindDeps{Store: g, NewWriter: func(tw tasks.Writer) kindWriter { seen = tw; return w }}, nil)
		_, _, err := runKind(t, byKind(t, defs, pcacheSweepKind), tasks.ModeLive, sent, "")
		require.NoError(t, err)
		require.Same(t, tasks.Writer(sent), seen, "NewWriter got the run's own rc.Writer()")
		require.Equal(t, []string{"DeleteRawBatch"}, w.methods())
		want := append([]string(nil), f.dead...)
		sort.Strings(want)
		require.Equal(t, want, w.keysOf("DeleteRawBatch"), "exactly the dead rows go through the writer")
		require.Zero(t, g.writes.Load(), "the store was written to")
	})
	t.Run("pcache-sweep preview", func(t *testing.T) {
		f := newSweepFixture(t)
		g := &guardStore{kindStore: f.wrap, t: t}
		defs := (&Plugin{}).Kinds(KindDeps{Store: g, NewWriter: func(tasks.Writer) kindWriter {
			t.Error("a Preview run built a writer")
			return nil
		}}, nil)
		_, out, err := runKind(t, byKind(t, defs, pcacheSweepKind), tasks.ModePreview, &sentinelWriter{}, "")
		require.NoError(t, err)
		var res pcacheSweepResult
		require.NoError(t, json.Unmarshal(out, &res))
		require.True(t, res.DryRun)
		require.Positive(t, res.Expired)
		require.Zero(t, g.writes.Load())
		for _, k := range f.dead {
			require.True(t, f.present(t, k), "a Preview run deleted %s", k)
		}
	})
	t.Run("asin-backfill live", func(t *testing.T) {
		fx := newBatmanASINFixture(t)
		g := &guardStore{kindStore: fx.store, t: t}
		w := &recWriter{store: fx.store}
		var seen tasks.Writer
		sent := &sentinelWriter{}
		defs := (&Plugin{}).Kinds(KindDeps{Store: g, NewWriter: func(tw tasks.Writer) kindWriter { seen = tw; return w },
			NewAudible: func() audibleIdentitySearcher { return fx.audible }}, nil)
		_, _, err := runKind(t, byKind(t, defs, asinBackfillKind), tasks.ModeLive, sent, "")
		require.NoError(t, err)
		require.Same(t, tasks.Writer(sent), seen, "NewWriter got the run's own rc.Writer()")
		require.Equal(t, []string{"ModifyBook", "RecordMetadataChange", "SetRawASINMiss", "SetRawPCache"}, w.methods())
		require.Zero(t, g.writes.Load(), "the store was written to")
	})
	t.Run("asin-backfill preview", func(t *testing.T) {
		fx := newBatmanASINFixture(t)
		g := &guardStore{kindStore: fx.store, t: t}
		defs := (&Plugin{}).Kinds(KindDeps{Store: g, NewWriter: func(tasks.Writer) kindWriter {
			t.Error("a Preview run built a writer")
			return nil
		}, NewAudible: func() audibleIdentitySearcher { return fx.audible }}, nil)
		_, out, err := runKind(t, byKind(t, defs, asinBackfillKind), tasks.ModePreview, &sentinelWriter{}, "")
		require.NoError(t, err)
		var res asinBackfillResult
		require.NoError(t, json.Unmarshal(out, &res))
		require.True(t, res.DryRun)
		require.Zero(t, g.writes.Load())
		require.Empty(t, pcacheKeys(t, fx.store, "asinbackfill:"), "a Preview run left a no-match marker")
		require.Empty(t, pcacheKeys(t, fx.store, database.PCachePrefix), "a Preview run wrote answer-cache rows")
	})
	t.Run("calibrate-scoring never writes", func(t *testing.T) {
		for _, mode := range []tasks.Mode{tasks.ModePreview, tasks.ModeLive} {
			cf := newCalibrateFixture(t)
			g := &guardStore{kindStore: cf.store, t: t}
			defs := (&Plugin{}).Kinds(KindDeps{Store: g, Candidates: cf.candidates, Scoring: cf.scoring, NewWriter: func(tasks.Writer) kindWriter {
				t.Error("the read-only report built a writer")
				return nil
			}}, nil)
			_, _, err := runKind(t, byKind(t, defs, calibrateScoringKind), mode, &sentinelWriter{}, "")
			require.NoError(t, err)
			require.Zero(t, g.writes.Load())
		}
	})
}
