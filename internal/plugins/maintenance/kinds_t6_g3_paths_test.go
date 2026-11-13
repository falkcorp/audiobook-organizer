// file: internal/plugins/maintenance/kinds_t6_g3_paths_test.go
// version: 1.0.1
// guid: d41c7b92-6a35-4e08-9f1d-83b5e2a07c64
// last-edited: 2026-11-13

package maintenance

import (
	"context"
	"encoding/json"
	"log/slog"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/ai"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/covertext"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
	"github.com/falkcorp/audiobook-organizer/internal/versions"
	"github.com/falkcorp/mediacore/tasks"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The tests here drive the two paths of the group's credits backfill and cover
// ops end to end over a real store: the HEAD shell (the def's own Run) and the
// native kind with a writer factory. Fixtures: Batman characters and
// storylines (seedCBCensusFixture, cbSplitFixture, coverOpFixture).

// --- recording over a real store ---

// t6g3CBStore is a Pebble store that records the writes the credits backfill
// makes through it, by method, and can route the credits write elsewhere.
type t6g3CBStore struct {
	*database.PebbleStore
	// mod, when set, takes the credits write (an interleaving edit).
	mod database.BookCreditsModifier

	mu    sync.Mutex
	calls []string
}

func (s *t6g3CBStore) note(c string) {
	s.mu.Lock()
	s.calls = append(s.calls, c)
	s.mu.Unlock()
}

func (s *t6g3CBStore) called() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.calls)
}

func (s *t6g3CBStore) ModifyBookCredits(id string, fn func(*database.Book, *database.BookCreditsEdit) error) (*database.Book, database.BookCreditsEdit, error) {
	s.note("ModifyBookCredits:" + id)
	if s.mod != nil {
		return s.mod.ModifyBookCredits(id, fn)
	}
	return s.PebbleStore.ModifyBookCredits(id, fn)
}

func (s *t6g3CBStore) CreateNarrator(name string) (*database.Narrator, error) {
	s.note("CreateNarrator:" + name)
	return s.PebbleStore.CreateNarrator(name)
}

func (s *t6g3CBStore) DeleteNarrator(id int) error {
	s.note("DeleteNarrator")
	return s.PebbleStore.DeleteNarrator(id)
}

func (s *t6g3CBStore) CreateOperationChange(c *database.OperationChange) error {
	s.note("CreateOperationChange:" + c.ChangeType)
	return s.PebbleStore.CreateOperationChange(c)
}

func (s *t6g3CBStore) ReplaceOpRowsPrefix(opID, prefix string, n int, at func(int) (database.OpRow, error)) error {
	s.note("ReplaceOpRowsPrefix:" + prefix + ":" + string(rune('0'+min(n, 9))))
	return s.PebbleStore.ReplaceOpRowsPrefix(opID, prefix, n, at)
}

// t6g3ClassListsSaved and t6g3ClassListsCleared are the store calls of the
// result's class id lists: SaveClassLists replaces the namespace with rows,
// ClearClassLists replaces it with none.
func t6g3ClassListsCleared() string {
	return "ReplaceOpRowsPrefix:" + database.ClassListRowPrefix + ":0"
}

func t6g3HasCall(calls []string, prefix string) bool {
	return slices.ContainsFunc(calls, func(c string) bool { return strings.HasPrefix(c, prefix) })
}

// t6g3CBDeps is HEAD's dependencies over the recording store, counting the
// authors-cache invalidations.
type t6g3CBDeps struct {
	fakeDeps
	invalidations *atomic.Int32
}

func (d t6g3CBDeps) InvalidateAuthorsCache() { d.invalidations.Add(1) }

func newT6G3CBHead(f *cbFixture, mod database.BookCreditsModifier) (*Plugin, *t6g3CBStore, *atomic.Int32) {
	rec := &t6g3CBStore{PebbleStore: f.s, mod: mod}
	inval := &atomic.Int32{}
	return New(t6g3CBDeps{fakeDeps: fakeDeps{store: rec}, invalidations: inval}), rec, inval
}

// --- the HEAD shell of the credits backfill (maintenance.credits-backfill) ---

