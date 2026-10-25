// file: internal/merge/plan_virtual_retired_test.go
// version: 1.0.5
// guid: 5d0b8e63-91a4-4c27-b6f3-7e2a1c9d4058
// last-edited: 2026-10-25

package merge

import (
	"errors"
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
	p, err := ms.planVirtualFile(b, targetBookID, nil)
	require.NoError(t, err)
	return p
}

// mustPlanOwnFile plans and requires that the path lookup succeeded.
func mustPlanOwnFile(t *testing.T, ms *Service, b *database.Book) virtualFilePlan {
	t.Helper()
	p, err := ms.planOwnFile(b, nil)
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
	plan, err := ms.planVirtualFile(target, target.ID, nil)
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
	_, err = ms.planVirtualFile(target, target.ID, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, database.ErrOwnershipLookupIncomplete)
}

// While no retired version exists the complete lookup is not needed to tell that
// a path is free: no retired row can hold the path, so a single-owner lookup that
// finds nothing is a whole answer and the row is created, warmup or not. It does
// not show who ELSE holds a path it names a row of, so taking that row off its
// book needs the complete lookup, and without it nothing is taken (a warning
// instead): see TestPlanVirtualFile_ColdLookupDoesNotTakeAnotherBooksRow.
func TestPlanVirtualFile_CompleteLookupUnavailableWithNoRetiredVersionsCreatesButTakesNothing(t *testing.T) {
	store := setupTestStore(t)
	ms := NewService(indexDownStore{Store: store})

	t.Run("new-row", func(t *testing.T) {
		b, err := store.CreateBook(&database.Book{ID: ulid.Make().String(), Title: "Rowless", FilePath: "/tmp/pvr-warm-new.mp3"})
		require.NoError(t, err)
		plan, err := ms.planVirtualFile(b, b.ID, nil)
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
		plan, err := ms.planVirtualFile(target, target.ID, nil)
		require.NoError(t, err)
		assert.Nil(t, plan.existing, "another book's row is not taken on the single-owner answer alone")
		assert.Nil(t, plan.create, "and no second row is put on its file")
		assert.Contains(t, plan.warning, "not available yet")
	})
}

// readErrStore fails the reads it is told to, and answers the rest from the
// real store. It opts into Unwrap like the production decorators.
type readErrStore struct {
	database.Store
	getBookFilesErr      error
	getBookFileByPathErr error
}

func (s readErrStore) Unwrap() database.Store { return s.Store }

func (s readErrStore) GetBookFiles(id string) ([]database.BookFile, error) {
	if s.getBookFilesErr != nil {
		return nil, s.getBookFilesErr
	}
	return s.Store.GetBookFiles(id)
}

func (s readErrStore) GetBookFileByPath(p string) (*database.BookFile, error) {
	if s.getBookFileByPathErr != nil {
		return nil, s.getBookFileByPathErr
	}
	return s.Store.GetBookFileByPath(p)
}

// A failed GetBookFiles is not "this book has no rows": planOwnFile used to
// discard the error and plan a create for a book that already had rows, putting
// a second live row on its file. It is an error now, and nothing is planned.
func TestPlanOwnFile_RowsReadErrorErrorsInsteadOfPlanningACreate(t *testing.T) {
	store := setupTestStore(t)
	b, err := store.CreateBook(&database.Book{ID: ulid.Make().String(), Title: "Has rows", FilePath: "/tmp/pvr-rowserr.mp3"})
	require.NoError(t, err)
	// The row is at another path than b.FilePath on purpose: the buggy planner
	// looked for a row at b.FilePath, found none, and planned a create, so a row
	// AT b.FilePath would have been found (and the test pass on the old code).
	require.NoError(t, store.CreateBookFile(&database.BookFile{ID: ulid.Make().String(), BookID: b.ID, FilePath: "/tmp/pvr-rowserr-row.mp3", Format: "mp3"}))

	boom := errors.New("injected pebble read error")
	ms := NewService(readErrStore{Store: store, getBookFilesErr: boom})
	plan, err := ms.planOwnFile(b, nil)
	require.ErrorIs(t, err, boom)
	assert.Nil(t, plan.create, "an unreadable row list must not plan a second live row")
	assert.Nil(t, plan.existing)
	assert.False(t, plan.attached)
}

