// file: internal/plugins/maintenance/kinds_t6_g7_test.go
// version: 1.0.7
// guid: 4c7e1a92-6b3d-4f58-8a0e-2d9c5b71e3f6
// last-edited: 2026-11-19

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
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
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/operations"
	"github.com/falkcorp/audiobook-organizer/internal/organizer"
	"github.com/falkcorp/audiobook-organizer/internal/tagger"
	"github.com/falkcorp/audiobook-organizer/internal/usermerge"
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
	// bookFiles lets ModifyBookFile run the caller's fn on a copy, by file id.
	bookFiles map[string]*database.BookFile
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

func (r *t6g7Recorder) BatchUpsertBookFiles(files []*database.BookFile) error {
	parts := make([]string, len(files))
	for i, f := range files {
		parts[i] = fmt.Sprintf("%s:trk%d", f.ID, f.TrackNumber)
	}
	sort.Strings(parts)
	r.rec("BatchUpsertBookFiles " + strings.Join(parts, ","))
	return nil
}

func (r *t6g7Recorder) ApplyUserMerge(_ context.Context, req usermerge.ApplyRequest, _ usermerge.Progress) (*usermerge.Journal, error) {
	r.rec(fmt.Sprintf("ApplyUserMerge actor=%s survivor=%s absorbed=%s", req.ActorID, req.SurvivorID, req.AbsorbedID))
	return &usermerge.Journal{ID: "01BATJRNL000000000000000A1", Status: "done"}, nil
}

func (r *t6g7Recorder) ResumeUserMerge(_ context.Context, id string, _ usermerge.Progress) (*usermerge.Journal, error) {
	r.rec("ResumeUserMerge " + id)
	return &usermerge.Journal{ID: id, Status: "done"}, nil
}

func (r *t6g7Recorder) UndoUserMerge(_ context.Context, req usermerge.UndoRequest, _ usermerge.Progress) (*usermerge.Journal, error) {
	r.rec("UndoUserMerge " + req.JournalID)
	return &usermerge.Journal{ID: req.JournalID, Status: "undone"}, nil
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
	line := "ModifyBook " + id + " title=" + cur.Title
	if fmt.Sprint(cur.SeriesID) != fmt.Sprint(b.SeriesID) {
		line += " series=" + t6g7IntPtr(cur.SeriesID)
	}
	if cur.FilePath != b.FilePath {
		line += " path=" + filepath.Base(cur.FilePath)
	}
	r.rec(line)
	return &cur, nil
}

// t6g7IntPtr is n, or "nil".
func t6g7IntPtr(n *int) string {
	if n == nil {
		return "nil"
	}
	return fmt.Sprint(*n)
}

func (r *t6g7Recorder) CreateSeries(name string, authorID *int) (*database.Series, error) {
	r.rec(fmt.Sprintf("CreateSeries %s author=%s", name, t6g7IntPtr(authorID)))
	return &database.Series{ID: 9001, Name: name, AuthorID: authorID}, nil
}

func (r *t6g7Recorder) CreateOperationChange(c *database.OperationChange) error {
	r.rec(fmt.Sprintf("CreateOperationChange %s %s %s->%s", c.BookID, c.FieldName, c.OldValue, c.NewValue))
	return nil
}

func (r *t6g7Recorder) ModifyBookFile(bookID, fileID string, fn func(*database.BookFile) error) (*database.BookFile, error) {
	r.mu.Lock()
	f := r.bookFiles[fileID]
	r.mu.Unlock()
	if f == nil {
		return nil, nil
	}
	cur := *f
	if err := fn(&cur); err != nil {
		if errors.Is(err, database.ErrSkipBookFileWrite) {
			return &cur, nil
		}
		return nil, err
	}
	r.rec(fmt.Sprintf("ModifyBookFile %s path=%s itunes=%s", fileID, filepath.Base(cur.FilePath), cur.ITunesPath))
	return &cur, nil
}

