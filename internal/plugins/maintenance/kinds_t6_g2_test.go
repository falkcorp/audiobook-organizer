// file: internal/plugins/maintenance/kinds_t6_g2_test.go
// version: 1.1.0
// guid: 7d3c1e58-2b94-4a6f-9e07-c5a18f2d4b63
// last-edited: 2026-11-13

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/audioutil"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/walkresume"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/tasks"
	"github.com/falkcorp/mediacore/tasks/taskstest"
)

// ---------------------------------------------------------------------------
// Recording fakes of t6g2Store and t6g2Writer (rule R10).
//
// Both embed the interface they fake and leave it nil: a method a test did not
// provide panics, so a core that reaches for something its scenario did not
// arrange fails loudly instead of reading a zero value.
// ---------------------------------------------------------------------------

type t6g2FakeStore struct {
	t6g2Store
	mu sync.Mutex

	ids      []string
	books    map[string]*database.Book
	files    map[string][]database.BookFile
	snaps    map[string][]database.BookSnapshot
	chapters map[string][]database.Chapter
	// acoustid is the page a scan from the start returns; pages answer a scan
	// after a cursor (a resumed run); verdict judges each entry.
	acoustid []database.BookFileAcoustIDEntry
	pages    map[string]t6g2ScanPage
	verdict  database.BookFileAcoustIDVerdict
	// core and fileCores are the library the shape report reads; verify is the
	// report the index verify reads.
	core      []database.BookCore
	fileCores []database.BookFileCore
	verify    database.BookAtPathIndexReport
	// unsupported names the capabilities Supports refuses.
	unsupported map[t6g2Capability]bool

	checked    int      // CheckBookHashIndex calls (preview of the hash repair)
	verified   int      // VerifyBookAtPathIndex calls
	classified []string // ClassifyBookSigMigration calls (preview of the sidecar migration)
	scanAfters []string // the cursor of every ScanBookFileAcoustIDIndex call
}

// t6g2ScanPage is one page of the acoustid index scan.
type t6g2ScanPage struct {
	entries []database.BookFileAcoustIDEntry
	next    string
}

func (s *t6g2FakeStore) Describe() string { return "t6g2FakeStore" }

func (s *t6g2FakeStore) Supports(c t6g2Capability) bool { return !s.unsupported[c] }

func (s *t6g2FakeStore) ListBookIDs() ([]string, error) { return slices.Clone(s.ids), nil }

func (s *t6g2FakeStore) GetBookByID(id string) (*database.Book, error) {
	if b, ok := s.books[id]; ok {
		cp := *b
		return &cp, nil
	}
	return nil, nil
}

func (s *t6g2FakeStore) GetBookFiles(id string) ([]database.BookFile, error) { return s.files[id], nil }

func (s *t6g2FakeStore) GetBookSnapshots(id string, _ int) ([]database.BookSnapshot, error) {
	return s.snaps[id], nil
}

func (s *t6g2FakeStore) GetChaptersForBook(id string) ([]database.Chapter, error) {
	return s.chapters[id], nil
}

func (s *t6g2FakeStore) GetAllBooksCore(int, int) ([]database.BookCore, error) { return s.core, nil }

func (s *t6g2FakeStore) GetAllBookFilesCore() ([]database.BookFileCore, error) {
	return s.fileCores, nil
}

func (s *t6g2FakeStore) VerifyBookAtPathIndex(context.Context) (database.BookAtPathIndexReport, error) {
	s.mu.Lock()
	s.verified++
	s.mu.Unlock()
	return s.verify, nil
}

func (s *t6g2FakeStore) ClassifyBookSigMigration(id string) (database.BookSigMigrateOutcome, error) {
	s.mu.Lock()
	s.classified = append(s.classified, id)
	s.mu.Unlock()
	return database.BookSigMigrateMigrated, nil
}

func (s *t6g2FakeStore) BookAtPathIndexBuilt() (bool, error) { return true, nil }

func (s *t6g2FakeStore) BookFilesAtPath(string) ([]database.BookFile, error) { return nil, nil }

func (s *t6g2FakeStore) LiveBookIDsAtPath(string) ([]string, error) { return nil, nil }

func (s *t6g2FakeStore) ScanBookFileAcoustIDIndex(after string, _ int) ([]database.BookFileAcoustIDEntry, string, error) {
	s.mu.Lock()
	s.scanAfters = append(s.scanAfters, after)
	s.mu.Unlock()
	if p, ok := s.pages[after]; ok {
		return p.entries, p.next, nil
	}
	if after != "" {
		return nil, "", nil
	}
	return s.acoustid, "", nil
}

func (s *t6g2FakeStore) JudgeBookFileAcoustIDEntry(database.BookFileAcoustIDEntry) (database.BookFileAcoustIDVerdict, error) {
	return s.verdict, nil
}

func (s *t6g2FakeStore) CheckBookHashIndex(context.Context, database.BookHashIndexProgress) (t6g2HashIndexRun, error) {
	s.mu.Lock()
	s.checked++
	s.mu.Unlock()
	return t6g2HashIndexRun{}, nil
}

// t6g2RecWriter records every write that reaches it, as "Method" or
// "Method:arg" strings in call order.
type t6g2RecWriter struct {
	t6g2Writer
	mu    sync.Mutex
	calls []string

	// The boot passes drive a walk of t6g2WalkItems items through the run they
	// are given, as the server's walks do, and record what they saw of it.
	resumes  []walkresume.State // run.Resume of each pass
	ifNeeded []bool             // the ifNeeded flag of each BackfillExternalIDs
	walked   []int              // every item index the run's pool executed
	marks    []int              // every watermark the pool reported
	yieldDue []bool             // run.YieldDue() after the walk, per pass
	// yieldWhenDue makes a pass return walkresume.ErrYield when YieldDue is true.
	yieldWhenDue bool
}

// t6g2WalkItems is the length of the walk a boot pass drives.
const t6g2WalkItems = 6

// walk is the body every boot pass shares: report progress, run the items from
// the resume index, checkpoint at the end, and ask whether to yield.
func (w *t6g2RecWriter) walk(ctx context.Context, run walkresume.Run) error {
	w.mu.Lock()
	w.resumes = append(w.resumes, run.Resume)
	yieldWhenDue := w.yieldWhenDue
	w.mu.Unlock()
	if run.Progress != nil {
		run.Progress(run.Resume.Index, t6g2WalkItems, "walking")
	}
	if run.Exec != nil {
		err := run.Exec(ctx, t6g2WalkItems, run.Resume.Index, func(_ context.Context, i int) error {
			w.mu.Lock()
			w.walked = append(w.walked, i)
			w.mu.Unlock()
			return nil
		}, func(_ context.Context, mark int) {
			w.mu.Lock()
			w.marks = append(w.marks, mark)
			w.mu.Unlock()
		})
		if err != nil {
			return err
		}
	}
	if run.Checkpoint != nil {
		run.Checkpoint(walkresume.State{Cursor: "bat-cave/06.m4b", Index: t6g2WalkItems})
	}
	due := run.YieldDue != nil && run.YieldDue()
	w.mu.Lock()
	w.yieldDue = append(w.yieldDue, due)
	w.mu.Unlock()
	if due && yieldWhenDue {
		return walkresume.ErrYield
	}
	return nil
}

// sortedWalked is the executed item indexes in order (a pool runs them in any).
func (w *t6g2RecWriter) sortedWalked() []int {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := slices.Clone(w.walked)
	slices.Sort(out)
	return out
}

func (w *t6g2RecWriter) rec(s string) {
	w.mu.Lock()
	w.calls = append(w.calls, s)
	w.mu.Unlock()
}

func (w *t6g2RecWriter) got() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.calls)
}

func (w *t6g2RecWriter) BackfillExternalIDs(ctx context.Context, ifNeeded bool, _ func(int, int, string), run walkresume.Run) (bool, error) {
	w.rec("BackfillExternalIDs")
	w.mu.Lock()
	w.ifNeeded = append(w.ifNeeded, ifNeeded)
	w.mu.Unlock()
	return false, w.walk(ctx, run)
}

func (w *t6g2RecWriter) StripMovementAtoms(ctx context.Context, run walkresume.Run) (bool, error) {
	w.rec("StripMovementAtoms")
	return false, w.walk(ctx, run)
}

func (w *t6g2RecWriter) RemuxMalformedM4BFiles(ctx context.Context, _ func(int, int, string), run walkresume.Run) (bool, error) {
	w.rec("RemuxMalformedM4BFiles")
	return false, w.walk(ctx, run)
}

func (w *t6g2RecWriter) TranscodeMalformedM4BFiles(ctx context.Context, _ func(int, int, string), run walkresume.Run) (bool, error) {
	w.rec("TranscodeMalformedM4BFiles")
	return false, w.walk(ctx, run)
}

func (w *t6g2RecWriter) QuarantineKnownBadFiles(context.Context) (int, bool, error) {
	w.rec("QuarantineKnownBadFiles")
	return 0, false, nil
}

func (w *t6g2RecWriter) RepairBookHashIndex(context.Context, database.BookHashIndexProgress) (t6g2HashIndexRun, error) {
	w.rec("RepairBookHashIndex")
	return t6g2HashIndexRun{}, nil
}

func (w *t6g2RecWriter) RebuildBookAtPathIndex(context.Context, database.BookAtPathProgress) (database.BookAtPathBackfillResult, error) {
	w.rec("RebuildBookAtPathIndex")
	return database.BookAtPathBackfillResult{}, nil
}

func (w *t6g2RecWriter) DeleteOrphanBookFileAcoustIDEntries(_ context.Context, e []database.BookFileAcoustIDEntry) (int, error) {
	w.rec(fmt.Sprintf("DeleteOrphanBookFileAcoustIDEntries:%d", len(e)))
	return len(e), nil
}

