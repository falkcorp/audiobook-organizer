// file: internal/plugins/maintenance/stores_fixer_test.go
// version: 1.0.0
// guid: 6d2a9f41-8c53-4b17-a0e6-3f7b1d94c285
// last-edited: 2026-11-12

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
	"github.com/falkcorp/audiobook-organizer/internal/versions"
)

// tripDeps is a plugin's deps that report every FixerStores accessor reached
// through them: what a fixer running under the native shell must never do, as
// the shell hands it the stores on its context. With a test, the report is an
// error naming the accessor and the function that called it; without, only the
// log. The accessors answer from the embedded deps when it is set (so a run
// goes on and finds every such call, not only the first) and with nil when not.
type tripDeps struct {
	ServerDeps
	t   *testing.T
	log *tripLog
}

type tripLog struct {
	mu    sync.Mutex
	names []string
}

func (l *tripLog) add(name string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.names = append(l.names, name)
}

func (l *tripLog) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.names)
}

func (d tripDeps) hit(name string) {
	if d.log != nil {
		d.log.add(name)
	}
	if d.t == nil {
		return
	}
	d.t.Helper()
	caller := "?"
	if pc, _, _, ok := runtime.Caller(2); ok {
		caller = runtime.FuncForPC(pc).Name()
		caller = caller[strings.LastIndex(caller, "/")+1:]
	}
	d.t.Errorf("the plugin's own %s was reached (by %s) where the shell's stores were on the context", name, caller)
}

func (d tripDeps) OpsStore() OpsStore {
	d.hit("OpsStore")
	if d.ServerDeps == nil {
		return nil
	}
	return d.ServerDeps.OpsStore()
}

func (d tripDeps) OperationQueueStore() OpQueueReader {
	d.hit("OperationQueueStore")
	if d.ServerDeps == nil {
		return nil
	}
	return d.ServerDeps.OperationQueueStore()
}

func (d tripDeps) VersionPrimaryStore() VersionPrimaryStore {
	d.hit("VersionPrimaryStore")
	if d.ServerDeps == nil {
		return nil
	}
	return d.ServerDeps.VersionPrimaryStore()
}

func (d tripDeps) IngestVersionStore() versions.IngestStore {
	d.hit("IngestVersionStore")
	if d.ServerDeps == nil {
		return nil
	}
	return d.ServerDeps.IngestVersionStore()
}

func (d tripDeps) FragmentRepairReader() FragmentRepairReader {
	d.hit("FragmentRepairReader")
	if d.ServerDeps == nil {
		return nil
	}
	return d.ServerDeps.FragmentRepairReader()
}

func (d tripDeps) DedupVerdictReader() DedupVerdictReader {
	d.hit("DedupVerdictReader")
	if d.ServerDeps == nil {
		return nil
	}
	return d.ServerDeps.DedupVerdictReader()
}

func (d tripDeps) BookTagReader() BookTagReader {
	d.hit("BookTagReader")
	if d.ServerDeps == nil {
		return nil
	}
	return d.ServerDeps.BookTagReader()
}

func (d tripDeps) ExternalIDBooks() ExternalIDBookLookup {
	d.hit("ExternalIDBooks")
	if d.ServerDeps == nil {
		return nil
	}
	return d.ServerDeps.ExternalIDBooks()
}

func (d tripDeps) UserReadStateStore() UserReadStateStore {
	d.hit("UserReadStateStore")
	if d.ServerDeps == nil {
		return nil
	}
	return d.ServerDeps.UserReadStateStore()
}

func (d tripDeps) MergeUserStateStore() merge.UserStateRepairStore {
	d.hit("MergeUserStateStore")
	if d.ServerDeps == nil {
		return nil
	}
	return d.ServerDeps.MergeUserStateStore()
}

func (d tripDeps) MasterLists() MasterListStore {
	d.hit("MasterLists")
	if d.ServerDeps == nil {
		return nil
	}
	return d.ServerDeps.MasterLists()
}

func (d tripDeps) OwnerVetoes() OwnerVetoReader {
	d.hit("OwnerVetoes")
	if d.ServerDeps == nil {
		return nil
	}
	return d.ServerDeps.OwnerVetoes()
}

func (d tripDeps) CombineJournals() CombineJournalScanner {
	d.hit("CombineJournals")
	if d.ServerDeps == nil {
		return nil
	}
	return d.ServerDeps.CombineJournals()
}

func (d tripDeps) MetadataCacheStore() database.MetadataCacheStore {
	d.hit("MetadataCacheStore")
	if d.ServerDeps == nil {
		return nil
	}
	return d.ServerDeps.MetadataCacheStore()
}

