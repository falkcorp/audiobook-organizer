// file: internal/server/protected_path_test.go
// version: 1.4.0
// guid: 7e2c4a19-0b53-4d86-9f1a-c8d5e3b7a602
// last-edited: 2026-10-30

package server

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/deluge"
	"github.com/falkcorp/audiobook-organizer/internal/organizer"
	"github.com/falkcorp/audiobook-organizer/internal/tagger"
)

// closedLoopbackURL is the URL of a loopback port nothing listens on: the port is
// bound and released, so a client dialling it is refused at once instead of
// waiting out a DNS lookup or the client's timeout.
func closedLoopbackURL(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := l.Addr().String()
	require.NoError(t, l.Close())
	return "http://" + addr
}

func TestIsProtectedPath_FailedDir(t *testing.T) {
	// isProtectedPath is now a method on *Server (SERVER-GLOBAL-STORE-AUDIT
	// phase 3a). A zero-value Server is fine for this test — only the
	// ".failed/" string-match branch is exercised; s.Ops() returns nil
	// so the import-path branch is skipped.
	s := &Server{}
	cases := []struct {
		path     string
		expected bool
	}{
		{"/library/.failed/Author/Book/book.m4b", true},
		{"/library/.failed/book.m4b", true},
		{"/library/Author/Book/book.m4b", false},
		{"/library/.failedish/book.m4b", false},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.expected, s.isProtectedPath(tc.path), "path: %s", tc.path)
	}
}