// ModifyBook runs fn on a book with an empty Description so the restore logic
// really executes, and records whether it changed the row.
func (w *t6g2RecWriter) ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error) {
	fresh := &database.Book{ID: id, Title: "The Dark Knight Returns"}
	switch err := fn(fresh); {
	case errors.Is(err, database.ErrSkipBookWrite):
		w.rec("ModifyBook:" + id + ":skipped")
	case err != nil:
		return nil, err
	default:
		w.rec("ModifyBook:" + id + ":written")
	}
	return fresh, nil
}

func (w *t6g2RecWriter) SaveChaptersForBook(id string, chapters []database.Chapter) error {
	w.rec(fmt.Sprintf("SaveChaptersForBook:%s:%d", id, len(chapters)))
	return nil
}

func (w *t6g2RecWriter) BatchCreateBookFiles(files []*database.BookFile) error {
	w.rec(fmt.Sprintf("BatchCreateBookFiles:%d", len(files)))
	return nil
}

func (w *t6g2RecWriter) MigrateBookSigToSidecar(id string) (database.BookSigMigrateOutcome, error) {
	w.rec("MigrateBookSigToSidecar:" + id)
	return database.BookSigMigrateMigrated, nil
}

// t6g2Fakes is one scenario's store, writer and the record of NewWriter calls.
type t6g2Fakes struct {
	store  *t6g2FakeStore
	writer *t6g2RecWriter

	mu         sync.Mutex
	newWriters []tasks.Writer
	reportDir  string
}

func newT6G2Fakes(t *testing.T) *t6g2Fakes {
	t.Helper()
	return &t6g2Fakes{
		store:     &t6g2FakeStore{unsupported: map[t6g2Capability]bool{}},
		writer:    &t6g2RecWriter{},
		reportDir: t.TempDir(),
	}
}

func (f *t6g2Fakes) deps() t6g2Deps {
	return t6g2Deps{
		Store: f.store,
		NewWriter: func(w tasks.Writer) t6g2Writer {
			f.mu.Lock()
			f.newWriters = append(f.newWriters, w)
			f.mu.Unlock()
			return f.writer
		},
		ReportPath: func(explicit, fileName string) (string, error) {
			if explicit != "" {
				return explicit, nil
			}
			return filepath.Join(f.reportDir, fileName), nil
		},
	}
}

// fakeT6G2Deps is the brief's helper: dependencies backed by fresh fakes.
func fakeT6G2Deps(t *testing.T) t6g2Deps { return newT6G2Fakes(t).deps() }

// ---------------------------------------------------------------------------
// A host with the mode and writer a test chooses (FakeRun is pinned to live
// with no writer).
// ---------------------------------------------------------------------------

type t6g2Host struct {
	*taskstest.FakeRun
	mode tasks.Mode
	w    tasks.Writer
	// yield is what ShouldYield answers.
	yield bool

	mu sync.Mutex
	// capped is every worker count a pool asked the host to cap: the
	// parallelism the run actually used.
	capped  []int
	touched int
	reports []t6g2Report
	notes   []string
}

// t6g2Report is one rc.Progress().Set.
type t6g2Report struct {
	done, total int64
	known       bool
}

func (h *t6g2Host) Mode() tasks.Mode     { return h.mode }
func (h *t6g2Host) Writer() tasks.Writer { return h.w }
func (h *t6g2Host) ShouldYield() bool    { return h.yield }

func (h *t6g2Host) CapWorkers(declared int) int {
	h.mu.Lock()
	h.capped = append(h.capped, declared)
	h.mu.Unlock()
	return h.FakeRun.CapWorkers(declared)
}

func (h *t6g2Host) Touch() {
	h.mu.Lock()
	h.touched++
	h.mu.Unlock()
}

func (h *t6g2Host) ReportProgress(done int64, total tasks.Total) {
	n, known := total.Value()
	h.mu.Lock()
	h.reports = append(h.reports, t6g2Report{done, n, known})
	h.mu.Unlock()
}

func (h *t6g2Host) Note(msg string) {
	h.mu.Lock()
	h.notes = append(h.notes, msg)
	h.mu.Unlock()
}

func (h *t6g2Host) cappedWorkers() []int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.capped)
}

// t6g2RunWriter is a distinguishable tasks.Writer: identity is the assertion.
type t6g2RunWriter struct{ name string }

func (*t6g2RunWriter) Do(context.Context, tasks.Write, func(context.Context) ([]tasks.Change, error)) error {
	return nil
}
func (*t6g2RunWriter) MarkRowApplied(context.Context, string)           {}
func (*t6g2RunWriter) RowApplied(context.Context, string) (bool, error) { return false, nil }

func t6g2NewHost(kind tasks.Kind, mode tasks.Mode, w tasks.Writer) *t6g2Host {
	return &t6g2Host{FakeRun: taskstest.NewFakeRun(context.Background(), kind), mode: mode, w: w}
}

func t6g2NewRC(kind tasks.Kind, mode tasks.Mode, w tasks.Writer) *tasks.RC {
	return tasks.NewRC(t6g2NewHost(kind, mode, w))
}

