// file: internal/plugins/maintenance/kinds_t6_g6_test.go
// version: 1.4.7
// guid: 5e2b8f4a-91c6-4d03-b7a5-2c8e60f1d9a7
// last-edited: 2026-11-15

package maintenance

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/fingerprint"
	"github.com/falkcorp/audiobook-organizer/internal/reconcile"
	"github.com/falkcorp/audiobook-organizer/internal/transcribe"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/tasks"
	"github.com/falkcorp/mediacore/tasks/taskstest"

	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// t6g6Recorder is the writer a test hands the kinds: it records the calls that
// reach it. It implements every method of t6g6Writer itself (the compiler checks
// that where the probe returns it), so a writer method added to the interface
// breaks this file until the recorder answers it, instead of panicking on a nil
// embedded interface the first time a kind calls it.
type t6g6Recorder struct {
	mu    sync.Mutex
	calls []string
	// books lets ModifyBook run the caller's fn on a copy, so the test sees the
	// status the repair would have written.
	books map[string]*database.Book
	// backing, when set, is the real store the rbf apply reads its own writes
	// back from.
	backing rbfWriter
	// us, when set, is the real user-state store the repair-merged writes (and
	// the test store's reads) go to; it records its own writes into this
	// recorder.
	us *t6g6UserStateSpy
	// cutoffs are the cutoffs CompactActivityEntries was handed.
	cutoffs []time.Time
	// ensure, when set, scripts what the opchange ensure answers; nil answers a
	// trusted, already-complete index.
	ensure func(database.OpChangeEnsureOptions) (database.OpChangeByBookEnsureResult, error)
}

// t6g6WriterResource maps every writer method the spy can record to the FULL set
// of Effects resources its call chain writes, as "books/<resource>" refs. It is
// this group's view of t6WriterResources (kinds_resources.go), the one method
// table of every converted group, where each row is traced from the store call
// chain and cited (the CollapseHealDuplicates chain is cited there). A recorded
// call whose method has no row fails the test, and t6g6AssertWritesDeclared
// requires every traced resource to be in the kind's declared write set. SetRaw
// is this group's user_state key family (the pending-repair keys).
//
// The heal's whisper journal (ai_journal) is not a writer-method call the spy
// records (HealWhisperJournal is an accessor the spy answers with nil, and the
// transcription that writes the journal needs ffmpeg and a whisper pool), so it
// is pinned by TestT6G6Kinds_HealWhisperJournalWritesTheDeclaredAIJournalKeys
// (kinds_t6_g6_m5_test.go): the production journal over a real store writes under
// aijournal:, and the heal's Effects declare ai_journal. The heal row of
// TestT6G6Kinds_MatchHEADDefs only compares the declaration with HEAD's.
var t6g6WriterResource = t6g6WriterView()

func t6g6WriterView() map[string][]string {
	out := map[string][]string{}
	for method, row := range t6WriterView(map[string]string{"SetRaw": "SetRaw/user_state"}, reflect.TypeFor[t6g6Writer]()) {
		out[method] = t6g6Eff(row...)
	}
	return out
}

// t6g6Eff is the sorted "books/<resource>" refs of the given pkg/ops constants.
// Expected write sets are spelled with the constants, so a coined resource name
// does not compile.
func t6g6Eff(rs ...ops.Resource) []string {
	out := make([]string, 0, len(rs))
	for _, r := range rs {
		out = append(out, "books/"+string(r))
	}
	sort.Strings(out)
	return out
}

// t6g6AssertWritesDeclared fails when a recorded write names a method with no
// mapped resource, or a resource the kind's declared WriteSet does not hold.
func t6g6AssertWritesDeclared(t *testing.T, calls []string, def tasks.Definition) {
	t.Helper()
	declared := map[string]bool{}
	for _, res := range tasks.CommonOf(def).Effects.WriteSet() {
		declared[tasks.FormatRef(res)] = true
	}
	for _, c := range calls {
		method, _, _ := strings.Cut(c, " ")
		traced, ok := t6g6WriterResource[method]
		require.True(t, ok, "%s: writer method %q has no resource set in t6g6WriterResource", def.Kind(), method)
		// A recorded call is a method the kind's own writer interfaces list: the
		// kind's expected write set is derived from those, so a call outside them
		// would be a write the derivation never counted.
		kw := t6g6KindWriters()[def.Kind()]
		require.Contains(t, t6WriterMethods(kw.writers...), method, "%s: recorded %q, which is not a method of the kind's writer interfaces", def.Kind(), c)
		for _, res := range traced {
			require.True(t, declared[res], "%s: %q writes %s, which its Effects do not declare", def.Kind(), c, res)
		}
	}
}

func (r *t6g6Recorder) rec(s string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, s)
}

func (r *t6g6Recorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.calls...)
}

func (r *t6g6Recorder) CompactActivityEntries(_ context.Context, cutoff time.Time, _ database.CompactProgress) (database.CompactResult, error) {
	r.mu.Lock()
	r.cutoffs = append(r.cutoffs, cutoff)
	r.mu.Unlock()
	r.rec("CompactActivityEntries")
	return database.CompactResult{DaysCompacted: 2, EntriesDeleted: 9}, nil
}

func (r *t6g6Recorder) RecompactActivityDigests(context.Context) (database.RecompactResult, error) {
	r.rec("RecompactActivityDigests")
	return database.RecompactResult{Touched: 3}, nil
}

func (r *t6g6Recorder) RebuildOpChangeByBookIndex(context.Context, database.OpChangeIndexProgress) (database.OpChangeByBookBackfillResult, error) {
	r.rec("RebuildOpChangeByBookIndex")
	return database.OpChangeByBookBackfillResult{Scanned: 4, Indexed: 4}, nil
}