// An apply through the def's own Run reaches every write: the book credits, the
// undo journal, the class id lists and the authors-cache invalidation, and the
// result is stored with the lists kept apart.
func TestT6G3_CreditsBackfillHeadShellApplyReachesEveryWrite(t *testing.T) {
	f := newCBFixture(t)
	seedCBCensusFixture(f)
	p, rec, inval := newT6G3CBHead(f, nil)
	rep := &cbReporter{id: cbTestOpID}
	require.NoError(t, p.creditsBackfillDef().Run(context.Background(), json.RawMessage(`{"apply":true}`), rep))

	calls := rec.called()
	assert.Contains(t, calls, "ModifyBookCredits:cbc-join-empty", "the credits write goes through HEAD's store")
	assert.Contains(t, calls, "CreateOperationChange:"+undo.ChangeTypeCreditsBackfill, "the undo journal row")
	assert.True(t, t6g3HasCall(calls, "ReplaceOpRowsPrefix:"+database.ClassListRowPrefix), "the class id lists")
	assert.EqualValues(t, 1, inval.Load(), "an apply that wrote books invalidates the authors cache once")
	stored, ok := rep.result.(*creditsBackfillResult)
	require.True(t, ok, "the result is stored on the reporter: %T", rep.result)
	assert.True(t, stored.ClassLists.Valid())
	assert.Positive(t, stored.BooksToWrite)
	for _, c := range stored.Classes {
		assert.Empty(t, c.BookIDs, "the stored summary keeps no id lists")
	}
	assert.NotEmpty(t, cbBookWriteRows(f), "the journal rows are under the run's operation")
}

// A narrator record made for a creator whose write is then refused is deleted
// again, and both the create and the delete reach HEAD's store.
func TestT6G3_CreditsBackfillHeadShellReachesNarratorCreateAndDelete(t *testing.T) {
	f := cbSplitFixture(t, "z-solo", "a-split")
	p, rec, _ := newT6G3CBHead(f, cbCreatorEdit(t, f, nil))
	rep := &cbReporter{id: "01RUNONE"}
	_ = p.creditsBackfillDef().Run(context.Background(), json.RawMessage(`{"apply":true}`), rep)
	calls := rec.called()
	assert.Contains(t, calls, "CreateNarrator:Renee Montoya")
	assert.Contains(t, calls, "DeleteNarrator", "the unused record is deleted again")
	assert.NotContains(t, cbRecordNames(t, f), "Renee Montoya")
}

// A dry run reads the library and keeps the class lists, and writes no book,
// narrator or journal row and never touches the authors cache.
func TestT6G3_CreditsBackfillHeadShellDryRunWritesNoBook(t *testing.T) {
	f := newCBFixture(t)
	seedCBCensusFixture(f)
	p, rec, inval := newT6G3CBHead(f, nil)
	rep := &cbReporter{id: cbTestOpID}
	require.NoError(t, p.creditsBackfillDef().Run(context.Background(), nil, rep))
	calls := rec.called()
	assert.False(t, t6g3HasCall(calls, "ModifyBookCredits"), "a dry run writes no book: %v", calls)
	assert.False(t, t6g3HasCall(calls, "CreateNarrator"), "%v", calls)
	assert.False(t, t6g3HasCall(calls, "CreateOperationChange"), "%v", calls)
	assert.Zero(t, inval.Load())
	assert.True(t, t6g3HasCall(calls, "ReplaceOpRowsPrefix:"+database.ClassListRowPrefix), "the class lists are kept even in a dry run")
}

// Nothing to write, nothing invalidated.
func TestT6G3_CreditsBackfillHeadShellDoesNotInvalidateWhenNothingIsWritten(t *testing.T) {
	f := newCBFixture(t)
	bruce := f.author("Bruce Wayne")
	edward := f.narrator("Edward Nygma")
	f.book("cbc-clean-only", "No Man's Land", func(b *database.Book) {
		b.AuthorID = intp(bruce)
		b.Narrator = strp("Edward Nygma")
	}, ba("cbc-clean-only", [2]int{bruce, 0}), bn("cbc-clean-only", [2]int{edward, 0}))
	p, rec, inval := newT6G3CBHead(f, nil)
	rep := &cbReporter{id: cbTestOpID}
	require.NoError(t, p.creditsBackfillDef().Run(context.Background(), json.RawMessage(`{"apply":true}`), rep))
	assert.False(t, t6g3HasCall(rec.called(), "ModifyBookCredits"), "%v", rec.called())
	assert.Zero(t, inval.Load(), "no book written: the authors cache is left alone")
}

