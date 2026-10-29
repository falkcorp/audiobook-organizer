// file: internal/scanner/retired_shell_emptied_test.go
// version: 1.1.0
// guid: bfb3aa29-e923-44bd-9e2a-7c709aa20de4
// last-edited: 2026-10-29

package scanner

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// unretire puts rows back under the shell the way the retire revert does.
func (f *retiredScanFixture) unretire(rows ...*database.BookFile) {
	f.t.Helper()
	ids := make([]string, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	require.NoError(f.t, f.store.UnretireBookFiles(
		[]database.BookFileMove{{FileIDs: ids, SourceBookID: f.holder.ID}}, f.shell.ID, f.version.ID, nil))
}

// A book is a retired shell while a retired version made from it still holds
// rows, and stops being one when UnretireBookFiles takes the last row back,
// although the emptied version keeps Status=retired. Before the fix the check
// counted versions, so the shell stayed a shell for ever and every scan lookup
// kept taking the shell branch.
func TestIsRetiredShell_FalseOnceTheLastRowIsUnretired(t *testing.T) {
	f := newRetiredScanFixture(t)
	first := f.retiredRow(filepath.Join(f.root, "shell", "01.mp3"), "h-first")
	second := f.retiredRow(filepath.Join(f.root, "shell", "02.mp3"), "h-second")

	shell, err := isRetiredShell(f.store, f.shell.ID)
	require.NoError(t, err)
	require.True(t, shell, "two retired rows were made from the shell")

	f.unretire(first)
	shell, err = isRetiredShell(f.store, f.shell.ID)
	require.NoError(t, err)
	require.True(t, shell, "one retired row is still held")

	f.unretire(second)
	shell, err = isRetiredShell(f.store, f.shell.ID)
	require.NoError(t, err)
	require.False(t, shell, "the version holds no row now, so the shell owns its files again")

	// The emptied version is left retired ON PURPOSE: the retire revert deletes
	// it through undo.CheckVersionCreateCurrent, which needs the status, and a
	// retire apply adopts an empty retired version of its own op. Clearing the
	// status instead of counting rows would break both.
	v, err := f.store.GetBookVersion(f.version.ID)
	require.NoError(t, err)
	require.NotNil(t, v)
	require.Equal(t, database.BookVersionStatusRetired, v.Status)
	vs, err := f.store.GetRetiredVersionsFromBook(f.shell.ID)
	require.NoError(t, err)
	require.Len(t, vs, 1, "the emptied version is still listed from the shell")

	// ...and it is still deletable, which is the last step of a revert.
	require.NoError(t, f.store.DeleteBookVersion(f.version.ID))
	shell, err = isRetiredShell(f.store, f.shell.ID)
	require.NoError(t, err)
	require.False(t, shell)
}

// A retired version made before its rows move (a retire cut off between the
// version create and the commit point) is not a shell either: the shell's
// files are still its own live rows.
func TestIsRetiredShell_FalseForAVersionThatHoldsNothingYet(t *testing.T) {
	f := newRetiredScanFixture(t)
	require.NoError(t, f.store.CreateBookFile(&database.BookFile{
		BookID: f.shell.ID, FilePath: filepath.Join(f.root, "shell", "01.mp3"), FileHash: "h-live", Format: "mp3"}))

	shell, err := isRetiredShell(f.store, f.shell.ID)
	require.NoError(t, err)
	require.False(t, shell)
	shell, err = isRetiredShell(f.store, f.holder.ID)
	require.NoError(t, err)
	require.False(t, shell, "the holder is not a shell of its own version")
}

// scopeCountingStore records the retired-hash scope the scanner opens on it and
// forwards everything else (and the capabilities) to the real store.
type scopeCountingStore struct {
	scannerStore
	opened, closed atomic.Int32
}

func (s *scopeCountingStore) Unwrap() database.Store {
	st, _ := s.scannerStore.(database.Store)
	return st
}

func (s *scopeCountingStore) BeginRetiredHashScope() func() {
	s.opened.Add(1)
	return func() { s.closed.Add(1) }
}

// ProcessBooksParallel, the one entry every scan path reaches the retired
// content lookup through, holds the retired-hash cache open for the run and
// closes it when the run returns.
func TestProcessBooksParallel_HoldsTheRetiredHashScopeForTheRun(t *testing.T) {
	f := newRetiredScanFixture(t)
	counting := &scopeCountingStore{scannerStore: f.store}
	useScannerStore(t, counting)

	require.NoError(t, ProcessBooksParallel(context.Background(), nil, 2, nil, nil))
	require.EqualValues(t, 1, counting.opened.Load())
	require.EqualValues(t, 1, counting.closed.Load(), "the scope is closed when the run ends")
}

// End to end through checkFileOwnership, on a folder book. While a retired
// version holds the book's only row the book is a shell, the path shortcut is
// not taken, and the folder is judged by who claims its files: a second file in
// it that only a retired row claims skips the scan. Once the revert has emptied
// the version (it keeps Status=retired) the book is an ordinary one again and
// takes the shortcut, which leaves a file another book claims to that book.
// Before the fix the emptied version still made it a shell, so the scan stayed
// skipped.
func TestCheckFileOwnership_EmptiedShellTakesTheOrdinaryBookShortcut(t *testing.T) {
	f := newRetiredScanFixture(t)
	dir := filepath.Join(f.root, "emptied-dir")
	ownPath := f.write("emptied-dir/01.mp3", "emptied-own-bytes")
	otherPath := f.write("emptied-dir/02.mp3", "emptied-other-bytes")
	ownHash, err := ComputeFileHash(ownPath)
	require.NoError(t, err)

	// The folder's own book, whose one row sits retired under the holder.
	shell, err := f.store.CreateBook(&database.Book{Title: "Arkham Asylum", FilePath: dir})
	require.NoError(t, err)
	v, err := f.store.CreateBookVersion(&database.BookVersion{
		BookID: f.holder.ID, Status: database.BookVersionStatusRetired, Format: "mp3", Source: "retired_book",
		RetiredFromBookID: shell.ID, RetireOpID: "op-emptied", RetiredAt: new(time.Now()),
	})
	require.NoError(t, err)
	row := &database.BookFile{BookID: shell.ID, FilePath: ownPath, FileHash: ownHash, Format: "mp3", TrackNumber: 1}
	require.NoError(t, f.store.CreateBookFile(row))
	require.NoError(t, f.store.RetireBookFiles(
		[]database.BookFileMove{{FileIDs: []string{row.ID}, SourceBookID: shell.ID}}, f.holder.ID, v.ID))
	// A second file in the folder that another book's retired version holds.
	f.retiredRow(otherPath, "h-other")
	scanned := func() *Book { return &Book{Title: "Arkham Asylum", FilePath: dir} }

	verdict, err := checkFileOwnership(scanned())
	require.NoError(t, err)
	require.True(t, verdict.skip, "a shell is judged by its files' claimants, and both files are retired-only")
	require.Contains(t, verdict.reason, "retired version of book "+f.holder.ID)

	require.NoError(t, f.store.UnretireBookFiles(
		[]database.BookFileMove{{FileIDs: []string{row.ID}, SourceBookID: f.holder.ID}}, shell.ID, v.ID, nil))
	emptied, err := f.store.GetBookVersion(v.ID)
	require.NoError(t, err)
	require.NotNil(t, emptied)
	require.Equal(t, database.BookVersionStatusRetired, emptied.Status, "the revert's deletion check still needs the status")

	verdict, err = checkFileOwnership(scanned())
	require.NoError(t, err)
	require.False(t, verdict.skip, "the shell owns its file again and takes the shortcut; reason: %s", verdict.reason)
}

// A library scan opens the retired-hash scope once, in PerformScan, and closes
// it when the run ends: the chunks' own scopes nest inside it, so the cache is
// not dropped and rebuilt between 500-book chunks.
func TestPerformScan_HoldsTheRetiredHashScopeForTheRun(t *testing.T) {
	f := newRetiredScanFixture(t)
	counting := &scopeCountingStore{scannerStore: f.store}
	useScannerStore(t, counting)
	folder := filepath.Join(f.root, "scan-folder")
	require.NoError(t, os.MkdirAll(folder, 0o755))

	require.NoError(t, NewScanService(f.store).PerformScan(context.Background(), &ScanRequest{FolderPath: &folder}, nil))
	require.GreaterOrEqual(t, counting.opened.Load(), int32(1), "PerformScan opens a scope")
	require.Equal(t, counting.opened.Load(), counting.closed.Load(), "and every scope it opened is closed")
}
