// file: internal/plugins/maintenance/kinds_t6_g5_native_test.go
// version: 1.1.2
// guid: 6c1f0b7e-3a52-4d8c-9f40-b8e2a7d15c63
// last-edited: 2026-11-14

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
	"reflect"
	"slices"
	"sort"
	"sync"
	"testing"
	"time"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/itunes"
	itunesservice "github.com/falkcorp/audiobook-organizer/internal/itunes/service"
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

// t6g5SpyWrites maps EVERY method of t6g5Writer to the books resources a call
// to it writes, named by the pkg/ops constants. An empty list is a method that
// changes nothing on disk or in a store: it either runs a closure the spy records
// separately (HardDeleteWithoutUserState runs the caller's delete, which arrives
// as DeleteBook, after a read-only user-state check), drops an in-memory cache
// (InvalidateAuthorsCache) or only reads (NewUserStateProbe). Reflink and
// CloneBookIntoLibrary change bytes on disk, so they map to files. The spy
// refuses a method with no entry (t6g5NativeRun.requireWriter fails), and
// TestT6G5Spy_MapsEveryWriterMethod fails when the interface and this table
// disagree, so a new writer method cannot slip past the Effects check.
var t6g5SpyWrites = map[string][]string{
	"CarryStateBeforeHardDelete": {string(ops.ResUserState)},
	"CarryStateBetweenLiveBooks": {string(ops.ResUserState)},
	"CarryStateThenHardDelete":   {string(ops.ResUserState)},
	"CloneBookIntoLibrary":       {string(ops.ResBooks), string(ops.ResBookFiles), string(ops.ResFiles)},
	"CreateBook":                 {string(ops.ResBooks)},
	"CreateNarrator":             {string(ops.ResBooks)},
	"DeleteBook":                 {string(ops.ResBooks), string(ops.ResBookFiles)},
	"DeleteBookFilesByIDs":       {string(ops.ResBookFiles)},
	"DeleteMetadataCache":        {string(ops.ResMetadataCache)},
	"DeleteNarrator":             {string(ops.ResBooks)},
	"HardDeleteWithoutUserState": {},
	"ImportSmartPlaylists":       {string(ops.ResUserState)},
	"InvalidateAuthorsCache":     {},
	"MergeBooks":                 {string(ops.ResBooks), string(ops.ResBookFiles), string(ops.ResExternalIDs), string(ops.ResUserState), string(ops.ResOperations)},
	"ModifyBook":                 {string(ops.ResBooks)},
	"ModifyBookFile":             {string(ops.ResBookFiles), string(ops.ResBooks)},
	"MoveBookFilesToBook":        {string(ops.ResBookFiles), string(ops.ResBooks)},
	"MoveBookFilesToBookBulk":    {string(ops.ResBookFiles), string(ops.ResBooks)},
	"NewUserStateProbe":          {},
	"Reflink":                    {string(ops.ResFiles)},
	"ReassignExternalID":         {string(ops.ResExternalIDs)},
	"RecomputeBookAggregates":    {string(ops.ResBooks)},
	"RecordOperationChange":      {string(ops.ResOperations)},
	"SaveChaptersForBook":        {string(ops.ResBooks)},
	"SetBookAuthors":             {string(ops.ResAuthors)},
	"SetBookNarrators":           {string(ops.ResBooks)},
	"SetUserPreferenceForUser":   {string(ops.ResUserState)},
	"UpdateBook":                 {string(ops.ResBooks)},
	"UpdateBookFiles":            {string(ops.ResBookFiles), string(ops.ResBooks)},
}

// TestT6G5Spy_MapsEveryWriterMethod keeps t6g5SpyWrites and t6g5Writer in step:
// a method added to the interface needs a resource entry before any native test
// can pass, and an entry for a method the interface no longer has is stale.
func TestT6G5Spy_MapsEveryWriterMethod(t *testing.T) {
	typ := reflect.TypeFor[t6g5Writer]()
	var methods []string
	for i := range typ.NumMethod() {
		methods = append(methods, typ.Method(i).Name)
	}
	var mapped []string
	for name := range t6g5SpyWrites {
		mapped = append(mapped, name)
	}
	sort.Strings(methods)
	sort.Strings(mapped)
	assert.Equal(t, methods, mapped, "t6g5SpyWrites names exactly the methods of t6g5Writer")
}

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
}

