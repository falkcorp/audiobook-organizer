// file: internal/plugins/maintenance/kinds_t6_g5_native_test.go
// version: 1.4.0
// guid: 6c1f0b7e-3a52-4d8c-9f40-b8e2a7d15c63
// last-edited: 2026-11-15

package maintenance

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/fileops"
	"github.com/falkcorp/audiobook-organizer/internal/itunes"
	itunesservice "github.com/falkcorp/audiobook-organizer/internal/itunes/service"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
	"github.com/falkcorp/mediacore/tasks"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The native shells run a real core over a real store here, with the
// recording spy below as the only write path. Each test proves the three things
// a unit test of the core cannot: NewWriter was handed THIS run's rc.Writer(),
// every write the core made reached the spy, and the kinds that hold the scan
// stand-down took the configured key and gave it back. The spy also names the
// resource each write touched, and requireWriter checks those against the
// kind's declared WriteSet, so the Effects list is tested against what the
// writer really did and not against a second copy of itself.

// t6g5ScanKey is the library scan's concurrency key the native tests configure.
const t6g5ScanKey tasks.Key = "books.library-scan"

// The resources each writer method writes are t6g5WriterResources, the table in
// production code that F5-18's writer reads too; the spy below reads the same one.

// t6g5Spy is the writer the native shells are handed: the real HEAD writer
// with every write the group makes recorded first. Every method is written out
// (no embedded interface), so a writer method the spy does not know about is a
// compile error, not a silent pass-through.
type t6g5Spy struct {
	inner    t6g5Writer
	mu       sync.Mutex
	calls    []string
	unmapped []string
	opIDs    []string
	merged   [][]string

	// Per-call attribution (set only by t6g5RunNativeWith over a PebbleStore):
	// snap reads every key of the store; skip is extra key prefixes to ignore
	// (the run's own act:op: index); stack holds the calls whose windows are
	// open, innermost last; observed is, per method, the resources the keys
	// changed during its own calls belong to (each with one sample key);
	// attrConcurrent is set when writer calls from two goroutines overlapped,
	// which disables the attribution for the rest of the run.
	snap           func() (map[string]string, error)
	skip           []string
	stack          []*t6g5Frame
	observed       map[string]map[string]string
	attrProblems   []string
	attrConcurrent bool
	mergeThrough   func(opID string, bookIDs []string, primaryID string) (int, error)
}

// t6g5Frame is one writer call whose window is open: the goroutine that made
// it and the keys that calls nested inside it already claimed (they are
// attributed to the nested method, not to this one).
type t6g5Frame struct {
	name    string
	gid     uint64
	claimed map[string]bool
}

// t6g5GoID is the calling goroutine's id, parsed from the stack header.
func t6g5GoID() uint64 {
	var buf [64]byte
	f := strings.Fields(string(buf[:runtime.Stack(buf[:], false)]))
	id, _ := strconv.ParseUint(f[1], 10, 64)
	return id
}

// t6g5ChangedKeys is every key whose value differs between before and after.
func t6g5ChangedKeys(before, after map[string]string) []string {
	var out []string
	for k, v := range after {
		if old, ok := before[k]; !ok || old != v {
			out = append(out, k)
		}
	}
	for k := range before {
		if _, ok := after[k]; !ok {
			out = append(out, k)
		}
	}
	return out
}

// note records a call of name and returns the function to defer, which closes
// the call's window: `defer s.note("X")()`. When the spy holds a store, every
// call is bracketed by a snapshot before and after, and the keys that changed
// are attributed to that method. Calls nest: the delete closure that
// CarryStateThenHardDelete runs arrives as its own DeleteBook call inside the
// outer window, and its keys are attributed to DeleteBook and removed from the
// outer call's diff, matching the table (whose CarryStateThenHardDelete row
// says "del arrives separately as DeleteBook"). Writer calls from two
// goroutines at once cannot be told apart by snapshots, so the first overlap
// turns the attribution off for the run (attrConcurrent), loudly for any test
// that pins a method's writes with requireMethodWrote; the run-wide checks do
// not depend on it.
func (s *t6g5Spy) note(name string) func() {
	s.mu.Lock()
	s.calls = append(s.calls, name)
	if _, ok := t6g5WriterResources[name]; !ok {
		s.unmapped = append(s.unmapped, name)
	}
	off := s.snap == nil || s.attrConcurrent
	s.mu.Unlock()
	if off {
		return func() {}
	}
	gid := t6g5GoID()
	f := &t6g5Frame{name: name, gid: gid, claimed: map[string]bool{}}
	// The frame is pushed BEFORE the snapshot: a writer call from another
	// goroutine that starts in the gap sees this frame and turns the attribution
	// off, instead of landing between "frame open" and "before taken" unnoticed.
	s.mu.Lock()
	if n := len(s.stack); s.attrConcurrent || (n > 0 && s.stack[n-1].gid != gid) {
		s.attrConcurrent = true
		s.stack, s.observed = nil, nil
		s.mu.Unlock()
		return func() {}
	}
	s.stack = append(s.stack, f)
	s.mu.Unlock()
	before, berr := s.snap()
	return func() {
		after, aerr := s.snap()
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.attrConcurrent {
			return
		}
		n := len(s.stack)
		if n == 0 || s.stack[n-1] != f {
			s.attrConcurrent = true
			s.stack, s.observed = nil, nil
			return
		}
		s.stack = s.stack[:n-1]
		if berr != nil || aerr != nil {
			s.attrProblems = append(s.attrProblems, fmt.Sprintf("%s: snapshot failed: %v %v", name, berr, aerr))
			return
		}
		own := after
		if len(f.claimed) > 0 {
			own = make(map[string]string, len(after))
			for k, v := range after {
				own[k] = v
			}
			for k := range f.claimed {
				if v, ok := before[k]; ok {
					own[k] = v
				} else {
					delete(own, k)
				}
			}
		}
		d := t6g5DiffStore(before, own, s.skip...)
		s.attrProblems = append(s.attrProblems, d.problems...)
		if s.observed == nil {
			s.observed = map[string]map[string]string{}
		}
		if s.observed[name] == nil {
			s.observed[name] = map[string]string{}
		}
		for _, res := range d.resources {
			s.observed[name][res] = d.sample[res]
		}
		if len(s.stack) > 0 {
			parent := s.stack[len(s.stack)-1]
			for k := range f.claimed {
				parent.claimed[k] = true
			}
			for _, k := range t6g5ChangedKeys(before, own) {
				parent.claimed[k] = true
			}
		}
	}
}

func (s *t6g5Spy) count(name string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, c := range s.calls {
		if c == name {
			n++
		}
	}
	return n
}

// recorded is every resource a call the spy saw writes, sorted and unique.
func (s *t6g5Spy) recorded() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	seen := map[string]bool{}
	for _, c := range s.calls {
		for _, r := range t6g5WriterResources[c] {
			seen[string(r)] = true
		}
	}
	out := make([]string, 0, len(seen))
	for r := range seen {
		out = append(out, r)
	}
	sort.Strings(out)
	return out
}

func (s *t6g5Spy) ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error) {
	defer s.note("ModifyBook")()
	return s.inner.ModifyBook(id, fn)
}

func (s *t6g5Spy) SetLibraryStateOrganized(id, from string) (bool, bool, error) {
	defer s.note("SetLibraryStateOrganized")()
	return s.inner.SetLibraryStateOrganized(id, from)
}

func (s *t6g5Spy) ModifyBookFile(bookID, fileID string, fn func(*database.BookFile) error) (*database.BookFile, error) {
	defer s.note("ModifyBookFile")()
	return s.inner.ModifyBookFile(bookID, fileID, fn)
}

