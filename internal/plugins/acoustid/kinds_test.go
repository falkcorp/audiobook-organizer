// file: internal/plugins/acoustid/kinds_test.go
// version: 1.0.1
// guid: 7fde7363-424f-4234-80ee-914af5f156bd
// last-edited: 2026-11-19

package acoustid

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
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

	"github.com/falkcorp/audiobook-organizer/internal/acoustid"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/fingerprint"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// F5-05's shared test kit for the acoustid kinds. Three top-level
// TestAcoustidKinds_ functions use it (the brief's acceptance counts them);
// every other test in the package's new files is TestAcoustidEnv_ or
// TestAcoustidCore_.

// ---- recording fakes ----

// recWriter is the kindWriter a native run is handed: it records every write and
// writes nothing (the store fake t.Fatals on a write, so a write that goes round
// it is a failure, rule R10).
type recWriter struct {
	mu       sync.Mutex
	calls    []string
	resetter fingerprintResetter
}

func (w *recWriter) log(format string, a ...any) {
	w.mu.Lock()
	w.calls = append(w.calls, fmt.Sprintf(format, a...))
	w.mu.Unlock()
}

func (w *recWriter) logged() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.calls)
}

func (w *recWriter) UpdateBookFile(id string, _ *database.BookFile) error {
	w.log("UpdateBookFile:%s", id)
	return nil
}

func (w *recWriter) ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error) {
	w.log("ModifyBook:%s", id)
	b := &database.Book{ID: id}
	return b, fn(b)
}

func (w *recWriter) ClearBookSignature(id string) error {
	w.log("ClearBookSignature:%s", id)
	return nil
}

func (w *recWriter) ReplaceFingerprintWindows(ref database.FingerprintWindowRef, ws []database.FingerprintWindow) error {
	w.log("ReplaceFingerprintWindows:%s:%d", ref, len(ws))
	return nil
}

func (w *recWriter) RecordFingerprintWindowFailure(f *database.FingerprintWindowFailure) error {
	w.log("RecordFingerprintWindowFailure:%s", f.Ref)
	return nil
}

func (w *recWriter) DeleteCandidates(ids []int64) (int, error) {
	w.log("DeleteCandidates:%d", len(ids))
	return len(ids), nil
}

func (w *recWriter) FingerprintResetter() fingerprintResetter { return w.resetter }

var _ kindWriter = (*recWriter)(nil)

// recResetter is the batched wipe a recWriter hands the reset op.
type recResetter struct {
	w       *recWriter
	cleared int
}

func (r *recResetter) ClearAllAcoustIDFingerprints(_ context.Context, _ int, progress func(processed, cleared, total int)) (int, int, error) {
	r.w.log("ClearAllAcoustIDFingerprints")
	if progress != nil {
		progress(r.cleared, r.cleared, r.cleared)
	}
	return r.cleared, r.cleared, nil
}

// recEngine is the scan engine NewEngine returns.
type recEngine struct {
	w      *recWriter
	failAt error
}

func (e *recEngine) AcoustIDScan(_ context.Context, progress func(done, total int)) error {
	e.w.log("AcoustIDScan")
	progress(1, 2)
	progress(2, 2)
	return e.failAt
}

// fakeCandidates answers the scan's pending-candidate count.
type fakeCandidates struct{ pending int }

func (f fakeCandidates) ListCandidates(database.CandidateFilter) ([]database.DedupCandidate, int, error) {
	return nil, f.pending, nil
}

