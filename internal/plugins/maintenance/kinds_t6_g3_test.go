// file: internal/plugins/maintenance/kinds_t6_g3_test.go
// version: 1.0.3
// guid: 9aaa6b83-dcdb-4af3-95bd-23f236575c90
// last-edited: 2026-11-13

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/ai"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/organizer"
	pathutil "github.com/falkcorp/audiobook-organizer/internal/pathutil"
	"github.com/falkcorp/audiobook-organizer/internal/versions"
	"github.com/falkcorp/mediacore/tasks"
	"github.com/falkcorp/mediacore/tasks/taskstest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- recording fakes of t6g3Writer and t6g3Store (rule R10) ---

// t6g3RecWriter records every write a core makes through it, by method name.
type t6g3RecWriter struct {
	mu    sync.Mutex
	calls []string
	// failPurge makes RunAutoPurgeSoftDeleted fail.
	failPurge error
	// trash is what CleanupTrashedVersions answers.
	trash    versions.CleanupResult
	trashErr error
}

func (w *t6g3RecWriter) note(name string) {
	w.mu.Lock()
	w.calls = append(w.calls, name)
	w.mu.Unlock()
}

func (w *t6g3RecWriter) called() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.calls)
}

func (w *t6g3RecWriter) RunAutoPurgeSoftDeleted(context.Context, string) error {
	w.note("RunAutoPurgeSoftDeleted")
	return w.failPurge
}
func (w *t6g3RecWriter) ActivityFlushOp(string) { w.note("ActivityFlushOp") }
func (w *t6g3RecWriter) ResolveTombstoneChains() (int, error) {
	w.note("ResolveTombstoneChains")
	return 3, nil
}
func (w *t6g3RecWriter) CleanupTrashedVersions() (versions.CleanupResult, error) {
	w.note("CleanupTrashedVersions")
	return w.trash, w.trashErr
}
func (w *t6g3RecWriter) CleanupOrphanedTempFiles(string, string) int {
	w.note("CleanupOrphanedTempFiles")
	return 2
}
func (w *t6g3RecWriter) CleanupStaleTempFiles(_ string, _ pathutil.AppDirs, _ time.Duration, dryRun bool) int {
	if dryRun {
		w.note("CleanupStaleTempFiles:dry")
	} else {
		w.note("CleanupStaleTempFiles:live")
	}
	return 1
}
func (w *t6g3RecWriter) RemoveOldBackupFiles(context.Context, string, pathutil.AppDirs, func(string) (time.Time, bool), time.Duration, func(slog.Level, string, ...any)) (int, error) {
	w.note("RemoveOldBackupFiles")
	return 4, nil
}
func (w *t6g3RecWriter) CompactActivityEntries(context.Context, time.Time, database.CompactProgress) (database.CompactResult, error) {
	w.note("CompactActivityEntries")
	return database.CompactResult{DaysCompacted: 2, EntriesDeleted: 9}, nil
}
func (w *t6g3RecWriter) MaintainActivityLog(context.Context, int, int) (int, int, int64, error) {
	w.note("MaintainActivityLog")
	return 1, 2, 3, nil
}
func (w *t6g3RecWriter) SetUserPreferenceForUser(_, key, _ string) error {
	w.note("SetUserPreferenceForUser:" + key)
	return nil
}

func (w *t6g3RecWriter) ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error) {
	w.note("ModifyBook:" + id)
	b := &database.Book{ID: id}
	return b, fn(b)
}
func (w *t6g3RecWriter) SetRaw(key string, _ []byte) error {
	w.note("SetRaw:" + key)
	return nil
}