func (s *t6g5Spy) DeleteBook(id string) error {
	defer s.note("DeleteBook")()
	return s.inner.DeleteBook(id)
}

func (s *t6g5Spy) DeleteBookFilesByIDs(refs []database.BookFileRef) ([]database.BookFile, error) {
	defer s.note("DeleteBookFilesByIDs")()
	return s.inner.DeleteBookFilesByIDs(refs)
}

func (s *t6g5Spy) SetBookAuthors(bookID string, authors []database.BookAuthor) error {
	defer s.note("SetBookAuthors")()
	return s.inner.SetBookAuthors(bookID, authors)
}

func (s *t6g5Spy) SetUserPreferenceForUser(userID, key, value string) error {
	defer s.note("SetUserPreferenceForUser")()
	return s.inner.SetUserPreferenceForUser(userID, key, value)
}

func (s *t6g5Spy) CloneBookIntoLibrary(book *database.Book, files []database.BookFile, opID string) (string, error) {
	defer s.note("CloneBookIntoLibrary")()
	return s.inner.CloneBookIntoLibrary(book, files, opID)
}

func (s *t6g5Spy) Reflink(src, dst string) error {
	defer s.note("Reflink")()
	return s.inner.Reflink(src, dst)
}

func (s *t6g5Spy) SaveChaptersForBook(bookID string, chapters []database.Chapter) error {
	defer s.note("SaveChaptersForBook")()
	return s.inner.SaveChaptersForBook(bookID, chapters)
}

func (s *t6g5Spy) CarryStateBeforeHardDelete(keepID, doomedID string) error {
	defer s.note("CarryStateBeforeHardDelete")()
	return s.inner.CarryStateBeforeHardDelete(keepID, doomedID)
}

func (s *t6g5Spy) HardDeleteWithoutUserState(bookID string, del func() error) error {
	defer s.note("HardDeleteWithoutUserState")()
	return s.inner.HardDeleteWithoutUserState(bookID, del)
}

func (s *t6g5Spy) CarryStateBetweenLiveBooks(toID, fromID string) error {
	defer s.note("CarryStateBetweenLiveBooks")()
	return s.inner.CarryStateBetweenLiveBooks(toID, fromID)
}

func (s *t6g5Spy) DeleteMetadataCache(bookID string) error {
	defer s.note("DeleteMetadataCache")()
	return s.inner.DeleteMetadataCache(bookID)
}

func (s *t6g5Spy) UpdateBookFiles(ctx context.Context, files []*database.BookFile, afterRow func(i int, applied bool)) (int, error) {
	defer s.note("UpdateBookFiles")()
	return s.inner.UpdateBookFiles(ctx, files, afterRow)
}

// MergeBooks records the merge and, unless a test set mergeThrough, does not run
// it: the merge itself is the server's, and what most tests prove is that the
// core asked for it under the run's ID with the right books. A test that needs
// the merge's own store writes (the store-diff test of merge-same-path-dupes)
// sets mergeThrough to a real merge.Service over the same store.
func (s *t6g5Spy) MergeBooks(opID string, bookIDs []string, primaryID string) (int, error) {
	defer s.note("MergeBooks")()
	s.mu.Lock()
	s.opIDs = append(s.opIDs, opID)
	s.merged = append(s.merged, append([]string{primaryID}, bookIDs...))
	through := s.mergeThrough
	s.mu.Unlock()
	if through != nil {
		return through(opID, bookIDs, primaryID)
	}
	return len(bookIDs) - 1, nil
}

func (s *t6g5Spy) MoveBookFilesToBookBulk(moves []database.BookFileMove, targetBookID string) error {
	defer s.note("MoveBookFilesToBookBulk")()
	return s.inner.MoveBookFilesToBookBulk(moves, targetBookID)
}

func (s *t6g5Spy) MoveBookFilesToBook(fileIDs []string, sourceBookID, targetBookID string) error {
	defer s.note("MoveBookFilesToBook")()
	return s.inner.MoveBookFilesToBook(fileIDs, sourceBookID, targetBookID)
}

func (s *t6g5Spy) DeleteNarrator(id int) error {
	defer s.note("DeleteNarrator")()
	return s.inner.DeleteNarrator(id)
}

func (s *t6g5Spy) InvalidateAuthorsCache() {
	defer s.note("InvalidateAuthorsCache")()
	s.inner.InvalidateAuthorsCache()
}

func (s *t6g5Spy) RecordOperationChange(c *database.OperationChange) error {
	defer s.note("RecordOperationChange")()
	return s.inner.RecordOperationChange(c)
}

func (s *t6g5Spy) CreateNarrator(name string) (*database.Narrator, error) {
	defer s.note("CreateNarrator")()
	return s.inner.CreateNarrator(name)
}

func (s *t6g5Spy) SetBookNarrators(bookID string, narrators []database.BookNarrator) error {
	defer s.note("SetBookNarrators")()
	return s.inner.SetBookNarrators(bookID, narrators)
}

func (s *t6g5Spy) ImportSmartPlaylists(lib *itunes.ITLLibrary, ownerUserID string) itunesservice.PlaylistImportResult {
	defer s.note("ImportSmartPlaylists")()
	return s.inner.ImportSmartPlaylists(lib, ownerUserID)
}

func (s *t6g5Spy) UpdateBook(id string, book *database.Book) (*database.Book, error) {
	defer s.note("UpdateBook")()
	return s.inner.UpdateBook(id, book)
}

func (s *t6g5Spy) RecomputeBookAggregates(bookID string) error {
	defer s.note("RecomputeBookAggregates")()
	return s.inner.RecomputeBookAggregates(bookID)
}

func (s *t6g5Spy) CreateBook(book *database.Book) (*database.Book, error) {
	defer s.note("CreateBook")()
	return s.inner.CreateBook(book)
}

func (s *t6g5Spy) ReassignExternalID(source, externalID, newBookID string) error {
	defer s.note("ReassignExternalID")()
	return s.inner.ReassignExternalID(source, externalID, newBookID)
}

func (s *t6g5Spy) NewUserStateProbe() (t6g5RegroupStateProbe, error) {
	defer s.note("NewUserStateProbe")()
	return s.inner.NewUserStateProbe()
}

func (s *t6g5Spy) CarryStateThenHardDelete(keepID, doomedID string, precheck, del func() error) error {
	defer s.note("CarryStateThenHardDelete")()
	return s.inner.CarryStateThenHardDelete(keepID, doomedID, precheck, del)
}

// t6g5NativeRun is one native run's observations.
type t6g5NativeRun struct {
	out    json.RawMessage
	err    error
	spy    *t6g5Spy
	holds  *t6g5Holds
	gate   *t6g5Gate
	handed []tasks.Writer
	runID  string
	def    tasks.Definition
	rc     *tasks.RC
}

// t6g5RunNative runs the native kind of headID live over store, with the spy as
// its writer and root as the library root.
func t6g5RunNative(t *testing.T, store database.Store, root, headID string, scanKey tasks.Key, params string) t6g5NativeRun {
	t.Helper()
	return t6g5RunNativeOn(t, New(fakeDeps{store: store, root: root}), tasks.ModeLive, headID, scanKey, params)
}

// t6g5RunNativeOn is t6g5RunNative over a plugin the test built (its deps
// carry whatever the kind's HEAD adapters call) and a chosen mode.
func t6g5RunNativeOn(t *testing.T, p *Plugin, mode tasks.Mode, headID string, scanKey tasks.Key, params string) t6g5NativeRun {
	t.Helper()
	return t6g5RunNativeWith(t, nil, p, mode, headID, scanKey, params)
}