// A result that cannot be stored takes the class id lists back: the clear goes
// through HEAD's writer, and no list row is left behind.
func TestT6G3_CreditsBackfillHeadShellFailedResultWriteClearsTheClassLists(t *testing.T) {
	f := newCBFixture(t)
	seedCBCensusFixture(f)
	p, rec, _ := newT6G3CBHead(f, nil)
	rep := &failingResultReporter{cbReporter{id: cbTestOpID}}
	err := p.creditsBackfillDef().Run(context.Background(), json.RawMessage(`{"apply":true}`), rep)
	require.Error(t, err, "a result that was not stored is a failed run")
	assert.Contains(t, rec.called(), t6g3ClassListsCleared())
	for _, id := range opRowIDs(t, f.s, cbTestOpID) {
		assert.False(t, strings.HasPrefix(id, database.ClassListRowPrefix), "left behind: %s", id)
	}
}

// --- the native kind of the credits backfill ---

// t6g3CBRecWriter notes the credits backfill's writes and passes them to the
// writer under it.
type t6g3CBRecWriter struct {
	t6g3Writer
	mu    sync.Mutex
	calls []string
}

func (w *t6g3CBRecWriter) note(c string) {
	w.mu.Lock()
	w.calls = append(w.calls, c)
	w.mu.Unlock()
}

func (w *t6g3CBRecWriter) called() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.calls)
}

func (w *t6g3CBRecWriter) ModifyBookCredits(id string, fn func(*database.Book, *database.BookCreditsEdit) error) (*database.Book, database.BookCreditsEdit, error) {
	w.note("ModifyBookCredits:" + id)
	return w.t6g3Writer.ModifyBookCredits(id, fn)
}

func (w *t6g3CBRecWriter) CreateNarrator(name string) (*database.Narrator, error) {
	w.note("CreateNarrator:" + name)
	return w.t6g3Writer.CreateNarrator(name)
}

func (w *t6g3CBRecWriter) DeleteNarrator(id int) error {
	w.note("DeleteNarrator")
	return w.t6g3Writer.DeleteNarrator(id)
}

func (w *t6g3CBRecWriter) CreateOperationChange(c *database.OperationChange) error {
	w.note("CreateOperationChange:" + c.ChangeType)
	return w.t6g3Writer.CreateOperationChange(c)
}

func (w *t6g3CBRecWriter) InvalidateAuthorsCache() {
	w.note("InvalidateAuthorsCache")
	w.t6g3Writer.InvalidateAuthorsCache()
}

func (w *t6g3CBRecWriter) ReplaceOpRowsPrefix(opID, prefix string, n int, at func(int) (database.OpRow, error)) error {
	w.note("ReplaceOpRowsPrefix:" + prefix)
	return w.t6g3Writer.ReplaceOpRowsPrefix(opID, prefix, n, at)
}

func (w *t6g3CBRecWriter) SaveClassLists(opID string, lists map[string][]string) (*database.ClassListSummary, error) {
	w.note("SaveClassLists")
	return w.t6g3Writer.SaveClassLists(opID, lists)
}

func (w *t6g3CBRecWriter) ClearClassLists(opID string) error {
	w.note("ClearClassLists")
	return w.t6g3Writer.ClearClassLists(opID)
}

// t6g3CBNative is the credits backfill's owner-declared dependencies over a real
// store: HEAD's read adapter, and a writer factory that hands out recording
// writers over HEAD's write adapter.
type t6g3CBNative struct {
	deps    t6g3Deps
	rec     *t6g3CBStore
	inval   *atomic.Int32
	mu      sync.Mutex
	handed  []tasks.Writer
	writers []*t6g3CBRecWriter
}

func newT6G3CBNative(f *cbFixture, key tasks.Key) *t6g3CBNative {
	p, rec, inval := newT6G3CBHead(f, nil)
	n := &t6g3CBNative{rec: rec, inval: inval}
	n.deps = t6g3Deps{
		Store:        headT6g3Store{OpsStore: rec, p: p},
		StandDownKey: key,
		NewWriter: func(w tasks.Writer) t6g3Writer {
			rw := &t6g3CBRecWriter{t6g3Writer: headT6g3Writer{p: p, store: rec}}
			n.mu.Lock()
			n.handed = append(n.handed, w)
			n.writers = append(n.writers, rw)
			n.mu.Unlock()
			return rw
		},
	}
	return n
}