func (w *t6g3RecWriter) ModifyBookCredits(id string, _ func(*database.Book, *database.BookCreditsEdit) error) (*database.Book, database.BookCreditsEdit, error) {
	w.note("ModifyBookCredits:" + id)
	return nil, database.BookCreditsEdit{}, errors.New("recording writer: credits write refused")
}
func (w *t6g3RecWriter) CreateNarrator(name string) (*database.Narrator, error) {
	w.note("CreateNarrator:" + name)
	return &database.Narrator{ID: 1, Name: name}, nil
}
func (w *t6g3RecWriter) DeleteNarrator(id int) error {
	w.note("DeleteNarrator")
	return nil
}
func (w *t6g3RecWriter) CreateOperationChange(*database.OperationChange) error {
	w.note("CreateOperationChange")
	return nil
}
func (w *t6g3RecWriter) InvalidateAuthorsCache() { w.note("InvalidateAuthorsCache") }
func (w *t6g3RecWriter) ReplaceOpRowsPrefix(_, prefix string, _ int, _ func(int) (database.OpRow, error)) error {
	w.note("ReplaceOpRowsPrefix:" + prefix)
	return nil
}
func (w *t6g3RecWriter) SaveClassLists(string, map[string][]string) (*database.ClassListSummary, error) {
	w.note("SaveClassLists")
	return &database.ClassListSummary{}, nil
}
func (w *t6g3RecWriter) ClearClassLists(string) error {
	w.note("ClearClassLists")
	return nil
}

var _ t6g3Writer = (*t6g3RecWriter)(nil)

// t6g3FakeStore answers the group's reads from fixtures.
type t6g3FakeStore struct {
	*censusFakeStore
	root  string
	paths []database.ImportPath
	prefs []database.UserPreferenceKV
	raw   map[string][]byte
	// active is what the operation queue lists.
	active []database.OperationV2Row
}

func (s *t6g3FakeStore) RootDir() string                           { return s.root }
func (s *t6g3FakeStore) BackupRetentionDays() int                  { return 7 }
func (s *t6g3FakeStore) ActivityLogNightlyCompactionEnabled() bool { return false }
func (s *t6g3FakeStore) ActivityLogCompactionDays() int            { return 14 }
func (s *t6g3FakeStore) ActivityLogRetentionChangeDays() int       { return 30 }
func (s *t6g3FakeStore) ActivityLogRetentionDebugDays() int        { return 7 }
func (s *t6g3FakeStore) GetAllImportPaths() ([]database.ImportPath, error) {
	return s.paths, nil
}
func (s *t6g3FakeStore) GetAllPreferencesForUser(string) ([]database.UserPreferenceKV, error) {
	return s.prefs, nil
}
func (s *t6g3FakeStore) GetAllBooksCore(int, int) ([]database.BookCore, error) {
	return nil, nil
}
func (s *t6g3FakeStore) GetBookFiles(string) ([]database.BookFile, error) { return nil, nil }
func (s *t6g3FakeStore) GetMetadataFieldStates(string) ([]database.MetadataFieldState, error) {
	return nil, nil
}
func (s *t6g3FakeStore) GetUserPreference(string) (*database.UserPreference, error) { return nil, nil }
func (s *t6g3FakeStore) ListActiveOperationsV2() ([]database.OperationV2Row, error) {
	return s.active, nil
}
func (s *t6g3FakeStore) GetAllSeries() ([]database.Series, error) { return nil, nil }
func (s *t6g3FakeStore) GetOperationChanges(string) ([]*database.OperationChange, error) {
	return nil, nil
}
func (s *t6g3FakeStore) Can(c t6g3Capability) bool {
	return c == t6g3CapOpsLister || c == t6g3CapClassLists
}
func (s *t6g3FakeStore) GetNarratorByName(string) (*database.Narrator, error) {
	return nil, errT6g3NoCapability
}
func (s *t6g3FakeStore) ListOperationsV2Since(time.Time, int) ([]database.OperationV2Row, error) {
	return nil, nil
}
func (s *t6g3FakeStore) GetOpRows(string, []string) ([][]byte, error) {
	return nil, errT6g3NoCapability
}
func (s *t6g3FakeStore) ScanOpRowIndex(string, func(string, []byte) error) error {
	return errT6g3NoCapability
}
func (s *t6g3FakeStore) GetAllNarratorRefs() (database.NarratorRefs, error) {
	return database.NarratorRefs{}, errT6g3NoCapability
}
func (s *t6g3FakeStore) CountNarratorBookLinks(int) (int, error) { return 0, nil }
func (s *t6g3FakeStore) BookNarratorsLinking(map[int]bool) (map[string][]database.BookNarrator, error) {
	return nil, errT6g3NoCapability
}
func (s *t6g3FakeStore) GetRaw(key string) ([]byte, error) { return s.raw[key], nil }
func (s *t6g3FakeStore) ScanPrefix(prefix string) ([]database.KVPair, error) {
	var out []database.KVPair
	for k, v := range s.raw {
		if strings.HasPrefix(k, prefix) {
			out = append(out, database.KVPair{Key: k, Value: v})
		}
	}
	return out, nil
}

