// file: internal/merge/plan_virtual_file_lookup_error_test.go
// version: 1.0.0
// guid: 4e8b2d71-6a3c-4f95-b1d0-7c52e9a83f16
// last-edited: 2026-10-24

package merge

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// errPathLookup is what pathLookupFailStore returns for a path lookup.
var errPathLookup = errors.New("injected book_file path lookup failure")

// pathLookupFailStore is a real store whose include-retired path lookup fails
// for one path, and only for that path. It implements RetiredBookFileReader
// itself (embedding the real store's reader) so the lookup reaches this
// override instead of the real store behind the decorator chain.
type pathLookupFailStore struct {
	database.Store
	database.RetiredBookFileReader
	failPath string
}

func (s *pathLookupFailStore) Unwrap() database.Store { return s.Store }

func (s *pathLookupFailStore) GetBookFileByPathIncludingRetired(path string) (*database.BookFile, error) {
	if path == s.failPath {
		return nil, errPathLookup
	}
	return s.RetiredBookFileReader.GetBookFileByPathIncludingRetired(path)
}

func newPathLookupFailStore(t *testing.T, real database.Store, failPath string) *pathLookupFailStore {
	t.Helper()
	reader, ok := database.AsCapability[database.RetiredBookFileReader](real)
	require.True(t, ok, "the real store reads retired rows")
	return &pathLookupFailStore{Store: real, RetiredBookFileReader: reader, failPath: failPath}
}

// mustPlanVirtualFile plans and requires that the path lookup succeeded.
func mustPlanVirtualFile(t *testing.T, ms *Service, b *database.Book, targetBookID string) virtualFilePlan {
	t.Helper()
	p, err := ms.planVirtualFile(b, targetBookID)
	require.NoError(t, err)
	return p
}

// mustPlanOwnFile plans and requires that the path lookup succeeded.
func mustPlanOwnFile(t *testing.T, ms *Service, b *database.Book) virtualFilePlan {
	t.Helper()
	p, err := ms.planOwnFile(b)
	require.NoError(t, err)
	return p
}

// A failed lookup must not read as "no row at the path": that plans a NEW row
// for a path that may already have one.
func TestPlanVirtualFile_LookupErrorPlansNothing(t *testing.T) {
	real := setupTestStore(t)
	b := &database.Book{ID: "pvf-lookup-err", Title: "Lookup", Format: "mp3", FilePath: "/tmp/pvf-lookup-err.mp3"}
	_, err := real.CreateBook(b)
	require.NoError(t, err)

	ms := NewService(newPathLookupFailStore(t, real, b.FilePath))
	plan, err := ms.planVirtualFile(b, b.ID)
	require.ErrorIs(t, err, errPathLookup)
	require.Nil(t, plan.create, "no new row may be planned from a lookup that failed")
	require.Nil(t, plan.existing)
	require.False(t, plan.attached)

	_, err = ms.planOwnFile(b)
	require.ErrorIs(t, err, errPathLookup)
}

// A store that cannot answer the retired-aware lookup at all fails the same
// way, through the sentinel the callers already recognise.
func TestPlanVirtualFile_OpaqueStoreFailsClosed(t *testing.T) {
	real := setupTestStore(t)
	b := &database.Book{ID: "pvf-opaque", Title: "Opaque", Format: "mp3", FilePath: "/tmp/pvf-opaque.mp3"}
	_, err := real.CreateBook(b)
	require.NoError(t, err)

	ms := NewService(&opaqueStore{Store: real})
	_, err = ms.planVirtualFile(b, b.ID)
	require.ErrorIs(t, err, database.ErrRetiredReaderUnavailable)
}

// opaqueStore is an opaque decorator: no Unwrap, so no capability can
// be found through it.
type opaqueStore struct{ database.Store }

// A combine whose survivor path cannot be checked stops before writing a row
// for it, and says why.
func TestCombineBooks_SurvivorPathLookupErrorStopsTheCombine(t *testing.T) {
	real := setupTestStore(t)
	f := seedUndoFixture(t, real)
	all := []string{f.survivor, f.absA, f.absB}

	ms := NewService(newPathLookupFailStore(t, real, "/tmp/undo/s.mp3"))
	_, err := ms.CombineBooks(all, f.survivor, &CombineOverride{Title: "Combined", Author: "Jane Undo"})
	require.ErrorIs(t, err, errPathLookup)

	rows, err := real.GetBookFiles(f.survivor)
	require.NoError(t, err)
	require.Empty(t, rows, "no row may be minted for a path that could not be checked")
}

// The same for an absorbed single-file book whose path cannot be checked.
func TestCombineBooks_AbsorbedPathLookupErrorStopsTheCombine(t *testing.T) {
	real := setupTestStore(t)
	f := seedUndoFixture(t, real)
	all := []string{f.survivor, f.absA, f.absB}

	ms := NewService(newPathLookupFailStore(t, real, "/tmp/undo/b.mp3"))
	_, err := ms.CombineBooks(all, f.survivor, &CombineOverride{Title: "Combined", Author: "Jane Undo"})
	require.ErrorIs(t, err, errPathLookup)

	rows, err := real.GetBookFiles(f.absB)
	require.NoError(t, err)
	require.Empty(t, rows)
	b, err := real.GetBookByID(f.absB)
	require.NoError(t, err)
	require.False(t, b.MarkedForDeletion != nil && *b.MarkedForDeletion, "the absorbed book must not be soft-deleted by a combine that stopped before it")
}