func (n *t6g3CBNative) calls() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	var out []string
	for _, w := range n.writers {
		out = append(out, w.called()...)
	}
	return out
}

func (n *t6g3CBNative) kind() tasks.Definition {
	return (&Plugin{}).creditsBackfillKind(n.deps, nil)
}

// A live run hands the factory this run's rc.Writer(), takes the owner's stand-
// down key, and every write reaches the recording writer (and, through it, the
// store); the preview that follows writes nothing and holds nothing.
func TestT6G3_CreditsBackfillNativeLiveWritesThroughTheWriter(t *testing.T) {
	f := newCBFixture(t)
	seedCBCensusFixture(f)
	n := newT6G3CBNative(f, t6g3ScanKey)
	gate := &t6g3Gate{id: "credits-live"}
	host, rc := newT6G3Host(creditsBackfillKind, tasks.ModeLive, gate)
	host.SetCPUSlots(3)
	raw, err := t6g3Run(t, n.kind(), rc, "")
	require.NoError(t, err)

	require.Len(t, n.handed, 1, "NewWriter is called once per run")
	assert.True(t, n.handed[0] == tasks.Writer(gate), "NewWriter is handed this run's rc.Writer()")
	calls := n.calls()
	assert.Contains(t, calls, "ModifyBookCredits:cbc-join-empty")
	assert.Contains(t, calls, "CreateOperationChange:"+undo.ChangeTypeCreditsBackfill)
	assert.Contains(t, calls, "SaveClassLists")
	assert.Contains(t, calls, "InvalidateAuthorsCache", "an apply that wrote books invalidates the authors cache")
	assert.Equal(t, []tasks.Key{t6g3ScanKey}, host.standDowns, "the apply holds the owner's scan key")
	var res creditsBackfillResult
	require.NoError(t, json.Unmarshal(raw, &res))
	assert.True(t, res.ClassLists.Valid(), "the result carries the class list summary")
	assert.NotEmpty(t, cbBookWriteRows(f), "the journal rows are under the run's id")
	// Rule R4: HEAD ran every loop on NumCPU workers; the native pool is the
	// kind's CPU parallelism, which takes its slots from the host.
	acquires, maxInUse := host.CPUStats()
	assert.Positive(t, acquires, "the item loops run on the CPU parallelism")
	assert.LessOrEqual(t, maxInUse, 3, "and never beyond the host's slots")
}

func TestT6G3_CreditsBackfillNativePreviewWritesAndHoldsNothing(t *testing.T) {
	f := newCBFixture(t)
	seedCBCensusFixture(f)
	n := newT6G3CBNative(f, t6g3ScanKey)
	host, rc := newT6G3Host(creditsBackfillKind, tasks.ModePreview, &t6g3Gate{})
	_, err := t6g3Run(t, n.kind(), rc, "")
	require.NoError(t, err)
	calls := n.calls()
	assert.False(t, t6g3HasCall(calls, "ModifyBookCredits"), "%v", calls)
	assert.False(t, t6g3HasCall(calls, "CreateNarrator"), "%v", calls)
	assert.False(t, t6g3HasCall(calls, "CreateOperationChange"), "%v", calls)
	assert.NotContains(t, calls, "InvalidateAuthorsCache")
	assert.Empty(t, host.standDowns, "a preview holds no stand-down")
}

// A live apply without the owner's scan key is refused before any write: the
// stand-down key is the owner's to declare (F5-02's pattern), not a constant.
func TestT6G3_CreditsBackfillNativeRefusesALiveApplyWithoutAStandDownKey(t *testing.T) {
	f := newCBFixture(t)
	seedCBCensusFixture(f)
	n := newT6G3CBNative(f, "")
	host, rc := newT6G3Host(creditsBackfillKind, tasks.ModeLive, &t6g3Gate{})
	_, err := t6g3Run(t, n.kind(), rc, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "StandDownKey")
	assert.Empty(t, host.standDowns)
	assert.False(t, t6g3HasCall(n.calls(), "ModifyBookCredits"), "no book is written without the stand-down")
	assert.False(t, t6g3HasCall(n.rec.called(), "ModifyBookCredits"))
}

