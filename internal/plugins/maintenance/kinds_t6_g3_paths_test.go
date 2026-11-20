// file: internal/plugins/maintenance/kinds_t6_g3_paths_test.go
// version: 1.0.8
// guid: d41c7b92-6a35-4e08-9f1d-83b5e2a07c64
// last-edited: 2026-11-15

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
	// failClear makes the class lists' clear (a replace with no rows) fail.
	failClear error
	// failMark makes the judged-marker write fail.
	failMark error
	// onGetBook, set before the run starts, is called on every book read (a
	// test stops the run's context from inside its work).
	onGetBook func()

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

func (s *t6g3CBStore) GetBookByID(id string) (*database.Book, error) {
	if s.onGetBook != nil {
		s.onGetBook()
	}
	return s.PebbleStore.GetBookByID(id)
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
	if n == 0 && prefix == database.ClassListRowPrefix && s.failClear != nil {
		return s.failClear
	}
	if prefix == cbSwept && s.failMark != nil {
		return s.failMark
	}
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
	return New(t6g3CBDeps{store: rec, invalidations: inval}), rec, inval
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
		b.AuthorID = new(bruce)
		b.Narrator = new("Edward Nygma")
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
	*t6g3ForwardSpy
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
	return w.t6g3ForwardSpy.ModifyBookCredits(id, fn)
}

func (w *t6g3CBRecWriter) CreateNarrator(name string) (*database.Narrator, error) {
	w.note("CreateNarrator:" + name)
	return w.t6g3ForwardSpy.CreateNarrator(name)
}

func (w *t6g3CBRecWriter) DeleteNarrator(id int) error {
	w.note("DeleteNarrator")
	return w.t6g3ForwardSpy.DeleteNarrator(id)
}

func (w *t6g3CBRecWriter) CreateOperationChange(c *database.OperationChange) error {
	w.note("CreateOperationChange:" + c.ChangeType)
	return w.t6g3ForwardSpy.CreateOperationChange(c)
}

func (w *t6g3CBRecWriter) InvalidateAuthorsCache() {
	w.note("InvalidateAuthorsCache")
	w.t6g3ForwardSpy.InvalidateAuthorsCache()
}

func (w *t6g3CBRecWriter) ReplaceOpRowsPrefix(opID, prefix string, n int, at func(int) (database.OpRow, error)) error {
	w.note("ReplaceOpRowsPrefix:" + prefix)
	return w.t6g3ForwardSpy.ReplaceOpRowsPrefix(opID, prefix, n, at)
}

func (w *t6g3CBRecWriter) SaveClassLists(opID string, lists map[string][]string) (*database.ClassListSummary, error) {
	w.note("SaveClassLists")
	return w.t6g3ForwardSpy.SaveClassLists(opID, lists)
}

func (w *t6g3CBRecWriter) ClearClassLists(opID string) error {
	w.note("ClearClassLists")
	return w.t6g3ForwardSpy.ClearClassLists(opID)
}

// t6g3CBNative is the credits backfill's owner-declared dependencies over a real
// store: HEAD's read adapter, and a writer factory that hands out recording
// writers over HEAD's write adapter.
type t6g3CBNative struct {
	deps t6g3Deps
	// runs is deps.Runs: the run store holding this run, running.
	runs    tasks.RunStore
	rec     *t6g3CBStore
	inval   *atomic.Int32
	mu      sync.Mutex
	handed  []tasks.Writer
	writers []*t6g3CBRecWriter
}