func (r *t6g7Recorder) SetUserPreferenceForUser(userID, key, value string) error {
	r.rec(fmt.Sprintf("SetUserPreferenceForUser %s %s=%q", userID, key, value))
	return nil
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
	// allFiles is what GetAllBookFilesCore answers.
	allFiles []database.BookFileCore
	// admin is the one user id ActorIsAdmin answers true for.
	admin string
	// files and states are what GetBookFiles and GetMetadataFieldStates answer.
	files  map[string][]database.BookFile
	states map[string][]database.MetadataFieldState
	// prefs are the _system preference rows GetAllPreferencesForUser answers.
	prefs []database.UserPreferenceKV
	// books is what GetBookByID answers; bookAt, LiveBookIDsAtPath by path.
	books  map[string]*database.Book
	bookAt map[string][]string
	// refCounts, series, trashed and seriesByName are the series census reads.
	refCounts    map[int]int
	series       []database.Series
	trashed      []database.Book
	seriesByName map[string]*database.Series
}

func (s *t6g7TestStore) GetAllSeriesBookRefCounts() (map[int]int, error) { return s.refCounts, nil }
func (s *t6g7TestStore) GetAllSeries() ([]database.Series, error)        { return s.series, nil }
func (s *t6g7TestStore) ListSoftDeletedBooks(int, int, *time.Time) ([]database.Book, error) {
	return s.trashed, nil
}
func (s *t6g7TestStore) GetSeriesByName(name string, _ *int) (*database.Series, error) {
	return s.seriesByName[name], nil
}
func (s *t6g7TestStore) GetUserPreference(string) (*database.UserPreference, error) { return nil, nil }

func (s *t6g7TestStore) GetAllPreferencesForUser(string) ([]database.UserPreferenceKV, error) {
	return s.prefs, nil
}

func (s *t6g7TestStore) LiveBookIDsAtPath(path string) ([]string, error) { return s.bookAt[path], nil }

// GetBookByID answers a copy: the series repoint applies its edit to the row it
// read (to test the field locks), which must not reach the stored row.
func (s *t6g7TestStore) GetBookByID(id string) (*database.Book, error) {
	b := s.books[id]
	if b == nil {
		return nil, nil
	}
	cp := *b
	return &cp, nil
}

func (s *t6g7TestStore) GetBookFileByID(bookID, fileID string) (*database.BookFile, error) {
	for i := range s.files[bookID] {
		if s.files[bookID][i].ID == fileID {
			f := s.files[bookID][i]
			return &f, nil
		}
	}
	return nil, nil
}

func (s *t6g7TestStore) GetAllBookFilesCoreIncludingRetired() ([]database.BookFileCore, error) {
	return s.allFiles, nil
}

func (s *t6g7TestStore) GetBookFiles(id string) ([]database.BookFile, error) { return s.files[id], nil }

func (s *t6g7TestStore) GetAllBookFilesCore() ([]database.BookFileCore, error) {
	return s.allFiles, nil
}
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

func (s *t6g7TestStore) ActorIsAdmin(id string) (bool, error) { return id == s.admin, nil }

func (s *t6g7TestStore) BuildPlan(context.Context, usermerge.PlanRequest) (*usermerge.Plan, error) {
	return &usermerge.Plan{Ops: 3}, nil
}

func (s *t6g7TestStore) LoadJournal(id string) (*usermerge.Journal, error) {
	return &usermerge.Journal{ID: id, Status: "interrupted"}, nil
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
	// seriesInvalidated counts InvalidateSeriesCache calls.
	seriesInvalidated int
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
		// A report lands in the run's root under the file name the kind derived.
		ResolveReportPath: func(explicit, fileName string) (string, error) {
			if explicit != "" {
				return filepath.Join(p.root, explicit), nil
			}
			return filepath.Join(p.root, fileName), nil
		},
		InvalidateSeriesCache: func() { p.mu.Lock(); p.seriesInvalidated++; p.mu.Unlock() },
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
	mode  tasks.Mode
	w     tasks.Writer
	actor tasks.Actor
	// resumed is what the run reports for Resumed.
	resumed bool

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

func (h *t6g7Host) Actor() tasks.Actor   { return h.actor }
func (h *t6g7Host) Resumed() bool        { return h.resumed }
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
	return runT6G7KindAs(t, d, mode, w, params, tasks.Actor{})
}