// t6g2RunKind runs d's own Run, the function the executor would call, with the
// params it would decode, and returns the encoded result.
func t6g2RunKind(t *testing.T, d tasks.Definition, rc *tasks.RC, params string) (json.RawMessage, error) {
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

func t6g2KindByName(t *testing.T, defs []tasks.Definition, kind tasks.Kind) tasks.Definition {
	t.Helper()
	for _, d := range defs {
		if d.Kind() == kind {
			return d
		}
	}
	t.Fatalf("no definition of kind %s", kind)
	return nil
}

// ---------------------------------------------------------------------------
// The table every test reads: one row per converted definition.
// ---------------------------------------------------------------------------

type t6g2Row struct {
	// head is HEAD's def, built from its own xxxDef() method.
	head sdk.OperationDef
	kind tasks.Kind
	// resume is the rule R5 mapping of head.ResumePolicy.
	resume tasks.ResumePolicy
	// writes is the declared write set (nil: ReadOnly); reads is AlsoReads.
	writes, reads []string
	// workers is HEAD's worker count and parPrefix the rule R4 spelling;
	// parPrefix "" means HEAD's body has no RunItems loop.
	workers   int
	parPrefix string
	// liveOnly is rule R7's "live-only writer".
	liveOnly bool
	// def builds HEAD's def from a plugin (head is def over an empty one).
	def func(*Plugin) sdk.OperationDef
	// live is a scenario that makes a live run write: params, an arranger, and
	// the writes it must produce (nil for a kind that writes nothing).
	params   string
	arrange  func(t *testing.T, f *t6g2Fakes)
	wantLive []string
	// wantPreview is what a preview run writes (empty for a refused or
	// read-only preview). wantClassified is what a preview reads through the
	// store instead: a preview's writer never runs.
	wantPreview    []string
	wantClassified []string
}

func t6g2Rows() []t6g2Row {
	p := &Plugin{}
	rows := []t6g2Row{
		{
			def: (*Plugin).externalIDBackfillDef, kind: t6g2KindExternalIDBackfill, resume: tasks.ResumeContinue,
			writes: []string{"external_ids", "settings"}, workers: t6g2BootExternalIDWorkers, parPrefix: "fixed(1)",
			liveOnly: true, params: `{}`, wantLive: []string{"BackfillExternalIDs"},
		},
		{
			def: (*Plugin).movementAtomCleanupDef, kind: t6g2KindMovementAtomCleanup, resume: tasks.ResumeContinue,
			writes: []string{"files", "settings"}, workers: bootWalkWorkers, parPrefix: "fixed(4)",
			liveOnly: true, params: `{}`, wantLive: []string{"StripMovementAtoms"},
		},
		{
			def: (*Plugin).malformedM4BRemuxDef, kind: t6g2KindMalformedM4BRemux, resume: tasks.ResumeContinue,
			writes: []string{"files", "book_files", "settings", "books"}, workers: bootWalkWorkers, parPrefix: "fixed(4)",
			liveOnly: true, params: `{}`, wantLive: []string{"RemuxMalformedM4BFiles"},
		},
		{
			def: (*Plugin).malformedM4BTranscodeDef, kind: t6g2KindMalformedM4BTranscode, resume: tasks.ResumeAsk,
			writes: []string{"files", "book_files", "settings", "books"}, workers: bootTranscodeWorkers, parPrefix: "fixed(2)",
			liveOnly: true, params: `{"quarantine_after":true}`,
			wantLive: []string{"TranscodeMalformedM4BFiles", "QuarantineKnownBadFiles"},
		},
		{
			def: (*Plugin).bookAtPathIndexVerifyDef, kind: t6g2KindBookAtPathIndexVerify, resume: tasks.ResumeDrop,
			params: `{}`,
			arrange: func(t *testing.T, f *t6g2Fakes) {
				f.store.verify = database.BookAtPathIndexReport{SentinelSet: true, BooksScanned: 3, IndexKeysScanned: 3}
			},
		},
		{
			def: (*Plugin).bookAtPathIndexBackfillDef, kind: t6g2KindBookAtPathIndexBackfill, resume: tasks.ResumeDrop,
			writes: []string{"books"}, liveOnly: true, params: `{}`,
			wantLive: []string{"RebuildBookAtPathIndex"},
		},
		{
			def: (*Plugin).bookHashIndexRepairDef, kind: t6g2KindBookHashIndexRepair, resume: tasks.ResumeFromZero,
			writes: []string{"books"}, reads: []string{"books"}, params: `{}`,
			wantLive: []string{"RepairBookHashIndex"},
		},
		{
			def: (*Plugin).bookShapeReportDef, kind: t6g2KindBookShapeReport, resume: tasks.ResumeDrop,
			workers: runtime.NumCPU(), parPrefix: "cpu", params: `{"oversizedThreshold":1}`,
			arrange: func(t *testing.T, f *t6g2Fakes) { t6g2ShapeFixture(t, f) },
		},
		{
			def: (*Plugin).bookFileAcoustIDIndexRepairDef, kind: t6g2KindBookFileAcoustIDIndexRepair, resume: tasks.ResumeContinue,
			writes: []string{"book_files"}, reads: []string{"book_files"}, workers: runtime.NumCPU(), parPrefix: "cpu",
			params: `{}`,
			arrange: func(t *testing.T, f *t6g2Fakes) {
				f.store.acoustid = []database.BookFileAcoustIDEntry{{}}
				f.store.verdict = database.AcoustIDEntryRowGone
			},
			wantLive: []string{"DeleteOrphanBookFileAcoustIDEntries:1"},
		},
		{
			def: (*Plugin).bookSigRecoveryAuditDef, kind: t6g2KindBookSigRecoveryAudit, resume: tasks.ResumeDrop,
			writes: []string{"books"}, params: `{}`,
			arrange: func(t *testing.T, f *t6g2Fakes) {
				desc := "A Bat out of Gotham"
				snap, err := json.Marshal(&database.Book{Title: "The Dark Knight Returns", Description: &desc})
				if err != nil {
					t.Fatal(err)
				}
				f.store.ids = []string{"bat-1"}
				f.store.books = map[string]*database.Book{"bat-1": {ID: "bat-1", Title: "The Dark Knight Returns"}}
				f.store.snaps = map[string][]database.BookSnapshot{"bat-1": {{Timestamp: time.Unix(1, 0), Data: snap}}}
			},
			wantLive: []string{"ModifyBook:bat-1:written"},
		},
		{
			def: (*Plugin).bookSigSidecarMigrateDef, kind: t6g2KindBookSigSidecarMigrate, resume: tasks.ResumeDrop,
			writes: []string{"books"}, workers: runtime.NumCPU(), parPrefix: "cpu", params: `{}`,
			arrange:        func(t *testing.T, f *t6g2Fakes) { f.store.ids = []string{"bat-1", "bat-2"} },
			wantLive:       []string{"MigrateBookSigToSidecar:bat-1", "MigrateBookSigToSidecar:bat-2"},
			wantClassified: []string{"bat-1", "bat-2"},
		},
		{
			def: (*Plugin).buildFolderBookFilesDef, kind: t6g2KindBuildFolderBookFiles, resume: tasks.ResumeDrop,
			writes: []string{"book_files", "books"}, workers: runtime.NumCPU(), parPrefix: "cpu",
			params: `{"book_ids":["bat-1"]}`,
			arrange: func(t *testing.T, f *t6g2Fakes) {
				stubProbe(t, map[string]int{"01.mp3": 100, "02.mp3": 200})
				organized, primary := "organized", true
				f.store.books = map[string]*database.Book{"bat-1": {
					ID: "bat-1", Title: "Batman: Year One", FilePath: makeFolder(t, "01.mp3", "02.mp3"),
					LibraryState: &organized, IsPrimaryVersion: &primary,
				}}
			},
			wantLive: []string{"BatchCreateBookFiles:2"},
		},
		{
			def: (*Plugin).chaptersBackfillDef, kind: t6g2KindChaptersBackfill, resume: tasks.ResumeContinue,
			writes: []string{"chapters"}, workers: runtime.NumCPU(), parPrefix: "cpu",
			params:   `{"book_ids":["bat-1"]}`,
			arrange:  func(t *testing.T, f *t6g2Fakes) { t6g2ChaptersFixture(t, f, "bat-1") },
			wantLive: []string{"SaveChaptersForBook:bat-1:2"},
		},
	}
	for i := range rows {
		rows[i].head = rows[i].def(p)
	}
	return rows
}

// t6g2ChaptersFixture arranges one single-file book per id, each with a file
// whose container carries two chapter markers (the probe is stubbed).
func t6g2ChaptersFixture(t *testing.T, f *t6g2Fakes, ids ...string) {
	t.Helper()
	chbfStubFFprobe(t)
	prev := probeChaptersFn
	probeChaptersFn = func(context.Context, string, string) ([]audioutil.Chapter, error) {
		return []audioutil.Chapter{{ID: 1, StartSec: 0, EndSec: 60, Title: "Gotham"}, {ID: 2, StartSec: 60, EndSec: 120, Title: "Arkham"}}, nil
	}
	t.Cleanup(func() { probeChaptersFn = prev })
	f.store.books = map[string]*database.Book{}
	f.store.files = map[string][]database.BookFile{}
	for _, id := range ids {
		file := filepath.Join(t.TempDir(), id+".m4b")
		if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		f.store.books[id] = &database.Book{ID: id, Title: "Knightfall " + id, FilePath: file}
		f.store.files[id] = []database.BookFile{{BookID: id, FilePath: file}}
	}
}

// t6g2ShapeFixture arranges one book holding two files in a folder: oversized
// at a threshold of one, so the report has a finding to write.
func t6g2ShapeFixture(t *testing.T, f *t6g2Fakes) {
	t.Helper()
	dir := t.TempDir()
	paths := touchAudio(t, dir, "01.mp3", "02.mp3")
	organized, primary := "organized", true
	f.store.core = []database.BookCore{{
		ID: "bat-1", Title: "Batman: Year One", FilePath: dir, LibraryState: &organized, IsPrimaryVersion: &primary,
	}}
	for i, p := range paths {
		f.store.fileCores = append(f.store.fileCores, database.BookFileCore{ID: fmt.Sprintf("bat-1-f%d", i), BookID: "bat-1", FilePath: p})
	}
}

// ---------------------------------------------------------------------------
// The three tests of rule R8 and R10.
// ---------------------------------------------------------------------------

func TestT6G2Kinds_Conformance(t *testing.T) {
	defs := (&Plugin{}).t6g2Kinds(fakeT6G2Deps(t), nil)
	rows := t6g2Rows()
	if len(defs) != 13 || len(rows) != 13 {
		t.Fatalf("t6g2Kinds returned %d definitions for %d rows, want 13", len(defs), len(rows))
	}
	for i, d := range defs {
		if d.Kind() != rows[i].kind {
			t.Errorf("definition %d is %s, want %s (file order)", i, d.Kind(), rows[i].kind)
		}
		if d.Shape() != tasks.ShapeTask {
			t.Errorf("%s has shape %v, want task", d.Kind(), d.Shape())
		}
	}
	taskstest.Conformance(t, defs...)

	t.Run("schedule hook", func(t *testing.T) {
		want := &tasks.Schedule{}
		for _, d := range (&Plugin{}).t6g2Kinds(fakeT6G2Deps(t), func(tasks.Kind) *tasks.Schedule { return want }) {
			if got := tasks.CommonOf(d).Schedule; !reflect.DeepEqual(got, want) {
				t.Errorf("%s: Schedule = %v, want the sched hook's value %v", d.Kind(), got, want)
			}
		}
		for _, d := range defs {
			if tasks.CommonOf(d).Schedule != nil {
				t.Errorf("%s: nil sched must mean no schedule", d.Kind())
			}
		}
	})

	t.Run("watermark", func(t *testing.T) {
		run := func(from, total, every int, order []int) []int {
			var got []int
			w := newT6G2Watermark(from, total, every, func(_ context.Context, mark int) error {
				got = append(got, mark)
				return nil
			})
			for _, i := range order {
				w.complete(context.Background(), i)
			}
			return got
		}
		// Absolute indexes: a resumed walk starting at 10 checkpoints 10+mark,
		// and an open gap stalls the mark until it closes.
		if got, want := run(10, 5, 1, []int{12, 11, 10, 14, 13}), []int{13, 15}; !slices.Equal(got, want) {
			t.Errorf("resumed out-of-order checkpoints = %v, want %v", got, want)
		}
		if got, want := run(0, 7, 3, []int{0, 1, 2, 3, 4, 5, 6}), []int{3, 6, 7}; !slices.Equal(got, want) {
			t.Errorf("every=3 checkpoints = %v, want %v", got, want)
		}
		if got := run(0, 3, 1, []int{9, -1}); len(got) != 0 {
			t.Errorf("out-of-range completions checkpointed %v", got)
		}
		if w := newT6G2Watermark(0, 3, 1, nil); w != nil {
			t.Error("nil fn must give a nil tracker")
		} else {
			w.complete(context.Background(), 0) // safe on nil
		}
	})
}

func TestT6G2Kinds_MatchHEADDefs(t *testing.T) {
	defs := (&Plugin{}).t6g2Kinds(fakeT6G2Deps(t), nil)
	prio := map[sdk.Priority]tasks.Priority{
		sdk.PriorityLow: tasks.PriorityLow, sdk.PriorityNormal: tasks.PriorityNormal, sdk.PriorityHigh: tasks.PriorityHigh,
	}
	atomScopes := t6g2AtomScopes(t, "settings.manage")
	for _, r := range t6g2Rows() {
		t.Run(string(r.kind), func(t *testing.T) {
			if want := tasks.Kind("books." + r.head.ID); r.kind != want {
				t.Fatalf("row kind %s, want %s", r.kind, want)
			}
			c := tasks.CommonOf(t6g2KindByName(t, defs, r.kind))
			if c.Title != r.head.DisplayName {
				t.Errorf("Title = %q, want HEAD's DisplayName %q", c.Title, r.head.DisplayName)
			}
			if c.Help != r.head.Description {
				t.Errorf("Help differs from HEAD's Description")
			}
			if c.Resume != r.resume {
				t.Errorf("Resume = %v, want %v", c.Resume, r.resume)
			}
			// Rule R5: Drop and Ask map to themselves; Restart becomes Continue,
			// or FromZero where the body never checkpoints.
			switch r.head.ResumePolicy {
			case sdk.ResumeDrop:
				if r.resume != tasks.ResumeDrop {
					t.Errorf("HEAD ResumeDrop must stay Drop, row says %v", r.resume)
				}
			case sdk.ResumeAsk:
				if r.resume != tasks.ResumeAsk {
					t.Errorf("HEAD ResumeAsk must stay Ask, row says %v", r.resume)
				}
			case sdk.ResumeRestart:
				if r.resume != tasks.ResumeContinue && r.resume != tasks.ResumeFromZero {
					t.Errorf("HEAD ResumeRestart must map to Continue or FromZero, row says %v", r.resume)
				}
			}
			if c.Priority != prio[r.head.DefaultPriority] {
				t.Errorf("Priority = %v, want %v", c.Priority, prio[r.head.DefaultPriority])
			}
			if c.Timeout != r.head.Timeout || c.ProgressTimeout != r.head.ProgressTimeout {
				t.Errorf("Timeout/ProgressTimeout = %v/%v, want HEAD's %v/%v",
					c.Timeout, c.ProgressTimeout, r.head.Timeout, r.head.ProgressTimeout)
			}
			wantKey := tasks.Key("")
			if r.head.ConcurrencyKey != "" {
				wantKey = tasks.Key("books." + r.head.ConcurrencyKey)
			}
			if c.ConcurrencyKey != wantKey {
				t.Errorf("ConcurrencyKey = %q, want %q", c.ConcurrencyKey, wantKey)
			}
			if r.head.NoWarmupWait {
				t.Fatalf("the table assumes NoWarmupWait false (rule R5)")
			}
			if len(c.Ready) != 1 || c.Ready[0] != "books.memdb" {
				t.Errorf("Ready = %v, want [books.memdb]", c.Ready)
			}
			// The start requirement is the settings.manage atom as internal/books
			// parses it: all of its scopes, not any of them.
			if c.Perms.Start.Kind() != rbac.KindAllOf {
				t.Errorf("Perms.Start is %v, want AllOf", c.Perms.Start.Kind())
			}
			var gotScopes []string
			for _, p := range c.Perms.Start.Permissions() {
				gotScopes = append(gotScopes, string(p))
			}
			if !slices.Equal(gotScopes, atomScopes) {
				t.Errorf("Perms.Start scopes = %v, want the atom's %v", gotScopes, atomScopes)
			}

			// Effects (rule R7).
			res := func(names []string) []string {
				out := make([]string, len(names))
				for i, n := range names {
					out[i] = tasks.Res("books", n).String()
				}
				return out
			}
			refs := func(rs []tasks.Resource) []string {
				out := make([]string, len(rs))
				for i, x := range rs {
					out[i] = x.String()
				}
				return out
			}
			if r.writes == nil {
				if !c.Effects.IsReadOnly() {
					t.Errorf("Effects are not ReadOnly")
				}
			} else if got := refs(c.Effects.WriteSet()); !slices.Equal(got, res(r.writes)) {
				t.Errorf("write set = %v, want %v", got, res(r.writes))
			}
			if got := refs(c.Effects.ReadSet()); !slices.Equal(got, res(r.reads)) {
				t.Errorf("read set = %v, want %v", got, res(r.reads))
			}
			// What HEAD declared stays declared: its Writes/Reads are a subset.
			for _, hw := range r.head.Writes {
				if !slices.Contains(refs(c.Effects.WriteSet()), tasks.Res("books", string(hw)).String()) {
					t.Errorf("HEAD's declared write %q is missing from the kind", hw)
				}
			}
			// A live-only writer is a def with no Preview and a write set.
			headNoPreview := r.head.Preview.Params == nil
			if r.liveOnly != (headNoPreview && r.writes != nil) {
				t.Errorf("liveOnly = %v but HEAD's Preview is nil = %v with writes %v", r.liveOnly, headNoPreview, r.writes)
			}
		})
	}

	t.Run("start requirement is the settings.manage atom", func(t *testing.T) {
		want := t6g2AtomScopes(t, "settings.manage")
		var got []string
		for _, p := range t6g2SettingsManageScopes {
			got = append(got, string(p))
		}
		if !slices.Equal(got, want) {
			t.Errorf("t6g2SettingsManageScopes = %v, internal/books atom table has %v", got, want)
		}
	})

	t.Run("effects cover every write", func(t *testing.T) {
		for _, r := range t6g2Rows() {
			if r.writes == nil {
				continue
			}
			c := tasks.CommonOf(t6g2KindByName(t, defs, r.kind))
			declared := map[string]bool{}
			for _, x := range c.Effects.WriteSet() {
				declared[x.String()] = true
			}
			// The resources the row's writer calls touch, from t6g2Touches (traced
			// from what each production writer reaches, not from the Effects).
			touched := map[string]bool{}
			for _, call := range r.wantLive {
				method, _, _ := strings.Cut(call, ":")
				res, ok := t6g2Touches[method]
				if !ok {
					t.Fatalf("%s: no traced touches for writer method %s", r.kind, method)
				}
				for _, n := range res {
					touched[tasks.Res("books", n).String()] = true
				}
			}
			for x := range touched {
				if !declared[x] {
					t.Errorf("%s: its writes touch %s but Effects do not declare it", r.kind, x)
				}
			}
			for x := range declared {
				if !touched[x] {
					t.Errorf("%s: Effects declare %s but no write of the row touches it", r.kind, x)
				}
			}
		}
	})

	t.Run("parallelism", func(t *testing.T) {
		pars := map[tasks.Kind]tasks.Parallelism{
			t6g2KindExternalIDBackfill:          t6g2ExternalIDBackfillParallelism(),
			t6g2KindMovementAtomCleanup:         t6g2MovementAtomCleanupParallelism(),
			t6g2KindMalformedM4BRemux:           t6g2MalformedM4BRemuxParallelism(),
			t6g2KindMalformedM4BTranscode:       t6g2MalformedM4BTranscodeParallelism(),
			t6g2KindBookShapeReport:             t6g2BookShapeReportParallelism(bookShapeReportParams{}),
			t6g2KindBookFileAcoustIDIndexRepair: t6g2BookFileAcoustIDIndexRepairParallelism(),
			t6g2KindBookSigSidecarMigrate:       t6g2BookSigSidecarMigrateParallelism(),
			t6g2KindBuildFolderBookFiles:        t6g2BuildFolderBookFilesParallelism(),
			t6g2KindChaptersBackfill:            t6g2ChaptersBackfillParallelism(),
		}
		for _, r := range t6g2Rows() {
			par, ok := pars[r.kind]
			if r.parPrefix == "" {
				if ok {
					t.Errorf("%s: has a parallelism but HEAD has no pool", r.kind)
				}
				continue
			}
			if !ok {
				t.Errorf("%s: HEAD runs a pool of %d but the kind declares none", r.kind, r.workers)
				continue
			}
			if !strings.HasPrefix(par.String(), r.parPrefix) {
				t.Errorf("%s: parallelism %q, want prefix %q", r.kind, par.String(), r.parPrefix)
			}
			if par.Workers() != r.workers {
				t.Errorf("%s: %d workers, want HEAD's %d", r.kind, par.Workers(), r.workers)
			}
		}
		// book-shape-report: the concurrency param when set, else every CPU.
		if got := t6g2BookShapeReportParallelism(bookShapeReportParams{Concurrency: 3}); got.Workers() != 3 {
			t.Errorf("shape report with concurrency 3 runs %d workers, want 3", got.Workers())
		}
	})
}

// t6g2AtomScopes reads one atom's scopes from internal/books/atoms.go.
// internal/books imports this package, so a test here cannot import it (an
// import cycle); the table's source is parsed instead, which fails the same way
// when the two drift.
func t6g2AtomScopes(t *testing.T, atom string) []string {
	t.Helper()
	path := filepath.Join("..", "..", "books", "atoms.go")
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	var scopes []string
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if id, ok := call.Fun.(*ast.Ident); !ok || id.Name != "atomRow" || len(call.Args) < 2 {
			return true
		}
		lit := func(e ast.Expr) string {
			b, ok := e.(*ast.BasicLit)
			if !ok {
				return ""
			}
			v, _ := strconv.Unquote(b.Value)
			return v
		}
		if lit(call.Args[0]) != atom {
			return true
		}
		for _, a := range call.Args[1:] {
			scopes = append(scopes, lit(a))
		}
		return false
	})
	if len(scopes) == 0 {
		t.Fatalf("no atomRow(%q, ...) in %s", atom, path)
	}
	return scopes
}

