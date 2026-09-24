// file: internal/versionprimary/vptest/vptest.go
// version: 1.0.0
// guid: 6d3f2a18-7c4b-4e91-a05d-8b2e9f1c4d70
// last-edited: 2026-09-24

// Package vptest seeds version groups on a real PebbleStore for the tests of
// every path that hands a group's primary flag on (versionprimary.Ensure-
// SinglePrimary / Crown callers), and asserts the invariant they share: the
// group ends with exactly one live explicit primary.
package vptest

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// Fixture is a Pebble store plus a library root on disk.
type Fixture struct {
	S    *database.PebbleStore
	Root string
}

// New opens a Pebble store in a temp dir, with a temp library root.
func New(t testing.TB) *Fixture {
	t.Helper()
	if testing.Short() {
		t.Skip("seeds a real PebbleStore; skipped in -short")
	}
	s, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	s.WaitForWarmup()
	return &Fixture{S: s, Root: t.TempDir()}
}

// Spec describes one seeded member.
type Spec struct {
	ID      string
	Group   string
	State   string // library_state; "" means organized
	Primary string // "true", "false" or "nil"
	// Outside puts the file outside the library root; NoFile seeds no
	// book_file row at all.
	Outside bool
	NoFile  bool
	Created time.Time
}

// Book creates the member and one .m4b file row whose file exists on disk.
func (f *Fixture) Book(t testing.TB, sp Spec) string {
	t.Helper()
	state := sp.State
	if state == "" {
		state = "organized"
	}
	g, created := sp.Group, sp.Created
	if created.IsZero() {
		created = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	b := &database.Book{ID: sp.ID, Title: "Book " + sp.ID, LibraryState: &state, CreatedAt: &created}
	if g != "" {
		b.VersionGroupID = &g
	}
	b.IsPrimaryVersion = flag(sp.Primary)
	out, err := f.S.CreateBook(b)
	require.NoError(t, err)
	_, err = f.S.ModifyBook(out.ID, func(row *database.Book) error {
		row.IsPrimaryVersion = flag(sp.Primary)
		return nil
	})
	require.NoError(t, err)
	if !sp.NoFile {
		dir := f.Root
		if sp.Outside {
			dir = t.TempDir()
		}
		path := filepath.Join(dir, "Author", sp.ID, sp.ID+".m4b")
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte("m4b"), 0o644))
		require.NoError(t, f.S.CreateBookFile(&database.BookFile{ID: "bf-" + sp.ID, BookID: out.ID, FilePath: path}))
		_, err = f.S.ModifyBook(out.ID, func(row *database.Book) error {
			row.FilePath = path
			return nil
		})
		require.NoError(t, err)
	}
	return out.ID
}

func flag(s string) *bool {
	switch s {
	case "true":
		v := true
		return &v
	case "false":
		v := false
		return &v
	}
	return nil
}

// Flag returns a book's stored flag as "true", "false" or "nil".
func (f *Fixture) Flag(t testing.TB, id string) string {
	t.Helper()
	b, err := f.S.GetBookByID(id)
	require.NoError(t, err)
	require.NotNil(t, b)
	switch {
	case b.IsPrimaryVersion == nil:
		return "nil"
	case *b.IsPrimaryVersion:
		return "true"
	}
	return "false"
}

// LivePrimaries lists the live (not soft-deleted) members of gid that store
// explicit true.
func (f *Fixture) LivePrimaries(t testing.TB, gid string) []string {
	t.Helper()
	members, err := f.S.GetBooksByVersionGroup(gid)
	require.NoError(t, err)
	var out []string
	for i := range members {
		m := &members[i]
		if !m.IsSoftDeleted() && m.IsPrimaryVersion != nil && *m.IsPrimaryVersion {
			out = append(out, m.ID)
		}
	}
	return out
}

// RequireSinglePrimary asserts gid has exactly one live explicit primary,
// and that it is want when want is not empty. Returns it.
func (f *Fixture) RequireSinglePrimary(t testing.TB, gid, want string) string {
	t.Helper()
	got := f.LivePrimaries(t, gid)
	require.Len(t, got, 1, "group %s live explicit primaries: %v", gid, got)
	if want != "" {
		require.Equal(t, want, got[0])
	}
	return got[0]
}

// SoftDelete marks a book deleted without touching its flag, as a retiring
// path would before handing off.
func (f *Fixture) SoftDelete(t testing.TB, id string) {
	t.Helper()
	_, err := f.S.ModifyBook(id, func(b *database.Book) error {
		v, now := true, time.Now()
		b.MarkedForDeletion, b.MarkedForDeletionAt = &v, &now
		return nil
	})
	require.NoError(t, err)
}
