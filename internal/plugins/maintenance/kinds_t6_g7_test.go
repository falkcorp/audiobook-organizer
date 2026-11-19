// file: internal/plugins/maintenance/kinds_t6_g7_test.go
// version: 1.0.3
// guid: 4c7e1a92-6b3d-4f58-8a0e-2d9c5b71e3f6
// last-edited: 2026-11-19

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/operations"
	"github.com/falkcorp/audiobook-organizer/internal/tagger"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/tasks"
	"github.com/falkcorp/mediacore/tasks/taskstest"

	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// t6g7Recorder is the writer a test hands the kinds: it records the calls that
// reach it. It implements every method of t6g7Writer itself (the compiler checks
// that where the probe returns it), so a writer method added to the interface
// breaks this file until the recorder answers it, instead of panicking on a nil
// embedded interface the first time a kind calls it.
type t6g7Recorder struct {
	mu    sync.Mutex
	calls []string
	// reviewIndex is what the status-index rebuild answers.
	reviewIndex database.ReviewStatusIndexRepair
	// books lets ModifyBook run the caller's fn on a copy, so the test sees the
	// row the write would have produced.
	books map[string]*database.Book
	// bulkErr is what RunBulkWriteBack answers.
	bulkErr error
}

// t6g7WriterResource maps every writer method the spy can record to the FULL set
// of Effects resources its call chain writes, as "books/<resource>" refs. It is
// this group's view of t6WriterResources (kinds_resources.go), the one method
// table of every converted group, where each row is traced from the store call
// chain and cited (the CollapseHealDuplicates chain is cited there). A recorded
// call whose method has no row fails the test, and t6g7AssertWritesDeclared
// requires every traced resource to be in the kind's declared write set. SetRaw
// is this group's user_state key family (the pending-repair keys).
//
// The heal's whisper journal (ai_journal) is not a writer-method call the spy
// records (HealWhisperJournal is an accessor the spy answers with nil, and the
// transcription that writes the journal needs ffmpeg and a whisper pool), so it
// is pinned by TestT6G7Kinds_HealWhisperJournalWritesTheDeclaredAIJournalKeys
// (kinds_t6_g6_m5_test.go): the production journal over a real store writes under
// aijournal:, and the heal's Effects declare ai_journal. The heal row of
// TestT6G7Kinds_MatchHEADDefs only compares the declaration with HEAD's.
var t6g7WriterResource = t6g7WriterView()

func t6g7WriterView() map[string][]string {
	out := map[string][]string{}
	for method, row := range t6WriterView(map[string]string{"SetRaw": "SetRaw/user_state"}, reflect.TypeFor[t6g7Writer]()) {
		out[method] = t6g7Eff(row...)
	}
	return out
}

// t6g7Eff is the sorted "books/<resource>" refs of the given pkg/ops constants.
// Expected write sets are spelled with the constants, so a coined resource name
// does not compile.
func t6g7Eff(rs ...ops.Resource) []string {
	out := make([]string, 0, len(rs))
	for _, r := range rs {
		out = append(out, "books/"+string(r))
	}
	sort.Strings(out)
	return out
}

// t6g7AssertWritesDeclared fails when a recorded write names a method with no
// mapped resource, or a resource the kind's declared WriteSet does not hold.
func t6g7AssertWritesDeclared(t *testing.T, calls []string, def tasks.Definition) {
	t.Helper()
	declared := map[string]bool{}
	for _, res := range tasks.CommonOf(def).Effects.WriteSet() {
		declared[tasks.FormatRef(res)] = true
	}
	for _, c := range calls {
		method, _, _ := strings.Cut(c, " ")
		traced, ok := t6g7WriterResource[method]
		require.True(t, ok, "%s: writer method %q has no resource set in t6g7WriterResource", def.Kind(), method)
		// A recorded call is a method the kind's own writer interfaces list: the
		// kind's expected write set is derived from those, so a call outside them
		// would be a write the derivation never counted.
		kw := t6g7KindWriters()[def.Kind()]
		require.Contains(t, t6WriterMethods(kw.writers...), method, "%s: recorded %q, which is not a method of the kind's writer interfaces", def.Kind(), c)
		for _, res := range traced {
			require.True(t, declared[res], "%s: %q writes %s, which its Effects do not declare", def.Kind(), c, res)
		}
	}
}

func (r *t6g7Recorder) rec(s string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, s)
}

func (r *t6g7Recorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.calls...)
}

func (r *t6g7Recorder) RebuildReviewStatusIndex(apply bool) (database.ReviewStatusIndexRepair, error) {
	if apply {
		r.rec("RebuildReviewStatusIndex apply")
	} else {
		r.rec("RebuildReviewStatusIndex dry")
	}
	return r.reviewIndex, nil
}

