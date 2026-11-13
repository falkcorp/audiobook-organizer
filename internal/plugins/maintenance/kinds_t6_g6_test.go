// file: internal/plugins/maintenance/kinds_t6_g6_test.go
// version: 1.1.0
// guid: 5e2b8f4a-91c6-4d03-b7a5-2c8e60f1d9a7
// last-edited: 2026-11-13

package maintenance

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
	"github.com/falkcorp/mediacore/tasks"
	"github.com/falkcorp/mediacore/tasks/taskstest"
)

// t6g6Recorder is the writer a test hands the kinds: it records the calls that
// reach it. Every method a test does not exercise panics (the nil embedded
// interface), so an unexpected write fails loudly.
type t6g6Recorder struct {
	t6g6Writer

	mu    sync.Mutex
	calls []string
	// books lets ModifyBook run the caller's fn on a copy, so the test sees the
	// status the repair would have written.
	books map[string]*database.Book
	// backing, when set, is the real store the rbf apply reads its own writes
	// back from.
	backing rbfWriter
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

func (r *t6g6Recorder) CompactActivityEntries(context.Context, time.Time, database.CompactProgress) (database.CompactResult, error) {
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

func (r *t6g6Recorder) RunITunesHeal(context.Context, json.RawMessage) error {
	r.rec("RunITunesHeal")
	return nil
}

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

func (r *t6g6Recorder) RepairMergedUserState(_ context.Context, o merge.UserStateRepairOptions) (*merge.UserStateRepairReport, error) {
	if o.Apply {
		r.rec("RepairMergedUserState apply")
	} else {
		r.rec("RepairMergedUserState report")
	}
	return &merge.UserStateRepairReport{Apply: o.Apply, PendingOnly: o.PendingOnly}, nil
}

func (r *t6g6Recorder) ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error) {
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

// Unwrap lets database.AsCapability reach the real store's retired-row reads.
func (r t6g6RBFStore) Unwrap() database.Store { return r.s }

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

// gateWriter6 is a distinguishable tasks.Writer: identity is the assertion.
type gateWriter6 struct{ name string }

func (*gateWriter6) Do(context.Context, tasks.Write, func(context.Context) ([]tasks.Change, error)) error {
	return nil
}
func (*gateWriter6) MarkRowApplied(context.Context, string)           {}
func (*gateWriter6) RowApplied(context.Context, string) (bool, error) { return false, nil }

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

func TestT6G6Kinds_MatchHEADDefs(t *testing.T) {
	p := &Plugin{}
	defs := p.t6g6Kinds(fakeT6G6Deps(t), nil)
	require.Len(t, defs, 14)

	type row struct {
		head     sdk.OperationDef
		par      tasks.Parallelism
		wantPar  string
		readOnly bool
	}
	rows := []row{
		{head: p.nightlyCompactActivityLogDef(), par: nightlyCompactActivityLogParallelism(), wantPar: "sequential: "},
		{head: p.opChangeBookIndexRebuildDef(), par: opChangeBookIndexParallelism(), wantPar: "sequential: "},
		{head: p.optimizeDef(), par: optimizeParallelism(), wantPar: "sequential: ", readOnly: true},
		{head: p.orphanBookFilesCleanupDef(), par: orphanBookFilesCleanupParallelism(), wantPar: "sequential: ", readOnly: true},
		{head: p.orphanBookFilesRepointPlanDef(), par: orphanBookFilesRepointPlanParallelism(), wantPar: "cpu", readOnly: true},
		{head: p.probeDirectoryBooksDef(), par: probeDirectoryBooksParallelism(), wantPar: "cpu"},
		{head: p.recompactActivityDigestsDef(), par: recompactActivityDigestsParallelism(), wantPar: "sequential: "},
		{head: p.itunesHealDef(), par: itunesHealParallelism(), wantPar: "sequential: "},
		{head: p.recoverMissingFilesDef(), par: recoverMissingFilesParallelism(), wantPar: "fixed(24): "},
		{head: p.regroupShatteredAIDef(), par: regroupShatteredAIParallelism(), wantPar: "cpu"},
		{head: p.relinkUnlinkedBooksDef(), par: relinkUnlinkedBooksParallelism(), wantPar: "cpu"},
		{head: p.repairMergedUserStateDef(), par: repairMergedUserStateParallelism(), wantPar: "sequential: "},
		{head: p.repairTranscribeStatusDef(), par: repairTranscribeStatusParallelism(), wantPar: "fixed(8): "},
		{head: p.repointBookFileRowsDef(), par: rbfParallelism(), wantPar: "sequential: "},
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
		// The kernel refuses a Timeout over tasks.MaxTimeout, so a def with a
		// longer one (library-optimize's 36h) runs capped natively.
		wantTimeout := min(r.head.Timeout, tasks.MaxTimeout)
		require.Equal(t, wantTimeout, c.Timeout, "%s timeout", id)
		require.Equal(t, r.head.ProgressTimeout, c.ProgressTimeout, "%s progress timeout", id)
		require.Equal(t, tasks.Key("books."+r.head.ConcurrencyKey), c.ConcurrencyKey, "%s concurrency key", id)

		// R5: Restart and Requeue are the two policies the brief maps apart.
		var wantResume tasks.ResumePolicy
		switch r.head.ResumePolicy {
		case sdk.ResumeRestart:
			wantResume = tasks.ResumeContinue
		case sdk.ResumeRequeue:
			wantResume = tasks.ResumeFromZero
		case sdk.ResumeDrop:
			wantResume = tasks.ResumeDrop
		case sdk.ResumeAsk:
			wantResume = tasks.ResumeAsk
		default:
			t.Fatalf("%s: HEAD resume policy %v has no mapping", id, r.head.ResumePolicy)
		}
		require.Equal(t, wantResume, c.Resume, "%s resume", id)

		if r.readOnly {
			require.True(t, c.Effects.IsReadOnly(), "%s must be declared ReadOnly", id)
		} else {
			require.False(t, c.Effects.IsReadOnly(), "%s writes", id)
			require.NotEmpty(t, c.Effects.WriteSet(), "%s write set", id)
		}

		ps := r.par.String()
		require.True(t, strings.HasPrefix(ps, r.wantPar), "%s parallelism %q, want prefix %q", id, ps, r.wantPar)
		require.Contains(t, c.Ready, t6g6SignalMemDB, "%s readiness", id)
		require.NotEmpty(t, c.Perms.Start, "%s start permission", id)
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
			live: []string{"RunITunesHeal"},
		},
		{
			kind: repairMergedUserStateKind,
			live: []string{"RepairMergedUserState apply"}, preview: []string{"RepairMergedUserState report"},
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
	gate := &gateWriter6{name: "gotham"}

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
	gate := &gateWriter6{name: "gotham"}
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
	gate := &gateWriter6{name: "gotham"}

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
