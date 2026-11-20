// file: internal/plugins/dedup/kinds_test.go
// version: 1.2.2
// guid: 74d898af-e4c2-4b28-9fc7-5a2b749513d4
// last-edited: 2026-11-19

package dedup

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/tasks"
	"github.com/falkcorp/mediacore/tasks/taskstest"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// F5-04's shared test kit. Each group's test file (kinds_<group>_test.go) owns
// its fakes and supplies three things to the four shared tests below:
//
//	<group>FakeDeps(t)      its XKindDeps over recording fakes
//	<group>Rows()           its kindRow literals (rule R8's table)
//	<group>WriteProbes(t)   one writeProbe per kind that declares writes (rule R10)
//
// The shared tests are exactly four top-level TestDedupKinds_ functions, because
// the brief's acceptance counts them; a group's own tests use another prefix.

// fakeKindDeps is every group's fake deps.
func fakeKindDeps(t *testing.T) KindDeps {
	t.Helper()
	return KindDeps{
		Scoring:   scoringFakeDeps(t),
		Embedding: embeddingFakeDeps(t),
		Labels:    labelFakeDeps(t),
		Index:     indexFakeDeps(t),
		Chain:     chainFakeDeps(t),
	}
}

// kindResumeFor is rule R5's ResumePolicy table, as the coordinator's cross-row
// resume ruling (RULINGS-common.md:6) refines it, written out once here so a
// definition that drifts from it fails whatever its row says. A v2 Restart is
// Continue only when HEAD reads its checkpoint on restart (readsCheckpoint);
// otherwise it is FromZero.
func kindResumeFor(p sdk.ResumePolicy, readsCheckpoint bool) tasks.ResumePolicy {
	switch p {
	case sdk.ResumeRestart:
		if readsCheckpoint {
			return tasks.ResumeContinue
		}
		return tasks.ResumeFromZero
	case sdk.ResumeRequeue:
		return tasks.ResumeFromZero
	case sdk.ResumeDrop:
		return tasks.ResumeDrop
	case sdk.ResumeAsk:
		return tasks.ResumeAsk
	}
	return 0
}

// kindRow is one definition's expected shape, spelled out literally (rule R8):
// the native kind is compared with this and HEAD's definition is compared with
// the same row, so a drift of either fails.
type kindRow struct {
	// HeadID is the HEAD def ID ("dedup.purge-stale"); the native kind is
	// "books." + HeadID (rule R3).
	HeadID string
	// Title is HEAD's DisplayName, Help its Description (rule R5: verbatim).
	Title string
	// HeadResume is HEAD's ResumePolicy; Resume the native one, which must be
	// kindResumeFor(HeadResume) (rule R5: Restart Continue, Requeue FromZero, Drop
	// Drop, Ask Ask).
	HeadResume sdk.ResumePolicy
	Resume     tasks.ResumePolicy
	// HeadReadsCheckpoint is set when HEAD's Run reads its checkpoint on restart.
	// Only then does a HEAD Restart map to Continue (RULINGS-common.md:6).
	HeadReadsCheckpoint bool
	// Priority is the native priority; HEAD's DefaultPriority must map to it.
	Priority tasks.Priority
	// Timeout is HEAD's Timeout, carried unchanged.
	Timeout time.Duration
	// NoWarmup is HEAD's NoWarmupWait: the native Ready is then empty.
	NoWarmup bool
	// Writes and Reads are the native Effects, as pkg/ops constants (sorted).
	// ReadOnly means tasks.ReadOnly: no write set.
	Writes   []ops.Resource
	Reads    []ops.Resource
	ReadOnly bool
	// Par is the parallelism String() of each pool the body runs (rule R4), in
	// body order; ParGot is the same list read from the code's own parallelism
	// functions, so a change of either fails. Empty for a body with no pool.
	Par    []string
	ParGot []tasks.Parallelism
	// HeadKey is HEAD's ConcurrencyKey; the native key is "books." + HeadKey, or
	// zero when HEAD's is empty (rule R5).
	HeadKey string
	// NotWhile is the native NotWhile list, from HEAD's DependsOn.
	NotWhile []tasks.Kind
}

// kindHeadPriority maps HEAD's three priorities onto the native ones.
func kindHeadPriority(p registry.Priority) tasks.Priority {
	switch p {
	case registry.PriorityHigh:
		return tasks.PriorityHigh
	case registry.PriorityNormal:
		return tasks.PriorityNormal
	default: // registry.PriorityLow is the zero value: a def that sets none is Low
		return tasks.PriorityLow
	}
}