func (r *t6g7Recorder) RunBulkWriteBack(ctx context.Context, opID string, ids []string, rename bool, start int, progress operations.ProgressReporter) error {
	if opID == "" {
		return errors.New("RunBulkWriteBack got no op id")
	}
	r.rec(fmt.Sprintf("RunBulkWriteBack ids=%v rename=%v start=%d backup=%v", ids, rename, start, tagger.BackupWanted(ctx)))
	_ = progress.UpdateProgress(len(ids), len(ids), "wrote")
	return r.bulkErr
}

func (r *t6g7Recorder) ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error) {
	r.mu.Lock()
	b := r.books[id]
	r.mu.Unlock()
	if b == nil {
		return nil, nil
	}
	cur := *b
	if err := fn(&cur); err != nil {
		if errors.Is(err, database.ErrSkipBookWrite) {
			return &cur, nil
		}
		return nil, err
	}
	r.rec("ModifyBook " + id + " title=" + cur.Title)
	return &cur, nil
}

// t6g7TestStore is the read side: whatever a test does not seed answers empty;
// whatever it does not model panics.
type t6g7TestStore struct {
	t6g7Store

	reviewIndex database.ReviewStatusIndexRepair
	// cores is what GetAllBooksCore answers.
	cores []database.BookCore
	// active are the ids of the operations ListActiveOperationsV2 answers.
	active []string
	// files and states are what GetBookFiles and GetMetadataFieldStates answer.
	files  map[string][]database.BookFile
	states map[string][]database.MetadataFieldState
}

func (s *t6g7TestStore) GetBookFiles(id string) ([]database.BookFile, error) { return s.files[id], nil }
func (s *t6g7TestStore) GetMetadataFieldStates(id string) ([]database.MetadataFieldState, error) {
	return s.states[id], nil
}

func (s *t6g7TestStore) ListActiveOperationsV2() ([]database.OperationV2Row, error) {
	rows := make([]database.OperationV2Row, 0, len(s.active))
	for _, id := range s.active {
		rows = append(rows, database.OperationV2Row{ID: id})
	}
	return rows, nil
}

func (s *t6g7TestStore) CountPrimaryBooks() (int, error) { return len(s.cores), nil }

func (s *t6g7TestStore) GetAllBooksCore(int, int) ([]database.BookCore, error) { return s.cores, nil }
func (s *t6g7TestStore) GetAllBooksCoreComplete(int, int) ([]database.BookCore, error) {
	return s.cores, nil
}

func (s *t6g7TestStore) ReviewStatusIndexReport() (database.ReviewStatusIndexRepair, error) {
	return s.reviewIndex, nil
}

// t6g7Probe records how the native shells called deps.NewWriter.
type t6g7Probe struct {
	mu    sync.Mutex
	calls int
	given tasks.Writer

	rec   *t6g7Recorder
	store t6g7Store
	root  string
}

func newT6G7Probe(t *testing.T) *t6g7Probe {
	t.Helper()
	return &t6g7Probe{rec: &t6g7Recorder{}, store: &t6g7TestStore{}, root: t.TempDir()}
}

func (p *t6g7Probe) deps() t6g7Deps {
	return t6g7Deps{
		Store: p.store,
		NewWriter: func(w tasks.Writer) t6g7Writer {
			p.mu.Lock()
			defer p.mu.Unlock()
			p.calls++
			p.given = w
			return p.rec
		},
		RootDir:      func() string { return p.root },
		StandDownKey: "books.gotham-watch",
		// A directory "agrees" on the title its name carries, so a test seeds the
		// tags by naming the folder.
		AgreedChapterTitle: func(dir string) (string, bool) { return filepath.Base(dir), true },
	}
}

func (p *t6g7Probe) writerCalls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

// fakeT6G7Deps is enough to build the 15 definitions: conformance reads their
// declarations and never runs a body.
func fakeT6G7Deps(t *testing.T) t6g7Deps {
	t.Helper()
	return newT6G7Probe(t).deps()
}

// t6g7GateWriter is a distinguishable tasks.Writer: identity is the assertion.
type t6g7GateWriter struct{ name string }

func (*t6g7GateWriter) Do(context.Context, tasks.Write, func(context.Context) ([]tasks.Change, error)) error {
	return nil
}
func (*t6g7GateWriter) MarkRowApplied(context.Context, string)           {}
func (*t6g7GateWriter) RowApplied(context.Context, string) (bool, error) { return false, nil }

type t6g7TestHold struct{ released *bool }

