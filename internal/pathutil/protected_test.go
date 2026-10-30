// file: internal/pathutil/protected_test.go
// version: 1.0.0
// guid: 5c0d7e92-3b84-4f1a-9a6e-2d18c4b7e063
// last-edited: 2026-10-29

package pathutil

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// protectedFixture is a library with an iTunes folder ("Gotham Music") holding
// one file, and a library folder ("Arkham") that is not protected. The base is
// t.TempDir() unresolved on purpose: on macOS it lives under /var, a link to
// /private/var, so every resolved spelling differs from the spelled one and a
// matcher that compared the wrong pair would show it.
type protectedFixture struct {
	base      string
	itunes    string // the protected folder
	hush      string // a real file inside it
	arkham    string // an unprotected folder
	unrelated string // a real file inside it
}

func newProtectedFixture(t *testing.T) *protectedFixture {
	t.Helper()
	base := t.TempDir()
	f := &protectedFixture{
		base:   base,
		itunes: filepath.Join(base, "lib", "Gotham Music"),
		arkham: filepath.Join(base, "lib", "Arkham"),
	}
	f.hush = filepath.Join(f.itunes, "Hush", "hush.m4b")
	f.unrelated = filepath.Join(f.arkham, "Joker", "killing-joke.m4b")
	for _, p := range []string{f.hush, f.unrelated} {
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte("x"), 0o644))
	}
	return f
}

func (f *protectedFixture) link(t *testing.T, target, name string) string {
	t.Helper()
	l := filepath.Join(f.base, "lib", name)
	require.NoError(t, os.Symlink(target, l))
	return l
}

func TestUnderProtectedPath_PlainSpellingIsProtected(t *testing.T) {
	f := newProtectedFixture(t)
	got, err := UnderProtectedPath(f.hush, []string{f.itunes})
	require.NoError(t, err)
	assert.True(t, got)

	got, err = UnderProtectedPath(f.itunes, []string{f.itunes})
	require.NoError(t, err)
	assert.True(t, got, "the folder itself")

	// A file that does not exist yet under the folder.
	got, err = UnderProtectedPath(filepath.Join(f.itunes, "Hush", "new.m4b"), []string{f.itunes})
	require.NoError(t, err)
	assert.True(t, got)
}

func TestUnderProtectedPath_UnprotectedPathStaysUnprotected(t *testing.T) {
	f := newProtectedFixture(t)
	// A link that points somewhere harmless must not be taken for protected.
	harmless := f.link(t, f.arkham, "harmless")
	// A sibling that shares the protected folder's name as a prefix.
	sibling := filepath.Join(f.base, "lib", "Gotham Music 2", "x.m4b")
	require.NoError(t, os.MkdirAll(filepath.Dir(sibling), 0o755))
	require.NoError(t, os.WriteFile(sibling, []byte("x"), 0o644))

	for name, p := range map[string]string{
		"real file":                   f.unrelated,
		"through a harmless link":     filepath.Join(harmless, "Joker", "killing-joke.m4b"),
		"missing under the library":   filepath.Join(f.arkham, "Joker", "missing.m4b"),
		"sibling folder, same prefix": sibling,
	} {
		t.Run(name, func(t *testing.T) {
			got, err := UnderProtectedPath(p, []string{f.itunes})
			require.NoError(t, err)
			assert.False(t, got)
		})
	}
	got, err := UnderProtectedPath(f.unrelated, nil)
	require.NoError(t, err)
	assert.False(t, got, "no roots at all")
}

func TestUnderProtectedPath_SymlinkedFileIntoProtectedFolder(t *testing.T) {
	f := newProtectedFixture(t)
	l := filepath.Join(f.arkham, "link.m4b")
	require.NoError(t, os.Symlink(f.hush, l))
	got, err := UnderProtectedPath(l, []string{f.itunes})
	require.NoError(t, err)
	assert.True(t, got)
}

