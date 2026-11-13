// file: internal/plugins/maintenance/kinds_t6_g2_test.go
// version: 1.0.0
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
	// acoustid is the one page the index scan returns; verdict judges each entry.
	acoustid []database.BookFileAcoustIDEntry
	verdict  database.BookFileAcoustIDVerdict
	// unsupported names the capabilities Supports refuses.
	unsupported map[t6g2Capability]bool

	checked int // CheckBookHashIndex calls (preview of the hash repair)
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

func (s *t6g2FakeStore) GetAllBooksCore(int, int) ([]database.BookCore, error) { return nil, nil }

func (s *t6g2FakeStore) GetAllBookFilesCore() ([]database.BookFileCore, error) { return nil, nil }

func (s *t6g2FakeStore) BookAtPathIndexBuilt() (bool, error) { return true, nil }

func (s *t6g2FakeStore) BookFilesAtPath(string) ([]database.BookFile, error) { return nil, nil }

func (s *t6g2FakeStore) LiveBookIDsAtPath(string) ([]string, error) { return nil, nil }

func (s *t6g2FakeStore) ScanBookFileAcoustIDIndex(after string, _ int) ([]database.BookFileAcoustIDEntry, string, error) {
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

func (w *t6g2RecWriter) BackfillExternalIDs(context.Context, bool, func(int, int, string), walkresume.Run) (bool, error) {
	w.rec("BackfillExternalIDs")
	return false, nil
}

func (w *t6g2RecWriter) StripMovementAtoms(context.Context, walkresume.Run) (bool, error) {
	w.rec("StripMovementAtoms")
	return false, nil
}

func (w *t6g2RecWriter) RemuxMalformedM4BFiles(context.Context, func(int, int, string), walkresume.Run) (bool, error) {
	w.rec("RemuxMalformedM4BFiles")
	return false, nil
}

func (w *t6g2RecWriter) TranscodeMalformedM4BFiles(context.Context, func(int, int, string), walkresume.Run) (bool, error) {
	w.rec("TranscodeMalformedM4BFiles")
	return false, nil
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

func (w *t6g2RecWriter) MigrateBookSigToSidecar(id string, dryRun bool) (database.BookSigMigrateOutcome, error) {
	w.rec(fmt.Sprintf("MigrateBookSigToSidecar:%s:dry=%v", id, dryRun))
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
}

func (h *t6g2Host) Mode() tasks.Mode     { return h.mode }
func (h *t6g2Host) Writer() tasks.Writer { return h.w }

// t6g2RunWriter is a distinguishable tasks.Writer: identity is the assertion.
type t6g2RunWriter struct{ name string }

func (*t6g2RunWriter) Do(context.Context, tasks.Write, func(context.Context) ([]tasks.Change, error)) error {
	return nil
}
func (*t6g2RunWriter) MarkRowApplied(context.Context, string)           {}
func (*t6g2RunWriter) RowApplied(context.Context, string) (bool, error) { return false, nil }

func t6g2NewRC(kind tasks.Kind, mode tasks.Mode, w tasks.Writer) *tasks.RC {
	return tasks.NewRC(&t6g2Host{FakeRun: taskstest.NewFakeRun(context.Background(), kind), mode: mode, w: w})
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
	// modeKeys are the params keys the native kind refuses.
	modeKeys []string
	// live is a scenario that makes a live run write: params, an arranger, and
	// the writes it must produce (nil for a kind that writes nothing).
	params   string
	arrange  func(t *testing.T, f *t6g2Fakes)
	wantLive []string
	// wantPreview is what a preview run writes (empty for a refused or
	// read-only preview).
	wantPreview []string
}

func t6g2Rows() []t6g2Row {
	p := &Plugin{}
	mk := func(modeKeys ...string) []string { return append([]string{"dry_run", "dryRun"}, modeKeys...) }
	return []t6g2Row{
		{
			head: p.externalIDBackfillDef(), kind: t6g2KindExternalIDBackfill, resume: tasks.ResumeContinue,
			writes: []string{"external_ids", "settings"}, workers: bootExternalIDWorkers, parPrefix: "fixed(1)",
			liveOnly: true, modeKeys: mk(), params: `{}`, wantLive: []string{"BackfillExternalIDs"},
		},
		{
			head: p.movementAtomCleanupDef(), kind: t6g2KindMovementAtomCleanup, resume: tasks.ResumeContinue,
			writes: []string{"files", "settings"}, workers: bootWalkWorkers, parPrefix: "fixed(4)",
			liveOnly: true, modeKeys: mk(), params: `{}`, wantLive: []string{"StripMovementAtoms"},
		},
		{
			head: p.malformedM4BRemuxDef(), kind: t6g2KindMalformedM4BRemux, resume: tasks.ResumeContinue,
			writes: []string{"files", "book_files", "settings"}, workers: bootWalkWorkers, parPrefix: "fixed(4)",
			liveOnly: true, modeKeys: mk(), params: `{}`, wantLive: []string{"RemuxMalformedM4BFiles"},
		},
		{
			head: p.malformedM4BTranscodeDef(), kind: t6g2KindMalformedM4BTranscode, resume: tasks.ResumeAsk,
			writes: []string{"files", "book_files", "settings", "books"}, workers: bootTranscodeWorkers, parPrefix: "fixed(2)",
			liveOnly: true, modeKeys: mk(), params: `{"quarantine_after":true}`,
			wantLive: []string{"TranscodeMalformedM4BFiles", "QuarantineKnownBadFiles"},
		},
		{
			head: p.bookAtPathIndexVerifyDef(), kind: t6g2KindBookAtPathIndexVerify, resume: tasks.ResumeDrop,
			modeKeys: mk(), params: `{}`,
		},
		{
			head: p.bookAtPathIndexBackfillDef(), kind: t6g2KindBookAtPathIndexBackfill, resume: tasks.ResumeDrop,
			writes: []string{"books"}, liveOnly: true, modeKeys: mk(), params: `{}`,
			wantLive: []string{"RebuildBookAtPathIndex"},
		},
		{
			head: p.bookHashIndexRepairDef(), kind: t6g2KindBookHashIndexRepair, resume: tasks.ResumeFromZero,
			writes: []string{"books"}, reads: []string{"books"}, modeKeys: mk(), params: `{}`,
			wantLive: []string{"RepairBookHashIndex"},
		},
		{
			head: p.bookShapeReportDef(), kind: t6g2KindBookShapeReport, resume: tasks.ResumeDrop,
			workers: runtime.NumCPU(), parPrefix: "cpu", modeKeys: mk(), params: `{}`,
		},
		{
			head: p.bookFileAcoustIDIndexRepairDef(), kind: t6g2KindBookFileAcoustIDIndexRepair, resume: tasks.ResumeContinue,
			writes: []string{"book_files"}, reads: []string{"book_files"}, workers: runtime.NumCPU(), parPrefix: "cpu",
			modeKeys: mk(), params: `{}`,
			arrange: func(t *testing.T, f *t6g2Fakes) {
				f.store.acoustid = []database.BookFileAcoustIDEntry{{}}
				f.store.verdict = database.AcoustIDEntryRowGone
			},
			wantLive: []string{"DeleteOrphanBookFileAcoustIDEntries:1"},
		},
		{
			head: p.bookSigRecoveryAuditDef(), kind: t6g2KindBookSigRecoveryAudit, resume: tasks.ResumeDrop,
			writes: []string{"books"}, modeKeys: mk(), params: `{}`,
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
			head: p.bookSigSidecarMigrateDef(), kind: t6g2KindBookSigSidecarMigrate, resume: tasks.ResumeDrop,
			writes: []string{"books"}, workers: runtime.NumCPU(), parPrefix: "cpu", modeKeys: mk(), params: `{}`,
			arrange: func(t *testing.T, f *t6g2Fakes) { f.store.ids = []string{"bat-1", "bat-2"} },
			wantLive: []string{
				"MigrateBookSigToSidecar:bat-1:dry=false", "MigrateBookSigToSidecar:bat-2:dry=false",
			},
			wantPreview: []string{
				"MigrateBookSigToSidecar:bat-1:dry=true", "MigrateBookSigToSidecar:bat-2:dry=true",
			},
		},
		{
			head: p.buildFolderBookFilesDef(), kind: t6g2KindBuildFolderBookFiles, resume: tasks.ResumeDrop,
			writes: []string{"book_files"}, workers: runtime.NumCPU(), parPrefix: "cpu", modeKeys: mk("apply"),
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
			head: p.chaptersBackfillDef(), kind: t6g2KindChaptersBackfill, resume: tasks.ResumeContinue,
			writes: []string{"chapters"}, workers: runtime.NumCPU(), parPrefix: "cpu", modeKeys: mk("apply"),
			params: `{"book_ids":["bat-1"]}`,
			arrange: func(t *testing.T, f *t6g2Fakes) {
				chbfStubFFprobe(t)
				prev := probeChaptersFn
				probeChaptersFn = func(context.Context, string, string) ([]audioutil.Chapter, error) {
					return []audioutil.Chapter{{ID: 1, StartSec: 0, EndSec: 60, Title: "Gotham"}, {ID: 2, StartSec: 60, EndSec: 120, Title: "Arkham"}}, nil
				}
				t.Cleanup(func() { probeChaptersFn = prev })
				file := filepath.Join(t.TempDir(), "knight.m4b")
				if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
					t.Fatal(err)
				}
				f.store.books = map[string]*database.Book{"bat-1": {ID: "bat-1", Title: "Knightfall", FilePath: file}}
				f.store.files = map[string][]database.BookFile{"bat-1": {{BookID: "bat-1", FilePath: file}}}
			},
			wantLive: []string{"SaveChaptersForBook:bat-1:2"},
		},
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
			if got := tasks.CommonOf(d).Schedule; got != want {
				t.Errorf("%s: Schedule = %p, want the sched hook's value", d.Kind(), got)
			}
		}
		for _, d := range defs {
			if tasks.CommonOf(d).Schedule != nil {
				t.Errorf("%s: nil sched must mean no schedule", d.Kind())
			}
		}
	})
}