func (h t6g7TestHold) Release() { *h.released = true }

// t6g7Host is a taskstest.FakeRun with the mode and writer a test chooses
// (FakeRun is pinned to ModeLive with no writer, and refuses a stand-down).
type t6g7Host struct {
	*taskstest.FakeRun
	mode tasks.Mode
	w    tasks.Writer

	mu         sync.Mutex
	standDowns []tasks.Key
	released   bool
	// result is the run's encoded result document.
	result json.RawMessage
	// caps are the worker counts the run's item loops declared (CapWorkers is
	// handed the declared count before every loop).
	caps []int
}

// CapWorkers records the declared count and applies the fake's own cap.
func (h *t6g7Host) CapWorkers(declared int) int {
	h.mu.Lock()
	h.caps = append(h.caps, declared)
	h.mu.Unlock()
	return h.FakeRun.CapWorkers(declared)
}

func (h *t6g7Host) workerCaps() []int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]int(nil), h.caps...)
}

func (h *t6g7Host) Mode() tasks.Mode     { return h.mode }
func (h *t6g7Host) Writer() tasks.Writer { return h.w }
func (h *t6g7Host) Log() *slog.Logger    { return slog.New(slog.DiscardHandler) }
func (h *t6g7Host) StandDown(k tasks.Key) (tasks.Hold, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.standDowns = append(h.standDowns, k)
	return t6g7TestHold{released: &h.released}, nil
}

// runT6G7Kind runs d's own Run, the function the executor would call.
func runT6G7Kind(t *testing.T, d tasks.Definition, mode tasks.Mode, w tasks.Writer, params string) (*t6g7Host, error) {
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
	require.NoError(t, err)
	h := &t6g7Host{FakeRun: taskstest.NewFakeRun(context.Background(), d.Kind()), mode: mode, w: w}
	h.result, err = task.Run(tasks.NewRC(h), p)
	return h, err
}

func t6g7ByKind(t *testing.T, defs []tasks.Definition, kind tasks.Kind) tasks.Definition {
	t.Helper()
	for _, d := range defs {
		if d.Kind() == kind {
			return d
		}
	}
	t.Fatalf("no definition of kind %s", kind)
	return nil
}

func TestT6G7Kinds_Conformance(t *testing.T) {
	taskstest.Conformance(t, (&Plugin{}).t6g7Kinds(fakeT6G7Deps(t), nil)...)
}

// t6g7KindWrite is what one group-7 kind writes: the writer interfaces its core is
// handed (the narrow interface of each definition, composed into t6g7Writer) and
// the resources it writes without a writer call (bytes on disk and the like).
type t6g7KindWrite struct {
	writers []reflect.Type
	outside []ops.Resource
}

// t6g7KindWriters is each kind's writes, keyed by kind. A kind absent from it is
// ReadOnly. The declared write set of a kind is the union of the shared table's
// rows (t6g7WriterResource) for every method of its writer interfaces, plus
// outside: derived by t6g7ExpectedEffects, enforced both ways by
// TestT6G7Kinds_MatchHEADDefs, so a row that widens widens the kind and a kind
// cannot declare a resource none of its methods writes.
var t6g7KindWritersOnce = sync.OnceValue(t6g7KindWritersBuild)

func t6g7KindWriters() map[tasks.Kind]t6g7KindWrite { return t6g7KindWritersOnce() }

func t6g7KindWritersBuild() map[tasks.Kind]t6g7KindWrite {
	p := &Plugin{}
	m := map[tasks.Kind]t6g7KindWrite{}
	add := func(head sdk.OperationDef, kw t6g7KindWrite) { m[tasks.Kind("books."+head.ID)] = kw }
	add(p.reviewStatusIndexRepairDef(), t6g7KindWrite{writers: []reflect.Type{reflect.TypeFor[reviewStatusIndexRepairWriter]()}})
	add(p.bulkWriteBackDef(), t6g7KindWrite{writers: []reflect.Type{reflect.TypeFor[bulkWriteBackWriter]()}})
	add(p.titleRepairDef(), t6g7KindWrite{writers: []reflect.Type{reflect.TypeFor[titleRepairWriter]()}})
	add(p.titleBackfillDef(), t6g7KindWrite{writers: []reflect.Type{reflect.TypeFor[titleBackfillWriter]()}})
	// reports-prune os.Remove-s report files and writes no row: files only.
	add(p.reportsPruneDef(), t6g7KindWrite{outside: []ops.Resource{ops.ResFiles}})
	return m
}