// A path spelled through a symlink into the iTunes library folder is protected:
// this predicate steers the organizer between move and copy, so an unresolved
// spelling would let it move a real iTunes file. An unrelated path, with a link
// near it, is not.
func TestIsProtectedPath_SymlinkIntoITunesFolder(t *testing.T) {
	prev := config.AppConfig.ITunes.LibraryReadPath
	t.Cleanup(func() { config.AppConfig.ITunes.LibraryReadPath = prev })
	base := t.TempDir()
	itunes := filepath.Join(base, "lib", "Gotham Music")
	config.AppConfig.ITunes.LibraryReadPath = filepath.Join(itunes, "Library.xml")
	require.NoError(t, os.MkdirAll(filepath.Join(itunes, "Hush"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(base, "lib", "Arkham", "Joker"), 0o755))
	into := filepath.Join(base, "lib", "link")
	require.NoError(t, os.Symlink(filepath.Join(itunes, "Hush"), into))
	away := filepath.Join(base, "lib", "away")
	require.NoError(t, os.Symlink(filepath.Join(base, "lib", "Arkham", "Joker"), away))

	s := &Server{}
	assert.True(t, s.isProtectedPath(filepath.Join(into, "hush.m4b")))
	assert.False(t, s.isProtectedPath(filepath.Join(away, "killing-joke.m4b")))
}

// The organizer's rename-source guard tells "protected" from "the Deluge list
// has not loaded". With Deluge configured but unreachable, a library path is
// SourceListNotLoaded (not SourceProtected: it is not known to be protected), a
// path under a static protected prefix is still SourceProtected, and with the list
// loaded a library path is SourceMovable.
func TestRenameSourceVerdict_TellsAnUnloadedDelugeListFromAProtectedPath(t *testing.T) {
	keep := filepath.Join(t.TempDir(), "Gotham Music")
	library := filepath.Join(t.TempDir(), "Arkham", "Joker", "killing-joke.m4b")

	unreachable, err := deluge.New(closedLoopbackURL(t), "deluge")
	require.NoError(t, err)
	down := &Server{protectedPathCache: deluge.NewProtectedPathCache(unreachable, []string{keep})}
	require.False(t, down.protectedPathCache.Loaded(), "fixture: the Deluge list must not have loaded")

	assert.Equal(t, organizer.SourceListNotLoaded, down.renameSourceVerdict(library))
	assert.Equal(t, organizer.SourceProtected, down.renameSourceVerdict(filepath.Join(keep, "Hush", "hush.m4b")),
		"a protected path is protected whether or not the list has loaded")

	up := &Server{protectedPathCache: deluge.NewProtectedPathCache(nil, []string{keep})}
	assert.Equal(t, organizer.SourceMovable, up.renameSourceVerdict(library))
	assert.Equal(t, organizer.SourceProtected, up.renameSourceVerdict(filepath.Join(keep, "Hush", "hush.m4b")))

	assert.Equal(t, organizer.SourceMovable, (&Server{}).renameSourceVerdict(library), "no cache: nothing to wait for")
}

// fakeDelugeList is a protected-path checker whose Deluge list loads at a moment
// the test chooses; before that it reports nothing protected, as the real one does.
type fakeDelugeList struct {
	loaded  bool
	seeding string
}

func (f *fakeDelugeList) IsProtected(p string) bool { return f.loaded && p == f.seeding }
func (f *fakeDelugeList) Loaded() bool              { return f.loaded }

var _ tagger.PathChecker = (*fakeDelugeList)(nil)
var _ tagger.LoadReporter = (*fakeDelugeList)(nil)

// A Deluge refresh that lands DURING the protected check must not make a seeding
// file look movable. The check below sees the list unloaded (so the seeding file
// is not found), and the refresh then lands; reading "loaded" after the check
// would say true and the file would be cleared to move.
func TestRenameVerdict_ARefreshLandingDuringTheCheckDoesNotClearASeedingFile(t *testing.T) {
	list := &fakeDelugeList{seeding: "/downloads/Batman Hush/hush.m4b"}
	isProtected := func(p string) bool {
		found := list.IsProtected(p)
		list.loaded = true // the refresh lands right after the check looked
		return found
	}

	got := renameVerdict(list.seeding, list, isProtected)
	assert.Equal(t, organizer.SourceListNotLoaded, got,
		"the list was down when the check began, so this answer is 'wait', not 'movable'")

	assert.Equal(t, organizer.SourceProtected, renameVerdict(list.seeding, list, isProtected),
		"asked again with the list loaded, the seeding file is protected")
}

// While the import-path list cannot be read, a file under an import path must not
// be answered "not protected": the rename-source guard would call it movable and an
// in-place move could take it out of the import folder. Every path is protected
// until the list reads again, and the next read recovers.
func TestIsProtectedPath_UnreadableImportPathListFailsClosed(t *testing.T) {
	importPathCacheMu.Lock()
	importPathCache = nil
	importPathCacheAt = time.Time{}
	importPathCacheMu.Unlock()
	t.Cleanup(func() {
		importPathCacheMu.Lock()
		importPathCache = nil
		importPathCacheAt = time.Time{}
		importPathCacheMu.Unlock()
	})

	base := t.TempDir()
	imports := filepath.Join(base, "Gotham Imports")
	library := filepath.Join(base, "Arkham", "Joker", "killing-joke.m4b")
	inImports := filepath.Join(imports, "Hush", "hush.m4b")

	failing := true
	mock := &database.MockStore{
		GetAllImportPathsFunc: func() ([]database.ImportPath, error) {
			if failing {
				return nil, errors.New("pebble: closed")
			}
			return []database.ImportPath{{Path: imports}}, nil
		},
	}
	srv := newTestServer(t, mock)

	assert.True(t, srv.isProtectedPath(inImports), "a file under an import path is protected while the list is unreadable")
	assert.True(t, srv.isProtectedPath(library), "no path can be vouched for while the list is unreadable")
	assert.Equal(t, organizer.SourceProtected, srv.renameSourceVerdict(inImports),
		"the rename-source guard must refuse, not answer SourceMovable")

	failing = false
	assert.True(t, srv.isProtectedPath(inImports))
	assert.False(t, srv.isProtectedPath(library), "once the list reads again a library file is movable")
	assert.Equal(t, organizer.SourceMovable, srv.renameSourceVerdict(library))
}