var _ t6g3Store = (*t6g3FakeStore)(nil)

// t6g3Fake is what fakeT6G3Deps builds: the recording writer and store, and the
// tasks.Writer values NewWriter was handed.
type t6g3Fake struct {
	t      *testing.T
	writer *t6g3RecWriter
	store  *t6g3FakeStore

	mu         sync.Mutex
	newWriters []tasks.Writer
}

func newT6G3Fake(t *testing.T) *t6g3Fake {
	t.Helper()
	return &t6g3Fake{
		t:      t,
		writer: &t6g3RecWriter{},
		store:  &t6g3FakeStore{censusFakeStore: newCensusFakeStore(), root: "/lib"},
	}
}

func (f *t6g3Fake) deps() t6g3Deps {
	return t6g3Deps{
		Store:        f.store,
		StandDownKey: t6g3ScanKey,
		NewWriter: func(w tasks.Writer) t6g3Writer {
			f.mu.Lock()
			f.newWriters = append(f.newWriters, w)
			f.mu.Unlock()
			return f.writer
		},
	}
}

func (f *t6g3Fake) handed() []tasks.Writer {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.newWriters)
}

// t6g3ScanKey is the library scan key the test owner declares.
const t6g3ScanKey tasks.Key = "books.library.scan"

// fakeT6G3Deps is the owner's declared dependencies as a test fake.
func fakeT6G3Deps(t *testing.T) t6g3Deps { return newT6G3Fake(t).deps() }

// t6g3Gate is a tasks.Writer the run hands NewWriter; identity is what the
// shell test compares.
type t6g3Gate struct{ id string }

func (*t6g3Gate) Do(context.Context, tasks.Write, func(context.Context) ([]tasks.Change, error)) error {
	return nil
}
func (*t6g3Gate) MarkRowApplied(context.Context, string)           {}
func (*t6g3Gate) RowApplied(context.Context, string) (bool, error) { return false, nil }

// t6g3Host is a FakeRun with a chosen mode and writer and a recorded log.
type t6g3Host struct {
	*taskstest.FakeRun
	mode tasks.Mode
	w    tasks.Writer

	mu   sync.Mutex
	logs []string
	// standDowns are the keys the run asked to stand down.
	standDowns []tasks.Key
}

type t6g3LogHandler struct{ h *t6g3Host }

func (l t6g3LogHandler) Enabled(context.Context, slog.Level) bool { return true }
func (l t6g3LogHandler) Handle(_ context.Context, r slog.Record) error {
	l.h.mu.Lock()
	l.h.logs = append(l.h.logs, r.Message)
	l.h.mu.Unlock()
	return nil
}
func (l t6g3LogHandler) WithAttrs([]slog.Attr) slog.Handler { return l }
func (l t6g3LogHandler) WithGroup(string) slog.Handler      { return l }

func (h *t6g3Host) Mode() tasks.Mode     { return h.mode }
func (h *t6g3Host) Writer() tasks.Writer { return h.w }

// t6g3Hold is the stand-down hold a test host hands out (FakeRun models none).
type t6g3Hold struct{}

func (t6g3Hold) Release() {}

