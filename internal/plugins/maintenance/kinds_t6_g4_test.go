// file: internal/plugins/maintenance/kinds_t6_g4_test.go
// version: 1.2.14
// guid: 952f6abf-08d2-41bd-aec8-95b8a3f39bd9
// last-edited: 2026-11-15

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"log/slog"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/ai"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/serverdecode"
	"github.com/falkcorp/audiobook-organizer/internal/transcribe"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
	"github.com/falkcorp/mediacore/rbac"
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
	zeroT6G4Store
	rec  *t6g4Calls
	root string

	bookFilesCore []database.BookFileCore
	identReport   database.IdentIndexReport
	// aiParsing and authors drive ai-dedup-batch: the core refuses a library
	// whose AI parsing is off, and builds the batch from the authors.
	aiParsing bool
	authors   []database.Author
}

func (s *fakeT6G4Store) HasAIParsing() bool { return s.aiParsing }

func (s *fakeT6G4Store) GetAllAuthors() ([]database.Author, error) {
	s.rec.add("GetAllAuthors")
	return s.authors, nil
}

// RootDir is a temp dir, so a core that writes a report writes it there.
func (s *fakeT6G4Store) RootDir() string { return s.root }

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
	recT6G4Writer
	// wrapped is the tasks.Writer NewWriter was handed.
	wrapped tasks.Writer
}

// DedupTriageApply answers a non-nil report, as the real service does.
func (w *fakeT6G4Writer) DedupTriageApply(context.Context) (*TriageReport, error) {
	w.rec.add("DedupTriageApply")
	return &TriageReport{}, nil
}

// SubmitAuthorDedupJob answers a job id derived from the source id, as a real
// submit's row would carry.
func (w *fakeT6G4Writer) SubmitAuthorDedupJob(_ context.Context, sourceID string, _ []ai.AuthorDiscoveryInput) (string, error) {
	w.rec.add("SubmitAuthorDedupJob")
	return "job-" + sourceID, nil
}

func (w *fakeT6G4Writer) RebuildIdentIndex(context.Context) (database.IdentRebuildStats, error) {
	w.rec.add("RebuildIdentIndex")
	return database.IdentRebuildStats{Books: 3}, nil
}

// zeroT6G4Store answers every read of t6g4Store with its zero value, so a core
// runs against an empty library without a nil-interface panic. recT6G4Writer
// records the name of every write a core makes. Both are generated from the two
// interfaces; a method added to either must be added here (the build fails).
type zeroT6G4Store struct{}

type recT6G4Writer struct{ rec *t6g4Calls }

func (zeroT6G4Store) BookFilesAtPath(a0 string) ([]database.BookFile, error) {
	var r0 []database.BookFile
	return r0, nil
}

func (zeroT6G4Store) CountAllBooks() (int, error) {
	var r0 int
	return r0, nil
}

func (zeroT6G4Store) DedupTriagePreview(a0 context.Context) (*TriageReport, error) {
	var r0 *TriageReport
	return r0, nil
}

func (zeroT6G4Store) DeferAggregatesAvailable() bool {
	var r0 bool
	return r0
}

func (zeroT6G4Store) ReportingRecomputeAvailable() bool {
	var r0 bool
	return r0
}

func (zeroT6G4Store) FileIntroWritesAvailable() bool {
	var r0 bool
	return r0
}

func (zeroT6G4Store) FileProvenanceAvailable() bool {
	var r0 bool
	return r0
}

func (zeroT6G4Store) FindFileEventsByHash(a0 string) ([]database.FileEvent, error) {
	var r0 []database.FileEvent
	return r0, nil
}

func (zeroT6G4Store) GetAllAuthors() ([]database.Author, error) {
	var r0 []database.Author
	return r0, nil
}

func (zeroT6G4Store) GetAllBookFilesCore() ([]database.BookFileCore, error) {
	var r0 []database.BookFileCore
	return r0, nil
}

func (zeroT6G4Store) GetAllBooksCore(a0 int, a1 int) ([]database.BookCore, error) {
	var r0 []database.BookCore
	return r0, nil
}

func (zeroT6G4Store) GetAllBooksFullFrom(a0 string, a1 int) ([]database.Book, error) {
	var r0 []database.Book
	return r0, nil
}

func (zeroT6G4Store) GetBookByID(a0 string) (*database.Book, error) {
	var r0 *database.Book
	return r0, nil
}

func (zeroT6G4Store) GetBookFileByID(a0 string, a1 string) (*database.BookFile, error) {
	var r0 *database.BookFile
	return r0, nil
}

func (zeroT6G4Store) GetBookFileByPath(a0 string) (*database.BookFile, error) {
	var r0 *database.BookFile
	return r0, nil
}

func (zeroT6G4Store) GetBookFiles(a0 string) ([]database.BookFile, error) {
	var r0 []database.BookFile
	return r0, nil
}

func (zeroT6G4Store) GetBooksByAuthorIDWithRoleCore(a0 int) ([]database.BookCore, error) {
	var r0 []database.BookCore
	return r0, nil
}

func (zeroT6G4Store) GetBooksByVersionGroup(a0 string) ([]database.Book, error) {
	var r0 []database.Book
	return r0, nil
}

func (zeroT6G4Store) GetChaptersForBook(a0 string) ([]database.Chapter, error) {
	var r0 []database.Chapter
	return r0, nil
}

func (zeroT6G4Store) GetExternalIDsForBook(a0 string) ([]database.ExternalIDMapping, error) {
	var r0 []database.ExternalIDMapping
	return r0, nil
}

func (zeroT6G4Store) GetFileHistory(a0 string) ([]database.FileEvent, error) {
	var r0 []database.FileEvent
	return r0, nil
}

func (zeroT6G4Store) GetFileProvExportCursor() (uint64, error) {
	var r0 uint64
	return r0, nil
}

func (zeroT6G4Store) GetMetadataFieldStates(a0 string) ([]database.MetadataFieldState, error) {
	var r0 []database.MetadataFieldState
	return r0, nil
}

func (zeroT6G4Store) GetUserPreference(a0 string) (*database.UserPreference, error) {
	var r0 *database.UserPreference
	return r0, nil
}

func (zeroT6G4Store) HasAIParsing() bool {
	var r0 bool
	return r0
}