// t6g2ArrangeAndRun builds fresh fakes, arranges the row's scenario and runs
// its native kind in the given mode. It returns the fakes, the run's writer and
// the error.
func t6g2ArrangeAndRun(t *testing.T, r t6g2Row, mode tasks.Mode, params string) (*t6g2Fakes, *t6g2Host, error) {
	t.Helper()
	f := newT6G2Fakes(t)
	if r.arrange != nil {
		r.arrange(t, f)
	}
	def := t6g2KindByName(t, (&Plugin{}).t6g2Kinds(f.deps(), nil), r.kind)
	h := t6g2NewHost(r.kind, mode, &t6g2RunWriter{name: string(r.kind)})
	_, err := t6g2RunKind(t, def, tasks.NewRC(h), params)
	return f, h, err
}

func TestT6G2Kinds_WritesThroughShell(t *testing.T) {
	for _, r := range t6g2Rows() {
		if r.writes == nil {
			continue
		}
		t.Run(string(r.kind)+"/live", func(t *testing.T) {
			f, h, err := t6g2ArrangeAndRun(t, r, tasks.ModeLive, r.params)
			if err != nil {
				t.Fatalf("live run: %v", err)
			}
			if len(f.newWriters) != 1 || f.newWriters[0] != h.w {
				t.Errorf("NewWriter called with %v, want exactly the run's rc.Writer()", f.newWriters)
			}
			if len(f.store.classified) != 0 {
				t.Errorf("a live run classified through the store: %v", f.store.classified)
			}
			if got := f.writer.got(); !slices.Equal(got, r.wantLive) {
				t.Errorf("writes reaching the t6g2Writer = %v, want %v", got, r.wantLive)
			}
		})
		t.Run(string(r.kind)+"/preview", func(t *testing.T) {
			f, _, err := t6g2ArrangeAndRun(t, r, tasks.ModePreview, r.params)
			if r.liveOnly {
				if err == nil || !strings.Contains(err.Error(), "preview not supported; start with mode live") {
					t.Fatalf("preview of a live-only writer: err = %v, want the refusal", err)
				}
				if !strings.HasPrefix(err.Error(), string(r.kind)+": ") {
					t.Errorf("refusal %q does not name the kind", err)
				}
				if len(f.newWriters) != 0 || len(f.writer.got()) != 0 {
					t.Errorf("a refused preview touched the writer: %v", f.writer.got())
				}
				return
			}
			if err != nil {
				t.Fatalf("preview run: %v", err)
			}
			if got := f.writer.got(); !slices.Equal(got, r.wantPreview) {
				t.Errorf("preview writes = %v, want %v", got, r.wantPreview)
			}
			if got := f.store.classified; !slices.Equal(got, r.wantClassified) {
				t.Errorf("preview classified %v through the store, want %v", got, r.wantClassified)
			}
		})
	}

	for _, r := range t6g2Rows() {
		t.Run(string(r.kind)+"/mode keys refused", func(t *testing.T) {
			// The set is the same for every kind, and a key is refused in any
			// case: the params decode with encoding/json, which matches keys
			// case-insensitively.
			var keys []string
			for _, k := range t6g2ModeKeys {
				keys = append(keys, k, strings.ToUpper(k), strings.ToUpper(k[:1])+k[1:])
			}
			for _, key := range keys {
				f := newT6G2Fakes(t)
				def := t6g2KindByName(t, (&Plugin{}).t6g2Kinds(f.deps(), nil), r.kind)
				_, err := t6g2RunKind(t, def, t6g2NewRC(r.kind, tasks.ModeLive, &t6g2RunWriter{}), fmt.Sprintf(`{%q:true}`, key))
				if err == nil || !strings.Contains(err.Error(), key) {
					t.Errorf("params key %q: err = %v, want a refusal naming it", key, err)
				}
				if len(f.writer.got()) != 0 {
					t.Errorf("params key %q: a refused run wrote %v", key, f.writer.got())
				}
			}
		})
	}

	t.Run("head shell", t6g2HeadShellSubtests)
	t.Run("read-only kinds", t6g2ReadOnlySubtests)
	t.Run("restore", t6g2RestoreSubtests)
	t.Run("parallelism used", t6g2ParallelismSubtests)
	t.Run("boot walk", t6g2BootWalkSubtests)
	t.Run("pool", t6g2PoolSubtests)
	t.Run("refusals", t6g2RefusalSubtests)

	t.Run("hash repair preview checks through the store", func(t *testing.T) {
		f := newT6G2Fakes(t)
		def := t6g2KindByName(t, (&Plugin{}).t6g2Kinds(f.deps(), nil), t6g2KindBookHashIndexRepair)
		if _, err := t6g2RunKind(t, def, t6g2NewRC(def.Kind(), tasks.ModePreview, &t6g2RunWriter{}), `{}`); err != nil {
			t.Fatal(err)
		}
		if f.store.checked != 1 || len(f.writer.got()) != 0 {
			t.Errorf("checked=%d writes=%v, want one store check and no write", f.store.checked, f.writer.got())
		}
	})

	t.Run("hash repair ensure refuses a preview", func(t *testing.T) {
		f := newT6G2Fakes(t)
		def := t6g2KindByName(t, (&Plugin{}).t6g2Kinds(f.deps(), nil), t6g2KindBookHashIndexRepair)
		_, err := t6g2RunKind(t, def, t6g2NewRC(def.Kind(), tasks.ModePreview, &t6g2RunWriter{}), `{"ensure":true}`)
		if err == nil {
			t.Fatal("ensure in preview mode ran; it writes, so it must be refused")
		}
	})

	t.Run("an unsupported store refuses before writing", func(t *testing.T) {
		f := newT6G2Fakes(t)
		f.store.unsupported[t6g2CapSigMigrate] = true
		f.store.ids = []string{"bat-1"}
		def := t6g2KindByName(t, (&Plugin{}).t6g2Kinds(f.deps(), nil), t6g2KindBookSigSidecarMigrate)
		_, err := t6g2RunKind(t, def, t6g2NewRC(def.Kind(), tasks.ModeLive, &t6g2RunWriter{}), `{}`)
		if err == nil || !strings.Contains(err.Error(), "t6g2FakeStore") {
			t.Fatalf("err = %v, want a refusal naming the store", err)
		}
		if len(f.writer.got()) != 0 {
			t.Errorf("wrote %v on a store that cannot migrate", f.writer.got())
		}
	})

	t.Run("missing deps are refused, not dereferenced", func(t *testing.T) {
		def := t6g2KindByName(t, (&Plugin{}).t6g2Kinds(t6g2Deps{}, nil), t6g2KindBookSigSidecarMigrate)
		if _, err := t6g2RunKind(t, def, t6g2NewRC(def.Kind(), tasks.ModeLive, &t6g2RunWriter{}), `{}`); err == nil {
			t.Fatal("a kind with no deps ran")
		}
	})
}

