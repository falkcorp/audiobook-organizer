// file: internal/scanner/scan_book_lock_test.go
// version: 1.0.0
// guid: 161af27f-a511-4b3d-a32f-02348506c28a
// last-edited: 2026-09-30

package scanner

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/scanlock"
	"github.com/stretchr/testify/require"
)

// scanLockFixture is a Pebble store wired as the scanner's store, with the
// config a single-file fallback scan needs: no size threshold, no AI, and a
// root dir the test chooses.
type scanLockFixture struct {
	store *database.PebbleStore
	dir   string
}

func newScanLockFixture(t *testing.T, rootDir string) scanLockFixture {
	t.Helper()
	SetScanner(nil)
	t.Cleanup(func() { SetScanner(nil) })
	store, cleanup := setupPebbleStore(t)
	t.Cleanup(cleanup)
	orig := database.GetGlobalStore()
	database.SetGlobalStore(store)
	t.Cleanup(func() { database.SetGlobalStore(orig) })
	useScannerStore(t, store)

	oldMin, oldAI, oldRoot := config.AppConfig.MinBookSizeBytes, config.AppConfig.EnableAIParsing, config.AppConfig.RootDir
	config.AppConfig.MinBookSizeBytes = 0
	config.AppConfig.EnableAIParsing = false
	config.AppConfig.RootDir = rootDir
	t.Cleanup(func() {
		config.AppConfig.MinBookSizeBytes, config.AppConfig.EnableAIParsing, config.AppConfig.RootDir = oldMin, oldAI, oldRoot
	})
	return scanLockFixture{store: store, dir: t.TempDir()}
}

// file writes a fake audio file. ProcessFile fails on it, so the book keeps
// whatever Title/Author the test preset: those stand in for "the file's tags".
func (f scanLockFixture) file(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(f.dir, name)
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
	require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
	return p
}

// withSaveHook runs hook before every scanner save: the seam between the tag
// read and the row merge.
func withSaveHook(t *testing.T, hook func(b *Book)) {
	t.Helper()
	orig := saveBook
	saveBook = func(ctx context.Context, b *Book) error {
		hook(b)
		return orig(ctx, b)
	}
	t.Cleanup(func() { saveBook = orig })
}

func scanOne(t *testing.T, b Book) {
	t.Helper()
	require.NoError(t, ProcessBooksParallel(context.Background(), []Book{b}, 1, nil, logger.New("test")))
}

