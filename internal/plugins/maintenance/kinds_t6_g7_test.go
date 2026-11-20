// file: internal/plugins/maintenance/kinds_t6_g7_test.go
// version: 1.0.11
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
	"github.com/falkcorp/audiobook-organizer/internal/testutil/durtest"
	"github.com/falkcorp/audiobook-organizer/internal/usermerge"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary"
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
	// opChangeErr fails CreateOperationChange (nothing is recorded for the row).
	opChangeErr error
	// keepSeries makes DeleteSeriesIfUnreferenced answer "still referenced".
	keepSeries bool
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
	if fmt.Sprint(cur.SeriesSequence) != fmt.Sprint(b.SeriesSequence) {
		line += " seq=" + t6g7IntPtr(cur.SeriesSequence)
	}
	if cur.FilePath != b.FilePath {
		line += " path=" + filepath.Base(cur.FilePath)
	}
	if t6g7StrPtr(cur.SourceImportPath) != t6g7StrPtr(b.SourceImportPath) {
		line += " src=" + filepath.Base(t6g7StrPtr(cur.SourceImportPath))
	}
	if storedPrimaryFlag(cur.IsPrimaryVersion) != storedPrimaryFlag(b.IsPrimaryVersion) {
		line += " primary=" + storedPrimaryFlag(cur.IsPrimaryVersion)
	}
	r.rec(line)
	return &cur, nil
}

// RecordMetadataChange records the history row a write of the repair leaves.
func (r *t6g7Recorder) RecordMetadataChange(rec *database.MetadataChangeRecord) error {
	prev, next := "", ""
	if rec.PreviousValue != nil {
		prev = *rec.PreviousValue
	}
	if rec.NewValue != nil {
		next = *rec.NewValue
	}
	r.rec(fmt.Sprintf("RecordMetadataChange %s %s %s->%s", rec.BookID, rec.Field, prev, next))
	return nil
}

// RecordPathChange records the move ledger row a rewrite leaves.
func (r *t6g7Recorder) RecordPathChange(c *database.BookPathChange) error {
	r.rec(fmt.Sprintf("RecordPathChange %s %s %s->%s", c.BookID, c.ChangeType, c.OldPath, c.NewPath))
	return nil
}

// UpdateBookFiles records one book's batch: each row as id>file name, whether it
// is marked missing and its duration, in the order the kind wrote them. Every row
// is reported applied, as the store reports a clean write.
func (r *t6g7Recorder) UpdateBookFiles(_ context.Context, files []*database.BookFile, afterRow func(i int, applied bool)) (int, error) {
	parts := make([]string, len(files))
	book := ""
	for i, f := range files {
		book = f.BookID
		parts[i] = fmt.Sprintf("%s>%s missing=%v dur=%d", f.ID, filepath.Base(f.FilePath), f.Missing, durtest.Sec(f.Duration))
	}
	r.rec("UpdateBookFiles " + book + " " + strings.Join(parts, ","))
	for i := range files {
		if afterRow != nil {
			afterRow(i, true)
		}
	}
	return len(files), nil
}

// t6g7StrPtr is *s, or "" for nil.
func t6g7StrPtr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
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

// DeleteSeriesIfUnreferenced records the delete and reports it done, unless the
// test made the series still referenced.
func (r *t6g7Recorder) DeleteSeriesIfUnreferenced(id int, expectName string, _ *int) (bool, error) {
	if r.keepSeries {
		return false, nil
	}
	r.rec(fmt.Sprintf("DeleteSeriesIfUnreferenced %d %s", id, expectName))
	return true, nil
}

func (r *t6g7Recorder) CreateOperationChange(c *database.OperationChange) error {
	if r.opChangeErr != nil {
		return r.opChangeErr
	}
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
	// scanStatus, when set, adds a library.scan operation in that status to what
	// ListActiveOperationsV2 answers; activeErr fails the read.
	scanStatus string
	activeErr  error
	// atPathUnbuilt reports the book_atpath index as not built.
	atPathUnbuilt bool
	// counts, members, perSeriesRefs and authors are the series-denumber reads;
	// applyReadyErr is what SeriesApplyReady refuses with.
	counts        map[int]int
	members       map[int][]database.BookCore
	perSeriesRefs map[int]int
	authors       []database.Author
	applyReadyErr error
}

func (s *t6g7TestStore) GetAllSeriesBookCounts() (map[int]int, error) { return s.counts, nil }
func (s *t6g7TestStore) GetAllAuthors() ([]database.Author, error)    { return s.authors, nil }
func (s *t6g7TestStore) GetSeriesBookRefCount(id int) (int, error)    { return s.perSeriesRefs[id], nil }
func (s *t6g7TestStore) SeriesApplyReady() error                      { return s.applyReadyErr }
func (s *t6g7TestStore) GetSeriesByID(id int) (*database.Series, error) {
	for i := range s.series {
		if s.series[i].ID == id {
			cp := s.series[i]
			return &cp, nil
		}
	}
	return nil, nil
}
func (s *t6g7TestStore) GetBooksBySeriesIDsAllVersions(ids []int) (map[int][]database.BookCore, error) {
	out := map[int][]database.BookCore{}
	for _, id := range ids {
		out[id] = append([]database.BookCore(nil), s.members[id]...)
	}
	return out, nil
}

// BookFilesAtPath answers every seeded row (by book) whose file is at path.
func (s *t6g7TestStore) BookFilesAtPath(path string) ([]database.BookFile, error) {
	var out []database.BookFile
	for _, rows := range s.files {
		for i := range rows {
			if rows[i].FilePath == path {
				out = append(out, rows[i])
			}
		}
	}
	return out, nil
}

func (s *t6g7TestStore) BookAtPathIndexBuilt() (bool, error) { return !s.atPathUnbuilt, nil }

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

// GetBookFiles answers a copy, as a store decodes fresh rows per call: a caller
// that edits a returned row (the repoint does, before it writes) must not edit
// the seed another worker is reading.
func (s *t6g7TestStore) GetBookFiles(id string) ([]database.BookFile, error) {
	return append([]database.BookFile(nil), s.files[id]...), nil
}

func (s *t6g7TestStore) GetAllBookFilesCore() ([]database.BookFileCore, error) {
	return s.allFiles, nil
}
func (s *t6g7TestStore) GetMetadataFieldStates(id string) ([]database.MetadataFieldState, error) {
	return s.states[id], nil
}