// StandDown hands out a hold that is never lost, which is what a credits
// backfill apply needs to get past its scan stand-down.
func (h *t6g3Host) StandDown(k tasks.Key) (tasks.Hold, error) {
	h.mu.Lock()
	h.standDowns = append(h.standDowns, k)
	h.mu.Unlock()
	return t6g3Hold{}, nil
}
func (h *t6g3Host) Log() *slog.Logger { return slog.New(t6g3LogHandler{h}) }

func newT6G3Host(kind tasks.Kind, mode tasks.Mode, w tasks.Writer) (*t6g3Host, *tasks.RC) {
	h := &t6g3Host{FakeRun: taskstest.NewFakeRun(context.Background(), kind), mode: mode, w: w}
	return h, tasks.NewRC(h)
}

// t6g3Run runs d's own Run with the params the executor would decode.
func t6g3Run(t *testing.T, d tasks.Definition, rc *tasks.RC, params string) (json.RawMessage, error) {
	t.Helper()
	r, ok := d.(interface {
		ExecutorRunners() (func(json.RawMessage) (any, error), *tasks.TaskSpec[any, json.RawMessage], *tasks.BatchSpec[any, any, json.RawMessage], *tasks.FixerSpec[any, any])
	})
	require.True(t, ok, "%T has no ExecutorRunners", d)
	decode, task, _, _ := r.ExecutorRunners()
	require.NotNil(t, task)
	var raw json.RawMessage
	if params != "" {
		raw = json.RawMessage(params)
	}
	p, err := decode(raw)
	require.NoError(t, err)
	return task.Run(rc, p)
}

// t6g3KindByName returns the native kind of one HEAD def ID.
func t6g3KindByName(t *testing.T, defs []tasks.Definition, headID string) tasks.Definition {
	t.Helper()
	for _, d := range defs {
		if d.Kind() == tasks.Kind("books."+headID) {
			return d
		}
	}
	t.Fatalf("no native kind for %s", headID)
	return nil
}

// --- the three tests the brief names ---

func TestT6G3Kinds_Conformance(t *testing.T) {
	defs := (&Plugin{}).t6g3Kinds(fakeT6G3Deps(t), nil)
	taskstest.Conformance(t, defs...)
}

// t6g3Want is one converted definition's expected native shape, written out
// per kind as literals (never derived from the code under test).
type t6g3Want struct {
	headID   string
	title    string
	resume   tasks.ResumePolicy
	key      tasks.Key
	timeout  time.Duration
	progress time.Duration
	readOnly bool
	writes   []string
	par      string
}

var t6g3Wants = []t6g3Want{
	{"maintenance.purge-deleted", "Purge soft-deleted books", tasks.ResumeDrop, "books.maintenance.purge-deleted", 2 * time.Hour, 0, false, []string{"activity", "books", "external_ids", "files", "operations"}, ""},
	{"maintenance.tombstone-cleanup", "Resolve author tombstone chains", tasks.ResumeDrop, "books.maintenance.tombstone-cleanup", time.Hour, 0, false, []string{"authors"}, ""},
	{"maintenance.temp-file-cleanup", "Clean orphaned temp files", tasks.ResumeDrop, "books.maintenance.temp-file-cleanup", time.Hour, time.Hour, false, []string{"activity", "files"}, ""},
	{"maintenance.transcode-temp-cleanup", "Clean stale transcode temp files", tasks.ResumeDrop, "books.maintenance.temp-file-cleanup", time.Hour, time.Hour, false, []string{"files"}, ""},
	{"maintenance.cleanup-activity-log", "Clean activity log", tasks.ResumeDrop, "books.maintenance.cleanup-activity-log", 30 * time.Minute, 20 * time.Minute, false, []string{"activity"}, ""},
	{"maintenance.cleanup-old-backups", "Clean old backup files", tasks.ResumeFromZero, "books.maintenance.cleanup-old-backups", time.Hour, time.Hour, false, []string{"files"}, ""},
	{"maintenance.trash-cleanup", "Purge trashed book versions", tasks.ResumeDrop, "books.maintenance.trash-cleanup", time.Hour, time.Hour, false, []string{"book_versions", "files"}, ""},
	{"maintenance.clear-apply-rename-failures", "Clear apply rename-failure records", tasks.ResumeDrop, "books.maintenance.clear-apply-rename-failures", 10 * time.Minute, 10 * time.Minute, false, []string{"user_state"}, ""},
	{"maintenance.combine-journal-check", "Combine journal readability check", tasks.ResumeDrop, "books.maintenance.combine-journal-check", 10 * time.Minute, 0, true, nil, ""},
	{"maintenance.compact-activity-log", "Compact activity log", tasks.ResumeDrop, "books.maintenance.cleanup-activity-log", 6 * time.Hour, 20 * time.Minute, false, []string{"activity"}, ""},
	{"maintenance.folder-cover-backfill", "Use folder cover images", tasks.ResumeDrop, "books.maintenance.folder-cover-backfill", 120 * time.Minute, 0, false, []string{"books"}, ""},
	{"maintenance.cover-text-read", "Read cover text (vision)", tasks.ResumeDrop, "books.maintenance.cover-text-read", 24 * time.Hour, 0, false, []string{"cover_text"}, ""},
	{"maintenance.credit-census", "Author/narrator credit census", tasks.ResumeDrop, "books.maintenance.credit-census", 2 * time.Hour, 0, true, nil, ""},
	{"maintenance.credits-backfill", "Author/narrator credits backfill", tasks.ResumeContinue, "books.maintenance.credit-census", 12 * time.Hour, 0, false, []string{"authors", "books", "narrators", "operations"}, ""},
}

