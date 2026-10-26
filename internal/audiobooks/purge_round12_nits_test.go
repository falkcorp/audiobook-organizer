// file: internal/audiobooks/purge_round12_nits_test.go
// version: 1.0.0
// guid: 5e2a8c13-7b94-4d06-a1f8-92c3d70b6e48
// last-edited: 2026-10-26

package audiobooks

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
)

// n-8. A Pebble store whose in-memory layer is off cannot sample the library's
// live files, so the library-root check is "not ready": a purge that deletes
// files fails closed instead of reading "no file here" as "already gone".
func TestLibraryRootReady_PebbleWithMemdbOffIsNotReady(t *testing.T) {
	svc, store, base := setupPurgeBoundary(t)
	liveBookWithFile(t, store, "mounted-witness", filepath.Join(base, "lib", "Gotham", "hush.m4b"))
	require.NoError(t, svc.libraryRootReady(config.AppConfig.RootDir), "with memdb serving the library is ready")

	store.UseMemDB = false
	err := svc.libraryRootReady(config.AppConfig.RootDir)
	require.ErrorIs(t, err, ErrLibraryRootNotReady)
	assert.Contains(t, err.Error(), "cannot sample")
	assert.Contains(t, err.Error(), "memdb is not serving")
}

// n-8. folderCheck: the dirSet exemption is what keeps a folder this run has
// itself emptied from reading as "a mount point that is not up", and nothing
// else. Each case names what makes the folder empty-and-unexplained or not.
func TestFolderCheck_EmptyFolderIsUnexplainedUnlessTheRunEmptiedItOrItIsTheRoot(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "lib")
	emptyDir := filepath.Join(root, "Gotham")
	require.NoError(t, os.MkdirAll(emptyDir, 0o755))
	gone := filepath.Join(emptyDir, "hush.m4b") // never created
	full := filepath.Join(root, "Wayne")
	require.NoError(t, os.MkdirAll(full, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(full, "year-one.m4b"), []byte("x"), 0o644))

	t.Run("empty and nothing explains it", func(t *testing.T) {
		missing, empty, err := folderCheck(gone, root, &dirSet{})
		require.NoError(t, err)
		assert.False(t, missing)
		assert.True(t, empty)
	})
	t.Run("a nil set explains nothing", func(t *testing.T) {
		_, empty, err := folderCheck(gone, root, nil)
		require.NoError(t, err)
		assert.True(t, empty)
	})
	t.Run("the run emptied it", func(t *testing.T) {
		set := &dirSet{}
		set.add(emptyDir + string(filepath.Separator)) // a trailing separator names the same folder
		missing, empty, err := folderCheck(gone, root, set)
		require.NoError(t, err)
		assert.False(t, missing)
		assert.False(t, empty, "a folder the run is unlinking in is not a mount point that is not up")
	})
	t.Run("another folder emptied by the run does not explain this one", func(t *testing.T) {
		set := &dirSet{}
		set.add(filepath.Join(root, "Arkham"))
		_, empty, err := folderCheck(gone, root, set)
		require.NoError(t, err)
		assert.True(t, empty)
	})
	t.Run("a folder with something in it is simply a folder", func(t *testing.T) {
		missing, empty, err := folderCheck(filepath.Join(full, "hush.m4b"), root, &dirSet{})
		require.NoError(t, err)
		assert.False(t, missing)
		assert.False(t, empty)
	})
	t.Run("a folder that is not there is missing", func(t *testing.T) {
		missing, empty, err := folderCheck(filepath.Join(root, "Arkham", "hush.m4b"), root, &dirSet{})
		require.NoError(t, err)
		assert.True(t, missing)
		assert.False(t, empty)
	})
	t.Run("the library root itself is exempt", func(t *testing.T) {
		emptyRoot := filepath.Join(base, "empty-lib")
		require.NoError(t, os.MkdirAll(emptyRoot, 0o755))
		missing, empty, err := folderCheck(filepath.Join(emptyRoot, "hush.m4b"), emptyRoot, nil)
		require.NoError(t, err)
		assert.False(t, missing)
		assert.False(t, empty, "the root check vouches for the root")
		// The same empty folder, not being the root, is not exempt.
		_, empty, err = folderCheck(filepath.Join(emptyRoot, "hush.m4b"), root, nil)
		require.NoError(t, err)
		assert.True(t, empty)
	})
}

// The set is shared by the workers of one run: adds and lookups from many
// goroutines are safe (run with -race) and every add is seen afterwards.
func TestDirSet_IsSafeForConcurrentWorkers(t *testing.T) {
	var set dirSet
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				dir := filepath.Join("/lib", "Gotham", fmt.Sprintf("w%02d-%02d", i, j))
				set.add(dir)
				assert.True(t, set.has(dir))
			}
		}()
	}
	wg.Wait()
	assert.True(t, set.has("/lib/Gotham/w15-49"))
	assert.False(t, set.has("/lib/Gotham/never-added"))
}
