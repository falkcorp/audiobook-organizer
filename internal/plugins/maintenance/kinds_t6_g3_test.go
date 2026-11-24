// file: internal/plugins/maintenance/kinds_t6_g3_test.go
// version: 1.0.12
// guid: 9aaa6b83-dcdb-4af3-95bd-23f236575c90
// last-edited: 2026-11-21

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance/resources"
	"log/slog"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/ai"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/organizer"
	pathutil "github.com/falkcorp/audiobook-organizer/internal/pathutil"
	"github.com/falkcorp/audiobook-organizer/internal/versions"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
	"github.com/falkcorp/mediacore/tasks"
	"github.com/falkcorp/mediacore/tasks/taskstest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- recording fakes of t6g3Writer and t6g3Store (rule R10) ---

// t6g3MethodResources maps each t6g3Writer write method to the pkg/ops/effects.go
// resources it can write: this group's view of t6WriterResources
// (kinds_resources.go), the one method table of every converted group, so a method
// carries the same row here as in every other group (ModifyBook writes the books
// row, the metadata-cache stale stamp and the ident: rows; ModifyBookCredits the
// same plus the author junction; the purge row is built from the carry and the
// book delete). A recording writer notes the resources of every method a run
// calls, and the test checks them against the kind's declared WriteSet, so the
// declaration is judged against what the run did, not against a second copy of
// itself. A method with no entry fails the test. SetRaw is this group's
// cover-text record (the same resource as t6g3CoverTextResource, cover_ops.go).
// The run's own rows (opv2:row:) and the judged markers (ReplaceOpRowsPrefix,
// SaveClassLists, ClearClassLists) are op rows, not an Effects resource: mapped
// to an empty row.
var t6g3MethodResources = t6WriterView(map[string]string{"SetRaw": "SetRaw/cover_text"}, reflect.TypeFor[t6g3Writer]())

// Every writer method has an entry, and no entry names a method the writer lacks.
func TestT6G3_MethodResourcesCoverTheWriterInterface(t *testing.T) {
	typ := reflect.TypeFor[t6g3Writer]()
	seen := map[string]bool{}
	for method := range typ.Methods() {
		name := method.Name
		seen[name] = true
		_, ok := t6g3MethodResources[name]
		assert.True(t, ok, "t6g3Writer.%s has no entry in t6g3MethodResources", name)
	}
	for name := range t6g3MethodResources {
		assert.True(t, seen[name], "t6g3MethodResources names %s, which t6g3Writer does not have", name)
	}
}

// t6g3ResLog collects the resources of the write methods a recording writer saw.
type t6g3ResLog struct {
	rmu       sync.Mutex
	resources map[string]bool
	unmapped  []string
}

// record notes the resources of call ("Method" or "Method:detail").
func (l *t6g3ResLog) record(call string) {
	method, _, _ := strings.Cut(call, ":")
	l.rmu.Lock()
	defer l.rmu.Unlock()
	res, ok := t6g3MethodResources[method]
	if !ok {
		l.unmapped = append(l.unmapped, method)
		return
	}
	if l.resources == nil {
		l.resources = map[string]bool{}
	}
	for _, r := range res {
		l.resources[string(r)] = true
	}
}

// merge adds what o saw.
func (l *t6g3ResLog) merge(o *t6g3ResLog) {
	o.rmu.Lock()
	defer o.rmu.Unlock()
	l.rmu.Lock()
	defer l.rmu.Unlock()
	if l.resources == nil {
		l.resources = map[string]bool{}
	}
	for r := range o.resources {
		l.resources[r] = true
	}
	l.unmapped = append(l.unmapped, o.unmapped...)
}