// ---------------------------------------------------------------------------
// What each writer method touches, traced (rule R7).
// ---------------------------------------------------------------------------

// t6g2Touches is, per t6g2Writer method, the resource kinds the production
// writer rewrites. It is traced from the code each method reaches, not copied
// from any kind's Effects: BatchCreateBookFiles runs notifyBookFileChanges and
// then RecomputeBookAggregates, which rewrites the books row; a remux's
// RecordRewrite runs UpdateBookFileHashes, which recomputes the same
// aggregates; the boot passes record their done flag in settings; the
// transcode's quarantine step marks books.
var t6g2Touches = map[string][]string{
	"BackfillExternalIDs":                 {"external_ids", "settings"},
	"StripMovementAtoms":                  {"files", "settings"},
	"RemuxMalformedM4BFiles":              {"files", "book_files", "settings", "books"},
	"TranscodeMalformedM4BFiles":          {"files", "book_files", "settings"},
	"QuarantineKnownBadFiles":             {"books", "settings"},
	"RepairBookHashIndex":                 {"books"},
	"RebuildBookAtPathIndex":              {"books"},
	"DeleteOrphanBookFileAcoustIDEntries": {"book_files"},
	"ModifyBook":                          {"books"},
	"SaveChaptersForBook":                 {"chapters"},
	"BatchCreateBookFiles":                {"book_files", "books"},
	"MigrateBookSigToSidecar":             {"books"},
}

// t6g2HeadLive is the params that make HEAD's shell write for each writing def.
var t6g2HeadLive = map[tasks.Kind]string{
	t6g2KindExternalIDBackfill:          `{}`,
	t6g2KindMovementAtomCleanup:         `{}`,
	t6g2KindMalformedM4BRemux:           `{}`,
	t6g2KindMalformedM4BTranscode:       `{"quarantine_after":true}`,
	t6g2KindBookAtPathIndexBackfill:     `{}`,
	t6g2KindBookHashIndexRepair:         `{"dry_run":false}`,
	t6g2KindBookFileAcoustIDIndexRepair: `{"dry_run":false}`,
	t6g2KindBookSigRecoveryAudit:        `{"dry_run":false}`,
	t6g2KindBookSigSidecarMigrate:       `{"dry_run":false}`,
	t6g2KindBuildFolderBookFiles:        `{"apply":true,"book_ids":["bat-1"]}`,
	t6g2KindChaptersBackfill:            `{"apply":true,"book_ids":["bat-1"]}`,
}

// ---------------------------------------------------------------------------
// HEAD's shell over the same fakes.
// ---------------------------------------------------------------------------

// t6g2HeadStore is the plugin's OpsStore over a scenario's fakes: reads reach
// the fake store, writes reach the recording writer, so a HEAD run and a native
// run of one scenario record the same writes.
type t6g2HeadStore struct {
	database.Store
	f *t6g2Fakes
}

func (s t6g2HeadStore) ListBookIDs() ([]string, error) { return s.f.store.ListBookIDs() }
func (s t6g2HeadStore) GetBookByID(id string) (*database.Book, error) {
	return s.f.store.GetBookByID(id)
}
func (s t6g2HeadStore) GetBookFiles(id string) ([]database.BookFile, error) {
	return s.f.store.GetBookFiles(id)
}
func (s t6g2HeadStore) GetBookSnapshots(id string, n int) ([]database.BookSnapshot, error) {
	return s.f.store.GetBookSnapshots(id, n)
}
func (s t6g2HeadStore) GetChaptersForBook(id string) ([]database.Chapter, error) {
	return s.f.store.GetChaptersForBook(id)
}
func (s t6g2HeadStore) GetAllBooksCore(limit, offset int) ([]database.BookCore, error) {
	return s.f.store.GetAllBooksCore(limit, offset)
}
func (s t6g2HeadStore) GetAllBookFilesCore() ([]database.BookFileCore, error) {
	return s.f.store.GetAllBookFilesCore()
}
func (s t6g2HeadStore) BookFilesAtPath(p string) ([]database.BookFile, error) {
	return s.f.store.BookFilesAtPath(p)
}
func (s t6g2HeadStore) LiveBookIDsAtPath(p string) ([]string, error) {
	return s.f.store.LiveBookIDsAtPath(p)
}
func (s t6g2HeadStore) BookAtPathIndexBuilt() (bool, error) { return s.f.store.BookAtPathIndexBuilt() }
func (s t6g2HeadStore) VerifyBookAtPathIndex(ctx context.Context) (database.BookAtPathIndexReport, error) {
	return s.f.store.VerifyBookAtPathIndex(ctx)
}
func (s t6g2HeadStore) ScanBookFileAcoustIDIndex(after string, n int) ([]database.BookFileAcoustIDEntry, string, error) {
	return s.f.store.ScanBookFileAcoustIDIndex(after, n)
}
func (s t6g2HeadStore) JudgeBookFileAcoustIDEntry(e database.BookFileAcoustIDEntry) (database.BookFileAcoustIDVerdict, error) {
	return s.f.store.JudgeBookFileAcoustIDEntry(e)
}
func (s t6g2HeadStore) CheckBookHashIndex(ctx context.Context, p database.BookHashIndexProgress) (database.BookHashIndexReport, error) {
	_, err := s.f.store.CheckBookHashIndex(ctx, p)
	return database.BookHashIndexReport{}, err
}

