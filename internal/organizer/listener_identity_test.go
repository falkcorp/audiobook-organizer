// file: internal/organizer/listener_identity_test.go
// version: 1.0.1
// guid: 237f021f-dc2b-4f70-98e1-5a6cd60ed033
// last-edited: 2026-10-30

package organizer

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
)

// listenerFixture is one book a listener (Bruce Wayne) is partway through,
// ready to be copy-organized: a source row with one file, and a landing for
// its organized copy.
type listenerFixture struct {
	store   *database.PebbleStore
	userID  string
	source  *database.Book
	fileID  string
	landing *Landing
}

func newListenerFixture(t *testing.T) *listenerFixture {
	t.Helper()
	store := newLandingTestStore(t)
	rootDir := t.TempDir()
	prev := config.AppConfig
	config.AppConfig = config.Config{RootDir: rootDir}
	t.Cleanup(func() { config.AppConfig = prev })

	user, err := store.CreateUser("bruce_wayne", "bruce@example.com", "argon2id", "x", []string{"user"}, "active")
	require.NoError(t, err)

	srcPath := "/srv/library/imports/Batman Year One/year-one.m4b"
	primary, imported := true, "organized"
	book, err := store.CreateBook(&database.Book{
		Title: "Batman: Year One", FilePath: srcPath, Format: "m4b",
		IsPrimaryVersion: &primary, LibraryState: &imported,
	})
	require.NoError(t, err)
	require.NoError(t, store.CreateBookFile(&database.BookFile{BookID: book.ID, FilePath: srcPath, Format: "m4b", Duration: 3600}))
	files, err := store.GetBookFiles(book.ID)
	require.NoError(t, err)
	require.Len(t, files, 1)

	newPath := filepath.Join(rootDir, "Lucius Fox", "Batman Year One", "year-one.m4b")
	require.NoError(t, os.MkdirAll(filepath.Dir(newPath), 0o755))
	require.NoError(t, os.WriteFile(newPath, []byte("m4b"), 0o644))

	return &listenerFixture{
		store: store, userID: user.ID, source: book, fileID: files[0].ID,
		landing: &Landing{Path: newPath, Created: []string{newPath}},
	}
}

// listen gives Bruce a place in the source: a position and a book state.
func (f *listenerFixture) listen(t *testing.T, seconds float64, pct int) {
	t.Helper()
	require.NoError(t, f.store.SetUserPosition(f.userID, f.source.ID, "seg-1", seconds))
	require.NoError(t, f.store.SetUserBookState(&database.UserBookState{
		UserID: f.userID, BookID: f.source.ID, Status: database.UserBookStatusInProgress, ProgressPct: pct,
	}))
}

func (f *listenerFixture) newFileID(t *testing.T, bookID string) string {
	t.Helper()
	files, err := f.store.GetBookFiles(bookID)
	require.NoError(t, err)
	require.Len(t, files, 1)
	return files[0].ID
}

func (f *listenerFixture) requirePositionOnlyOn(t *testing.T, onID, offID string, seconds float64) {
	t.Helper()
	on, err := f.store.ListUserPositionsForBook(f.userID, onID)
	require.NoError(t, err)
	require.Len(t, on, 1)
	require.InDelta(t, seconds, on[0].PositionSeconds, 0.001)
	off, err := f.store.ListUserPositionsForBook(f.userID, offID)
	require.NoError(t, err)
	require.Empty(t, off, "nothing is left on %s", offID)
}

// A book Bruce is partway through, copy-organized, keeps its sync id, its
// ino and his place on the new primary, and leaves nothing on the demoted
// original. Before the follow, the id and place stayed on the invisible
// original and the visible copy started at zero on a fresh id.
func TestCreateOrganizedVersion_FollowsSyncIdentityAndProgressToTheCopy(t *testing.T) {
	f := newListenerFixture(t)
	ids := database.AsSyncIdentityStore(f.store)
	sf := database.AsSyncFileStore(f.store)
	syncID, err := ids.MintOrGetSyncID(f.source.ID)
	require.NoError(t, err)
	ino, err := sf.MintOrGetSyncFileID(f.source.ID, f.fileID)
	require.NoError(t, err)
	f.listen(t, 1234, 40)

	created, err := NewService(f.store).CreateOrganizedVersion(f.source, f.landing, "", &noopLogger{})
	require.NoError(t, err)
	require.NotEqual(t, f.source.ID, created.ID)

	got, has, err := ids.GetSyncIDForBook(created.ID)
	require.NoError(t, err)
	require.True(t, has)
	require.Equal(t, syncID, got, "the client-visible id is the copy's")
	_, onSource, err := ids.GetSyncIDForBook(f.source.ID)
	require.NoError(t, err)
	require.False(t, onSource, "the demoted original keeps no sync id")

	copyFileID := f.newFileID(t, created.ID)
	require.NotEqual(t, f.fileID, copyFileID, "the copy's file row has a new id")
	gotIno, has, err := sf.GetSyncFileID(created.ID, copyFileID)
	require.NoError(t, err)
	require.True(t, has, "the ino resolves to a row the copy has")
	require.Equal(t, ino, gotIno)
	left, err := sf.ListSyncFilesForBook(f.source.ID)
	require.NoError(t, err)
	require.Empty(t, left)

	f.requirePositionOnlyOn(t, created.ID, f.source.ID, 1234)
	state, err := f.store.GetUserBookState(f.userID, created.ID)
	require.NoError(t, err)
	require.NotNil(t, state)
	require.Equal(t, 40, state.ProgressPct)
	old, err := f.store.GetUserBookState(f.userID, f.source.ID)
	require.NoError(t, err)
	if old != nil {
		require.Equal(t, 0, old.ProgressPct, "the original's state is neutralized")
	}

	// The handover itself still happened.
	src, err := f.store.GetBookByID(f.source.ID)
	require.NoError(t, err)
	require.False(t, *src.IsPrimaryVersion)
	require.Equal(t, "organized_source", *src.LibraryState)
}