func (r *t6g6Recorder) EnsureOpChangeByBookIndexWith(_ context.Context, opts database.OpChangeEnsureOptions) (database.OpChangeByBookEnsureResult, error) {
	r.rec("EnsureOpChangeByBookIndexWith")
	r.mu.Lock()
	f := r.ensure
	r.mu.Unlock()
	if f != nil {
		return f(opts)
	}
	return database.OpChangeByBookEnsureResult{Trusted: true, Backfill: database.OpChangeByBookBackfillResult{Skipped: true}}, nil
}

func (r *t6g6Recorder) CollapseHealDuplicates(_, keepID string, _ []string) error {
	r.rec("CollapseHealDuplicates " + keepID)
	return nil
}

func (r *t6g6Recorder) HealTrack(dst, src string) error {
	r.rec("HealTrack " + filepath.Base(dst) + " <- " + filepath.Base(src))
	return nil
}

func (r *t6g6Recorder) HealWhisperJournal() transcribe.ResultJournal { return nil }

func (r *t6g6Recorder) UpdateBookFiles(_ context.Context, files []*database.BookFile, afterRow func(int, bool)) (int, error) {
	for i, f := range files {
		r.rec("UpdateBookFiles " + f.ID + " -> " + filepath.Base(f.FilePath))
		if afterRow != nil {
			afterRow(i, true)
		}
	}
	return len(files), nil
}

func (r *t6g6Recorder) UpsertReviewItem(item database.ReviewItem) (database.ReviewItem, error) {
	r.rec("UpsertReviewItem " + item.FolderRef)
	item.Status = database.ReviewStatusPending
	return item, nil
}

func (r *t6g6Recorder) DeleteReviewItemIfPending(id string) (bool, error) {
	r.rec("DeleteReviewItemIfPending " + id)
	return true, nil
}

func (r *t6g6Recorder) CreateBookFile(f *database.BookFile) error {
	r.rec("CreateBookFile " + filepath.Base(f.FilePath))
	return nil
}

func (r *t6g6Recorder) BatchCreateBookFiles(files []*database.BookFile) error {
	for _, f := range files {
		r.rec("CreateBookFile " + filepath.Base(f.FilePath))
	}
	return nil
}

func (r *t6g6Recorder) ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error) {
	if r.us != nil {
		return r.us.ModifyBook(id, fn)
	}
	cur := *r.books[id]
	if err := fn(&cur); err != nil {
		return nil, err
	}
	status := "cleared"
	if cur.TranscribeStatus != nil {
		status = *cur.TranscribeStatus
	}
	r.rec("ModifyBook " + id + " status=" + status)
	return &cur, nil
}

func (r *t6g6Recorder) ModifyBookFile(bookID, fileID string, fn func(*database.BookFile) error) (*database.BookFile, error) {
	r.rec("ModifyBookFile " + fileID)
	return r.backing.ModifyBookFile(bookID, fileID, fn)
}

func (r *t6g6Recorder) RecomputeBookAggregates(bookID string) error {
	r.rec("RecomputeBookAggregates " + bookID)
	return r.backing.RecomputeBookAggregates(bookID)
}

func (r *t6g6Recorder) CreateOperationChange(c *database.OperationChange) error {
	r.rec("CreateOperationChange")
	return r.backing.CreateOperationChange(c)
}

// t6g6TestVerifier is the read-only verify surface of a store with the index.
type t6g6TestVerifier struct{}

func (t6g6TestVerifier) VerifyOpChangeByBookIndex(context.Context, database.OpChangeIndexProgress) (database.OpChangeByBookIndexReport, error) {
	return database.OpChangeByBookIndexReport{}, nil
}

// t6g6TestStore is the read side: an in-memory library. Whatever a test does not
// seed answers empty; whatever it does not model panics.
type t6g6TestStore struct {
	t6g6Store

	order []string
	books map[string]*database.Book
	files map[string][]database.BookFile
	cores []database.BookFileCore
}

func (s *t6g6TestStore) ListBookIDs() ([]string, error) { return s.order, nil }
func (s *t6g6TestStore) GetBookByID(id string) (*database.Book, error) {
	return s.books[id], nil
}
func (s *t6g6TestStore) GetBookFiles(id string) ([]database.BookFile, error) {
	return s.files[id], nil
}
func (s *t6g6TestStore) AllBookFilesCoreIncludingRetired() ([]database.BookFileCore, error) {
	return s.cores, nil
}
func (s *t6g6TestStore) ListReviewItems(database.ReviewFilter) ([]database.ReviewItem, int, error) {
	return nil, 0, nil
}
func (s *t6g6TestStore) OpChangeIndexVerifier() (t6g6OpChangeIndexVerifier, string, bool) {
	return t6g6TestVerifier{}, "fake", true
}

// GetBookFileByPath answers the seeded file whose last two path elements match
// (a test's temp root may be reached through a symlink).
func (s *t6g6TestStore) GetBookFileByPath(path string) (*database.BookFile, error) {
	key := func(p string) string { return filepath.Join(filepath.Base(filepath.Dir(p)), filepath.Base(p)) }
	for _, fs := range s.files {
		for _, f := range fs {
			if key(f.FilePath) == key(path) {
				return &f, nil
			}
		}
	}
	return nil, nil
}

// add seeds one book (and its files) under id.
func (s *t6g6TestStore) add(b database.Book, files ...database.BookFile) {
	if s.books == nil {
		s.books = map[string]*database.Book{}
		s.files = map[string][]database.BookFile{}
	}
	s.order = append(s.order, b.ID)
	s.books[b.ID] = &b
	s.files[b.ID] = files
}

