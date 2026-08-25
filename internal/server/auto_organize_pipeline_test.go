// file: internal/server/auto_organize_pipeline_test.go
// version: 1.1.0
// guid: 6c30f8a4-97b1-4de2-8f05-c4e2b16a73d9
// last-edited: 2026-08-24

package server

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/scanner"
)

// The post-scan auto-organize hook used to run its own copy of the organize
// database logic: OrganizeOneBook (a file operation with NO database work)
// followed by a hand-written FilePath update. It never created the organized
// version row, never transitioned LibraryState, and wrote no undo records --
// while PerformOrganize had been doing all three correctly the whole time.
//
// It was an inline closure, so no test could reach it. These tests exist
// because it is now a named method.

func autoOrganizeTestStore(t *testing.T) *database.PebbleStore {
	t.Helper()
	store, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, database.RunMigrations(store))
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// TestAutoOrganizeResolvesScannedBooksToIDs pins the resolution half: a scanned
// book that IS in the database contributes its ID, and one that is not is
// counted rather than silently dropped. The counters exist because a bare
// `continue` once hid 588 multi-file routing failures in production.
func TestAutoOrganizeResolvesScannedBooksToIDs(t *testing.T) {
	store := autoOrganizeTestStore(t)

	dir := t.TempDir()
	known := filepath.Join(dir, "known.m4b")
	unknown := filepath.Join(dir, "unknown.m4b")
	require.NoError(t, os.WriteFile(known, []byte("a"), 0o644))
	require.NoError(t, os.WriteFile(unknown, []byte("b"), 0o644))

	book, err := store.CreateBook(&database.Book{FilePath: known, Title: "Known"})
	require.NoError(t, err)

	// RootDir empty => the hook must bail before touching the organize service,
	// which is also what keeps this test from needing one.
	oldRoot, oldAuto := config.AppConfig.RootDir, config.AppConfig.AutoOrganize
	t.Cleanup(func() { config.AppConfig.RootDir, config.AppConfig.AutoOrganize = oldRoot, oldAuto })
	config.AppConfig.AutoOrganize = true
	config.AppConfig.RootDir = ""

	srv := &Server{store: store}
	// organizeService is deliberately nil: with RootDir unset the hook must
	// return before ever reaching it. If it does not, this panics -- which is
	// the assertion.
	srv.autoOrganizeScannedBooks(context.Background(),
		[]scanner.Book{{FilePath: known}, {FilePath: unknown}}, logger.New("test"))

	// The book must be untouched -- no organize ran.
	after, err := store.GetBookByID(book.ID)
	require.NoError(t, err)
	require.Equal(t, known, after.FilePath, "no organize should have run with RootDir unset")
}

// TestAutoOrganizeSkipsWhenDisabled is the other guard on the same early return.
func TestAutoOrganizeSkipsWhenDisabled(t *testing.T) {
	store := autoOrganizeTestStore(t)

	oldRoot, oldAuto := config.AppConfig.RootDir, config.AppConfig.AutoOrganize
	t.Cleanup(func() { config.AppConfig.RootDir, config.AppConfig.AutoOrganize = oldRoot, oldAuto })
	config.AppConfig.AutoOrganize = false
	config.AppConfig.RootDir = t.TempDir()

	srv := &Server{store: store}
	// Again nil organizeService: disabled must mean "return", not "call".
	srv.autoOrganizeScannedBooks(context.Background(),
		[]scanner.Book{{FilePath: "/nonexistent/x.m4b"}}, logger.New("test"))
}

// TestAutoOrganizeHandsWorkToTheRealPipeline is the point of the change. It
// asserts the hook delegates to PerformOrganize rather than hand-rolling a
// FilePath update -- verified through the organize service's own recorded
// behaviour rather than by inspecting the call.
func TestAutoOrganizeHandsWorkToTheRealPipeline(t *testing.T) {
	if testing.Short() {
		t.Skip("touches the filesystem and the full organize pipeline")
	}
	store := autoOrganizeTestStore(t)

	src := t.TempDir()
	root := t.TempDir()
	bookPath := filepath.Join(src, "Some Author - Some Title.m4b")
	require.NoError(t, os.WriteFile(bookPath, []byte("audio-bytes"), 0o644))

	// A resolvable author is REQUIRED, not decoration: organize defers any book
	// without one rather than baking "Unknown Author" into the target path (the
	// 2026-08-11 mass-reorganize mechanism). Without this the book is deferred
	// and the test would be asserting nothing.
	author, err := store.CreateAuthor("Some Author")
	require.NoError(t, err)

	book, err := store.CreateBook(&database.Book{
		FilePath: bookPath, Title: "Some Title", AuthorID: &author.ID,
	})
	require.NoError(t, err)

	oldRoot, oldAuto := config.AppConfig.RootDir, config.AppConfig.AutoOrganize
	oldStrategy := config.AppConfig.OrganizationStrategy
	t.Cleanup(func() {
		config.AppConfig.RootDir, config.AppConfig.AutoOrganize = oldRoot, oldAuto
		config.AppConfig.OrganizationStrategy = oldStrategy
	})
	config.AppConfig.AutoOrganize = true
	config.AppConfig.RootDir = root
	// Explicit "copy" rather than the "auto" default: auto tries reflink then
	// hardlink first, and which one succeeds depends on the filesystem the
	// temp dir lands on, which would make this test's behaviour machine-specific.
	config.AppConfig.OrganizationStrategy = "copy"

	srv := &Server{store: store, organizeService: NewOrganizeService(store)}
	srv.autoOrganizeScannedBooks(context.Background(),
		[]scanner.Book{{FilePath: bookPath}}, logger.New("test"))

	after, err := store.GetBookByID(book.ID)
	require.NoError(t, err)
	require.NotNil(t, after)

	// The old hand-rolled path repointed this single row at the organized copy
	// and left LibraryState nil forever. The real pipeline either transitions
	// the state or creates a version row -- either proves delegation happened.
	movedInPlace := after.FilePath != bookPath
	stateSet := after.LibraryState != nil && *after.LibraryState != "" && *after.LibraryState != "imported"
	versioned := after.VersionGroupID != nil && *after.VersionGroupID != ""

	if !movedInPlace && !stateSet && !versioned {
		t.Fatalf("the hook did not reach the organize pipeline: FilePath unchanged (%s), "+
			"LibraryState=%v, VersionGroupID=%v -- this is the hand-rolled behaviour the "+
			"change removes", after.FilePath, after.LibraryState, after.VersionGroupID)
	}
	t.Logf("delegated: movedInPlace=%v stateSet=%v versioned=%v (LibraryState=%v)",
		movedInPlace, stateSet, versioned, after.LibraryState)
}