// t6g7ExpectedEffects is the sorted "books/<resource>" write set a kind must
// declare, nil for a ReadOnly kind. A method of a kind's interfaces with no row
// fails the test rather than counting as empty.
func t6g7ExpectedEffects(t *testing.T, kind tasks.Kind) []string {
	t.Helper()
	kw, ok := t6g7KindWriters()[kind]
	if !ok {
		return nil
	}
	set := map[string]bool{}
	for _, m := range t6WriterMethods(kw.writers...) {
		refs, ok := t6g7WriterResource[m]
		require.True(t, ok, "%s: writer method %q has no row in the shared table", kind, m)
		for _, r := range refs {
			set[r] = true
		}
	}
	for _, r := range t6g7Eff(kw.outside...) {
		set[r] = true
	}
	out := make([]string, 0, len(set))
	for r := range set {
		out = append(out, r)
	}
	sort.Strings(out)
	if len(out) == 0 {
		return nil
	}
	return out
}

// TestT6G7Resources_TableCoversEveryWriterMethod: the group's view of the shared
// table names exactly the write methods of t6g7Writer.
func TestT6G7Resources_TableCoversEveryWriterMethod(t *testing.T) {
	methods := t6WriterMethods(reflect.TypeFor[t6g7Writer]())
	rows := make([]string, 0, len(t6g7WriterResource))
	for name := range t6g7WriterResource {
		rows = append(rows, name)
	}
	sort.Strings(rows)
	var missing, extra []string
	for _, m := range methods {
		if !slices.Contains(rows, m) {
			missing = append(missing, m)
		}
	}
	for _, r := range rows {
		if !slices.Contains(methods, r) {
			extra = append(extra, r)
		}
	}
	require.Empty(t, missing, "t6g7Writer methods with no row in the shared table")
	require.Empty(t, extra, "rows in the group's view for a method t6g7Writer does not have")
}

func TestT6G7Kinds_MatchHEADDefs(t *testing.T) {
	p := &Plugin{}
	defs := p.t6g7Kinds(fakeT6G7Deps(t), nil)
	require.Len(t, defs, 7, "grows to 15 as the group's definitions land")

	// Every expected value below is written out by hand from the HEAD def and
	// the coordinator's rulings, never derived from the code under test.
	type row struct {
		head    sdk.OperationDef
		resume  tasks.ResumePolicy
		timeout time.Duration
		// par is the HEAD loop's parallelism, for the kinds whose body runs a pool
		// of its own; nil for a kind with no loop.
		par     *tasks.Parallelism
		wantPar string
	}
	titleRepairPar := titleRepairParallelism()
	rows := []row{
		{head: p.reviewStatusIndexRepairDef(), resume: tasks.ResumeDrop, timeout: 30 * time.Minute},
		{head: p.unknownAuthorAuditDef(), resume: tasks.ResumeDrop, timeout: 30 * time.Minute},
		{head: p.versionGroupPrimaryReportDef(), resume: tasks.ResumeDrop, timeout: 10 * time.Minute},
		{head: p.reportsPruneDef(), resume: tasks.ResumeDrop, timeout: time.Hour},
		{head: p.titleBackfillDef(), resume: tasks.ResumeDrop, timeout: 30 * time.Minute},
		{head: p.bulkWriteBackDef(), resume: tasks.ResumeAsk, timeout: 240 * time.Minute},
		{head: p.titleRepairDef(), resume: tasks.ResumeDrop, timeout: 4 * time.Hour,
			par: &titleRepairPar, wantPar: fmt.Sprintf("fixed(%d): ", max(min(runtime.NumCPU(), 8), 1))},
	}

	seen := map[tasks.Kind]bool{}
	for _, r := range rows {
		id := r.head.ID
		kind := tasks.Kind("books." + id)
		seen[kind] = true
		def := t6g7ByKind(t, defs, kind)
		c := tasks.CommonOf(def)

		require.Equal(t, r.head.DisplayName, c.Title, "%s title", id)
		require.Equal(t, r.head.Description, c.Help, "%s help", id)
		wantPrio := tasks.PriorityNormal
		switch r.head.DefaultPriority {
		case sdk.PriorityLow:
			wantPrio = tasks.PriorityLow
		case sdk.PriorityHigh:
			wantPrio = tasks.PriorityHigh
		}
		require.Equal(t, wantPrio, c.Priority, "%s priority", id)
		require.Equal(t, r.timeout, c.Timeout, "%s timeout", id)
		require.Equal(t, r.head.ProgressTimeout, c.ProgressTimeout, "%s progress timeout", id)
		require.Equal(t, tasks.Key("books."+r.head.ConcurrencyKey), c.ConcurrencyKey, "%s concurrency key", id)
		require.Equal(t, r.resume, c.Resume, "%s resume", id)

		var gotWrites []string
		for _, res := range c.Effects.WriteSet() {
			gotWrites = append(gotWrites, tasks.FormatRef(res))
		}
		sort.Strings(gotWrites)
		// The expected write set is derived, never typed: the union of the shared
		// table's rows for every write method of the kind's writer interfaces, plus
		// what it writes without a writer call (t6g7KindWriters). A kind with
		// neither is ReadOnly.
		if wantWrites := t6g7ExpectedEffects(t, kind); wantWrites == nil {
			require.True(t, c.Effects.IsReadOnly(), "%s must be declared ReadOnly", id)
		} else {
			require.False(t, c.Effects.IsReadOnly(), "%s writes", id)
			require.Equal(t, wantWrites, gotWrites, "%s write set: the union of the rows of every method of its writer interfaces", id)
		}

		if r.par != nil {
			require.True(t, strings.HasPrefix(r.par.String(), r.wantPar), "%s parallelism %q, want prefix %q", id, r.par.String(), r.wantPar)
		}
		require.Contains(t, c.Ready, t6g7SignalMemDB, "%s readiness", id)
		require.Equal(t, rbac.KindAllOf, c.Perms.Start.Kind(), "%s start requirement must be AllOf", id)
		var gotScopes []string
		for _, sc := range c.Perms.Start.Permissions() {
			gotScopes = append(gotScopes, string(sc))
		}
		require.Equal(t, []string{
			"core:settings.manage", "books:settings.manage", "core:tasks.manage", "core:system.view",
			"core:system.manage", "core:library.manage", "core:library.browse", "core:integrations.manage",
		}, gotScopes, "%s start scopes", id)
	}
	require.Len(t, seen, len(defs), "every definition has a row")
}