func (zeroT6G4Store) HasDedupEngine() bool {
	var r0 bool
	return r0
}

func (zeroT6G4Store) HasOperationQueue() bool {
	var r0 bool
	return r0
}

func (zeroT6G4Store) ListAIJobs(a0 string, a1 string, a2 int, a3 int) ([]database.AIJob, error) {
	var r0 []database.AIJob
	return r0, nil
}

func (zeroT6G4Store) ListActiveOperationsV2() ([]database.OperationV2Row, error) {
	var r0 []database.OperationV2Row
	return r0, nil
}

func (zeroT6G4Store) ListBookIDs() ([]string, error) {
	var r0 []string
	return r0, nil
}

func (zeroT6G4Store) LiveBookIDsAtPath(a0 string) ([]string, error) {
	var r0 []string
	return r0, nil
}

func (zeroT6G4Store) MaxFileEventSeq() (uint64, error) {
	var r0 uint64
	return r0, nil
}

func (zeroT6G4Store) RootDir() string {
	var r0 string
	return r0
}

func (zeroT6G4Store) ScanFileEventsBySeq(a0 uint64, a1 int) ([]database.FileEventSeqRow, error) {
	var r0 []database.FileEventSeqRow
	return r0, nil
}

func (zeroT6G4Store) VerifyIdentIndex(a0 context.Context) (database.IdentIndexReport, error) {
	var r0 database.IdentIndexReport
	return r0, nil
}

func (w recT6G4Writer) AppendFileEvent(a0 database.FileEvent) error {
	w.rec.add("AppendFileEvent")
	return nil
}

func (w recT6G4Writer) CarryOverFingerprintWindows(a0 []database.FingerprintWindowRef, a1 database.FingerprintWindowRef) (int, error) {
	w.rec.add("CarryOverFingerprintWindows")
	var r0 int
	return r0, nil
}

func (w recT6G4Writer) ClearBookFileIntro(a0 string, a1 string, a2 string) (*database.BookFile, bool, error) {
	w.rec.add("ClearBookFileIntro")
	var r0 *database.BookFile
	var r1 bool
	return r0, r1, nil
}

func (w recT6G4Writer) ClearIntroTimeouts(a0 string, a1 string, a2 string) (bool, error) {
	w.rec.add("ClearIntroTimeouts")
	var r0 bool
	return r0, nil
}

func (w recT6G4Writer) CreateBookFile(a0 *database.BookFile) error {
	w.rec.add("CreateBookFile")
	return nil
}

func (w recT6G4Writer) CreateOperationChange(a0 *database.OperationChange) error {
	w.rec.add("CreateOperationChange")
	return nil
}

func (w recT6G4Writer) DedupTriageApply(a0 context.Context) (*TriageReport, error) {
	w.rec.add("DedupTriageApply")
	var r0 *TriageReport
	return r0, nil
}

func (w recT6G4Writer) DeleteBookFilesByIDs(a0 []database.BookFileRef) ([]database.BookFile, error) {
	w.rec.add("DeleteBookFilesByIDs")
	var r0 []database.BookFile
	return r0, nil
}

func (w recT6G4Writer) MarkOperationChangesReverted(a0 string, a1 []string) error {
	w.rec.add("MarkOperationChangesReverted")
	return nil
}

func (w recT6G4Writer) ModifyBook(a0 string, a1 func(*database.Book) error) (*database.Book, error) {
	w.rec.add("ModifyBook")
	var r0 *database.Book
	return r0, nil
}

func (w recT6G4Writer) ModifyBookFileDeferAggregates(a0 string, a1 string, a2 func(*database.BookFile) error) (*database.BookFile, error) {
	w.rec.add("ModifyBookFileDeferAggregates")
	var r0 *database.BookFile
	return r0, nil
}

func (w recT6G4Writer) ModifyBookFileReportingRecompute(a0 string, a1 string, a2 func(*database.BookFile) error) (*database.BookFile, error) {
	w.rec.add("ModifyBookFileReportingRecompute")
	var r0 *database.BookFile
	return r0, nil
}

func (w recT6G4Writer) MoveBookFilesToBookBulk(a0 []database.BookFileMove, a1 string) error {
	w.rec.add("MoveBookFilesToBookBulk")
	return nil
}

func (w recT6G4Writer) OpenWhisperJournal() (transcribe.ResultJournal, error) {
	w.rec.add("OpenWhisperJournal")
	var r0 transcribe.ResultJournal
	return r0, nil
}

func (w recT6G4Writer) ReassignExternalID(a0 string, a1 string, a2 string) error {
	w.rec.add("ReassignExternalID")
	return nil
}

func (w recT6G4Writer) RebuildIdentIndex(a0 context.Context) (database.IdentRebuildStats, error) {
	w.rec.add("RebuildIdentIndex")
	var r0 database.IdentRebuildStats
	return r0, nil
}

func (w recT6G4Writer) RecomputeBookAggregates(a0 string) error {
	w.rec.add("RecomputeBookAggregates")
	return nil
}

func (w recT6G4Writer) RecordIntroTimeout(a0 string, a1 string, a2 database.IntroTimeoutStrike) (database.IntroTimeoutOutcome, error) {
	w.rec.add("RecordIntroTimeout")
	var r0 database.IntroTimeoutOutcome
	return r0, nil
}

func (w recT6G4Writer) SetBookFileHash(a0 string, a1 string) error {
	w.rec.add("SetBookFileHash")
	return nil
}

func (w recT6G4Writer) SetBookFileIntro(a0 string, a1 string, a2 database.BookFileIntroResult) (database.BookFileIntroWrite, error) {
	w.rec.add("SetBookFileIntro")
	var r0 database.BookFileIntroWrite
	return r0, nil
}

func (w recT6G4Writer) SetFileProvExportCursor(a0 uint64) error {
	w.rec.add("SetFileProvExportCursor")
	return nil
}

func (w recT6G4Writer) SubmitAuthorDedupJob(a0 context.Context, a1 string, a2 []ai.AuthorDiscoveryInput) (string, error) {
	w.rec.add("SubmitAuthorDedupJob")
	var r0 string
	return r0, nil
}