// assertWithin fails when a run wrote a resource d does not declare in its
// WriteSet, or called a writer method t6g3MethodResources does not know.
func (l *t6g3ResLog) assertWithin(t *testing.T, d tasks.Definition) {
	t.Helper()
	l.rmu.Lock()
	defer l.rmu.Unlock()
	require.Empty(t, l.unmapped, "writer methods with no entry in t6g3MethodResources")
	declared := t6g3WriteKinds(tasks.CommonOf(d).Effects)
	for r := range l.resources {
		assert.Contains(t, declared, r, "%s: the run wrote resource %q that its Effects do not declare", d.Kind(), r)
	}
}

// t6g3RecWriter records every write a core makes through it, by method name.
type t6g3RecWriter struct {
	t6g3ResLog
	mu    sync.Mutex
	calls []string
	// failPurge makes RunAutoPurgeSoftDeleted fail.
	failPurge error
	// trash is what CleanupTrashedVersions answers.
	trash    versions.CleanupResult
	trashErr error
}

func (w *t6g3RecWriter) note(name string) {
	w.record(name)
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
func (w *t6g3RecWriter) CleanupStaleTempFiles(string, pathutil.AppDirs, time.Duration) int {
	w.note("CleanupStaleTempFiles")
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

// t6g3ForwardSpy wraps a real t6g3Writer, records every call (by method name, with
// the id or key where one identifies it) and the resources it writes, and
// forwards. It implements every method itself and embeds nothing, so a method
// added to t6g3Writer breaks the compile here, and a method with no entry in
// t6g3MethodResources fails assertWithin.
type t6g3ForwardSpy struct {
	t6g3ResLog
	inner t6g3Writer
	mu    sync.Mutex
	calls []string
}

var _ t6g3Writer = (*t6g3ForwardSpy)(nil)

func newT6G3ForwardSpy(inner t6g3Writer) *t6g3ForwardSpy { return &t6g3ForwardSpy{inner: inner} }

func (s *t6g3ForwardSpy) note(c string) {
	s.record(c)
	s.mu.Lock()
	s.calls = append(s.calls, c)
	s.mu.Unlock()
}

func (s *t6g3ForwardSpy) called() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.calls)
}

// argsOf returns the detail of every call of method ("Method:detail" -> detail).
func (s *t6g3ForwardSpy) argsOf(method string) []string {
	var out []string
	for _, c := range s.called() {
		if m, d, ok := strings.Cut(c, ":"); ok && m == method {
			out = append(out, d)
		}
	}
	return out
}