// t6g7WriteCase is one writing kind's live run.
type t6g7WriteCase struct {
	kind   tasks.Kind
	params string
	// liveOnly kinds refuse a Preview run before they build a writer.
	liveOnly bool
	// setup seeds the probe's store, fakes and files.
	setup func(t *testing.T, p *t6g7Probe)
	// live is every recorded call a Live run makes; preview, a Preview run's.
	live, preview []string
	// standsDown is true when a Live run holds the scan stand-down.
	standsDown bool
}

func t6g7WriteCases() []t6g7WriteCase {
	return []t6g7WriteCase{
		{
			kind:       t6g7KindFor("maintenance.bulk-write-back"),
			params:     `{"book_ids":["01BATBOOK00000000000000K1","01BATBOOK00000000000000K2"],"rename":true,"start_idx":1}`,
			liveOnly:   true,
			standsDown: true,
			setup: func(t *testing.T, p *t6g7Probe) {
				// Bulk write-back keeps no .bak sibling (D69): the ctx the writer gets
				// must opt out even when create_backups is on.
				prev := config.Snapshot()
				config.Mutate(func(c *config.Config) { c.CreateBackups = true })
				t.Cleanup(func() { config.Mutate(func(c *config.Config) { *c = prev }) })
			},
			live: []string{"RunBulkWriteBack ids=[01BATBOOK00000000000000K1 01BATBOOK00000000000000K2] rename=true start=1 backup=false"},
		},
		{
			kind: titleRepairKind,
			setup: func(t *testing.T, p *t6g7Probe) {
				id := "01BATBOOK00000000000000J1"
				p.store = &t6g7TestStore{
					cores: []database.BookCore{{ID: id, Title: "Chapter 01"}},
					files: map[string][]database.BookFile{id: {
						{BookID: id, FilePath: "/lib/The Long Halloween/01.mp3"},
						{BookID: id, FilePath: "/lib/The Long Halloween/02.mp3"},
					}},
				}
				p.rec.books = map[string]*database.Book{id: {ID: id, Title: "Chapter 01"}}
			},
			live: []string{"ModifyBook 01BATBOOK00000000000000J1 title=The Long Halloween"},
		},
		{
			kind: titleBackfillKind,
			setup: func(t *testing.T, p *t6g7Probe) {
				p.store = &t6g7TestStore{cores: []database.BookCore{
					{ID: "01BATBOOK00000000000000H1", Title: "(76/85) Tarkin"},
					{ID: "01BATBOOK00000000000000H2", Title: "Year One"},
				}}
				p.rec.books = map[string]*database.Book{
					"01BATBOOK00000000000000H1": {ID: "01BATBOOK00000000000000H1", Title: "(76/85) Tarkin"},
				}
			},
			live: []string{"ModifyBook 01BATBOOK00000000000000H1 title=Tarkin"},
		},
		{
			kind: reviewStatusIndexRepairKind,
			// A Live run rebuilds through the gated writer; a Preview run reads the
			// report through the store and writes nothing.
			live: []string{"RebuildReviewStatusIndex apply"},
		},
	}
}