func (s *t6g7TestStore) ListActiveOperationsV2() ([]database.OperationV2Row, error) {
	if s.activeErr != nil {
		return nil, s.activeErr
	}
	rows := make([]database.OperationV2Row, 0, len(s.active)+1)
	for _, id := range s.active {
		rows = append(rows, database.OperationV2Row{ID: id})
	}
	if s.scanStatus != "" {
		rows = append(rows, database.OperationV2Row{ID: "01BATSCAN000000000000000S1", DefID: "library.scan", Status: s.scanStatus})
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
	// prober is the chapter counter ChapterProber hands out; probeErr is its error.
	prober   versionprimary.ChapterProber
	probeErr error
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
		ChapterProber:      func() (versionprimary.ChapterProber, error) { return p.prober, p.probeErr },
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
	add(p.versionGroupPrimaryRepairDef(), t6g7KindWrite{writers: []reflect.Type{reflect.TypeFor[vgPrimaryRepairWriter]()}})
	// rewrite-path-prefix also writes its per-row report TSV, in Preview too: a file.
	add(p.rewritePathPrefixDef(), t6g7KindWrite{writers: []reflect.Type{reflect.TypeFor[rewritePathPrefixWriter]()}, outside: []ops.Resource{ops.ResFiles}})
	// repoint-missing-to-folder-audio also writes its per-row report TSV, in Preview
	// too: a file.
	add(p.repointMissingToFolderAudioDef(), t6g7KindWrite{writers: []reflect.Type{reflect.TypeFor[rfWriter]()}, outside: []ops.Resource{ops.ResFiles}})
	// series-denumber also writes its candidate report and the .applied outcome
	// report: files, in Preview too.
	add(p.seriesDenumberDef(), t6g7KindWrite{writers: []reflect.Type{reflect.TypeFor[seriesDenumberWriter]()}, outside: []ops.Resource{ops.ResFiles}})
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
	require.Len(t, defs, 15, "every definition of the group")

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
		// more are the kind's other pools, when it runs more than one at different
		// sizes.
		more []struct {
			par     tasks.Parallelism
			wantPar string
		}
	}
	titleRepairPar := titleRepairParallelism()
	tagBackfillPar := tagBackfillParallelism()
	repointPar := repointUnrecordedRenamesParallelism()
	seriesPhantomPar := seriesPhantomRepairParallelism()
	vgPrimaryPar := vgPrimaryRepairParallelism()
	rewritePar := rewritePathPrefixParallelism()
	rfStatPar := rfStatParallelism()
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
		// HEAD's loop is vgRepairWorkers (4) groups at a time, set explicitly because
		// RunItems defaults to one. HEAD is ResumeDrop, so the kind is too.
		{head: p.versionGroupPrimaryRepairDef(), resume: tasks.ResumeDrop, timeout: 2 * time.Hour,
			par: &vgPrimaryPar, wantPar: "fixed(4): "},
		// HEAD's two RunItems pools (the check sweep and the write phase) are both 24
		// workers; the def sets no timeout. HEAD is ResumeDrop.
		{head: p.rewritePathPrefixDef(), resume: tasks.ResumeDrop,
			par: &rewritePar, wantPar: "fixed(24): "},
		// HEAD's three RunItems pools: the stat sweep (missingFileStatConcurrency, 24),
		// the read-only plan (4) and the one-book-per-item write (4). No timeout is
		// set; HEAD is ResumeDrop.
		{head: p.repointMissingToFolderAudioDef(), resume: tasks.ResumeDrop,
			par: &rfStatPar, wantPar: "fixed(24): ", more: []struct {
				par     tasks.Parallelism
				wantPar string
			}{{rfPlanParallelism(), "fixed(4): "}, {rfWriteParallelism(), "fixed(4): "}}},
		// HEAD's apply is one sequential loop over plans (a plan's books and the
		// delete of its emptied series depend on the plans before it), so the kind
		// declares no pool. HEAD is ResumeDrop.
		{head: p.seriesDenumberDef(), resume: tasks.ResumeDrop, timeout: 2 * time.Hour},
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
		for _, m := range r.more {
			require.True(t, strings.HasPrefix(m.par.String(), m.wantPar), "%s parallelism %q, want prefix %q", id, m.par.String(), m.wantPar)
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
			kind:       vgPrimaryRepairKind,
			params:     `{"group_ids":["vg-double"]}`,
			standsDown: true,
			setup: func(t *testing.T, p *t6g7Probe) {
				seedVGPrimaryRig(t, p)
			},
			live: vgPrimaryDoubleWrites,
			// A Preview run is the dry run: it plans on reads and writes nothing.
		},
		{
			kind:       rewritePathPrefixKind,
			params:     rppParams(`,"require_target_exists":false`),
			standsDown: true,
			setup: func(t *testing.T, p *t6g7Probe) {
				seedRewriteRig(t, p, 1)
			},
			live: []string{
				"ModifyBook 01BATBOOK00000000000000W1 title=Year One path=Year One src=Year One.m4b",
				"ModifyBookFile 01BATFILE00000000000000W1 path=01.mp3 itunes=",
				"RecordPathChange 01BATBOOK00000000000000W1 prefix-rewrite /gotham/old/Year One->/gotham/new/Year One",
			},
		},
		{
			kind:       rfKind,
			params:     `{}`,
			standsDown: true,
			setup: func(t *testing.T, p *t6g7Probe) {
				seedFolderAudioRig(t, p, 1)
			},
			// One book's rows in one call: the repointed row first, then the rows
			// marked missing. A Preview run plans on reads and writes nothing.
			live: []string{
				"UpdateBookFiles 01BATBOOK00000000000000F1 01BATFILE000000000000000F1a>Year One.m4b missing=false dur=3600," +
					"01BATFILE000000000000000F1b>Chapter 02.mp3 missing=true dur=0," +
					"01BATFILE000000000000000F1c>Chapter 03.mp3 missing=true dur=0",
			},
		},
		{
			kind:       seriesDenumberKind,
			params:     `{"reportPath":"denumber.tsv"}`,
			standsDown: true,
			setup: func(t *testing.T, p *t6g7Probe) {
				seedDenumberRig(p)
			},
			// The journal rows of a move come before the write they describe, and the
			// emptied series' row is journaled before its delete. A Preview run plans
			// on reads and writes nothing.
			live: denumberLiveWrites,
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
	kinds := []tasks.Kind{reviewStatusIndexRepairKind, reportsPruneKind, titleBackfillKind, titleRepairKind, t6g7KindFor("maintenance.bulk-write-back"), t6g7KindFor("maintenance.user-merge"), tagBackfillKind, repointUnrecordedRenamesKind, seriesPhantomRepairKind, vgPrimaryRepairKind, rewritePathPrefixKind, rfKind, seriesDenumberKind}
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

// vgPrimaryDoubleWrites is every write a Live run of the repair makes on the
// vg-double group of the fixture (A wins on content and keeps its narrator; B
// donates three empty fields and is demoted; C has no file and is demoted from
// an unset flag): the winner's row and its history, then each demoted row.
var vgPrimaryDoubleWrites = []string{
	"ModifyBook A title=Book A",
	`RecordMetadataChange A description ""->"donor description"`,
	`RecordMetadataChange A publisher ""->"Donor House"`,
	`RecordMetadataChange A asin ""->"B0TESTASIN"`,
	"ModifyBook B title=Book B primary=false",
	`RecordMetadataChange B is_primary_version "true"->"false"`,
	"ModifyBook C title=Book C primary=false",
	`RecordMetadataChange C is_primary_version ""->"false"`,
}

// vgPrimaryRigStore answers the repair's reads from a real store, and panics on
// anything else (the embedded interface is nil): the fixture's rows are written
// to a real store by HEAD's own helpers.
type vgPrimaryRigStore struct {
	t6g7Store
	s *database.PebbleStore
}

func (r vgPrimaryRigStore) GetAllBooksCoreComplete(limit, offset int) ([]database.BookCore, error) {
	return r.s.GetAllBooksCoreComplete(limit, offset)
}
func (r vgPrimaryRigStore) GetAllSeries() ([]database.Series, error) { return r.s.GetAllSeries() }
func (r vgPrimaryRigStore) GetBooksByVersionGroup(gid string) ([]database.Book, error) {
	return r.s.GetBooksByVersionGroup(gid)
}
func (r vgPrimaryRigStore) GetBookFiles(id string) ([]database.BookFile, error) {
	return r.s.GetBookFiles(id)
}
func (r vgPrimaryRigStore) GetBookByID(id string) (*database.Book, error) { return r.s.GetBookByID(id) }
func (r vgPrimaryRigStore) GetChaptersForBook(id string) ([]database.Chapter, error) {
	return r.s.GetChaptersForBook(id)
}
func (r vgPrimaryRigStore) GetMetadataFieldStates(id string) ([]database.MetadataFieldState, error) {
	return r.s.GetMetadataFieldStates(id)
}
func (r vgPrimaryRigStore) GetUserPreference(key string) (*database.UserPreference, error) {
	return r.s.GetUserPreference(key)
}

// seedVGPrimaryRig builds the version-group fixture (vg-double, vg-held, vg-ok) in
// a real store under the probe's root, with the chapter counts of its files, and
// seeds the recorder with the rows the writer would modify.
func seedVGPrimaryRig(t *testing.T, p *t6g7Probe) *vgRepairFixture {
	t.Helper()
	f := newVGRepairFixture(t)
	f.seed(t)
	p.store = vgPrimaryRigStore{s: f.s}
	p.root = f.root
	p.prober = f.probe
	p.rec.books = map[string]*database.Book{}
	for _, id := range []string{"A", "B", "C", "S", "L", "OK1", "OK2"} {
		b, err := f.s.GetBookByID(id)
		require.NoError(t, err)
		require.NotNil(t, b, id)
		p.rec.books[id] = b
	}
	return f
}

func runVGPrimaryKind(t *testing.T, p *t6g7Probe, mode tasks.Mode, params string) (*vgRepairReport, *t6g7Host, error) {
	t.Helper()
	def := t6g7ByKind(t, (&Plugin{}).t6g7Kinds(p.deps(), nil), vgPrimaryRepairKind)
	h, err := runT6G7Kind(t, def, mode, &t6g7GateWriter{name: "gotham"}, params)
	var res *vgRepairReport
	if h != nil && len(h.result) > 0 {
		require.NoError(t, json.Unmarshal(h.result, &res))
	}
	return res, h, err
}

// A Preview run is the dry run: it elects and plans on reads, builds no writer,
// parks no scan and writes nothing, and what it plans is what a Live run writes.
func TestT6G7Kinds_VGPrimaryRepairPreviewPlansOnReadsAndWritesNothing(t *testing.T) {
	p := newT6G7Probe(t)
	seedVGPrimaryRig(t, p)
	rep, h, err := runVGPrimaryKind(t, p, tasks.ModePreview, "")
	require.NoError(t, err)
	require.True(t, rep.DryRun)
	require.Equal(t, 2, rep.Candidates, "vg-ok is correct and must not be a candidate")
	require.Equal(t, 1, rep.ByDecision[versionprimary.DecisionElect])
	require.Equal(t, 1, rep.ByHoldReason[versionprimary.HoldBetterCopyNotInLibrary])
	require.Equal(t, 1, rep.ContentNotMetadataBest)
	require.Len(t, rep.Groups, 2)
	var double *vgRepairGroupReport
	for i := range rep.Groups {
		if rep.Groups[i].GroupID == "vg-double" {
			double = &rep.Groups[i]
		}
	}
	require.NotNil(t, double)
	require.Equal(t, "A", double.WinnerID)
	require.Equal(t, []string{"B", "C"}, double.DemotedIDs, "the plan names what a Live run demotes")
	require.Zero(t, p.writerCalls(), "a preview built a writer")
	require.Empty(t, h.standDowns, "a preview parked the scan")
	require.Empty(t, p.rec.snapshot())
}

// A Live run applies the groups it is told to: the winner and every demotion go
// through the run's writer, each with its history, under the scan stand-down,
// on the declared four workers; a typo and a group that is not a candidate are
// reported, not written.
func TestT6G7Kinds_VGPrimaryRepairLiveAppliesThroughTheWriter(t *testing.T) {
	p := newT6G7Probe(t)
	seedVGPrimaryRig(t, p)
	rep, h, err := runVGPrimaryKind(t, p, tasks.ModeLive,
		`{"group_ids":["vg-double","vg-held","vg-ok","vg-typo"]}`)
	require.NoError(t, err)
	require.False(t, rep.DryRun)
	require.Equal(t, 1, rep.Applied)
	require.Equal(t, 3, rep.FieldsFilled)
	require.Equal(t, []string{"vg-typo"}, rep.RequestedUnmatched)
	require.Equal(t, []string{"vg-ok"}, rep.RequestedNotCandidate)
	require.Zero(t, rep.HistoryFailed)
	require.Equal(t, vgPrimaryDoubleWrites, p.rec.snapshot())
	require.Equal(t, 1, p.writerCalls())
	require.Equal(t, []tasks.Key{"books.gotham-watch"}, h.standDowns)
	require.True(t, h.released, "the stand-down was not released")
	require.Contains(t, h.workerCaps(), vgRepairWorkers)
}

// A group whose row changed between the plan and the write is left alone and
// counted: the check and the write are one step inside the writer's ModifyBook,
// so nothing is written for the stale plan.
func TestT6G7Kinds_VGPrimaryRepairLeavesAGroupChangedSincePlan(t *testing.T) {
	p := newT6G7Probe(t)
	seedVGPrimaryRig(t, p)
	// The row the writer sees was demoted meanwhile; the store's plan saw true.
	no := false
	p.rec.books["A"].IsPrimaryVersion = &no
	rep, _, err := runVGPrimaryKind(t, p, tasks.ModeLive, `{"group_ids":["vg-double"]}`)
	require.NoError(t, err)
	require.Equal(t, 1, rep.ChangedSincePlan)
	require.Zero(t, rep.Applied)
	require.Empty(t, p.rec.snapshot(), "a stale plan was written")
}

// A Live run needs group_ids, and refuses before it parks the scan or writes.
func TestT6G7Kinds_VGPrimaryRepairLiveNeedsGroupIDs(t *testing.T) {
	p := newT6G7Probe(t)
	seedVGPrimaryRig(t, p)
	_, h, err := runVGPrimaryKind(t, p, tasks.ModeLive, `{}`)
	require.ErrorContains(t, err, "group_ids")
	require.Empty(t, h.standDowns)
	require.Empty(t, p.rec.snapshot())
}

// A live run that cannot park the scan (no stand-down key) writes nothing.
func TestT6G7Kinds_VGPrimaryRepairLiveNeedsTheScanStandDown(t *testing.T) {
	p := newT6G7Probe(t)
	seedVGPrimaryRig(t, p)
	deps := p.deps()
	deps.StandDownKey = ""
	def := t6g7ByKind(t, (&Plugin{}).t6g7Kinds(deps, nil), vgPrimaryRepairKind)
	_, err := runT6G7Kind(t, def, tasks.ModeLive, &t6g7GateWriter{name: "gotham"}, `{"group_ids":["vg-double"]}`)
	require.ErrorContains(t, err, "no scan stand-down")
	require.ErrorContains(t, err, "nothing written")
	require.Empty(t, p.rec.snapshot())
}

// No ffprobe is a warning, not a failure: the run goes on with the chapter table
// alone, as HEAD's run wrapper did, and a member whose count came from nowhere is
// flagged.
func TestT6G7Kinds_VGPrimaryRepairWithoutFFprobeFallsBackToTheChapterTable(t *testing.T) {
	p := newT6G7Probe(t)
	seedVGPrimaryRig(t, p)
	p.probeErr = errors.New("ffprobe not found")
	rep, _, err := runVGPrimaryKind(t, p, tasks.ModePreview, "")
	require.NoError(t, err)
	require.Positive(t, rep.ChapterUnknownGroups, "the probe was not used and no chapter table row exists")
}

// A kind whose owner left a dependency unset refuses before it reads.
func TestT6G7Kinds_VGPrimaryRepairNeedsItsDeps(t *testing.T) {
	for name, strip := range map[string]func(*t6g7Deps){
		"Store":         func(d *t6g7Deps) { d.Store = nil },
		"RootDir":       func(d *t6g7Deps) { d.RootDir = nil },
		"ChapterProber": func(d *t6g7Deps) { d.ChapterProber = nil },
	} {
		t.Run(name, func(t *testing.T) {
			p := newT6G7Probe(t)
			deps := p.deps()
			strip(&deps)
			def := t6g7ByKind(t, (&Plugin{}).t6g7Kinds(deps, nil), vgPrimaryRepairKind)
			_, err := runT6G7Kind(t, def, tasks.ModePreview, nil, "")
			require.ErrorContains(t, err, "t6g7Deps."+name+" is not set")
		})
	}
}

// The prefixes every rewrite test uses. Neither exists on disk: a test that needs
// the new path present creates it under the probe's root with a different prefix.
const (
	rppOld = "/gotham/old"
	rppNew = "/gotham/new"
)

// rppParams is the params of a run over rppOld -> rppNew; extra is more JSON
// members, each led by a comma.
func rppParams(extra string) string {
	return `{"old_prefix":"` + rppOld + `","new_prefix":"` + rppNew + `"` + extra + `}`
}

var rppTitles = []string{"Year One", "The Long Halloween", "Hush", "Dark Victory"}

// seedRewriteRig seeds n books filed under rppOld, each with one book_file row
// and a source import path under rppOld, in the probe's store (what the planner
// reads) and in its recorder (what the write callbacks run on). The iTunes tree
// is configured at a root the prefixes stay clear of. It returns the book ids.
func seedRewriteRig(t *testing.T, p *t6g7Probe, n int) []string {
	t.Helper()
	prev := config.Snapshot()
	config.Mutate(func(c *config.Config) { c.ITunes = config.ITunesConfig{MediaRoot: "/gotham/itunes"} })
	t.Cleanup(func() { config.Mutate(func(c *config.Config) { *c = prev }) })

	st := &t6g7TestStore{bookAt: map[string][]string{}}
	p.rec.books = map[string]*database.Book{}
	p.rec.bookFiles = map[string]*database.BookFile{}
	var ids []string
	for i := 1; i <= n; i++ {
		id := fmt.Sprintf("01BATBOOK00000000000000W%d", i)
		fid := fmt.Sprintf("01BATFILE00000000000000W%d", i)
		title := rppTitles[i-1]
		dir := rppOld + "/" + title
		src := rppOld + "/imports/" + title + ".m4b"
		st.cores = append(st.cores, database.BookCore{ID: id, Title: title, FilePath: dir, SourceImportPath: &src})
		row := database.BookFile{ID: fid, BookID: id, FilePath: dir + "/01.mp3"}
		st.allFiles = append(st.allFiles, row.Core())
		st.bookAt[dir] = []string{id}
		p.rec.books[id] = &database.Book{ID: id, Title: title, FilePath: dir, SourceImportPath: &src}
		p.rec.bookFiles[fid] = &row
		ids = append(ids, id)
	}
	p.store = st
	return ids
}

func runRewriteKind(t *testing.T, p *t6g7Probe, mode tasks.Mode, params string) (rewritePathPrefixPlan, *t6g7Host, error) {
	t.Helper()
	def := t6g7ByKind(t, (&Plugin{}).t6g7Kinds(p.deps(), nil), rewritePathPrefixKind)
	h, err := runT6G7Kind(t, def, mode, &t6g7GateWriter{name: "gotham"}, params)
	var plan rewritePathPrefixPlan
	if h != nil && len(h.result) > 0 {
		require.NoError(t, json.Unmarshal(h.result, &plan))
	}
	return plan, h, err
}

// rppReports lists the report files a run left in the probe's root.
func rppReports(t *testing.T, p *t6g7Probe) []string {
	t.Helper()
	got, err := filepath.Glob(filepath.Join(p.root, "*.tsv"))
	require.NoError(t, err)
	return got
}

// A Preview run is the dry run HEAD defaulted to: it plans on reads, builds no
// writer, parks no scan and writes no row. It does write the per-row report, to
// the path the params name (the kind declares files for it).
func TestT6G7Kinds_RewritePathPrefixPreviewPlansAndWritesNoRow(t *testing.T) {
	p := newT6G7Probe(t)
	seedRewriteRig(t, p, 2)
	plan, h, err := runRewriteKind(t, p, tasks.ModePreview, rppParams(`,"require_target_exists":false,"report_path":"audit.tsv"`))
	require.NoError(t, err)
	require.True(t, plan.DryRun)
	require.Equal(t, 2, plan.MatchedBooks)
	require.Equal(t, 2, plan.Rewritable)
	require.Zero(t, plan.BooksRewritten)
	require.Zero(t, p.writerCalls(), "a preview built a writer")
	require.Empty(t, p.rec.snapshot())
	require.Empty(t, h.standDowns, "a preview parked the scan")

	require.Equal(t, filepath.Join(p.root, "audit.tsv"), plan.ReportPath)
	body, rerr := os.ReadFile(plan.ReportPath)
	require.NoError(t, rerr)
	// Three fields per book (book path, source import path, file path), each a row.
	require.Equal(t, 6, strings.Count(string(body), "\twould rewrite\n"))
}

// A Live run rewrites every book through the run's gated writer, a book row
// before its file rows and the move ledger last, parks the scan for the writes,
// and leaves its report under the derived name.
func TestT6G7Kinds_RewritePathPrefixLiveRewritesThroughTheWriter(t *testing.T) {
	p := newT6G7Probe(t)
	ids := seedRewriteRig(t, p, 3)
	plan, h, err := runRewriteKind(t, p, tasks.ModeLive, rppParams(`,"require_target_exists":false`))
	require.NoError(t, err)
	require.False(t, plan.DryRun)
	require.Equal(t, 3, plan.BooksRewritten)
	require.Equal(t, 9, plan.FieldsRewritten, "book path, source import path and file path of each book")
	require.Zero(t, plan.UpdateErrs)
	require.Equal(t, 1, p.writerCalls(), "NewWriter calls")
	require.Equal(t, []tasks.Key{"books.gotham-watch"}, h.standDowns)
	require.True(t, h.released, "stand-down released")
	require.Contains(t, h.workerCaps(), rewritePathPrefixConcurrency)

	calls := p.rec.snapshot()
	require.Len(t, calls, 9)
	for i, id := range ids {
		title := rppTitles[i]
		fid := fmt.Sprintf("01BATFILE00000000000000W%d", i+1)
		modBook := slices.Index(calls, fmt.Sprintf("ModifyBook %s title=%s path=%s src=%s.m4b", id, title, title, title))
		modFile := slices.Index(calls, fmt.Sprintf("ModifyBookFile %s path=01.mp3 itunes=", fid))
		ledger := slices.Index(calls, fmt.Sprintf("RecordPathChange %s prefix-rewrite %s/%s->%s/%s", id, rppOld, title, rppNew, title))
		require.GreaterOrEqual(t, modBook, 0, "%s: book row not written", title)
		require.Greater(t, modFile, modBook, "%s: file row before the book row", title)
		require.Greater(t, ledger, modFile, "%s: ledger row before the file row", title)
	}
	reports := rppReports(t, p)
	require.Len(t, reports, 1)
	require.Equal(t, reports[0], plan.ReportPath)
	require.Contains(t, filepath.Base(reports[0]), "rewrite-path-prefix-")
}

// A book whose stored path no longer starts with old_prefix when its write runs
// was moved by another writer since the scan: nothing of it is written.
func TestT6G7Kinds_RewritePathPrefixLiveLeavesABookChangedSinceTheScan(t *testing.T) {
	p := newT6G7Probe(t)
	ids := seedRewriteRig(t, p, 1)
	p.rec.books[ids[0]].FilePath = "/gotham/elsewhere/Year One"
	plan, _, err := runRewriteKind(t, p, tasks.ModeLive, rppParams(`,"require_target_exists":false`))
	require.NoError(t, err)
	require.Zero(t, plan.BooksRewritten)
	require.Zero(t, plan.FieldsRewritten)
	require.Equal(t, 3, plan.SkippedChanged)
	require.Empty(t, p.rec.snapshot())
}

// A target that is not on disk, or that another live book holds, refuses the
// whole book, in Live and in Preview alike, and writes nothing.
func TestT6G7Kinds_RewritePathPrefixRefusesAMissingOrClaimedTarget(t *testing.T) {
	for _, mode := range []tasks.Mode{tasks.ModeLive, tasks.ModePreview} {
		p := newT6G7Probe(t)
		seedRewriteRig(t, p, 1)
		plan, _, err := runRewriteKind(t, p, mode, rppParams(``))
		require.NoError(t, err, fmt.Sprint(mode))
		require.Equal(t, 1, plan.TargetMissing, "the new path is not on disk (%s)", mode)
		require.Zero(t, plan.Rewritable)
		require.Empty(t, p.rec.snapshot(), "%s", mode)

		p = newT6G7Probe(t)
		seedRewriteRig(t, p, 1)
		p.store.(*t6g7TestStore).bookAt[rppNew+"/Year One"] = []string{"01BATBOOK00000000000000W9"}
		plan, _, err = runRewriteKind(t, p, mode, rppParams(`,"require_target_exists":false`))
		require.NoError(t, err, fmt.Sprint(mode))
		require.Equal(t, 1, plan.TargetCollision, "another book holds the new path (%s)", mode)
		require.Empty(t, p.rec.snapshot(), "%s", mode)
	}
}

// max bounds how many books one Live run rewrites; the rest are reported capped.
func TestT6G7Kinds_RewritePathPrefixMaxCapsTheBooksRewritten(t *testing.T) {
	p := newT6G7Probe(t)
	seedRewriteRig(t, p, 3)
	plan, _, err := runRewriteKind(t, p, tasks.ModeLive, rppParams(`,"require_target_exists":false,"max":2`))
	require.NoError(t, err)
	require.Equal(t, 2, plan.CappedAt)
	require.Equal(t, 2, plan.BooksRewritten)
	body, rerr := os.ReadFile(plan.ReportPath)
	require.NoError(t, rerr)
	require.Contains(t, string(body), "capped\t")
}

// A Live run needs the scan stand-down: without a key it refuses before a row is
// written, as HEAD refused without the scan controller's interlock.
func TestT6G7Kinds_RewritePathPrefixLiveNeedsTheScanStandDown(t *testing.T) {
	p := newT6G7Probe(t)
	seedRewriteRig(t, p, 1)
	deps := p.deps()
	deps.StandDownKey = ""
	def := t6g7ByKind(t, (&Plugin{}).t6g7Kinds(deps, nil), rewritePathPrefixKind)
	_, err := runT6G7Kind(t, def, tasks.ModeLive, &t6g7GateWriter{name: "gotham"}, rppParams(`,"require_target_exists":false`))
	require.ErrorContains(t, err, "acquire scan stand-down")
	require.ErrorContains(t, err, "no scan stand-down key configured")
	require.Empty(t, p.rec.snapshot())
}

// Params HEAD refused are refused before the store is read or a writer built: an
// unknown key (HEAD decoded strictly, so a habitual "apply" cannot silently mean
// dry run), a relative or identical prefix, and a prefix that reaches the frozen
// iTunes tree from either side.
func TestT6G7Kinds_RewritePathPrefixRefusesBadParams(t *testing.T) {
	for name, tc := range map[string]struct{ params, want string }{
		"unknown key":      {rppParams(`,"requre_target_exists":false`), "unknown field"},
		"duplicate key":    {rppParams(`,"max":1,"max":2`), "duplicate key"},
		"relative old":     {`{"old_prefix":"gotham/old","new_prefix":"` + rppNew + `"}`, "is not absolute"},
		"identical":        {`{"old_prefix":"` + rppOld + `","new_prefix":"` + rppOld + `"}`, "identical"},
		"nested":           {`{"old_prefix":"` + rppOld + `","new_prefix":"` + rppOld + `/inner"}`, "nested"},
		"inside itunes":    {`{"old_prefix":"/gotham/itunes/Batman","new_prefix":"` + rppNew + `"}`, "iTunes library root"},
		"contains itunes":  {`{"old_prefix":"/gotham","new_prefix":"/metropolis"}`, "iTunes library root"},
		"new inside itune": {`{"old_prefix":"` + rppOld + `","new_prefix":"/gotham/itunes/Batman"}`, "iTunes library root"},
		"missing new":      {`{"old_prefix":"` + rppOld + `"}`, "both required"},
	} {
		t.Run(name, func(t *testing.T) {
			p := newT6G7Probe(t)
			seedRewriteRig(t, p, 1)
			_, _, err := runRewriteKind(t, p, tasks.ModeLive, tc.params)
			require.ErrorContains(t, err, tc.want)
			require.Zero(t, p.writerCalls(), "a writer was built")
			require.Empty(t, p.rec.snapshot())
			require.Empty(t, rppReports(t, p), "a refused run left a report")
		})
	}
}

// A native run whose owner left a dependency unset refuses before it reads.
func TestT6G7Kinds_RewritePathPrefixNeedsItsDeps(t *testing.T) {
	for name, tc := range map[string]struct {
		mutate func(*t6g7Deps)
		mode   tasks.Mode
		want   string
	}{
		"store":  {func(d *t6g7Deps) { d.Store = nil }, tasks.ModePreview, "Store is not set"},
		"report": {func(d *t6g7Deps) { d.ResolveReportPath = nil }, tasks.ModePreview, "ResolveReportPath is not set"},
		"writer": {func(d *t6g7Deps) { d.NewWriter = nil }, tasks.ModeLive, "NewWriter is not set"},
	} {
		t.Run(name, func(t *testing.T) {
			p := newT6G7Probe(t)
			seedRewriteRig(t, p, 1)
			deps := p.deps()
			tc.mutate(&deps)
			def := t6g7ByKind(t, (&Plugin{}).t6g7Kinds(deps, nil), rewritePathPrefixKind)
			_, err := runT6G7Kind(t, def, tc.mode, &t6g7GateWriter{name: "gotham"}, rppParams(`,"require_target_exists":false`))
			require.ErrorContains(t, err, tc.want)
			require.Empty(t, p.rec.snapshot())
		})
	}
}

// The core stops writing when the lease it holds lapses (HEAD's controller can
// lose it; the native kernel fences the writer instead and reports it never
// lost), and a Live core with no interlock refuses before a write.
func TestRewritePathPrefixCore_StopsWritingWhenTheLeaseLapses(t *testing.T) {
	var params rewritePathPrefixParams
	require.NoError(t, json.Unmarshal([]byte(rppParams(`,"require_target_exists":false`)), &params))

	p := newT6G7Probe(t)
	seedRewriteRig(t, p, 3)
	released := false
	env := rewritePathPrefixEnv{t6g7Env: t6g7HeadEnv(&fakeReporter{}, true), Store: p.store, Writer: p.rec}
	env.StandDown = func(context.Context, string) (t6g7Hold, error) {
		return t6g7Hold{Lost: func() bool { return true }, Release: func() { released = true }}, nil
	}
	plan, err := rewritePathPrefixPlanCore(context.Background(), params, env)
	require.ErrorContains(t, err, "lease lapsed mid-apply")
	require.Zero(t, plan.BooksRewritten)
	require.Empty(t, p.rec.snapshot(), "a book was written after the lease lapsed")
	require.True(t, released, "the lapsed hold was not released")

	p = newT6G7Probe(t)
	seedRewriteRig(t, p, 1)
	env = rewritePathPrefixEnv{t6g7Env: t6g7HeadEnv(&fakeReporter{}, true), Store: p.store, Writer: p.rec}
	_, err = rewritePathPrefixPlanCore(context.Background(), params, env)
	require.ErrorContains(t, err, "no interlock configured")
	require.Empty(t, p.rec.snapshot())

	env = rewritePathPrefixEnv{t6g7Env: t6g7HeadEnv(&fakeReporter{}, true), Store: p.store}
	_, err = rewritePathPrefixPlanCore(context.Background(), params, env)
	require.ErrorContains(t, err, "no writer for a live run")
}

// seedFolderAudioRig seeds n books, each in its own folder under the probe's root
// with three chapter rows whose files are gone and one consolidated file standing
// in the folder, in the probe's store (what the planner reads). Ids are
// ...F1..Fn; the rows are ...Fna, Fnb, Fnc. The header read is stubbed to 3600
// seconds, and the iTunes tree is configured at a root the folders stay clear of.
// It returns the book ids.
func seedFolderAudioRig(t *testing.T, p *t6g7Probe, n int) []string {
	t.Helper()
	prev := config.Snapshot()
	config.Mutate(func(c *config.Config) { c.ITunes = config.ITunesConfig{MediaRoot: "/gotham/itunes"} })
	t.Cleanup(func() { config.Mutate(func(c *config.Config) { *c = prev }) })
	rfStubProbe(t, 3600)

	st := &t6g7TestStore{files: map[string][]database.BookFile{}, bookAt: map[string][]string{}}
	var ids []string
	for i := 1; i <= n; i++ {
		id := fmt.Sprintf("01BATBOOK00000000000000F%d", i)
		title := rppTitles[i-1]
		folder := filepath.Join(p.root, "Gotham", title)
		writeFile(t, filepath.Join(folder, title+".m4b"), 250)
		st.cores = append(st.cores, rfBookCore(id, folder))
		for j, suffix := range []string{"a", "b", "c"} {
			row := database.BookFile{
				ID: fmt.Sprintf("01BATFILE000000000000000F%d%s", i, suffix), BookID: id,
				FilePath: filepath.Join(folder, fmt.Sprintf("Chapter %02d.mp3", j+1)), FileSize: 100, TrackNumber: j + 1,
			}
			st.files[id] = append(st.files[id], row)
			st.allFiles = append(st.allFiles, row.Core())
		}
		ids = append(ids, id)
	}
	p.store = st
	return ids
}

func runFolderAudioKind(t *testing.T, p *t6g7Probe, mode tasks.Mode, params string) (rfReport, *t6g7Host, error) {
	t.Helper()
	def := t6g7ByKind(t, (&Plugin{}).t6g7Kinds(p.deps(), nil), rfKind)
	h, err := runT6G7Kind(t, def, mode, &t6g7GateWriter{name: "gotham"}, params)
	var rep rfReport
	if h != nil && len(h.result) > 0 {
		require.NoError(t, json.Unmarshal(h.result, &rep))
	}
	return rep, h, err
}

// A Preview run is the dry run HEAD defaulted to: it plans on reads, builds no
// writer, parks no scan and writes no row. It does write the per-row report, to
// the path the params name (the kind declares files for it), and a library scan
// that is running does not stop it.
func TestT6G7Kinds_RepointFolderAudioPreviewPlansAndWritesNoRow(t *testing.T) {
	p := newT6G7Probe(t)
	seedFolderAudioRig(t, p, 2)
	p.store.(*t6g7TestStore).scanStatus = "running"
	rep, h, err := runFolderAudioKind(t, p, tasks.ModePreview, `{"reportPath":"audit.tsv"}`)
	require.NoError(t, err)
	require.True(t, rep.DryRun)
	require.Equal(t, 2, rep.ScopedBooks)
	require.Equal(t, 2, rep.BooksWithMissing)
	require.Equal(t, 2, rep.Planned)
	require.Equal(t, map[string]int{rfDecisionConsolidated: 2}, rep.ByDecision)
	require.Equal(t, 2, rep.RowsRepointed)
	require.Equal(t, 4, rep.RowsMarkedMissing)
	for _, b := range rep.Books {
		require.Equal(t, 3600, b.DurationSec, "a dry run reports the duration the apply would write")
		require.Empty(t, b.Outcome, "a dry run attempts nothing")
	}
	require.Zero(t, p.writerCalls(), "a preview built a writer")
	require.Empty(t, p.rec.snapshot())
	require.Empty(t, h.standDowns, "a preview parked the scan")

	want := filepath.Join(p.root, "audit.tsv")
	require.Equal(t, want, rep.ReportPath)
	body, rerr := os.ReadFile(want)
	require.NoError(t, rerr)
	require.Len(t, strings.Split(strings.TrimSpace(string(body)), "\n"), 1+6, "a header and one line per planned row")
}

// A Live run writes each book's rows in one call through the run's writer, under
// the scan stand-down, on the three pools HEAD sized (24, 4, 4).
func TestT6G7Kinds_RepointFolderAudioLiveWritesEachBookThroughTheWriter(t *testing.T) {
	p := newT6G7Probe(t)
	seedFolderAudioRig(t, p, 2)
	rep, h, err := runFolderAudioKind(t, p, tasks.ModeLive, `{}`)
	require.NoError(t, err)
	require.False(t, rep.DryRun)
	require.Equal(t, map[string]int{rfOutcomeApplied: 2}, rep.ByOutcome)
	require.Equal(t, 2, rep.RowsRepointed)
	require.Equal(t, 4, rep.RowsMarkedMissing)
	require.ElementsMatch(t, []string{
		"UpdateBookFiles 01BATBOOK00000000000000F1 01BATFILE000000000000000F1a>Year One.m4b missing=false dur=3600," +
			"01BATFILE000000000000000F1b>Chapter 02.mp3 missing=true dur=0,01BATFILE000000000000000F1c>Chapter 03.mp3 missing=true dur=0",
		"UpdateBookFiles 01BATBOOK00000000000000F2 01BATFILE000000000000000F2a>The Long Halloween.m4b missing=false dur=3600," +
			"01BATFILE000000000000000F2b>Chapter 02.mp3 missing=true dur=0,01BATFILE000000000000000F2c>Chapter 03.mp3 missing=true dur=0",
	}, p.rec.snapshot())
	require.Equal(t, []tasks.Key{"books.gotham-watch"}, h.standDowns, "scan stand-down")
	require.True(t, h.released, "stand-down released")
	caps := h.workerCaps()
	require.Contains(t, caps, 24, "the stat sweep")
	require.Contains(t, caps, 4, "the plan and the write")
}

// A library scan that is running (or whose queue cannot be read) refuses the
// apply before it takes the stand-down or writes: a scan rewrites the rows this
// op changes. The per-row report of what was planned is still left behind.
func TestT6G7Kinds_RepointFolderAudioLiveRefusesWhileAScanRuns(t *testing.T) {
	for name, tc := range map[string]struct {
		mutate func(*t6g7TestStore)
		want   string
	}{
		"running": {func(s *t6g7TestStore) { s.scanStatus = "running" }, "library.scan is running"},
		"queued":  {func(s *t6g7TestStore) { s.scanStatus = "queued" }, "library.scan is queued"},
		"unread":  {func(s *t6g7TestStore) { s.activeErr = errors.New("queue down") }, "cannot list active operations"},
	} {
		t.Run(name, func(t *testing.T) {
			p := newT6G7Probe(t)
			seedFolderAudioRig(t, p, 1)
			tc.mutate(p.store.(*t6g7TestStore))
			_, h, err := runFolderAudioKind(t, p, tasks.ModeLive, `{}`)
			require.ErrorContains(t, err, tc.want)
			require.Empty(t, p.rec.snapshot(), "rows were written beside a scan")
			require.Empty(t, h.standDowns, "the stand-down was taken before the refusal")
			require.Len(t, rppReports(t, p), 1, "the plan's report is left behind")
		})
	}
}

// A book whose row changed between the plan and the write is skipped, not
// overwritten: the apply re-reads the rows and re-checks every precondition.
func TestT6G7Kinds_RepointFolderAudioLiveLeavesABookChangedSinceThePlan(t *testing.T) {
	p := newT6G7Probe(t)
	ids := seedFolderAudioRig(t, p, 1)
	st := p.store.(*t6g7TestStore)
	// The row GetBookFiles answers at apply time has moved on from the one the
	// plan read in GetAllBookFilesCore.
	st.files[ids[0]][1].FilePath = filepath.Join(p.root, "Gotham", "Elsewhere.mp3")
	rep, _, err := runFolderAudioKind(t, p, tasks.ModeLive, `{}`)
	require.NoError(t, err)
	require.Equal(t, map[string]int{rfOutcomeChanged: 1}, rep.ByOutcome)
	require.Contains(t, rep.Books[0].Error, "changed since the plan")
	require.Empty(t, p.rec.snapshot(), "a changed book was written")
}

// A file another live book references is not this book's to take: the book is
// reported ambiguous, in both modes, and nothing is written.
func TestT6G7Kinds_RepointFolderAudioLeavesAFileAnotherBookOwns(t *testing.T) {
	for _, mode := range []tasks.Mode{tasks.ModePreview, tasks.ModeLive} {
		p := newT6G7Probe(t)
		ids := seedFolderAudioRig(t, p, 1)
		st := p.store.(*t6g7TestStore)
		target := filepath.Join(p.root, "Gotham", "Year One", "Year One.m4b")
		st.bookAt[target] = []string{"01BATBOOK0000000000000OTHER"}
		rep, _, err := runFolderAudioKind(t, p, mode, `{}`)
		require.NoError(t, err)
		require.Equal(t, map[string]int{rfDecisionAmbiguous: 1}, rep.ByDecision)
		require.Equal(t, ids[0], rep.Books[0].BookID)
		require.Equal(t, "folder-shared-with-other-book", rep.Books[0].Reason)
		require.Empty(t, p.rec.snapshot(), "an ambiguous book was written (%v)", mode)
	}
}

// limit caps how many books are planned, lowest ids first so a re-run is stable;
// book_ids (or its bookIds alias) scopes the run and names what was out of scope.
func TestT6G7Kinds_RepointFolderAudioLimitAndBookIDsScopeTheRun(t *testing.T) {
	p := newT6G7Probe(t)
	ids := seedFolderAudioRig(t, p, 3)
	rep, _, err := runFolderAudioKind(t, p, tasks.ModePreview, `{"limit":2}`)
	require.NoError(t, err)
	require.Equal(t, 3, rep.BooksWithMissing)
	require.Equal(t, 2, rep.Planned)
	require.Equal(t, 2, rep.CappedAt)
	require.Equal(t, ids[0], rep.Books[0].BookID)
	require.Equal(t, ids[1], rep.Books[1].BookID)

	rep, _, err = runFolderAudioKind(t, p, tasks.ModePreview, `{"bookIds":["`+ids[2]+`","01BATBOOK00000000000000NOPE"]}`)
	require.NoError(t, err)
	require.Equal(t, 1, rep.Planned)
	require.Equal(t, ids[2], rep.Books[0].BookID)
	require.Equal(t, []string{"01BATBOOK00000000000000NOPE"}, rep.OutOfScope)
}

// Until the book_atpath index is built every per-file owner check would scan every
// book row, so the run aborts before it plans.
func TestT6G7Kinds_RepointFolderAudioRefusesWhileTheAtPathIndexIsNotBuilt(t *testing.T) {
	p := newT6G7Probe(t)
	seedFolderAudioRig(t, p, 1)
	p.store.(*t6g7TestStore).atPathUnbuilt = true
	_, _, err := runFolderAudioKind(t, p, tasks.ModePreview, `{}`)
	require.ErrorContains(t, err, "book_atpath index not built")
	require.Empty(t, p.rec.snapshot())
}

// Params HEAD refused are refused before the store is read or a writer built: an
// unknown or duplicate key (HEAD decoded strictly, so a typo cannot silently mean
// a default) and a negative limit.
func TestT6G7Kinds_RepointFolderAudioRefusesBadParams(t *testing.T) {
	for name, tc := range map[string]struct{ params, want string }{
		"mode key":       {`{"apply":false,"frobnicate":1}`, "mode comes from the start request"},
		"unknown field":  {`{"frobnicate":1}`, "unknown field"},
		"duplicate key":  {`{"limit":1,"limit":2}`, "duplicate key"},
		"negative limit": {`{"limit":-1}`, "limit must be >= 0"},
	} {
		t.Run(name, func(t *testing.T) {
			p := newT6G7Probe(t)
			seedFolderAudioRig(t, p, 1)
			_, _, err := runFolderAudioKind(t, p, tasks.ModeLive, tc.params)
			require.ErrorContains(t, err, tc.want)
			require.Zero(t, p.writerCalls(), "a writer was built")
			require.Empty(t, p.rec.snapshot())
		})
	}
}

// A native run whose owner left a dependency unset refuses before it reads.
func TestT6G7Kinds_RepointFolderAudioNeedsItsDeps(t *testing.T) {
	for name, tc := range map[string]struct {
		mutate func(*t6g7Deps)
		mode   tasks.Mode
		want   string
	}{
		"store":   {func(d *t6g7Deps) { d.Store = nil }, tasks.ModePreview, "Store is not set"},
		"report":  {func(d *t6g7Deps) { d.ResolveReportPath = nil }, tasks.ModePreview, "ResolveReportPath is not set"},
		"rootdir": {func(d *t6g7Deps) { d.RootDir = nil }, tasks.ModePreview, "RootDir is not set"},
		"writer":  {func(d *t6g7Deps) { d.NewWriter = nil }, tasks.ModeLive, "NewWriter is not set"},
		"blank root": {func(d *t6g7Deps) { d.RootDir = func() string { return "" } }, tasks.ModePreview,
			"root_dir is not set"},
	} {
		t.Run(name, func(t *testing.T) {
			p := newT6G7Probe(t)
			seedFolderAudioRig(t, p, 1)
			deps := p.deps()
			tc.mutate(&deps)
			def := t6g7ByKind(t, (&Plugin{}).t6g7Kinds(deps, nil), rfKind)
			_, err := runT6G7Kind(t, def, tc.mode, &t6g7GateWriter{name: "gotham"}, `{}`)
			require.ErrorContains(t, err, tc.want)
			require.Empty(t, p.rec.snapshot())
		})
	}
}

func TestT6G7Kinds_RepointFolderAudioLiveNeedsTheScanStandDown(t *testing.T) {
	p := newT6G7Probe(t)
	seedFolderAudioRig(t, p, 1)
	deps := p.deps()
	deps.StandDownKey = ""
	def := t6g7ByKind(t, (&Plugin{}).t6g7Kinds(deps, nil), rfKind)
	_, err := runT6G7Kind(t, def, tasks.ModeLive, &t6g7GateWriter{name: "gotham"}, `{}`)
	require.ErrorContains(t, err, "acquire scan stand-down")
	require.ErrorContains(t, err, "no scan stand-down key configured")
	require.Empty(t, p.rec.snapshot())
}

// The core stops writing when the lease it holds lapses (HEAD's controller can
// lose it; the native kernel fences the writer instead and reports it never
// lost), and a Live core with no interlock or no writer refuses before a write.
func TestRepointFolderAudioCore_StopsWritingWhenTheLeaseLapses(t *testing.T) {
	newEnv := func(t *testing.T) (*t6g7Probe, rfCoreEnv) {
		p := newT6G7Probe(t)
		seedFolderAudioRig(t, p, 3)
		return p, rfCoreEnv{
			t6g7Env: t6g7HeadEnv(&fakeReporter{}, true),
			Reads:   p.store, Writer: p.rec, Queue: p.store, RootDir: p.root,
		}
	}

	p, env := newEnv(t)
	released := false
	env.StandDown = func(context.Context, string) (t6g7Hold, error) {
		return t6g7Hold{Lost: func() bool { return true }, Release: func() { released = true }}, nil
	}
	rep, err := repointMissingToFolderAudioCore(context.Background(), rfParams{}, env)
	require.ErrorContains(t, err, "scan stand-down lease lapsed")
	require.Equal(t, map[string]int{rfOutcomeSkipped: 3}, rep.ByOutcome, "every planned book is left not attempted")
	require.Empty(t, p.rec.snapshot(), "rows were written after the lease lapsed")
	require.True(t, released, "the hold was not released")

	p, env = newEnv(t)
	_, err = repointMissingToFolderAudioCore(context.Background(), rfParams{}, env)
	require.ErrorContains(t, err, "no interlock configured")
	require.Empty(t, p.rec.snapshot())

	p, env = newEnv(t)
	env.Writer = nil
	_, err = repointMissingToFolderAudioCore(context.Background(), rfParams{}, env)
	require.ErrorContains(t, err, "no writer for a live run")
	require.Empty(t, p.rec.snapshot())
}

// denumberBook ids: the base's own book, the book the numbered series holds, and
// the two siblings that make "Gotham Knights 05" a run of three.
const (
	denumberBaseBook  = "01BATBOOK00000000000000D0"
	denumberMoverBook = "01BATBOOK00000000000000D5"
)

// denumberLiveWrites is every write a Live run of the de-numbering makes on the
// rig: the mover's two journal rows before its write (link, then position), the
// write itself, the emptied series' journal row before its delete, then the
// delete. The siblings hold a position of their own (99), so they move nothing.
var denumberLiveWrites = []string{
	"CreateOperationChange " + denumberMoverBook + " series_id 11->10",
	"CreateOperationChange " + denumberMoverBook + " series_sequence ->5",
	"ModifyBook " + denumberMoverBook + " title=Year One series=10 seq=5",
	"CreateOperationChange   {\"id\":11,\"name\":\"Gotham Knights 05\",\"author_id\":5}->",
	"DeleteSeriesIfUnreferenced 11 Gotham Knights 05",
}

// seedDenumberRig seeds a library where "Gotham Knights 05" holds one unplaced
// book, its base "Gotham Knights" exists with a book at position 1, and two
// sibling series "04" and "06" (one book each, a position of their own, their own
// titles) make 05 a run of three consecutive numbers: the one plan that moves a
// book.
func seedDenumberRig(p *t6g7Probe) {
	author := 5
	ser := func(id int, name string) database.Series {
		return database.Series{ID: id, Name: name, AuthorID: &author}
	}
	seq := func(n int) *int { return &n }
	core := func(id, title string, seriesID int, pos *int) database.BookCore {
		sid := seriesID
		return database.BookCore{ID: id, Title: title, SeriesID: &sid, SeriesSequence: pos}
	}
	base := core(denumberBaseBook, "Hush", 10, seq(1))
	mover := core(denumberMoverBook, "Year One", 11, nil)
	sib4 := core("01BATBOOK00000000000000D4", "The Long Halloween", 20, seq(99))
	sib6 := core("01BATBOOK00000000000000D6", "Dark Victory", 21, seq(99))
	p.store = &t6g7TestStore{
		series: []database.Series{
			ser(10, "Gotham Knights"), ser(11, "Gotham Knights 05"), ser(20, "Gotham Knights 04"), ser(21, "Gotham Knights 06"),
		},
		authors:   []database.Author{{ID: author, Name: "Bruce Wayne"}},
		counts:    map[int]int{10: 1, 11: 1, 20: 1, 21: 1},
		refCounts: map[int]int{10: 1, 11: 1, 20: 1, 21: 1},
		// The count the apply takes again before it writes into the base and
		// before it deletes the emptied source: the source reads empty once its
		// book has moved, which a static fixture states up front.
		perSeriesRefs: map[int]int{10: 1, 11: 0, 20: 1, 21: 1},
		members: map[int][]database.BookCore{
			10: {base}, 11: {mover}, 20: {sib4}, 21: {sib6},
		},
	}
	moverRow := &database.Book{ID: denumberMoverBook, Title: "Year One", AuthorID: &author, SeriesID: mover.SeriesID}
	p.rec.books = map[string]*database.Book{denumberMoverBook: moverRow}
}

func runDenumberKind(t *testing.T, p *t6g7Probe, mode tasks.Mode, params string) (*t6g7Host, error) {
	t.Helper()
	def := t6g7ByKind(t, (&Plugin{}).t6g7Kinds(p.deps(), nil), seriesDenumberKind)
	return runT6G7Kind(t, def, mode, &t6g7GateWriter{name: "gotham"}, params)
}

// A Preview run is HEAD's dry run: it plans on reads, writes the candidate report
// and nothing else, and never builds a writer, parks the scan or drops a cache.
func TestT6G7Kinds_SeriesDenumberPreviewPlansAndWritesNoRow(t *testing.T) {
	p := newT6G7Probe(t)
	seedDenumberRig(p)
	h, err := runDenumberKind(t, p, tasks.ModePreview, `{"reportPath":"denumber.tsv"}`)
	require.NoError(t, err)
	require.Zero(t, p.writerCalls(), "a preview built a writer")
	require.Empty(t, p.rec.snapshot())
	require.Empty(t, h.standDowns, "a preview parked the scan")
	require.Zero(t, p.seriesInvalidated)
	body, rerr := os.ReadFile(filepath.Join(p.root, "denumber.tsv"))
	require.NoError(t, rerr)
	require.Contains(t, string(body), "Gotham Knights 05")
	require.Contains(t, string(body), denumberMoverBook)
	_, aerr := os.Stat(filepath.Join(p.root, "denumber.tsv.applied"))
	require.True(t, os.IsNotExist(aerr), "a preview wrote an outcome report")
}

// A Live run journals each move before its write, deletes the emptied series
// behind its own journal row, drops the series caches once, and writes the
// outcome report beside the candidate report.
func TestT6G7Kinds_SeriesDenumberLiveMergesJournalsAndDeletes(t *testing.T) {
	p := newT6G7Probe(t)
	seedDenumberRig(p)
	h, err := runDenumberKind(t, p, tasks.ModeLive, `{"reportPath":"denumber.tsv"}`)
	require.NoError(t, err)
	require.Equal(t, denumberLiveWrites, p.rec.snapshot())
	require.Equal(t, []tasks.Key{"books.gotham-watch"}, h.standDowns)
	require.True(t, h.released, "the stand-down was not released")
	require.Equal(t, 1, p.seriesInvalidated)
	applied, rerr := os.ReadFile(filepath.Join(p.root, "denumber.tsv") + ".applied")
	_ = applied
	// The test's resolver joins an absolute path under the root, so the outcome
	// report is where the core computed it: beside the resolved report.
	if rerr != nil {
		applied, rerr = os.ReadFile(filepath.Join(p.root, "denumber.tsv.applied"))
	}
	require.NoError(t, rerr)
	require.Contains(t, string(applied), "moved")
	require.Contains(t, string(applied), "deleted")
}

// An apply needs its report: refused before a writer or the scan hold exists.
func TestT6G7Kinds_SeriesDenumberLiveNeedsAReport(t *testing.T) {
	p := newT6G7Probe(t)
	seedDenumberRig(p)
	h, err := runDenumberKind(t, p, tasks.ModeLive, `{}`)
	require.ErrorContains(t, err, "apply requires reportPath")
	require.Zero(t, p.writerCalls())
	require.Empty(t, h.standDowns)
	require.Empty(t, p.rec.snapshot())
}

// A store that cannot restore, recount or check-and-delete refuses the apply
// before anything is read, written or held; a Preview needs none of it.
func TestT6G7Kinds_SeriesDenumberLiveRefusesAStoreThatCannotUndo(t *testing.T) {
	p := newT6G7Probe(t)
	seedDenumberRig(p)
	p.store.(*t6g7TestStore).applyReadyErr = errors.New("this store cannot restore a deleted series")
	h, err := runDenumberKind(t, p, tasks.ModeLive, `{"reportPath":"denumber.tsv"}`)
	require.ErrorContains(t, err, "refusing to apply")
	require.ErrorContains(t, err, "cannot restore a deleted series")
	require.Empty(t, h.standDowns, "the scan was parked before the refusal")
	require.Empty(t, p.rec.snapshot())
	_, serr := os.Stat(filepath.Join(p.root, "denumber.tsv"))
	require.True(t, os.IsNotExist(serr), "a refused apply wrote its report")

	_, err = runDenumberKind(t, p, tasks.ModePreview, `{}`)
	require.NoError(t, err, "a preview needs none of the undo capabilities")
}

// A reference that appears between the plan and the delete keeps the emptied
// series: it is neither deleted nor journaled, and the books that moved stay
// moved.
func TestT6G7Kinds_SeriesDenumberKeepsTheSeriesAReferenceAppearedIn(t *testing.T) {
	p := newT6G7Probe(t)
	seedDenumberRig(p)
	p.store.(*t6g7TestStore).perSeriesRefs[11] = 1
	_, err := runDenumberKind(t, p, tasks.ModeLive, `{"reportPath":"denumber.tsv"}`)
	require.NoError(t, err)
	require.Equal(t, denumberLiveWrites[:3], p.rec.snapshot(), "the move landed; the series was not journaled or deleted")

	// The store's own check-and-delete refuses it: nothing is recorded as deleted.
	p = newT6G7Probe(t)
	seedDenumberRig(p)
	p.rec.keepSeries = true
	_, err = runDenumberKind(t, p, tasks.ModeLive, `{"reportPath":"denumber.tsv"}`)
	require.NoError(t, err)
	require.NotContains(t, p.rec.snapshot(), "DeleteSeriesIfUnreferenced 11 Gotham Knights 05")
}

// A journal row that cannot be written stops the book write it describes.
func TestT6G7Kinds_SeriesDenumberAJournalFailureStopsTheBookWrite(t *testing.T) {
	p := newT6G7Probe(t)
	seedDenumberRig(p)
	p.rec.opChangeErr = errors.New("journal is down")
	_, err := runDenumberKind(t, p, tasks.ModeLive, `{"reportPath":"denumber.tsv"}`)
	require.Error(t, err, "a run whose writes failed is not a success")
	for _, c := range p.rec.snapshot() {
		require.False(t, strings.HasPrefix(c, "ModifyBook"), "a book was written without its journal row: %v", p.rec.snapshot())
		require.False(t, strings.HasPrefix(c, "DeleteSeries"), "a series was deleted after a failed move: %v", p.rec.snapshot())
	}
}

// baseName and minBooks scope the apply: a canary that selects no plan writes
// nothing, and the candidate report still lists every candidate.
func TestT6G7Kinds_SeriesDenumberCanaryScopesTheApply(t *testing.T) {
	p := newT6G7Probe(t)
	seedDenumberRig(p)
	_, err := runDenumberKind(t, p, tasks.ModeLive, `{"reportPath":"denumber.tsv","baseName":"Arkham"}`)
	require.NoError(t, err)
	require.Empty(t, p.rec.snapshot())
	body, rerr := os.ReadFile(filepath.Join(p.root, "denumber.tsv"))
	require.NoError(t, rerr)
	require.Contains(t, string(body), "Gotham Knights 05")

	p = newT6G7Probe(t)
	seedDenumberRig(p)
	_, err = runDenumberKind(t, p, tasks.ModeLive, `{"reportPath":"denumber.tsv","minBooks":2}`)
	require.NoError(t, err)
	require.Empty(t, p.rec.snapshot(), "a plan that moves one book is below minBooks 2")
}

func TestT6G7Kinds_SeriesDenumberRefusesBadParams(t *testing.T) {
	for name, params := range map[string]string{
		"unknown field":  `{"reportPth":"x"}`,
		"duplicate key":  `{"limit":1,"limit":2}`,
		"negative limit": `{"limit":-1}`,
		"negative min":   `{"minBooks":-1}`,
		"mode key":       `{"apply":true,"reportPath":"x"}`,
	} {
		t.Run(name, func(t *testing.T) {
			p := newT6G7Probe(t)
			seedDenumberRig(p)
			_, err := runDenumberKind(t, p, tasks.ModeLive, params)
			require.Error(t, err)
			require.Zero(t, p.writerCalls(), "a writer was built")
			require.Empty(t, p.rec.snapshot())
		})
	}
}

func TestT6G7Kinds_SeriesDenumberNeedsItsDeps(t *testing.T) {
	for name, strip := range map[string]func(*t6g7Deps){
		"Store":                 func(d *t6g7Deps) { d.Store = nil },
		"ResolveReportPath":     func(d *t6g7Deps) { d.ResolveReportPath = nil },
		"InvalidateSeriesCache": func(d *t6g7Deps) { d.InvalidateSeriesCache = nil },
		"NewWriter":             func(d *t6g7Deps) { d.NewWriter = nil },
	} {
		t.Run(name, func(t *testing.T) {
			p := newT6G7Probe(t)
			seedDenumberRig(p)
			deps := p.deps()
			strip(&deps)
			def := t6g7ByKind(t, (&Plugin{}).t6g7Kinds(deps, nil), seriesDenumberKind)
			_, err := runT6G7Kind(t, def, tasks.ModeLive, &t6g7GateWriter{name: "gotham"}, `{"reportPath":"denumber.tsv"}`)
			require.ErrorContains(t, err, name+" is not set")
			require.Empty(t, p.rec.snapshot())
		})
	}
}

func TestT6G7Kinds_SeriesDenumberLiveNeedsTheScanStandDown(t *testing.T) {
	p := newT6G7Probe(t)
	seedDenumberRig(p)
	deps := p.deps()
	deps.StandDownKey = ""
	def := t6g7ByKind(t, (&Plugin{}).t6g7Kinds(deps, nil), seriesDenumberKind)
	_, err := runT6G7Kind(t, def, tasks.ModeLive, &t6g7GateWriter{name: "gotham"}, `{"reportPath":"denumber.tsv"}`)
	require.ErrorContains(t, err, "no scan stand-down key configured")
	require.Empty(t, p.rec.snapshot())
}

// The core stops writing when the lease it holds lapses, and a Live core with no
// interlock, no writer or no operation id refuses before a write.
func TestSeriesDenumberCore_StopsWritingWhenTheLeaseLapses(t *testing.T) {
	newEnv := func() (*t6g7Probe, seriesDenumberEnv) {
		p := newT6G7Probe(t)
		seedDenumberRig(p)
		return p, seriesDenumberEnv{
			t6g7Env:       t6g7HeadEnv(&opIDReporter{id: denumberOpID}, true),
			Reads:         p.store,
			Writer:        p.rec,
			ResolveReport: func(explicit string) (string, error) { return filepath.Join(p.root, explicit), nil },
		}
	}
	params := SeriesDenumberParams{ReportPath: "denumber.tsv"}

	p, env := newEnv()
	released := false
	env.StandDown = func(context.Context, string) (t6g7Hold, error) {
		return t6g7Hold{Lost: func() bool { return true }, Release: func() { released = true }}, nil
	}
	err := seriesDenumberCore(context.Background(), params, env)
	require.ErrorContains(t, err, "scan stand-down lease lapsed")
	require.Empty(t, p.rec.snapshot(), "rows were written after the lease lapsed")
	require.True(t, released, "the hold was not released")

	p, env = newEnv()
	err = seriesDenumberCore(context.Background(), params, env)
	require.ErrorContains(t, err, "no scan stand-down is available")
	require.Empty(t, p.rec.snapshot())

	p, env = newEnv()
	env.Writer = nil
	err = seriesDenumberCore(context.Background(), params, env)
	require.ErrorContains(t, err, "no writer for a live run")
	require.Empty(t, p.rec.snapshot())

	p, env = newEnv()
	env.OpID = ""
	err = seriesDenumberCore(context.Background(), params, env)
	require.ErrorContains(t, err, "no operation id")
	require.Empty(t, p.rec.snapshot())
}

// The lease is checked once more right before the emptied series is deleted: the
// plan and its book were written under it, the delete is the one write with no
// loop around it, and a lapse there leaves the series (and its journal row)
// alone.
func TestSeriesDenumberCore_LeaseLapsingBeforeTheDeleteKeepsTheSeries(t *testing.T) {
	p := newT6G7Probe(t)
	seedDenumberRig(p)
	checks := 0
	env := seriesDenumberEnv{
		t6g7Env:       t6g7HeadEnv(&opIDReporter{id: denumberOpID}, true),
		Reads:         p.store,
		Writer:        p.rec,
		ResolveReport: func(explicit string) (string, error) { return filepath.Join(p.root, explicit), nil },
	}
	env.StandDown = func(context.Context, string) (t6g7Hold, error) {
		// Held for the plan check and the book check; lapsed at the delete.
		return t6g7Hold{Lost: func() bool { checks++; return checks >= 3 }, Release: func() {}}, nil
	}
	err := seriesDenumberCore(context.Background(), SeriesDenumberParams{ReportPath: "denumber.tsv"}, env)
	require.ErrorContains(t, err, "lapsed before deleting series")
	require.Equal(t, denumberLiveWrites[:3], p.rec.snapshot(), "the book moved under the lease; the series was neither journaled nor deleted")
}