func (s *t6g3ForwardSpy) RunAutoPurgeSoftDeleted(ctx context.Context, opID string) error {
	s.note("RunAutoPurgeSoftDeleted")
	return s.inner.RunAutoPurgeSoftDeleted(ctx, opID)
}
func (s *t6g3ForwardSpy) ActivityFlushOp(opID string) {
	s.note("ActivityFlushOp")
	s.inner.ActivityFlushOp(opID)
}
func (s *t6g3ForwardSpy) ResolveTombstoneChains() (int, error) {
	s.note("ResolveTombstoneChains")
	return s.inner.ResolveTombstoneChains()
}
func (s *t6g3ForwardSpy) CleanupTrashedVersions() (versions.CleanupResult, error) {
	s.note("CleanupTrashedVersions")
	return s.inner.CleanupTrashedVersions()
}
func (s *t6g3ForwardSpy) CleanupOrphanedTempFiles(rootDir string, opID string) int {
	s.note("CleanupOrphanedTempFiles")
	return s.inner.CleanupOrphanedTempFiles(rootDir, opID)
}
func (s *t6g3ForwardSpy) CleanupStaleTempFiles(root string, app pathutil.AppDirs, maxAge time.Duration) int {
	s.note("CleanupStaleTempFiles")
	return s.inner.CleanupStaleTempFiles(root, app, maxAge)
}
func (s *t6g3ForwardSpy) RemoveOldBackupFiles(ctx context.Context, root string, app pathutil.AppDirs, match func(string) (time.Time, bool), maxAge time.Duration, log func(slog.Level, string, ...any)) (int, error) {
	s.note("RemoveOldBackupFiles")
	return s.inner.RemoveOldBackupFiles(ctx, root, app, match, maxAge, log)
}
func (s *t6g3ForwardSpy) CompactActivityEntries(ctx context.Context, cutoff time.Time, progress database.CompactProgress) (database.CompactResult, error) {
	s.note("CompactActivityEntries")
	return s.inner.CompactActivityEntries(ctx, cutoff, progress)
}
func (s *t6g3ForwardSpy) MaintainActivityLog(ctx context.Context, changeDays, debugDays int) (int, int, int64, error) {
	s.note("MaintainActivityLog")
	return s.inner.MaintainActivityLog(ctx, changeDays, debugDays)
}
func (s *t6g3ForwardSpy) SetUserPreferenceForUser(userID, key, value string) error {
	s.note("SetUserPreferenceForUser:" + key)
	return s.inner.SetUserPreferenceForUser(userID, key, value)
}
func (s *t6g3ForwardSpy) ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error) {
	s.note("ModifyBook:" + id)
	return s.inner.ModifyBook(id, fn)
}
func (s *t6g3ForwardSpy) SetRaw(key string, value []byte) error {
	s.note("SetRaw:" + key)
	return s.inner.SetRaw(key, value)
}
func (s *t6g3ForwardSpy) ModifyBookCredits(bookID string, fn func(*database.Book, *database.BookCreditsEdit) error) (*database.Book, database.BookCreditsEdit, error) {
	s.note("ModifyBookCredits:" + bookID)
	return s.inner.ModifyBookCredits(bookID, fn)
}
func (s *t6g3ForwardSpy) CreateNarrator(name string) (*database.Narrator, error) {
	s.note("CreateNarrator:" + name)
	return s.inner.CreateNarrator(name)
}
func (s *t6g3ForwardSpy) DeleteNarrator(id int) error {
	s.note("DeleteNarrator")
	return s.inner.DeleteNarrator(id)
}
func (s *t6g3ForwardSpy) CreateOperationChange(c *database.OperationChange) error {
	s.note("CreateOperationChange:" + c.ChangeType)
	return s.inner.CreateOperationChange(c)
}
func (s *t6g3ForwardSpy) InvalidateAuthorsCache() {
	s.note("InvalidateAuthorsCache")
	s.inner.InvalidateAuthorsCache()
}
func (s *t6g3ForwardSpy) ReplaceOpRowsPrefix(opID, prefix string, n int, at func(int) (database.OpRow, error)) error {
	s.note("ReplaceOpRowsPrefix:" + prefix)
	return s.inner.ReplaceOpRowsPrefix(opID, prefix, n, at)
}
func (s *t6g3ForwardSpy) SaveClassLists(opID string, lists map[string][]string) (*database.ClassListSummary, error) {
	s.note("SaveClassLists")
	return s.inner.SaveClassLists(opID, lists)
}
func (s *t6g3ForwardSpy) ClearClassLists(opID string) error {
	s.note("ClearClassLists")
	return s.inner.ClearClassLists(opID)
}

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
	return c == t6g3CapOpsLister
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
	// runs is the credits backfill's run store (runningCBRuns), made on the
	// first deps().
	runs tasks.RunStore

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
	if f.runs == nil {
		f.runs = runningCBRuns(f.t)
	}
	return t6g3Deps{
		Store:        f.store,
		StandDownKey: t6g3ScanKey,
		Runs:         f.runs,
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
	// lines are the log lines with their attributes.
	lines []t6g3AttrLine
	// standDowns are the keys the run asked to stand down.
	standDowns []tasks.Key
}

// t6g3AttrLine is one log line with its attributes, values rendered as text.
type t6g3AttrLine struct {
	Level slog.Level
	Msg   string
	Attrs map[string]string
}

// t6g3FindLine returns the first line whose message contains sub.
func t6g3FindLine(lines []t6g3AttrLine, sub string) (t6g3AttrLine, bool) {
	for _, l := range lines {
		if strings.Contains(l.Msg, sub) {
			return l, true
		}
	}
	return t6g3AttrLine{}, false
}