// t6g5SpyOption configures the spy before a run starts.
type t6g5SpyOption func(*t6g5Spy)

// t6g5MergeThroughStore makes the spy's MergeBooks run a real merge.Service over
// store, the way the server's BookMerger does, so the merge's own writes (the
// sibling journal, the group redirect, the sync-identity rows) reach the store
// the store-diff test watches.
func t6g5MergeThroughStore(store *database.PebbleStore) t6g5SpyOption {
	return func(s *t6g5Spy) {
		s.mergeThrough = func(opID string, bookIDs []string, primaryID string) (int, error) {
			res, err := merge.NewService(store).MergeBooksWithOptions(bookIDs, primaryID, merge.MergeOptions{OperationID: opID})
			if err != nil {
				return 0, err
			}
			return res.SoftDeleted, nil
		}
	}
}

// t6g5RunNativeWith is t6g5RunNativeOn with an optional store to attribute
// writes on: given one, the spy snapshots it around every outermost writer call.
func t6g5RunNativeWith(t *testing.T, store *database.PebbleStore, p *Plugin, mode tasks.Mode, headID string, scanKey tasks.Key, params string, opts ...t6g5SpyOption) t6g5NativeRun {
	t.Helper()
	b := p.t6g5HeadBase(context.Background(), &fakeReporter{}, true)
	spy := &t6g5Spy{inner: b.Writer}
	if store != nil {
		spy.snap = func() (map[string]string, error) { return t6g5SnapshotOf(store) }
	}
	for _, o := range opts {
		o(spy)
	}
	var mu sync.Mutex
	var handed []tasks.Writer
	deps := t6g5Deps{
		Store: b.Store,
		NewWriter: func(w tasks.Writer) t6g5Writer {
			mu.Lock()
			handed = append(handed, w)
			mu.Unlock()
			return spy
		},
		ScanKey: scanKey,
	}
	d := t6g5KindByName(t, p.t6g5Kinds(deps, nil), headID)
	gate := &t6g5Gate{id: headID}
	holds := &t6g5Holds{}
	rc, _ := newT6G5HostCtx(context.Background(), d.Kind(), mode, gate, holds)
	spy.skip = []string{"act:op:" + string(rc.RunID())}
	out, err := t6g5Run(t, d, rc, params)
	mu.Lock()
	defer mu.Unlock()
	return t6g5NativeRun{out: out, err: err, spy: spy, holds: holds, gate: gate, handed: slices.Clone(handed), runID: string(rc.RunID()), def: d, rc: rc}
}

// t6g5TB is the part of testing.T requireWriter uses, so a test can hand it a
// recording double and see it fail.
type t6g5TB interface {
	require.TestingT
	Helper()
}

// requireWriter proves the shell built its writer from this run's rc.Writer(),
// and that every resource a write reached is one the kind declares: the
// resources the spy saw must be a subset of the kind's Effects.WriteSet(), and
// the spy must have met no method it has no mapping for. A run that recorded
// nothing fails: the subset check is vacuous over an empty record.
func (r t6g5NativeRun) requireWriter(t *testing.T) { t.Helper(); r.requireWriterOn(t) }

func (r t6g5NativeRun) requireWriterOn(t t6g5TB) {
	t.Helper()
	require.Len(t, r.handed, 1, "NewWriter is called once per run")
	assert.Same(t, r.gate, r.handed[0], "NewWriter is handed this run's rc.Writer()")
	assert.Empty(t, r.spy.unmapped, "every writer method the run called has a resource mapping")
	require.NotEmpty(t, r.spy.recorded(), "a live run of a writing kind wrote something, or the subset check below proves nothing")
	declared := t6g5WriteKinds(t, tasks.CommonOf(r.def).Effects)
	for _, res := range r.spy.recorded() {
		assert.Contains(t, declared, res, "the run wrote %q, which %s does not declare in its Effects", res, r.def.Kind())
	}
}

// t6g5RecordingTB records the failures requireWriter reports instead of failing
// the test, and stops the goroutine on FailNow as testing.T does.
type t6g5RecordingTB struct {
	mu   sync.Mutex
	msgs []string
}

func (*t6g5RecordingTB) Helper() {}
func (r *t6g5RecordingTB) Errorf(format string, args ...any) {
	r.mu.Lock()
	r.msgs = append(r.msgs, fmt.Sprintf(format, args...))
	r.mu.Unlock()
}
func (*t6g5RecordingTB) FailNow() { runtime.Goexit() }

// requireWriter must fail a run that wrote nothing, and a run whose spy recorded
// a resource the kind does not declare, so a vacuous or over-wide live test cannot
// pass the subset check.
func TestT6G5RequireWriter_FailsOnAnEmptyRecordAndOnAnUndeclaredResource(t *testing.T) {
	d := t6g5KindByName(t, (&Plugin{}).t6g5Kinds(fakeT6G5Deps(t), nil), "maintenance.metadata-cache-reap")
	gate := &t6g5Gate{id: "meta"}
	run := func(calls ...string) []string {
		r := t6g5NativeRun{spy: &t6g5Spy{calls: calls}, gate: gate, handed: []tasks.Writer{gate}, def: d}
		rec := &t6g5RecordingTB{}
		done := make(chan struct{})
		go func() { defer close(done); r.requireWriterOn(rec) }()
		<-done
		return rec.msgs
	}
	assert.Empty(t, run("DeleteMetadataCache"), "a recorded write of a declared resource passes")
	empty := run()
	require.NotEmpty(t, empty, "a run that recorded nothing is a failure")
	assert.Contains(t, empty[0], "wrote something")
	undeclared := run("DeleteMetadataCache", "SetBookAuthors")
	require.NotEmpty(t, undeclared, "a recorded resource outside the kind's Effects is a failure")
	assert.Contains(t, undeclared[0], "does not declare")
}

// requireHold proves the run stood the scan down under the configured key and
// released it.
func (r t6g5NativeRun) requireHold(t *testing.T) {
	t.Helper()
	keys, released := r.holds.snapshot()
	assert.Equal(t, []tasks.Key{t6g5ScanKey}, keys, "the scan stand-down is taken under the configured key")
	assert.Equal(t, 1, released, "and released")
}

func TestT6G5Native_MoveBookFileRows(t *testing.T) {
	f := newMBFFixture(t)
	r, d := t6g5RunNativeDiff(t, f.s, New(fakeDeps{store: f.s, root: t.TempDir()}), tasks.ModeLive, "maintenance.move-book-file-rows", t6g5ScanKey,
		`{"moves":[{"row_id":"`+f.ch01+`","to_book_id":"`+f.dst+`"}]}`)
	require.NoError(t, r.err)
	r.requireWriter(t)
	r.requireStoreWritesWithin(t, d, string(ops.ResBookFiles))
	r.requireHold(t)
	var rep mbfReport
	require.NoError(t, json.Unmarshal(r.out, &rep))
	assert.False(t, rep.DryRun)
	assert.Equal(t, 1, rep.Moved)
	assert.Equal(t, 1, r.spy.count("MoveBookFilesToBookBulk"), "the move reached the writer")
	files, err := f.s.GetBookFiles(f.dst)
	require.NoError(t, err)
	var ids []string
	for _, bf := range files {
		ids = append(ids, bf.ID)
	}
	assert.Contains(t, ids, f.ch01, "the row now belongs to the target")
}