// allRows is every group's rows.
func allRows() []kindRow {
	var rows []kindRow
	rows = append(rows, scoringRows()...)
	rows = append(rows, embeddingRows()...)
	rows = append(rows, labelRows()...)
	rows = append(rows, indexRows()...)
	rows = append(rows, chainRows()...)
	return rows
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

// TestDedupKinds_Conformance: taskstest.Conformance over every native kind
// (05-tasks 5T.9 last row).
func TestDedupKinds_Conformance(t *testing.T) {
	taskstest.Conformance(t, (&Plugin{}).Kinds(fakeKindDeps(t), nil)...)
}

// TestDedupKinds_MatchHEADDefs: every row compares one native kind and the HEAD
// definition it came from with the same literal row; the rows and the list are
// the same set (rule R8). The full 32 of the brief are 31 here: dedup.check-book
// needs core's Requires, which core/tasks does not have at this base (seams.d).
func TestDedupKinds_MatchHEADDefs(t *testing.T) {
	p := &Plugin{}
	defs := p.Kinds(fakeKindDeps(t), nil)
	heads := map[string]sdk.OperationDef{}
	for _, h := range p.OperationDefs() {
		heads[h.ID] = h
	}
	rows := allRows()

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
			require.Equal(t, r.Title, c.Title)
			require.Equal(t, r.HeadResume, head.ResumePolicy, "row's HEAD resume is HEAD's")
			require.Equal(t, r.Resume, c.Resume)
			require.Equal(t, kindResumeFor(head.ResumePolicy, r.HeadReadsCheckpoint), c.Resume, "HEAD's resume policy maps to the native one by rule R5 and the cross-row resume ruling")
			require.Equal(t, r.Priority, c.Priority)
			require.Equal(t, kindHeadPriority(head.DefaultPriority), c.Priority, "HEAD's priority maps to the native one")
			require.Equal(t, head.Timeout, c.Timeout, "timeout is carried unchanged")
			require.Equal(t, r.Timeout, c.Timeout)
			require.Equal(t, r.NoWarmup, head.NoWarmupWait)
			if r.NoWarmup {
				require.Empty(t, c.Ready)
			} else {
				require.Equal(t, []tasks.ReadySignal{"books.memdb"}, c.Ready)
			}
			require.Equal(t, r.HeadKey, head.ConcurrencyKey)
			if r.HeadKey == "" {
				require.Empty(t, c.ConcurrencyKey)
			} else {
				require.Equal(t, tasks.Key("books."+r.HeadKey), c.ConcurrencyKey)
			}
			require.Equal(t, r.NotWhile, c.NotWhile)
			require.Equal(t, rbac.KindAllOf, c.Perms.Start.Kind(), "every dedup definition is settings.manage")
			require.Equal(t, rbac.Requirement(kindStartRequirement()), c.Perms.Start)
			require.Empty(t, c.Perms.StartableByViewers)
			require.Nil(t, c.Schedule, "sched is nil in the test; no conversion declares a schedule itself")

			require.Equal(t, r.ReadOnly, c.Effects.IsReadOnly())
			require.Equal(t, refStrings(r.Writes), refStrings2(c.Effects.WriteSet()), "write set")
			require.Equal(t, refStrings(r.Reads), refStrings2(c.Effects.ReadSet()), "read set")
			require.Empty(t, c.Effects.DeleteSet(), "no AlsoDeletes: tasklint wants an owner review for each")

			require.Equal(t, r.Par, parStrings(r.ParGot), "parallelism of the body's pools (rule R4)")
		})
	}
	// The list and the rows are one set.
	var got []string
	for _, d := range defs {
		got = append(got, string(d.Kind()))
	}
	var want []string
	for k := range seen {
		want = append(want, string(k))
	}
	sort.Strings(got)
	sort.Strings(want)
	require.Equal(t, want, got, "every native kind has a row and every row a kind")
	require.Len(t, defs, len(rows))
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

func parStrings(ps []tasks.Parallelism) []string {
	var out []string
	for _, p := range ps {
		out = append(out, p.String())
	}
	return out
}