func TestT6G7Kinds_WritesThroughShell(t *testing.T) {
	gate := &t6g7GateWriter{name: "gotham"}

	for _, c := range t6g7WriteCases() {
		t.Run(string(c.kind), func(t *testing.T) {
			// Live: every write goes through the writer NewWriter built over the
			// run's own writer.
			p := newT6G7Probe(t)
			if c.setup != nil {
				c.setup(t, p)
			}
			defs := (&Plugin{}).t6g7Kinds(p.deps(), nil)
			def := t6g7ByKind(t, defs, c.kind)

			h, err := runT6G7Kind(t, def, tasks.ModeLive, gate, c.params)
			require.NoError(t, err)
			require.Equal(t, 1, p.writerCalls(), "NewWriter calls")
			require.Same(t, gate, p.given, "NewWriter must be given the run's writer")
			require.Equal(t, c.live, p.rec.snapshot(), "writes in a live run")
			t6g7AssertWritesDeclared(t, p.rec.snapshot(), def)
			if c.standsDown {
				require.Equal(t, []tasks.Key{"books.gotham-watch"}, h.standDowns, "scan stand-down")
				require.True(t, h.released, "stand-down released")
			} else {
				require.Empty(t, h.standDowns)
			}

			// Preview: a live-only kind refuses before it builds a writer; every
			// other kind reports without writing.
			p = newT6G7Probe(t)
			if c.setup != nil {
				c.setup(t, p)
			}
			defs = (&Plugin{}).t6g7Kinds(p.deps(), nil)
			def = t6g7ByKind(t, defs, c.kind)
			_, err = runT6G7Kind(t, def, tasks.ModePreview, nil, c.params)
			if c.liveOnly {
				require.Error(t, err)
				require.Contains(t, err.Error(), "preview not supported")
				require.Zero(t, p.writerCalls(), "a refused preview built a writer")
				require.Empty(t, p.rec.snapshot())
				return
			}
			require.NoError(t, err)
			require.Equal(t, c.preview, p.rec.snapshot(), "calls in a preview run")
			require.Zero(t, p.writerCalls(), "a preview built a writer")
		})
	}
}

// Every native shell that writes refuses a mode key in its params, whatever its
// case, before it builds a writer or touches the store: the run's mode comes
// from the start request, never from params (coordinator ruling 3).
func TestT6G7Kinds_WritingShellsRefuseModeKeys(t *testing.T) {
	kinds := []tasks.Kind{reviewStatusIndexRepairKind, reportsPruneKind, titleBackfillKind, titleRepairKind, t6g7KindFor("maintenance.bulk-write-back")}
	keys := []string{"dry_run", "dryRun", "DRY_RUN", "DryRun", "apply", "Apply", "APPLY"}
	gate := &t6g7GateWriter{name: "gotham"}
	for _, kind := range kinds {
		for _, key := range keys {
			t.Run(string(kind)+"/"+key, func(t *testing.T) {
				p := newT6G7Probe(t)
				defs := (&Plugin{}).t6g7Kinds(p.deps(), nil)
				_, err := runT6G7Kind(t, t6g7ByKind(t, defs, kind), tasks.ModeLive, gate, `{"`+key+`": true}`)
				require.Error(t, err)
				require.Contains(t, err.Error(), "mode comes from the start request")
				require.Zero(t, p.writerCalls(), "a writer was built")
				require.Empty(t, p.rec.snapshot(), "something was written")
			})
		}
	}
}

// The census is ReadOnly: it runs the same in Preview and Live, builds no writer,
// and its result is the report HEAD logged.
func TestT6G7Kinds_UnknownAuthorAuditReportsAndWritesNothing(t *testing.T) {
	for _, mode := range []tasks.Mode{tasks.ModePreview, tasks.ModeLive} {
		p := newT6G7Probe(t)
		p.store = &t6g7TestStore{cores: []database.BookCore{
			{ID: "01BATBOOK00000000000000F1", Title: "Year One", FilePath: "/lib/Unknown Author/Year One/01.m4b"},
			{ID: "01BATBOOK00000000000000F2", Title: "Dark Victory", FilePath: "/lib/Bruce Wayne/Dark Victory/01.m4b"},
			{ID: "01BATBOOK00000000000000F3", Title: "Hush"},
		}}
		def := t6g7ByKind(t, (&Plugin{}).t6g7Kinds(p.deps(), nil), unknownAuthorAuditKind)
		require.True(t, tasks.CommonOf(def).Effects.IsReadOnly())
		h, err := runT6G7Kind(t, def, mode, nil, `{"sample_limit": 5}`)
		require.NoError(t, err, "%v", mode)
		var rep unknownAuthorAuditReport
		require.NoError(t, json.Unmarshal(h.result, &rep))
		require.Equal(t, 3, rep.TotalBooks)
		require.Equal(t, 1, rep.EmptyFilePath)
		require.Equal(t, 1, rep.PlaceholderInPath)
		require.Equal(t, []unknownAuthorAuditSample{{
			BookID: "01BATBOOK00000000000000F1", Title: "Year One", FilePath: "/lib/Unknown Author/Year One/01.m4b",
		}}, rep.Sample)
		require.Zero(t, p.writerCalls(), "a ReadOnly kind built a writer")
		require.Empty(t, p.rec.snapshot())
	}
}