// guardedStore is a MockStore whose write methods fail the test: the store the
// native runs read from is never written (rule R10).
func guardedStore(t *testing.T) *database.MockStore {
	t.Helper()
	return &database.MockStore{
		UpdateBookFileFunc: func(id string, _ *database.BookFile) error {
			t.Errorf("store.UpdateBookFile(%s) bypassed the writer", id)
			return nil
		},
		ModifyBookFunc: func(id string, _ func(*database.Book) error) (*database.Book, error) {
			t.Errorf("store.ModifyBook(%s) bypassed the writer", id)
			return nil, nil
		},
		ClearBookSignatureFunc: func(id string) error {
			t.Errorf("store.ClearBookSignature(%s) bypassed the writer", id)
			return nil
		},
		ReplaceFingerprintWindowsFunc: func(ref database.FingerprintWindowRef, _ []database.FingerprintWindow) error {
			t.Errorf("store.ReplaceFingerprintWindows(%s) bypassed the writer", ref)
			return nil
		},
		RecordFingerprintWindowFailureFunc: func(f *database.FingerprintWindowFailure) error {
			t.Errorf("store.RecordFingerprintWindowFailure(%s) bypassed the writer", f.Ref)
			return nil
		},
	}
}

// kit is one test's fakes: the writer and engine the deps factories hand out,
// and the tasks.Writer values those factories saw.
type kit struct {
	mu      sync.Mutex
	writers []tasks.Writer
	rec     *recWriter
	eng     *recEngine
}

func newKit() *kit {
	k := &kit{rec: &recWriter{}}
	k.eng = &recEngine{w: k.rec}
	k.rec.resetter = &recResetter{w: k.rec, cleared: 3}
	return k
}

// deps is KindDeps over store with the kit's factories; the hooks are fakes that
// touch no process state.
func (k *kit) deps(store kindStore) KindDeps {
	return KindDeps{
		Store: store,
		NewWriter: func(w tasks.Writer) kindWriter {
			k.mu.Lock()
			k.writers = append(k.writers, w)
			k.mu.Unlock()
			return k.rec
		},
		NewEngine: func(w tasks.Writer) scanEngine {
			k.mu.Lock()
			k.writers = append(k.writers, w)
			k.mu.Unlock()
			return k.eng
		},
		Candidates: fakeCandidates{pending: 2},
		Lookup: LookupDeps{
			APIKey:    func() string { return "api-key-bruce-wayne" },
			NewLookup: func(string) LookupFunc { return fakeLookup },
			Wait:      func(context.Context, time.Duration) error { return nil },
		},
		Fingerprint: fakeFingerprintDeps(),
	}
}

// fakeLookup answers every lookup with one strong Batman-titled match.
func fakeLookup(context.Context, string, int) (acoustid.LookupResult, error) {
	return acoustid.LookupResult{RecordingID: "rec-the-long-halloween", Score: 0.97}, nil
}

func (k *kit) handed() []tasks.Writer {
	k.mu.Lock()
	defer k.mu.Unlock()
	return slices.Clone(k.writers)
}

// fakeFingerprintDeps is host access that touches no process state: an fpcalc
// backend exists, no reference pair, one worker, a report written nowhere.
func fakeFingerprintDeps() FingerprintDeps {
	return FingerprintDeps{
		ServerDecodeCheck:     func(string) error { return nil },
		FingerprintAvailable:  func() bool { return true },
		Workers:               func() int { return 2 },
		ReferenceToolsSet:     func() bool { return false },
		WriteIneligibleReport: func(string, []ineligibleEntry) (string, error) { return "", nil },
	}
}

// fakeKindDeps is deps over an empty guarded store, for the shape tests.
func fakeKindDeps(t *testing.T) KindDeps {
	t.Helper()
	return newKit().deps(guardedStore(t))
}

// stubFingerprint replaces the fpcalc call so doFingerprintFile writes a print
// without a binary, and restores it.
func stubFingerprint(t *testing.T) {
	t.Helper()
	prev := fileFingerprintLengthFn
	t.Cleanup(func() { fileFingerprintLengthFn = prev })
	fileFingerprintLengthFn = func(string, int) (*fingerprint.WholeFile, error) {
		return &fingerprint.WholeFile{Raw: []byte{1, 2, 3, 4, 5, 6, 7, 8}, DurationSec: 600}, nil
	}
}