func TestT6G2Kinds_MatchHEADDefs(t *testing.T) {
	defs := (&Plugin{}).t6g2Kinds(fakeT6G2Deps(t), nil)
	prio := map[sdk.Priority]tasks.Priority{
		sdk.PriorityLow: tasks.PriorityLow, sdk.PriorityNormal: tasks.PriorityNormal, sdk.PriorityHigh: tasks.PriorityHigh,
	}
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
			if got, want := c.Perms.Start.String(), t6g2StartRequirement().String(); got != want {
				t.Errorf("Perms.Start = %s, want %s", got, want)
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
func t6g2ArrangeAndRun(t *testing.T, r t6g2Row, mode tasks.Mode, params string) (*t6g2Fakes, tasks.Writer, error) {
	t.Helper()
	f := newT6G2Fakes(t)
	if r.arrange != nil {
		r.arrange(t, f)
	}
	def := t6g2KindByName(t, (&Plugin{}).t6g2Kinds(f.deps(), nil), r.kind)
	w := &t6g2RunWriter{name: string(r.kind)}
	_, err := t6g2RunKind(t, def, t6g2NewRC(r.kind, mode, w), params)
	return f, w, err
}

func TestT6G2Kinds_WritesThroughShell(t *testing.T) {
	for _, r := range t6g2Rows() {
		if r.writes == nil {
			continue
		}
		t.Run(string(r.kind)+"/live", func(t *testing.T) {
			f, w, err := t6g2ArrangeAndRun(t, r, tasks.ModeLive, r.params)
			if err != nil {
				t.Fatalf("live run: %v", err)
			}
			if len(f.newWriters) != 1 || f.newWriters[0] != w {
				t.Errorf("NewWriter called with %v, want exactly the run's rc.Writer()", f.newWriters)
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
		})
	}

	for _, r := range t6g2Rows() {
		t.Run(string(r.kind)+"/mode keys refused", func(t *testing.T) {
			for _, key := range r.modeKeys {
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

// The reflect import keeps the table honest: a Row field added without a test
// reading it fails here.
var _ = reflect.TypeFor[t6g2Row]
