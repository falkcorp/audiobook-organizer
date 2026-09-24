// file: internal/metafetch/service_apply_match4_versiongroup_test.go
// version: 1.0.0
// guid: 7669f76e-d58c-4ea7-9a18-44badb294ca5
// last-edited: 2026-09-24

package metafetch

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// match4Fixture is a real Pebble store with a library root and a separate
// "newbooks" import tree, the two places the prod organize pair lives.
type match4Fixture struct {
	store    *database.PebbleStore
	root     string
	newbooks string
	hash     string
}

func newMatch4Fixture(t *testing.T) *match4Fixture {
	t.Helper()
	store, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, database.RunMigrations(store))
	t.Cleanup(func() { _ = store.Close() })
	orig := config.AppConfig
	t.Cleanup(func() { config.AppConfig = orig })
	f := &match4Fixture{store: store, root: t.TempDir(), newbooks: t.TempDir(), hash: "match4-hash"}
	config.AppConfig.RootDir = f.root
	return f
}

// book creates a book in gid (none when "") whose files are real files under
// dir, and stamps the shared metadata_source_hash.
func (f *match4Fixture) book(t *testing.T, id, gid, state, dir string, files int, created time.Time) {
	t.Helper()
	st, c, h := state, created, f.hash
	b := &database.Book{ID: id, Title: "The Lost Star Gate", LibraryState: &st, CreatedAt: &c, MetadataSourceHash: &h}
	if gid != "" {
		g := gid
		b.VersionGroupID = &g
	}
	_, err := f.store.CreateBook(b)
	require.NoError(t, err)
	for i := 0; i < files; i++ {
		path := filepath.Join(dir, "Author", "The Lost Star Gate "+id, id+string(rune('a'+i))+".m4b")
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte("m4b"), 0o644))
		require.NoError(t, f.store.CreateBookFile(&database.BookFile{ID: "bf-" + id + string(rune('a'+i)), BookID: id, FilePath: path}))
	}
}

func (f *match4Fixture) mergedInto(t *testing.T, id string) string {
	t.Helper()
	b, err := f.store.GetBookByID(id)
	require.NoError(t, err)
	require.NotNil(t, b)
	if b.MergedIntoBookID == nil {
		return ""
	}
	return *b.MergedIntoBookID
}

var match4Base = time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)

// The prod shape (The Lost Star Gate, 2026-09-24): the source copy in
// newbooks was created first, organize then created the library copy under
// the root in the SAME version group with the same file count. The old rule
// (most files, then earliest) kept the source and merged the library copy
// into it, leaving the group with no copy ABS can list. Versions of one book
// are not duplicates: nothing may be flagged.
func TestMatch4_SameVersionGroupIsNeverFlagged(t *testing.T) {
	f := newMatch4Fixture(t)
	f.book(t, "src", "vg-lsg", "organized_source", f.newbooks, 1, match4Base)
	f.book(t, "org", "vg-lsg", "organized", f.root, 1, match4Base.Add(time.Hour))

	svc := NewService(f.store)
	require.NoError(t, svc.checkMetadataSourceHashDuplicates("org", f.hash))
	require.Empty(t, f.mergedInto(t, "org"), "the organized copy must not be merged into its own source")
	require.Empty(t, f.mergedInto(t, "src"))
}

// Across version groups the eligible library copy survives even when the
// other copy is older and has more files.
func TestMatch4_EligibleCopyWinsAcrossGroups(t *testing.T) {
	f := newMatch4Fixture(t)
	f.book(t, "outside", "vg-a", "organized_source", f.newbooks, 2, match4Base)
	f.book(t, "library", "vg-b", "organized", f.root, 1, match4Base.Add(time.Hour))

	svc := NewService(f.store)
	require.NoError(t, svc.checkMetadataSourceHashDuplicates("library", f.hash))
	require.Empty(t, f.mergedInto(t, "library"))
	require.Equal(t, "library", f.mergedInto(t, "outside"))
}

// With no eligible candidate the old rule decides unchanged: most files,
// then the earliest created.
func TestMatch4_NoEligibleCandidateKeepsOldRule(t *testing.T) {
	f := newMatch4Fixture(t)
	f.book(t, "older", "vg-a", "organized_source", f.newbooks, 1, match4Base)
	f.book(t, "newer", "vg-b", "organized_source", f.newbooks, 1, match4Base.Add(time.Hour))

	svc := NewService(f.store)
	require.NoError(t, svc.checkMetadataSourceHashDuplicates("newer", f.hash))
	require.Empty(t, f.mergedInto(t, "older"))
	require.Equal(t, "older", f.mergedInto(t, "newer"))
}

// A cluster spanning a version group and an outside book: the group's
// eligible copy survives, the outside book is flagged into it, and the
// group's other member (a version, not a duplicate) is left alone.
func TestMatch4_OnlyOtherGroupsAreFlagged(t *testing.T) {
	f := newMatch4Fixture(t)
	f.book(t, "src", "vg-lsg", "organized_source", f.newbooks, 1, match4Base)
	f.book(t, "org", "vg-lsg", "organized", f.root, 1, match4Base.Add(time.Hour))
	f.book(t, "stray", "", "imported", f.newbooks, 1, match4Base.Add(-time.Hour))

	svc := NewService(f.store)
	require.NoError(t, svc.checkMetadataSourceHashDuplicates("stray", f.hash))
	require.Empty(t, f.mergedInto(t, "org"))
	require.Empty(t, f.mergedInto(t, "src"))
	require.Equal(t, "org", f.mergedInto(t, "stray"))
}
