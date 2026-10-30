// file: internal/server/protected_path_test.go
// version: 1.2.0
// guid: 7e2c4a19-0b53-4d86-9f1a-c8d5e3b7a602
// last-edited: 2026-10-29

package server

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/deluge"
	"github.com/falkcorp/audiobook-organizer/internal/organizer"
)

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

	unreachable, err := deluge.New("http://deluge.invalid:8112", "deluge")
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