// t6g6RBFStore forwards the six reads the repoint plans with to a real in-memory
// store, which the repoint's own write-back reads agree with.
type t6g6RBFStore struct {
	t6g6Store
	s *database.PebbleStore
}

func (r t6g6RBFStore) GetBookByID(id string) (*database.Book, error) { return r.s.GetBookByID(id) }
func (r t6g6RBFStore) GetBookFiles(id string) ([]database.BookFile, error) {
	return r.s.GetBookFiles(id)
}
func (r t6g6RBFStore) GetBookFileByID(b, f string) (*database.BookFile, error) {
	return r.s.GetBookFileByID(b, f)
}
func (r t6g6RBFStore) GetBookFileByPath(p string) (*database.BookFile, error) {
	return r.s.GetBookFileByPath(p)
}
func (r t6g6RBFStore) GetAllBookFilesCore() ([]database.BookFileCore, error) {
	return r.s.GetAllBookFilesCore()
}

// AllBookFilesCoreIncludingRetired is the held store's own method (MINOR 3): the
// native claims pass calls it by name, so nothing here depends on Unwrap.
func (r t6g6RBFStore) AllBookFilesCoreIncludingRetired() ([]database.BookFileCore, error) {
	return database.AllBookFilesCoreIncludingRetired(r.s)
}

func (r t6g6RBFStore) LiveBookIDsAtPath(p string) ([]string, error) { return r.s.LiveBookIDsAtPath(p) }

// t6g6Probe records how the native shells called deps.NewWriter.
type t6g6Probe struct {
	mu    sync.Mutex
	calls int
	given tasks.Writer

	rec        *t6g6Recorder
	store      t6g6Store
	root       string
	reportPath string
	standKey   tasks.Key
}

func newT6G6Probe(t *testing.T) *t6g6Probe {
	t.Helper()
	root := t.TempDir()
	return &t6g6Probe{
		rec:        &t6g6Recorder{},
		store:      &t6g6TestStore{},
		root:       root,
		reportPath: filepath.Join(t.TempDir(), "report.tsv"),
		standKey:   tasks.Key("books.gotham-watch"),
	}
}

func (p *t6g6Probe) deps() t6g6Deps {
	return t6g6Deps{
		Store: p.store,
		NewWriter: func(w tasks.Writer) t6g6Writer {
			p.mu.Lock()
			defer p.mu.Unlock()
			p.calls++
			p.given = w
			return p.rec
		},
		ActivityLogFullDetailDays: func() int { return 30 },
		StandDownKey:              p.standKey,
		RootDir:                   func() string { return p.root },
		ResolveReportPath:         func(string, string) (string, error) { return p.reportPath, nil },
		ITunesRoots:               func() ([]string, error) { return nil, nil },
	}
}

func (p *t6g6Probe) writerCalls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

// fakeT6G6Deps is enough to build the 14 definitions: conformance reads their
// declarations and never runs a body.
func fakeT6G6Deps(t *testing.T) t6g6Deps {
	t.Helper()
	return newT6G6Probe(t).deps()
}

// t6g6GateWriter is a distinguishable tasks.Writer: identity is the assertion.
type t6g6GateWriter struct{ name string }

func (*t6g6GateWriter) Do(context.Context, tasks.Write, func(context.Context) ([]tasks.Change, error)) error {
	return nil
}
func (*t6g6GateWriter) MarkRowApplied(context.Context, string)           {}
func (*t6g6GateWriter) RowApplied(context.Context, string) (bool, error) { return false, nil }

type t6g6TestHold struct{ released *bool }

func (h t6g6TestHold) Release() { *h.released = true }

// t6g6Host is a taskstest.FakeRun with the mode and writer a test chooses
// (FakeRun is pinned to ModeLive with no writer, and refuses a stand-down).
type t6g6Host struct {
	*taskstest.FakeRun
	mode tasks.Mode
	w    tasks.Writer

	mu         sync.Mutex
	standDowns []tasks.Key
	released   bool
	// caps are the worker counts the run's item loops declared (CapWorkers is
	// handed the declared count before every loop).
	caps []int
}

// CapWorkers records the declared count and applies the fake's own cap.
func (h *t6g6Host) CapWorkers(declared int) int {
	h.mu.Lock()
	h.caps = append(h.caps, declared)
	h.mu.Unlock()
	return h.FakeRun.CapWorkers(declared)
}

func (h *t6g6Host) workerCaps() []int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]int(nil), h.caps...)
}

func (h *t6g6Host) Mode() tasks.Mode     { return h.mode }
func (h *t6g6Host) Writer() tasks.Writer { return h.w }
func (h *t6g6Host) Log() *slog.Logger    { return slog.New(slog.DiscardHandler) }
func (h *t6g6Host) StandDown(k tasks.Key) (tasks.Hold, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.standDowns = append(h.standDowns, k)
	return t6g6TestHold{released: &h.released}, nil
}

// runT6G6Kind runs d's own Run, the function the executor would call.
func runT6G6Kind(t *testing.T, d tasks.Definition, mode tasks.Mode, w tasks.Writer, params string) (*t6g6Host, error) {
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
	h := &t6g6Host{FakeRun: taskstest.NewFakeRun(context.Background(), d.Kind()), mode: mode, w: w}
	_, err = task.Run(tasks.NewRC(h), p)
	return h, err
}