func TestT6G5Native_MarkMissingFiles(t *testing.T) {
	s := newRepairPebble(t)
	dir := t.TempDir()
	gone := filepath.Join(dir, "gone.mp3")
	_, err := s.CreateBook(&database.Book{ID: "01BATMAN00000000000000MM01", Title: "Year One", FilePath: dir})
	require.NoError(t, err)
	require.NoError(t, s.CreateBookFile(&database.BookFile{ID: "01ROWMM0000000000000000001", BookID: "01BATMAN00000000000000MM01", FilePath: gone}))

	r, d := t6g5RunNativeDiff(t, s, New(fakeDeps{store: s, root: dir}), tasks.ModeLive, "maintenance.mark-missing-files", t6g5ScanKey,
		`{"reportPath":"`+filepath.Join(dir, "r.tsv")+`"}`)
	require.NoError(t, r.err)
	r.requireWriter(t)
	r.requireStoreWritesWithin(t, d, string(ops.ResBookFiles))
	r.requireHold(t)
	var plan markMissingPlan
	require.NoError(t, json.Unmarshal(r.out, &plan))
	assert.True(t, plan.Apply)
	assert.Equal(t, 1, plan.WouldMarkMissing)
	assert.Equal(t, 1, plan.MarkedMissing)
	assert.Equal(t, 1, r.spy.count("UpdateBookFiles"), "the flag write reached the writer")
	files, err := s.GetBookFiles("01BATMAN00000000000000MM01")
	require.NoError(t, err)
	require.Len(t, files, 1)
	assert.True(t, files[0].Missing)
}

func TestT6G5Native_MissingFileRepoint(t *testing.T) {
	s := newRepairPebble(t)
	dir := t.TempDir()
	real := filepath.Join(dir, "Year One - 02.mp3")
	require.NoError(t, os.WriteFile(real, make([]byte, 1234), 0o644))
	broken := filepath.Join(dir, "Year One - 2", "35.mp3")
	const book = "01BATMAN00000000000000RP01"
	_, err := s.CreateBook(&database.Book{ID: book, Title: "The Long Halloween", FilePath: dir})
	require.NoError(t, err)
	require.NoError(t, s.CreateBookFile(&database.BookFile{ID: "01ROWRP0000000000000000001", BookID: book, FilePath: broken, FileSize: 1234}))

	r, d := t6g5RunNativeDiff(t, s, New(fakeDeps{store: s, root: dir}), tasks.ModeLive, "maintenance.missing-file-repoint", t6g5ScanKey,
		`{"reportPath":"`+filepath.Join(dir, "r.tsv")+`"}`)
	require.NoError(t, r.err)
	r.requireWriter(t)
	r.requireStoreWritesWithin(t, d, string(ops.ResBookFiles))
	r.requireHold(t)
	var plan repointPlan
	require.NoError(t, json.Unmarshal(r.out, &plan))
	assert.Equal(t, 1, plan.Repointed)
	assert.Equal(t, 1, r.spy.count("UpdateBookFiles"))
	files, err := s.GetBookFiles(book)
	require.NoError(t, err)
	require.Len(t, files, 1)
	assert.Equal(t, real, files[0].FilePath)
}

func TestT6G5Native_MergeSamePathDupes(t *testing.T) {
	s := newRepairPebble(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "Bruce Wayne", "Hush.m4b")
	ids := []string{"01BATMAN00000000000000MG01", "01BATMAN00000000000000MG02"}
	for _, id := range ids {
		_, err := s.CreateBook(&database.Book{ID: id, Title: "Hush", FilePath: path})
		require.NoError(t, err)
		require.NoError(t, s.CreateBookFile(&database.BookFile{BookID: id, FilePath: path, FileHash: "H"}))
	}
	// The merge runs for real (merge.Service over the same store), so the store
	// diff sees the merge's own journal, redirect and sync rows, not the stub's none.
	r, d := t6g5RunNativeDiff(t, s, New(fakeDeps{store: s, root: dir}), tasks.ModeLive, "maintenance.merge-same-path-dupes", t6g5ScanKey,
		`{"reportPath":"`+filepath.Join(dir, "r.tsv")+`"}`, t6g5MergeThroughStore(s))
	require.NoError(t, r.err)
	r.requireWriter(t)
	r.requireStoreWritesWithin(t, d, string(ops.ResBooks))
	r.requireMethodWrote(t, "MergeBooks", string(ops.ResBooks))
	keys, _ := r.holds.snapshot()
	assert.Empty(t, keys, "the merge takes no scan stand-down (HEAD took none)")
	var plan mergeSamePathPlan
	require.NoError(t, json.Unmarshal(r.out, &plan))
	assert.True(t, plan.Apply)
	assert.Equal(t, 1, plan.Merged)
	require.Equal(t, 1, r.spy.count("MergeBooks"), "the merge reached the writer")
	assert.Equal(t, []string{r.runID}, r.spy.opIDs, "merged under the run's own ID")
	assert.ElementsMatch(t, ids, r.spy.merged[0][1:])
	deleted := 0
	for _, id := range ids {
		b, err := s.GetBookByID(id)
		require.NoError(t, err)
		if b == nil || b.IsSoftDeleted() {
			deleted++
		}
	}
	assert.Equal(t, 1, deleted, "the real merge retired exactly one of the two rows")
}

func TestT6G5Native_MetadataCacheReap(t *testing.T) {
	s := newRepairPebble(t)
	dir := t.TempDir()
	const orphan = "01BATMAN00000000000000OR01"
	require.NoError(t, s.PutMetadataCache(&database.MetadataCandidateCache{BookID: orphan, FetchedAt: time.Now()}))
	// A live book with a file row and a cache of its own: rows in resources the
	// reap must NOT write (books, book_files) and a cache it must spare, so a
	// reap that also touched the book, its files or a live cache changes a key
	// the value diff can see.
	const live = "01BATMAN00000000000000OR02"
	_, err := s.CreateBook(&database.Book{ID: live, Title: "Under the Red Hood", FilePath: filepath.Join(dir, "Jason Todd")})
	require.NoError(t, err)
	require.NoError(t, s.CreateBookFile(&database.BookFile{ID: "01ROWOR0000000000000000002", BookID: live, FilePath: filepath.Join(dir, "Jason Todd", "01.mp3")}))
	require.NoError(t, s.PutMetadataCache(&database.MetadataCandidateCache{BookID: live, FetchedAt: time.Now()}))

	r, d := t6g5RunNativeDiff(t, s, New(fakeDeps{store: s, root: dir}), tasks.ModeLive, "maintenance.metadata-cache-reap", "",
		`{"reportPath":"`+filepath.Join(dir, "r.tsv")+`"}`)
	require.NoError(t, r.err)
	r.requireWriter(t)
	r.requireStoreWritesWithin(t, d, string(ops.ResMetadataCache))
	assert.Equal(t, []string{string(ops.ResMetadataCache)}, d.resources, "the reap writes the metadata cache and nothing else")
	var plan reapPlan
	require.NoError(t, json.Unmarshal(r.out, &plan))
	assert.Equal(t, 1, plan.Reaped)
	assert.Equal(t, 1, r.spy.count("DeleteMetadataCache"))
	got, err := s.GetMetadataCache(orphan)
	require.NoError(t, err)
	assert.Nil(t, got, "the orphaned row is gone")
	kept, err := s.GetMetadataCache(live)
	require.NoError(t, err)
	assert.NotNil(t, kept, "a live book's cache is spared")
	keys, _ := r.holds.snapshot()
	assert.Empty(t, keys, "the reap takes no scan stand-down")
}