func TestT6G3Kinds_MatchHEADDefs(t *testing.T) {
	defs := (&Plugin{}).t6g3Kinds(fakeT6G3Deps(t), nil)
	require.Len(t, defs, len(t6g3Wants), "every converted definition has exactly one native kind")
	for i, w := range t6g3Wants {
		d := defs[i]
		t.Run(w.headID, func(t *testing.T) {
			c := tasks.CommonOf(d)
			assert.Equal(t, tasks.Kind("books."+w.headID), d.Kind(), "R3: kind is books.<HEAD ID>")
			assert.Equal(t, w.title, c.Title)
			assert.Equal(t, w.resume, c.Resume, "R5 resume mapping")
			assert.Equal(t, w.timeout, c.Timeout)
			assert.Equal(t, w.progress, c.ProgressTimeout)
			assert.Equal(t, w.key, c.ConcurrencyKey, "R5 concurrency key")
			assert.Equal(t, tasks.PriorityLow, c.Priority)
			assert.Equal(t, []tasks.ReadySignal{"books.memdb"}, c.Ready)
			assert.Nil(t, c.Schedule, "R9: nil sched means no schedule")
			assert.False(t, c.Perms.Start.IsZero(), "R6")
			assert.True(t, c.Effects.Declared())
			assert.Equal(t, w.readOnly, c.Effects.IsReadOnly(), "R7 effects class")
			if !w.readOnly {
				assert.Equal(t, w.writes, t6g3WriteKinds(c.Effects), "R7 write set")
			}
			assert.Equal(t, w.par, t6g3ParString(d), "a Task declares no Parallelism: its loops pass theirs to RunItems")
		})
	}
}

// TestT6G3_LoopParallelism pins rule R4 for each in-body loop, as literals.
func TestT6G3_LoopParallelism(t *testing.T) {
	assert.Equal(t, "cpu", creditCensusParallelism().String(), "HEAD: runtime.NumCPU()")
	assert.Equal(t, "cpu", creditsBackfillParallelism().String(), "HEAD: runtime.NumCPU()")
	// The cover ops' pools are sized by the core (workers param, else twice the
	// cores, clamped 1-32), not by a def-level constant.
	assert.Equal(t, min(max(runtime.NumCPU()*2, 1), 32), coverOpDefaultWorkers(0))
	assert.Equal(t, 3, coverOpDefaultWorkers(3))
	assert.Equal(t, 32, coverOpDefaultWorkers(500))
}

