// file: internal/server/server_library_clone_test.go
// version: 1.0.0
// guid: 8d2e4b17-6a3c-4f59-b0e8-5c1a9d7f2e64
// last-edited: 2026-09-24

package server

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/fileops"
	"github.com/falkcorp/audiobook-organizer/internal/organizer"
)

// requireReflink skips when dir's filesystem cannot clone: the cloner is
// reflink-only by design, so on such a filesystem the only correct outcome
// is a failure, which is not what these tests exercise.
func requireReflink(t *testing.T, dir string) {
	t.Helper()
	src := filepath.Join(dir, ".reflink-probe")
	require.NoError(t, os.WriteFile(src, []byte("probe"), 0o644))
	err := fileops.Reflink(src, src+".clone")
	if errors.Is(err, fileops.ErrReflinkUnsupported) {
		t.Skip("filesystem cannot reflink; the cloner never falls back to a copy")
	}
	require.NoError(t, err)
	_ = os.Remove(src)
	_ = os.Remove(src + ".clone")
}

// cloneFixture is an "iTunes" book outside the library root whose files carry
// iTunes PIDs, and a server wired with the real organize service. The config
// strategy is "copy" on purpose: the cloner must force reflink regardless of
// the configured strategy, and must not leak that override into AppConfig.
func cloneFixture(t *testing.T, nFiles int) (*Server, *database.PebbleStore, *database.Book, []database.BookFile, string) {
	t.Helper()
	base := t.TempDir()
	requireReflink(t, base)
	root := filepath.Join(base, "library")
	srcDir := filepath.Join(base, "itunes", "Audiobooks", "Clone Title")
	require.NoError(t, os.MkdirAll(root, 0o755))
	require.NoError(t, os.MkdirAll(srcDir, 0o755))

	old := config.AppConfig
	t.Cleanup(func() { config.AppConfig = old })
	config.AppConfig.RootDir = root
	config.AppConfig.OrganizationStrategy = "copy"

	store := rowWritersStore(t)
	author, err := store.CreateAuthor("Clone Author")
	require.NoError(t, err)
	st := "organized"
	book, err := store.CreateBook(&database.Book{FilePath: srcDir, Title: "Clone Title",
		AuthorID: &author.ID, LibraryState: &st})
	require.NoError(t, err)
	for i := 0; i < nFiles; i++ {
		p := filepath.Join(srcDir, fmt.Sprintf("%02d - part.m4b", i+1))
		require.NoError(t, os.WriteFile(p, []byte(fmt.Sprintf("audio-%d", i)), 0o644))
		require.NoError(t, store.CreateBookFile(&database.BookFile{BookID: book.ID, FilePath: p,
			TrackNumber: i + 1, TrackCount: nFiles, ITunesPersistentID: fmt.Sprintf("PID-CLONE-%d", i)}))
	}
	files, err := store.GetBookFiles(book.ID)
	require.NoError(t, err)
	return &Server{store: store, organizeService: NewOrganizeService(store)}, store, book, files, root
}

func TestCloneBookIntoLibrary(t *testing.T) {
	if testing.Short() {
		t.Skip("touches the filesystem and the full organize pipeline")
	}
	for _, n := range []int{1, 2} {
		t.Run(fmt.Sprintf("%d_files", n), func(t *testing.T) {
			srv, store, book, files, root := cloneFixture(t, n)
			before := map[string][]byte{}
			for _, f := range files {
				b, err := os.ReadFile(f.FilePath)
				require.NoError(t, err)
				before[f.FilePath] = b
			}

			planned, err := srv.PlanLibraryClone(book, files)
			require.NoError(t, err)
			require.Len(t, planned, n)
			for _, p := range planned {
				require.True(t, strings.HasPrefix(p, root+string(filepath.Separator)), "planned %s outside root", p)
				_, statErr := os.Stat(p)
				require.True(t, os.IsNotExist(statErr), "planning wrote %s", p)
			}

			cloneID, err := srv.CloneBookIntoLibrary(book, files, "op-clone-test")
			require.NoError(t, err)
			require.NotEqual(t, book.ID, cloneID)
			require.Equal(t, "copy", config.AppConfig.OrganizationStrategy, "the reflink override leaked into AppConfig")

			// The clone landed exactly where the plan said, one row per file.
			rows, err := store.GetBookFiles(cloneID)
			require.NoError(t, err)
			got := make([]string, 0, len(rows))
			for _, r := range rows {
				got = append(got, r.FilePath)
			}
			require.ElementsMatch(t, planned, got)

			// The library copy owns every PID; the iTunes files are untouched.
			for i, f := range files {
				owner, err := store.GetBookFileByPID(fmt.Sprintf("PID-CLONE-%d", i))
				require.NoError(t, err)
				require.NotNil(t, owner)
				require.Equal(t, cloneID, owner.BookID)
				after, err := os.ReadFile(f.FilePath)
				require.NoError(t, err)
				require.Equal(t, before[f.FilePath], after)
			}
			clone, err := store.GetBookByID(cloneID)
			require.NoError(t, err)
			require.Equal(t, "organized", *clone.LibraryState)
			src, err := store.GetBookByID(book.ID)
			require.NoError(t, err)
			require.Equal(t, "organized_source", *src.LibraryState)

			// A second clone refuses before writing anything: no _copyN
			// sibling, no book adopting the first clone's files.
			nBefore, err := store.GetAllBooksCore(0, 0)
			require.NoError(t, err)
			_, err = srv.CloneBookIntoLibrary(book, files, "op-clone-test-2")
			require.ErrorIs(t, err, fs.ErrExist)
			nAfter, err := store.GetAllBooksCore(0, 0)
			require.NoError(t, err)
			require.Len(t, nAfter, len(nBefore), "a refused clone made a book row")
			matches, err := filepath.Glob(filepath.Join(root, "*_copy*"))
			require.NoError(t, err)
			require.Empty(t, matches)
		})
	}
}

// The pre-check cannot see a writer that races in between it and the
// organize; freshLanding is what catches that, from the Landing alone.
func TestFreshLanding(t *testing.T) {
	a, b := "/lib/A/01.m4b", "/lib/A/02.m4b"
	cases := []struct {
		name    string
		landing organizer.Landing
		planned []string
		ok      bool
	}{
		{"single written", organizer.Landing{Path: a, Created: []string{a}}, []string{a}, true},
		{"single adopted", organizer.Landing{Path: a}, []string{a}, false},
		{"single copyN", organizer.Landing{Path: "/lib/A/01_copy1.m4b", Created: []string{"/lib/A/01_copy1.m4b"}}, []string{a}, false},
		{"dir written", organizer.Landing{Path: "/lib/A", Files: map[string]string{"s1": a, "s2": b}, Created: []string{a, b}}, []string{a, b}, true},
		{"dir one adopted", organizer.Landing{Path: "/lib/A", Files: map[string]string{"s1": a, "s2": b}, Created: []string{a}}, []string{a, b}, false},
		{"dir short", organizer.Landing{Path: "/lib/A", Files: map[string]string{"s1": a}, Created: []string{a}}, []string{a, b}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			l := c.landing
			err := freshLanding(&l, c.planned)
			if c.ok {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, errLandingNotFresh)
			}
		})
	}
}