func (s *t6g5Spy) note(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, name)
	if _, ok := t6g5SpyWrites[name]; !ok {
		s.unmapped = append(s.unmapped, name)
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
		for _, r := range t6g5SpyWrites[c] {
			seen[r] = true
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
	s.note("ModifyBook")
	return s.inner.ModifyBook(id, fn)
}

func (s *t6g5Spy) ModifyBookFile(bookID, fileID string, fn func(*database.BookFile) error) (*database.BookFile, error) {
	s.note("ModifyBookFile")
	return s.inner.ModifyBookFile(bookID, fileID, fn)
}

func (s *t6g5Spy) DeleteBook(id string) error {
	s.note("DeleteBook")
	return s.inner.DeleteBook(id)
}

func (s *t6g5Spy) DeleteBookFilesByIDs(refs []database.BookFileRef) ([]database.BookFile, error) {
	s.note("DeleteBookFilesByIDs")
	return s.inner.DeleteBookFilesByIDs(refs)
}

func (s *t6g5Spy) SetBookAuthors(bookID string, authors []database.BookAuthor) error {
	s.note("SetBookAuthors")
	return s.inner.SetBookAuthors(bookID, authors)
}

func (s *t6g5Spy) SetUserPreferenceForUser(userID, key, value string) error {
	s.note("SetUserPreferenceForUser")
	return s.inner.SetUserPreferenceForUser(userID, key, value)
}

func (s *t6g5Spy) CloneBookIntoLibrary(book *database.Book, files []database.BookFile, opID string) (string, error) {
	s.note("CloneBookIntoLibrary")
	return s.inner.CloneBookIntoLibrary(book, files, opID)
}

func (s *t6g5Spy) Reflink(src, dst string) error {
	s.note("Reflink")
	return s.inner.Reflink(src, dst)
}

func (s *t6g5Spy) SaveChaptersForBook(bookID string, chapters []database.Chapter) error {
	s.note("SaveChaptersForBook")
	return s.inner.SaveChaptersForBook(bookID, chapters)
}

func (s *t6g5Spy) CarryStateBeforeHardDelete(keepID, doomedID string) error {
	s.note("CarryStateBeforeHardDelete")
	return s.inner.CarryStateBeforeHardDelete(keepID, doomedID)
}

func (s *t6g5Spy) HardDeleteWithoutUserState(bookID string, del func() error) error {
	s.note("HardDeleteWithoutUserState")
	return s.inner.HardDeleteWithoutUserState(bookID, del)
}

func (s *t6g5Spy) CarryStateBetweenLiveBooks(toID, fromID string) error {
	s.note("CarryStateBetweenLiveBooks")
	return s.inner.CarryStateBetweenLiveBooks(toID, fromID)
}

func (s *t6g5Spy) DeleteMetadataCache(bookID string) error {
	s.note("DeleteMetadataCache")
	return s.inner.DeleteMetadataCache(bookID)
}

func (s *t6g5Spy) UpdateBookFiles(ctx context.Context, files []*database.BookFile, afterRow func(i int, applied bool)) (int, error) {
	s.note("UpdateBookFiles")
	return s.inner.UpdateBookFiles(ctx, files, afterRow)
}

// MergeBooks records the merge and does not run it: the merge itself is the
// server's, and what these tests prove is that the core asked for it under the
// run's ID with the right books.
func (s *t6g5Spy) MergeBooks(opID string, bookIDs []string, primaryID string) (int, error) {
	s.note("MergeBooks")
	s.mu.Lock()
	s.opIDs = append(s.opIDs, opID)
	s.merged = append(s.merged, append([]string{primaryID}, bookIDs...))
	s.mu.Unlock()
	return len(bookIDs) - 1, nil
}

func (s *t6g5Spy) MoveBookFilesToBookBulk(moves []database.BookFileMove, targetBookID string) error {
	s.note("MoveBookFilesToBookBulk")
	return s.inner.MoveBookFilesToBookBulk(moves, targetBookID)
}

func (s *t6g5Spy) MoveBookFilesToBook(fileIDs []string, sourceBookID, targetBookID string) error {
	s.note("MoveBookFilesToBook")
	return s.inner.MoveBookFilesToBook(fileIDs, sourceBookID, targetBookID)
}

func (s *t6g5Spy) DeleteNarrator(id int) error {
	s.note("DeleteNarrator")
	return s.inner.DeleteNarrator(id)
}

func (s *t6g5Spy) InvalidateAuthorsCache() {
	s.note("InvalidateAuthorsCache")
	s.inner.InvalidateAuthorsCache()
}

func (s *t6g5Spy) RecordOperationChange(c *database.OperationChange) error {
	s.note("RecordOperationChange")
	return s.inner.RecordOperationChange(c)
}

func (s *t6g5Spy) CreateNarrator(name string) (*database.Narrator, error) {
	s.note("CreateNarrator")
	return s.inner.CreateNarrator(name)
}

func (s *t6g5Spy) SetBookNarrators(bookID string, narrators []database.BookNarrator) error {
	s.note("SetBookNarrators")
	return s.inner.SetBookNarrators(bookID, narrators)
}

func (s *t6g5Spy) ImportSmartPlaylists(lib *itunes.ITLLibrary, ownerUserID string) itunesservice.PlaylistImportResult {
	s.note("ImportSmartPlaylists")
	return s.inner.ImportSmartPlaylists(lib, ownerUserID)
}

func (s *t6g5Spy) UpdateBook(id string, book *database.Book) (*database.Book, error) {
	s.note("UpdateBook")
	return s.inner.UpdateBook(id, book)
}

func (s *t6g5Spy) RecomputeBookAggregates(bookID string) error {
	s.note("RecomputeBookAggregates")
	return s.inner.RecomputeBookAggregates(bookID)
}

func (s *t6g5Spy) CreateBook(book *database.Book) (*database.Book, error) {
	s.note("CreateBook")
	return s.inner.CreateBook(book)
}

func (s *t6g5Spy) ReassignExternalID(source, externalID, newBookID string) error {
	s.note("ReassignExternalID")
	return s.inner.ReassignExternalID(source, externalID, newBookID)
}

func (s *t6g5Spy) NewUserStateProbe() (t6g5RegroupStateProbe, error) {
	s.note("NewUserStateProbe")
	return s.inner.NewUserStateProbe()
}

func (s *t6g5Spy) CarryStateThenHardDelete(keepID, doomedID string, precheck, del func() error) error {
	s.note("CarryStateThenHardDelete")
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
	b := p.t6g5HeadBase(context.Background(), &fakeReporter{}, true)
	spy := &t6g5Spy{inner: b.Writer}
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
	out, err := t6g5Run(t, d, rc, params)
	mu.Lock()
	defer mu.Unlock()
	return t6g5NativeRun{out: out, err: err, spy: spy, holds: holds, gate: gate, handed: slices.Clone(handed), runID: string(rc.RunID()), def: d, rc: rc}
}

// requireWriter proves the shell built its writer from this run's rc.Writer(),
// and that every resource a write reached is one the kind declares: the
// resources the spy saw must be a subset of the kind's Effects.WriteSet(), and
// the spy must have met no method it has no mapping for. A run that recorded
// nothing fails: the subset check is vacuous over an empty record.
func (r t6g5NativeRun) requireWriter(t *testing.T) {
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
	r := t6g5RunNative(t, f.s, t.TempDir(), "maintenance.move-book-file-rows", t6g5ScanKey,
		`{"moves":[{"row_id":"`+f.ch01+`","to_book_id":"`+f.dst+`"}]}`)
	require.NoError(t, r.err)
	r.requireWriter(t)
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

	r := t6g5RunNative(t, s, dir, "maintenance.mark-missing-files", t6g5ScanKey,
		`{"reportPath":"`+filepath.Join(dir, "r.tsv")+`"}`)
	require.NoError(t, r.err)
	r.requireWriter(t)
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
	real := filepath.Join(dir, "Stem - 02.mp3")
	require.NoError(t, os.WriteFile(real, make([]byte, 1234), 0o644))
	broken := filepath.Join(dir, "Stem - 2", "35.mp3")
	const book = "01BATMAN00000000000000RP01"
	_, err := s.CreateBook(&database.Book{ID: book, Title: "The Long Halloween", FilePath: dir})
	require.NoError(t, err)
	require.NoError(t, s.CreateBookFile(&database.BookFile{ID: "01ROWRP0000000000000000001", BookID: book, FilePath: broken, FileSize: 1234}))

	r := t6g5RunNative(t, s, dir, "maintenance.missing-file-repoint", t6g5ScanKey,
		`{"reportPath":"`+filepath.Join(dir, "r.tsv")+`"}`)
	require.NoError(t, r.err)
	r.requireWriter(t)
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
	path := "/lib/Author/Book.m4b"
	for _, id := range []string{"01BATMAN00000000000000MG01", "01BATMAN00000000000000MG02"} {
		_, err := s.CreateBook(&database.Book{ID: id, Title: "Hush", FilePath: path})
		require.NoError(t, err)
		require.NoError(t, s.CreateBookFile(&database.BookFile{BookID: id, FilePath: path, FileHash: "H"}))
	}
	r := t6g5RunNative(t, s, dir, "maintenance.merge-same-path-dupes", t6g5ScanKey,
		`{"reportPath":"`+filepath.Join(dir, "r.tsv")+`"}`)
	require.NoError(t, r.err)
	r.requireWriter(t)
	keys, _ := r.holds.snapshot()
	assert.Empty(t, keys, "the merge takes no scan stand-down (HEAD took none)")
	var plan mergeSamePathPlan
	require.NoError(t, json.Unmarshal(r.out, &plan))
	assert.True(t, plan.Apply)
	assert.Equal(t, 1, plan.Merged)
	require.Equal(t, 1, r.spy.count("MergeBooks"), "the merge reached the writer")
	assert.Equal(t, []string{r.runID}, r.spy.opIDs, "merged under the run's own ID")
	assert.ElementsMatch(t, []string{"01BATMAN00000000000000MG01", "01BATMAN00000000000000MG02"}, r.spy.merged[0][1:])
}

func TestT6G5Native_MetadataCacheReap(t *testing.T) {
	s := newRepairPebble(t)
	dir := t.TempDir()
	const orphan = "01BATMAN00000000000000OR01"
	require.NoError(t, s.PutMetadataCache(&database.MetadataCandidateCache{BookID: orphan, FetchedAt: time.Now()}))

	r := t6g5RunNative(t, s, dir, "maintenance.metadata-cache-reap", "",
		`{"reportPath":"`+filepath.Join(dir, "r.tsv")+`"}`)
	require.NoError(t, r.err)
	r.requireWriter(t)
	var plan reapPlan
	require.NoError(t, json.Unmarshal(r.out, &plan))
	assert.Equal(t, 1, plan.Reaped)
	assert.Equal(t, 1, r.spy.count("DeleteMetadataCache"))
	got, err := s.GetMetadataCache(orphan)
	require.NoError(t, err)
	assert.Nil(t, got, "the orphaned row is gone")
	keys, _ := r.holds.snapshot()
	assert.Empty(t, keys, "the reap takes no scan stand-down")
}

func TestT6G5Native_PurgeEmptyNarrators(t *testing.T) {
	s := newSeriesPhantomStore(t)
	n, err := s.CreateNarrator("Selina Kyle")
	require.NoError(t, err)

	r := t6g5RunNative(t, s, t.TempDir(), "maintenance.purge-empty-narrators", t6g5ScanKey, "")
	require.NoError(t, r.err)
	r.requireWriter(t)
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
	r := t6g5RunNative(t, f.s, t.TempDir(), "maintenance.split-joined-narrators", t6g5ScanKey, "")
	require.NoError(t, r.err)
	r.requireWriter(t)
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
		FilePath: filepath.Join(root, "Some Author", "Some Book", "01.mp3"), LibraryState: &state, OrganizedFileHash: &hash})
	require.NoError(t, err)

	r := t6g5RunNative(t, s, root, "maintenance.repair-library-state", "", "")
	require.NoError(t, r.err)
	r.requireWriter(t)
	var res libraryStateRepairResult
	require.NoError(t, json.Unmarshal(r.out, &res))
	assert.False(t, res.DryRun)
	assert.Equal(t, 1, res.Outcomes[lsrRepaired])
	assert.Equal(t, 1, r.spy.count("ModifyBook"))
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
		FilePath: filepath.Join(root, "Some Author", "Some Book", "01.mp3"), LibraryState: &state, OrganizedFileHash: &hash})
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

	r := t6g5RunNativeOn(t, t6g5ClonePlugin(f), tasks.ModeLive, "itunes.clone-into-library", t6g5ScanKey,
		`{"apply":true,"group_ids":["vg-t"]}`)
	require.NoError(t, r.err)
	r.requireWriter(t)
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

	r := t6g5RunNativeOn(t, t6g5ClonePlugin(f), tasks.ModeLive, "itunes.clone-into-library", t6g5ScanKey,
		`{"rollback":true,"group_ids":["vg-t"]}`)
	require.NoError(t, r.err)
	r.requireWriter(t)
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

	r := t6g5RunNativeOn(t, New(fakeDeps{store: s, root: rgRoot}), tasks.ModeLive, "itunes.regroup", "",
		`{"xmlPath":"`+xml+`"}`)
	require.NoError(t, r.err)
	r.requireWriter(t)
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

func TestT6G5Native_RegroupPreviewWritesNothing(t *testing.T) {
	s, _, plan, _ := regroupWithState(t)
	loser := plan.DeleteBooks[0]
	r := t6g5RunNativeOn(t, New(fakeDeps{store: s, root: rgRoot}), tasks.ModePreview, "itunes.regroup", "",
		`{"xmlPath":"`+t6g5RegroupXML(t)+`"}`)
	require.NoError(t, r.err)
	assert.Empty(t, r.spy.calls, "a preview makes no write")
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

	preview := t6g5RunNativeOn(t, New(fakeDeps{store: s}), tasks.ModePreview, "itunes.playlist-import", "", params)
	require.NoError(t, preview.err)
	assert.Zero(t, preview.spy.count("ImportSmartPlaylists"), "a preview imports nothing")
	pl, err := s.GetUserPlaylistByITunesPID(t6g5PlaylistPIDKey)
	require.NoError(t, err)
	assert.Nil(t, pl)

	r := t6g5RunNativeOn(t, New(fakeDeps{store: s}), tasks.ModeLive, "itunes.playlist-import", "", params)
	require.NoError(t, r.err)
	r.requireWriter(t)
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