// The primary census is ReadOnly: Preview and Live are the same, no writer is
// built, and the result is the report HEAD logged (a double of two explicit
// trues, a double that is only a nil flag, and a group with no primary).
func TestT6G7Kinds_VersionGroupPrimaryReportCountsAndWritesNothing(t *testing.T) {
	yes, no := true, false
	vg := func(s string) *string { return &s }
	for _, mode := range []tasks.Mode{tasks.ModePreview, tasks.ModeLive} {
		p := newT6G7Probe(t)
		p.store = &t6g7TestStore{cores: []database.BookCore{
			{ID: "01BATBOOK00000000000000G1", VersionGroupID: vg("vg-explicit"), IsPrimaryVersion: &yes},
			{ID: "01BATBOOK00000000000000G2", VersionGroupID: vg("vg-explicit"), IsPrimaryVersion: &yes},
			{ID: "01BATBOOK00000000000000G3", VersionGroupID: vg("vg-nil"), IsPrimaryVersion: &yes},
			{ID: "01BATBOOK00000000000000G4", VersionGroupID: vg("vg-nil")},
			{ID: "01BATBOOK00000000000000G5", VersionGroupID: vg("vg-none"), IsPrimaryVersion: &no},
			{ID: "01BATBOOK00000000000000G6", VersionGroupID: vg("vg-fine"), IsPrimaryVersion: &yes},
			{ID: "01BATBOOK00000000000000G7"},
		}}
		def := t6g7ByKind(t, (&Plugin{}).t6g7Kinds(p.deps(), nil), versionGroupPrimaryReportKind)
		require.True(t, tasks.CommonOf(def).Effects.IsReadOnly())
		h, err := runT6G7Kind(t, def, mode, nil, "")
		require.NoError(t, err, "%v", mode)
		var rep vgPrimaryReport
		require.NoError(t, json.Unmarshal(h.result, &rep))
		require.Equal(t, 7, rep.TotalBooks)
		require.Equal(t, 4, rep.Groups)
		require.Equal(t, 2, rep.DoubleGroups)
		require.Equal(t, 1, rep.DoubleExplicit)
		require.Equal(t, 1, rep.DoubleNilOnly)
		require.Equal(t, 4, rep.BooksInDoubles)
		require.Equal(t, 1, rep.ZeroPrimary)
		require.Len(t, rep.Sample, 2)
		require.Zero(t, p.writerCalls(), "a ReadOnly kind built a writer")
		require.Empty(t, p.rec.snapshot())
	}
}