func TestT6G5Native_PurgeEmptyNarrators(t *testing.T) {
	s := newSeriesPhantomStore(t)
	n, err := s.CreateNarrator("Selina Kyle")
	require.NoError(t, err)

	r, d := t6g5RunNativeDiff(t, s, New(fakeDeps{store: s, root: t.TempDir()}), tasks.ModeLive, "maintenance.purge-empty-narrators", t6g5ScanKey, "")
	require.NoError(t, r.err)
	r.requireWriter(t)
	r.requireStoreWritesWithin(t, d, string(ops.ResBooks), string(ops.ResOperations))
	r.requireHold(t)
	var rep emptyNarratorReport
	require.NoError(t, json.Unmarshal(r.out, &rep))
	assert.Equal(t, 1, rep.Deleted)
	assert.Equal(t, 1, r.spy.count("DeleteNarrator"))
	assert.Equal(t, 1, r.spy.count("RecordOperationChange"), "the undo-ledger row precedes the delete")
	got, err := s.GetNarratorByID(n.ID)
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestT6G5Native_SplitJoinedNarrators(t *testing.T) {
	f := newSplitJoinedFixture(t)
	r, d := t6g5RunNativeDiff(t, f.s, New(fakeDeps{store: f.s, root: t.TempDir()}), tasks.ModeLive, "maintenance.split-joined-narrators", t6g5ScanKey, "")
	require.NoError(t, r.err)
	r.requireWriter(t)
	r.requireStoreWritesWithin(t, d, string(ops.ResBooks), string(ops.ResOperations))
	r.requireHold(t)
	var rep splitJoinedReport
	require.NoError(t, json.Unmarshal(r.out, &rep))
	assert.Positive(t, rep.BooksRewritten)
	assert.Positive(t, r.spy.count("SetBookNarrators"), "the rewrite reached the writer")
	assert.Positive(t, r.spy.count("RecordOperationChange"))
}

func TestT6G5Native_RepairLibraryState(t *testing.T) {
	s := newRepairPebble(t)
	root := t.TempDir()
	state, hash := libStateImported, "deadbeef"
	b, err := s.CreateBook(&database.Book{ID: "01BATMAN00000000000000LS01", Title: "Arkham Asylum",
		FilePath: filepath.Join(root, "Bruce Wayne", "Arkham Asylum", "01.mp3"), LibraryState: &state, OrganizedFileHash: &hash})
	require.NoError(t, err)
	// A cache row for the book, a resource the repair must NOT write: a repair
	// that also dropped or stamped it changes a key the diff can see.
	require.NoError(t, s.PutMetadataCache(&database.MetadataCandidateCache{BookID: b.ID, FetchedAt: time.Now()}))

	r, d := t6g5RunNativeDiff(t, s, New(fakeDeps{store: s, root: root}), tasks.ModeLive, "maintenance.repair-library-state", "", "")
	require.NoError(t, r.err)
	r.requireWriter(t)
	r.requireStoreWritesWithin(t, d, string(ops.ResBooks))
	assert.Equal(t, []string{string(ops.ResBooks)}, d.resources, "the repair writes the books resource and nothing else: its one write sets LibraryState, which trips no metadata-cache stale stamp")
	var res libraryStateRepairResult
	require.NoError(t, json.Unmarshal(r.out, &res))
	assert.False(t, res.DryRun)
	assert.Equal(t, 1, res.Outcomes[lsrRepaired])
	assert.Equal(t, 1, r.spy.count("SetLibraryStateOrganized"))
	got, err := s.GetBookByID(b.ID)
	require.NoError(t, err)
	require.NotNil(t, got.LibraryState)
	assert.Equal(t, libStateOrganized, *got.LibraryState)
	keys, _ := r.holds.snapshot()
	assert.Empty(t, keys, "the repair takes no scan stand-down")
}

// A native run of a kind that holds the scan refuses with no key configured
// and writes nothing: the refusal is the first thing the apply phase does.
func TestT6G5Native_LeaseKindsRefuseWithoutAScanKey(t *testing.T) {
	f := newMBFFixture(t)
	r := t6g5RunNative(t, f.s, t.TempDir(), "maintenance.move-book-file-rows", "",
		`{"moves":[{"row_id":"`+f.ch01+`","to_book_id":"`+f.dst+`"}]}`)
	require.Error(t, r.err)
	assert.Contains(t, r.err.Error(), "no scan stand-down key configured")
	assert.Zero(t, r.spy.count("MoveBookFilesToBookBulk"))
}

// --- the HEAD shells of the same kinds, over a real store ---

func TestT6G5Head_RepairLibraryStateApplies(t *testing.T) {
	s := newRepairPebble(t)
	root := t.TempDir()
	state, hash := libStateImported, "deadbeef"
	b, err := s.CreateBook(&database.Book{ID: "01BATMAN00000000000000LS02", Title: "Arkham Knight",
		FilePath: filepath.Join(root, "Bruce Wayne", "Arkham Asylum", "01.mp3"), LibraryState: &state, OrganizedFileHash: &hash})
	require.NoError(t, err)
	p := New(fakeDeps{store: s, root: root})

	preview := &resultReporter{}
	require.NoError(t, p.repairLibraryStateDef().Run(context.Background(), []byte(`{}`), preview))
	got, err := s.GetBookByID(b.ID)
	require.NoError(t, err)
	assert.Equal(t, libStateImported, *got.LibraryState, "the default is a dry run and writes nothing")

	live := &resultReporter{}
	require.NoError(t, p.repairLibraryStateDef().Run(context.Background(), []byte(`{"dry_run":false}`), live))
	got, err = s.GetBookByID(b.ID)
	require.NoError(t, err)
	assert.Equal(t, libStateOrganized, *got.LibraryState)

	err = p.repairLibraryStateDef().Run(context.Background(), []byte(`{"dry_run":false,"dryRun":true}`), &resultReporter{})
	require.Error(t, err, "disagreeing mode keys are refused")
}

func TestT6G5Head_MarkMissingFilesApplies(t *testing.T) {
	s := newRepairPebble(t)
	dir := t.TempDir()
	const book = "01BATMAN00000000000000MM02"
	_, err := s.CreateBook(&database.Book{ID: book, Title: "Knightfall", FilePath: dir})
	require.NoError(t, err)
	require.NoError(t, s.CreateBookFile(&database.BookFile{ID: "01ROWMM0000000000000000002", BookID: book, FilePath: filepath.Join(dir, "gone.mp3")}))
	p := New(fakeDeps{store: s, root: dir})
	report := filepath.Join(dir, "r.tsv")

	require.NoError(t, p.markMissingFilesDef().Run(context.Background(), []byte(`{"reportPath":"`+report+`"}`), &resultReporter{}))
	files, err := s.GetBookFiles(book)
	require.NoError(t, err)
	assert.False(t, files[0].Missing, "no apply, no write")

	require.NoError(t, p.markMissingFilesDef().Run(context.Background(), []byte(`{"apply":true,"reportPath":"`+report+`"}`), &resultReporter{}))
	files, err = s.GetBookFiles(book)
	require.NoError(t, err)
	assert.True(t, files[0].Missing)
}

// --- clone: a native apply and the rollback the review found refused ---

// t6g5ClonePlugin is a plugin over the clone fixture's deps (the cloner and
// the library root the fixture set up), for the native runs.
func t6g5ClonePlugin(f *icFixture) *Plugin {
	return New(icTestDeps{fakeDeps: fakeDeps{store: f.s, root: f.root}, s: f.s, root: f.root})
}

func TestT6G5Native_CloneApply(t *testing.T) {
	f := newICFixture(t)
	src := f.itunes("Year One.m4b")
	f.book(t, "T", "vg-t", "Year One", []string{src})

	r, d := t6g5RunNativeDiff(t, f.s, t6g5ClonePlugin(f), tasks.ModeLive, "itunes.clone-into-library", t6g5ScanKey,
		`{"apply":true,"group_ids":["vg-t"]}`)
	require.NoError(t, r.err)
	r.requireWriter(t)
	// operations: CreateOrganizedVersion journals its changes under the run's op ID
	// (opchange: keys), a write no method name says. The clone's own undo record
	// (a user_state preference) is judged like any write but is a self record
	// (t6g5SelfRecordKeys), so it cannot stand in for a must-write user_state, which
	// is not asserted here: the call count below proves the record reached the
	// writer, and the per-call check holds SetUserPreferenceForUser to its row.
	r.requireStoreWritesWithin(t, d, string(ops.ResBooks), string(ops.ResBookFiles), string(ops.ResOperations))
	r.requireHold(t)
	var rep icReport
	require.NoError(t, json.Unmarshal(r.out, &rep))
	g := icGroup(t, &rep, "vg-t")
	require.Equal(t, icOutcomeApplied, g.Outcome, g.Error)
	assert.Equal(t, 1, r.spy.count("CloneBookIntoLibrary"), "the clone reached the writer")
	assert.Positive(t, r.spy.count("SetUserPreferenceForUser"), "the undo record reached the writer")
	owner, err := f.s.GetBookFileByPID("PID-T0")
	require.NoError(t, err)
	assert.Equal(t, g.CloneBookID, owner.BookID, "the library copy owns the PID")
}

// A mixed book (one file already in the library, one under the iTunes tree) is
// completed in place by reflinking only the iTunes file. That clone must reach
// the run's writer as Reflink, which changes bytes on disk and so maps to
// files: with an empty Reflink entry in the spy map the declared write set
// could lose files and no test would notice.
func TestT6G5Native_CloneMixedApplyReflinksThroughTheWriter(t *testing.T) {
	f := newICFixture(t)
	// "Author" is the folder icTestDeps.plan lands every clone under, not a fixture name.
	lib := filepath.Join(f.root, "Author", "The Long Halloween", "01.mp3")
	it := f.itunes("02.mp3")
	f.book(t, "M", "vg-m", "The Long Halloween", []string{lib, it})
	p := t6g5ClonePlugin(f)
	p.reflinkFile = fileops.CopyFileIngestExclusive // a plain exclusive copy: CI filesystems cannot clone

	r := t6g5RunNativeOn(t, p, tasks.ModeLive, "itunes.clone-into-library", t6g5ScanKey,
		`{"apply":true,"group_ids":["vg-m"]}`)
	require.NoError(t, r.err)
	r.requireWriter(t)
	r.requireHold(t)
	var rep icReport
	require.NoError(t, json.Unmarshal(r.out, &rep))
	g := icGroup(t, &rep, "vg-m")
	require.Equal(t, icKindMixed, g.Kind)
	require.Equal(t, icOutcomeApplied, g.Outcome, g.Reason+g.Error)
	assert.Equal(t, 1, r.spy.count("Reflink"), "the iTunes file was cloned through the writer")
	assert.Contains(t, r.spy.recorded(), string(ops.ResFiles), "the clone's bytes on disk are a recorded write")
	_, err := os.Stat(filepath.Join(f.root, "Author", "The Long Halloween", "02.mp3"))
	require.NoError(t, err, "the clone landed in the library")
}

// The rollback reads the run's store for the retired rows
// (database.RefuseRetiredShell). With the native env's `reads` unset every
// rollback is refused before it writes anything; this run must complete.
func TestT6G5Native_CloneRollback(t *testing.T) {
	f := newICFixture(t)
	src := f.itunes("Year One.m4b")
	f.book(t, "T", "vg-t", "Year One", []string{src})
	g := icGroup(t, f.run(t, icParams{Apply: true, GroupIDs: []string{"vg-t"}}), "vg-t")
	require.Equal(t, icOutcomeApplied, g.Outcome, g.Error)
	cloneID := g.CloneBookID
	require.NotEmpty(t, cloneID)

	r, d := t6g5RunNativeDiff(t, f.s, t6g5ClonePlugin(f), tasks.ModeLive, "itunes.clone-into-library", t6g5ScanKey,
		`{"rollback":true,"group_ids":["vg-t"]}`)
	require.NoError(t, r.err)
	r.requireWriter(t)
	r.requireStoreWritesWithin(t, d, string(ops.ResBooks), string(ops.ResBookFiles))
	r.requireHold(t)
	var rep icReport
	require.NoError(t, json.Unmarshal(r.out, &rep))
	rb := icGroup(t, &rep, "vg-t")
	require.Equal(t, icOutcomeRolled, rb.Outcome, "the rollback was not refused: "+rb.Error)
	assert.Zero(t, r.spy.count("CloneBookIntoLibrary"))
	assert.Positive(t, r.spy.count("ModifyBookFile"), "the PID went back through the writer")
	assert.Positive(t, r.spy.count("DeleteBookFilesByIDs"))
	assert.Positive(t, r.spy.count("DeleteBook"), "the clone book was removed through the writer")
	assert.Positive(t, r.spy.count("SaveChaptersForBook"))
	assert.Positive(t, r.spy.count("SetUserPreferenceForUser"), "the record was cleared through the writer")

	owner, err := f.s.GetBookFileByPID("PID-T0")
	require.NoError(t, err)
	assert.Equal(t, "T", owner.BookID, "the PID is back on the source row")
	gone, err := f.s.GetBookByID(cloneID)
	require.NoError(t, err)
	assert.True(t, gone == nil || gone.IsSoftDeleted(), "clone book removed")
	tb, err := f.s.GetBookByID("T")
	require.NoError(t, err)
	assert.Equal(t, "organized", *tb.LibraryState)
}

// A rollback carries the clone's listening state back onto the source. When the
// clone's reader had FINISHED it, the carry also moves the iTunes play-count mark
// onto the source's book row (carryPlayCountMark), so CarryStateBeforeHardDelete
// writes `books` as well as `user_state`. The plain rollback has no state to
// carry, so the row's `books` entry was never exercised by any run: dropping it
// from t6g5WriterResources passed every check. Here the per-call attribution
// sees the carry's own books write against its own row.
func TestT6G5Native_CloneRollbackCarriesAFinishedReadersPlayCountMark(t *testing.T) {
	f := newICFixture(t)
	src := f.itunes("Year One.m4b")
	f.book(t, "T", "vg-t", "Year One", []string{src})
	g := icGroup(t, f.run(t, icParams{Apply: true, GroupIDs: []string{"vg-t"}}), "vg-t")
	require.Equal(t, icOutcomeApplied, g.Outcome, g.Error)
	cloneID := g.CloneBookID
	require.NotEmpty(t, cloneID)

	u, err := f.s.CreateUser("alfred", "alfred@example.com", "argon2id", "x", []string{"user"}, "active")
	require.NoError(t, err)
	require.NoError(t, f.s.SetUserBookState(&database.UserBookState{UserID: u.ID, BookID: cloneID, Status: database.UserBookStatusFinished, ProgressPct: 100}))
	mark := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	_, err = f.s.ModifyBook(cloneID, func(b *database.Book) error { b.ITunesPlayCountBumpedAt = &mark; return nil })
	require.NoError(t, err)

	r, d := t6g5RunNativeDiff(t, f.s, t6g5ClonePlugin(f), tasks.ModeLive, "itunes.clone-into-library", t6g5ScanKey,
		`{"rollback":true,"group_ids":["vg-t"]}`)
	require.NoError(t, r.err)
	r.requireWriter(t)
	r.requireStoreWritesWithin(t, d, string(ops.ResBooks), string(ops.ResBookFiles), string(ops.ResUserState))
	var rep icReport
	require.NoError(t, json.Unmarshal(r.out, &rep))
	rb := icGroup(t, &rep, "vg-t")
	require.Equal(t, icOutcomeRolled, rb.Outcome, "the rollback was not refused: "+rb.Error)
	require.Equal(t, 1, r.spy.count("CarryStateBeforeHardDelete"), "the state carry reached the writer")
	r.requireMethodWrote(t, "CarryStateBeforeHardDelete", string(ops.ResBooks), string(ops.ResUserState))
	tb, err := f.s.GetBookByID("T")
	require.NoError(t, err)
	require.NotNil(t, tb.ITunesPlayCountBumpedAt, "the finished reader's play-count mark landed on the source")
	assert.True(t, tb.ITunesPlayCountBumpedAt.Equal(mark))
}

// --- regroup ---

// t6g5RegroupXML is an iTunes export holding the two audiobook tracks
// regroupWithState's fragments carry (PIDs p1 and p2), under one album.
func t6g5RegroupXML(t *testing.T) string {
	t.Helper()
	track := func(id int, pid string) string {
		return fmt.Sprintf(`<key>%d</key><dict><key>Track ID</key><integer>%d</integer>`+
			`<key>Persistent ID</key><string>%s</string><key>Name</key><string>Chapter %d</string>`+
			`<key>Artist</key><string>Frank Miller</string><key>Album</key><string>Merged Book</string>`+
			`<key>Genre</key><string>Audiobook</string><key>Track Number</key><integer>%d</integer></dict>`, id, id, pid, id, id)
	}
	body := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict><key>Application Version</key><string>12.13.10.3</string>
<key>Tracks</key><dict>` + track(1, "p1") + track(2, "p2") + `</dict></dict></plist>`
	path := filepath.Join(t.TempDir(), "iTunes Library.xml")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	return path
}

// The plan deletes the emptied fragment, which carries a user's listening
// state: the carry and the delete must both go through the run's writer, the
// state landing on the surviving book.
func TestT6G5Native_Regroup(t *testing.T) {
	s, u, plan, _ := regroupWithState(t)
	loser, target := plan.DeleteBooks[0], plan.Groups[0].Target
	xml := t6g5RegroupXML(t)

	r, d := t6g5RunNativeDiff(t, s, New(fakeDeps{store: s, root: rgRoot}), tasks.ModeLive, "itunes.regroup", "",
		`{"xmlPath":"`+xml+`"}`)
	require.NoError(t, r.err)
	r.requireWriter(t)
	r.requireStoreWritesWithin(t, d, string(ops.ResBooks), string(ops.ResUserState))
	keys, _ := r.holds.snapshot()
	assert.Empty(t, keys, "the regroup takes no scan stand-down (HEAD took none)")
	assert.Positive(t, r.spy.count("NewUserStateProbe"), "the delete pass listed users through the writer")
	assert.Equal(t, 1, r.spy.count("CarryStateThenHardDelete"), "the carry and delete went through the writer")
	assert.Positive(t, r.spy.count("MoveBookFilesToBook")+r.spy.count("MoveBookFilesToBookBulk"), "the files moved through the writer")
	assert.Positive(t, r.spy.count("DeleteBook"), "the delete reached the writer")
	b, err := s.GetBookByID(loser)
	require.NoError(t, err)
	assert.Nil(t, b, "the emptied fragment is deleted")
	pos, err := s.ListUserPositionsForBook(u.ID, target)
	require.NoError(t, err)
	assert.NotEmpty(t, pos, "the user's state landed on the surviving book")
}

// The regroup's delete pass carries the loser's state through
// CarryStateThenHardDelete. With a FINISHED loser that carries its iTunes
// play-count mark, the carry stamps the mark on the target's book row, a books
// write of the carry's own (the delete closure arrives as DeleteBook and is
// attributed to that method). regroupWithState seeds an in-progress loser, so
// without this fixture the row's `books` entry was never exercised.
func TestT6G5Native_RegroupCarriesAFinishedLosersPlayCountMark(t *testing.T) {
	s, u, plan, _ := regroupWithState(t)
	loser, target := plan.DeleteBooks[0], plan.Groups[0].Target
	require.NoError(t, s.SetUserBookState(&database.UserBookState{UserID: u.ID, BookID: loser, Status: database.UserBookStatusFinished, ProgressPct: 100}))
	mark := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	_, err := s.ModifyBook(loser, func(b *database.Book) error { b.ITunesPlayCountBumpedAt = &mark; return nil })
	require.NoError(t, err)

	r, d := t6g5RunNativeDiff(t, s, New(fakeDeps{store: s, root: rgRoot}), tasks.ModeLive, "itunes.regroup", "",
		`{"xmlPath":"`+t6g5RegroupXML(t)+`"}`)
	require.NoError(t, r.err)
	r.requireWriter(t)
	r.requireStoreWritesWithin(t, d, string(ops.ResBooks), string(ops.ResUserState))
	require.Equal(t, 1, r.spy.count("CarryStateThenHardDelete"), "the carry went through the writer")
	r.requireMethodWrote(t, "CarryStateThenHardDelete", string(ops.ResBooks), string(ops.ResUserState))
	tb, err := s.GetBookByID(target)
	require.NoError(t, err)
	require.NotNil(t, tb.ITunesPlayCountBumpedAt, "the finished loser's play-count mark landed on the target")
	assert.True(t, tb.ITunesPlayCountBumpedAt.Equal(mark))
}

func TestT6G5Native_RegroupPreviewWritesNothing(t *testing.T) {
	s, _, plan, _ := regroupWithState(t)
	loser := plan.DeleteBooks[0]
	r, d := t6g5RunNativeDiff(t, s, New(fakeDeps{store: s, root: rgRoot}), tasks.ModePreview, "itunes.regroup", "",
		`{"xmlPath":"`+t6g5RegroupXML(t)+`"}`)
	require.NoError(t, r.err)
	r.requireNoStoreWrites(t, d)
	b, err := s.GetBookByID(loser)
	require.NoError(t, err)
	assert.NotNil(t, b)
}

// HEAD's regroup shell resolves the root from config and the mode from the
// request, and applies through the plugin's own store.
func TestT6G5Head_RegroupApplies(t *testing.T) {
	s, u, plan, _ := regroupWithState(t)
	loser, target := plan.DeleteBooks[0], plan.Groups[0].Target
	prev := config.AppConfig.RootDir
	config.AppConfig.RootDir = rgRoot
	t.Cleanup(func() { config.AppConfig.RootDir = prev })
	p := New(fakeDeps{store: s})
	xml := t6g5RegroupXML(t)

	require.NoError(t, p.itunesRegroupDef().Run(context.Background(), []byte(`{"xmlPath":"`+xml+`"}`), &fakeReporter{}))
	b, err := s.GetBookByID(loser)
	require.NoError(t, err)
	assert.NotNil(t, b, "no dry_run:false, no write")

	require.NoError(t, p.itunesRegroupDef().Run(context.Background(), []byte(`{"dry_run":false,"xmlPath":"`+xml+`"}`), &fakeReporter{}))
	b, err = s.GetBookByID(loser)
	require.NoError(t, err)
	assert.Nil(t, b, "the live run deleted the emptied fragment")
	pos, err := s.ListUserPositionsForBook(u.ID, target)
	require.NoError(t, err)
	assert.NotEmpty(t, pos)
}

// --- playlist import ---

// t6g5SmartBlob is a Smart Criteria blob the importer can decode and cannot
// translate (the format is only partly mapped), so a live import must say
// allowEmptyQueries.
func t6g5SmartBlob(operand string) []byte {
	b := make([]byte, 136+56)
	copy(b, "SLst")
	binary.BigEndian.PutUint32(b[136:], 3)
	binary.BigEndian.PutUint32(b[140:], 0x01000002)
	binary.BigEndian.PutUint32(b[136+52:], uint32(len(operand)*2))
	for _, r := range operand {
		b = binary.BigEndian.AppendUint16(b, uint16(r))
	}
	return append(b, 0, 0, 0, 0)
}

const (
	t6g5PlaylistPID    = "AA00000000000001"
	t6g5PlaylistPIDKey = "aa00000000000001"
)

// t6g5PlaylistXML is an iTunes export with one smart playlist.
func t6g5PlaylistXML(t *testing.T) string {
	t.Helper()
	body := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict><key>Application Version</key><string>12.13.10.3</string>
<key>Playlists</key><array><dict><key>Name</key><string>Gotham Nights</string>
<key>Playlist Persistent ID</key><string>` + t6g5PlaylistPID + `</string>
<key>Smart Info</key><data>AQID</data>
<key>Smart Criteria</key><data>` + base64.StdEncoding.EncodeToString(t6g5SmartBlob("Hush")) + `</data>
</dict></array></dict></plist>`
	path := filepath.Join(t.TempDir(), "iTunes Library.xml")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	return path
}

func TestT6G5Native_PlaylistImport(t *testing.T) {
	s := newRepairPebble(t)
	xml := t6g5PlaylistXML(t)
	params := `{"libraryPath":"` + xml + `","ownerUserId":"alfred","allowEmptyQueries":true}`

	preview, pd := t6g5RunNativeDiff(t, s, New(fakeDeps{store: s}), tasks.ModePreview, "itunes.playlist-import", "", params)
	require.NoError(t, preview.err)
	preview.requireNoStoreWrites(t, pd)
	assert.Zero(t, preview.spy.count("ImportSmartPlaylists"), "a preview imports nothing")
	pl, err := s.GetUserPlaylistByITunesPID(t6g5PlaylistPIDKey)
	require.NoError(t, err)
	assert.Nil(t, pl)

	r, d := t6g5RunNativeDiff(t, s, New(fakeDeps{store: s}), tasks.ModeLive, "itunes.playlist-import", "", params)
	require.NoError(t, r.err)
	r.requireWriter(t)
	r.requireStoreWritesWithin(t, d, string(ops.ResUserState))
	assert.Equal(t, 1, r.spy.count("ImportSmartPlaylists"), "the import reached the writer")
	pl, err = s.GetUserPlaylistByITunesPID(t6g5PlaylistPIDKey)
	require.NoError(t, err)
	require.NotNil(t, pl, "the playlist was created")
	assert.Equal(t, "Gotham Nights", pl.Name)
	assert.Equal(t, "alfred", pl.CreatedByUserID)
}

func TestT6G5Native_PlaylistImportRefusesEmptyQueries(t *testing.T) {
	s := newRepairPebble(t)
	r := t6g5RunNativeOn(t, New(fakeDeps{store: s}), tasks.ModeLive, "itunes.playlist-import", "",
		`{"libraryPath":"`+t6g5PlaylistXML(t)+`"}`)
	require.Error(t, r.err)
	assert.Contains(t, r.err.Error(), "refusing to apply")
	assert.Zero(t, r.spy.count("ImportSmartPlaylists"))
}

// t6g5Capture is a slog handler that keeps what it was given.
type t6g5Capture struct {
	mu   sync.Mutex
	recs []slog.Record
}

func (*t6g5Capture) Enabled(context.Context, slog.Level) bool { return true }
func (c *t6g5Capture) Handle(_ context.Context, r slog.Record) error {
	c.mu.Lock()
	c.recs = append(c.recs, r)
	c.mu.Unlock()
	return nil
}
func (c *t6g5Capture) WithAttrs([]slog.Attr) slog.Handler { return c }
func (c *t6g5Capture) WithGroup(string) slog.Handler      { return c }

// has reports whether a record with msg (and, when level is non-zero, that
// level) was handled.
func (c *t6g5Capture) has(msg string, level slog.Level) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, r := range c.recs {
		if r.Message == msg && r.Level == level {
			return true
		}
	}
	return false
}