// audioFile writes a placeholder .m4b the eligibility stat accepts.
func audioFile(t *testing.T, name string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name+".m4b")
	require.NoError(t, os.WriteFile(p, []byte("audio:"+name), 0o644))
	return p
}

// ---- the literal rows (rule R8) ----

// kindRow is one definition's expected shape, spelled out literally: the native
// kind is compared with it and HEAD's definition with the same row, so a drift of
// either fails.
type kindRow struct {
	HeadID string
	Title  string
	// HeadResume is HEAD's ResumePolicy, Resume the native one (standing ruling:
	// Restart is Continue only if HEAD reads its checkpoint on restart, else
	// FromZero; Requeue FromZero; Drop Drop; Ask Ask).
	HeadResume sdk.ResumePolicy
	Resume     tasks.ResumePolicy
	Priority   tasks.Priority
	Timeout    time.Duration
	// HeadTimeout differs from Timeout only for the 72h window op (the kernel caps
	// a kind at tasks.MaxTimeout).
	HeadTimeout time.Duration
	// HeadWrites is what HEAD declares (only acoustid.backfill declares any).
	HeadWrites []sdk.Resource
	// Writes is the native write set, sorted.
	Writes []ops.Resource
	// Par is the prefix of String() of each pool the body runs, in body order;
	// ParGot is the same list from the code's own parallelism functions.
	Par     []string
	ParGot  []tasks.Parallelism
	HeadKey string
}

// settingsManageLiteral is HEAD's settings.manage atom (internal/books/atoms.go),
// spelled out here so a drift of the copy in kinds.go fails.
var settingsManageLiteral = []rbac.Permission{
	"core:settings.manage", "books:settings.manage", "core:tasks.manage", "core:system.view",
	"core:system.manage", "core:library.manage", "core:library.browse", "core:integrations.manage",
}