type t6g3LogHandler struct{ h *t6g3Host }

func (l t6g3LogHandler) Enabled(context.Context, slog.Level) bool { return true }
func (l t6g3LogHandler) Handle(_ context.Context, r slog.Record) error {
	line := t6g3AttrLine{Level: r.Level, Msg: r.Message, Attrs: map[string]string{}}
	r.Attrs(func(a slog.Attr) bool {
		line.Attrs[a.Key] = a.Value.String()
		return true
	})
	l.h.mu.Lock()
	l.h.logs = append(l.h.logs, r.Message)
	l.h.lines = append(l.h.lines, line)
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

// Renew succeeds: this hold is never lost.
func (t6g3Hold) Renew() error { return nil }

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
	par      string
}

// t6g3KindWrites is what one kind's core writes: the writer methods it can call
// (the keys of the group's view of the shared table, t6g3MethodResources) and the
// resources it writes without a writer call. The declared write set a kind must
// carry is DERIVED from it (t6g3ExpectedWrites): the sorted union of the shared
// table's rows for calls plus outside. It is never retyped as a resource list, so
// a row that widens widens the expectation, and a kind that did not widen with it
// fails TestT6G3Kinds_MatchHEADDefs. A kind with neither is ReadOnly.
type t6g3KindWrites struct {
	calls   []string
	outside []ops.Resource
}

var t6g3KindCalls = map[string]t6g3KindWrites{
	"maintenance.purge-deleted":               {calls: []string{"RunAutoPurgeSoftDeleted", "ActivityFlushOp"}},
	"maintenance.tombstone-cleanup":           {calls: []string{"ResolveTombstoneChains"}},
	"maintenance.temp-file-cleanup":           {calls: []string{"CleanupOrphanedTempFiles"}},
	"maintenance.transcode-temp-cleanup":      {calls: []string{"CleanupStaleTempFiles"}},
	"maintenance.cleanup-activity-log":        {calls: []string{"CompactActivityEntries", "MaintainActivityLog"}},
	"maintenance.cleanup-old-backups":         {calls: []string{"RemoveOldBackupFiles"}},
	"maintenance.trash-cleanup":               {calls: []string{"CleanupTrashedVersions"}},
	"maintenance.clear-apply-rename-failures": {calls: []string{"SetUserPreferenceForUser"}},
	"maintenance.combine-journal-check":       {},
	"maintenance.compact-activity-log":        {calls: []string{"CompactActivityEntries"}},
	// The folder cover copies the image into the book's folder (files) and stamps
	// the book (ModifyBook).
	"maintenance.folder-cover-backfill": {calls: []string{"ModifyBook"}, outside: []ops.Resource{ops.ResFiles}},
	"maintenance.cover-text-read":       {calls: []string{"SetRaw"}},
	"maintenance.credit-census":         {},
	"maintenance.credits-backfill": {calls: []string{
		"ModifyBookCredits", "CreateNarrator", "DeleteNarrator", "CreateOperationChange", "InvalidateAuthorsCache"}},
}

// t6g3ExpectedWrites is the write set the kind must declare, sorted: the union of
// t6g3MethodResources' rows for its calls and its outside resources. A method with
// no row in the group's view fails the test rather than counting as empty.
func t6g3ExpectedWrites(t *testing.T, headID string) []string {
	t.Helper()
	kw, ok := t6g3KindCalls[headID]
	require.True(t, ok, "t6g3KindCalls has no entry for %s", headID)
	rows := [][]ops.Resource{kw.outside}
	for _, m := range kw.calls {
		row, ok := t6g3MethodResources[m]
		require.True(t, ok, "%s: calls %s, which has no row in t6g3MethodResources", headID, m)
		rows = append(rows, row)
	}
	var out []string
	for _, r := range resources.Union(rows...) {
		out = append(out, string(r))
	}
	return out
}