// t6g5LogReporter is a reporter whose run logger is a capture of its own,
// apart from the process default.
type t6g5LogReporter struct {
	fakeReporter
	logger *slog.Logger
	// progressErr is what UpdateProgress returns.
	progressErr error
}

func (r *t6g5LogReporter) Logger() *slog.Logger                    { return r.logger }
func (r *t6g5LogReporter) UpdateProgress(_, _ int, _ string) error { return r.progressErr }

// HEAD's playlist import wrote its attribute lines to the process-wide logger,
// not the run's: the run's logger also fans each line out to the op log, the
// bus and the activity log, so moving them would add entries HEAD never made.
func TestT6G5Head_PlaylistImportApplies(t *testing.T) {
	s := newRepairPebble(t)
	xml := t6g5PlaylistXML(t)
	global, run := &t6g5Capture{}, &t6g5Capture{}
	prev := slog.Default()
	slog.SetDefault(slog.New(global))
	t.Cleanup(func() { slog.SetDefault(prev) })
	rep := &t6g5LogReporter{logger: slog.New(run)}
	p := New(fakeDeps{store: s})
	base := `"libraryPath":"` + xml + `","ownerUserId":"alfred","allowEmptyQueries":true`

	require.NoError(t, p.itunesPlaylistImportDef().Run(context.Background(), []byte(`{`+base+`}`), rep))
	pl, err := s.GetUserPlaylistByITunesPID(t6g5PlaylistPIDKey)
	require.NoError(t, err)
	assert.Nil(t, pl, "no dry_run:false, no write")

	require.NoError(t, p.itunesPlaylistImportDef().Run(context.Background(), []byte(`{"dry_run":false,`+base+`}`), rep))
	pl, err = s.GetUserPlaylistByITunesPID(t6g5PlaylistPIDKey)
	require.NoError(t, err)
	require.NotNil(t, pl, "the live run created the playlist")
	assert.Equal(t, "alfred", pl.CreatedByUserID)

	assert.True(t, global.has("itunes-playlist-import complete", slog.LevelInfo), "the completion line goes to the process logger")
	assert.False(t, run.has("itunes-playlist-import complete", slog.LevelInfo), "and not to the run's logger")
	assert.True(t, global.has("itunes-playlist-import: empty translated queries", slog.LevelWarn))
	assert.False(t, run.has("itunes-playlist-import: empty translated queries", slog.LevelWarn))
}

// A progress write that fails is logged on the run's logger and never fails
// the move (HEAD logged "progress not recorded").
func TestT6G5MbfHeadProgress_LogsAFailedWrite(t *testing.T) {
	run := &t6g5Capture{}
	rep := &t6g5LogReporter{logger: slog.New(run), progressErr: errors.New("bus down")}
	t6g5MbfHeadProgress(rep)(1, 2, "moving")
	assert.True(t, run.has("move-book-file-rows: progress not recorded", slog.LevelWarn))

	quiet := &t6g5Capture{}
	ok := &t6g5LogReporter{logger: slog.New(quiet)}
	t6g5MbfHeadProgress(ok)(1, 2, "moving")
	assert.False(t, quiet.has("move-book-file-rows: progress not recorded", slog.LevelWarn), "a good write logs nothing")
}