// A resumed run restores HEAD's checkpoint (the params JSON HEAD's reporter
// stored): the cohort limit comes back, so one book is written, not nine.
func TestT6G3_CreditsBackfillNativeRestoresHEADCheckpoint(t *testing.T) {
	f := newCBFixture(t)
	seedCBCensusFixture(f)
	n := newT6G3CBNative(f, t6g3ScanKey)
	host, rc := newT6G3Host(creditsBackfillKind, tasks.ModeLive, &t6g3Gate{})
	require.NoError(t, host.SetCheckpoint("", json.RawMessage(`{"apply":true,"limit":1,"attempt":1}`)))
	require.True(t, rc.Resumed())
	_, err := t6g3Run(t, n.kind(), rc, "")
	require.NoError(t, err)
	written := 0
	for _, c := range n.calls() {
		if strings.HasPrefix(c, "ModifyBookCredits:") {
			written++
		}
	}
	assert.Equal(t, 1, written, "the restored limit bounds the cohort: %v", n.calls())
}

// The run's own errgroups are bounded by NumCPU in both shells, as HEAD's shell
// set it; the native kind used to leave it unset.
func TestT6G3_CreditsBackfillCoreEnvBoundsItsGroupsByNumCPU(t *testing.T) {
	env := t6g3CreditsCoreEnv(t6g3Base{}, nil, nativeCBEngine{})
	assert.Equal(t, runtime.NumCPU(), env.Workers)
}

// The credits backfill's effects name every resource its writer touches: the
// books, the author joins a credits write replaces, the narrator records and
// the operation rows; and what it reads.
func TestT6G3_CreditsBackfillEffects(t *testing.T) {
	d := t6g3KindByName(t, (&Plugin{}).t6g3Kinds(t6g3Deps{}, nil), "maintenance.credits-backfill")
	e := tasks.CommonOf(d).Effects
	assert.Equal(t, []string{"authors", "books", "narrators", "operations"}, t6g3WriteKinds(e))
	var reads []string
	for _, r := range e.ReadSet() {
		reads = append(reads, r.Kind)
	}
	slices.Sort(reads)
	assert.Equal(t, []string{"authors", "books", "series"}, reads)
}

// --- the cover ops' pools ---

// t6g3GateReader is a vision reader that holds each read until `barrier` reads
// are in flight (or a short wait), so the most that ever ran at once is what
// the pool allowed.
type t6g3GateReader struct {
	capacity int
	barrier  int

	mu       sync.Mutex
	inflight int
	max      int
	ready    chan struct{}
	once     sync.Once
}

func newT6G3GateReader(capacity, barrier int) *t6g3GateReader {
	return &t6g3GateReader{capacity: capacity, barrier: barrier, ready: make(chan struct{})}
}

func (g *t6g3GateReader) Capacity() int { return g.capacity }

func (g *t6g3GateReader) ReadCoverText(_ context.Context, _ []byte, _ string) (*ai.CoverTextResult, error) {
	g.mu.Lock()
	g.inflight++
	g.max = max(g.max, g.inflight)
	if g.inflight >= g.barrier {
		g.once.Do(func() { close(g.ready) })
	}
	g.mu.Unlock()
	select {
	case <-g.ready:
	case <-time.After(300 * time.Millisecond):
	}
	g.mu.Lock()
	g.inflight--
	g.mu.Unlock()
	return &ai.CoverTextResult{Text: &covertext.Text{Title: "Read"}, Raw: `{"title":"Read"}`, Model: "vision-model", EndpointID: "pool-a"}, nil
}

func (g *t6g3GateReader) maxInFlight() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.max
}