func kindRows() []kindRow {
	fp := []ops.Resource{ops.ResAuthors, ops.ResBookFiles, ops.ResBooks, ops.ResFingerprintIndex, ops.ResIndexes, ops.ResMetadataCache}
	return []kindRow{
		{HeadID: "acoustid.scan", Title: "AcoustID fingerprint scan", HeadResume: sdk.ResumeDrop, Resume: tasks.ResumeDrop,
			Priority: tasks.PriorityLow, Timeout: 6 * time.Hour, HeadTimeout: 6 * time.Hour,
			Writes: []ops.Resource{ops.ResEmbeddings}, HeadKey: "acoustid.fingerprint"},
		{HeadID: "acoustid.backfill", Title: "AcoustID backfill", HeadResume: sdk.ResumeRestart, Resume: tasks.ResumeContinue,
			Priority: tasks.PriorityLow, Timeout: 24 * time.Hour, HeadTimeout: 24 * time.Hour,
			HeadWrites: []sdk.Resource{sdk.ResBooks, sdk.ResBookFiles},
			Writes:     []ops.Resource{ops.ResBookFiles, ops.ResBooks, ops.ResIndexes, ops.ResMetadataCache},
			Par:        []string{"fixed(4): "}, ParGot: []tasks.Parallelism{backfillParallelism(4)}, HeadKey: "acoustid.fingerprint"},
		{HeadID: "acoustid.fingerprint-rescan", Title: "Fingerprint rescan", HeadResume: sdk.ResumeDrop, Resume: tasks.ResumeDrop,
			Priority: tasks.PriorityLow, Timeout: 12 * time.Hour, HeadTimeout: 12 * time.Hour,
			Writes: []ops.Resource{ops.ResAuthors, ops.ResBookFiles, ops.ResBooks, ops.ResFiles, ops.ResFingerprintIndex, ops.ResIndexes, ops.ResMetadataCache},
			Par:    []string{"fixed(4): "}, ParGot: []tasks.Parallelism{fingerprintRescanParallelism(4)}, HeadKey: "acoustid.fingerprint"},
		{HeadID: "acoustid.reset-all", Title: "Reset all AcoustID fingerprints", HeadResume: sdk.ResumeDrop, Resume: tasks.ResumeDrop,
			Priority: tasks.PriorityHigh, Timeout: 2 * time.Hour, HeadTimeout: 2 * time.Hour,
			Writes: []ops.Resource{ops.ResAuthors, ops.ResBookFiles, ops.ResBooks, ops.ResEmbeddings, ops.ResFingerprintIndex, ops.ResIndexes, ops.ResMetadataCache},
			Par:    []string{"cpu", "sequential: "}, ParGot: []tasks.Parallelism{resetAllSlowParallelism(), resetAllChunkParallelism()}, HeadKey: "acoustid.fingerprint"},
		{HeadID: "acoustid.lsh-backfill", Title: "Backfill LSH fingerprint index", HeadResume: sdk.ResumeDrop, Resume: tasks.ResumeDrop,
			Priority: tasks.PriorityLow, Timeout: 2 * time.Hour, HeadTimeout: 2 * time.Hour,
			Writes: fp, Par: []string{"cpu"}, ParGot: []tasks.Parallelism{lshBackfillParallelism()}, HeadKey: "acoustid.fingerprint"},
		{HeadID: "acoustid.lookup-online", Title: "Look up fingerprints on AcoustID.org", HeadResume: sdk.ResumeRequeue, Resume: tasks.ResumeFromZero,
			Priority: tasks.PriorityLow, Timeout: 24 * time.Hour, HeadTimeout: 24 * time.Hour,
			Writes: fp, HeadKey: "acoustid.online"},
		{HeadID: "acoustid.fingerprint-duration-repair", Title: "Repair fingerprint duration (re-runs fpcalc)", HeadResume: sdk.ResumeDrop, Resume: tasks.ResumeDrop,
			Priority: tasks.PriorityLow, Timeout: 6 * time.Hour, HeadTimeout: 6 * time.Hour,
			Writes: fp, Par: []string{"fixed(4): "}, ParGot: []tasks.Parallelism{durationRepairParallelism(4)}, HeadKey: "acoustid.fingerprint"},
		{HeadID: "acoustid.window-backfill", Title: "AcoustID window backfill", HeadResume: sdk.ResumeRestart, Resume: tasks.ResumeContinue,
			Priority: tasks.PriorityLow, Timeout: tasks.MaxTimeout, HeadTimeout: 72 * time.Hour,
			Writes: []ops.Resource{ops.ResFingerprintIndex},
			Par:    []string{"fixed(4): ", "fixed(32): "}, ParGot: []tasks.Parallelism{windowParallelism(4), windowPlanParallelism()}, HeadKey: "acoustid.fingerprint"},
	}
}

func kindHeadPriority(p registry.Priority) tasks.Priority {
	switch p {
	case registry.PriorityHigh:
		return tasks.PriorityHigh
	case registry.PriorityNormal:
		return tasks.PriorityNormal
	default: // registry.PriorityLow is the zero value
		return tasks.PriorityLow
	}
}

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

// TestAcoustidKinds_Conformance: taskstest.Conformance over every native kind
// (05-tasks 5T.9 last row).
func TestAcoustidKinds_Conformance(t *testing.T) {
	taskstest.Conformance(t, (&Plugin{}).Kinds(fakeKindDeps(t), nil)...)
}

