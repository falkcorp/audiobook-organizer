// file: internal/plugins/maintenance/batch_children_test.go
// version: 0.1.0
// guid: 2c8e61d7-4f0b-49a3-b5d9-7e13a0c4f862
// last-edited: 2026-11-22

package maintenance

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/tasks"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/serverdecode"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// The per-batch children and what they hold. Fixture people are Batman characters.

// batchChildKinds are the three children and the parent each belongs to.
var batchChildKinds = []struct{ parent, child tasks.Kind }{
	{extractWAVClipsKind, extractWAVBatchChildKind},
	{fileIntroKind, fileIntroBatchChildKind},
	{introMigrateSingleFileKind, introMigrateBatchChildKind},
}

func batchDefs(t *testing.T) map[tasks.Kind]tasks.Definition {
	t.Helper()
	deps, _ := fakeT6G4Deps(t)
	out := map[tasks.Kind]tasks.Definition{}
	for _, d := range t6g4KindsUnchecked(deps, nil) {
		out[d.Kind()] = d
	}
	return out
}

// scopeJSON is the params a parent would send for a batch of books named
// bk-<n>, each with one file f-<n>.
func scopeJSON(t *testing.T, ids ...string) json.RawMessage {
	t.Helper()
	var books []batchBook
	for _, id := range ids {
		books = append(books, batchBook{ID: id, Files: []string{"f-" + id}})
	}
	raw, err := json.Marshal(fileIntroBatchParams{batchScope: batchScope{ParentRunID: "run-parent", Batch: 3, Books: books}})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// holds is what the dispatcher holds for a run of def with params: the Subjects
// when the kind declares them, else its Effects' write set.
func holds(t *testing.T, def tasks.Definition, params json.RawMessage) []entity.Ref {
	t.Helper()
	c := tasks.CommonOf(def)
	if c.Subjects == nil {
		return c.Effects.WriteSet()
	}
	refs, err := c.Subjects(params)
	if err != nil {
		t.Fatalf("%s Subjects: %v", def.Kind(), err)
	}
	return refs
}

func overlapsAny(a, b []entity.Ref) bool {
	for _, x := range a {
		for _, y := range b {
			if tasks.Overlaps(x, y) {
				return true
			}
		}
	}
	return false
}

// A child's Subjects name only its batch's book and book_file rows (the wav child
// also holds files whole-kind, for the clips and links it writes on disk).
func TestBatchChildren_SubjectsAreOnlyTheBatch(t *testing.T) {
	defs := batchDefs(t)
	raw := scopeJSON(t, "bk-alfred", "bk-barbara")
	for _, k := range batchChildKinds {
		t.Run(string(k.child), func(t *testing.T) {
			got := holds(t, defs[k.child], raw)
			want := []entity.Ref{
				bookRef("bk-alfred"), bookFileRef("f-bk-alfred"),
				bookRef("bk-barbara"), bookFileRef("f-bk-barbara"),
			}
			if k.child == extractWAVBatchChildKind {
				want = append(want, tasks.Res("books", string(ops.ResFiles)))
			}
			if !slices.Equal(got, want) {
				t.Fatalf("Subjects = %v, want %v", got, want)
			}
			for _, r := range got {
				if r.ID == "" && r.Kind != string(ops.ResFiles) {
					t.Errorf("%s is a whole-kind hold: it would block every other writer of the kind, the thing a batch exists to avoid", r)
				}
			}
		})
	}
}

// A scope that would hold nothing (the dispatcher reads an empty write set as "no
// rows", so the child would overlap nothing), more than a batch, or no parent, or
// params that do not decode, is refused by Subjects, before the run is admitted.
func TestBatchChildren_SubjectsRefuseABadScope(t *testing.T) {
	defs := batchDefs(t)
	big := make([]string, batchSweepSize+1)
	for i := range big {
		big[i] = fmt.Sprintf("bk-%03d", i)
	}
	noParent, _ := json.Marshal(fileIntroBatchParams{batchScope: batchScope{Books: booksOfIDs([]string{"bk-a"})}})
	noID, _ := json.Marshal(fileIntroBatchParams{batchScope: batchScope{ParentRunID: "r", Books: []batchBook{{ID: ""}}}})
	cases := map[string]json.RawMessage{
		"no books":    json.RawMessage(`{"parent_run_id":"r","batch":0,"books":[]}`),
		"no params":   json.RawMessage(`{}`),
		"too many":    scopeJSON(t, big...),
		"no parent":   noParent,
		"a nameless":  noID,
		"not an obj":  json.RawMessage(`[1]`),
		"bad type":    json.RawMessage(`{"parent_run_id":"r","books":"all"}`),
		"null params": json.RawMessage(`null`),
	}
	for _, k := range batchChildKinds {
		for name, raw := range cases {
			t.Run(string(k.child)+"/"+name, func(t *testing.T) {
				_, err := tasks.CommonOf(defs[k.child]).Subjects(raw)
				if err == nil {
					t.Fatal("Subjects accepted the params")
				}
				if !errors.Is(err, errBatchScope) {
					t.Fatalf("err = %v, want errBatchScope", err)
				}
			})
		}
	}
}

// The dispatcher requires every Subjects ref to lie within the kind's declared
// write kinds; the children's do, for any batch.
func TestBatchChildren_SubjectsLieInsideEffects(t *testing.T) {
	defs := batchDefs(t)
	raw := scopeJSON(t, "bk-alfred")
	for _, k := range batchChildKinds {
		c := tasks.CommonOf(defs[k.child])
		kinds := map[string]bool{}
		for _, r := range c.Effects.WriteSet() {
			kinds[r.Module+"/"+r.Kind] = true
		}
		for _, r := range holds(t, defs[k.child], raw) {
			if !kinds[r.Module+"/"+r.Kind] {
				t.Errorf("%s: Subjects name %s, outside its Effects %v", k.child, r, c.Effects.WriteSet())
			}
		}
	}
}

// The parents HOLD no rows (Subjects names none), and a child shares neither the
// parent's concurrency key, nor its held set, nor anything in its NotWhile: the
// kernel refuses an Await on a run that would block on its own awaiter.
func TestBatchChildren_ParentsHoldNothingAndNeverSelfBlock(t *testing.T) {
	defs := batchDefs(t)
	for _, k := range batchChildKinds {
		t.Run(string(k.parent), func(t *testing.T) {
			p, c := tasks.CommonOf(defs[k.parent]), tasks.CommonOf(defs[k.child])
			if p.Subjects == nil {
				t.Fatalf("parent has no Subjects: it would hold its whole Effects.Writes %v for days", p.Effects.WriteSet())
			}
			if held := holds(t, defs[k.parent], nil); len(held) != 0 {
				t.Errorf("parent holds %v, want nothing", held)
			}
			if p.Resume != tasks.ResumeContinue {
				t.Errorf("parent Resume = %v, want ResumeContinue (the kernel honours ErrContinueInPlace only for it)", p.Resume)
			}
			if p.Timeout != tasks.MaxTimeout {
				t.Errorf("parent Timeout = %v, want the maximum (it yields rather than time out)", p.Timeout)
			}
			if c.Effects.IsReadOnly() || len(c.Effects.WriteSet()) == 0 {
				t.Errorf("child Effects %v: the child is where the writes are declared", c.Effects.WriteSet())
			}
			if c.Subjects == nil {
				t.Errorf("child has no Subjects: it would hold its whole Effects, as the parent used to")
			}
			if p.ConcurrencyKey == c.ConcurrencyKey {
				t.Errorf("parent and child share the key %q", p.ConcurrencyKey)
			}
			if slices.Contains(c.NotWhile, k.parent) || slices.Contains(p.NotWhile, k.child) {
				t.Errorf("NotWhile makes the child and parent wait on each other: %v / %v", c.NotWhile, p.NotWhile)
			}
			// awaitSelfBlock compares what each run HOLDS (core/internal/executor/
			// dispatch.go:698-703), not what each declares.
			if overlapsAny(holds(t, defs[k.parent], nil), holds(t, defs[k.child], scopeJSON(t, "bk-alfred"))) {
				t.Errorf("the parent's held set overlaps the child's: the Await would refuse")
			}
			if !strings.HasSuffix(string(k.child), ".batch") || string(k.child) != string(k.parent)+".batch" {
				t.Errorf("child kind %s is not parent kind + .batch", k.child)
			}
		})
	}
}

// The mode an executor resolves for a start that names none, restated from
// core/internal/executor/start.go:149-152 (HEAD): Preview for a kind with declared
// writes, Live for one declared ReadOnly. A batch parent must not become the
// second, or an omitted mode would run Live where HEAD's kind starts in Preview
// (for extract-wav-clips, Live-only, that start is a refusal). The parents keep
// HEAD's Effects for this reason and narrow what they hold with Subjects.
func omittedModeOf(c tasks.Common) tasks.Mode {
	if c.Effects.IsReadOnly() {
		return tasks.ModeLive
	}
	return tasks.ModePreview
}

// A batch parent started with no mode does not run Live: it resolves to Preview
// exactly as the same kind did at HEAD, and then does what a Preview run of it
// did (extract-wav-clips refuses it, the other two count and start no child).
func TestBatchParent_OmittedModeDoesNotRunLive(t *testing.T) {
	defs := batchDefs(t)
	for _, k := range batchChildKinds {
		t.Run(string(k.parent), func(t *testing.T) {
			c := tasks.CommonOf(defs[k.parent])
			if got := omittedModeOf(c); got != tasks.ModePreview {
				t.Fatalf("omitted mode resolves to %v, want Preview (HEAD's kind declared writes: start.go:149)", got)
			}
			// The declaration is HEAD's, not just "non-empty": the pinned set the
			// rest of the suite holds every kind to.
			pinnedWrites := strings.Fields(t6KindEffectsPinned[k.parent].writes)
			slices.Sort(pinnedWrites)
			if got, want := t6g4WriteKinds(c.Effects), pinnedWrites; !slices.Equal(got, want) {
				t.Errorf("parent Effects.Writes %v, want HEAD's %v", got, want)
			}
		})
	}
	// And what the resolved Preview does, through the real shells.
	for _, w := range t6g4Expected {
		if w.batchParent {
			t.Run("preview/"+string(w.kind), func(t *testing.T) { t6g4CheckBatchParentShell(t, w) })
		}
	}
}

// The admission rule, on the real definitions: while a child of batch N holds, a
// metadata write to a book outside batch N is admitted (no overlap), and one to a
// book inside it waits. A whole-kind writer of books waits behind each short child
// (it used to wait behind the whole run); the parent holds nothing at all.
func TestBatchAdmission_OnlyTheBatchIsHeld(t *testing.T) {
	defs := batchDefs(t)
	batchN := scopeJSON(t, "bk-alfred", "bk-barbara")
	nextBatch := scopeJSON(t, "bk-cassandra", "bk-dick")

	// The writes of a ModifyBook on one book, as a per-book metadata kind
	// (internal/repairs-style Subjects) declares them.
	modify := func(book string) []entity.Ref {
		return []entity.Ref{bookRef(book), tasks.ResID("books", string(ops.ResMetadataCache), book)}
	}
	wholeKind := tasks.CommonOf(defs[durationBackfillKind]).Effects.WriteSet() // acoustid.backfill's shape
	if len(wholeKind) == 0 {
		t.Fatal("fixture: the whole-kind writer declares no writes")
	}

	for _, k := range batchChildKinds {
		t.Run(string(k.child), func(t *testing.T) {
			held := holds(t, defs[k.child], batchN)
			if overlapsAny(held, modify("bk-gordon")) {
				t.Errorf("a metadata write to a book outside batch N is held behind the child (%v)", held)
			}
			// Two batches of one sweep share no row. (The wav child also holds files
			// whole-kind, for the clips and links it writes by path, so two wav
			// children overlap there; the sweep runs one child at a time anyway.)
			rowsOnly := func(rs []entity.Ref) []entity.Ref {
				return slices.DeleteFunc(slices.Clone(rs), func(r entity.Ref) bool { return r.ID == "" })
			}
			if overlapsAny(rowsOnly(held), rowsOnly(holds(t, defs[k.child], nextBatch))) {
				t.Errorf("two batches of one sweep overlap on a row: the next batch's child could not be admitted alongside")
			}
			for _, in := range []string{"bk-alfred", "bk-barbara"} {
				if !overlapsAny(held, modify(in)) {
					t.Errorf("a metadata write to %s, inside batch N, is NOT held: the child writes that book", in)
				}
			}
			if !overlapsAny(held, wholeKind) {
				t.Errorf("a whole-kind books writer is admitted alongside a child that writes books")
			}
			if parent := holds(t, defs[k.parent], nil); len(parent) != 0 || overlapsAny(parent, wholeKind) || overlapsAny(parent, modify("bk-alfred")) {
				t.Errorf("the parent holds %v: it must hold nothing", parent)
			}
		})
	}
}

// ---- scope guards: a child never writes a row its Subjects do not hold ----

// migrateFixtureStore is the library of the migrate tests: single-file books that
// carry an intro transcript, with a recording writer.
type migrateFixtureStore struct {
	*fakeT6G4Store
	ids   []string
	books map[string]*database.Book
	files map[string][]database.BookFile
}

func (s *migrateFixtureStore) ListBookIDs() ([]string, error) { return s.ids, nil }
func (s *migrateFixtureStore) GetBookByID(id string) (*database.Book, error) {
	return s.books[id], nil
}
func (s *migrateFixtureStore) GetBookFiles(id string) ([]database.BookFile, error) {
	return slices.Clone(s.files[id]), nil
}

func newMigrateFixture(t *testing.T, n int) (*migrateFixtureStore, t6g4Deps, *t6g4Probe) {
	t.Helper()
	deps, pr := fakeT6G4Deps(t)
	st := &migrateFixtureStore{
		fakeT6G4Store: pr.store,
		books:         map[string]*database.Book{},
		files:         map[string][]database.BookFile{},
	}
	for i := range n {
		id := fmt.Sprintf("bk-%04d", i)
		st.ids = append(st.ids, id)
		st.books[id] = &database.Book{ID: id, IntroTranscription: new("Arkham Asylum read by Dennis O'Neil")}
		st.files[id] = []database.BookFile{{ID: "f-" + id, BookID: id, FilePath: "/lib/" + id + "/01.m4b"}}
	}
	deps.Store = st
	return st, deps, pr
}

func (st *migrateFixtureStore) writes() []string { return st.rec.all() }

// A migrate child writes the file of a book of its batch, and counts, without
// writing, a book whose file is not one the batch holds (the file set changed
// after the parent planned it).
func TestBatchScope_MigrateChildRefusesAFileOutsideTheBatch(t *testing.T) {
	st, deps, _ := newMigrateFixture(t, 3)
	p := &Plugin{}
	// The batch holds bk-0000's f-bk-0000 and bk-0001 under a file id it was NOT
	// planned with (a relinked row); bk-0002 is not in the batch at all.
	scope := batchScope{ParentRunID: "run-parent", Books: []batchBook{
		{ID: "bk-0000", Files: []string{"f-bk-0000"}},
		{ID: "bk-0001", Files: []string{"f-planned-earlier"}},
	}}
	_, rc := newT6G4RC(introMigrateBatchChildKind, tasks.ModeLive, &t6g4RunWriter{})
	base, err := t6g4NativeBase(rc, deps, true)
	if err != nil {
		t.Fatal(err)
	}
	got, err := p.runIntroMigrateSingleFileCore(rc.Context(), introMigrateParams{batch: &scope}, introMigrateEnv{t6g4Base: base})
	if err != nil {
		t.Fatal(err)
	}
	if got.TotalBooks != 2 {
		t.Errorf("TotalBooks = %d, want the batch's 2 books, not the library's 3", got.TotalBooks)
	}
	if got.Counts[migrateWrote] != 1 || got.Counts[migrateOutOfScope] != 1 {
		t.Errorf("counts %v, want one write and one out-of-scope refusal", got.Counts)
	}
	if w := st.writes(); !slices.Equal(w, []string{"UpdateBookFile"}) {
		t.Errorf("writer calls %v, want exactly one UpdateBookFile (the in-scope file)", w)
	}
}

// The scoped writer refuses every write outside the scope, naming it, and passes
// one inside through.
func TestBatchScope_ScopedFileWriter(t *testing.T) {
	calls := &t6g4Calls{}
	w := scopedFileWriter{
		w:      recT6G4Writer{rec: calls},
		owners: map[string]string{"f1": "b1", "f2": "b2"},
		bookID: "b1",
	}
	if err := w.UpdateBookFile("f1", &database.BookFile{}); err != nil {
		t.Fatalf("in scope: %v", err)
	}
	for _, id := range []string{"f2" /* another book's */, "f9" /* unknown */} {
		if err := w.UpdateBookFile(id, &database.BookFile{}); !errors.Is(err, errBatchOutOfScope) {
			t.Errorf("file %s: err = %v, want errBatchOutOfScope", id, err)
		}
	}
	if got := calls.all(); !slices.Equal(got, []string{"UpdateBookFile"}) {
		t.Errorf("writer calls %v, want only the in-scope one", got)
	}
}

// The wav child skips and counts a book whose first audio file is not one it
// holds, before it runs ffmpeg or writes a hash; a book of its batch proceeds.
func TestBatchScope_WAVChildSkipsAFileOutsideTheBatch(t *testing.T) {
	st, deps, _ := newMigrateFixture(t, 2)
	for _, id := range st.ids {
		f := st.files[id][0]
		f.FileHash = "h-" + id
		st.files[id] = []database.BookFile{f}
	}
	cache := wavCacheDir(st.RootDir())
	if err := os.MkdirAll(cache, 0o775); err != nil {
		t.Fatal(err)
	}
	// bk-0000's clip is already cached: skip_existing counts it without ffmpeg.
	if err := os.WriteFile(cachedClipPath(cache, "h-bk-0000"), []byte("RIFF"), 0o644); err != nil {
		t.Fatal(err)
	}
	scope := batchScope{ParentRunID: "run-parent", Books: []batchBook{
		{ID: "bk-0000", Files: []string{"f-bk-0000"}},
		{ID: "bk-0001", Files: []string{"f-the-row-it-was-planned-with"}},
	}}
	_, rc := newT6G4RC(extractWAVBatchChildKind, tasks.ModeLive, &t6g4RunWriter{})
	base, err := t6g4NativeBase(rc, deps, true)
	if err != nil {
		t.Fatal(err)
	}
	got, err := (&Plugin{}).runExtractWAVClipsCore(rc.Context(), extractWAVParams{batch: &scope}, extractWAVClipsEnv{t6g4Base: base})
	if err != nil {
		t.Fatal(err)
	}
	if got.Total != 2 || got.Skipped != 1 || got.OutOfScope != 1 || got.Extracted != 0 || got.Failed != 0 {
		t.Fatalf("result %+v, want 2 books: one skipped (cached), one out of scope, none extracted", got)
	}
	for _, c := range st.writes() {
		if c == "SetBookFileHash" {
			t.Errorf("a hash was written for a file outside the batch")
		}
	}
}

// Selection in a child covers only the batch's files; a donor file in another
// book, which is read and never written, still serves a twin in the batch.
func TestBatchScope_FileIntroSelectionStaysInTheBatch(t *testing.T) {
	donor := core("f-donor", "bk-other", "/lib/o/01.m4b", "h-twin", 1)
	donor.IntroSourceHash = "h-twin"
	cores := []database.BookFileCore{
		core("f-in", "bk-in", "/lib/i/01.m4b", "h-in", 1),
		core("f-twin", "bk-in", "/lib/i/02.m4b", "h-twin", 2),
		core("f-out", "bk-out", "/lib/x/01.m4b", "h-out", 1),
		core("f-moved", "bk-out", "/lib/x/02.m4b", "h-moved", 2),
		donor,
	}
	scope := map[string]string{"f-in": "bk-in", "f-twin": "bk-in", "f-moved": "bk-in" /* now under another book */}
	sel := selectFileIntroWork(cores, fileIntroSelectOpts{onlyMissing: true, scope: scope})

	var members []string
	var donors []string
	for _, g := range sel.groups {
		for _, m := range g.Members {
			members = append(members, m.FileID)
		}
		if g.DonorFileID != "" {
			donors = append(donors, g.DonorFileID)
		}
	}
	slices.Sort(members)
	if !slices.Equal(members, []string{"f-in", "f-twin"}) {
		t.Fatalf("selected %v, want only the batch's files f-in and f-twin (f-out is another book's, f-moved moved under it)", members)
	}
	if !slices.Equal(donors, []string{"f-donor"}) {
		t.Fatalf("donors %v, want the other book's file as the read-only donor", donors)
	}

	whole := selectFileIntroWork(cores, fileIntroSelectOpts{onlyMissing: true})
	if whole.selected <= sel.selected {
		t.Fatalf("an unscoped selection (%d files) is not wider than the scoped one (%d)", whole.selected, sel.selected)
	}
}

type clearFixtureReads struct{ cores []database.BookFileCore }

func (r clearFixtureReads) GetAllBookFilesCore() ([]database.BookFileCore, error) {
	return r.cores, nil
}
func (clearFixtureReads) GetBookFileByID(string, string) (*database.BookFile, error) {
	return nil, nil
}
func (clearFixtureReads) GetBookByID(string) (*database.Book, error) { return nil, nil }

type clearFixtureWrites struct {
	mu      sync.Mutex
	cleared []string
}

func (w *clearFixtureWrites) ClearBookFileIntro(_, fileID, _ string) (*database.BookFile, bool, error) {
	w.mu.Lock()
	w.cleared = append(w.cleared, fileID)
	w.mu.Unlock()
	return nil, true, nil
}
func (*clearFixtureWrites) ModifyBook(string, func(*database.Book) error) (*database.Book, error) {
	return nil, nil
}

// A rollback child clears only the files of its batch that the named run wrote.
func TestBatchScope_ClearChildTouchesOnlyItsBatch(t *testing.T) {
	stamped := func(id, book string) database.BookFileCore {
		c := core(id, book, "/lib/"+id+".m4b", "h-"+id, 1)
		c.IntroRunOpID = "run-joker"
		return c
	}
	cores := []database.BookFileCore{
		stamped("f-in", "bk-in"),
		stamped("f-other-book", "bk-out"),
		core("f-not-this-run", "bk-in", "/lib/z.m4b", "h-z", 2), // a different run wrote it
	}
	deps, _ := fakeT6G4Deps(t)
	_, rc := newT6G4RC(fileIntroBatchChildKind, tasks.ModeLive, &t6g4RunWriter{})
	base, err := t6g4NativeBase(rc, deps, false)
	if err != nil {
		t.Fatal(err)
	}
	w := &clearFixtureWrites{}
	res, err := (&Plugin{}).t6g4ClearFileIntros(rc.Context(), fileIntroClearIO{
		reads: clearFixtureReads{cores: cores}, writes: w, writable: true, each: base.Each,
		scope: map[string]string{"f-in": "bk-in", "f-not-this-run": "bk-in"},
	}, &fakeQueue{}, base.reporter(rc.Context()), "run-joker", false)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(w.cleared, []string{"f-in"}) || res.Files != 1 {
		t.Fatalf("cleared %v (Files=%d), want only f-in", w.cleared, res.Files)
	}
}

// ---- rollup ----

func TestBatchRollup_MergeFileIntro(t *testing.T) {
	seed := fileIntroResult{RunID: "run-1", Eligible: 900, Selected: 300, Stats: &database.FileIntroStats{
		Eligible: 900, TranscribedAtStart: 600, TranscribedNow: 600, Selected: 300,
	}}
	child := func(start, now, ok, silence int) fileIntroResult {
		return fileIntroResult{Eligible: 7, Selected: 7, Stats: &database.FileIntroStats{
			Eligible: 7, TranscribedAtStart: start, TranscribedNow: now, Selected: 7,
			Attempted: ok + silence, OK: ok, Silence: silence, Reused: 1, BookFills: 1,
		}}
	}
	acc := seed
	acc.Stats = new(*seed.Stats)
	mergeFileIntro(&acc, child(10, 14, 3, 1))
	mergeFileIntro(&acc, child(40, 45, 5, 0))
	s := acc.Stats
	if s.OK != 8 || s.Silence != 1 || s.Attempted != 9 || s.Reused != 2 || s.BookFills != 2 {
		t.Errorf("outcome counters not summed: %+v", s)
	}
	// TranscribedNow moves by what each child added to ITS OWN start (4 and 5).
	if s.TranscribedNow != 600+4+5 {
		t.Errorf("TranscribedNow = %d, want 609", s.TranscribedNow)
	}
	// The selection-wide numbers are the parent's and are not summed.
	if s.Eligible != 900 || s.Selected != 300 || s.TranscribedAtStart != 600 || acc.Eligible != 900 || acc.Selected != 300 {
		t.Errorf("selection-wide numbers changed: %+v eligible=%d selected=%d", s, acc.Eligible, acc.Selected)
	}

	clear := fileIntroResult{Cleared: &fileIntroClearResult{RunID: "run-x", Files: 3, BookReverts: 1, Errors: 0}}
	rb := fileIntroResult{RunID: "run-x", Cleared: &fileIntroClearResult{RunID: "run-x"}}
	mergeFileIntro(&rb, clear)
	mergeFileIntro(&rb, fileIntroResult{Cleared: &fileIntroClearResult{Files: 4, Errors: 2}})
	if c := rb.Cleared; c.Files != 7 || c.BookReverts != 1 || c.Errors != 2 {
		t.Errorf("clear tallies %+v, want files 7, reverts 1, errors 2", c)
	}
}

func TestBatchRollup_MergeIntroMigrateAndWAV(t *testing.T) {
	m := introMigrateResult{Counts: map[string]int{}}
	mergeIntroMigrate(&m, introMigrateResult{TotalBooks: 100, Counts: map[string]int{migrateWrote: 90, migrateNoTranscript: 10}})
	mergeIntroMigrate(&m, introMigrateResult{TotalBooks: 50, Counts: map[string]int{migrateWrote: 40, migrateOutOfScope: 1}})
	if m.TotalBooks != 150 || m.Counts[migrateWrote] != 130 || m.Counts[migrateNoTranscript] != 10 || m.Counts[migrateOutOfScope] != 1 {
		t.Errorf("migrate rollup %+v", m)
	}
	var w extractWAVResult
	mergeExtractWAV(&w, extractWAVResult{Extracted: 5, Skipped: 1, Failed: 1, HashFailed: 1, HashMismatched: 1, ClipLinkFailed: 1, Total: 9, OutOfScope: 1})
	mergeExtractWAV(&w, extractWAVResult{Extracted: 1, Total: 1})
	if w != (extractWAVResult{Extracted: 6, Skipped: 1, Failed: 1, HashFailed: 1, HashMismatched: 1, ClipLinkFailed: 1, Total: 10, OutOfScope: 1}) {
		t.Errorf("wav rollup %+v", w)
	}
}

// ---- the parents, end to end, through an in-process executor ----

// batchRunHost is a run host whose Enqueue records the child a parent starts and
// whose Await runs that child's own Run in process, as the executor would: the
// params go through JSON and the kind's own decode, and Subjects is asked first
// (a refusal fails the child as an admission refusal would). The child runs in
// the parent's mode, as an rc.Enqueue without a mode option does.
type batchRunHost struct {
	tasks.RunHost
	t    *testing.T
	defs map[tasks.Kind]tasks.Definition
	mode tasks.Mode
	// child builds the RC a child runs under, and the writer that RC hands out;
	// nil runs it on a plain host.
	child func(kind tasks.Kind) (*tasks.RC, tasks.Writer)

	mu      sync.Mutex
	runs    []*hostChild
	resumed bool
	// fail makes the children of a kind end failed with this message.
	fail map[tasks.Kind]string
	// writers are the writers the children's RCs hand out, in start order.
	writers []tasks.Writer
}

type hostChild struct {
	id      tasks.RunID
	kind    tasks.Kind
	raw     json.RawMessage
	subject []entity.Ref
	out     *tasks.Outcome
}

func (h *batchRunHost) Resumed() bool { return h.resumed }

func (h *batchRunHost) Enqueue(kind tasks.Kind, params any, _ ...tasks.StartOption) (tasks.RunRef, error) {
	raw, err := json.Marshal(params)
	if err != nil {
		return tasks.RunRef{}, err
	}
	def, ok := h.defs[kind]
	if !ok {
		return tasks.RunRef{}, fmt.Errorf("no kind %s registered", kind)
	}
	subj, err := tasks.CommonOf(def).Subjects(raw)
	if err != nil {
		return tasks.RunRef{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.runs) >= batchTestRunaway {
		return tasks.RunRef{}, fmt.Errorf("runaway sweep: %d children enqueued, the cursor is not moving", len(h.runs))
	}
	c := &hostChild{id: tasks.RunID(fmt.Sprintf("child-%d", len(h.runs)+1)), kind: kind, raw: raw, subject: subj}
	h.runs = append(h.runs, c)
	return tasks.RunRef{ID: c.id}, nil
}

func (h *batchRunHost) Await(ref tasks.RunRef) (tasks.Outcome, error) {
	h.mu.Lock()
	var c *hostChild
	for _, r := range h.runs {
		if r.id == ref.ID {
			c = r
		}
	}
	h.mu.Unlock()
	if c == nil {
		return tasks.Outcome{}, fmt.Errorf("no run %s", ref.ID)
	}
	if c.out != nil {
		return *c.out, nil
	}
	var out tasks.Outcome
	if msg, ok := h.fail[c.kind]; ok {
		out = tasks.Outcome{State: tasks.StateFailed, Err: msg}
	} else {
		var rc *tasks.RC
		var w tasks.Writer
		if h.child != nil {
			rc, w = h.child(c.kind)
		} else {
			w = &t6g4RunWriter{name: string(c.id)}
			_, rc = newT6G4RC(c.kind, h.mode, w)
		}
		h.mu.Lock()
		h.writers = append(h.writers, w)
		h.mu.Unlock()
		res, err := runT6G4Kind(h.t, h.defs[c.kind], rc, string(c.raw))
		out = tasks.Outcome{State: tasks.StateSucceeded, Result: res}
		if err != nil {
			out = tasks.Outcome{State: tasks.StateFailed, Err: err.Error()}
		}
	}
	c.out = &out
	return out, nil
}

// batchDefsOf lists defs by kind.
func batchDefsOf(defs []tasks.Definition) map[tasks.Kind]tasks.Definition {
	out := map[tasks.Kind]tasks.Definition{}
	for _, d := range defs {
		out[d.Kind()] = d
	}
	return out
}

// newBatchRunHost is a parent's RC over base, whose children run in process.
func newBatchRunHost(t *testing.T, base tasks.RunHost, mode tasks.Mode, defs map[tasks.Kind]tasks.Definition) (*batchRunHost, *tasks.RC) {
	t.Helper()
	h := &batchRunHost{RunHost: base, t: t, defs: defs, mode: mode}
	return h, tasks.NewRC(h)
}

// newBatchParentRC is the common shape: a plain parent host in mode.
func newBatchParentRC(t *testing.T, kind tasks.Kind, mode tasks.Mode, defs map[tasks.Kind]tasks.Definition) (*batchRunHost, *tasks.RC) {
	t.Helper()
	th, _ := newT6G4RC(kind, mode, &t6g4RunWriter{name: "parent"})
	return newBatchRunHost(t, th, mode, defs)
}

// The migrate parent, over 250 single-file books: three children of 100, 100 and
// 50 books, each holding only its own books and their files, run one at a time,
// and the parent's result is the sum of theirs.
func TestBatchParent_MigrateSweepsTheLibraryInBatchesOfOneHundred(t *testing.T) {
	st, deps, pr := newMigrateFixture(t, 250)
	defs := batchDefsOf(t6g4KindsUnchecked(deps, nil))
	h, rc := newBatchParentRC(t, introMigrateSingleFileKind, tasks.ModeLive, defs)

	raw, err := runT6G4Kind(t, defs[introMigrateSingleFileKind], rc, "")
	if err != nil {
		t.Fatal(err)
	}
	var got introMigrateResult
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.TotalBooks != 250 || got.Counts[migrateWrote] != 250 {
		t.Fatalf("result %+v, want 250 books written", got)
	}
	var sizes []int
	seen := map[string]bool{}
	for i, c := range h.runs {
		if c.kind != introMigrateBatchChildKind {
			t.Fatalf("child %d is %s", i, c.kind)
		}
		var p introMigrateBatchParams
		if err := json.Unmarshal(c.raw, &p); err != nil {
			t.Fatal(err)
		}
		sizes = append(sizes, len(p.Books))
		if p.ParentRunID != string(rc.RunID()) || p.Batch != i {
			t.Errorf("child %d params: parent %q batch %d", i, p.ParentRunID, p.Batch)
		}
		for _, b := range p.Books {
			if seen[b.ID] {
				t.Errorf("book %s is in two batches", b.ID)
			}
			seen[b.ID] = true
		}
		// Subjects named this batch's rows and no others.
		if want := len(p.Books) * 2; len(c.subject) != want {
			t.Errorf("child %d holds %d refs, want %d (a book and a file row each)", i, len(c.subject), want)
		}
	}
	if !slices.Equal(sizes, []int{100, 100, 50}) || len(seen) != 250 {
		t.Fatalf("batches %v over %d distinct books, want [100 100 50] over 250", sizes, len(seen))
	}
	if n := len(st.writes()); n != 250 {
		t.Errorf("%d writes, want one per book", n)
	}
	if pr.newWriterCalls != 3 {
		t.Errorf("NewWriter called %d times, want once per child and never for the parent", pr.newWriterCalls)
	}
}

// A parent restarted after it finished two batches picks up at the third: the
// cursor is in its checkpoint, not recomputed from the library.
func TestBatchParent_MigrateRestartResumesAtTheCursor(t *testing.T) {
	_, deps, _ := newMigrateFixture(t, 250)
	defs := batchDefsOf(t6g4KindsUnchecked(deps, nil))
	h, rc := newBatchParentRC(t, introMigrateSingleFileKind, tasks.ModeLive, defs)
	// The earlier attempt finished batches 0 and 1 (cursor b0199) and folded their
	// results into its checkpoint.
	if err := rc.Checkpoint(batchState[introMigrateResult]{
		After: "bk-0199", Batches: 2, Steps: 4,
		Rollup: introMigrateResult{TotalBooks: 200, Counts: map[string]int{migrateWrote: 200}},
	}); err != nil {
		t.Fatal(err)
	}
	h.resumed = true
	raw, err := runT6G4Kind(t, defs[introMigrateSingleFileKind], rc, "")
	if err != nil {
		t.Fatal(err)
	}
	var got introMigrateResult
	_ = json.Unmarshal(raw, &got)
	if len(h.runs) != 1 {
		t.Fatalf("%d children after the restart, want only the last batch", len(h.runs))
	}
	var p introMigrateBatchParams
	_ = json.Unmarshal(h.runs[0].raw, &p)
	if len(p.Books) != 50 || p.Books[0].ID != "bk-0200" || p.Batch != 2 {
		t.Fatalf("resumed batch: %d books from %s as batch %d, want 50 from bk-0200 as batch 2", len(p.Books), p.Books[0].ID, p.Batch)
	}
	if got.TotalBooks != 250 || got.Counts[migrateWrote] != 250 {
		t.Fatalf("result %+v, want the checkpointed 200 plus the resumed 50", got)
	}
}

// A parent started in preview runs the shared core's preview and starts no child.
func TestBatchParent_PreviewStartsNoChild(t *testing.T) {
	_, deps, pr := newMigrateFixture(t, 5)
	defs := batchDefsOf(t6g4KindsUnchecked(deps, nil))
	h, rc := newBatchParentRC(t, introMigrateSingleFileKind, tasks.ModePreview, defs)
	raw, err := runT6G4Kind(t, defs[introMigrateSingleFileKind], rc, "")
	if err != nil {
		t.Fatal(err)
	}
	var got introMigrateResult
	_ = json.Unmarshal(raw, &got)
	if len(h.runs) != 0 || !got.DryRun || got.Counts[migrateDryRun] != 5 {
		t.Fatalf("preview started %d children, result %+v", len(h.runs), got)
	}
	if pr.newWriterCalls != 0 {
		t.Errorf("a preview built a writer")
	}
}

// A failed child stops the sweep and the parent run fails with it.
func TestBatchParent_FailedChildFailsTheParent(t *testing.T) {
	_, deps, _ := newMigrateFixture(t, 250)
	defs := batchDefsOf(t6g4KindsUnchecked(deps, nil))
	h, rc := newBatchParentRC(t, introMigrateSingleFileKind, tasks.ModeLive, defs)
	h.fail = map[tasks.Kind]string{introMigrateBatchChildKind: "the Penguin pulled the plug"}
	_, err := runT6G4Kind(t, defs[introMigrateSingleFileKind], rc, "")
	if !errors.Is(err, errBatchChild) || !strings.Contains(err.Error(), "the Penguin pulled the plug") {
		t.Fatalf("err = %v, want errBatchChild carrying the child's error", err)
	}
	if len(h.runs) != 1 {
		t.Fatalf("%d children, want the sweep stopped at the first failure", len(h.runs))
	}
}

// ---- dark ----

// The per-batch scope can never be set by a decode: HEAD's v2 shells decode their
// params into these types, and a client cannot name a batch to confine a run to.
func TestBatchDark_ScopeFieldsAreUnexported(t *testing.T) {
	for _, typ := range []reflect.Type{
		reflect.TypeFor[extractWAVParams](), reflect.TypeFor[introMigrateParams](), reflect.TypeFor[fileIntroParams](),
	} {
		f, ok := typ.FieldByName("batch")
		if !ok || f.IsExported() {
			t.Errorf("%s.batch: present=%v exported=%v, want an unexported field", typ.Name(), ok, f.IsExported())
		}
	}
}

// Nothing new is reachable from the process's boot: the child kinds and the
// registration roots are named, outside tests, only in the files that define them
// and in kinds_t6_g4.go (whose own anchor, `var _ = t6g4Kinds`, is what the
// deadcode ratchet keeps dark). In particular HEAD's registration (plugin.go) and
// every package outside this one never mention them.
func TestBatchDark_NewKindsAreNamedOnlyByTheirRegistrationRoot(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	names := []string{"extractWAVBatchKind", "introMigrateBatchKind", "fileIntroBatchKind", "runBatchSweep", "newBatchHost",
		"extractWAVBatchParent", "introMigrateBatchParent", "fileIntroBatchParent"}
	allowed := map[string]bool{
		filepath.Join("internal", "plugins", "maintenance", "batch_children.go"): true,
		filepath.Join("internal", "plugins", "maintenance", "batch_sweep.go"):    true,
		filepath.Join("internal", "plugins", "maintenance", "kinds_t6_g4.go"):    true,
		// The three parent shells call their own parent run.
		filepath.Join("internal", "plugins", "maintenance", "extract_wav_clips.go"):         true,
		filepath.Join("internal", "plugins", "maintenance", "intro_migrate_single_file.go"): true,
		filepath.Join("internal", "plugins", "maintenance", "file_intro_transcribe.go"):     true,
	}
	var hits, scanned int
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "web", ".worktrees", "vendor", ".standards":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		scanned++
		rel, _ := filepath.Rel(root, path)
		src := string(b)
		for _, n := range names {
			if strings.Contains(src, n) {
				hits++
				if !allowed[rel] {
					t.Errorf("%s names %s: a new kind must be reachable only through t6g4KindsUnchecked", rel, n)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if scanned < 100 || hits < len(names) {
		t.Fatalf("scanned %d files with %d hits: the walk is vacuous", scanned, hits)
	}
	// HEAD's registration never mentions a batch kind.
	reg, err := os.ReadFile("plugin.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range append(names, "t6g4Kinds", "t6g4KindsUnchecked") {
		if strings.Contains(string(reg), n) {
			t.Errorf("plugin.go (HEAD's Register) mentions %s", n)
		}
	}
}

// ---- the shells of the children and of the parents ----

// t6g4CheckBatchParentShell is the parent half of TestT6G4Kinds_WritesThroughShell:
// a preview run starts no child and builds no writer (a Live-only parent refuses
// by name), and a live run over the fake library builds no writer and records no
// write: the writes are the children's.
func t6g4CheckBatchParentShell(t *testing.T, w t6g4Want) {
	t.Helper()
	t.Setenv(serverdecode.EnvVar, "true") // extract-wav-clips checks the host's decode opt-in first
	for _, mode := range []tasks.Mode{tasks.ModePreview, tasks.ModeLive} {
		deps, pr := fakeT6G4Deps(t)
		defs := batchDefsOf(t6g4KindsUnchecked(deps, nil))
		h, rc := newBatchParentRC(t, w.kind, mode, defs)
		err := t6g4RunSafely(t, defs[w.kind], rc, "")
		if mode == tasks.ModePreview && w.liveOnly && (err == nil || !strings.Contains(err.Error(), "preview not supported")) {
			t.Errorf("%s: Preview run: err = %v, want \"preview not supported\"", w.kind, err)
		}
		if pr.newWriterCalls != 0 {
			t.Errorf("%s: a %s run of the parent built a writer %d time(s)", w.kind, mode, pr.newWriterCalls)
		}
		for _, c := range pr.store.rec.all() {
			if t6g4WriterCalls(c) {
				t.Errorf("%s: a %s run of the parent made the write %s", w.kind, mode, c)
			}
		}
		if mode == tasks.ModePreview && len(h.runs) != 0 {
			t.Errorf("%s: a preview started %d children", w.kind, len(h.runs))
		}
	}
}

// Each child: a live run builds its writer once, from this run's rc.Writer(); a
// preview run is refused (a child exists only to write); and the writes it records
// are within its declared Effects.
func TestBatchChildren_WritesThroughShell(t *testing.T) {
	t.Setenv(serverdecode.EnvVar, "true")
	for _, k := range batchChildKinds {
		t.Run(string(k.child), func(t *testing.T) {
			deps, pr := fakeT6G4Deps(t)
			def := t6g4KindByName(t, t6g4KindsUnchecked(deps, nil), k.child)
			_, rc := newT6G4RC(k.child, tasks.ModePreview, &t6g4RunWriter{})
			if err := t6g4RunSafely(t, def, rc, string(scopeJSON(t, "bk-alfred"))); err == nil {
				t.Errorf("a preview run of a child succeeded")
			}
			if pr.newWriterCalls != 0 {
				t.Errorf("a refused preview built a writer")
			}

			run := &t6g4RunWriter{name: "run"}
			_, rc = newT6G4RC(k.child, tasks.ModeLive, run)
			_ = t6g4RunSafely(t, def, rc, string(scopeJSON(t, "bk-alfred")))
			if pr.newWriterCalls != 1 || pr.handed != tasks.Writer(run) {
				t.Errorf("NewWriter called %d time(s) with %v, want once with this run's rc.Writer()", pr.newWriterCalls, pr.handed)
			}
			t6g4CheckRecordedWritesDeclared(t, def, pr)
		})
	}
}

// The children decode the same params as their parents and refuse the same three
// mode keys: a child runs in the mode of the parent that started it.
func TestBatchChildren_RefuseModeKeys(t *testing.T) {
	for _, k := range batchChildKinds {
		for _, key := range []string{"dry_run", "dryRun", "apply", "DRY_RUN"} {
			t.Run(string(k.child)+"/"+key, func(t *testing.T) {
				deps, pr := fakeT6G4Deps(t)
				def := t6g4KindByName(t, t6g4KindsUnchecked(deps, nil), k.child)
				_, rc := newT6G4RC(k.child, tasks.ModeLive, &t6g4RunWriter{})
				body := fmt.Sprintf(`{"parent_run_id":"r","books":[{"id":"bk-a","files":["f-a"]}],%q:true}`, key)
				err := t6g4RunSafely(t, def, rc, body)
				if err == nil || !strings.Contains(err.Error(), "param is not accepted") {
					t.Fatalf("err = %v, want a refusal of the mode key", err)
				}
				if pr.newWriterCalls != 0 {
					t.Errorf("a refused run built a writer")
				}
			})
		}
	}
}