func TestUnderProtectedPath_SymlinkedParentDirectory(t *testing.T) {
	f := newProtectedFixture(t)
	l := f.link(t, filepath.Join(f.itunes, "Hush"), "link")
	got, err := UnderProtectedPath(filepath.Join(l, "hush.m4b"), []string{f.itunes})
	require.NoError(t, err)
	assert.True(t, got)
}

func TestUnderProtectedPath_ProtectedFolderConfiguredThroughASymlink(t *testing.T) {
	f := newProtectedFixture(t)
	// The configured folder is the link; the file is spelled by its real path,
	// and by the link's.
	root := f.link(t, f.itunes, "music")
	for name, p := range map[string]string{
		"real spelling":   f.hush,
		"link spelling":   filepath.Join(root, "Hush", "hush.m4b"),
		"missing, real":   filepath.Join(f.itunes, "Hush", "new.m4b"),
		"missing, linked": filepath.Join(root, "Hush", "new.m4b"),
	} {
		t.Run(name, func(t *testing.T) {
			got, err := UnderProtectedPath(p, []string{root})
			require.NoError(t, err)
			assert.True(t, got)
		})
	}
	got, err := UnderProtectedPath(f.unrelated, []string{root})
	require.NoError(t, err)
	assert.False(t, got, "control: the link root does not protect an unrelated folder")
}

func TestUnderProtectedPath_DanglingLink(t *testing.T) {
	f := newProtectedFixture(t)
	missing := filepath.Join(f.itunes, "Hush", "gone.m4b")
	abs := filepath.Join(f.arkham, "dangling-abs.m4b")
	require.NoError(t, os.Symlink(missing, abs))
	rel := filepath.Join(f.arkham, "dangling-rel.m4b")
	require.NoError(t, os.Symlink(filepath.Join("..", "Gotham Music", "Hush", "gone.m4b"), rel))
	harmless := filepath.Join(f.arkham, "dangling-harmless.m4b")
	require.NoError(t, os.Symlink(filepath.Join(f.arkham, "Joker", "gone.m4b"), harmless))

	for name, p := range map[string]string{"absolute target": abs, "relative target": rel} {
		t.Run(name, func(t *testing.T) {
			got, err := UnderProtectedPath(p, []string{f.itunes})
			require.NoError(t, err)
			assert.True(t, got)
		})
	}
	got, err := UnderProtectedPath(harmless, []string{f.itunes})
	require.NoError(t, err)
	assert.False(t, got, "a dangling link to an unprotected place is unprotected")
}

func TestUnderProtectedPath_MissingFinalElementUnderASymlinkedDirectory(t *testing.T) {
	f := newProtectedFixture(t)
	l := f.link(t, filepath.Join(f.itunes, "Hush"), "link")
	got, err := UnderProtectedPath(filepath.Join(l, "not-yet", "new.m4b"), []string{f.itunes})
	require.NoError(t, err)
	assert.True(t, got)

	harmless := f.link(t, filepath.Join(f.arkham, "Joker"), "harmless")
	got, err = UnderProtectedPath(filepath.Join(harmless, "not-yet", "new.m4b"), []string{f.itunes})
	require.NoError(t, err)
	assert.False(t, got)
}

func TestUnderProtectedPath_ITunesMediaAndFailedFoldersReachedThroughALink(t *testing.T) {
	f := newProtectedFixture(t)
	media := filepath.Join(f.arkham, "iTunes Media", "Audiobooks")
	failed := filepath.Join(f.arkham, ".failed", "Knightfall")
	for _, d := range []string{media, failed} {
		require.NoError(t, os.MkdirAll(d, 0o755))
	}
	mediaLink := f.link(t, media, "media-link")
	failedLink := f.link(t, failed, "failed-link")

	got, err := UnderProtectedPath(filepath.Join(mediaLink, "hush.m4b"), nil)
	require.NoError(t, err)
	assert.True(t, got, "iTunes Media only in the resolved spelling")
	got, err = UnderProtectedPath(filepath.Join(failedLink, "knightfall.m4b"), nil)
	require.NoError(t, err)
	assert.True(t, got, ".failed only in the resolved spelling")

	// And in the spelled one, with no filesystem at all.
	got, err = UnderProtectedPath(filepath.Join(f.base, "nowhere", "iTunes Media", "x.m4b"), nil)
	require.NoError(t, err)
	assert.True(t, got)
	got, err = UnderProtectedPath(filepath.Join(f.base, "nowhere", ".failed", "x.m4b"), nil)
	require.NoError(t, err)
	assert.True(t, got)
}