// TestAcoustidKinds_MatchHEADDefs: each row compares one native kind and the HEAD
// definition it came from with the same literal row; the rows and the list are
// the same set (rule R8).
func TestAcoustidKinds_MatchHEADDefs(t *testing.T) {
	p := &Plugin{}
	defs := p.Kinds(fakeKindDeps(t), nil)
	heads := map[string]sdk.OperationDef{}
	for _, h := range p.OperationDefs() {
		heads[h.ID] = h
	}
	rows := kindRows()

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
			require.Equal(t, r.Priority, c.Priority)
			require.Equal(t, kindHeadPriority(head.DefaultPriority), c.Priority, "HEAD's priority maps to the native one")
			require.Equal(t, r.HeadTimeout, head.Timeout)
			require.Equal(t, r.Timeout, c.Timeout)
			require.False(t, head.NoWarmupWait)
			require.Equal(t, []tasks.ReadySignal{"books.memdb"}, c.Ready)
			require.Equal(t, r.HeadKey, head.ConcurrencyKey)
			require.Equal(t, tasks.Key("books."+r.HeadKey), c.ConcurrencyKey)
			require.Empty(t, c.NotWhile)
			require.Equal(t, rbac.KindAllOf, c.Perms.Start.Kind(), "every acoustid definition is settings.manage")
			require.Equal(t, rbac.Requirement(kindStartRequirement()), c.Perms.Start)
			require.Equal(t, rbac.AllOf(settingsManageLiteral...), c.Perms.Start, "the requirement is the literal settings.manage scope list")
			require.Empty(t, c.Perms.StartableByViewers)
			require.Nil(t, c.Schedule, "sched is nil in the test; no conversion declares a schedule itself")
			require.Empty(t, head.DependsOn)
			require.False(t, head.Isolate)

			require.Equal(t, r.HeadWrites, head.Writes, "the row's HEAD writes are HEAD's")
			require.False(t, c.Effects.IsReadOnly())
			require.Equal(t, refStrings(r.Writes), refStrings2(c.Effects.WriteSet()), "write set")
			require.Empty(t, c.Effects.DeleteSet(), "no AlsoDeletes: tasklint wants an owner review for each")

			require.Len(t, r.ParGot, len(r.Par))
			for i, par := range r.ParGot {
				require.True(t, strings.HasPrefix(par.String(), r.Par[i]), "pool %d is %q, want prefix %q", i, par.String(), r.Par[i])
			}
		})
	}
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
	require.Len(t, defs, 8)
	require.Len(t, p.OperationDefs(), 8, "HEAD still has all eight definitions")
}

// ---- a host with the mode and writer a test chooses ----

type barFrame struct {
	Done, Total int64
	Known       bool
}

type kindHost struct {
	*taskstest.FakeRun
	mode tasks.Mode
	w    tasks.Writer

	mu    sync.Mutex
	notes []string
	bar   []barFrame
	logs  bytes.Buffer
}

func (h *kindHost) Mode() tasks.Mode     { return h.mode }
func (h *kindHost) Writer() tasks.Writer { return h.w }

func (h *kindHost) ReportProgress(done int64, total tasks.Total) {
	n, known := total.Value()
	h.mu.Lock()
	h.bar = append(h.bar, barFrame{Done: done, Total: n, Known: known})
	h.mu.Unlock()
}

func (h *kindHost) bars() []barFrame {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.bar)
}

func (h *kindHost) Note(s string) {
	h.mu.Lock()
	h.notes = append(h.notes, s)
	h.mu.Unlock()
}

func (h *kindHost) allNotes() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.notes)
}

func (h *kindHost) Log() *slog.Logger {
	return slog.New(slog.NewTextHandler(&lockedWriter{mu: &h.mu, w: &h.logs}, nil))
}

func (h *kindHost) logged() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.logs.String()
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

func newKindHost(ctx context.Context, d tasks.Definition, mode tasks.Mode, w tasks.Writer) *kindHost {
	return &kindHost{FakeRun: taskstest.NewFakeRun(ctx, d.Kind()), mode: mode, w: w}
}

// runKind runs d's own Run, the function the executor would call, on a fresh host.
func runKind(t *testing.T, d tasks.Definition, mode tasks.Mode, w tasks.Writer, params string) (*kindHost, error) {
	t.Helper()
	h := newKindHost(context.Background(), d, mode, w)
	return h, runKindOn(t, d, h, params)
}

// runKindOn runs d on h and returns the run's error.
func runKindOn(t *testing.T, d tasks.Definition, h *kindHost, params string) error {
	t.Helper()
	_, err := runKindResult(t, d, h, params)
	return err
}

