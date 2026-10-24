// file: internal/merge/plan_virtual_retired_test.go
// version: 1.0.1
// guid: 5d0b8e63-91a4-4c27-b6f3-7e2a1c9d4058
// last-edited: 2026-10-24

package merge

import (
	"testing"
	"time"

	ulid "github.com/oklog/ulid/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// retireRowOnto creates a row for path under a fresh shell book and retires it
// into holderID, so the row is keyed under the holder, flagged Retired, and the
// path index names it. Returns the retired row's id.
func retireRowOnto(t *testing.T, store database.Store, holderID, path string) string {
	t.Helper()
	ps, ok := store.(*database.PebbleStore)
	require.True(t, ok)
	ps.WaitForWarmup()
	shell, err := ps.CreateBook(&database.Book{ID: ulid.Make().String(), Title: "Retired shell", FilePath: "/tmp/pvr-shell-" + ulid.Make().String()})
	require.NoError(t, err)
	now := time.Now()
	v, err := ps.CreateBookVersion(&database.BookVersion{
		BookID: holderID, Status: database.BookVersionStatusRetired, Format: "mp3", Source: "retired_book",
		RetiredFromBookID: shell.ID, RetireOpID: "op-pvr", RetiredAt: &now,
	})
	require.NoError(t, err)
	row := &database.BookFile{ID: ulid.Make().String(), BookID: shell.ID, FilePath: path, Format: "mp3"}
	require.NoError(t, ps.CreateBookFile(row))
	require.NoError(t, ps.RetireBookFiles([]database.BookFileMove{{FileIDs: []string{row.ID}, SourceBookID: shell.ID}}, holderID, v.ID))
	hidden, err := ps.GetBookFileByPath(path)
	require.NoError(t, err)
	require.Nil(t, hidden, "the single-owner getter must not see a retired row")
	return row.ID
}

// A combine must not put a second row on a file a retired version holds: not a
// created row (the book's own FilePath is that file) and not a move of the
// retired row onto the target. planVirtualFile asked GetBookFileByPath, which
// cannot see a retired row, so it planned a create.
func TestPlanVirtualFile_FileHeldByRetiredVersionPlansNothing(t *testing.T) {
	for _, tc := range []struct {
		name         string
		holderIsSelf bool
	}{
		{"held-by-another-book", false},
		{"held-by-the-target-book-itself", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := setupTestStore(t)
			ms := NewService(store)
			other, err := store.CreateBook(&database.Book{ID: ulid.Make().String(), Title: "Other", FilePath: "/tmp/pvr-other.mp3"})
			require.NoError(t, err)
			target, err := store.CreateBook(&database.Book{ID: ulid.Make().String(), Title: "Target", FilePath: "/tmp/pvr-held.mp3"})
			require.NoError(t, err)
			holder := other.ID
			if tc.holderIsSelf {
				holder = target.ID
			}
			retiredID := retireRowOnto(t, store, holder, target.FilePath)

			plan := mustPlanVirtualFile(t, ms, target, target.ID)
			assert.Nil(t, plan.create, "no row may be created on a retired version's file")
			assert.Nil(t, plan.existing, "the retired row must not be moved")
			assert.False(t, plan.attached)
			assert.Empty(t, plan.fm.FileID)
			assert.Zero(t, ms.applyVirtualFile(plan, target.ID))

			at, err := store.(*database.PebbleStore).BookFilesAtPath(target.FilePath)
			require.NoError(t, err)
			require.Len(t, at, 1)
			assert.Equal(t, retiredID, at[0].ID)
			assert.True(t, at[0].Retired)
		})
	}
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

// indexDownStore reports the complete multi-row lookup as unavailable, the way
// a store does while memdb is warming up. Everything else is the real store, so
// GetBookFileByPath still answers (and still cannot see a retired row). It
// opts into Unwrap, as the production decorators do, so the retired-version
// probe resolves through it.
type indexDownStore struct{ database.Store }

func (s indexDownStore) Unwrap() database.Store { return s.Store }

func (indexDownStore) BookFilesAtPath(string) ([]database.BookFile, error) {
	return nil, database.ErrBookFilesAtPathUnavailable
}

// opaqueIndexDownStore is indexDownStore without Unwrap: a decorator whose
// capabilities cannot be resolved, so nothing can prove that no retired version
// exists.
type opaqueIndexDownStore struct{ database.Store }

func (opaqueIndexDownStore) BookFilesAtPath(string) ([]database.BookFile, error) {
	return nil, database.ErrBookFilesAtPathUnavailable
}

// With retired versions in the store and the complete lookup unavailable, a
// retired-held path must not read as free (the single-owner index hides retired
// rows, so a fallback to it would plan a second row on the file), and it must
// not read as "nothing to attach" either: that is a silent drop. The plan is an
// error, so the combine stops.
func TestPlanVirtualFile_CompleteLookupUnavailableWithRetiredVersionsErrors(t *testing.T) {
	store := setupTestStore(t)
	other, err := store.CreateBook(&database.Book{ID: ulid.Make().String(), Title: "Other", FilePath: "/tmp/pvr-down-other.mp3"})
	require.NoError(t, err)
	target, err := store.CreateBook(&database.Book{ID: ulid.Make().String(), Title: "Target", FilePath: "/tmp/pvr-down.mp3"})
	require.NoError(t, err)
	retiredID := retireRowOnto(t, store, other.ID, target.FilePath)

	ms := NewService(indexDownStore{Store: store})
	plan, err := ms.planVirtualFile(target, target.ID)
	require.Error(t, err, "a lookup that cannot be answered is an error, never an empty plan")
	assert.ErrorIs(t, err, database.ErrOwnershipLookupIncomplete)
	assert.Nil(t, plan.create, "an unanswerable lookup must not read as a free path")
	assert.Nil(t, plan.existing)
	assert.False(t, plan.attached)

	at, err := store.(*database.PebbleStore).BookFilesAtPath(target.FilePath)
	require.NoError(t, err)
	require.Len(t, at, 1)
	assert.Equal(t, retiredID, at[0].ID)
}

// A store that cannot say whether retired versions exist is treated like one
// that has them: the complete lookup is required.
func TestPlanVirtualFile_CompleteLookupUnavailableOnOpaqueStoreErrors(t *testing.T) {
	store := setupTestStore(t)
	target, err := store.CreateBook(&database.Book{ID: ulid.Make().String(), Title: "Target", FilePath: "/tmp/pvr-opaque.mp3"})
	require.NoError(t, err)

	ms := NewService(opaqueIndexDownStore{Store: store})
	_, err = ms.planVirtualFile(target, target.ID)
	require.Error(t, err)
	assert.ErrorIs(t, err, database.ErrOwnershipLookupIncomplete)
}

// While no retired version exists the complete lookup is not needed: no retired
// row can hold the path, so the single-owner lookup is the whole answer and the
// plan is the one made before retired rows existed, warmup or not.
func TestPlanVirtualFile_CompleteLookupUnavailableWithNoRetiredVersionsPlansNormally(t *testing.T) {
	store := setupTestStore(t)
	ms := NewService(indexDownStore{Store: store})

	t.Run("new-row", func(t *testing.T) {
		b, err := store.CreateBook(&database.Book{ID: ulid.Make().String(), Title: "Rowless", FilePath: "/tmp/pvr-warm-new.mp3"})
		require.NoError(t, err)
		plan, err := ms.planVirtualFile(b, b.ID)
		require.NoError(t, err)
		require.NotNil(t, plan.create)
		assert.True(t, plan.attached)
		assert.Equal(t, b.FilePath, plan.create.FilePath)
	})
	t.Run("move-existing", func(t *testing.T) {
		owner, err := store.CreateBook(&database.Book{ID: ulid.Make().String(), Title: "Stray owner", FilePath: "/tmp/pvr-warm-stray-owner.mp3"})
		require.NoError(t, err)
		target, err := store.CreateBook(&database.Book{ID: ulid.Make().String(), Title: "Target", FilePath: "/tmp/pvr-warm-move.mp3"})
		require.NoError(t, err)
		stray := &database.BookFile{ID: ulid.Make().String(), BookID: owner.ID, FilePath: target.FilePath, Format: "mp3"}
		require.NoError(t, store.CreateBookFile(stray))
		plan, err := ms.planVirtualFile(target, target.ID)
		require.NoError(t, err)
		require.NotNil(t, plan.existing)
		assert.Equal(t, stray.ID, plan.existing.ID)
	})
}

// seedWarmupCombine builds a survivor and an absorbed book that are both
// rowless single-file (virtual) books.
func seedWarmupCombine(t *testing.T, store database.Store) (survivor, absorbed *database.Book) {
	t.Helper()
	survivor, err := store.CreateBook(&database.Book{ID: ulid.Make().String(), Title: "Survivor", Format: "mp3", FilePath: "/tmp/pvr-combine-s.mp3"})
	require.NoError(t, err)
	absorbed, err = store.CreateBook(&database.Book{ID: ulid.Make().String(), Title: "Absorbed", Format: "mp3", FilePath: "/tmp/pvr-combine-a.mp3"})
	require.NoError(t, err)
	return survivor, absorbed
}

// The reviewer's blocker: a combine during memdb warmup (the complete lookup
// unavailable) used to plan nothing for a rowless absorbed book, soft-delete it
// and report success, leaving its file with no row under the survivor. With no
// retired version in the store the single-owner lookup is enough, so the
// combine attaches the file.
func TestCombineBooks_WarmupWithNoRetiredVersionsStillAttachesTheFile(t *testing.T) {
	store := setupTestStore(t)
	survivor, absorbed := seedWarmupCombine(t, store)

	ms := NewService(indexDownStore{Store: store})
	res, err := ms.CombineBooks([]string{survivor.ID, absorbed.ID}, survivor.ID, nil)
	require.NoError(t, err)
	assert.Equal(t, 1, res.BooksDeleted)

	files, err := store.GetBookFiles(survivor.ID)
	require.NoError(t, err)
	paths := map[string]bool{}
	for _, f := range files {
		paths[f.FilePath] = true
	}
	assert.True(t, paths[absorbed.FilePath], "the absorbed book's file must have a row under the survivor, got %v", paths)
	assert.True(t, paths[survivor.FilePath], "the survivor's own file must have a row, got %v", paths)
	assert.Equal(t, 2, res.FilesMoved)
}

// With a retired version in the store the complete lookup is required, and when
// it cannot be answered the combine stops with its journal pending instead of
// soft-deleting the absorbed book and reporting success.
func TestCombineBooks_WarmupWithRetiredVersionsStopsInsteadOfDroppingTheFile(t *testing.T) {
	store := setupTestStore(t)
	survivor, absorbed := seedWarmupCombine(t, store)
	bystander, err := store.CreateBook(&database.Book{ID: ulid.Make().String(), Title: "Bystander", FilePath: "/tmp/pvr-combine-by.mp3"})
	require.NoError(t, err)
	retireRowOnto(t, store, bystander.ID, "/tmp/pvr-combine-elsewhere.mp3")

	ms := NewService(indexDownStore{Store: store})
	_, err = ms.CombineBooks([]string{survivor.ID, absorbed.ID}, survivor.ID, nil)
	require.Error(t, err, "a combine whose file lookup cannot be answered must not report success")
	assert.ErrorIs(t, err, database.ErrOwnershipLookupIncomplete)

	assert.False(t, viewBook(t, store, absorbed.ID).SoftDel, "the absorbed book must not be soft-deleted")
	files, err := store.GetBookFiles(survivor.ID)
	require.NoError(t, err)
	for _, f := range files {
		assert.NotEqual(t, absorbed.FilePath, f.FilePath)
	}
}
