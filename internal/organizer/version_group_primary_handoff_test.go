// file: internal/organizer/version_group_primary_handoff_test.go
// version: 1.0.0
// guid: 1f6d8a43-7b2e-4c95-9d10-e3a4b7c58f26
// last-edited: 2026-09-24

package organizer

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary/vptest"
)

// An incumbent primary ABS cannot show (not organized, no files on disk) no
// longer wins by default: the new library copy takes the primary and the
// incumbent is demoted, so the group still has exactly one.
func TestCreateOrganizedVersion_TakesPrimaryFromIneligibleIncumbent(t *testing.T) {
	f := vptest.New(t)
	prev := config.AppConfig.RootDir
	config.AppConfig.RootDir = f.Root
	t.Cleanup(func() { config.AppConfig.RootDir = prev })
	svc := NewService(f.S)

	incumbent := f.Book(t, vptest.Spec{ID: "inc", Group: "g", Primary: "true", State: "imported", NoFile: true})
	source := f.Book(t, vptest.Spec{ID: "src", Group: "g", Primary: "false", State: "imported", Outside: true})
	src, err := f.S.GetBookByID(source)
	require.NoError(t, err)

	newPath := filepath.Join(f.Root, "Real Author", "Book", "book.m4b")
	require.NoError(t, os.MkdirAll(filepath.Dir(newPath), 0o755))
	require.NoError(t, os.WriteFile(newPath, []byte("m4b"), 0o644))
	created, err := svc.CreateOrganizedVersion(src, &Landing{Path: newPath}, "", &noopLogger{})
	require.NoError(t, err)

	f.RequireSinglePrimary(t, "g", created.ID)
	require.Equal(t, "false", f.Flag(t, incumbent))
}

// An eligible incumbent keeps the primary (the pre-existing rule).
func TestCreateOrganizedVersion_EligibleIncumbentKeepsPrimary(t *testing.T) {
	f := vptest.New(t)
	prev := config.AppConfig.RootDir
	config.AppConfig.RootDir = f.Root
	t.Cleanup(func() { config.AppConfig.RootDir = prev })
	svc := NewService(f.S)

	incumbent := f.Book(t, vptest.Spec{ID: "inc", Group: "g", Primary: "true"})
	source := f.Book(t, vptest.Spec{ID: "src", Group: "g", Primary: "false", State: "imported", Outside: true})
	src, err := f.S.GetBookByID(source)
	require.NoError(t, err)

	newPath := filepath.Join(f.Root, "Unknown Author", "Book", "book.m4b")
	require.NoError(t, os.MkdirAll(filepath.Dir(newPath), 0o755))
	require.NoError(t, os.WriteFile(newPath, []byte("m4b"), 0o644))
	_, err = svc.CreateOrganizedVersion(src, &Landing{Path: newPath}, "", &noopLogger{})
	require.NoError(t, err)
	f.RequireSinglePrimary(t, "g", incumbent)
}

// groupReadFailStore fails the version-group read.
type groupReadFailStore struct{ *database.PebbleStore }

func (groupReadFailStore) GetBooksByVersionGroup(string) ([]database.Book, error) {
	return nil, os.ErrPermission
}

// A failed version-group read refuses the organize instead of creating a
// possibly second primary, and removes the file it landed.
func TestCreateOrganizedVersion_GroupReadFailureRefuses(t *testing.T) {
	f := vptest.New(t)
	prev := config.AppConfig.RootDir
	config.AppConfig.RootDir = f.Root
	t.Cleanup(func() { config.AppConfig.RootDir = prev })
	svc := NewService(groupReadFailStore{f.S})
	source := f.Book(t, vptest.Spec{ID: "src", Group: "g", Primary: "true", State: "imported"})
	src, err := f.S.GetBookByID(source)
	require.NoError(t, err)

	newPath := filepath.Join(f.Root, "Author", "Book", "landed.m4b")
	require.NoError(t, os.MkdirAll(filepath.Dir(newPath), 0o755))
	require.NoError(t, os.WriteFile(newPath, []byte("m4b"), 0o644))
	_, err = svc.CreateOrganizedVersion(src, &Landing{Path: newPath, Created: []string{newPath}}, "", &noopLogger{})
	require.Error(t, err)
	require.Equal(t, "true", f.Flag(t, source), "the source is not demoted")
	_, statErr := os.Stat(newPath)
	require.True(t, os.IsNotExist(statErr), "the landed file is rolled back")
}