// A path that cannot be resolved for a reason other than not-exist is protected,
// and the error says why. A link cycle is the portable way to get one (a
// permission error does not happen when the tests run as root).
func TestUnderProtectedPath_ResolutionErrorFailsClosed(t *testing.T) {
	f := newProtectedFixture(t)
	a := filepath.Join(f.arkham, "a")
	b := filepath.Join(f.arkham, "b")
	require.NoError(t, os.Symlink(b, a))
	require.NoError(t, os.Symlink(a, b))

	t.Run("the path", func(t *testing.T) {
		got, err := UnderProtectedPath(filepath.Join(a, "x.m4b"), []string{f.itunes})
		assert.True(t, got)
		assert.Error(t, err)
	})
	t.Run("a protected folder", func(t *testing.T) {
		got, err := UnderProtectedPath(f.unrelated, []string{a})
		assert.True(t, got)
		assert.Error(t, err)
	})
	t.Run("the plain spelling still wins without an error", func(t *testing.T) {
		// Under the protected folder as spelled, so nothing is resolved.
		loopRoot := filepath.Join(f.itunes, "loop")
		require.NoError(t, os.Symlink(loopRoot, loopRoot))
		got, err := UnderProtectedPath(filepath.Join(loopRoot, "x.m4b"), []string{f.itunes})
		assert.True(t, got)
		assert.NoError(t, err)
	})
}

func TestResolveExistingPrefix(t *testing.T) {
	f := newProtectedFixture(t)
	real, err := filepath.EvalSymlinks(f.itunes)
	require.NoError(t, err)

	l := f.link(t, f.itunes, "music")
	got, err := ResolveExistingPrefix(filepath.Join(l, "Hush", "new", "x.m4b"))
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(real, "Hush", "new", "x.m4b"), got)

	a := filepath.Join(f.arkham, "a")
	b := filepath.Join(f.arkham, "b")
	require.NoError(t, os.Symlink(b, a))
	require.NoError(t, os.Symlink(a, b))
	_, err = ResolveExistingPrefix(a)
	assert.Error(t, err)
}

// A link cycle is ELOOP, so a purge can tell a path that will never resolve from
// a filesystem that failed to answer. This also pins the message
// evalSymlinksCycle matches: if a Go release rewords it, this fails.
func TestResolveExistingPrefix_LinkCycleIsELOOP(t *testing.T) {
	f := newProtectedFixture(t)
	a := filepath.Join(f.arkham, "a")
	b := filepath.Join(f.arkham, "b")
	require.NoError(t, os.Symlink(b, a))
	require.NoError(t, os.Symlink(a, b))

	_, err := ResolveExistingPrefix(filepath.Join(a, "x.m4b"))
	require.Error(t, err)
	assert.ErrorIs(t, err, syscall.ELOOP)

	// A cycle of dangling relative links goes through the Readlink loop.
	c := filepath.Join(f.arkham, "c")
	d := filepath.Join(f.arkham, "d")
	require.NoError(t, os.Symlink("d", c))
	require.NoError(t, os.Symlink("c", d))
	_, err = ResolveExistingPrefix(c)
	require.Error(t, err)
	assert.ErrorIs(t, err, syscall.ELOOP)
}