// kindHost is a taskstest.FakeRun with the mode, writer and child-run behaviour a
// test chooses (FakeRun is pinned to ModeLive with no writer and refuses
// Enqueue and Await).
type kindHost struct {
	*taskstest.FakeRun
	mode tasks.Mode
	w    tasks.Writer

	mu       sync.Mutex
	enqueued []enqueuedRun
	// onAwait, when set, answers rc.Await; nil answers a succeeded outcome.
	onAwait func(ref tasks.RunRef) (tasks.Outcome, error)
	// enqueueErr, when set, fails rc.Enqueue.
	enqueueErr error
	// emptyRunID, when set, makes rc.Enqueue return a ref with no id.
	emptyRunID bool
	notes      []string
	awaited    []string
	logs       bytes.Buffer
	bar        []barFrame
	// capObserver, when set, sees every worker count a run's pool declared.
	capObserver func(declared int)
}

// barFrame is one progress-bar report: a run's rc.Progress().Set.
type barFrame struct {
	Done, Total int64
	Known       bool
}

// ReportProgress records the bar frames (FakeRun drops them).
func (h *kindHost) ReportProgress(done int64, total tasks.Total) {
	n, known := total.Value()
	h.mu.Lock()
	h.bar = append(h.bar, barFrame{Done: done, Total: n, Known: known})
	h.mu.Unlock()
}

// bars is every bar frame the run reported, in order.
func (h *kindHost) bars() []barFrame {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]barFrame(nil), h.bar...)
}

// Log is a logger over the host's own buffer (FakeRun discards), so a test can
// read what the run logged with logged().
func (h *kindHost) Log() *slog.Logger {
	return slog.New(slog.NewTextHandler(&lockedWriter{mu: &h.mu, w: &h.logs}, nil))
}

// logged is everything the run logged.
func (h *kindHost) logged() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.logs.String()
}

// lockedWriter serialises writes to the host's log buffer.
type lockedWriter struct {
	mu *sync.Mutex
	w  *bytes.Buffer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// Note records the run's progress notes (FakeRun drops them).
func (h *kindHost) Note(s string) {
	h.mu.Lock()
	h.notes = append(h.notes, s)
	h.mu.Unlock()
}

// allNotes is every progress note, in order.
func (h *kindHost) allNotes() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.notes...)
}

// lastNote is the last progress note, or "".
func (h *kindHost) lastNote() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.notes) == 0 {
		return ""
	}
	return h.notes[len(h.notes)-1]
}

// CapWorkers records the declared count and applies the fake's own cap.
func (h *kindHost) CapWorkers(declared int) int {
	if h.capObserver != nil {
		h.capObserver(declared)
	}
	return h.FakeRun.CapWorkers(declared)
}

// enqueuedRun is one rc.Enqueue a native run made.
type enqueuedRun struct {
	Kind   tasks.Kind
	Params json.RawMessage
	// Mode is what the start options stated for the child (ModeRequestUnset when
	// none did, which the kernel resolves to the parent's mode).
	Mode tasks.ModeRequest
	// ID is the run id the host answered.
	ID string
}

func (h *kindHost) Mode() tasks.Mode     { return h.mode }
func (h *kindHost) Writer() tasks.Writer { return h.w }

func (h *kindHost) Enqueue(k tasks.Kind, params any, opts ...tasks.StartOption) (tasks.RunRef, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.enqueueErr != nil {
		return tasks.RunRef{}, h.enqueueErr
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return tasks.RunRef{}, err
	}
	var req tasks.StartRequest
	for _, o := range opts {
		o(&req)
	}
	id := fmt.Sprintf("child-%d", len(h.enqueued)+1)
	if h.emptyRunID {
		return tasks.RunRef{}, nil
	}
	h.enqueued = append(h.enqueued, enqueuedRun{Kind: k, Params: raw, Mode: req.Mode, ID: id})
	return tasks.RunRef{ID: tasks.RunID(id)}, nil
}

func (h *kindHost) Await(ref tasks.RunRef) (tasks.Outcome, error) {
	h.mu.Lock()
	h.awaited = append(h.awaited, string(ref.ID))
	fn := h.onAwait
	h.mu.Unlock()
	if fn != nil {
		return fn(ref)
	}
	return tasks.Outcome{State: tasks.StateSucceeded}, nil
}

// awaitedIDs are the run ids the run waited on, in order.
func (h *kindHost) awaitedIDs() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.awaited)
}

func (h *kindHost) enqueuedRuns() []enqueuedRun {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.enqueued)
}