func newT6G3CBNative(f *cbFixture, key tasks.Key) *t6g3CBNative {
	p, rec, inval := newT6G3CBHead(f, nil)
	n := &t6g3CBNative{rec: rec, inval: inval, runs: runningCBRuns(f.t)}
	n.deps = t6g3Deps{
		Store:        headT6g3Store{OpsStore: rec, p: p},
		StandDownKey: key,
		Runs:         n.runs,
		NewWriter: func(w tasks.Writer) t6g3Writer {
			rw := &t6g3CBRecWriter{t6g3ForwardSpy: newT6G3ForwardSpy(headT6g3Writer{p: p, store: rec})}
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

// assertWithin checks every resource the run's writers wrote against d's WriteSet.
func (n *t6g3CBNative) assertWithin(t *testing.T, d tasks.Definition) {
	t.Helper()
	n.mu.Lock()
	var all t6g3ResLog
	for _, w := range n.writers {
		all.merge(&w.t6g3ResLog)
	}
	n.mu.Unlock()
	all.assertWithin(t, d)
}

// classListRows counts the class-list rows of the run (the fake run's id).
func (n *t6g3CBNative) classListRows(t *testing.T) int {
	t.Helper()
	c := 0
	for row, err := range n.runs.Rows(t.Context(), cbFakeRunID) {
		require.NoError(t, err)
		if strings.HasPrefix(row.ID, database.ClassListRowPrefix) {
			c++
		}
	}
	return c
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
	host, rc := newT6G3Host(t6g3CreditsBackfillKind, tasks.ModeLive, gate)
	host.SetCPUSlots(3)
	raw, err := t6g3Run(t, n.kind(), rc, "")
	require.NoError(t, err)
	n.assertWithin(t, n.kind())

	require.Len(t, n.handed, 1, "NewWriter is called once per run")
	assert.True(t, n.handed[0] == tasks.Writer(gate), "NewWriter is handed this run's rc.Writer()")
	calls := n.calls()
	assert.Contains(t, calls, "ModifyBookCredits:cbc-join-empty")
	assert.Contains(t, calls, "CreateOperationChange:"+undo.ChangeTypeCreditsBackfill)
	assert.NotContains(t, calls, "SaveClassLists", "the class lists are not a writer call (F5-18d, O10)")
	assert.NotZero(t, n.classListRows(t), "the class lists are the run's own rows")
	assert.Contains(t, calls, "InvalidateAuthorsCache", "an apply that wrote books invalidates the authors cache")
	assert.Equal(t, []tasks.Key{t6g3ScanKey}, host.standDowns, "the apply holds the owner's scan key")
	var res creditsBackfillResult
	require.NoError(t, json.Unmarshal(raw, &res))
	assert.True(t, res.ClassLists.Valid(), "the result carries the class list summary")
	assert.NotEmpty(t, cbBookWriteRowsFor(f, string(rc.RunID())), "the journal rows are under the run's id")
	assert.Empty(t, cbBookWriteRows(f), "and none leak under the reporter-test id")
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
	host, rc := newT6G3Host(t6g3CreditsBackfillKind, tasks.ModePreview, &t6g3Gate{})
	_, err := t6g3Run(t, n.kind(), rc, "")
	require.NoError(t, err)
	calls := n.calls()
	assert.False(t, t6g3HasCall(calls, "ModifyBookCredits"), "%v", calls)
	assert.False(t, t6g3HasCall(calls, "CreateNarrator"), "%v", calls)
	assert.False(t, t6g3HasCall(calls, "CreateOperationChange"), "%v", calls)
	assert.NotContains(t, calls, "InvalidateAuthorsCache")
	assert.Empty(t, host.standDowns, "a preview holds no stand-down")
	assert.NotZero(t, n.classListRows(t), "a preview keeps its class lists as its own rows (the owner reads them)")
}

// A live apply without the owner's scan key is refused before any write: the
// stand-down key is the owner's to declare (F5-02's pattern), not a constant.
func TestT6G3_CreditsBackfillNativeRefusesALiveApplyWithoutAStandDownKey(t *testing.T) {
	f := newCBFixture(t)
	seedCBCensusFixture(f)
	n := newT6G3CBNative(f, "")
	host, rc := newT6G3Host(t6g3CreditsBackfillKind, tasks.ModeLive, &t6g3Gate{})
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
	host, rc := newT6G3Host(t6g3CreditsBackfillKind, tasks.ModeLive, &t6g3Gate{})
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
	env := t6g3CreditsCoreEnv(t6g3Base{}, nil, t6g3NativeCBEngine{})
	assert.Equal(t, runtime.NumCPU(), env.Workers)
}

// The credits backfill's effects are the union of the rows of every method of
// its writer (t6g3CreditsWriter): the book and narrator writes, the author joins a
// credits write replaces, the metadata-cache stamp and ident: rows a credits write
// carries, and the operation store's rows; and what it reads. The native live
// test checks the writes the run made against this set.
func TestT6G3_CreditsBackfillEffects(t *testing.T) {
	d := t6g3KindByName(t, (&Plugin{}).t6g3Kinds(t6g3Deps{}, nil), "maintenance.credits-backfill")
	e := tasks.CommonOf(d).Effects
	// The writes are the union of the shared table's rows for the writer methods
	// the backfill can call (t6g3KindCalls), never a retyped resource list.
	assert.Equal(t, t6g3ExpectedWrites(t, "maintenance.credits-backfill"), t6g3WriteKinds(e))
	var reads []string
	for _, r := range e.ReadSet() {
		reads = append(reads, r.Kind)
	}
	slices.Sort(reads)
	assert.Equal(t, []string{"authors", "books", "series"}, reads)
}

// --- the cover ops' pools ---

// t6g3Barrier holds each caller of pass until `n` are in flight (or a short wait),
// so the most that ever ran at once is what the pool above it allowed.
type t6g3Barrier struct {
	n int

	mu       sync.Mutex
	inflight int
	max      int
	ready    chan struct{}
	once     sync.Once
}

func newT6G3Barrier(n int) *t6g3Barrier { return &t6g3Barrier{n: n, ready: make(chan struct{})} }

func (b *t6g3Barrier) pass() {
	b.mu.Lock()
	b.inflight++
	b.max = max(b.max, b.inflight)
	if b.inflight >= b.n {
		b.once.Do(func() { close(b.ready) })
	}
	b.mu.Unlock()
	select {
	case <-b.ready:
	case <-time.After(2 * time.Second):
	}
	b.mu.Lock()
	b.inflight--
	b.mu.Unlock()
}

func (b *t6g3Barrier) maxInFlight() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.max
}

// t6g3GateReader is a vision reader whose reads pass through a barrier.
type t6g3GateReader struct {
	capacity int
	*t6g3Barrier
}

func newT6G3GateReader(capacity, barrier int) *t6g3GateReader {
	return &t6g3GateReader{capacity: capacity, t6g3Barrier: newT6G3Barrier(barrier)}
}

func (g *t6g3GateReader) Capacity() int { return g.capacity }

func (g *t6g3GateReader) ReadCoverText(_ context.Context, _ []byte, _ string) (*ai.CoverTextResult, error) {
	g.pass()
	return &ai.CoverTextResult{Text: &covertext.Text{Title: "Read"}, Raw: `{"title":"Read"}`, Model: "vision-model", EndpointID: "pool-a"}, nil
}

// The native cover-text read sizes its pool like HEAD: the workers param, else the
// vision pool's capacity when live, as the pool itself (no semaphore inside an
// item for a read to queue on, where the item timeout would already be running).
// The capacity here is 1, which cannot equal the default pool (twice the cores,
// never below 2): two images are never in flight together under capacity 1, and
// with the capacity ignored the default pool would put both in flight at once.
// Capacity 2 with workers=1 never runs more than one.
func TestT6G3_CoverTextReadNativePoolIsTheWorkersParamOrTheCapacity(t *testing.T) {
	require.Greater(t, t6g3CoverOpDefaultWorkers(0), 1, "the capacity case needs a default that differs from 1")
	for _, tc := range []struct {
		name     string
		capacity int
		params   string
		want     int
	}{
		{"capacity", 1, `{}`, 1},
		{"capacity above one", 2, `{}`, 2},
		{"workers param", 2, `{"workers":1}`, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, deps, ids := coverOpFixture(t)
			reader := newT6G3GateReader(tc.capacity, 2)
			orig := newCoverTextReader
			newCoverTextReader = func() coverTextReader { return reader }
			t.Cleanup(func() { newCoverTextReader = orig })
			p := New(deps)
			var (
				wmu    sync.Mutex
				handed []tasks.Writer
				spy    *t6g3ForwardSpy
			)
			kd := t6g3Deps{
				Store: headT6g3Store{OpsStore: st, p: p},
				NewWriter: func(w tasks.Writer) t6g3Writer {
					wmu.Lock()
					defer wmu.Unlock()
					handed = append(handed, w)
					spy = newT6G3ForwardSpy(headT6g3Writer{p: p, store: st})
					return spy
				},
			}
			d := t6g3KindByName(t, (&Plugin{}).t6g3Kinds(kd, nil), "maintenance.cover-text-read")
			gate := &t6g3Gate{id: "cover-text"}
			_, rc := newT6G3Host(d.Kind(), tasks.ModeLive, gate)
			_, err := t6g3Run(t, d, rc, tc.params)
			require.NoError(t, err)
			wmu.Lock()
			require.Len(t, handed, 1, "NewWriter is called once per run")
			assert.Same(t, gate, handed[0], "NewWriter is handed this run's rc.Writer()")
			require.NotNil(t, spy)
			assert.NotEmpty(t, spy.argsOf("SetRaw"), "the records are written through the run's writer")
			spy.assertWithin(t, d)
			wmu.Unlock()
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

// t6g3CoverGateStore holds each book-files read at a barrier, which is where
// foldercover starts work on a book, and reports an empty operation queue (no
// scan), so a live folder-cover run is allowed.
type t6g3CoverGateStore struct {
	t6g3Store
	b *t6g3Barrier
}

func (s *t6g3CoverGateStore) GetBookFiles(id string) ([]database.BookFile, error) {
	s.b.pass()
	return s.t6g3Store.GetBookFiles(id)
}

func (s *t6g3CoverGateStore) ListActiveOperationsV2() ([]database.OperationV2Row, error) {
	return nil, nil
}

// A live native folder-cover run hands NewWriter this run's rc.Writer(), writes
// each book's cover through that writer (t6g3CoverFolderStore.ModifyBook), sets the
// cover on both books, and sizes its pool from the workers param, else the
// default (twice the cores, never below 2).
func TestT6G3_FolderCoverBackfillNativeLiveWritesThroughTheWriter(t *testing.T) {
	for _, tc := range []struct {
		name   string
		params string
		want   int
	}{
		{"workers param", `{"workers":1}`, 1},
		{"no param", `{}`, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, deps, ids := coverOpFixture(t)
			p := New(deps)
			barrier := newT6G3Barrier(2)
			var (
				mu     sync.Mutex
				handed []tasks.Writer
				spy    *t6g3ForwardSpy
			)
			kd := t6g3Deps{
				Store: &t6g3CoverGateStore{t6g3Store: headT6g3Store{OpsStore: st, p: p}, b: barrier},
				NewWriter: func(w tasks.Writer) t6g3Writer {
					mu.Lock()
					defer mu.Unlock()
					handed = append(handed, w)
					spy = newT6G3ForwardSpy(headT6g3Writer{p: p, store: st})
					return spy
				},
			}
			d := t6g3KindByName(t, (&Plugin{}).t6g3Kinds(kd, nil), "maintenance.folder-cover-backfill")
			gate := &t6g3Gate{id: "folder-cover"}
			_, rc := newT6G3Host(d.Kind(), tasks.ModeLive, gate)
			_, err := t6g3Run(t, d, rc, tc.params)
			require.NoError(t, err)

			mu.Lock()
			defer mu.Unlock()
			require.Len(t, handed, 1, "NewWriter is called once per run")
			assert.Same(t, gate, handed[0], "NewWriter is handed this run's rc.Writer()")
			require.NotNil(t, spy)
			assert.ElementsMatch(t, ids, spy.argsOf("ModifyBook"), "each book's write went through the run's writer")
			spy.assertWithin(t, d)
			for _, id := range ids {
				b, err := st.GetBookByID(id)
				require.NoError(t, err)
				require.NotNil(t, b)
				require.NotNil(t, b.CoverURL, "the cover is set on %s", id)
			}
			assert.Equal(t, tc.want, barrier.maxInFlight(), "the pool is the workers param, else the default")
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

// --- the env each credits-backfill shell hands the core ---

// captureCreditsCoreEnv keeps the env each shell hands the core, through the
// observer hook, and removes the hook when the test ends.
func captureCreditsCoreEnv(t *testing.T) *[]t6g3CreditsBackfillCoreEnv {
	t.Helper()
	var (
		mu   sync.Mutex
		envs []t6g3CreditsBackfillCoreEnv
	)
	t6g3ObserveCreditsEnv = func(env t6g3CreditsBackfillCoreEnv) {
		mu.Lock()
		envs = append(envs, env)
		mu.Unlock()
	}
	t.Cleanup(func() { t6g3ObserveCreditsEnv = nil })
	return &envs
}

// Both shells bound the core's worker pools by the machine's cores (the HEAD
// engine's pools use Workers; the native engine's pool is the kind's declared
// CPU slots, but the core's own fan-out reads Workers), and each hands the
// core its own engine.
func TestT6G3_CreditsBackfillShellsHandTheCoreTheirWorkersAndEngine(t *testing.T) {
	t.Run("head", func(t *testing.T) {
		f := newCBFixture(t)
		seedCBCensusFixture(f)
		envs := captureCreditsCoreEnv(t)
		p, _, _ := newT6G3CBHead(f, nil)
		require.NoError(t, p.creditsBackfillDef().Run(context.Background(), json.RawMessage(`{}`), &cbReporter{id: cbTestOpID}))
		require.Len(t, *envs, 1)
		assert.Equal(t, runtime.NumCPU(), (*envs)[0].Workers)
		assert.IsType(t, headT6g3CBEngine{}, (*envs)[0].Eng)
	})
	t.Run("native", func(t *testing.T) {
		f := newCBFixture(t)
		seedCBCensusFixture(f)
		envs := captureCreditsCoreEnv(t)
		n := newT6G3CBNative(f, t6g3ScanKey)
		host, rc := newT6G3Host(t6g3CreditsBackfillKind, tasks.ModePreview, &t6g3Gate{})
		host.SetCPUSlots(3)
		_, err := t6g3Run(t, n.kind(), rc, "")
		require.NoError(t, err)
		require.Len(t, *envs, 1)
		assert.Equal(t, runtime.NumCPU(), (*envs)[0].Workers)
		assert.IsType(t, t6g3NativeCBEngine{}, (*envs)[0].Eng)
	})
}

// --- the warnings a failed bookkeeping write leaves in the run's log ---

// t6g3AttrReporter is a cbReporter that keeps each log line with its attributes
// and can refuse the result write.
type t6g3AttrReporter struct {
	cbReporter
	failResult bool
	amu        sync.Mutex
	lines      []t6g3AttrLine
}

func (r *t6g3AttrReporter) Log(level slog.Level, msg string, attrs ...slog.Attr) error {
	line := t6g3AttrLine{Level: level, Msg: msg, Attrs: map[string]string{}}
	for _, a := range attrs {
		line.Attrs[a.Key] = a.Value.String()
	}
	r.amu.Lock()
	r.lines = append(r.lines, line)
	r.amu.Unlock()
	return r.cbReporter.Log(level, msg, attrs...)
}

func (r *t6g3AttrReporter) SetResult(v any) error {
	if r.failResult {
		return errors.New("result store unavailable")
	}
	return r.cbReporter.SetResult(v)
}

func (r *t6g3AttrReporter) snapshot() []t6g3AttrLine {
	r.amu.Lock()
	defer r.amu.Unlock()
	return slices.Clone(r.lines)
}

// A result write that fails clears the class lists it had just saved; when that
// clear fails too, the run logs a warning that carries the clear's error.
func TestT6G3_CreditsBackfillHeadShellWarnsWhenClassListsAreNotCleared(t *testing.T) {
	f := newCBFixture(t)
	seedCBCensusFixture(f)
	p, rec, _ := newT6G3CBHead(f, nil)
	rec.failClear = errors.New("clear refused")
	rep := &t6g3AttrReporter{id: cbTestOpID, failResult: true}
	err := p.creditsBackfillDef().Run(context.Background(), json.RawMessage(`{}`), rep)
	require.Error(t, err, "the result write failed")
	line, ok := t6g3FindLine(rep.snapshot(), "class lists not cleared")
	require.True(t, ok, "no warning in %v", rep.snapshot())
	assert.Equal(t, slog.LevelWarn, line.Level)
	assert.Equal(t, "clear refused", line.Attrs["error"])
}

// An operation whose leftovers this run judged gets a marker; when that write
// fails, the run warns, naming the operation and carrying the store's error.
func TestT6G3_CreditsBackfillHeadShellWarnsWhenAJudgedMarkerIsNotWritten(t *testing.T) {
	f, rec := cbLeftoverFixture(t, false)
	cbSeedOtherOp(t, f, cbCrashedOp, creditsBackfillOpID, "failed", rec)
	p, st, _ := newT6G3CBHead(f, nil)
	st.failMark = errors.New("marker refused")
	rep := &t6g3AttrReporter{id: cbLeftoverOp}
	require.NoError(t, p.creditsBackfillDef().Run(context.Background(), json.RawMessage(`{"apply":true}`), rep))
	line, ok := t6g3FindLine(rep.snapshot(), "could not mark operation's leftovers judged")
	require.True(t, ok, "no warning in %v", rep.snapshot())
	assert.Equal(t, slog.LevelWarn, line.Level)
	assert.Equal(t, cbCrashedOp, line.Attrs["operation"])
	assert.Equal(t, "marker refused", line.Attrs["error"])
}

// The native kind writes no judged marker (its judgements ride in its result,
// F5-18d O12), so it has no marker write to warn about:
// TestT6G3_CreditsBackfillNativeJudgesAFailedNativeRunAndCarriesTheJudgement
// (credits_backfill_native_test.go) covers its ledger.

// Both HEAD-shell journal forwarders create a row behind the classification
// check (database.RecordOperationChange): an unclassified change type is
// refused and nothing is written, a classified one lands. The journal-writer
// guard (internal/arch) no longer allowlists kinds_t6_g3.go, so this is the
// check that stays with it.
func TestT6G3_HeadJournalForwardersRefuseAnUnclassifiedChangeType(t *testing.T) {
	st := newRepairPebble(t)
	w := headT6g3Writer{p: &Plugin{deps: fakeDeps{store: st}}, store: st}
	view := t6g3CBStoreView{w: w}
	forwarders := map[string]func(*database.OperationChange) error{
		"writer": w.CreateOperationChange,
		"view":   view.CreateOperationChange,
	}
	for name, create := range forwarders {
		t.Run(name, func(t *testing.T) {
			opID := "op-" + name
			err := create(&database.OperationChange{OperationID: opID, BookID: "b-1", ChangeType: "no_such_change_type"})
			require.ErrorIs(t, err, database.ErrUnclassifiedChangeType)
			rows, gerr := st.GetOperationChanges(opID)
			require.NoError(t, gerr)
			require.Empty(t, rows, "an unclassified row was not written")

			require.NoError(t, create(&database.OperationChange{OperationID: opID, BookID: "b-1", ChangeType: "author_merge_credits"}))
			rows, gerr = st.GetOperationChanges(opID)
			require.NoError(t, gerr)
			require.Len(t, rows, 1)
		})
	}
}
