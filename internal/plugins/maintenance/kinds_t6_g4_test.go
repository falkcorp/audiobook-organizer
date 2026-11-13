// file: internal/plugins/maintenance/kinds_t6_g4_test.go
// version: 1.1.2
// guid: 952f6abf-08d2-41bd-aec8-95b8a3f39bd9
// last-edited: 2026-11-13

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/ai"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/serverdecode"
	"github.com/falkcorp/audiobook-organizer/internal/transcribe"
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

func (w recT6G4Writer) MoveBookFilesToBookBulk(a0 []database.BookFileMove, a1 string) error {
	w.rec.add("MoveBookFilesToBookBulk")
	return nil
}

func (w recT6G4Writer) OpenWhisperJournal() (transcribe.ResultJournal, error) {
	w.rec.add("OpenWhisperJournal")
	var r0 transcribe.ResultJournal
	return r0, nil
}

func (w recT6G4Writer) PutFileIntroStats(a0 *database.FileIntroStats) error {
	w.rec.add("PutFileIntroStats")
	return nil
}

func (w recT6G4Writer) PutTranscribeStats(a0 *database.TranscribeStats) error {
	w.rec.add("PutTranscribeStats")
	return nil
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
	defs := t6g4Kinds(deps, nil)
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
	// writes is rule R7's native write set, as books resource kinds; nil with
	// readOnly true means "Declared ReadOnly".
	writes   []string
	readOnly bool
	// liveOnly: HEAD runs this only in an explicit live mode (rule R7).
	liveOnly bool
	// notWhile is rule R5's DependsOn mapping.
	notWhile []tasks.Kind
	// refuse is a params body naming a mode key the native shell must refuse
	// ("" when HEAD's params carry none).
	refuse string
	// noLive skips the Live scenario: the core reaches the network or an external
	// tool before it can make a recorded write.
	noLive bool
	// helpReworded: the native Help drops HEAD's dry-run wording, because the
	// native shell refuses dry_run and a run's mode is the Preview flag, not a
	// param. Every other row's Help is HEAD's Description verbatim.
	helpReworded bool
}

// t6g4Expected is in file order (the order of t6g4Kinds).
var t6g4Expected = []t6g4Want{
	{kind: aiDedupBatchKind, head: func(p *Plugin) sdk.OperationDef { return p.aiDedupBatchDef() },
		resume: tasks.ResumeContinue, writes: []string{"operations"}, liveOnly: true},
	{kind: dedupExactTriageKind, head: func(p *Plugin) sdk.OperationDef { return p.dedupExactTriageDef() },
		resume: tasks.ResumeDrop, writes: []string{"dedup_candidates"}, refuse: `{"apply":true}`},
	{kind: dedupeBookFileRowsKind, head: func(p *Plugin) sdk.OperationDef { return p.dedupeBookFileRowsDef() },
		resume: tasks.ResumeContinue, writes: []string{"book_files"}, notWhile: []tasks.Kind{"library.scan"}, refuse: `{"apply":true}`},
	{kind: durationBackfillKind, head: func(p *Plugin) sdk.OperationDef { return p.durationBackfillDef() },
		resume: tasks.ResumeDrop, writes: []string{"book_files", "books"}, refuse: `{"dry_run":true}`, helpReworded: true},
	{kind: extractWAVClipsKind, head: func(p *Plugin) sdk.OperationDef { return p.extractWAVClipsDef() },
		resume: tasks.ResumeContinue, writes: []string{"book_files"}, liveOnly: true},
	{kind: fileIntroKind, head: func(p *Plugin) sdk.OperationDef { return p.fileIntroTranscribeDef() },
		resume: tasks.ResumeContinue, writes: []string{"book_files", "books"}, refuse: `{"dry_run":true}`, helpReworded: true},
	{kind: fileProvenanceCaptureKind, head: func(p *Plugin) sdk.OperationDef { return p.fileProvenanceCaptureDef() },
		resume: tasks.ResumeDrop, writes: []string{"file_provenance"}, refuse: `{"apply":true}`},
	{kind: fileProvenanceExportKind, head: func(p *Plugin) sdk.OperationDef { return p.fileProvenanceExportDef() },
		resume: tasks.ResumeDrop, writes: []string{"file_provenance"}, refuse: `{"apply":true}`},
	{kind: filePathCollisionReportKind, head: func(p *Plugin) sdk.OperationDef { return p.filePathCollisionReportDef() },
		resume: tasks.ResumeDrop, readOnly: true},
	{kind: fsRegroupKind, head: func(p *Plugin) sdk.OperationDef { return p.fsRegroupXMLDef() },
		resume: tasks.ResumeDrop, writes: []string{"book_files", "books"}, refuse: `{"dry_run":true}`, helpReworded: true},
	{kind: identIndexKind, head: func(p *Plugin) sdk.OperationDef { return p.identIndexRebuildDef() },
		resume: tasks.ResumeDrop, writes: []string{"books"}},
	{kind: integrityCheckKind, head: func(p *Plugin) sdk.OperationDef { return p.integrityCheckDef() },
		resume: tasks.ResumeDrop, readOnly: true},
	{kind: introMigrateSingleFileKind, head: func(p *Plugin) sdk.OperationDef { return p.introMigrateSingleFileDef() },
		resume: tasks.ResumeContinue, writes: []string{"book_files"}, refuse: `{"dry_run":true}`},
	{kind: introTranscribeKind, head: func(p *Plugin) sdk.OperationDef { return p.introTranscribeDef() },
		resume: tasks.ResumeContinue, writes: []string{"book_files", "books"}, refuse: `{"dry_run":true}`, noLive: true},
}