// t6g3ParString renders a definition's declared parallelism, "" for a Task.
func t6g3ParString(d tasks.Definition) string {
	p := tasks.ParallelismOf(d)
	if p == (tasks.Parallelism{}) {
		return ""
	}
	return p.String()
}

// t6g3WriteKinds lists the books resource kinds an Effects writes, sorted.
func t6g3WriteKinds(e tasks.Effects) []string {
	var out []string
	for _, r := range e.WriteSet() {
		out = append(out, r.Kind)
	}
	slices.Sort(out)
	return out
}

func TestT6G3Kinds_WritesThroughShell(t *testing.T) {
	root := t.TempDir()
	cases := []struct {
		headID string
		params string
		setup  func(f *t6g3Fake)
		// wantCalls are the writer calls a live run must make, in order.
		wantCalls []string
	}{
		{headID: "maintenance.purge-deleted", wantCalls: []string{"RunAutoPurgeSoftDeleted", "ActivityFlushOp"}},
		{headID: "maintenance.tombstone-cleanup", wantCalls: []string{"ResolveTombstoneChains"}},
		{headID: "maintenance.temp-file-cleanup", wantCalls: []string{"CleanupOrphanedTempFiles"}},
		{
			headID: "maintenance.transcode-temp-cleanup",
			setup: func(f *t6g3Fake) {
				f.store.paths = []database.ImportPath{{ID: 1, Path: root}}
			},
			wantCalls: []string{"CleanupStaleTempFiles:live"},
		},
		{headID: "maintenance.cleanup-activity-log", wantCalls: []string{"CompactActivityEntries", "MaintainActivityLog"}},
		{headID: "maintenance.cleanup-old-backups", wantCalls: []string{"RemoveOldBackupFiles"}},
		{headID: "maintenance.trash-cleanup", wantCalls: []string{"CleanupTrashedVersions"}},
		{
			headID: "maintenance.clear-apply-rename-failures",
			setup: func(f *t6g3Fake) {
				f.store.prefs = []database.UserPreferenceKV{{
					Key:   organizer.ApplyRenameFailurePrefix + "bk-1",
					Value: `{"target_path":"/a","reason":"collision"}`,
				}}
			},
			wantCalls: []string{"SetUserPreferenceForUser:" + organizer.ApplyRenameFailurePrefix + "bk-1"},
		},
		{headID: "maintenance.compact-activity-log", params: `{"older_than_days":3}`, wantCalls: []string{"CompactActivityEntries"}},
	}
	for _, tc := range cases {
		t.Run(tc.headID, func(t *testing.T) {
			f := newT6G3Fake(t)
			if tc.setup != nil {
				tc.setup(f)
			}
			d := t6g3KindByName(t, (&Plugin{}).t6g3Kinds(f.deps(), nil), tc.headID)
			require.False(t, tasks.CommonOf(d).Effects.IsReadOnly(), "this table is the kinds that declare writes")
			gate := &t6g3Gate{id: tc.headID}
			_, rc := newT6G3Host(d.Kind(), tasks.ModeLive, gate)
			_, err := t6g3Run(t, d, rc, tc.params)
			require.NoError(t, err)
			handed := f.handed()
			require.Len(t, handed, 1, "NewWriter is called once per run")
			assert.Same(t, gate, handed[0], "NewWriter is handed this run's rc.Writer()")
			assert.Equal(t, tc.wantCalls, f.writer.called(), "every write reaches the recording t6g3Writer")
		})
	}

	t.Run("read-only kinds get no writer", func(t *testing.T) {
		f := newT6G3Fake(t)
		for _, headID := range []string{"maintenance.combine-journal-check", "maintenance.credit-census"} {
			d := t6g3KindByName(t, (&Plugin{}).t6g3Kinds(f.deps(), nil), headID)
			require.True(t, tasks.CommonOf(d).Effects.IsReadOnly())
			_, rc := newT6G3Host(d.Kind(), tasks.ModeLive, nil)
			_, err := t6g3Run(t, d, rc, "")
			require.NoError(t, err, headID)
		}
		assert.Empty(t, f.handed(), "a ReadOnly run never asks for a writer")
		assert.Empty(t, f.writer.called())
	})

	t.Run("live-only writers refuse a preview and write nothing", func(t *testing.T) {
		for _, headID := range t6g3LiveOnlyIDs {
			f := newT6G3Fake(t)
			d := t6g3KindByName(t, (&Plugin{}).t6g3Kinds(f.deps(), nil), headID)
			_, rc := newT6G3Host(d.Kind(), tasks.ModePreview, &t6g3Gate{})
			params := ""
			if headID == "maintenance.compact-activity-log" {
				params = `{"older_than_days":3}`
			}
			_, err := t6g3Run(t, d, rc, params)
			require.Error(t, err, headID)
			assert.Contains(t, err.Error(), "preview not supported; start with mode live", headID)
			assert.Empty(t, f.writer.called(), headID)
		}
	})

	t.Run("a missing NewWriter fails the run before the core", func(t *testing.T) {
		d := t6g3KindByName(t, (&Plugin{}).t6g3Kinds(t6g3Deps{Store: newT6G3Fake(t).store}, nil), "maintenance.tombstone-cleanup")
		_, rc := newT6G3Host(d.Kind(), tasks.ModeLive, &t6g3Gate{})
		_, err := t6g3Run(t, d, rc, "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "NewWriter is not set")
	})
}

// t6g3LiveOnlyIDs are the defs HEAD runs only in an explicit live mode (R7
// "Live-only writer").
var t6g3LiveOnlyIDs = []string{
	"maintenance.purge-deleted",
	"maintenance.tombstone-cleanup",
	"maintenance.temp-file-cleanup",
	"maintenance.cleanup-activity-log",
	"maintenance.compact-activity-log",
	"maintenance.cleanup-old-backups",
	"maintenance.trash-cleanup",
}

// t6g3VisionStub is a coverTextReader with a fixed capacity.
type t6g3VisionStub struct{ capacity int }

func (v t6g3VisionStub) Capacity() int { return v.capacity }
func (v t6g3VisionStub) ReadCoverText(context.Context, []byte, string) (*ai.CoverTextResult, error) {
	return nil, errors.New("no model in a test")
}

func TestT6G3_CreditsBackfillApplyWritesThroughTheWriter(t *testing.T) {
	f := newT6G3Fake(t)
	authorID := 1
	f.store.authors[1] = database.Author{ID: 1, Name: "Bruce Wayne"}
	f.store.books["bk-1"] = &database.Book{ID: "bk-1", Title: "Year One", AuthorID: &authorID}
	d := t6g3KindByName(t, (&Plugin{}).t6g3Kinds(f.deps(), nil), "maintenance.credits-backfill")
	gate := &t6g3Gate{id: "credits"}
	_, rc := newT6G3Host(d.Kind(), tasks.ModeLive, gate)
	_, err := t6g3Run(t, d, rc, "")
	require.Error(t, err, "the recording writer refuses the credits write, so the run reports it")
	calls := f.writer.called()
	assert.Contains(t, calls, "CreateOperationChange", "the journal row goes through the writer")
	assert.Contains(t, calls, "ModifyBookCredits:bk-1", "the book write goes through the writer")
	assert.Contains(t, calls, "SaveClassLists", "the class lists go through the writer")
	assert.Same(t, gate, f.handed()[0])
}

func TestT6G3_CreditsBackfillPreviewWritesNothing(t *testing.T) {
	f := newT6G3Fake(t)
	authorID := 1
	f.store.authors[1] = database.Author{ID: 1, Name: "Bruce Wayne"}
	f.store.books["bk-1"] = &database.Book{ID: "bk-1", Title: "Year One", AuthorID: &authorID}
	d := t6g3KindByName(t, (&Plugin{}).t6g3Kinds(f.deps(), nil), "maintenance.credits-backfill")
	_, rc := newT6G3Host(d.Kind(), tasks.ModePreview, &t6g3Gate{})
	_, err := t6g3Run(t, d, rc, "")
	require.NoError(t, err)
	for _, c := range f.writer.called() {
		assert.NotContains(t, c, "ModifyBookCredits", "a preview never writes a book")
		assert.NotContains(t, c, "CreateNarrator")
		assert.NotContains(t, c, "CreateOperationChange")
	}
}

func TestT6G3_ModeKeysAreRefused(t *testing.T) {
	// Every native shell that writes refuses a mode key (decision 3), in any
	// case; the spelling varies per kind on purpose.
	cases := map[string]string{
		"maintenance.purge-deleted":               `{"dry_run":false}`,
		"maintenance.tombstone-cleanup":           `{"dryRun":false}`,
		"maintenance.temp-file-cleanup":           `{"APPLY":true}`,
		"maintenance.transcode-temp-cleanup":      `{"dry_run":true}`,
		"maintenance.cleanup-activity-log":        `{"Apply":true}`,
		"maintenance.cleanup-old-backups":         `{"dry_run":false}`,
		"maintenance.trash-cleanup":               `{"dryRun":false}`,
		"maintenance.clear-apply-rename-failures": `{"apply":true}`,
		"maintenance.compact-activity-log":        `{"older_than_days":3,"dry_run":false}`,
		"maintenance.folder-cover-backfill":       `{"apply":true}`,
		"maintenance.cover-text-read":             `{"dryRun":false}`,
		"maintenance.credits-backfill":            `{"Apply":true}`,
	}
	for id, params := range cases {
		f := newT6G3Fake(t)
		d := t6g3KindByName(t, (&Plugin{}).t6g3Kinds(f.deps(), nil), id)
		_, rc := newT6G3Host(d.Kind(), tasks.ModePreview, &t6g3Gate{})
		_, err := t6g3Run(t, d, rc, params)
		require.Error(t, err, id)
		assert.Contains(t, err.Error(), "mode comes from the start request", id)
		assert.Empty(t, f.writer.called(), id)
	}
}

func TestT6G3_CompactActivityLogRequiresItsAge(t *testing.T) {
	f := newT6G3Fake(t)
	d := t6g3KindByName(t, (&Plugin{}).t6g3Kinds(f.deps(), nil), "maintenance.compact-activity-log")
	_, rc := newT6G3Host(d.Kind(), tasks.ModeLive, &t6g3Gate{})
	_, err := t6g3Run(t, d, rc, "")
	require.Error(t, err, "HEAD refuses a run with no older_than_days")
	assert.Empty(t, f.writer.called())
}

func TestT6G3_FolderCoverRefusesALiveRunWhileAScanRuns(t *testing.T) {
	f := newT6G3Fake(t)
	f.store.active = []database.OperationV2Row{{ID: "op-scan", DefID: "library.scan", Status: "running"}}
	d := t6g3KindByName(t, (&Plugin{}).t6g3Kinds(f.deps(), nil), "maintenance.folder-cover-backfill")
	_, rc := newT6G3Host(d.Kind(), tasks.ModeLive, &t6g3Gate{})
	_, err := t6g3Run(t, d, rc, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "library.scan is running")
	assert.Empty(t, f.writer.called())

	// The same scan does not refuse a preview.
	_, prc := newT6G3Host(d.Kind(), tasks.ModePreview, &t6g3Gate{})
	_, err = t6g3Run(t, d, prc, "")
	require.NoError(t, err)
}

func TestT6G3_CoverTextReadNeedsACapableReaderWhenLive(t *testing.T) {
	old := newCoverTextReader
	newCoverTextReader = func() coverTextReader { return t6g3VisionStub{capacity: 0} }
	t.Cleanup(func() { newCoverTextReader = old })
	f := newT6G3Fake(t)
	d := t6g3KindByName(t, (&Plugin{}).t6g3Kinds(f.deps(), nil), "maintenance.cover-text-read")
	_, rc := newT6G3Host(d.Kind(), tasks.ModeLive, &t6g3Gate{})
	_, err := t6g3Run(t, d, rc, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no AI endpoint can read cover text")
	assert.Empty(t, f.writer.called())
}