func (s t6g2HeadStore) ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error) {
	return s.f.writer.ModifyBook(id, fn)
}
func (s t6g2HeadStore) SaveChaptersForBook(id string, c []database.Chapter) error {
	return s.f.writer.SaveChaptersForBook(id, c)
}
func (s t6g2HeadStore) BatchCreateBookFiles(files []*database.BookFile) error {
	return s.f.writer.BatchCreateBookFiles(files)
}
func (s t6g2HeadStore) RebuildBookAtPathIndex(ctx context.Context, p database.BookAtPathProgress) (database.BookAtPathBackfillResult, error) {
	return s.f.writer.RebuildBookAtPathIndex(ctx, p)
}
func (s t6g2HeadStore) DeleteOrphanBookFileAcoustIDEntries(ctx context.Context, e []database.BookFileAcoustIDEntry) (int, error) {
	return s.f.writer.DeleteOrphanBookFileAcoustIDEntries(ctx, e)
}
func (s t6g2HeadStore) RepairBookHashIndex(ctx context.Context, p database.BookHashIndexProgress) (database.BookHashIndexReport, error) {
	_, err := s.f.writer.RepairBookHashIndex(ctx, p)
	return database.BookHashIndexReport{}, err
}

// MigrateBookSigToSidecar is the store's one method for both directions, as
// HEAD's store has it: a dry run classifies, a live call migrates.
func (s t6g2HeadStore) MigrateBookSigToSidecar(id string, dryRun bool) (database.BookSigMigrateOutcome, error) {
	if dryRun {
		return s.f.store.ClassifyBookSigMigration(id)
	}
	return s.f.writer.MigrateBookSigToSidecar(id)
}

// t6g2HeadDeps is HEAD's ServerDeps over the fakes: the boot passes reach the
// recording writer.
type t6g2HeadDeps struct {
	fakeDeps
	w *t6g2RecWriter
}

func (d t6g2HeadDeps) BackfillExternalIDs(ctx context.Context, ifNeeded bool, p func(int, int, string), run walkresume.Run) (bool, error) {
	return d.w.BackfillExternalIDs(ctx, ifNeeded, p, run)
}
func (d t6g2HeadDeps) StripMovementAtoms(ctx context.Context, run walkresume.Run) (bool, error) {
	return d.w.StripMovementAtoms(ctx, run)
}
func (d t6g2HeadDeps) RemuxMalformedM4BFiles(ctx context.Context, p func(int, int, string), run walkresume.Run) (bool, error) {
	return d.w.RemuxMalformedM4BFiles(ctx, p, run)
}
func (d t6g2HeadDeps) TranscodeMalformedM4BFiles(ctx context.Context, p func(int, int, string), run walkresume.Run) (bool, error) {
	return d.w.TranscodeMalformedM4BFiles(ctx, p, run)
}
func (d t6g2HeadDeps) QuarantineKnownBadFiles(ctx context.Context) (int, bool, error) {
	return d.w.QuarantineKnownBadFiles(ctx)
}

// t6g2ResultReporter is a reporter that can persist a result, as the registry's
// is: build-folder-book-files stores its report on the row and fails without it.
type t6g2ResultReporter struct {
	*fakeReporter
	result any
}

func (r *t6g2ResultReporter) SetResult(v any) error {
	r.result = v
	return nil
}

// t6g2HeadShellSubtests drives every writing HEAD def's Run over a fixture
// plugin and asserts its writes reach the store, the same writes the native
// kind makes of the same scenario.
func t6g2HeadShellSubtests(t *testing.T) {
	for _, r := range t6g2Rows() {
		if r.writes == nil {
			continue
		}
		t.Run(string(r.kind), func(t *testing.T) {
			f := newT6G2Fakes(t)
			if r.arrange != nil {
				r.arrange(t, f)
			}
			p := &Plugin{deps: t6g2HeadDeps{fakeDeps: fakeDeps{store: t6g2HeadStore{f: f}}, w: f.writer}}
			rep := &t6g2ResultReporter{fakeReporter: &fakeReporter{}}
			if err := r.def(p).Run(context.Background(), json.RawMessage(t6g2HeadLive[r.kind]), rep); err != nil {
				t.Fatalf("HEAD run: %v", err)
			}
			if got := f.writer.got(); !slices.Equal(got, r.wantLive) {
				t.Errorf("writes reaching the store = %v, want %v", got, r.wantLive)
			}
			if r.kind == t6g2KindBuildFolderBookFiles {
				// The shell persists the report on the run's row.
				report, ok := rep.result.(*folderBuildReport)
				if !ok || report == nil || report.RowsBuilt != 2 {
					t.Errorf("persisted result = %#v, want the report with 2 rows built", rep.result)
				}
			}
		})
	}

	t.Run("build-folder-book-files without a result sink fails", func(t *testing.T) {
		r := t6g2Rows()[11]
		if r.kind != t6g2KindBuildFolderBookFiles {
			t.Fatalf("row 11 is %s", r.kind)
		}
		f := newT6G2Fakes(t)
		r.arrange(t, f)
		p := &Plugin{deps: t6g2HeadDeps{fakeDeps: fakeDeps{store: t6g2HeadStore{f: f}}, w: f.writer}}
		err := r.def(p).Run(context.Background(), json.RawMessage(t6g2HeadLive[r.kind]), &fakeReporter{})
		if err == nil || !strings.Contains(err.Error(), "persist result") {
			t.Errorf("err = %v, want the persist-result failure", err)
		}
	})
}

// t6g2ReadOnlySubtests runs the two ReadOnly kinds natively: nothing is written,
// and the shape report's one output is the TSV at the resolved path.
func t6g2ReadOnlySubtests(t *testing.T) {
	rows := map[tasks.Kind]t6g2Row{}
	for _, r := range t6g2Rows() {
		rows[r.kind] = r
	}

	t.Run("verify", func(t *testing.T) {
		r := rows[t6g2KindBookAtPathIndexVerify]
		f, h, err := t6g2ArrangeAndRun(t, r, tasks.ModePreview, r.params)
		if err != nil {
			t.Fatalf("verify: %v", err)
		}
		if f.store.verified != 1 || len(f.writer.got()) != 0 || len(f.newWriters) != 1 {
			t.Errorf("verified=%d writes=%v writers=%d, want one verify, no writes", f.store.verified, f.writer.got(), len(f.newWriters))
		}
		_ = h
		// An incomplete index fails the run, as HEAD's does.
		f2 := newT6G2Fakes(t)
		f2.store.verify = database.BookAtPathIndexReport{SentinelSet: true, MissingLive: 2}
		def := t6g2KindByName(t, (&Plugin{}).t6g2Kinds(f2.deps(), nil), r.kind)
		if _, err := t6g2RunKind(t, def, t6g2NewRC(r.kind, tasks.ModePreview, nil), `{}`); err == nil || !strings.Contains(err.Error(), "INCOMPLETE") {
			t.Errorf("incomplete index: err = %v, want the INCOMPLETE refusal", err)
		}
	})

	t.Run("shape report writes its TSV and nothing else", func(t *testing.T) {
		r := rows[t6g2KindBookShapeReport]
		f, h, err := t6g2ArrangeAndRun(t, r, tasks.ModePreview, r.params)
		if err != nil {
			t.Fatalf("shape report: %v", err)
		}
		if len(f.writer.got()) != 0 {
			t.Errorf("a read-only report wrote %v", f.writer.got())
		}
		paths, _ := filepath.Glob(filepath.Join(f.reportDir, "book-shape-report-*.tsv"))
		if len(paths) != 1 {
			t.Fatalf("report files = %v, want exactly one under the resolved directory", paths)
		}
		tsv, err := os.ReadFile(paths[0])
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(string(tsv), "shape\tdir\tbook_ids\t") || !strings.Contains(string(tsv), shapeOversized+"\t") || !strings.Contains(string(tsv), "bat-1") {
			t.Errorf("TSV = %q, want the header and an oversized finding for bat-1", tsv)
		}
		_ = h

		// An explicit reportPath param wins over the resolved directory.
		explicit := filepath.Join(t.TempDir(), "nested", "shapes.tsv")
		f2 := newT6G2Fakes(t)
		t6g2ShapeFixture(t, f2)
		def := t6g2KindByName(t, (&Plugin{}).t6g2Kinds(f2.deps(), nil), r.kind)
		if _, err := t6g2RunKind(t, def, t6g2NewRC(r.kind, tasks.ModeLive, nil), fmt.Sprintf(`{"oversizedThreshold":1,"reportPath":%q}`, explicit)); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(explicit); err != nil {
			t.Errorf("explicit report path not written: %v", err)
		}
	})

	t.Run("shape report stamps liveness around each directory stat", func(t *testing.T) {
		// One group: with the disk stat on the core touches once more than with it
		// off, so the difference is the env's Touch reaching the host.
		touches := func(params string) int {
			f := newT6G2Fakes(t)
			t6g2ShapeFixture(t, f)
			def := t6g2KindByName(t, (&Plugin{}).t6g2Kinds(f.deps(), nil), t6g2KindBookShapeReport)
			h := t6g2NewHost(t6g2KindBookShapeReport, tasks.ModePreview, nil)
			if _, err := t6g2RunKind(t, def, tasks.NewRC(h), params); err != nil {
				t.Fatal(err)
			}
			return h.touched
		}
		on, off := touches(`{"oversizedThreshold":1}`), touches(`{"oversizedThreshold":1,"skipDiskStat":true}`)
		if on-off != 1 {
			t.Errorf("touches with the stat = %d, without = %d, want exactly one more", on, off)
		}
	})
}