// iTunes-bookmarked books get positions from backfill-itunes-positions and no
// sync id. The copy-organize of one (the itunes-clone and protected-library-
// copy callers start from exactly these) must still carry his place.
func TestCreateOrganizedVersion_FollowsProgressWhenTheSourceHasNoSyncID(t *testing.T) {
	f := newListenerFixture(t)
	f.listen(t, 900, 25)

	created, err := NewService(f.store).CreateOrganizedVersion(f.source, f.landing, "", &noopLogger{})
	require.NoError(t, err)

	f.requirePositionOnlyOn(t, created.ID, f.source.ID, 900)
	_, has, err := database.AsSyncIdentityStore(f.store).GetSyncIDForBook(created.ID)
	require.NoError(t, err)
	require.False(t, has, "no sync id is invented")
}

// A book nobody has listened to or synced is organized exactly as before:
// no sync id, no state, no pending repair.
func TestCreateOrganizedVersion_BookWithNoSyncStateIsUnchanged(t *testing.T) {
	f := newListenerFixture(t)

	created, err := NewService(f.store).CreateOrganizedVersion(f.source, f.landing, "", &noopLogger{})
	require.NoError(t, err)

	ids := database.AsSyncIdentityStore(f.store)
	for _, id := range []string{created.ID, f.source.ID} {
		_, has, err := ids.GetSyncIDForBook(id)
		require.NoError(t, err)
		require.False(t, has)
		pos, err := f.store.ListUserPositionsForBook(f.userID, id)
		require.NoError(t, err)
		require.Empty(t, pos)
		st, err := f.store.GetUserBookState(f.userID, id)
		require.NoError(t, err)
		require.Nil(t, st)
	}
	pending, err := f.store.ScanPrefix(merge.PendingUserStateRepairPrefix)
	require.NoError(t, err)
	require.Empty(t, pending)
}

// repointFailStore fails the sync-item repoint, the step that moves the
// client-visible id.
type repointFailStore struct{ *database.PebbleStore }

func (repointFailStore) RepointSyncItem(string, string) error {
	return errors.New("injected repoint failure")
}

// errLog records Error lines; the rest is discarded.
type errLog struct {
	noopLogger
	mu   sync.Mutex
	errs []string
}

func (l *errLog) Error(msg string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.errs = append(l.errs, strings.TrimSpace(fmt.Sprintf(msg, args...)))
}

func (l *errLog) With(string) logger.Logger { return l }

// A failed identity repoint does not fail or undo the organize (the files are
// copied and the rows written): the copy is returned, the original is demoted,
// identity and progress stay TOGETHER on the original (consistent, nothing
// lost), and the organize operation's log says so.
func TestCreateOrganizedVersion_SyncFollowFailureKeepsOrganizeAndLogsIt(t *testing.T) {
	f := newListenerFixture(t)
	ids := database.AsSyncIdentityStore(f.store)
	syncID, err := ids.MintOrGetSyncID(f.source.ID)
	require.NoError(t, err)
	f.listen(t, 1234, 40)
	log := &errLog{}

	created, err := NewService(repointFailStore{f.store}).CreateOrganizedVersion(f.source, f.landing, "", log)
	require.NoError(t, err, "a failed follow never fails the organize")
	require.NotNil(t, created)

	got, has, err := ids.GetSyncIDForBook(f.source.ID)
	require.NoError(t, err)
	require.True(t, has)
	require.Equal(t, syncID, got, "identity stays on the original")
	f.requirePositionOnlyOn(t, f.source.ID, created.ID, 1234)

	src, err := f.store.GetBookByID(f.source.ID)
	require.NoError(t, err)
	require.Equal(t, "organized_source", *src.LibraryState, "the original is still demoted")
	require.Len(t, log.errs, 1)
	require.Contains(t, log.errs[0], "did NOT move")
	require.Contains(t, log.errs[0], f.source.ID)
}

// plainStore hides every capability of the store: only the organizer.Store
// surface is visible, as with a test double.
type plainStore struct{ Store }

// A store that cannot move listening state is reported and the organize still
// succeeds.
func TestCreateOrganizedVersion_StoreWithoutListenerCapabilityWarnsAndOrganizes(t *testing.T) {
	f := newListenerFixture(t)
	f.listen(t, 1234, 40)

	created, err := NewService(plainStore{f.store}).CreateOrganizedVersion(f.source, f.landing, "", &noopLogger{})
	require.NoError(t, err)
	require.NotNil(t, created)
	f.requirePositionOnlyOn(t, f.source.ID, created.ID, 1234)
}