// runT6G6KindRefusing is runT6G6Kind for a start that may be refused before the
// run body: an error from the executor's decode of the params is returned (not
// failed on), so a test can assert a refusal wherever it happens. A refused
// start returns a nil host, since no run began.
func runT6G6KindRefusing(t *testing.T, d tasks.Definition, mode tasks.Mode, w tasks.Writer, params string) (*t6g6Host, error) {
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
	h := &t6g6Host{FakeRun: taskstest.NewFakeRun(context.Background(), d.Kind()), mode: mode, w: w}
	_, err = task.Run(tasks.NewRC(h), p)
	return h, err
}

func t6g6ByKind(t *testing.T, defs []tasks.Definition, kind tasks.Kind) tasks.Definition {
	t.Helper()
	for _, d := range defs {
		if d.Kind() == kind {
			return d
		}
	}
	t.Fatalf("no definition of kind %s", kind)
	return nil
}

func TestT6G6Kinds_Conformance(t *testing.T) {
	taskstest.Conformance(t, (&Plugin{}).t6g6Kinds(fakeT6G6Deps(t), nil)...)
}

// t6g6KindWrite is what one group-6 kind writes: the writer interfaces its core is
// handed (the narrow interface of each definition, composed into t6g6Writer) and
// the resources it writes without a writer call (bytes on disk and the like).
type t6g6KindWrite struct {
	writers []reflect.Type
	outside []ops.Resource
}

// t6g6KindWriters is each kind's writes, keyed by kind. A kind absent from it is
// ReadOnly. The declared write set of a kind is the union of the shared table's
// rows (t6g6WriterResource) for every method of its writer interfaces, plus
// outside: derived by t6g6ExpectedEffects, enforced both ways by
// TestT6G6Kinds_MatchHEADDefs, so a row that widens widens the kind and a kind
// cannot declare a resource none of its methods writes.
var t6g6KindWritersOnce = sync.OnceValue(t6g6KindWritersBuild)

func t6g6KindWriters() map[tasks.Kind]t6g6KindWrite { return t6g6KindWritersOnce() }

func t6g6KindWritersBuild() map[tasks.Kind]t6g6KindWrite {
	p := &Plugin{}
	m := map[tasks.Kind]t6g6KindWrite{}
	add := func(head sdk.OperationDef, kw t6g6KindWrite) { m[tasks.Kind("books."+head.ID)] = kw }
	add(p.nightlyCompactActivityLogDef(), t6g6KindWrite{writers: []reflect.Type{reflect.TypeFor[nightlyCompactActivityLogWriter]()}})
	add(p.opChangeBookIndexRebuildDef(), t6g6KindWrite{writers: []reflect.Type{reflect.TypeFor[opChangeBookIndexWriter]()}})
	add(p.probeDirectoryBooksDef(), t6g6KindWrite{writers: []reflect.Type{reflect.TypeFor[probeDirectoryBooksWriter]()}})
	add(p.recompactActivityDigestsDef(), t6g6KindWrite{writers: []reflect.Type{reflect.TypeFor[recompactActivityDigestsWriter]()}})
	add(p.itunesHealDef(), t6g6KindWrite{writers: []reflect.Type{reflect.TypeFor[reconcile.HealWriter]()}})
	add(p.recoverMissingFilesDef(), t6g6KindWrite{
		writers: []reflect.Type{reflect.TypeFor[recoverMissingFilesWriter]()},
		outside: []ops.Resource{ops.ResFiles}})
	add(p.regroupShatteredAIDef(), t6g6KindWrite{writers: []reflect.Type{
		reflect.TypeFor[regroupShatteredAIWriter](), reflect.TypeFor[database.ReviewItemPendingDeleter]()}})
	add(p.relinkUnlinkedBooksDef(), t6g6KindWrite{writers: []reflect.Type{reflect.TypeFor[relinkUnlinkedBooksWriter]()}})
	add(p.repairMergedUserStateDef(), t6g6KindWrite{writers: []reflect.Type{reflect.TypeFor[repairMergedUserStateWrites]()}})
	add(p.repairTranscribeStatusDef(), t6g6KindWrite{writers: []reflect.Type{reflect.TypeFor[repairTranscribeStatusWriter]()}})
	add(p.repointBookFileRowsDef(), t6g6KindWrite{writers: []reflect.Type{reflect.TypeFor[rbfWriter]()}})
	return m
}