// ---------------------------------------------------------------------------
// Restore: HEAD-shaped checkpoint bytes reach the core.
// ---------------------------------------------------------------------------

// t6g2Seeded runs a kind's native Run with a checkpoint an earlier attempt
// (HEAD's shell, or the native one, which saves the same shape) left behind.
func t6g2Seeded(t *testing.T, f *t6g2Fakes, kind tasks.Kind, params, ckpt string) (*t6g2Host, error) {
	t.Helper()
	def := t6g2KindByName(t, (&Plugin{}).t6g2Kinds(f.deps(), nil), kind)
	h := t6g2NewHost(kind, tasks.ModeLive, &t6g2RunWriter{})
	if err := h.SetCheckpoint("", json.RawMessage(ckpt)); err != nil {
		t.Fatal(err)
	}
	_, err := t6g2RunKind(t, def, tasks.NewRC(h), params)
	return h, err
}

func t6g2RestoreSubtests(t *testing.T) {
	// The four boot passes: a checkpoint is the params with `resume` updated, and
	// it replaces the start params whole.
	state := walkresume.State{Cursor: "bat-cave/02.m4b", Phase: "itunes", Index: 4}
	for _, tc := range []struct {
		kind  tasks.Kind
		ckpt  string
		calls []string
	}{
		{t6g2KindExternalIDBackfill, `{"if_needed":true,"resume":{"cursor":"bat-cave/02.m4b","phase":"itunes","index":4}}`, []string{"BackfillExternalIDs"}},
		{t6g2KindMovementAtomCleanup, `{"resume":{"cursor":"bat-cave/02.m4b","phase":"itunes","index":4}}`, []string{"StripMovementAtoms"}},
		{t6g2KindMalformedM4BRemux, `{"resume":{"cursor":"bat-cave/02.m4b","phase":"itunes","index":4}}`, []string{"RemuxMalformedM4BFiles"}},
		{t6g2KindMalformedM4BTranscode, `{"quarantine_after":true,"resume":{"cursor":"bat-cave/02.m4b","phase":"itunes","index":4}}`,
			[]string{"TranscodeMalformedM4BFiles", "QuarantineKnownBadFiles"}},
	} {
		t.Run(string(tc.kind), func(t *testing.T) {
			f := newT6G2Fakes(t)
			if _, err := t6g2Seeded(t, f, tc.kind, `{}`, tc.ckpt); err != nil {
				t.Fatal(err)
			}
			w := f.writer
			if len(w.resumes) == 0 || w.resumes[0] != state {
				t.Errorf("the walk was given resume %v, want %v", w.resumes, state)
			}
			if got := w.sortedWalked(); !slices.Equal(got, []int{4, 5}) {
				t.Errorf("items executed = %v, want only those after the saved index", got)
			}
			if got := w.marks; !slices.Equal(got, []int{t6g2WalkItems}) {
				t.Errorf("watermarks = %v, want the absolute end of the walk", got)
			}
			if got := w.got(); !slices.Equal(got, tc.calls) {
				t.Errorf("calls = %v, want %v (the restored params replace the start params)", got, tc.calls)
			}
			if tc.kind == t6g2KindExternalIDBackfill && !slices.Equal(w.ifNeeded, []bool{true}) {
				t.Errorf("ifNeeded = %v, want the restored true", w.ifNeeded)
			}
		})
	}

	t.Run(string(t6g2KindBookFileAcoustIDIndexRepair), func(t *testing.T) {
		f := newT6G2Fakes(t)
		f.store.verdict = database.AcoustIDEntryRowGone
		f.store.pages = map[string]t6g2ScanPage{
			"bat-key-02": {entries: []database.BookFileAcoustIDEntry{{}}, next: "bat-key-09"},
			"bat-key-09": {},
		}
		// HEAD's checkpoint carries its own dry_run spelling; the run's mode wins.
		h, err := t6g2Seeded(t, f, t6g2KindBookFileAcoustIDIndexRepair, `{}`,
			`{"dry_run":true,"resume_after":"bat-key-02","done":{"scanned":7,"live":1,"lacks_seg":0,"row_gone":2,"malformed":0}}`)
		if err != nil {
			t.Fatal(err)
		}
		if got := f.store.scanAfters; !slices.Equal(got, []string{"bat-key-02", "bat-key-09"}) {
			t.Errorf("scan cursors = %v, want the scan to start after the saved key", got)
		}
		if got := f.writer.got(); !slices.Equal(got, []string{"DeleteOrphanBookFileAcoustIDEntries:1"}) {
			t.Errorf("writes = %v, want the live run to delete (the checkpoint's dry_run is not the mode)", got)
		}
		raw, ok := h.CheckpointJSON("")
		if !ok {
			t.Fatal("no checkpoint written after the page")
		}
		var saved acoustIDRepairParams
		if err := json.Unmarshal(raw, &saved); err != nil {
			t.Fatal(err)
		}
		if saved.ResumeAfter != "bat-key-09" || saved.Done == nil || saved.Done.Scanned != 8 {
			t.Errorf("saved checkpoint = %s, want the cursor advanced and the count carried on (7 + 1)", raw)
		}
	})

	t.Run(string(t6g2KindChaptersBackfill), func(t *testing.T) {
		f := newT6G2Fakes(t)
		t6g2ChaptersFixture(t, f, "bat-1", "bat-2", "bat-3")
		h, err := t6g2Seeded(t, f, t6g2KindChaptersBackfill, `{"book_ids":["bat-1","bat-2","bat-3"]}`,
			`{"apply":true,"book_ids":["bat-1","bat-2","bat-3"],"resumeFrom":2,"alreadyPersisted":1}`)
		if err != nil {
			t.Fatal(err)
		}
		if got := f.writer.got(); !slices.Equal(got, []string{"SaveChaptersForBook:bat-3:2"}) {
			t.Errorf("writes = %v, want only the book after the saved watermark", got)
		}
		raw, ok := h.CheckpointJSON("")
		if !ok {
			t.Fatal("no checkpoint written")
		}
		var saved chaptersBackfillParams
		if err := json.Unmarshal(raw, &saved); err != nil {
			t.Fatal(err)
		}
		if saved.ResumeFrom != 3 || saved.AlreadyPersisted != 2 {
			t.Errorf("saved checkpoint = %s, want resumeFrom 3 and alreadyPersisted 2 (1 carried + 1 written)", raw)
		}
	})
}

// ---------------------------------------------------------------------------
// Parallelism actually used, and the boot walk.
// ---------------------------------------------------------------------------

func t6g2ParallelismSubtests(t *testing.T) {
	for _, r := range t6g2Rows() {
		mode := tasks.ModeLive
		if r.writes == nil {
			mode = tasks.ModePreview
		}
		t.Run(string(r.kind), func(t *testing.T) {
			_, h, err := t6g2ArrangeAndRun(t, r, mode, r.params)
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			got := h.cappedWorkers()
			if r.parPrefix == "" {
				if len(got) != 0 {
					t.Errorf("HEAD has no pool but the run asked the host to cap %v workers", got)
				}
				return
			}
			if len(got) == 0 {
				t.Fatalf("no pool ran")
			}
			for _, n := range got {
				if n != r.workers {
					t.Errorf("a pool ran %d workers, want HEAD's %d (all: %v)", n, r.workers, got)
				}
			}
		})
	}

	t.Run("shape report follows its concurrency param", func(t *testing.T) {
		r := t6g2Rows()[7]
		if r.kind != t6g2KindBookShapeReport {
			t.Fatalf("row 7 is %s", r.kind)
		}
		for _, tc := range []struct {
			params string
			want   int
		}{
			{`{"oversizedThreshold":1,"concurrency":3}`, 3},
			{`{"oversizedThreshold":1,"concurrency":-2}`, runtime.NumCPU()},
		} {
			_, h, err := t6g2ArrangeAndRun(t, r, tasks.ModePreview, tc.params)
			if err != nil {
				t.Fatal(err)
			}
			for _, n := range h.cappedWorkers() {
				if n != tc.want {
					t.Errorf("params %s: pool of %d workers, want %d", tc.params, n, tc.want)
				}
			}
		}
	})
}