// runKindResult runs d on h and returns the run's result and error.
func runKindResult(t *testing.T, d tasks.Definition, h *kindHost, params string) (any, error) {
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
	out, err := task.Run(tasks.NewRC(h), p)
	return out, err
}

// sentinelWriter is a tasks.Writer only its identity matters for.
type sentinelWriter struct{ tasks.Writer }

// writeProbe is one Live run of a kind (rule R10).
type writeProbe struct {
	Kind   tasks.Kind
	Def    tasks.Definition
	Params string
	Kit    *kit
	// Want is a write the run must have made through the recording writer.
	Want string
}

// writeProbes builds one probe per kind, each over its own fixture.
func writeProbes(t *testing.T) []writeProbe {
	t.Helper()
	stubFingerprint(t)
	p := &Plugin{}
	var out []writeProbe
	add := func(k *kit, store kindStore, plug *Plugin, kind tasks.Kind, params, want string) {
		defs := plug.Kinds(k.deps(store), nil)
		out = append(out, writeProbe{Kind: kind, Def: byKind(t, defs, kind), Params: params, Kit: k, Want: want})
	}

	// acoustid.scan: the engine is built over the run's writer and scans.
	add(newKit(), guardedStore(t), p, scanKind, ``, "AcoustIDScan")

	// acoustid.backfill: one book, one unfingerprinted file.
	path := audioFile(t, "Batman Year One")
	fx := newPagedFixture(1, 1, func(bookID string, _ int) database.BookFile {
		return database.BookFile{ID: "f-year-one", BookID: bookID, FilePath: path, Format: "m4b"}
	})
	fx.store.UpdateBookFileFunc = func(id string, _ *database.BookFile) error {
		t.Errorf("store.UpdateBookFile(%s) bypassed the writer", id)
		return nil
	}
	add(newKit(), fx.store, p, backfillKind, ``, "UpdateBookFile:f-year-one")

	// acoustid.fingerprint-rescan: one book by id, one file.
	rpath := audioFile(t, "The Dark Knight Returns")
	rs := guardedStore(t)
	rs.GetBookByIDFunc = func(id string) (*database.Book, error) {
		return &database.Book{ID: id, Title: "The Dark Knight Returns"}, nil
	}
	rs.GetBookFilesFunc = func(string) ([]database.BookFile, error) {
		return []database.BookFile{{ID: "f-dkr", BookID: "b-dkr", FilePath: rpath, Format: "m4b"}}, nil
	}
	add(newKit(), rs, p, fingerprintRescanKind, `{"scope":"books","book_ids":["b-dkr"]}`, "UpdateBookFile:f-dkr")

	// acoustid.reset-all: the batched wipe resolved from the writer.
	add(newKit(), guardedStore(t), p, resetAllKind, ``, "ClearAllAcoustIDFingerprints")

	// acoustid.lsh-backfill: one fingerprinted file.
	ls := guardedStore(t)
	ls.GetAllBookFilesCoreFunc = func() ([]database.BookFileCore, error) {
		return []database.BookFileCore{{ID: "f-ra", BookID: "b-ra", AcoustIDFingerprintDurationSec: 1800}}, nil
	}
	ls.GetBookFileByIDFunc = func(_, id string) (*database.BookFile, error) {
		return &database.BookFile{ID: id, BookID: "b-ra", AcoustIDFingerprint: []byte{1, 2, 3, 4}}, nil
	}
	add(newKit(), ls, p, lshBackfillKind, ``, "UpdateBookFile:f-ra")

	// acoustid.lookup-online: one current-era fingerprinted file.
	os := guardedStore(t)
	os.GetAllBookFilesCoreFunc = func() ([]database.BookFileCore, error) {
		return []database.BookFileCore{{ID: "f-hush", BookID: "b-hush", AcoustIDFingerprintDurationSec: 1800, AcoustIDFPVersion: fingerprint.PrintEncodingVersion}}, nil
	}
	os.GetBookFilesFunc = func(string) ([]database.BookFile, error) {
		return []database.BookFile{{ID: "f-hush", BookID: "b-hush", AcoustIDFingerprint: []byte{1, 2, 3, 4, 5, 6, 7, 8}}}, nil
	}
	add(newKit(), os, p, onlineLookupKind, ``, "UpdateBookFile:f-hush")

	// acoustid.fingerprint-duration-repair: one zero-duration file.
	dpath := audioFile(t, "Batman Hush")
	ds := guardedStore(t)
	ds.GetFilesWithZeroDurationFingerprintFunc = func(int, int) ([]database.BookFile, int64, error) {
		return []database.BookFile{{ID: "f-hush2", BookID: "b-hush2", FilePath: dpath, Format: "m4b", AcoustIDFingerprint: []byte{1, 2, 3, 4}}}, 1, nil
	}
	add(newKit(), ds, p, durationRepairKind, ``, "UpdateBookFile:f-hush2")

	// acoustid.window-backfill: a real store and fake tools, one present file; the
	// plugin carries the fake tool wiring the default Tools hook resolves.
	e := newWBEnv(t)
	e.addFileWith("", "arkham/Batman Year One.m4b", fakeAudio(1800, 4096), 1800)
	add(newKit(), e.store, e.plugin, windowBackfillKind, ``, "ReplaceFingerprintWindows:")
	return out
}