func (w recT6G4Writer) UpdateBookFile(a0 string, a1 *database.BookFile) error {
	w.rec.add("UpdateBookFile")
	return nil
}

func (w recT6G4Writer) UpdateBookFiles(a0 context.Context, a1 []*database.BookFile, a2 func(i int, applied bool)) (int, error) {
	w.rec.add("UpdateBookFiles")
	var r0 int
	return r0, nil
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

// fakeT6G4Scan is a ScanController that grants every stand-down at once.
type fakeT6G4Scan struct{ acquires atomic.Int32 }

func (f *fakeT6G4Scan) AcquireScanStandDown(context.Context, string, string) (func(), error) {
	f.acquires.Add(1)
	return func() {}, nil
}
func (*fakeT6G4Scan) RenewScanStandDown(string) bool { return true }
func (*fakeT6G4Scan) ScanStandDownValid(string) bool { return true }

// fakeT6G4Deps is the helper the brief names: deps with a recording store and a
// NewWriter that records the tasks.Writer it was handed.
func fakeT6G4Deps(t *testing.T) (t6g4Deps, *t6g4Probe) {
	t.Helper()
	rec := &t6g4Calls{}
	pr := &t6g4Probe{
		store:  &fakeT6G4Store{rec: rec, root: t.TempDir()},
		writer: &fakeT6G4Writer{recT6G4Writer: recT6G4Writer{rec: rec}},
	}
	return t6g4Deps{
		Store: pr.store,
		Scan:  &fakeT6G4Scan{},
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
	notes    []string // Progress.Note: the run's status line, in order
	touches  int      // Progress.Tick: liveness stamps
}

// Note records the run's status line (FakeRun's is a no-op).
func (h *t6g4Host) Note(msg string) {
	h.mu.Lock()
	h.notes = append(h.notes, msg)
	h.mu.Unlock()
}

// Touch counts a liveness stamp (FakeRun's is a no-op).
func (h *t6g4Host) Touch() {
	h.mu.Lock()
	h.touches++
	h.mu.Unlock()
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

// The native base is the one place the engine's seams (status line, liveness,
// cancel) are wired to the env functions every core calls, and the reporter the
// helpers beneath a core take is built from those functions. A native base that
// dropped the status line, never stamped liveness, or answered "not canceled"
// whatever the host said would pass every other test here: the cores read these
// through the env and the fakes never look. The run's context is the HOST's, so
// the reporter's own context (a separate, live one) cannot be what answers
// IsCanceled.
func TestT6G4Base_ReporterBridgesTheHostSeams(t *testing.T) {
	hostCtx, cancelHost := context.WithCancel(context.Background())
	defer cancelHost()
	h := &t6g4Host{FakeRun: taskstest.NewFakeRun(hostCtx, durationBackfillKind), mode: tasks.ModeLive, w: &t6g4RunWriter{}}
	base, err := t6g4NativeBase(tasks.NewRC(h), t6g4Deps{Store: zeroT6G4Store{}}, false)
	if err != nil {
		t.Fatal(err)
	}
	rep := base.reporter(context.Background()) // live: never the source of IsCanceled here

	rep.SetCurrentItem("Batman: Hush")
	h.mu.Lock()
	notes := slices.Clone(h.notes)
	h.mu.Unlock()
	if !slices.Equal(notes, []string{"Batman: Hush"}) {
		t.Fatalf("SetCurrentItem reached the host's status line as %q, want exactly [Batman: Hush]", notes)
	}

	toucher, ok := rep.(interface{ TouchLiveness() })
	if !ok {
		t.Fatalf("%T has no TouchLiveness: registry.TouchLiveness would find nothing to call", rep)
	}
	h.mu.Lock()
	before := h.touches
	h.mu.Unlock()
	toucher.TouchLiveness()
	base.Touch()
	h.mu.Lock()
	after := h.touches
	h.mu.Unlock()
	if after-before != 2 {
		t.Fatalf("the reporter's TouchLiveness and the base's Touch stamped the host %d times, want 2", after-before)
	}

	if rep.IsCanceled() {
		t.Fatal("IsCanceled is true before anything was canceled")
	}
	cancelHost()
	if !rep.IsCanceled() {
		t.Fatal("IsCanceled is false after the host's run was canceled")
	}
	if !base.Canceled() {
		t.Fatal("the base's Canceled is false after the host's run was canceled")
	}
}

// t6g4RunEach is the native loop every group 4 core runs its items on. With
// Collect an item's error does not stop the loop: every item runs and the errors
// come back joined (HEAD's ErrModeCollect); without it the first error stops the
// dispatch (sequentially, the items after it never start).
func TestT6G4RunEach_CollectKeepsGoingAndReturnsTheError(t *testing.T) {
	boom := errors.New("the Riddler struck on item 1")
	run := func(collect bool) ([]int, error) {
		_, rc := newT6G4RC(durationBackfillKind, tasks.ModeLive, &t6g4RunWriter{})
		var ran []int // sequential: one item at a time, in order
		err := t6g4RunEach(rc, 3, t6g4EachOpts{Collect: collect}, func(_ context.Context, i int) error {
			ran = append(ran, i)
			if i == 1 {
				return boom
			}
			return nil
		})
		return ran, err
	}

	ran, err := run(true)
	if !slices.Equal(ran, []int{0, 1, 2}) {
		t.Fatalf("Collect ran items %v, want every item [0 1 2]", ran)
	}
	if !errors.Is(err, boom) {
		t.Fatalf("Collect returned %v, want it to carry item 1's error", err)
	}

	ran, err = run(false)
	if !slices.Equal(ran, []int{0, 1}) {
		t.Fatalf("without Collect ran items %v, want the loop stopped after item 1 [0 1]", ran)
	}
	if !errors.Is(err, boom) {
		t.Fatalf("without Collect returned %v, want item 1's error", err)
	}
}

// ProgressTotal is the absolute denominator and ProgressBase the offset of item 0
// when the n items are the tail of a larger set: 3 items from base 4 of 10 report
// 5/10, 6/10, 7/10, not 1/3 or 5/7.
func TestT6G4RunEach_ProgressTotalAndBaseReachTheProgress(t *testing.T) {
	h, rc := newT6G4RC(durationBackfillKind, tasks.ModeLive, &t6g4RunWriter{})
	err := t6g4RunEach(rc, 3, t6g4EachOpts{ProgressTotal: 10, ProgressBase: 4},
		func(context.Context, int) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	want := []taskstest.ProgressEvent{{Done: 5, Total: 10}, {Done: 6, Total: 10}, {Done: 7, Total: 10}}
	got := h.FakeRun.Progress()
	for i := range got {
		got[i].Current = "" // the label is not under test
	}
	if !slices.Equal(got, want) {
		t.Fatalf("progress %+v, want %+v", got, want)
	}
}

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
	defs := t6g4KindsUnchecked(deps, nil)
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
	// headCheckpoints: HEAD's shell saves a checkpoint AND reads it on restart.
	// Only then does HEAD's ResumeRestart map to ResumeContinue; a Restart that
	// re-reads nothing is a run from zero (the arch guard's
	// restartWithoutCheckpoint list names those shells).
	headCheckpoints bool
	// yieldsNatively: HEAD checkpoints nothing, but the native shell checkpoints a
	// cursor and yields before its deadline (review M4), so its Resume is Continue
	// by that ruling and not by rule R5's mapping. Exactly one kind does this.
	yieldsNatively bool
	// writes is the native write set, spelled with the pkg/ops constants (and
	// checked against pkg/ops/effects.go by TestT6G4Kinds_EveryDeclaredResourceIsAPkgOpsConstant);
	// nil with readOnly true means "Declared ReadOnly".
	writes []ops.Resource
	// calls are the writer methods the kind's core can call and outside the
	// resources it writes without a writer call; writes is DERIVED from them by
	// t6g4DeriveWrites (the sorted union of the shared table's rows for calls plus
	// outside), never typed, so a row that widens widens the expectation.
	calls    []string
	outside  []ops.Resource
	readOnly bool
	// liveOnly: HEAD runs this only in an explicit live mode (rule R7).
	liveOnly bool
	// notWhile is rule R5's DependsOn mapping, derived from HEAD's DependsOn in
	// t6g4WantNotWhile; this field only records which rows have one.
	hasDependsOn bool
	// timeout overrides the HEAD-derived Timeout where HEAD declared none and
	// the native kind states one explicitly (review MINOR 13).
	timeout time.Duration
	// noLive skips the Live scenario: the core reaches the network or an external
	// tool before it can make a recorded write.
	noLive bool
	// helpReworded: HEAD's Description names a mode param (dry_run, dryRun,
	// apply), which the native shell refuses, so the native Help is reworded.
	// The test derives this from HEAD's text and requires the flag to agree.
	helpReworded bool
}

// t6g4DeriveWrites sets every row's writes to the union of the shared table's
// rows for its calls plus its outside resources (nil for a row with neither).
func t6g4DeriveWrites(rows []t6g4Want) []t6g4Want {
	for i := range rows {
		if len(rows[i].calls) > 0 || len(rows[i].outside) > 0 {
			rows[i].writes = t6RowsUnion(t6RowsOf(rows[i].calls...), rows[i].outside)
		}
	}
	return rows
}

// t6g4Expected is in file order (the order of t6g4Kinds). Write sets use only the
// resource names pkg/ops defines; a store with no such name is not declared (see
// the PR's Review decisions).
var t6g4Expected = t6g4DeriveWrites([]t6g4Want{
	{kind: aiDedupBatchKind, head: func(p *Plugin) sdk.OperationDef { return p.aiDedupBatchDef() },
		resume: tasks.ResumeContinue, headCheckpoints: true, calls: []string{"SubmitAuthorDedupJob"}, liveOnly: true},
	{kind: dedupExactTriageKind, head: func(p *Plugin) sdk.OperationDef { return p.dedupExactTriageDef() },
		resume: tasks.ResumeDrop, calls: []string{"DedupTriageApply"}, helpReworded: true},
	{kind: dedupeBookFileRowsKind, head: func(p *Plugin) sdk.OperationDef { return p.dedupeBookFileRowsDef() },
		resume: tasks.ResumeFromZero, calls: []string{"CarryOverFingerprintWindows", "CreateOperationChange", "DeleteBookFilesByIDs", "RecomputeBookAggregates", "UpdateBookFile"}, outside: []ops.Resource{ops.ResFiles}, hasDependsOn: true, helpReworded: true},
	{kind: durationBackfillKind, head: func(p *Plugin) sdk.OperationDef { return p.durationBackfillDef() },
		resume: tasks.ResumeDrop, calls: []string{"ModifyBook", "ModifyBookFileDeferAggregates", "ModifyBookFileReportingRecompute", "RecomputeBookAggregates", "UpdateBookFiles"}, helpReworded: true},
	{kind: extractWAVClipsKind, head: func(p *Plugin) sdk.OperationDef { return p.extractWAVClipsDef() },
		resume: tasks.ResumeFromZero, calls: []string{"SetBookFileHash"}, outside: []ops.Resource{ops.ResFiles}, liveOnly: true},
	{kind: fileIntroKind, head: func(p *Plugin) sdk.OperationDef { return p.fileIntroTranscribeDef() },
		resume: tasks.ResumeContinue, yieldsNatively: true, calls: []string{"SetBookFileIntro", "ClearBookFileIntro", "RecordIntroTimeout", "ClearIntroTimeouts", "ModifyBook"}, helpReworded: true},
	{kind: fileProvenanceCaptureKind, head: func(p *Plugin) sdk.OperationDef { return p.fileProvenanceCaptureDef() },
		resume: tasks.ResumeDrop, calls: []string{"AppendFileEvent"}, timeout: 120 * time.Minute, helpReworded: true},
	{kind: fileProvenanceExportKind, head: func(p *Plugin) sdk.OperationDef { return p.fileProvenanceExportDef() },
		resume: tasks.ResumeDrop, calls: []string{"SetFileProvExportCursor"}, outside: []ops.Resource{ops.ResFiles}, timeout: 120 * time.Minute, helpReworded: true},
	{kind: filePathCollisionReportKind, head: func(p *Plugin) sdk.OperationDef { return p.filePathCollisionReportDef() },
		resume: tasks.ResumeDrop, readOnly: true},
	{kind: fsRegroupKind, head: func(p *Plugin) sdk.OperationDef { return p.fsRegroupXMLDef() },
		resume: tasks.ResumeDrop, calls: []string{"CreateBookFile", "MarkOperationChangesReverted", "ModifyBook", "MoveBookFilesToBookBulk", "ReassignExternalID", "RecomputeBookAggregates", "UpdateBookFiles"}, helpReworded: true},
	{kind: identIndexKind, head: func(p *Plugin) sdk.OperationDef { return p.identIndexRebuildDef() },
		resume: tasks.ResumeDrop, calls: []string{"RebuildIdentIndex"}, outside: []ops.Resource{ops.ResBooks}, helpReworded: true},
	{kind: integrityCheckKind, head: func(p *Plugin) sdk.OperationDef { return p.integrityCheckDef() },
		resume: tasks.ResumeDrop, readOnly: true},
	{kind: introMigrateSingleFileKind, head: func(p *Plugin) sdk.OperationDef { return p.introMigrateSingleFileDef() },
		resume: tasks.ResumeFromZero, calls: []string{"UpdateBookFile"}, helpReworded: true},
	{kind: introTranscribeKind, head: func(p *Plugin) sdk.OperationDef { return p.introTranscribeDef() },
		resume: tasks.ResumeFromZero, calls: []string{"ClearIntroTimeouts", "ModifyBook", "OpenWhisperJournal", "RecordIntroTimeout"}, outside: []ops.Resource{ops.ResFiles}, noLive: true, helpReworded: true},
})

// t6g4PkgOpsResources is pkg/ops's resource vocabulary: the string value of every
// constant of type Resource declared in pkg/ops/effects.go, read from the source
// with go/ast so it cannot drift from a hand-kept copy. (A coined name still
// compiles as a bare string, so the vocabulary is checked by value, not by the
// compiler.)
func t6g4PkgOpsResources(t *testing.T) []ops.Resource {
	t.Helper()
	// go test runs in the package directory, and -trimpath only strips file
	// names from the binary, not the working directory.
	path := filepath.Join("..", "..", "..", "pkg", "ops", "effects.go")
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	var out []ops.Resource
	for _, d := range f.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, sp := range gd.Specs {
			vs := sp.(*ast.ValueSpec)
			if id, ok := vs.Type.(*ast.Ident); !ok || id.Name != "Resource" {
				continue
			}
			for _, v := range vs.Values {
				lit, ok := v.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					t.Fatalf("a Resource constant in %s is not a string literal: %T", path, v)
				}
				s, err := strconv.Unquote(lit.Value)
				if err != nil {
					t.Fatal(err)
				}
				out = append(out, ops.Resource(s))
			}
		}
	}
	if len(out) < 17 || !slices.Contains(out, ops.ResBooks) || !slices.Contains(out, ops.ResFiles) {
		t.Fatalf("read %d Resource constants from %s (%v); the parse is wrong", len(out), path, out)
	}
	return out
}

// TestT6G4Kinds_EveryDeclaredResourceIsAPkgOpsConstant: every resource name any
// group-4 kind declares (writes, reads or deletes), every name in the spy map and
// every name in the expected table is the value of a constant in pkg/ops. The
// vocabulary is closed: a new name is a decision for the owner of pkg/ops, not
// something a kind coins.
func TestT6G4Kinds_EveryDeclaredResourceIsAPkgOpsConstant(t *testing.T) {
	vocab := t6g4PkgOpsResources(t)
	deps, _ := fakeT6G4Deps(t)
	for _, d := range t6g4KindsUnchecked(deps, nil) {
		e := tasks.CommonOf(d).Effects
		var kinds []string
		for _, r := range e.WriteSet() {
			kinds = append(kinds, r.Kind)
		}
		for _, r := range e.ReadSet() {
			kinds = append(kinds, r.Kind)
		}
		for _, r := range e.DeleteSet() {
			kinds = append(kinds, r.Kind)
		}
		for _, k := range kinds {
			if !slices.Contains(vocab, ops.Resource(k)) {
				t.Errorf("%s declares %q, which is not a pkg/ops Resource constant", d.Kind(), k)
			}
		}
	}
	for m, rs := range t6g4WriterResource {
		for _, r := range rs {
			if !slices.Contains(vocab, r) {
				t.Errorf("the spy map sends %s to %q, which is not a pkg/ops Resource constant", m, r)
			}
		}
	}
	for _, w := range t6g4Expected {
		for _, r := range w.writes {
			if !slices.Contains(vocab, r) {
				t.Errorf("the expected table gives %s the write %q, which is not a pkg/ops Resource constant", w.kind, r)
			}
		}
	}
}

// t6g4HeadResume is rule R5's mapping of HEAD's ResumePolicy: Restart is
// Continue only when HEAD's shell reads the checkpoint it saved (else a run from
// zero), Requeue is FromZero, Drop and Ask map to themselves.
func t6g4HeadResume(p sdk.ResumePolicy, headCheckpoints bool) tasks.ResumePolicy {
	switch p {
	case sdk.ResumeRestart:
		if headCheckpoints {
			return tasks.ResumeContinue
		}
		return tasks.ResumeFromZero
	case sdk.ResumeRequeue:
		return tasks.ResumeFromZero
	case sdk.ResumeAsk:
		return tasks.ResumeAsk
	default:
		return tasks.ResumeDrop
	}
}

// t6g4WantNotWhile derives rule R5's DependsOn mapping from HEAD's definition:
// R3 renames each plugin-local id to "books."+id, and 05-tasks rename rule 4
// adds the scan-full companion to a library.scan dependency (the full scan holds
// the same stand-down; F5-13).
func t6g4WantNotWhile(head sdk.OperationDef) []tasks.Kind {
	var out []tasks.Kind
	for _, id := range head.DependsOn {
		out = append(out, tasks.Kind("books."+id))
		if id == "library.scan" {
			out = append(out, "books.library.scan-full")
		}
	}
	return out
}

// t6g4NamesModeKey reports whether a Help text tells the operator to pass a mode
// param the native shell refuses.
func t6g4NamesModeKey(help string) bool {
	h := strings.ToLower(help)
	for _, tok := range []string{"dry_run", "dryrun", "dry-run", "apply=", "\"apply\"", "apply param", "param apply", "apply:"} {
		if strings.Contains(h, tok) {
			return true
		}
	}
	return false
}

// TestT6G4Kinds_MatchHEADDefs: each native kind equals "books." + HEAD's ID, and
// its Title, Resume, Priority, Timeout, key, NotWhile, readiness, effects class
// and Start permission are rule R5's, R6's and R7's mapping of the HEAD definition
// built in the same test, so a drift in either fails.
func TestT6G4Kinds_MatchHEADDefs(t *testing.T) {
	deps, _ := fakeT6G4Deps(t)
	p := &Plugin{}
	defs := t6g4KindsUnchecked(deps, nil)
	t.Run("schedule-hook-and-parallelism", t6g4CheckScheduleHookAndParallelism)
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
			// HEAD's Description tells the operator to pass a mode param the native
			// shell refuses; the test derives which rows that is from HEAD's own
			// text, so the flag cannot drift from it. A native Help never names one.
			if got := t6g4NamesModeKey(head.Description); got != w.helpReworded {
				t.Errorf("HEAD Description names a mode param = %v, table helpReworded = %v", got, w.helpReworded)
			}
			if t6g4NamesModeKey(c.Help) {
				t.Errorf("native Help still names a mode param: %q", c.Help)
			}
			if w.helpReworded {
				if c.Help == "" || c.Help == head.Description {
					t.Errorf("Help must be set and reworded, got %q", c.Help)
				}
			} else if c.Help != head.Description {
				t.Errorf("Help differs from HEAD's Description")
			}
			if wantResume := t6g4HeadResume(head.ResumePolicy, w.headCheckpoints || w.yieldsNatively); c.Resume != w.resume || c.Resume != wantResume {
				t.Errorf("Resume = %v, table %v, mapping of HEAD's %v (checkpoints=%v) is %v", c.Resume, w.resume, head.ResumePolicy, w.headCheckpoints, wantResume)
			}
			wantTimeout := min(head.Timeout, tasks.MaxTimeout)
			if w.timeout != 0 {
				if head.Timeout != 0 {
					t.Errorf("table gives an explicit timeout %v but HEAD declares %v", w.timeout, head.Timeout)
				}
				wantTimeout = w.timeout
			}
			if c.Timeout != wantTimeout || c.ProgressTimeout != head.ProgressTimeout {
				t.Errorf("Timeout/ProgressTimeout = %v/%v, want %v/%v", c.Timeout, c.ProgressTimeout, wantTimeout, head.ProgressTimeout)
			}
			if wantKey := tasks.Key("books." + head.ConcurrencyKey); head.ConcurrencyKey != "" && c.ConcurrencyKey != wantKey {
				t.Errorf("ConcurrencyKey = %q, want %q", c.ConcurrencyKey, wantKey)
			}
			wantPrio := map[sdk.Priority]tasks.Priority{sdk.PriorityLow: tasks.PriorityLow, sdk.PriorityNormal: tasks.PriorityNormal, sdk.PriorityHigh: tasks.PriorityHigh}[head.DefaultPriority]
			if c.Priority != wantPrio {
				t.Errorf("Priority = %v, want %v", c.Priority, wantPrio)
			}
			if want := t6g4WantNotWhile(head); !slices.Equal(c.NotWhile, want) || (len(want) > 0) != w.hasDependsOn {
				t.Errorf("NotWhile = %v, want %v derived from HEAD's DependsOn %v", c.NotWhile, want, head.DependsOn)
			}
			if !slices.Equal(c.Ready, []tasks.ReadySignal{t6g4SignalMemDB}) {
				t.Errorf("Ready = %v, want [%s]", c.Ready, t6g4SignalMemDB)
			}
			if c.Perms.Start.IsZero() {
				t.Errorf("Perms.Start is not set")
			}
			if c.Schedule != nil {
				t.Errorf("Schedule = %v with a nil hook, want none", c.Schedule)
			}
			want := make([]string, len(w.writes))
			for i, r := range w.writes {
				want[i] = string(r)
			}
			slices.Sort(want)
			if got := t6g4WriteKinds(c.Effects); w.readOnly != c.Effects.IsReadOnly() || !slices.Equal(got, want) {
				t.Errorf("Effects: readOnly=%v writes=%v, want readOnly=%v writes=%v", c.Effects.IsReadOnly(), got, w.readOnly, want)
			}
			// Every declared name (reads included) is a pkg/ops resource constant:
			// the vocabulary is closed, a new name is a Review decision, not a coin.
			for _, r := range append(c.Effects.WriteSet(), c.Effects.ReadSet()...) {
				if !slices.Contains(t6g4PkgOpsResources(t), ops.Resource(r.Kind)) {
					t.Errorf("Effects name %q, which is not a pkg/ops resource constant", r.Kind)
				}
			}
			if head.ID != string(w.kind)[len("books."):] {
				t.Errorf("table kind %s is not books.<HEAD ID %s>", w.kind, head.ID)
			}
		})
	}
}