func t6g2BootWalkSubtests(t *testing.T) {
	run := func(t *testing.T, ckpt string, yield bool) (*t6g2Fakes, *t6g2Host, error) {
		t.Helper()
		f := newT6G2Fakes(t)
		f.writer.yieldWhenDue = yield
		def := t6g2KindByName(t, (&Plugin{}).t6g2Kinds(f.deps(), nil), t6g2KindExternalIDBackfill)
		h := t6g2NewHost(t6g2KindExternalIDBackfill, tasks.ModeLive, &t6g2RunWriter{})
		h.yield = yield
		if ckpt != "" {
			if err := h.SetCheckpoint("", json.RawMessage(ckpt)); err != nil {
				t.Fatal(err)
			}
		}
		_, err := t6g2RunKind(t, def, tasks.NewRC(h), `{}`)
		return f, h, err
	}

	t.Run("a fresh walk runs every item and checkpoints the position", func(t *testing.T) {
		f, h, err := run(t, "", false)
		if err != nil {
			t.Fatal(err)
		}
		if got := f.writer.sortedWalked(); !slices.Equal(got, []int{0, 1, 2, 3, 4, 5}) {
			t.Errorf("executed %v, want every item", got)
		}
		if !slices.Equal(f.writer.marks, []int{t6g2WalkItems}) {
			t.Errorf("watermarks = %v", f.writer.marks)
		}
		raw, ok := h.CheckpointJSON("")
		if !ok {
			t.Fatal("run.Checkpoint saved nothing through the run")
		}
		var saved bootBackfillParams
		if err := json.Unmarshal(raw, &saved); err != nil {
			t.Fatal(err)
		}
		if saved.Resume.Cursor != "bat-cave/06.m4b" || saved.Resume.Index != t6g2WalkItems {
			t.Errorf("saved %s, want the position the walk reported", raw)
		}
		if !slices.Equal(f.writer.yieldDue, []bool{false}) {
			t.Errorf("YieldDue = %v, want false while the run is not asked to yield", f.writer.yieldDue)
		}
	})

	t.Run("pool progress uses the walk's absolute numbering", func(t *testing.T) {
		_, h, err := run(t, `{"resume":{"index":4}}`, false)
		if err != nil {
			t.Fatal(err)
		}
		var dones []int64
		for _, e := range h.Progress() {
			if e.Total != t6g2WalkItems {
				t.Errorf("pool progress total = %d, want the walk's %d", e.Total, t6g2WalkItems)
			}
			dones = append(dones, e.Done)
		}
		if !slices.Equal(dones, []int64{5, 6}) {
			t.Errorf("pool progress = %v, want 5 and 6 (the resumed items, numbered from the whole walk)", dones)
		}
	})

	t.Run("a resume past the end reports the walk complete", func(t *testing.T) {
		f, h, err := run(t, `{"resume":{"index":6}}`, false)
		if err != nil {
			t.Fatal(err)
		}
		if got := f.writer.sortedWalked(); len(got) != 0 {
			t.Errorf("executed %v, want nothing", got)
		}
		last := h.reports[len(h.reports)-1]
		if last != (t6g2Report{done: t6g2WalkItems, total: t6g2WalkItems, known: true}) {
			t.Errorf("last progress report = %+v, want 6 of 6", last)
		}
		if !slices.Contains(h.notes, "resumed: all items already complete") {
			t.Errorf("notes = %v, want the all-complete frame", h.notes)
		}
	})

	t.Run("a yield hands the run back in place", func(t *testing.T) {
		f, _, err := run(t, "", true)
		if !errors.Is(err, tasks.ErrContinueInPlace) {
			t.Fatalf("err = %v, want the continue-in-place sentinel", err)
		}
		if !slices.Equal(f.writer.yieldDue, []bool{true}) {
			t.Errorf("YieldDue = %v, want true when the host says to yield", f.writer.yieldDue)
		}
	})
}

// ---------------------------------------------------------------------------
// The pool seam and the refusals.
// ---------------------------------------------------------------------------

func t6g2PoolSubtests(t *testing.T) {
	rc := t6g2NewRC(t6g2KindExternalIDBackfill, tasks.ModeLive, nil)
	boomA, boomB := errors.New("Joker escaped"), errors.New("Penguin escaped")

	t.Run("collect runs every item and returns every error", func(t *testing.T) {
		var mu sync.Mutex
		var ran []int
		err := t6g2NativeItems(rc, tasks.Fixed(2, "test"))(context.Background(), 5, t6g2Loop{Collect: true}, func(_ context.Context, i int) error {
			mu.Lock()
			ran = append(ran, i)
			mu.Unlock()
			switch i {
			case 1:
				return boomA
			case 3:
				return boomB
			}
			return nil
		})
		slices.Sort(ran)
		if !slices.Equal(ran, []int{0, 1, 2, 3, 4}) {
			t.Errorf("ran %v, want every item", ran)
		}
		if !errors.Is(err, boomA) || !errors.Is(err, boomB) {
			t.Errorf("err = %v, want both item errors", err)
		}
	})

	t.Run("fail fast stops at the first error and returns it alone", func(t *testing.T) {
		var ran []int
		err := t6g2NativeItems(rc, tasks.Fixed(1, "test"))(context.Background(), 5, t6g2Loop{}, func(_ context.Context, i int) error {
			ran = append(ran, i)
			if i >= 1 {
				return fmt.Errorf("item %d: %w", i, boomA)
			}
			return nil
		})
		if !slices.Equal(ran, []int{0, 1}) {
			t.Errorf("ran %v, want the loop stopped after the first failure", ran)
		}
		if !errors.Is(err, boomA) || !strings.Contains(err.Error(), "item 1") || strings.Contains(err.Error(), "item 2") {
			t.Errorf("err = %v, want exactly the first item's error", err)
		}
	})

	t.Run("a resumed loop starts at the saved item and reports it", func(t *testing.T) {
		var got []int
		var marks []int
		err := t6g2NativeItems(rc, tasks.Fixed(1, "test"))(context.Background(), 5, t6g2Loop{
			ResumeFrom: 3, CheckpointEvery: 1,
			CheckpointState: func(_ context.Context, mark int) error { marks = append(marks, mark); return nil },
		}, func(_ context.Context, i int) error {
			got = append(got, i)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(got, []int{3, 4}) || !slices.Equal(marks, []int{4, 5}) {
			t.Errorf("ran %v with marks %v, want items 3 and 4 with absolute marks 4 and 5", got, marks)
		}
	})

	t.Run("t6g2ItemsError passes other errors through", func(t *testing.T) {
		if got := t6g2ItemsError(nil, true); got != nil {
			t.Errorf("nil became %v", got)
		}
		if got := t6g2ItemsError(boomA, true); got != boomA {
			t.Errorf("a plain error became %v", got)
		}
	})
}

func t6g2RefusalSubtests(t *testing.T) {
	kindOf := func(f *t6g2Fakes, deps t6g2Deps, kind tasks.Kind) tasks.Definition {
		return t6g2KindByName(t, (&Plugin{}).t6g2Kinds(deps, nil), kind)
	}

	t.Run("missing dependencies", func(t *testing.T) {
		f := newT6G2Fakes(t)
		for _, tc := range []struct {
			name string
			deps t6g2Deps
			kind tasks.Kind
			want string
		}{
			{"no writer factory", t6g2Deps{Store: f.store}, t6g2KindBookSigSidecarMigrate, "NewWriter is not set"},
			{"a factory with no writer", t6g2Deps{Store: f.store, NewWriter: func(tasks.Writer) t6g2Writer { return nil }}, t6g2KindBookSigSidecarMigrate, "returned no writer"},
			{"no report path", t6g2Deps{Store: f.store, NewWriter: func(tasks.Writer) t6g2Writer { return f.writer }}, t6g2KindBookShapeReport, "ReportPath is not set"},
		} {
			_, err := t6g2RunKind(t, kindOf(f, tc.deps, tc.kind), t6g2NewRC(tc.kind, tasks.ModeLive, nil), `{}`)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("%s: err = %v, want %q", tc.name, err, tc.want)
			}
		}
		if len(f.store.classified)+len(f.writer.got()) != 0 {
			t.Errorf("a refused run reached the store or writer")
		}
	})

	t.Run("build-folder-book-files refuses a negative limit and a store without path lookups", func(t *testing.T) {
		f := newT6G2Fakes(t)
		def := kindOf(f, f.deps(), t6g2KindBuildFolderBookFiles)
		if _, err := t6g2RunKind(t, def, t6g2NewRC(def.Kind(), tasks.ModeLive, nil), `{"limit":-1}`); err == nil || !strings.Contains(err.Error(), "limit must be >= 0") {
			t.Errorf("limit -1: err = %v", err)
		}
		f.store.unsupported[t6g2CapFolderBuild] = true
		if _, err := t6g2RunKind(t, def, t6g2NewRC(def.Kind(), tasks.ModeLive, nil), `{}`); err == nil || !strings.Contains(err.Error(), "ownership check") {
			t.Errorf("no path lookups: err = %v, want the ownership-check refusal", err)
		}
		if len(f.writer.got()) != 0 {
			t.Errorf("a refused run wrote %v", f.writer.got())
		}
	})

	// Every kind that probes a capability refuses, naming the store, before it
	// writes anything.
	for _, tc := range []struct {
		kind   tasks.Kind
		cap    t6g2Capability
		params string
	}{
		{t6g2KindBookAtPathIndexVerify, t6g2CapAtPathVerify, `{}`},
		{t6g2KindBookAtPathIndexBackfill, t6g2CapAtPathRebuild, `{}`},
		{t6g2KindBookAtPathIndexBackfill, t6g2CapAtPathBackfill, `{"if_needed":true}`},
		{t6g2KindBookHashIndexRepair, t6g2CapHashIndex, `{}`},
		{t6g2KindBookFileAcoustIDIndexRepair, t6g2CapAcoustIDRepair, `{}`},
		{t6g2KindBookSigSidecarMigrate, t6g2CapSigMigrate, `{}`},
		{t6g2KindChaptersBackfill, t6g2CapChapters, `{"book_ids":["bat-1"]}`},
	} {
		t.Run(string(tc.kind)+"/"+string(tc.cap), func(t *testing.T) {
			f := newT6G2Fakes(t)
			if tc.kind == t6g2KindChaptersBackfill {
				t6g2ChaptersFixture(t, f, "bat-1")
			}
			f.store.unsupported[tc.cap] = true
			f.store.ids = []string{"bat-1"}
			def := kindOf(f, f.deps(), tc.kind)
			_, err := t6g2RunKind(t, def, t6g2NewRC(tc.kind, tasks.ModeLive, nil), tc.params)
			if err == nil || !strings.Contains(err.Error(), "t6g2FakeStore") {
				t.Fatalf("err = %v, want a refusal naming the store", err)
			}
			if len(f.writer.got()) != 0 {
				t.Errorf("wrote %v on a store without the capability", f.writer.got())
			}
		})
	}
}