// t6g4HeadResume is rule R5's mapping of HEAD's ResumePolicy: Restart is
// Continue, Requeue is FromZero, Drop and Ask map to themselves.
func t6g4HeadResume(p sdk.ResumePolicy) tasks.ResumePolicy {
	switch p {
	case sdk.ResumeRestart:
		return tasks.ResumeContinue
	case sdk.ResumeRequeue:
		return tasks.ResumeFromZero
	case sdk.ResumeAsk:
		return tasks.ResumeAsk
	default:
		return tasks.ResumeDrop
	}
}

// TestT6G4Kinds_MatchHEADDefs: each native kind equals "books." + HEAD's ID, and
// its Title, Resume, Priority, Timeout, key, NotWhile, readiness, effects class
// and Start permission are rule R5's, R6's and R7's mapping of the HEAD definition
// built in the same test, so a drift in either fails.
func TestT6G4Kinds_MatchHEADDefs(t *testing.T) {
	deps, _ := fakeT6G4Deps(t)
	p := &Plugin{}
	defs := t6g4Kinds(deps, nil)
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
			if w.helpReworded {
				if c.Help == "" || c.Help == head.Description || strings.Contains(strings.ToLower(c.Help), "dry-run") ||
					strings.Contains(c.Help, "dry_run") || strings.Contains(c.Help, "dryRun") {
					t.Errorf("Help must be set, reworded and free of dry-run params, got %q", c.Help)
				}
			} else if c.Help != head.Description {
				t.Errorf("Help differs from HEAD's Description")
			}
			if c.Resume != w.resume || c.Resume != t6g4HeadResume(head.ResumePolicy) {
				t.Errorf("Resume = %v, table %v, mapping of HEAD's %v is %v", c.Resume, w.resume, head.ResumePolicy, t6g4HeadResume(head.ResumePolicy))
			}
			wantTimeout := min(head.Timeout, tasks.MaxTimeout)
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
			if !slices.Equal(c.NotWhile, w.notWhile) {
				t.Errorf("NotWhile = %v, want %v", c.NotWhile, w.notWhile)
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
			if got := t6g4WriteKinds(c.Effects); w.readOnly != c.Effects.IsReadOnly() || !slices.Equal(got, w.writes) {
				t.Errorf("Effects: readOnly=%v writes=%v, want readOnly=%v writes=%v", c.Effects.IsReadOnly(), got, w.readOnly, w.writes)
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
	for _, d := range t6g4Kinds(deps, hook) {
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
		{0, tasks.Sequential("r")}, {1, tasks.Sequential("r")}, {6, tasks.Fixed(6, "r")}, {t6g4CPU, tasks.CPU()},
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
// a Preview run of a kind never builds a write that reaches the writer, a
// Live-only kind refuses a Preview run by name, and every kind refuses the mode
// key its HEAD params carried.
func TestT6G4Kinds_WritesThroughShell(t *testing.T) {
	for _, w := range t6g4Expected {
		t.Run(string(w.kind), func(t *testing.T) {
			if w.refuse != "" {
				deps, pr := fakeT6G4Deps(t)
				def := t6g4KindByName(t, t6g4Kinds(deps, nil), w.kind)
				_, rc := newT6G4RC(w.kind, tasks.ModeLive, &t6g4RunWriter{})
				err := t6g4RunSafely(t, def, rc, w.refuse)
				if err == nil || !strings.Contains(err.Error(), "not accepted") {
					t.Errorf("params %s: err = %v, want a refusal naming the key", w.refuse, err)
				}
				if pr.newWriterCalls != 0 {
					t.Errorf("a refused run built a writer")
				}
			}
			if w.readOnly {
				return
			}
			if w.liveOnly {
				deps, pr := fakeT6G4Deps(t)
				def := t6g4KindByName(t, t6g4Kinds(deps, nil), w.kind)
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
				def := t6g4KindByName(t, t6g4Kinds(deps, nil), w.kind)
				_, rc := newT6G4RC(w.kind, tasks.ModePreview, &t6g4RunWriter{})
				_ = t6g4RunSafely(t, def, rc, "")
				for _, c := range pr.store.rec.all() {
					if t6g4WriterCalls[c] {
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
			def := t6g4KindByName(t, t6g4Kinds(deps, nil), w.kind)
			run := &t6g4RunWriter{name: "run"}
			_, rc := newT6G4RC(w.kind, tasks.ModeLive, run)
			_ = t6g4RunSafely(t, def, rc, "")
			if pr.newWriterCalls != 1 || pr.handed != tasks.Writer(run) {
				t.Errorf("NewWriter called %d time(s) with %v, want once with this run's rc.Writer()", pr.newWriterCalls, pr.handed)
			}
		})
	}
}

// t6g4WriterCalls is the set of method names recT6G4Writer records, so a test
// can tell a write from a store read in the shared call record.
var t6g4WriterCalls = map[string]bool{}

func init() {
	for _, n := range []string{
		"AppendFileEvent", "CarryOverFingerprintWindows", "ClearBookFileIntro", "ClearIntroTimeouts",
		"CreateBookFile", "CreateOperationChange", "DedupTriageApply", "DeleteBookFilesByIDs",
		"MarkOperationChangesReverted", "ModifyBook", "ModifyBookFileDeferAggregates", "MoveBookFilesToBookBulk",
		"PutFileIntroStats", "PutTranscribeStats", "ReassignExternalID", "RebuildIdentIndex", "RecordIntroTimeout",
		"SetBookFileHash", "SetBookFileIntro", "SetFileProvExportCursor", "UpdateBookFile", "UpdateBookFiles",
	} {
		t6g4WriterCalls[n] = true
	}
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