func (d tripDeps) VersionTwinMetadataService() VersionTwinMetadataService {
	d.hit("VersionTwinMetadataService")
	if d.ServerDeps == nil {
		return nil
	}
	return d.ServerDeps.VersionTwinMetadataService()
}

// shellOnly runs the fixture's fixer the way the native shell does: its
// plugin's own deps report any store accessor reached, and every plan and apply
// of the fixture carries the real stores as the shell's FixerStores.
func (rf *retiredFixture) shellOnly() {
	rf.t.Helper()
	real := rf.f.p.deps
	rf.f.p.deps = tripDeps{ServerDeps: real, t: rf.t}
	rf.ctx = repairs.WithStores[FixerStores](context.Background(), pluginFixerStores{real})
}

// recStores is a FixerStores that counts the accessors called, by name.
type recStores struct {
	mu   *sync.Mutex
	hits map[string]int
}

func (s recStores) rec(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hits[name]++
}

func (s recStores) OpsStore() OpsStore                 { s.rec("OpsStore"); return nil }
func (s recStores) OperationQueueStore() OpQueueReader { s.rec("OperationQueueStore"); return nil }
func (s recStores) VersionPrimaryStore() VersionPrimaryStore {
	s.rec("VersionPrimaryStore")
	return nil
}
func (s recStores) IngestVersionStore() versions.IngestStore { s.rec("IngestVersionStore"); return nil }
func (s recStores) FragmentRepairReader() FragmentRepairReader {
	s.rec("FragmentRepairReader")
	return nil
}
func (s recStores) DedupVerdictReader() DedupVerdictReader { s.rec("DedupVerdictReader"); return nil }
func (s recStores) BookTagReader() BookTagReader           { s.rec("BookTagReader"); return nil }
func (s recStores) ExternalIDBooks() ExternalIDBookLookup  { s.rec("ExternalIDBooks"); return nil }
func (s recStores) UserReadStateStore() UserReadStateStore { s.rec("UserReadStateStore"); return nil }
func (s recStores) MergeUserStateStore() merge.UserStateRepairStore {
	s.rec("MergeUserStateStore")
	return nil
}
func (s recStores) MasterLists() MasterListStore           { s.rec("MasterLists"); return nil }
func (s recStores) OwnerVetoes() OwnerVetoReader           { s.rec("OwnerVetoes"); return nil }
func (s recStores) CombineJournals() CombineJournalScanner { s.rec("CombineJournals"); return nil }
func (s recStores) MetadataCacheStore() database.MetadataCacheStore {
	s.rec("MetadataCacheStore")
	return nil
}
func (s recStores) VersionTwinMetadataService() VersionTwinMetadataService {
	s.rec("VersionTwinMetadataService")
	return nil
}

var _ FixerStores = recStores{}

// shellDeps answers every FixerStores accessor from the shell's stores and none
// from the plugin's: the method-set check that keeps a new FixerStores method
// from falling through to the embedded deps. Each accessor of the interface is
// called by name on a shellDeps whose embedded deps report any call; recStores
// is a FixerStores with every method, so adding a method to FixerStores breaks
// the build here until this test knows it, and a method shellDeps does not
// override reaches the embedded deps and fails the test.
func TestShellDeps_EveryFixerStoreAccessorAnswersFromTheShell(t *testing.T) {
	log := &tripLog{}
	rec := recStores{mu: &sync.Mutex{}, hits: map[string]int{}}
	d := shellDeps{ServerDeps: tripDeps{log: log}, st: rec}

	typ := reflect.TypeOf((*FixerStores)(nil)).Elem()
	require.Equal(t, 15, typ.NumMethod(), "the accessors the fixers read their stores through")
	for i := 0; i < typ.NumMethod(); i++ {
		name := typ.Method(i).Name
		m := reflect.ValueOf(d).MethodByName(name)
		require.True(t, m.IsValid(), "shellDeps has %s", name)
		m.Call(nil)
		require.Equal(t, 1, rec.hits[name], "%s answers from the shell's stores", name)
	}
	require.Zero(t, log.count(), "no accessor fell through to the plugin's own deps: %v", log.names)

	// The plugin's own set, for the callers that run a fixer without the shell.
	pf := pluginFixerStores{ServerDeps: tripDeps{log: log}}
	for i := 0; i < typ.NumMethod(); i++ {
		reflect.ValueOf(pf).MethodByName(typ.Method(i).Name).Call(nil)
	}
	require.Equal(t, 15, log.count(), "pluginFixerStores is the plugin's deps, accessor for accessor")
}