// t6g4CheckScheduleHookAndParallelism: the schedule hook reaches each kind
// (rule R9), and t6g4Par maps HEAD's RunItems concurrency as rule R4 says. It
// runs as a subtest of TestT6G4Kinds_MatchHEADDefs.
func t6g4CheckScheduleHookAndParallelism(t *testing.T) {
	deps, _ := fakeT6G4Deps(t)
	by := map[tasks.Kind]*tasks.Schedule{}
	hook := func(k tasks.Kind) *tasks.Schedule {
		s := tasks.Every(time.Duration(len(by)+1) * time.Hour)
		by[k] = s
		return s
	}
	for _, d := range t6g4KindsUnchecked(deps, hook) {
		if got := tasks.CommonOf(d).Schedule; got == nil || !reflect.DeepEqual(got, by[d.Kind()]) {
			t.Errorf("%s: Schedule = %v, want the hook's value for that kind", d.Kind(), got)
		}
	}
	if len(by) != len(t6g4Expected) {
		t.Errorf("hook called for %d kinds, want %d", len(by), len(t6g4Expected))
	}
	for _, c := range []struct {
		conc int
		want tasks.Parallelism
	}{
		{0, tasks.Sequential("r")}, {1, tasks.Sequential("r")}, {2, tasks.Fixed(2, "r")}, {6, tasks.Fixed(6, "r")}, {t6g4CPU, tasks.CPU()},
	} {
		if got := t6g4Par(t6g4EachOpts{Concurrency: c.conc}, "r"); got.String() != c.want.String() {
			t.Errorf("t6g4Par(%d) = %s, want %s", c.conc, got, c.want)
		}
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

// t6g4RunSafely runs a kind and turns a core's panic on an empty fixture into an
// error, so one kind's unmet fixture cannot hide the others' results.
func t6g4RunSafely(t *testing.T, d tasks.Definition, rc *tasks.RC, params string) (err error) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	_, err = runT6G4Kind(t, d, rc, params)
	return err
}

// TestT6G4Kinds_WritesThroughShell: for every kind whose Effects declare writes,
// one Live run shows NewWriter called once with that run's rc.Writer() (rule R10);
// a Preview run of a kind never builds a write that reaches the writer, and a
// Live-only kind refuses a Preview run by name. The mode-key refusal has its own
// test, TestT6G4Kinds_RefuseModeKeys.
func TestT6G4Kinds_WritesThroughShell(t *testing.T) {
	for _, w := range t6g4Expected {
		t.Run(string(w.kind), func(t *testing.T) {
			if w.readOnly {
				return
			}
			if w.liveOnly {
				deps, pr := fakeT6G4Deps(t)
				def := t6g4KindByName(t, t6g4KindsUnchecked(deps, nil), w.kind)
				_, rc := newT6G4RC(w.kind, tasks.ModePreview, &t6g4RunWriter{})
				err := t6g4RunSafely(t, def, rc, "")
				if err == nil || !strings.Contains(err.Error(), "preview not supported") {
					t.Errorf("Preview run: err = %v, want \"preview not supported\"", err)
				}
				if pr.newWriterCalls != 0 || len(pr.store.rec.all()) != 0 {
					t.Errorf("a refused Preview run built a writer or wrote: %v", pr.store.rec.all())
				}
			} else {
				deps, pr := fakeT6G4Deps(t)
				def := t6g4KindByName(t, t6g4KindsUnchecked(deps, nil), w.kind)
				_, rc := newT6G4RC(w.kind, tasks.ModePreview, &t6g4RunWriter{})
				_ = t6g4RunSafely(t, def, rc, "")
				for _, c := range pr.store.rec.all() {
					if t6g4WriterCalls(c) {
						t.Errorf("a Preview run made the write %s", c)
					}
				}
			}
			if w.noLive {
				return
			}
			if scenario, ok := t6g4WriteScenarios[w.kind]; ok {
				scenario(t)
				return
			}
			// extract-wav-clips checks the host's decode opt-in before it builds its
			// writer; pin it on so the run does not depend on the host's environment.
			t.Setenv(serverdecode.EnvVar, "true")
			deps, pr := fakeT6G4Deps(t)
			def := t6g4KindByName(t, t6g4KindsUnchecked(deps, nil), w.kind)
			run := &t6g4RunWriter{name: "run"}
			_, rc := newT6G4RC(w.kind, tasks.ModeLive, run)
			_ = t6g4RunSafely(t, def, rc, "")
			if pr.newWriterCalls != 1 || pr.handed != tasks.Writer(run) {
				t.Errorf("NewWriter called %d time(s) with %v, want once with this run's rc.Writer()", pr.newWriterCalls, pr.handed)
			}
			t6g4CheckRecordedWritesDeclared(t, def, pr)
		})
	}
}

// TestT6G4Kinds_RefuseModeKeys: every native WRITING kind refuses each of the
// three mode keys HEAD's params carried (dry_run, dryRun, apply), in any letter
// case and with any value, null included, through the real decode path and
// before it builds a writer. The mode of a native run is the start request's,
// so a param that asks for another would be a silent override.
func TestT6G4Kinds_RefuseModeKeys(t *testing.T) {
	keys := [][]string{
		{"dry_run", "DRY_RUN", "Dry_Run"},
		{"dryRun", "DRYRUN", "dryrun"},
		{"apply", "Apply", "APPLY"},
	}
	refused := 0
	for _, w := range t6g4Expected {
		if w.readOnly {
			continue
		}
		t.Run(string(w.kind), func(t *testing.T) {
			for _, spellings := range keys {
				for i, key := range spellings {
					values := []string{"true"}
					if i == 0 {
						values = []string{"true", "false", "null"}
					}
					for _, v := range values {
						body := fmt.Sprintf(`{%q:%s}`, key, v)
						deps, pr := fakeT6G4Deps(t)
						def := t6g4KindByName(t, t6g4KindsUnchecked(deps, nil), w.kind)
						_, rc := newT6G4RC(w.kind, tasks.ModeLive, &t6g4RunWriter{})
						err := t6g4RunSafely(t, def, rc, body)
						if err == nil || !strings.Contains(err.Error(), "the "+spellings[0]+" param is not accepted") {
							t.Errorf("params %s: err = %v, want a refusal naming the key", body, err)
						}
						if pr.newWriterCalls != 0 || len(pr.store.rec.all()) != 0 {
							t.Errorf("params %s: a refused run built a writer or touched the store: %v", body, pr.store.rec.all())
						}
						refused++
					}
				}
			}
		})
	}
	// 12 writing kinds x (3 values for the first spelling + 1 each for the other
	// two) x 3 keys: a kind dropping out of the table would shrink this.
	if want := 12 * 3 * (3 + 1 + 1); refused != want {
		t.Errorf("exercised %d refusals, want %d (12 writing kinds x 3 keys x 5 spellings/values)", refused, want)
	}
}

// t6g4WriterResource is the spy's method-to-resource map: for every method of
// t6g4Writer, the pkg/ops resources a call to it writes. It is this group's view
// of t6WriterResources (kinds_resources.go), the one table of every converted
// group, so a method that reaches two resources carries both: every book_file
// method that notifies the aggregate recompute (SetBookFileHash, UpdateBookFile,
// UpdateBookFiles, CreateBookFile, MoveBookFilesToBookBulk, DeleteBookFilesByIDs)
// writes the books row too, and ModifyBook carries the metadata_cache stale stamp
// and the ident: rows (`indexes`). This map held one resource per method until the
// stack's parity review (P1); its shape could not say so. The two stats keys are
// not here at all: they are written outside the gated writer (t6g4StatsSink). The
// map is the oracle of TestT6G4Kinds_EffectsCoverRecordedWrites, so it is compared
// against the real interface by reflection and against the vocabulary by name.
var t6g4WriterResource = t6WriterView(nil, reflect.TypeFor[t6g4Writer]())

// t6g4WriterCalls tells a write from a store read in the shared call record.
func t6g4WriterCalls(name string) bool { _, ok := t6g4WriterResource[name]; return ok }

// TestT6G4Kinds_WriterSpyMapCoversInterface: every method of t6g4Writer has a
// spy mapping, so a writer method added without one fails here rather than
// slipping past the recorded-writes check; every mapped name is a real method
// and a pkg/ops resource.
func TestT6G4Kinds_WriterSpyMapCoversInterface(t *testing.T) {
	iface := reflect.TypeOf((*t6g4Writer)(nil)).Elem()
	seen := map[string]bool{}
	for i := 0; i < iface.NumMethod(); i++ {
		name := iface.Method(i).Name
		seen[name] = true
		if _, ok := t6g4WriterResource[name]; !ok {
			t.Errorf("t6g4Writer.%s has no entry in t6g4WriterResource", name)
		}
	}
	for name, rs := range t6g4WriterResource {
		if !seen[name] {
			t.Errorf("t6g4WriterResource names %s, which is not a t6g4Writer method", name)
		}
		if rs == nil {
			t.Errorf("%s has a nil row: an empty row is an explicit empty list", name)
		}
		for _, res := range rs {
			if !slices.Contains(t6g4PkgOpsResources(t), res) {
				t.Errorf("%s maps to %q, not a pkg/ops resource", name, res)
			}
		}
	}
}

// t6g4CheckRecordedWritesDeclared asserts the resources of the writer calls a
// run RECORDED are a subset of the kind's declared WriteSet: the declaration
// is checked against what the code did, not against a second typed table.
func t6g4CheckRecordedWritesDeclared(t *testing.T, def tasks.Definition, pr *t6g4Probe) {
	t.Helper()
	declared := map[string]bool{}
	for _, r := range tasks.CommonOf(def).Effects.WriteSet() {
		declared[r.Kind] = true
	}
	for _, r := range t6g4RecordedResources(t, pr.store.rec.all()) {
		if !declared[r] {
			t.Errorf("%s recorded a write to %q, which its Effects do not declare (declared %v)", def.Kind(), r, t6g4WriteKinds(tasks.CommonOf(def).Effects))
		}
	}
}

// TestT6G4FSRegroup_LiveApplyNeedsScanStandDown (review M11): a live fs-regroup
// apply with no scan stand-down service refuses by name and writes nothing; a
// preview needs none and runs; with the service present the live run is not
// refused for its absence.
func TestT6G4FSRegroup_LiveApplyNeedsScanStandDown(t *testing.T) {
	run := func(t *testing.T, mode tasks.Mode, withScan bool) (*t6g4Probe, error) {
		deps, pr := fakeT6G4Deps(t)
		if !withScan {
			deps.Scan = nil
		}
		def := t6g4KindByName(t, t6g4KindsUnchecked(deps, nil), fsRegroupKind)
		_, rc := newT6G4RC(fsRegroupKind, mode, &t6g4RunWriter{})
		return pr, t6g4RunSafely(t, def, rc, "")
	}
	pr, err := run(t, tasks.ModeLive, false)
	if err == nil || !strings.Contains(err.Error(), "Scan is not set") {
		t.Errorf("live apply with no Scan: err = %v, want a refusal naming t6g4Deps.Scan", err)
	}
	if got := t6g4RecordedResources(t, pr.store.rec.all()); len(got) != 0 {
		t.Errorf("the refused live apply wrote %v", got)
	}
	if _, err := run(t, tasks.ModePreview, false); err != nil && strings.Contains(err.Error(), "Scan is not set") {
		t.Errorf("a preview must not need the scan stand-down, got %v", err)
	}
	if _, err := run(t, tasks.ModeLive, true); err != nil && strings.Contains(err.Error(), "Scan is not set") {
		t.Errorf("a live apply with Scan set was refused for its absence: %v", err)
	}
}

// t6g4RecordedResources maps the writer calls a run recorded to the resources
// they wrote. A name the spy map does not know is skipped, as a store read (the
// shared call record also holds those); it does NOT fail here. That a writer
// method has a map entry is pinned by TestT6G4Kinds_WriterSpyMapCoversInterface,
// which compares the map with t6g4Writer's methods by reflection. A method with
// an empty row is a method that writes nothing itself.
func t6g4RecordedResources(t *testing.T, calls []string) []string {
	t.Helper()
	set := map[string]bool{}
	for _, c := range calls {
		rs, ok := t6g4WriterResource[c]
		if !ok {
			continue // a store read, not a writer call
		}
		for _, res := range rs {
			set[string(res)] = true
		}
	}
	out := make([]string, 0, len(set))
	for r := range set {
		out = append(out, r)
	}
	slices.Sort(out)
	return out
}

// t6g4WriteScenarios holds a stricter Live-run scenario for a kind, replacing the
// generic NewWriter-handoff check; each definition's section below adds its own.
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
		t6g4CheckRecordedWritesDeclared(t, def, pr)
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

// T6G4StartRequirement exposes t6g4StartRequirement to kinds_t6_g4_atoms_test.go,
// which is an external test package (it imports internal/books, which imports
// this package).
func T6G4StartRequirement() rbac.Requirement { return t6g4StartRequirement() }

// T6G4KindStarts returns every native kind of the group with the Start
// requirement it declares (tasks.CommonOf(d).Perms.Start), for the external atoms
// test: asserting on the shared constructor alone would not catch a kind that
// stopped using it.
func T6G4KindStarts(t *testing.T) map[tasks.Kind]rbac.Requirement {
	deps, _ := fakeT6G4Deps(t)
	out := map[tasks.Kind]rbac.Requirement{}
	for _, d := range t6g4KindsUnchecked(deps, nil) {
		out[d.Kind()] = tasks.CommonOf(d).Perms.Start
	}
	return out
}