// t6g6ExpectedEffects is the sorted "books/<resource>" write set a kind must
// declare, nil for a ReadOnly kind. A method of a kind's interfaces with no row
// fails the test rather than counting as empty.
func t6g6ExpectedEffects(t *testing.T, kind tasks.Kind) []string {
	t.Helper()
	kw, ok := t6g6KindWriters()[kind]
	if !ok {
		return nil
	}
	set := map[string]bool{}
	for _, m := range t6WriterMethods(kw.writers...) {
		refs, ok := t6g6WriterResource[m]
		require.True(t, ok, "%s: writer method %q has no row in the shared table", kind, m)
		for _, r := range refs {
			set[r] = true
		}
	}
	for _, r := range t6g6Eff(kw.outside...) {
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

// TestT6G6Resources_TableCoversEveryWriterMethod: the group's view of the shared
// table names exactly the write methods of t6g6Writer, including the ones that
// only exist behind a capability getter (repair-merged-user-state writes its sync
// identity, bookmarks and position timestamps through them). Before the view
// walked the getters, SetUserPositionAt and its two siblings had no row and the
// repair's recorded writes failed with "has no resource set".
func TestT6G6Resources_TableCoversEveryWriterMethod(t *testing.T) {
	methods := t6WriterMethods(reflect.TypeFor[t6g6Writer]())
	for _, capMethod := range []string{"MintOrGetSyncID", "RecordSyncMerge", "CopyBookmarkIfAbsent", "SetUserPositionAt"} {
		require.Contains(t, methods, capMethod, "the capability-getter walk must reach %s", capMethod)
	}
	for _, getter := range []string{"UserStateSyncWrites", "UserStateBookmarkWrites", "UserStateTimestampWriter"} {
		require.NotContains(t, methods, getter, "%s hands out an interface; it is not a write", getter)
	}
	rows := make([]string, 0, len(t6g6WriterResource))
	for name := range t6g6WriterResource {
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
	require.Empty(t, missing, "t6g6Writer methods with no row in the shared table")
	require.Empty(t, extra, "rows in the group's view for a method t6g6Writer does not have")
}

func TestT6G6Kinds_MatchHEADDefs(t *testing.T) {
	p := &Plugin{}
	defs := p.t6g6Kinds(fakeT6G6Deps(t), nil)
	require.Len(t, defs, 14)

	// Every expected value below is written out by hand from the HEAD def and
	// the coordinator's rulings, never derived from the code under test.
	type row struct {
		head    sdk.OperationDef
		resume  tasks.ResumePolicy
		timeout time.Duration
		// par is the HEAD loop's parallelism, for the kinds whose body runs a pool
		// of its own (the *Parallelism func is the one both shells call); nil for
		// a kind with no loop.
		par     *tasks.Parallelism
		wantPar string
	}
	cpu := func(f func() tasks.Parallelism) *tasks.Parallelism { v := f(); return &v }
	rows := []row{
		{head: p.nightlyCompactActivityLogDef(), resume: tasks.ResumeDrop, timeout: 6 * time.Hour},
		{head: p.opChangeBookIndexRebuildDef(), resume: tasks.ResumeContinue, timeout: 60 * time.Minute},
		{head: p.optimizeDef(), resume: tasks.ResumeContinue, timeout: tasks.MaxTimeout},
		{head: p.orphanBookFilesCleanupDef(), resume: tasks.ResumeDrop, timeout: 30 * time.Minute},
		{head: p.orphanBookFilesRepointPlanDef(), resume: tasks.ResumeDrop, timeout: 30 * time.Minute,
			par: cpu(orphanBookFilesRepointPlanParallelism), wantPar: "cpu"},
		{head: p.probeDirectoryBooksDef(), resume: tasks.ResumeDrop, timeout: 120 * time.Minute,
			par: cpu(probeDirectoryBooksParallelism), wantPar: "cpu"},
		{head: p.recompactActivityDigestsDef(), resume: tasks.ResumeDrop, timeout: 60 * time.Minute},
		{head: p.itunesHealDef(), resume: tasks.ResumeFromZero, timeout: 60 * time.Minute},
		{head: p.recoverMissingFilesDef(), resume: tasks.ResumeDrop, timeout: 4 * time.Hour,
			par: cpu(recoverMissingFilesParallelism), wantPar: "fixed(24): "},
		{head: p.regroupShatteredAIDef(), resume: tasks.ResumeDrop, timeout: 120 * time.Minute,
			par: cpu(regroupShatteredAIParallelism), wantPar: "cpu"},
		{head: p.relinkUnlinkedBooksDef(), resume: tasks.ResumeDrop, timeout: 120 * time.Minute,
			par: cpu(relinkUnlinkedBooksParallelism), wantPar: "cpu"},
		{head: p.repairMergedUserStateDef(), resume: tasks.ResumeDrop, timeout: 30 * time.Minute},
		{head: p.repairTranscribeStatusDef(), resume: tasks.ResumeFromZero, timeout: 2 * time.Hour,
			par: cpu(repairTranscribeStatusParallelism), wantPar: "fixed(8): "},
		{head: p.repointBookFileRowsDef(), resume: tasks.ResumeDrop, timeout: p.repointBookFileRowsDef().Timeout},
	}

	seen := map[tasks.Kind]bool{}
	for _, r := range rows {
		id := r.head.ID
		kind := tasks.Kind("books." + id)
		seen[kind] = true
		def := t6g6ByKind(t, defs, kind)
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

		// R5 with the coordinator's NIT 5 ruling: a Restart def maps to
		// ResumeContinue only when its native body reads a checkpoint on restart,
		// else ResumeFromZero. The expected policy is written out per row.
		require.Equal(t, r.resume, c.Resume, "%s resume", id)

		var gotWrites []string
		for _, res := range c.Effects.WriteSet() {
			gotWrites = append(gotWrites, tasks.FormatRef(res))
		}
		sort.Strings(gotWrites)
		// The expected write set is derived, never typed: the union of the shared
		// table's rows for every write method of the kind's writer interfaces, plus
		// what it writes without a writer call (t6g6KindWriters). A kind with
		// neither is ReadOnly.
		if wantWrites := t6g6ExpectedEffects(t, kind); wantWrites == nil {
			require.True(t, c.Effects.IsReadOnly(), "%s must be declared ReadOnly", id)
		} else {
			require.False(t, c.Effects.IsReadOnly(), "%s writes", id)
			require.Equal(t, wantWrites, gotWrites, "%s write set: the union of the rows of every method of its writer interfaces", id)
		}

		if r.par != nil {
			require.True(t, strings.HasPrefix(r.par.String(), r.wantPar), "%s parallelism %q, want prefix %q", id, r.par.String(), r.wantPar)
		}
		require.Contains(t, c.Ready, t6g6SignalMemDB, "%s readiness", id)
		// Atoms (coordinator ruling 7): read what the definition declares, and
		// check it against the settings.manage scope list written out by hand.
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
	require.Len(t, seen, 14, "14 distinct kinds")
}

// t6g6WriteCase is one writing kind's live run.
type t6g6WriteCase struct {
	kind   tasks.Kind
	params string
	// liveOnly kinds refuse a Preview run before they build a writer.
	liveOnly bool
	// setup seeds the probe's store, fakes and files.
	setup func(t *testing.T, p *t6g6Probe)
	// live is every recorded call a Live run makes; preview, a Preview run's.
	live, preview []string
	// standsDown is true when a Live run holds the scan stand-down.
	standsDown bool
}

func t6g6ShatterStore(t *testing.T) *t6g6TestStore {
	t.Helper()
	s := &t6g6TestStore{}
	base := "/lib/Bruce Wayne/Year One/"
	for i := 1; i <= 6; i++ {
		id := "01BATBOOK00000000000000A" + itoa(i)
		s.add(database.Book{ID: id}, database.BookFile{BookID: id, FilePath: base + "Year One - " + itoa(i) + "/01.mp3"})
	}
	return s
}

// t6g6HealLibrary sets up one iTunes track whose mapped path is missing and a
// library copy of its file under each of the named folders of the probe's root.
// One folder gives layer 1 a single candidate; two give it a tie.
func t6g6HealLibrary(t *testing.T, p *t6g6Probe, folders ...string) {
	t.Helper()
	healDir := t.TempDir()
	for _, f := range folders {
		writeFile(t, filepath.Join(p.root, f, "Hush.mp3"), 10)
	}
	xml := filepath.Join(t.TempDir(), "Library.xml")
	require.NoError(t, os.WriteFile(xml, []byte(`<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict><key>Tracks</key><dict><key>1</key><dict>
<key>Location</key><string>file://localhost/W:/Gotham/Hush.mp3</string>
<key>Name</key><string>Hush</string><key>Artist</key><string>Bruce Wayne</string>
<key>Persistent ID</key><string>BATPID0000000001</string></dict></dict></dict></plist>`), 0o644))
	oldITunes, oldRoot := config.AppConfig.ITunes, config.AppConfig.RootDir
	t.Cleanup(func() { config.AppConfig.ITunes, config.AppConfig.RootDir = oldITunes, oldRoot })
	config.AppConfig.ITunes.LibraryReadPath = xml
	config.AppConfig.ITunes.PathMappings = []config.ITunesPathMap{{From: "W:/Gotham", To: healDir}}
	config.AppConfig.RootDir = p.root
}

// The heal's duplicate collapse runs a full book merge, and only a native test
// that makes it fire exercises the traced resource set of CollapseHealDuplicates
// against the kind's Effects. Two library copies of the track tie on layer 1;
// each holds the same stored print, so layer 2 collapses their books.
func TestT6G6Native_HealCollapseWritesAreTraced(t *testing.T) {
	p := newT6G6Probe(t)
	t6g6HealLibrary(t, p, "Arkham", "Gotham")
	fp := []byte{1, 2, 3, 4, 5, 6, 7, 8}
	s := &t6g6TestStore{}
	for i, folder := range []string{"Arkham", "Gotham"} {
		id := "01BATBOOK0000000000000H0" + itoa(i+1)
		s.add(database.Book{ID: id}, database.BookFile{
			ID: "01BATFILE0000000000000H0" + itoa(i+1), BookID: id,
			FilePath:            filepath.Join(p.root, folder, "Hush.mp3"),
			AcoustIDFingerprint: fp, AcoustIDFPVersion: fingerprint.PrintEncodingVersion,
		})
	}
	p.store = s
	def := t6g6ByKind(t, (&Plugin{}).t6g6Kinds(p.deps(), nil), itunesHealKind)
	_, err := runT6G6Kind(t, def, tasks.ModeLive, &t6g6GateWriter{name: "gotham"}, "")
	require.NoError(t, err)
	require.Equal(t, []string{
		"CollapseHealDuplicates 01BATBOOK0000000000000H01",
		"HealTrack Hush.mp3 <- Hush.mp3",
	}, p.rec.snapshot())
	t6g6AssertWritesDeclared(t, p.rec.snapshot(), def)
}

func t6g6WriteCases() []t6g6WriteCase {
	const transportErr = `transcribe remote: transcribe-batch chunk 0-32: Post "http://<host>/transcribe-batch": dial tcp: connect: connection refused`
	return []t6g6WriteCase{
		{
			kind: nightlyCompactActivityLogKind, liveOnly: true,
			live: []string{"CompactActivityEntries"},
		},
		{
			kind: opChangeBookIndexKind, params: `{}`,
			live: []string{"RebuildOpChangeByBookIndex"},
		},
		{
			kind: recompactActivityDigestsKind, liveOnly: true,
			live: []string{"RecompactActivityDigests"},
		},
		{
			kind: itunesHealKind, liveOnly: true,
			setup: func(t *testing.T, p *t6g6Probe) { t6g6HealLibrary(t, p, "Gotham") },
			live:  []string{"HealTrack Hush.mp3 <- Hush.mp3"},
		},
		{
			kind: relinkUnlinkedBooksKind,
			setup: func(t *testing.T, p *t6g6Probe) {
				path := filepath.Join(t.TempDir(), "The Long Halloween.mp3")
				require.NoError(t, os.WriteFile(path, []byte("x"), 0o644))
				s := &t6g6TestStore{}
				s.add(database.Book{ID: "01BATBOOK00000000000000B1", Title: "The Long Halloween", FilePath: path})
				p.store = s
			},
			live: []string{"CreateBookFile The Long Halloween.mp3"},
		},
		{
			kind: probeDirectoryBooksKind,
			setup: func(t *testing.T, p *t6g6Probe) {
				files := []string{"The Killing Joke_01_.mp3", "The Killing Joke_02_.mp3", "The Killing Joke_03_.mp3"}
				dir := makeFolder(t, files...)
				installFFprobeLookup(t, "ffprobe", nil)
				installProber(t, &fakeProber{bySec: map[string]float64{files[0]: 1500, files[1]: 1480, files[2]: 1520}})
				s := &t6g6TestStore{}
				s.add(database.Book{ID: "01BATBOOK00000000000000C1", Title: "The Killing Joke", FilePath: dir})
				p.store = s
			},
			live: []string{
				"CreateBookFile The Killing Joke_01_.mp3",
				"CreateBookFile The Killing Joke_02_.mp3",
				"CreateBookFile The Killing Joke_03_.mp3",
			},
		},
		{
			kind: repairTranscribeStatusKind,
			setup: func(t *testing.T, p *t6g6Probe) {
				b := database.Book{
					ID:               "01BATBOOK00000000000000D1",
					TranscribeStatus: new(statusWhisperError),
					TranscribeError:  new(transportErr),
				}
				s := &t6g6TestStore{}
				s.add(b)
				p.store = s
				p.rec.books = map[string]*database.Book{b.ID: &b}
			},
			live: []string{"ModifyBook 01BATBOOK00000000000000D1 status=cleared"},
		},
		{
			kind: regroupShatteredAIKind,
			setup: func(t *testing.T, p *t6g6Probe) {
				p.store = t6g6ShatterStore(t)
			},
			live: []string{"UpsertReviewItem /lib/Bruce Wayne/Year One"},
		},
		{
			kind: recoverMissingFilesKind, standsDown: true,
			setup: func(t *testing.T, p *t6g6Probe) {
				gone := filepath.Join(p.root, "Gotham", "Dark Victory", "gone.mp3")
				target := filepath.Join(p.root, "Gotham", "Dark Victory", "renamed.mp3")
				writeFile(t, target, 4242)
				s := &t6g6TestStore{cores: []database.BookFileCore{
					{ID: "01BATFILE00000000000000E1", BookID: "01BATBOOK00000000000000E1", FilePath: gone, FileSize: 4242},
				}}
				s.files = map[string][]database.BookFile{"01BATBOOK00000000000000E1": {{
					ID: "01BATFILE00000000000000E1", BookID: "01BATBOOK00000000000000E1", FilePath: gone, FileSize: 4242,
				}}}
				p.store = s
			},
			live: []string{"UpdateBookFiles 01BATFILE00000000000000E1 -> renamed.mp3"},
		},
	}
}

func TestT6G6Kinds_WritesThroughShell(t *testing.T) {
	gate := &t6g6GateWriter{name: "gotham"}

	for _, c := range t6g6WriteCases() {
		t.Run(string(c.kind), func(t *testing.T) {
			// Live: every write goes through the writer NewWriter built over the
			// run's own writer.
			p := newT6G6Probe(t)
			if c.setup != nil {
				c.setup(t, p)
			}
			defs := (&Plugin{}).t6g6Kinds(p.deps(), nil)
			def := t6g6ByKind(t, defs, c.kind)

			h, err := runT6G6Kind(t, def, tasks.ModeLive, gate, c.params)
			require.NoError(t, err)
			require.Equal(t, 1, p.writerCalls(), "NewWriter calls")
			require.Same(t, gate, p.given, "NewWriter must be given the run's writer")
			require.Equal(t, c.live, p.rec.snapshot(), "writes in a live run")
			t6g6AssertWritesDeclared(t, p.rec.snapshot(), def)
			if c.standsDown {
				require.Equal(t, []tasks.Key{p.standKey}, h.standDowns, "scan stand-down")
				require.True(t, h.released, "stand-down released")
			} else {
				require.Empty(t, h.standDowns)
			}

			// Preview: a live-only kind refuses before it builds a writer; every
			// other kind reports without writing.
			p = newT6G6Probe(t)
			if c.setup != nil {
				c.setup(t, p)
			}
			defs = (&Plugin{}).t6g6Kinds(p.deps(), nil)
			def = t6g6ByKind(t, defs, c.kind)
			_, err = runT6G6Kind(t, def, tasks.ModePreview, nil, c.params)
			if c.liveOnly {
				require.Error(t, err)
				require.Contains(t, err.Error(), "preview not supported")
				require.Zero(t, p.writerCalls(), "a refused preview built a writer")
				require.Empty(t, p.rec.snapshot())
				return
			}
			require.NoError(t, err)
			require.Equal(t, c.preview, p.rec.snapshot(), "calls in a preview run")
		})
	}
}

// The repoint writes through a real store: its apply reads its own writes back.
func TestT6G6More_RepointWritesThroughShell(t *testing.T) {
	gate := &t6g6GateWriter{name: "gotham"}
	build := func(t *testing.T) (*t6g6Probe, rbfFixture, tasks.Definition) {
		f := newRBFFixture(t, -1, "-")
		p := newT6G6Probe(t)
		p.store = t6g6RBFStore{s: f.s}
		p.rec.backing = f.s
		defs := (&Plugin{}).t6g6Kinds(p.deps(), nil)
		return p, f, t6g6ByKind(t, defs, rbfKind)
	}
	params := func(f rbfFixture) string {
		b, err := json.Marshal(map[string]any{"repoints": []rbfRepoint{f.item()}})
		require.NoError(t, err)
		return string(b)
	}

	p, f, def := build(t)
	h, err := runT6G6Kind(t, def, tasks.ModeLive, gate, params(f))
	require.NoError(t, err)
	require.Same(t, gate, p.given, "NewWriter must be given the run's writer")
	require.Equal(t, 1, p.writerCalls())
	t6g6AssertWritesDeclared(t, p.rec.snapshot(), def)
	calls := strings.Join(p.rec.snapshot(), "\n")
	require.Contains(t, calls, "ModifyBookFile "+f.row)
	require.Contains(t, calls, "CreateOperationChange")
	require.Equal(t, f.newPath, f.storedRow(t).FilePath, "the row is repointed")
	require.Equal(t, []tasks.Key{p.standKey}, h.standDowns)
	require.True(t, h.released)

	// Preview plans and writes nothing.
	p, f, def = build(t)
	_, err = runT6G6Kind(t, def, tasks.ModePreview, nil, params(f))
	require.NoError(t, err)
	require.Empty(t, p.rec.snapshot(), "a preview wrote")
	require.Zero(t, p.writerCalls(), "a preview built a writer")
	require.Equal(t, f.oldPath, f.storedRow(t).FilePath, "a preview moved the row")
}

// A live apply that rewrites rows never runs without the scan stand-down.
func TestT6G6More_ApplyRefusesWithoutStandDownKey(t *testing.T) {
	gate := &t6g6GateWriter{name: "gotham"}

	p := newT6G6Probe(t)
	for _, c := range t6g6WriteCases() {
		if c.kind == recoverMissingFilesKind {
			c.setup(t, p)
		}
	}
	p.standKey = ""
	defs := (&Plugin{}).t6g6Kinds(p.deps(), nil)
	_, err := runT6G6Kind(t, t6g6ByKind(t, defs, recoverMissingFilesKind), tasks.ModeLive, gate, "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "stand-down")
	require.Empty(t, p.rec.snapshot(), "recover wrote without a stand-down")

	f := newRBFFixture(t, -1, "-")
	p = newT6G6Probe(t)
	p.store = t6g6RBFStore{s: f.s}
	p.rec.backing = f.s
	p.standKey = ""
	defs = (&Plugin{}).t6g6Kinds(p.deps(), nil)
	b, err := json.Marshal(map[string]any{"repoints": []rbfRepoint{f.item()}})
	require.NoError(t, err)
	_, err = runT6G6Kind(t, t6g6ByKind(t, defs, rbfKind), tasks.ModeLive, gate, string(b))
	require.Error(t, err)
	require.Contains(t, err.Error(), "stand-down")
	require.Empty(t, p.rec.snapshot(), "repoint wrote without a stand-down")
	require.Equal(t, f.oldPath, f.storedRow(t).FilePath)
}

// The three reports write nothing and are declared ReadOnly, so the kernel hands
// them no writer: their shells never ask deps for one.
func TestT6G6More_ReadOnlyKindsBuildNoWriter(t *testing.T) {
	p := newT6G6Probe(t)
	defs := (&Plugin{}).t6g6Kinds(p.deps(), nil)
	for _, kind := range []tasks.Kind{optimizeKind, orphanBookFilesCleanupKind, orphanBookFilesRepointPlanKind} {
		require.True(t, tasks.CommonOf(t6g6ByKind(t, defs, kind)).Effects.IsReadOnly(), "%s", kind)
	}
	for _, kind := range []tasks.Kind{orphanBookFilesCleanupKind, orphanBookFilesRepointPlanKind} {
		_, err := runT6G6Kind(t, t6g6ByKind(t, defs, kind), tasks.ModeLive, nil, "")
		require.NoError(t, err, "%s", kind)
	}
	require.Zero(t, p.writerCalls(), "a ReadOnly kind built a writer")
}

func (s *t6g6TestStore) GetAllBooksCoreComplete(int, int) ([]database.BookCore, error) {
	return nil, nil
}
func (s *t6g6TestStore) ListSoftDeletedBooks(int, int, *time.Time) ([]database.Book, error) {
	return nil, nil
}
func (s *t6g6TestStore) BookAtPathIndexBuilt() (bool, error) { return true, nil }

// Every native shell that writes refuses a mode key in its params, whatever its
// case, before it builds a writer or touches the store: the run's mode comes
// from the start request, never from params (coordinator ruling 3).
func TestT6G6More_WritingShellsRefuseModeKeys(t *testing.T) {
	kinds := []tasks.Kind{
		nightlyCompactActivityLogKind, opChangeBookIndexKind, recompactActivityDigestsKind,
		itunesHealKind, repairMergedUserStateKind, relinkUnlinkedBooksKind, probeDirectoryBooksKind,
		repairTranscribeStatusKind, regroupShatteredAIKind, recoverMissingFilesKind, rbfKind,
	}
	require.Len(t, kinds, 11)
	keys := []string{"dry_run", "dryRun", "DRY_RUN", "DryRun", "apply", "Apply", "APPLY"}
	gate := &t6g6GateWriter{name: "gotham"}
	for _, kind := range kinds {
		for _, key := range keys {
			t.Run(string(kind)+"/"+key, func(t *testing.T) {
				p := newT6G6Probe(t)
				defs := (&Plugin{}).t6g6Kinds(p.deps(), nil)
				_, err := runT6G6Kind(t, t6g6ByKind(t, defs, kind), tasks.ModeLive, gate, `{"`+key+`": true}`)
				require.Error(t, err)
				require.Contains(t, err.Error(), "mode comes from the start request")
				require.Zero(t, p.writerCalls(), "a writer was built")
				require.Empty(t, p.rec.snapshot(), "something was written")
			})
		}
	}
}