// The native cover-text read sizes its pool like HEAD: the workers param, else
// the vision pool's capacity when live, as the pool itself (no semaphore inside
// an item for a read to queue on, where the item timeout would already be
// running). Two images, capacity two: both reads are in flight together; with
// workers=1 never more than one.
func TestT6G3_CoverTextReadNativePoolIsTheWorkersParamOrTheCapacity(t *testing.T) {
	for _, tc := range []struct {
		name   string
		params string
		want   int
	}{
		{"capacity", "", 2},
		{"workers param", `{"workers":1}`, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, deps, ids := coverOpFixture(t)
			reader := newT6G3GateReader(2, 2)
			orig := newCoverTextReader
			newCoverTextReader = func() coverTextReader { return reader }
			t.Cleanup(func() { newCoverTextReader = orig })
			p := New(deps)
			kd := t6g3Deps{
				Store: headT6g3Store{OpsStore: st, p: p},
				NewWriter: func(tasks.Writer) t6g3Writer {
					return headT6g3Writer{p: p, store: st}
				},
			}
			d := t6g3KindByName(t, (&Plugin{}).t6g3Kinds(kd, nil), "maintenance.cover-text-read")
			params := tc.params
			if params == "" {
				params = `{}`
			}
			_, rc := newT6G3Host(d.Kind(), tasks.ModeLive, &t6g3Gate{})
			_, err := t6g3Run(t, d, rc, params)
			require.NoError(t, err)
			assert.Equal(t, tc.want, reader.maxInFlight())
			for _, id := range ids {
				got, err := covertext.ForBook(st, id)
				require.NoError(t, err)
				require.Len(t, got, 1)
				require.NotNil(t, got[0].Record, "every image is read and stored through the writer")
			}
		})
	}
}

// --- the shells that refuse a missing store ---

// A native shell whose owner declared no store refuses the run before the core
// reads one (it used to dereference nil).
func TestT6G3_NativeShellsRefuseAMissingStore(t *testing.T) {
	f := newT6G3Fake(t)
	deps := f.deps()
	deps.Store = nil
	for _, headID := range []string{
		"maintenance.temp-file-cleanup",
		"maintenance.transcode-temp-cleanup",
		"maintenance.cleanup-activity-log",
		"maintenance.cleanup-old-backups",
	} {
		d := t6g3KindByName(t, (&Plugin{}).t6g3Kinds(deps, nil), headID)
		_, rc := newT6G3Host(d.Kind(), tasks.ModeLive, &t6g3Gate{})
		var err error
		require.NotPanics(t, func() { _, err = t6g3Run(t, d, rc, "") }, headID)
		require.Error(t, err, headID)
		assert.Contains(t, err.Error(), "t6g3Deps.Store is not set", headID)
		assert.Empty(t, f.writer.called(), headID)
	}
}

// --- the trash cleanup's result is stored where HEAD stored it ---

// orderReporter records the order of the result write and the log lines.
type orderReporter struct {
	levelReporter
	events []string
}

func (r *orderReporter) SetResult(v any) error {
	r.events = append(r.events, "result")
	return r.levelReporter.SetResult(v)
}

func (r *orderReporter) Log(l slog.Level, msg string, attrs ...slog.Attr) error {
	r.events = append(r.events, "log")
	return r.levelReporter.Log(l, msg, attrs...)
}

// HEAD stored the result as soon as the payload was built, before the first
// log line, so a run that dies while logging still has its result.
func TestT6G3_TrashCleanupHeadShellStoresTheResultBeforeItLogs(t *testing.T) {
	p := &Plugin{deps: trashCleanupDeps{res: versions.CleanupResult{Purged: 1}}}
	rep := &orderReporter{}
	require.NoError(t, p.runTrashCleanup(context.Background(), nil, rep))
	require.NotEmpty(t, rep.events)
	assert.Equal(t, "result", rep.events[0], "the result is written first: %v", rep.events)
	assert.Contains(t, rep.events, "log")
}

// HEAD's cover-text read stamps the run's liveness clock before each model
// read (the watchdog's clock): one stamp per distinct image, through the
// def's own Run over a reporter that counts them.
func TestT6G3_CoverTextReadHeadShellStampsLivenessBeforeEachRead(t *testing.T) {
	_, deps, _ := coverOpFixture(t)
	reader := newT6G3GateReader(2, 1)
	orig := newCoverTextReader
	newCoverTextReader = func() coverTextReader { return reader }
	t.Cleanup(func() { newCoverTextReader = orig })
	rep := &touchReporter{}
	require.NoError(t, New(deps).runCoverTextRead(context.Background(), json.RawMessage(`{"dry_run":false}`), rep))
	assert.GreaterOrEqual(t, rep.touches.Load(), int64(2), "one liveness stamp per image read")
}