// Retired-versions' Apply reaches its stores in four places beyond its Plan: the
// journaled retire writer, the `applied` decision record, and (finishing another
// op's retire) that op's provenance read. Under the shell none of them may
// touch the plugin's own: the deps here report every accessor reached.
func TestRetiredApply_ReadsAndWritesOnlyThroughTheShellStores(t *testing.T) {
	rf := newRetiredFixture(t)
	rf.enableApply()
	rf.seedApplyLibrary()
	rf.shellOnly()
	res := rf.planAll()
	ids := rf.applicableIDs(res)
	out := rf.applyRows(rf.f, res, ids, false, "op-shell")
	require.Equal(t, 3, out.Applied, "%v %+v", out.ByOutcome, out.Rows)
}

// A run cut off after the retire's commit point is finished by the next op,
// which reads the retiring op's provenance (retireOpProvenance) to write the
// record it owes: through the shell's stores too.
func TestRetiredApply_ResumedTailReadsProvenanceThroughTheShellStores(t *testing.T) {
	rf := newRetiredFixture(t)
	rf.enableApply()
	rf.seedApplyLibrary()
	rf.shellOnly()
	res := rf.planAll()
	ids := []string{rf.ids["would-retire"]}
	var armed atomic.Bool
	armed.Store(true)
	rf.f.retireWriterFor = func(ctx context.Context, w *repairs.Writer) (retireWriter, error) {
		inner, err := rf.f.newJournaledRetireWriter(ctx, w)
		return failingExternalIDs{inner, &armed}, err
	}
	out := rf.applyRows(rf.f, res, ids, false, "op-cut")
	require.Equal(t, 1, out.Partial, "%v %+v", out.ByOutcome, out.Rows)
	armed.Store(false)
	out = rf.applyRows(rf.f, res, ids, false, "op-resume")
	require.Equal(t, 1, out.Applied, "%v %+v", out.ByOutcome, out.Rows)
}

// The retire the fragment, duplicate-copies and consolidation-leftovers fixers
// share carries user state and hands the version group's primary on through the
// run's stores, not the plugin's.
func TestRetireInto_ReadsItsStoresFromTheShell(t *testing.T) {
	d := newDCFixture(t)
	s, l := d.dune(t)
	gid := "vg-dune"
	yes, no := true, false
	_, err := d.s.ModifyBook(s, func(b *database.Book) error { b.VersionGroupID, b.IsPrimaryVersion = &gid, &no; return nil })
	require.NoError(t, err)
	_, err = d.s.ModifyBook(l, func(b *database.Book) error { b.VersionGroupID, b.IsPrimaryVersion = &gid, &yes; return nil })
	require.NoError(t, err)

	real := d.p.deps
	d.p.deps = tripDeps{ServerDeps: real, t: t}
	ctx := repairs.WithStores[FixerStores](context.Background(), pluginFixerStores{real})
	w := repairs.NewWriter(d.s, d.s, dcFixerID, "bulk_update", "repairs-").WithJournal(d.s, d.s, "op-shell")
	_, err = retireInto(ctx, d.p, d.s, w, time.Now, dcFixerID, l, s, nil)
	require.NoError(t, err)
	require.False(t, d.live(t, "L"))
	require.Equal(t, []string{s}, d.livePrimaries(t, gid), "the primary was handed on, through the shell's VersionPrimaryStore")
}

// shellLibrary is a small invented library for the all-fixers run below: a few
// books by Gotham's authors in one series, some titles with stray whitespace and
// a duplicated title, each with a file row at a path that does not exist.
func shellLibrary(t *testing.T, st *database.PebbleStore) {
	t.Helper()
	bruce, err := st.CreateAuthor("Bruce Wayne")
	require.NoError(t, err)
	jim, err := st.CreateAuthor("  Jim  Gordon ")
	require.NoError(t, err)
	ser, err := st.CreateSeries("Gotham Chronicles", &bruce.ID)
	require.NoError(t, err)
	for i, ti := range []string{"Year One", "Year One ", " The Long Halloween", "Hush", "Hush - Part 2", "Dark Victory"} {
		author := &bruce.ID
		if i%2 == 1 {
			author = &jim.ID
		}
		seq, dur := i+1, 600
		b, err := st.CreateBook(&database.Book{Title: ti, FilePath: fmt.Sprintf("/lib/gotham/%d", i),
			AuthorID: author, SeriesID: &ser.ID, SeriesSequence: &seq})
		require.NoError(t, err)
		require.NoError(t, st.CreateBookFile(&database.BookFile{BookID: b.ID, FilePath: fmt.Sprintf("/lib/gotham/%d/part1.mp3", i),
			Format: "mp3", TrackNumber: 1, Duration: &dur}))
	}
}