var t6g3Wants = []t6g3Want{
	{"maintenance.purge-deleted", "Purge soft-deleted books past retention", tasks.ResumeDrop, "books.maintenance.purge-deleted", 2 * time.Hour, 0, false, ""},
	{"maintenance.tombstone-cleanup", "Resolve author tombstone chains", tasks.ResumeDrop, "books.maintenance.tombstone-cleanup", time.Hour, 0, false, ""},
	{"maintenance.temp-file-cleanup", "Clean orphaned temp files", tasks.ResumeDrop, "books.maintenance.temp-file-cleanup", time.Hour, time.Hour, false, ""},
	{"maintenance.transcode-temp-cleanup", "Clean stale transcode temp files", tasks.ResumeDrop, "books.maintenance.temp-file-cleanup", time.Hour, time.Hour, false, ""},
	{"maintenance.cleanup-activity-log", "Clean activity log", tasks.ResumeDrop, "books.maintenance.cleanup-activity-log", 30 * time.Minute, 20 * time.Minute, false, ""},
	{"maintenance.cleanup-old-backups", "Clean old backup files", tasks.ResumeFromZero, "books.maintenance.cleanup-old-backups", time.Hour, time.Hour, false, ""},
	{"maintenance.trash-cleanup", "Purge trashed book versions", tasks.ResumeDrop, "books.maintenance.trash-cleanup", time.Hour, time.Hour, false, ""},
	{"maintenance.clear-apply-rename-failures", "Clear apply rename-failure records", tasks.ResumeDrop, "books.maintenance.clear-apply-rename-failures", 10 * time.Minute, 10 * time.Minute, false, ""},
	{"maintenance.combine-journal-check", "Combine journal readability check", tasks.ResumeDrop, "books.maintenance.combine-journal-check", 10 * time.Minute, 0, true, ""},
	{"maintenance.compact-activity-log", "Compact activity log", tasks.ResumeDrop, "books.maintenance.cleanup-activity-log", 6 * time.Hour, 20 * time.Minute, false, ""},
	{"maintenance.folder-cover-backfill", "Use folder cover images", tasks.ResumeDrop, "books.maintenance.folder-cover-backfill", 120 * time.Minute, 0, false, ""},
	{"maintenance.cover-text-read", "Read cover text (vision)", tasks.ResumeDrop, "books.maintenance.cover-text-read", 24 * time.Hour, 0, false, ""},
	{"maintenance.credit-census", "Author/narrator credit census", tasks.ResumeDrop, "books.maintenance.credit-census", 2 * time.Hour, 0, true, ""},
	{"maintenance.credits-backfill", "Author/narrator credits backfill", tasks.ResumeContinue, "books.maintenance.credit-census", 12 * time.Hour, 0, false, ""},
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
			if w.readOnly {
				assert.Empty(t, t6g3KindCalls[w.headID].calls, "a ReadOnly kind has no writer method to call")
			} else {
				assert.Equal(t, t6g3ExpectedWrites(t, w.headID), t6g3WriteKinds(c.Effects), "R7 write set: the union of the rows of every method the kind can call")
			}
			assert.Equal(t, w.par, t6g3ParString(d), "a Task declares no Parallelism: its loops pass theirs to RunItems")
		})
	}
}

// t6g3KindCalls names exactly the group's kinds, and a ReadOnly kind has no calls
// while a writing kind has some (or outside writes).
func TestT6G3Kinds_KindCallsCoverEveryKind(t *testing.T) {
	var want, got []string
	for _, w := range t6g3Wants {
		want = append(want, w.headID)
		kw := t6g3KindCalls[w.headID]
		assert.Equal(t, w.readOnly, len(kw.calls) == 0 && len(kw.outside) == 0, "%s: ReadOnly iff it has no calls and no outside writes", w.headID)
	}
	for id := range t6g3KindCalls {
		got = append(got, id)
	}
	slices.Sort(want)
	slices.Sort(got)
	assert.Equal(t, want, got, "t6g3KindCalls has one entry per kind of t6g3Wants")
}