// TestAcoustidKinds_WritesThroughShell: for every kind, one ModeLive run on a fake
// host shows the deps factory called with that run's rc.Writer() and a write
// reaching the recording writer, while the guarded store is never written (rule
// R10). A Preview run makes no write.
func TestAcoustidKinds_WritesThroughShell(t *testing.T) {
	probes := writeProbes(t)
	probed := map[tasks.Kind]bool{}
	for _, pr := range probes {
		t.Run(string(pr.Kind), func(t *testing.T) {
			require.False(t, probed[pr.Kind], "two probes for %s", pr.Kind)
			probed[pr.Kind] = true
			w := &sentinelWriter{}
			_, err := runKind(t, pr.Def, tasks.ModeLive, w, pr.Params)
			require.NoError(t, err)
			handed := pr.Kit.handed()
			require.NotEmpty(t, handed, "the deps factory was never called with the run's writer")
			for _, got := range handed {
				require.Same(t, w, got, "the factory must be handed this run's rc.Writer()")
			}
			var hit bool
			for _, c := range pr.Kit.rec.logged() {
				hit = hit || strings.HasPrefix(c, pr.Want)
			}
			require.True(t, hit, "no %q write reached the recording writer: %v", pr.Want, pr.Kit.rec.logged())
		})
	}
	require.Len(t, probed, 8, "every kind has a probe")
	for _, d := range (&Plugin{}).Kinds(fakeKindDeps(t), nil) {
		require.True(t, probed[d.Kind()], "%s has no write probe", d.Kind())
	}

	// Preview: a live-only kind refuses (HEAD has no preview), a kind with a dry
	// run reports it, and neither writes.
	for _, pr := range writeProbes(t) {
		t.Run("preview/"+string(pr.Kind), func(t *testing.T) {
			_, err := runKind(t, pr.Def, tasks.ModePreview, &sentinelWriter{}, pr.Params)
			switch pr.Kind {
			case durationRepairKind, windowBackfillKind:
				require.NoError(t, err, "HEAD's dry run is the native Preview")
			default:
				require.ErrorContains(t, err, "preview not supported")
			}
			require.Empty(t, pr.Kit.handed(), "a Preview run builds no writer")
			require.Empty(t, pr.Kit.rec.logged(), "a Preview run makes no write")
		})
	}
}

// ReportUpdate is Progress.Update: the position, then the status line when
// there is one, recorded the way ReportProgress and Note record them.
func (h *kindHost) ReportUpdate(done int64, total tasks.Total, msg string) {
	h.ReportProgress(done, total)
	if msg != "" {
		h.Note(msg)
	}
}