// planVirtualFile finds the live row to reattach in the rows the lookup already
// returned, so a failing GetBookFileByPath cannot make a held path look free (the
// old second read discarded its error and planned a create over the live row).
func TestPlanVirtualFile_ReattachesTheLiveRowFromTheLookupNotASecondRead(t *testing.T) {
	store := setupTestStore(t)
	owner, err := store.CreateBook(&database.Book{ID: ulid.Make().String(), Title: "Stray owner", FilePath: "/tmp/pvr-reread-owner.mp3"})
	require.NoError(t, err)
	target, err := store.CreateBook(&database.Book{ID: ulid.Make().String(), Title: "Target", FilePath: "/tmp/pvr-reread.mp3"})
	require.NoError(t, err)
	stray := &database.BookFile{ID: ulid.Make().String(), BookID: owner.ID, FilePath: target.FilePath, Format: "mp3"}
	require.NoError(t, store.CreateBookFile(stray))

	ms := NewService(readErrStore{Store: store, getBookFileByPathErr: errors.New("injected path read error")})
	plan, err := ms.planVirtualFile(target, target.ID, nil)
	require.NoError(t, err)
	assert.Nil(t, plan.create, "the path is held by a live row; no second row may be planned")
	require.NotNil(t, plan.existing)
	assert.Equal(t, stray.ID, plan.existing.ID)
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

// requireRefusedBeforeJournal asserts that a combine refused for want of a path
// lookup answer did so cleanly: the lookup error, not a CombineIncompleteError
// (which tells the user to revert a combine that wrote nothing), no journal of any
// status, and neither book touched.
func requireRefusedBeforeJournal(t *testing.T, store database.Store, ms *Service, err error, survivor, absorbed *database.Book) {
	t.Helper()
	require.Error(t, err, "a combine whose file lookup cannot be answered must not report success")
	assert.ErrorIs(t, err, database.ErrOwnershipLookupIncomplete)
	var incomplete *CombineIncompleteError
	assert.False(t, errors.As(err, &incomplete), "a refusal before the journal must not ask the user to revert: %v", err)

	journals, jerr := ms.ListCombineJournals(0)
	require.NoError(t, jerr)
	assert.Empty(t, journals, "a refusal must leave no combine journal behind, Pending or otherwise")

	assert.False(t, viewBook(t, store, absorbed.ID).SoftDel, "the absorbed book must not be soft-deleted")
	files, ferr := store.GetBookFiles(survivor.ID)
	require.NoError(t, ferr)
	for _, f := range files {
		assert.NotEqual(t, absorbed.FilePath, f.FilePath)
	}
}

// With a retired version in the store the complete lookup is required, and when
// it cannot be answered the combine refuses before it journals anything instead of
// soft-deleting the absorbed book and reporting success. Here the survivor is
// rowless, so planning ITS own file is what cannot be answered.
func TestCombineBooks_WarmupWithRetiredVersionsRefusesOnTheSurvivorLookupBeforeJournaling(t *testing.T) {
	store := setupTestStore(t)
	survivor, absorbed := seedWarmupCombine(t, store)
	bystander, err := store.CreateBook(&database.Book{ID: ulid.Make().String(), Title: "Bystander", FilePath: "/tmp/pvr-combine-by.mp3"})
	require.NoError(t, err)
	retireRowOnto(t, store, bystander.ID, "/tmp/pvr-combine-elsewhere.mp3")

	ms := NewService(indexDownStore{Store: store})
	_, err = ms.CombineBooks([]string{survivor.ID, absorbed.ID}, survivor.ID, nil)
	requireRefusedBeforeJournal(t, store, ms, err, survivor, absorbed)
}

// The same refusal when the survivor already has a real row, so its own plan needs
// no lookup and the ABSORBED book's path lookup is the one that cannot be answered.
// This is the absorbed-loop branch the test above never reaches (the survivor
// refused first), and it used to fire only after the journal and the undo pointer
// were written, leaving a Pending journal over an untouched library.
func TestCombineBooks_WarmupWithRetiredVersionsRefusesOnTheAbsorbedLookupBeforeJournaling(t *testing.T) {
	store := setupTestStore(t)
	survivor, absorbed := seedWarmupCombine(t, store)
	require.NoError(t, store.CreateBookFile(&database.BookFile{
		ID: ulid.Make().String(), BookID: survivor.ID, FilePath: survivor.FilePath, Format: "mp3",
	}))
	bystander, err := store.CreateBook(&database.Book{ID: ulid.Make().String(), Title: "Bystander", FilePath: "/tmp/pvr-combine-by2.mp3"})
	require.NoError(t, err)
	retireRowOnto(t, store, bystander.ID, "/tmp/pvr-combine-elsewhere2.mp3")

	ms := NewService(indexDownStore{Store: store})
	// The survivor really is the one with rows, so only the absorbed lookup can refuse.
	plan, perr := ms.planOwnFile(survivor, nil)
	require.NoError(t, perr)
	require.False(t, plan.attached)
	require.Nil(t, plan.create)

	_, err = ms.CombineBooks([]string{survivor.ID, absorbed.ID}, survivor.ID, nil)
	requireRefusedBeforeJournal(t, store, ms, err, survivor, absorbed)
	assert.Contains(t, err.Error(), absorbed.ID, "the refusal must name the absorbed book whose path could not be checked")
}