// Every Effects resource of the group-3 kinds (writes, reads and deletes)
// belongs to module "books": t6g3Res is the only constructor, and nothing in the
// kind tests read the module it files a resource under.
func TestT6G3Kinds_EffectsResourcesAreBooksModule(t *testing.T) {
	defs := (&Plugin{}).t6g3Kinds(fakeT6G3Deps(t), nil)
	require.Len(t, defs, len(t6g3Wants))
	for _, d := range defs {
		e := tasks.CommonOf(d).Effects
		if !e.IsReadOnly() {
			require.NotEmpty(t, e.WriteSet(), "%s: a writer declares a write set", d.Kind())
		}
		inBooks := func(module, kind string) {
			assert.Equal(t, "books", module, "%s: resource %q is filed under module %q", d.Kind(), kind, module)
		}
		for _, r := range e.WriteSet() {
			inBooks(r.Module, r.Kind)
		}
		for _, r := range e.ReadSet() {
			inBooks(r.Module, r.Kind)
		}
		for _, r := range e.DeleteSet() {
			inBooks(r.Module, r.Kind)
		}
	}
}

// TestT6G3_LoopParallelism pins rule R4 for each in-body loop, as literals.
func TestT6G3_LoopParallelism(t *testing.T) {
	assert.Equal(t, "cpu", creditCensusParallelism().String(), "HEAD: runtime.NumCPU()")
	assert.Equal(t, "cpu", creditsBackfillParallelism().String(), "HEAD: runtime.NumCPU()")
	// The cover ops' pools are sized by the core (workers param, else twice the
	// cores, clamped 1-32), not by a def-level constant.
	assert.Equal(t, min(max(runtime.NumCPU()*2, 1), 32), t6g3CoverOpDefaultWorkers(0))
	assert.Equal(t, 3, t6g3CoverOpDefaultWorkers(3))
	assert.Equal(t, 32, t6g3CoverOpDefaultWorkers(500))
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
			wantCalls: []string{"CleanupStaleTempFiles"},
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
			for _, call := range f.writer.called() {
				method, _, _ := strings.Cut(call, ":")
				assert.Contains(t, t6g3KindCalls[tc.headID].calls, method, "%s called %s, which t6g3KindCalls does not list for the kind", tc.headID, method)
			}
			f.writer.assertWithin(t, d)
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
	assert.NotContains(t, calls, "SaveClassLists", "the class lists are the run's own rows, not a writer call (F5-18d, O10)")
	assert.Same(t, gate, f.handed()[0])
	f.writer.assertWithin(t, d)
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

// Every native shell that writes refuses a mode key (decision 3), under every
// spelling and case, in Preview and in Live: one row per kind per key per value.
// The rule is that the KEY's presence is refused, so a falsy value (false) is
// refused exactly like true. The refusal comes before the live-only check, the
// store and the writer, so nothing is written and the error is the mode-key one
// in both modes.
func TestT6G3_ModeKeysAreRefused(t *testing.T) {
	kinds := map[string]string{
		"maintenance.purge-deleted":               `{%q:true}`,
		"maintenance.tombstone-cleanup":           `{%q:true}`,
		"maintenance.temp-file-cleanup":           `{%q:true}`,
		"maintenance.transcode-temp-cleanup":      `{%q:true}`,
		"maintenance.cleanup-activity-log":        `{%q:true}`,
		"maintenance.cleanup-old-backups":         `{%q:true}`,
		"maintenance.trash-cleanup":               `{%q:true}`,
		"maintenance.clear-apply-rename-failures": `{%q:true}`,
		"maintenance.compact-activity-log":        `{"older_than_days":3,%q:true}`,
		"maintenance.folder-cover-backfill":       `{%q:true}`,
		"maintenance.cover-text-read":             `{%q:true}`,
		"maintenance.credits-backfill":            `{%q:true}`,
	}
	keys := []string{"dry_run", "DRY_RUN", "dryRun", "DryRun", "apply", "APPLY", "Apply"}
	values := []string{"true", "false"}
	modes := map[string]tasks.Mode{"preview": tasks.ModePreview, "live": tasks.ModeLive}
	for id, tmpl := range kinds {
		for _, key := range keys {
			for _, val := range values {
				params := fmt.Sprintf(strings.Replace(tmpl, ":true}", ":"+val+"}", 1), key)
				for mname, mode := range modes {
					t.Run(id+"/"+key+"="+val+"/"+mname, func(t *testing.T) {
						f := newT6G3Fake(t)
						d := t6g3KindByName(t, (&Plugin{}).t6g3Kinds(f.deps(), nil), id)
						_, rc := newT6G3Host(d.Kind(), mode, &t6g3Gate{})
						_, err := t6g3Run(t, d, rc, params)
						require.Error(t, err, "%s: %s", id, params)
						assert.Contains(t, err.Error(), "mode comes from the start request")
						assert.Empty(t, f.writer.called())
					})
				}
			}
		}
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

// The native loop maps t6g3ItemsOpts onto tasks.ItemsOptions: Collect runs every
// item after one fails (the default stops at the first error) and ItemTimeout
// bounds each item's context.
func TestT6G3_NativeItemsCollectsErrorsAndBoundsEachItem(t *testing.T) {
	loop := func() t6g3ItemsFunc {
		_, rc := newT6G3Host(t6g3CreditCensusKind, tasks.ModeLive, nil)
		return t6g3NativeItems(rc, tasks.Fixed(1, "test: one worker, so item order is fixed"))
	}
	failFirst := func(ran *int) func(context.Context, int) error {
		return func(_ context.Context, i int) error {
			*ran++
			if i == 0 {
				return errors.New("item zero failed")
			}
			return nil
		}
	}
	t.Run("Collect runs the rest", func(t *testing.T) {
		var ran int
		err := loop()(3, t6g3ItemsOpts{Collect: true}, failFirst(&ran))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "item zero failed")
		assert.Equal(t, 3, ran, "every item ran")
	})
	t.Run("without Collect the first error stops the loop", func(t *testing.T) {
		var ran int
		err := loop()(3, t6g3ItemsOpts{}, failFirst(&ran))
		require.Error(t, err)
		assert.Less(t, ran, 3, "items after the failure did not run")
	})
	t.Run("ItemTimeout ends an item's context", func(t *testing.T) {
		var got error
		err := loop()(1, t6g3ItemsOpts{ItemTimeout: 20 * time.Millisecond}, func(ctx context.Context, _ int) error {
			select {
			case <-ctx.Done():
				got = ctx.Err()
			case <-time.After(5 * time.Second):
			}
			return nil
		})
		require.NoError(t, err)
		assert.ErrorIs(t, got, context.DeadlineExceeded, "the item saw its own deadline")
	})
}

// t6g3TombstoneStore answers the one write the tombstone cleanup makes.
type t6g3TombstoneStore struct {
	database.Store
	calls atomic.Int32
}

func (s *t6g3TombstoneStore) ResolveTombstoneChains() (int, error) {
	s.calls.Add(1)
	return 3, nil
}

// HEAD's tombstone cleanup resolves through the store the shell captured: a
// writer whose store was left nil would fail every nightly run with "database not
// initialized".
func TestT6G3_TombstoneCleanupHeadShellResolvesThroughTheCapturedStore(t *testing.T) {
	st := &t6g3TombstoneStore{}
	p := New(fakeDeps{store: st})
	rep := &fakeReporter{}
	require.NoError(t, p.tombstoneCleanupDef().Run(context.Background(), nil, rep))
	assert.EqualValues(t, 1, st.calls.Load(), "the write reached the store")
	rep.mu.Lock()
	defer rep.mu.Unlock()
	assert.Contains(t, rep.logs, "Resolved 3 tombstone chains")
}