// O-during: the scanner holds X (tags read, merge pending) when an apply
// arrives. The apply waits; an apply of another book does not; after the
// scanner moves on the apply lands and nothing reverts it.
func TestScanBookLock_ApplyDuringScanWaitsAndIsNotReverted(t *testing.T) {
	f := newScanLockFixture(t, "")
	p := f.file(t, "a/book.m4b", "x-data")
	x, err := f.store.CreateBook(&database.Book{Title: "Tagged Title", FilePath: p, Format: "m4b"})
	require.NoError(t, err)
	y, err := f.store.CreateBook(&database.Book{Title: "Other", FilePath: f.file(t, "b/other.m4b", "y")})
	require.NoError(t, err)

	inSave := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	withSaveHook(t, func(b *Book) {
		once.Do(func() { close(inSave); <-release })
	})

	scanDone := make(chan struct{})
	go func() {
		defer close(scanDone)
		scanOne(t, Book{FilePath: p, Title: "Tagged Title", Author: "A. Author", Format: ".m4b"})
	}()
	<-inSave

	// Another book is free right now: no warning, no wait.
	hy, ok := scanlock.Books.TryLockSet([]string{y.ID})
	require.True(t, ok, "an apply of an unrelated book was blocked by the scan")
	hy.Release()

	applied := make(chan struct{})
	go func() {
		h, lerr := scanlock.Books.LockSet(context.Background(), []string{x.ID})
		if lerr != nil {
			t.Error(lerr)
			return
		}
		defer h.Release()
		_, merr := f.store.ModifyBook(x.ID, func(b *database.Book) error { b.Title = "Applied Title"; return nil })
		if merr != nil {
			t.Error(merr)
		}
		close(applied)
	}()
	select {
	case <-applied:
		t.Fatal("the apply wrote the book while the scanner held it")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	<-scanDone
	<-applied

	got, err := f.store.GetBookByID(x.ID)
	require.NoError(t, err)
	require.Equal(t, "Applied Title", got.Title)
	require.Zero(t, scanlock.Books.Held(), "a scan lock outlived ProcessBooksParallel (auto-organize runs after it returns)")
}

// Safety net: a writer that does NOT take the lock changes fields between the
// tag read and the merge. Those fields keep its values; untouched fields still
// take the scan's.
func TestScanBookLock_ForeignEditBetweenReadAndMergeIsKept(t *testing.T) {
	f := newScanLockFixture(t, "")
	p := f.file(t, "a/book.m4b", "x-data")
	x, err := f.store.CreateBook(&database.Book{Title: "Tagged Title", FilePath: p, Format: "m4b", Publisher: new("Old Pub")})
	require.NoError(t, err)

	var once sync.Once
	withSaveHook(t, func(b *Book) {
		once.Do(func() {
			_, merr := f.store.ModifyBook(x.ID, func(b *database.Book) error {
				b.Title = "Edited Mid-Scan"
				b.Narrator = new("Edited Narrator")
				return nil
			})
			require.NoError(t, merr)
		})
	})
	scanOne(t, Book{FilePath: p, Title: "Scanned Title", Author: "A. Author", Narrator: "Scanned Narrator",
		Publisher: "Scanned Pub", Format: ".m4b"})

	got, err := f.store.GetBookByID(x.ID)
	require.NoError(t, err)
	require.Equal(t, "Edited Mid-Scan", got.Title)
	require.Equal(t, "Edited Narrator", *got.Narrator)
	require.Equal(t, "Scanned Pub", *got.Publisher, "a field nobody else touched must still take the scan's value")
}

// O-before-pending: the apply holds X across its (pending) file job. The scan
// must not touch X until the hold is released, and must not stall either:
// round 0 skips it, a later round waits for it.
func TestScanBookLock_ScannerWaitsForHeldBookThenProcessesIt(t *testing.T) {
	f := newScanLockFixture(t, "")
	p := f.file(t, "a/book.m4b", "x-data")
	x, err := f.store.CreateBook(&database.Book{Title: "Before", FilePath: p, Format: "m4b"})
	require.NoError(t, err)

	hold, err := scanlock.Books.LockSet(context.Background(), []string{x.ID})
	require.NoError(t, err)

	var mu sync.Mutex
	saved := false
	withSaveHook(t, func(*Book) { mu.Lock(); saved = true; mu.Unlock() })

	scanDone := make(chan struct{})
	go func() {
		defer close(scanDone)
		scanOne(t, Book{FilePath: p, Title: "After File Job", Author: "A. Author", Format: ".m4b"})
	}()
	time.Sleep(150 * time.Millisecond)
	mu.Lock()
	require.False(t, saved, "the scanner saved a book an apply was holding")
	mu.Unlock()

	hold.Release()
	select {
	case <-scanDone:
	case <-time.After(10 * time.Second):
		t.Fatal("the scanner never picked the book up after the apply released it")
	}
	got, err := f.store.GetBookByID(x.ID)
	require.NoError(t, err)
	// The fake file's "tags" come from ProcessFile's filename fallback; what
	// matters is that the scanner merged the book after the release.
	require.NotEqual(t, "Before", got.Title, "the scanner never merged the book once the apply released it")
	mu.Lock()
	require.True(t, saved)
	mu.Unlock()
}

// The scanner's waits are bounded: a book held past every round is recorded as
// busy and left untouched, and the scan returns.
func TestScanBookLock_ScannerGivesUpOnABookHeldTooLong(t *testing.T) {
	f := newScanLockFixture(t, "")
	old := scanLockWait
	scanLockWait = 50 * time.Millisecond
	t.Cleanup(func() { scanLockWait = old })

	p := f.file(t, "a/book.m4b", "x-data")
	x, err := f.store.CreateBook(&database.Book{Title: "Before", FilePath: p, Format: "m4b"})
	require.NoError(t, err)
	hold, err := scanlock.Books.LockSet(context.Background(), []string{x.ID})
	require.NoError(t, err)
	defer hold.Release()

	failures := &FileFailures{}
	ctx := withFileFailures(context.Background(), failures)
	start := time.Now()
	require.NoError(t, ProcessBooksParallel(ctx, []Book{{FilePath: p, Title: "Scanned", Author: "A", Format: ".m4b"}}, 1, nil, logger.New("test")))
	require.Less(t, time.Since(start), 5*time.Second)

	busy := 0
	for _, s := range failures.Samples() {
		if s.Stage == FileFailureStageBusy {
			busy++
		}
	}
	require.Equal(t, 1, busy, "the held book was not recorded as busy for the next scan")
	got, err := f.store.GetBookByID(x.ID)
	require.NoError(t, err)
	require.Equal(t, "Before", got.Title)
}

// Move (a): an apply renamed the book's file after the walk listed it. The
// scanner must not mint a row for the vanished path.
func TestScanBookLock_VanishedPathIsNotImported(t *testing.T) {
	f := newScanLockFixture(t, "")
	p := f.file(t, "a/renamed-away.m4b", "x-data")
	require.NoError(t, os.Remove(p))

	scanOne(t, Book{FilePath: p, Title: "Ghost", Author: "A. Author", Format: ".m4b"})

	row, err := f.store.GetBookByFilePath(p)
	require.NoError(t, err)
	require.Nil(t, row, "the scan created a row for a file that no longer exists")
}

// Move (b): an apply of protected book X is making its library copy; the copy's
// file is on disk before its row exists. The scanner finds X by content hash,
// must not repoint X at the copy, and must merge onto the copy's own row once
// the apply finishes.
func TestScanBookLock_LibraryCopyBeforeItsRowIsNotPromotedOntoTheOriginal(t *testing.T) {
	root := t.TempDir()
	f := newScanLockFixture(t, root)
	orig := f.file(t, "protected/x.m4b", "same-bytes")
	hash, err := ComputeFileHash(orig)
	require.NoError(t, err)
	x, err := f.store.CreateBook(&database.Book{Title: "Original", FilePath: orig, Format: "m4b", FileHash: &hash, OriginalFileHash: &hash})
	require.NoError(t, err)

	// The apply holds X for its whole file job.
	hold, err := scanlock.Books.LockSet(context.Background(), []string{x.ID})
	require.NoError(t, err)

	copyPath := filepath.Join(root, "Author", "x.m4b")
	require.NoError(t, os.MkdirAll(filepath.Dir(copyPath), 0o755))
	require.NoError(t, os.WriteFile(copyPath, []byte("same-bytes"), 0o644))

	scanDone := make(chan struct{})
	go func() {
		defer close(scanDone)
		scanOne(t, Book{FilePath: copyPath, Title: "Copy Tags", Author: "A. Author", Format: ".m4b"})
	}()
	time.Sleep(150 * time.Millisecond)

	// The apply finishes the copy: its row, in X's version group.
	gid := "vg-test-copy"
	_, err = f.store.ModifyBook(x.ID, func(b *database.Book) error { b.VersionGroupID = &gid; return nil })
	require.NoError(t, err)
	s, err := f.store.CreateBook(&database.Book{Title: "Applied", FilePath: copyPath, Format: "m4b", VersionGroupID: &gid, IsPrimaryVersion: new(true)})
	require.NoError(t, err)
	hold.Release()
	<-scanDone

	gotX, err := f.store.GetBookByID(x.ID)
	require.NoError(t, err)
	require.Equal(t, orig, gotX.FilePath, "the scan repointed the original at its library copy")
	atPath, err := f.store.GetBookByFilePath(copyPath)
	require.NoError(t, err)
	require.NotNil(t, atPath)
	require.Equal(t, s.ID, atPath.ID, "a second row was created at the copy's path")
}

// Amendment 2: the AI phase's re-save must be able to overlay what the MAIN
// pass wrote (the snapshot is refreshed after each scanner write), while a
// user edit made between the two saves is still kept.
func TestScanBookLock_AIPhaseSaveOverlaysMainPassButKeepsUserEdit(t *testing.T) {
	f := newScanLockFixture(t, "")
	p := f.file(t, "a/book.m4b", "x-data")
	x, err := f.store.CreateBook(&database.Book{Title: "Old", FilePath: p, Format: "m4b"})
	require.NoError(t, err)

	b := &Book{FilePath: p, Title: "Main Pass Title", Author: "A. Author", Format: ".m4b"}
	hold, outcome := acquireScanBookLock(context.Background(), b, 1, true)
	require.Equal(t, scanLockHeld, outcome)
	require.NoError(t, saveBookToDatabase(scanlock.WithHold(context.Background(), hold), b))
	hold.Release()

	// A user apply between the main pass and the AI phase.
	_, err = f.store.ModifyBook(x.ID, func(r *database.Book) error { r.Narrator = new("User Narrator"); return nil })
	require.NoError(t, err)

	b.Title = "AI Title"
	b.Narrator = "AI Narrator"
	saved, err := saveBookUnderScanLock(context.Background(), b, saveBookToDatabase)
	require.NoError(t, err)
	require.True(t, saved)

	got, err := f.store.GetBookByID(x.ID)
	require.NoError(t, err)
	require.Equal(t, "AI Title", got.Title, "the AI result was discarded as a foreign edit of the main pass's own write")
	require.Equal(t, "User Narrator", *got.Narrator, "the user's edit between the two saves was reverted")
}