// runT6G7KindAs is runT6G7Kind started by actor.
func runT6G7KindAs(t *testing.T, d tasks.Definition, mode tasks.Mode, w tasks.Writer, params string, actor tasks.Actor) (*t6g7Host, error) {
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
	h := &t6g7Host{FakeRun: taskstest.NewFakeRun(context.Background(), d.Kind()), mode: mode, w: w, actor: actor}
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
	// series-phantom-repair also writes its report TSV: a file.
	add(p.seriesPhantomRepairDef(), t6g7KindWrite{writers: []reflect.Type{reflect.TypeFor[seriesPhantomWriter]()}, outside: []ops.Resource{ops.ResFiles}})
	add(p.repointUnrecordedRenamesDef(), t6g7KindWrite{writers: []reflect.Type{reflect.TypeFor[unrecordedRenameWriter]()}})
	add(p.tagBackfillDef(), t6g7KindWrite{writers: []reflect.Type{reflect.TypeFor[tagBackfillWriter]()}})
	add(p.userMergeDef(), t6g7KindWrite{writers: []reflect.Type{reflect.TypeFor[userMergeWriter]()}})
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
	require.Len(t, defs, 11, "grows to 15 as the group's definitions land")

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
	tagBackfillPar := tagBackfillParallelism()
	repointPar := repointUnrecordedRenamesParallelism()
	seriesPhantomPar := seriesPhantomRepairParallelism()
	rows := []row{
		{head: p.reviewStatusIndexRepairDef(), resume: tasks.ResumeDrop, timeout: 30 * time.Minute},
		{head: p.unknownAuthorAuditDef(), resume: tasks.ResumeDrop, timeout: 30 * time.Minute},
		{head: p.versionGroupPrimaryReportDef(), resume: tasks.ResumeDrop, timeout: 10 * time.Minute},
		{head: p.reportsPruneDef(), resume: tasks.ResumeDrop, timeout: time.Hour},
		{head: p.titleBackfillDef(), resume: tasks.ResumeDrop, timeout: 30 * time.Minute},
		{head: p.bulkWriteBackDef(), resume: tasks.ResumeAsk, timeout: 240 * time.Minute},
		{head: p.userMergeDef(), resume: tasks.ResumeDrop, timeout: 2 * time.Hour},
		// HEAD ran ResumeRestart and reads no checkpoint on restart: from zero.
		{head: p.seriesPhantomRepairDef(), resume: tasks.ResumeFromZero, timeout: 2 * time.Hour,
			par: &seriesPhantomPar, wantPar: "cpu"},
		// HEAD's loop is four workers over units of records that share a
		// destination path.
		{head: p.repointUnrecordedRenamesDef(), resume: tasks.ResumeDrop, timeout: 30 * time.Minute,
			par: &repointPar, wantPar: "fixed(4): "},
		{head: p.tagBackfillDef(), resume: tasks.ResumeDrop, timeout: 120 * time.Minute,
			par: &tagBackfillPar, wantPar: fmt.Sprintf("fixed(%d): ", runtime.NumCPU()*4)},
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
			kind: tagBackfillKind,
			setup: func(t *testing.T, p *t6g7Probe) {
				// One book of two files whose tags carry the opposite track order:
				// distinct across the book, so the tag positions are taken for both.
				book := "01BATBOOK00000000000000T1"
				f1, f2 := "01BATFILE00000000000000T11", "01BATFILE00000000000000T12"
				rows := []database.BookFile{
					{ID: f1, BookID: book, FilePath: filepath.Join(p.root, f1+".mp3"), TrackNumber: 2},
					{ID: f2, BookID: book, FilePath: filepath.Join(p.root, f2+".mp3"), TrackNumber: 1},
				}
				for _, r := range rows {
					mustWriteFile(t, r.FilePath)
				}
				metadata.SetMetadataExtractor(&mapTagExtractor{byBase: map[string]metadata.Metadata{
					f1 + ".mp3": tagMeta(1, 2, 0, 0), f2 + ".mp3": tagMeta(2, 2, 0, 0)}})
				t.Cleanup(func() { drainAbandonedTagReads(t); metadata.SetMetadataExtractor(nil) })
				p.store = &t6g7TestStore{
					allFiles: []database.BookFileCore{rows[0].Core(), rows[1].Core()},
					files:    map[string][]database.BookFile{book: rows},
				}
			},
			live: []string{"BatchUpsertBookFiles 01BATFILE00000000000000T11:trk1,01BATFILE00000000000000T12:trk2"},
		},
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
			kind:       seriesPhantomRepairKind,
			params:     `{"mode":"null"}`,
			standsDown: true,
			setup: func(t *testing.T, p *t6g7Probe) {
				seedSeriesPhantomRig(p, 7)
			},
			live: []string{
				"CreateOperationChange 01BATBOOK00000000000000S1 series_id 7->",
				"ModifyBook 01BATBOOK00000000000000S1 title=The Killing Joke series=nil",
			},
		},
		{
			kind: repointUnrecordedRenamesKind,
			setup: func(t *testing.T, p *t6g7Probe) {
				seedRepointRig(t, p, "01BATBOOK00000000000000R1", "01BATFILE00000000000000R11", true)
			},
			live: []string{
				"ModifyBookFile 01BATFILE00000000000000R11 path=new-01.mp3 itunes=file:///gotham/new-01.mp3",
				`SetUserPreferenceForUser _system ` + organizer.RenamePathWriteFailureKey("01BATBOOK00000000000000R1", "01BATFILE00000000000000R11") + `=""`,
			},
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
	kinds := []tasks.Kind{reviewStatusIndexRepairKind, reportsPruneKind, titleBackfillKind, titleRepairKind, t6g7KindFor("maintenance.bulk-write-back"), t6g7KindFor("maintenance.user-merge"), tagBackfillKind, repointUnrecordedRenamesKind, seriesPhantomRepairKind}
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

const t6g7MergeActor = "01BATUSER00000000000000AD1"

func t6g7MergeGrant(t *testing.T, g usermerge.Grant) string {
	t.Helper()
	g.ActorID = t6g7MergeActor
	tok, err := usermerge.DefaultGrants.Issue(g)
	require.NoError(t, err)
	return tok
}

func t6g7UserMergeDef(t *testing.T, p *t6g7Probe) tasks.Definition {
	t.Helper()
	return t6g7ByKind(t, (&Plugin{}).t6g7Kinds(p.deps(), nil), t6g7KindFor("maintenance.user-merge"))
}

// A live apply redeems the grant the Users page minted for this actor, then
// applies through the run's writer: the actor is the run's own, never a param.
func TestT6G7Kinds_UserMergeAppliesWithAGrantForTheRunsActor(t *testing.T) {
	p := newT6G7Probe(t)
	p.store = &t6g7TestStore{admin: t6g7MergeActor}
	g := usermerge.Grant{Action: usermerge.ActionApply, SurvivorID: "01BATUSER00000000000000S01", AbsorbedID: "01BATUSER00000000000000A01", Digest: "d1"}
	tok := t6g7MergeGrant(t, g)
	params := `{"survivor_id":"` + g.SurvivorID + `","absorbed_id":"` + g.AbsorbedID + `","digest":"d1","grant":"` + tok + `"}`
	h, err := runT6G7KindAs(t, t6g7UserMergeDef(t, p), tasks.ModeLive, &t6g7GateWriter{name: "gotham"}, params, tasks.Actor{UserID: t6g7MergeActor})
	require.NoError(t, err)
	require.Equal(t, []string{"ApplyUserMerge actor=" + t6g7MergeActor + " survivor=" + g.SurvivorID + " absorbed=" + g.AbsorbedID}, p.rec.snapshot())
	require.Contains(t, string(h.result), "01BATJRNL000000000000000A1", "the run's result is the journal's redacted form")
}

// Without the grant the Users page mints, a live run writes nothing, and it
// builds no writer: the kernel path is no way around the route's checks.
func TestT6G7Kinds_UserMergeLiveWithoutAGrantWritesNothing(t *testing.T) {
	p := newT6G7Probe(t)
	p.store = &t6g7TestStore{admin: t6g7MergeActor}
	_, err := runT6G7KindAs(t, t6g7UserMergeDef(t, p), tasks.ModeLive, &t6g7GateWriter{name: "gotham"},
		`{"survivor_id":"s","absorbed_id":"a","digest":"d1","grant":"forged"}`, tasks.Actor{UserID: t6g7MergeActor})
	require.Error(t, err)
	require.Empty(t, p.rec.snapshot())
}

// A grant is bound to the actor it was minted for.
func TestT6G7Kinds_UserMergeGrantIsBoundToTheActor(t *testing.T) {
	p := newT6G7Probe(t)
	p.store = &t6g7TestStore{admin: "01BATUSER00000000000000AD2"}
	tok := t6g7MergeGrant(t, usermerge.Grant{Action: usermerge.ActionApply, SurvivorID: "s", AbsorbedID: "a", Digest: "d1"})
	_, err := runT6G7KindAs(t, t6g7UserMergeDef(t, p), tasks.ModeLive, &t6g7GateWriter{name: "gotham"},
		`{"survivor_id":"s","absorbed_id":"a","digest":"d1","grant":"`+tok+`"}`, tasks.Actor{UserID: "01BATUSER00000000000000AD2"})
	require.Error(t, err)
	require.Empty(t, p.rec.snapshot())
}

// An actor who is not an active admin is refused, in preview too: a preview reads
// two accounts' data.
func TestT6G7Kinds_UserMergeRefusesAnActorWhoIsNotAnAdmin(t *testing.T) {
	for _, mode := range []tasks.Mode{tasks.ModePreview, tasks.ModeLive} {
		p := newT6G7Probe(t)
		p.store = &t6g7TestStore{admin: "someone-else"}
		_, err := runT6G7KindAs(t, t6g7UserMergeDef(t, p), mode, &t6g7GateWriter{name: "gotham"}, `{}`, tasks.Actor{UserID: t6g7MergeActor})
		require.ErrorContains(t, err, "not an active admin", "mode %v", mode)
		require.Empty(t, p.rec.snapshot())
	}
}

// Preview is the default: it reports the plan and writes nothing, building no
// writer.
func TestT6G7Kinds_UserMergePreviewReportsThePlanAndWritesNothing(t *testing.T) {
	p := newT6G7Probe(t)
	p.store = &t6g7TestStore{admin: t6g7MergeActor}
	h, err := runT6G7KindAs(t, t6g7UserMergeDef(t, p), tasks.ModePreview, nil, `{"survivor_id":"s","absorbed_id":"a"}`, tasks.Actor{UserID: t6g7MergeActor})
	require.NoError(t, err)
	require.Empty(t, p.rec.snapshot())
	require.Zero(t, p.writerCalls())
	require.NotEmpty(t, h.result, "the plan summary is the result")
}

// A run that was resumed is not fresh: a merge never runs twice from one click.
func TestT6G7Kinds_UserMergeRefusesAResumedRun(t *testing.T) {
	p := newT6G7Probe(t)
	p.store = &t6g7TestStore{admin: t6g7MergeActor}
	tok := t6g7MergeGrant(t, usermerge.Grant{Action: usermerge.ActionResume, JournalID: "01BATJRNL000000000000000A2"})
	d := t6g7UserMergeDef(t, p)
	r, ok := d.(interface {
		ExecutorRunners() (func(json.RawMessage) (any, error), *tasks.TaskSpec[any, json.RawMessage], *tasks.BatchSpec[any, any, json.RawMessage], *tasks.FixerSpec[any, any])
	})
	require.True(t, ok)
	decode, task, _, _ := r.ExecutorRunners()
	params, err := decode(json.RawMessage(`{"action":"resume","journal_id":"01BATJRNL000000000000000A2","grant":"` + tok + `"}`))
	require.NoError(t, err)
	h := &t6g7Host{FakeRun: taskstest.NewFakeRun(context.Background(), d.Kind()), mode: tasks.ModeLive, w: &t6g7GateWriter{name: "gotham"}, actor: tasks.Actor{UserID: t6g7MergeActor}, resumed: true}
	_, err = task.Run(tasks.NewRC(h), params)
	require.ErrorContains(t, err, "never retried in place")
	require.Empty(t, p.rec.snapshot())
}

// seedRepointRig seeds one unrecorded rename of one book_file row: the file
// moved from old-01.mp3 to new-01.mp3 (the new one on disk, the old one gone),
// and the row still holds the old path. The probe's store and recorder both see
// the row.
func seedRepointRig(t *testing.T, p *t6g7Probe, bookID, fileID string, rowAtOld bool) (oldPath, newPath, key string) {
	t.Helper()
	prev := config.Snapshot()
	config.Mutate(func(c *config.Config) { c.ITunes = config.ITunesConfig{} })
	t.Cleanup(func() { config.Mutate(func(c *config.Config) { *c = prev }) })
	oldPath, newPath = filepath.Join(p.root, "old-01.mp3"), filepath.Join(p.root, "new-01.mp3")
	mustWriteFile(t, newPath)
	held := oldPath
	if !rowAtOld {
		held = filepath.Join(p.root, "elsewhere-01.mp3")
	}
	row := database.BookFile{ID: fileID, BookID: bookID, FilePath: held}
	rec := organizer.RenamePathWriteFailure{BookID: bookID, BookFileID: fileID, OldPath: oldPath, NewPath: newPath,
		NewITunesPath: "file:///gotham/new-01.mp3", RecordedAt: "2026-11-19T00:00:00Z"}
	body, err := json.Marshal(rec)
	require.NoError(t, err)
	key = organizer.RenamePathWriteFailureKey(bookID, fileID)
	p.store = &t6g7TestStore{
		prefs:    []database.UserPreferenceKV{{UserID: "_system", Key: key, Value: string(body)}},
		files:    map[string][]database.BookFile{bookID: {row}},
		allFiles: []database.BookFileCore{row.Core()},
	}
	p.rec.bookFiles = map[string]*database.BookFile{fileID: &row}
	return oldPath, newPath, key
}

func runRepointKind(t *testing.T, p *t6g7Probe, mode tasks.Mode) (repointUnrecordedRenamesResult, error) {
	t.Helper()
	def := t6g7ByKind(t, (&Plugin{}).t6g7Kinds(p.deps(), nil), repointUnrecordedRenamesKind)
	h, err := runT6G7Kind(t, def, mode, &t6g7GateWriter{name: "gotham"}, `{}`)
	var res repointUnrecordedRenamesResult
	if h != nil && len(h.result) > 0 {
		require.NoError(t, json.Unmarshal(h.result, &res))
	}
	return res, err
}

// A Preview run reports what a Live run would repoint and takes no writer: the
// decision is made on a read of the row, and no record is cleared.
func TestT6G7Kinds_RepointUnrecordedRenamesPreviewDecidesOnARead(t *testing.T) {
	p := newT6G7Probe(t)
	seedRepointRig(t, p, "01BATBOOK00000000000000R2", "01BATFILE00000000000000R21", true)
	res, err := runRepointKind(t, p, tasks.ModePreview)
	require.NoError(t, err)
	require.False(t, res.Apply)
	require.Equal(t, 1, res.Records)
	require.Equal(t, map[string]int{repointOutcomeWouldRepoint: 1}, res.Outcomes)
	require.Zero(t, p.writerCalls(), "a preview built a writer")
	require.Empty(t, p.rec.snapshot())
}

// A row that no longer holds the recorded old path is left alone and its record
// kept, in a Live run and in a Preview alike; a row already at the new path
// counts as repointed-already and its record is cleared (live only).
func TestT6G7Kinds_RepointUnrecordedRenamesLeavesAChangedRowAndItsRecord(t *testing.T) {
	for _, mode := range []tasks.Mode{tasks.ModeLive, tasks.ModePreview} {
		p := newT6G7Probe(t)
		seedRepointRig(t, p, "01BATBOOK00000000000000R3", "01BATFILE00000000000000R31", false)
		res, err := runRepointKind(t, p, mode)
		require.NoError(t, err)
		require.Equal(t, map[string]int{repointOutcomeRowChanged: 1}, res.Outcomes, fmt.Sprint(mode))
		require.Empty(t, p.rec.snapshot(), "nothing is written or cleared for a changed row (%s)", mode)
	}
}

// A record whose old path is still on disk (the files may have been moved back)
// is skipped and kept.
func TestT6G7Kinds_RepointUnrecordedRenamesKeepsARecordWhoseOldPathStillExists(t *testing.T) {
	p := newT6G7Probe(t)
	oldPath, _, _ := seedRepointRig(t, p, "01BATBOOK00000000000000R4", "01BATFILE00000000000000R41", true)
	mustWriteFile(t, oldPath)
	res, err := runRepointKind(t, p, tasks.ModeLive)
	require.NoError(t, err)
	require.Equal(t, map[string]int{repointOutcomeOldPresent: 1}, res.Outcomes)
	require.Empty(t, p.rec.snapshot())
}

// The destination another book_file already holds is never taken.
func TestT6G7Kinds_RepointUnrecordedRenamesRefusesAClaimedDestination(t *testing.T) {
	p := newT6G7Probe(t)
	_, newPath, _ := seedRepointRig(t, p, "01BATBOOK00000000000000R5", "01BATFILE00000000000000R51", true)
	st := p.store.(*t6g7TestStore)
	st.allFiles = append(st.allFiles, database.BookFileCore{ID: "01BATFILE00000000000000R59", BookID: "01BATBOOK00000000000000R6", FilePath: newPath})
	res, err := runRepointKind(t, p, tasks.ModeLive)
	require.NoError(t, err)
	require.Equal(t, map[string]int{repointOutcomeNewClaimed: 1}, res.Outcomes)
	require.Empty(t, p.rec.snapshot())
}

// A Live run without a writer factory refuses before it reads a record.
func TestT6G7Kinds_RepointUnrecordedRenamesLiveNeedsAWriter(t *testing.T) {
	p := newT6G7Probe(t)
	seedRepointRig(t, p, "01BATBOOK00000000000000R7", "01BATFILE00000000000000R71", true)
	deps := p.deps()
	deps.NewWriter = nil
	def := t6g7ByKind(t, (&Plugin{}).t6g7Kinds(deps, nil), repointUnrecordedRenamesKind)
	_, err := runT6G7Kind(t, def, tasks.ModeLive, &t6g7GateWriter{name: "gotham"}, `{}`)
	require.ErrorContains(t, err, "NewWriter is not set")
}

// seedSeriesPhantomRig seeds one book holding phantom series id phantom (no
// series row has it): the census sees it through the store, the recorder can
// write it.
func seedSeriesPhantomRig(p *t6g7Probe, phantom int) {
	id := "01BATBOOK00000000000000S1"
	author := 5
	core := database.BookCore{ID: id, Title: "The Killing Joke", AuthorID: &author, SeriesID: &phantom}
	book := &database.Book{ID: id, Title: "The Killing Joke", AuthorID: &author, SeriesID: &phantom}
	p.store = &t6g7TestStore{
		refCounts: map[int]int{phantom: 1},
		cores:     []database.BookCore{core},
		books:     map[string]*database.Book{id: book},
	}
	p.rec.books = map[string]*database.Book{id: book}
}

func runSeriesPhantomKind(t *testing.T, p *t6g7Probe, mode tasks.Mode, params string) (*seriesPhantomRepairResult, error) {
	t.Helper()
	def := t6g7ByKind(t, (&Plugin{}).t6g7Kinds(p.deps(), nil), seriesPhantomRepairKind)
	h, err := runT6G7Kind(t, def, mode, &t6g7GateWriter{name: "gotham"}, params)
	var res *seriesPhantomRepairResult
	if h != nil && len(h.result) > 0 {
		require.NoError(t, json.Unmarshal(h.result, &res))
	}
	return res, err
}

// Mode "report" only reports: a Live run of it is refused before any writer or
// stand-down exists, as HEAD's start check refused it; a Preview run reports the
// census and writes its TSV, and nothing else.
func TestT6G7Kinds_SeriesPhantomReportModeReportsAndNeverWrites(t *testing.T) {
	p := newT6G7Probe(t)
	seedSeriesPhantomRig(p, 7)
	_, err := runSeriesPhantomKind(t, p, tasks.ModeLive, `{}`)
	require.ErrorContains(t, err, "needs mode")
	require.Zero(t, p.writerCalls(), "a refused run built a writer")

	res, err := runSeriesPhantomKind(t, p, tasks.ModePreview, `{"report_path":"phantoms.tsv"}`)
	require.NoError(t, err)
	require.Equal(t, seriesPhantomModeReport, res.Mode)
	require.True(t, res.DryRun)
	require.Equal(t, 1, res.PhantomSeries)
	require.Equal(t, 1, res.BooksLive)
	require.Equal(t, filepath.Join(p.root, "phantoms.tsv"), res.ReportPath)
	raw, rerr := os.ReadFile(res.ReportPath)
	require.NoError(t, rerr)
	require.Contains(t, string(raw), "\t01BATBOOK00000000000000S1\tThe Killing Joke\tfalse\t5\n")
	require.Zero(t, p.writerCalls())
	require.Empty(t, p.rec.snapshot())
	require.Zero(t, p.seriesInvalidated)
}

// A Live "null" run clears the id through the writer and drops the series
// caches once; a Preview of it counts what it would clear and touches nothing.
func TestT6G7Kinds_SeriesPhantomNullClearsThroughTheWriterAndInvalidatesCaches(t *testing.T) {
	p := newT6G7Probe(t)
	seedSeriesPhantomRig(p, 7)
	res, err := runSeriesPhantomKind(t, p, tasks.ModePreview, `{"mode":"null"}`)
	require.NoError(t, err)
	require.True(t, res.DryRun)
	require.Equal(t, 1, res.Nulled, "a preview counts what a live run would clear")
	require.Empty(t, p.rec.snapshot())
	require.Zero(t, p.seriesInvalidated)

	res, err = runSeriesPhantomKind(t, p, tasks.ModeLive, `{"mode":"null"}`)
	require.NoError(t, err)
	require.False(t, res.DryRun)
	require.Equal(t, 1, res.Nulled)
	require.Equal(t, 1, p.seriesInvalidated)
}

// "recreate" creates the named series under the holders' majority author and
// repoints the holders to it; a phantom id with no name is skipped and counted.
func TestT6G7Kinds_SeriesPhantomRecreateNamesTheSeriesAndSkipsTheUnnamed(t *testing.T) {
	p := newT6G7Probe(t)
	seedSeriesPhantomRig(p, 7)
	st := p.store.(*t6g7TestStore)
	// A second phantom id, held by one book, that nobody gave a name.
	other := 8
	st.refCounts[other] = 1
	st.cores = append(st.cores, database.BookCore{ID: "01BATBOOK00000000000000S2", Title: "Year Two", SeriesID: &other})
	res, err := runSeriesPhantomKind(t, p, tasks.ModeLive, `{"mode":"recreate","names":{"7":"Year One"}}`)
	require.NoError(t, err)
	require.Equal(t, 1, res.SeriesRecreated)
	require.Equal(t, 1, res.Repointed)
	require.Equal(t, 1, res.Skipped["no_name"], "the unnamed phantom id's holder is skipped and counted")
	require.Equal(t, []string{
		"CreateSeries Year One author=5",
		"CreateOperationChange 01BATBOOK00000000000000S1 series_id 7->9001",
		"ModifyBook 01BATBOOK00000000000000S1 title=The Killing Joke series=9001",
	}, p.rec.snapshot())
}

// A live run that cannot be fenced (no stand-down key) refuses to write.
func TestT6G7Kinds_SeriesPhantomLiveNeedsTheScanStandDown(t *testing.T) {
	p := newT6G7Probe(t)
	seedSeriesPhantomRig(p, 7)
	deps := p.deps()
	deps.StandDownKey = ""
	def := t6g7ByKind(t, (&Plugin{}).t6g7Kinds(deps, nil), seriesPhantomRepairKind)
	_, err := runT6G7Kind(t, def, tasks.ModeLive, &t6g7GateWriter{name: "gotham"}, `{"mode":"null"}`)
	require.ErrorContains(t, err, "stand")
	require.Empty(t, p.rec.snapshot())
}
