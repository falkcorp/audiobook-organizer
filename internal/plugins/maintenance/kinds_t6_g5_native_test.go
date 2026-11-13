// file: internal/plugins/maintenance/kinds_t6_g5_native_test.go
// version: 1.0.0
// guid: 6c1f0b7e-3a52-4d8c-9f40-b8e2a7d15c63
// last-edited: 2026-11-13

package maintenance

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/mediacore/tasks"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The native shells run a real core over a real store here, with the
// recording spy below as the only write path. Each test proves the three things
// a unit test of the core cannot: NewWriter was handed THIS run's rc.Writer(),
// every write the core made reached the spy, and the kinds that hold the scan
// stand-down took the configured key and gave it back.

// t6g5ScanKey is the library scan's concurrency key the native tests configure.
const t6g5ScanKey tasks.Key = "books.library-scan"

// t6g5Spy is the writer the native shells are handed: the real HEAD writer
// with every write the group makes recorded first.
type t6g5Spy struct {
	t6g5Writer
	mu     sync.Mutex
	calls  []string
	opIDs  []string
	merged [][]string
}

func (s *t6g5Spy) note(name string) {
	s.mu.Lock()
	s.calls = append(s.calls, name)
	s.mu.Unlock()
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

func (s *t6g5Spy) ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error) {
	s.note("ModifyBook")
	return s.t6g5Writer.ModifyBook(id, fn)
}

func (s *t6g5Spy) DeleteMetadataCache(bookID string) error {
	s.note("DeleteMetadataCache")
	return s.t6g5Writer.DeleteMetadataCache(bookID)
}

func (s *t6g5Spy) UpdateBookFiles(ctx context.Context, files []*database.BookFile, afterRow func(i int, applied bool)) (int, error) {
	s.note("UpdateBookFiles")
	return s.t6g5Writer.UpdateBookFiles(ctx, files, afterRow)
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
	return s.t6g5Writer.MoveBookFilesToBookBulk(moves, targetBookID)
}

func (s *t6g5Spy) ModifyBookFile(bookID, fileID string, fn func(*database.BookFile) error) (*database.BookFile, error) {
	s.note("ModifyBookFile")
	return s.t6g5Writer.ModifyBookFile(bookID, fileID, fn)
}

func (s *t6g5Spy) DeleteNarrator(id int) error {
	s.note("DeleteNarrator")
	return s.t6g5Writer.DeleteNarrator(id)
}

func (s *t6g5Spy) CreateNarrator(name string) (*database.Narrator, error) {
	s.note("CreateNarrator")
	return s.t6g5Writer.CreateNarrator(name)
}

func (s *t6g5Spy) SetBookNarrators(bookID string, narrators []database.BookNarrator) error {
	s.note("SetBookNarrators")
	return s.t6g5Writer.SetBookNarrators(bookID, narrators)
}

func (s *t6g5Spy) RecordOperationChange(c *database.OperationChange) error {
	s.note("RecordOperationChange")
	return s.t6g5Writer.RecordOperationChange(c)
}

// t6g5NativeRun is one native run's observations.
type t6g5NativeRun struct {
	out     json.RawMessage
	err     error
	spy     *t6g5Spy
	holds   *t6g5Holds
	gate    *t6g5Gate
	handed  []tasks.Writer
	runID   string
	started time.Time
}

// t6g5RunNative runs the native kind of headID live over store, with the spy as
// its writer and root as the library root.
func t6g5RunNative(t *testing.T, store database.Store, root, headID string, scanKey tasks.Key, params string) t6g5NativeRun {
	t.Helper()
	p := New(fakeDeps{store: store, root: root})
	b := p.t6g5HeadBase(context.Background(), &fakeReporter{}, true)
	spy := &t6g5Spy{t6g5Writer: b.Writer}
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
	rc, _ := newT6G5HostCtx(context.Background(), d.Kind(), tasks.ModeLive, gate, holds)
	out, err := t6g5Run(t, d, rc, params)
	mu.Lock()
	defer mu.Unlock()
	return t6g5NativeRun{out: out, err: err, spy: spy, holds: holds, gate: gate, handed: slices.Clone(handed), runID: string(rc.RunID())}
}

// requireWriter proves the shell built its writer from this run's rc.Writer().
func (r t6g5NativeRun) requireWriter(t *testing.T) {
	t.Helper()
	require.Len(t, r.handed, 1, "NewWriter is called once per run")
	assert.Same(t, r.gate, r.handed[0], "NewWriter is handed this run's rc.Writer()")
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
	r.requireHold(t)
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