// newKindHost is a host in mode with writer w.
func newKindHost(d tasks.Definition, mode tasks.Mode, w tasks.Writer) *kindHost {
	return &kindHost{FakeRun: taskstest.NewFakeRun(context.Background(), d.Kind()), mode: mode, w: w}
}

// runKind runs d's own Run, the function the executor would call, on a fresh
// host. The host comes back with any error the run returned.
func runKind(t *testing.T, d tasks.Definition, mode tasks.Mode, w tasks.Writer, params string) (*kindHost, error) {
	t.Helper()
	h := newKindHost(d, mode, w)
	return h, runKindOn(t, d, h, params)
}

// runKindOn runs d on h.
func runKindOn(t *testing.T, d tasks.Definition, h *kindHost, params string) error {
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
		return err
	}
	_, err = task.Run(tasks.NewRC(h), p)
	return err
}

// sentinelWriter is a tasks.Writer only its identity matters for: a probe asserts
// that the writer the deps factory saw is the very writer the run's rc.Writer()
// returned.
type sentinelWriter struct{ tasks.Writer }

// writeProbe is one Live run of a kind that declares writes (rule R10).
type writeProbe struct {
	Kind   tasks.Kind
	Def    tasks.Definition
	Params string
	// Writers are the tasks.Writer values the deps factory (NewWriter or
	// NewEngine) was handed, in order.
	Writers func() []tasks.Writer
	// Calls are the writes that reached the recording writer or engine.
	Calls func() []string
}

// allProbes is every group's probes.
func allProbes(t *testing.T) []writeProbe {
	var ps []writeProbe
	ps = append(ps, scoringWriteProbes(t)...)
	ps = append(ps, embeddingWriteProbes(t)...)
	ps = append(ps, labelWriteProbes(t)...)
	ps = append(ps, indexWriteProbes(t)...)
	ps = append(ps, chainWriteProbes(t)...)
	return ps
}

// TestDedupKinds_WritesThroughShell: for every kind whose Effects declare
// writes, one ModeLive run on a fake host shows the deps factory called with that
// run's rc.Writer() and a write reaching the recording writer (rule R10).
func TestDedupKinds_WritesThroughShell(t *testing.T) {
	defs := (&Plugin{}).Kinds(fakeKindDeps(t), nil)
	probed := map[tasks.Kind]bool{}
	writerOracle := oracleWriters(t)
	usedWriterWords := map[string]bool{}
	for _, pr := range allProbes(t) {
		t.Run(string(pr.Kind), func(t *testing.T) {
			require.False(t, probed[pr.Kind], "two probes for %s", pr.Kind)
			probed[pr.Kind] = true
			w := &sentinelWriter{}
			_, err := runKind(t, pr.Def, tasks.ModeLive, w, pr.Params)
			require.NoError(t, err)
			ws := pr.Writers()
			require.NotEmpty(t, ws, "the deps factory was never called with the run's writer")
			for _, got := range ws {
				require.Same(t, w, got, "the factory must be handed this run's rc.Writer()")
			}
			require.NotEmpty(t, pr.Calls(), "no write reached the recording writer")
			// The oracle (kinds_oracle_test.go): what the run actually wrote is
			// inside what the kind declares.
			observed, words := observedWrites(t, pr, writerOracle)
			requireDeclaredCoversObserved(t, pr.Def, pr.Kind, observed)
			for _, w := range words {
				usedWriterWords[w] = true
			}
		})
	}
	for word := range writerOracle {
		require.True(t, usedWriterWords[word], "oracleWriters entry %q is recorded by no probe: stale", word)
	}
	// Every kind that declares writes has a probe, and no read-only kind has one.
	for _, d := range defs {
		c := tasks.CommonOf(d)
		if c.Effects.IsReadOnly() {
			require.False(t, probed[d.Kind()], "%s is ReadOnly but has a write probe", d.Kind())
			continue
		}
		require.True(t, probed[d.Kind()], "%s declares writes but has no write probe", d.Kind())
	}
}

// TestDedupKinds_ChildWaitsUseAwait: the native shells of the child-waiting
// definitions call rc.Enqueue then rc.Await on a native books.dedup.* kind
// (05-tasks 5T.8.1 childop row); HEAD's shells keep childop.
func TestDedupKinds_ChildWaitsUseAwait(t *testing.T) {
	chainChildWaitChecks(t)
}

var _ = strings.Contains
