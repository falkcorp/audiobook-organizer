// file: internal/merge/plan_virtual_lookup_error_test.go
// version: 1.0.0
// guid: 34eec890-9285-4b3a-96ac-ea435703e5ee
// last-edited: 2026-10-26

package merge

import (
	"errors"
	"testing"

	"github.com/oklog/ulid/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// errPathLookup is what indexErrStore answers for the one path it fails: an I/O
// failure, not the ErrBookFilesAtPathUnavailable sentinel the warmup tests use.
var errPathLookup = errors.New("injected book_file path lookup failure")

// indexErrStore fails the complete path lookup with a GENERIC error for one
// path, and answers every other path from the real store. It opts into Unwrap,
// as the production decorators do, so the retired-version probe resolves
// through it: with no retired version in the store the rows come from
// BookFilesAtPath, which is where the error surfaces.
type indexErrStore struct {
	database.Store
	failPath string
}

func (s indexErrStore) Unwrap() database.Store { return s.Store }

func (s indexErrStore) BookFilesAtPath(path string) ([]database.BookFile, error) {
	if path == s.failPath {
		return nil, errPathLookup
	}
	return s.Store.BookFilesAtPath(path)
}

// requireStoppedWithNothingWritten asserts a combine stopped on errPathLookup
// before it wrote anything: the lookup error itself (not a CombineIncompleteError,
// which asks the user to revert a combine that wrote nothing), no journal, and
// neither book touched.
func requireStoppedWithNothingWritten(t *testing.T, store database.Store, ms *Service, err error, survivor, absorbed *database.Book) {
	t.Helper()
	require.ErrorIs(t, err, errPathLookup)
	var incomplete *CombineIncompleteError
	assert.False(t, errors.As(err, &incomplete), "a stop before the journal must not ask the user to revert: %v", err)

	journals, jerr := ms.ListCombineJournals(0)
	require.NoError(t, jerr)
	assert.Empty(t, journals, "no combine journal may be left behind")

	assert.False(t, viewBook(t, store, survivor.ID).SoftDel)
	assert.False(t, viewBook(t, store, absorbed.ID).SoftDel, "the absorbed book must not be soft-deleted by a combine that stopped before it")
	files, ferr := store.GetBookFiles(survivor.ID)
	require.NoError(t, ferr)
	for _, f := range files {
		assert.NotEqual(t, absorbed.FilePath, f.FilePath, "no row may be minted for a path that could not be checked")
	}
}

// A failed lookup must not read as "no row at the path": that plans a NEW row
// for a path that may already have one.
func TestPlanVirtualFile_PathLookupErrorPlansNothing(t *testing.T) {
	store := setupTestStore(t)
	b, err := store.CreateBook(&database.Book{ID: ulid.Make().String(), Title: "Knightfall", Format: "mp3", FilePath: "/tmp/pvle-lookup.mp3"})
	require.NoError(t, err)

	ms := NewService(indexErrStore{Store: store, failPath: b.FilePath})
	plan, err := ms.planVirtualFile(b, b.ID, nil)
	require.ErrorIs(t, err, errPathLookup)
	assert.Nil(t, plan.create, "no new row may be planned from a lookup that failed")
	assert.Nil(t, plan.existing)
	assert.False(t, plan.attached)

	_, err = ms.planOwnFile(b, nil)
	require.ErrorIs(t, err, errPathLookup)
}

// A combine whose survivor path cannot be checked stops before it journals or
// writes a row for it, and says why. The survivor is rowless, so planning ITS
// own file is the lookup that fails.
func TestCombineBooks_SurvivorPathLookupErrorStopsTheCombine(t *testing.T) {
	store := setupTestStore(t)
	survivor, absorbed := seedWarmupCombine(t, store)

	ms := NewService(indexErrStore{Store: store, failPath: survivor.FilePath})
	_, err := ms.CombineBooks([]string{survivor.ID, absorbed.ID}, survivor.ID, nil)
	requireStoppedWithNothingWritten(t, store, ms, err, survivor, absorbed)
}

// The same for an absorbed single-file book whose path cannot be checked. The
// survivor already has a real row, so only the absorbed book's lookup can fail.
func TestCombineBooks_AbsorbedPathLookupErrorStopsTheCombine(t *testing.T) {
	store := setupTestStore(t)
	survivor, absorbed := seedWarmupCombine(t, store)
	require.NoError(t, store.CreateBookFile(&database.BookFile{
		ID: ulid.Make().String(), BookID: survivor.ID, FilePath: survivor.FilePath, Format: "mp3",
	}))

	ms := NewService(indexErrStore{Store: store, failPath: absorbed.FilePath})
	_, err := ms.CombineBooks([]string{survivor.ID, absorbed.ID}, survivor.ID, nil)
	requireStoppedWithNothingWritten(t, store, ms, err, survivor, absorbed)
	assert.Contains(t, err.Error(), absorbed.ID, "the stop must name the absorbed book whose path could not be checked")
}