// recordingStores is the shell's FixerStores over the real stores, counting the
// accessors a fixer reads.
type recordingStores struct {
	FixerStores
	mu   *sync.Mutex
	hits map[string]int
}

func (s recordingStores) rec(name string) {
	s.mu.Lock()
	s.hits[name]++
	s.mu.Unlock()
}
func (s recordingStores) OpsStore() OpsStore { s.rec("OpsStore"); return s.FixerStores.OpsStore() }
func (s recordingStores) OperationQueueStore() OpQueueReader {
	s.rec("OperationQueueStore")
	return s.FixerStores.OperationQueueStore()
}
func (s recordingStores) VersionPrimaryStore() VersionPrimaryStore {
	s.rec("VersionPrimaryStore")
	return s.FixerStores.VersionPrimaryStore()
}
func (s recordingStores) BookTagReader() BookTagReader {
	s.rec("BookTagReader")
	return s.FixerStores.BookTagReader()
}

// Invariant 7, with working stores: the shell's FixerStores on the context are
// the only stores any of the 22 fixers' Plan, Replan, Apply, BeginApply and
// AfterApply reach, including through the bodies they share with the v2
// operations. The plugin's own deps report every accessor reached (tripDeps);
// the shell's stores are the real in-memory ones, so each fixer reads a library
// and runs its rows through Replan and Apply instead of stopping at its first
// nil-store check. A panic fails the test: nothing is swallowed.
func TestFixers_PlanReplanAndApplyReadOnlyTheShellStores(t *testing.T) {
	st, err := database.NewPebbleStoreInMemory(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	st.WaitForWarmup()
	shellLibrary(t, st)

	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	real := fakeDeps{store: st, root: root, labels: &fakeLabels{}}
	reg := New(tripDeps{ServerDeps: real, t: t}).Repairs()
	fixers := reg.List()
	require.Len(t, fixers, 22)
	for _, f := range fixers {
		t.Run(f.ID(), func(t *testing.T) {
			rec := recordingStores{FixerStores: pluginFixerStores{real}, mu: &sync.Mutex{}, hits: map[string]int{}}
			ctx := repairs.WithStores[FixerStores](context.Background(), rec)
			guard := func(what string, fn func()) {
				defer func() {
					if r := recover(); r != nil {
						t.Errorf("%s of %s panicked: %v", what, f.ID(), r)
					}
				}()
				fn()
			}
			rep := &fakeReporter{}
			var rows []repairs.Row
			guard("Plan", func() {
				var err error
				rows, err = f.Plan(ctx, nil, rep)
				if err != nil {
					t.Logf("Plan: %v", err)
				}
			})
			if len(rows) > 8 {
				rows = rows[:8]
			}
			applyCtx := ctx
			if sc, ok := f.(repairs.ApplyScoped); ok {
				guard("BeginApply", func() {
					scoped, end := sc.BeginApply(ctx, false)
					applyCtx = scoped
					t.Cleanup(end)
				})
			}
			var applied []string
			for _, row := range rows {
				guard("Replan/Apply", func() {
					fresh, err := f.Replan(applyCtx, nil, row, rep)
					if err != nil || !fresh.Applicable() {
						return
					}
					w := repairs.NewWriter(st, st, f.ID(), "bulk_update", "repairs-").WithJournal(st, st, "op-"+f.ID()).WithFieldStates(st)
					if err := f.Apply(applyCtx, w, fresh); err == nil {
						applied = append(applied, fresh.BookIDs...)
					}
				})
			}
			if aa, ok := f.(repairs.AfterApplier); ok && len(applied) > 0 {
				guard("AfterApply", func() { _, _ = aa.AfterApply(applyCtx, applied) })
			}
			rec.mu.Lock()
			total := 0
			for _, n := range rec.hits {
				total += n
			}
			rec.mu.Unlock()
			t.Logf("%s: %d planned rows, %d applied, %d store reads through the shell", f.ID(), len(rows), len(applied), total)
		})
	}
}

// Without the shell a fixer reads HEAD's stores: the plugin's own accessors.
// The v2 repairs operations and the fixers' own tests run that way; the native
// shell refuses to (internal/repairs.KindDeps.BindStores is required).
func TestFixers_FallBackToPluginStoresWithoutShell(t *testing.T) {
	log := &tripLog{}
	p := New(tripDeps{log: log})
	f, ok := p.Repairs().Get("maintenance.tag-franchise")
	require.True(t, ok)
	func() {
		defer func() { _ = recover() }()
		_, _ = f.Plan(context.Background(), json.RawMessage(`{}`), &fakeReporter{})
	}()
	require.Positive(t, log.count(), "with no shell stores on the context the fixer reads the plugin's, as at HEAD")
}