// reports-prune deletes files from disk through no writer: Live removes the
// stale report and leaves the live run's, Preview counts and removes nothing, the
// retention floors refuse, and a missing queue refuses before anything goes.
func TestT6G7Kinds_ReportsPruneRemovesOnlyWhenLive(t *testing.T) {
	run := func(t *testing.T, mode tasks.Mode, params string, active ...string) (root, doomed string, res ReportsPruneResult, err error) {
		root, doomed = seedOpFixture(t)
		p := newT6G7Probe(t)
		p.root = root
		p.store = &t6g7TestStore{active: active}
		def := t6g7ByKind(t, (&Plugin{}).t6g7Kinds(p.deps(), nil), reportsPruneKind)
		h, err := runT6G7Kind(t, def, mode, nil, params)
		if h != nil && len(h.result) > 0 {
			require.NoError(t, json.Unmarshal(h.result, &res))
		}
		require.Zero(t, p.writerCalls(), "reports-prune writes through no writer")
		return root, doomed, res, err
	}

	_, doomed, res, err := run(t, tasks.ModeLive, `{}`)
	require.NoError(t, err)
	require.False(t, exists(doomed), "a live run removes the stale report")
	require.False(t, res.DryRun)
	require.Equal(t, 1, res.Removed)
	require.EqualValues(t, 4, res.BytesFreed)

	_, doomed, res, err = run(t, tasks.ModePreview, `{}`)
	require.NoError(t, err)
	require.True(t, exists(doomed), "a preview removed a report")
	require.True(t, res.DryRun)
	require.Equal(t, 1, res.Removed, "a preview counts what a live run would remove")

	_, doomed, res, err = run(t, tasks.ModeLive, `{}`, "4")
	require.NoError(t, err)
	require.True(t, exists(doomed), "the report of a running operation was removed")
	require.Equal(t, 1, res.SkippedLive)

	_, doomed, _, err = run(t, tasks.ModeLive, `{"keep_per_kind": 0}`)
	require.ErrorContains(t, err, "keep_per_kind must be at least 1")
	require.True(t, exists(doomed))
	_, doomed, _, err = run(t, tasks.ModeLive, `{"max_age_days": 0}`)
	require.ErrorContains(t, err, "max_age_days must be between 1 and")
	require.True(t, exists(doomed))

	// No queue to ask which operations run: refuse, remove nothing.
	root, doomed := seedOpFixture(t)
	p := newT6G7Probe(t)
	p.root = root
	deps := p.deps()
	deps.Store = nil
	def := t6g7ByKind(t, (&Plugin{}).t6g7Kinds(deps, nil), reportsPruneKind)
	_, err = runT6G7Kind(t, def, tasks.ModeLive, nil, `{}`)
	require.ErrorContains(t, err, "cannot verify which operations are running")
	require.True(t, exists(doomed))
}

// title-backfill writes only a title that is still the one it derived the new
// title from: a book retitled meanwhile is left alone and counted, not failed.
func TestT6G7Kinds_TitleBackfillLeavesATitleChangedUnderneath(t *testing.T) {
	p := newT6G7Probe(t)
	p.store = &t6g7TestStore{cores: []database.BookCore{{ID: "01BATBOOK00000000000000H3", Title: "(1/9) Hush"}}}
	p.rec.books = map[string]*database.Book{"01BATBOOK00000000000000H3": {ID: "01BATBOOK00000000000000H3", Title: "Hush (curated)"}}
	def := t6g7ByKind(t, (&Plugin{}).t6g7Kinds(p.deps(), nil), titleBackfillKind)
	_, err := runT6G7Kind(t, def, tasks.ModeLive, &t6g7GateWriter{name: "gotham"}, "")
	require.NoError(t, err)
	require.Empty(t, p.rec.snapshot(), "a title changed underneath was overwritten")
}

// No books, no hold: HEAD returned before taking the scan stand-down, so a run
// with nothing to write never parks the scan.
func TestT6G7Kinds_BulkWriteBackWithNoBooksTakesNoHold(t *testing.T) {
	p := newT6G7Probe(t)
	def := t6g7ByKind(t, (&Plugin{}).t6g7Kinds(p.deps(), nil), t6g7KindFor("maintenance.bulk-write-back"))
	h, err := runT6G7Kind(t, def, tasks.ModeLive, &t6g7GateWriter{name: "gotham"}, `{"book_ids":[]}`)
	require.NoError(t, err)
	require.Empty(t, h.standDowns, "an empty run parked the scan")
	require.Empty(t, p.rec.snapshot())
}

// A failed write releases the hold, and the hold's Finish decides the error the
// run ends with (a lost lease turns a clean pass into a failure).
func TestBulkWriteBackCore_FinishDecidesTheError(t *testing.T) {
	var finished int
	env0 := t6g7Env{OpID: "op-1", Log: func(slog.Level, string, ...slog.Attr) {}, Progress: func(int, int, string) {}}
	hold := func(ctx context.Context, _ string) (t6g7Held, error) {
		return t6g7Held{Ctx: ctx, Progress: t6g7OpsProgress{env: env0}, Finish: func(err error) error {
			finished++
			if err == nil {
				return errors.New("scan stand-down lease lost")
			}
			return err
		}}, nil
	}
	rec := &t6g7Recorder{}
	env := bulkWriteBackEnv{t6g7Env: env0, Writer: rec, Hold: hold}
	err := bulkWriteBackCore(context.Background(), BulkWriteBackParams{BookIDs: []string{"b"}}, env)
	require.ErrorContains(t, err, "lease lost")
	require.Equal(t, 1, finished)

	rec.bulkErr = errors.New("disk full")
	err = bulkWriteBackCore(context.Background(), BulkWriteBackParams{BookIDs: []string{"b"}}, env)
	require.ErrorContains(t, err, "disk full")
	require.Equal(t, 2, finished, "a failed write did not release the hold")
}
