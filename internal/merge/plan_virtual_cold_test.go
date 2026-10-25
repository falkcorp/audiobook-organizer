// file: internal/merge/plan_virtual_cold_test.go
// version: 1.0.1
// guid: 6f1b7c2e-3d54-4a8e-9b0c-2e7d5a4f1c83
// last-edited: 2026-10-25

package merge

import (
	"errors"
	"testing"

	ulid "github.com/oklog/ulid/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// goCold makes the store behave as it does while memdb is warming up after a
// restart: memdb is not serving, so the complete path lookup (BookFilesAtPath) is
// unavailable and the path lookup falls back to the single-owner index, which
// names ONE row per path. Real Pebble, not a stub, so the planner meets exactly
// what production meets. Call it after seeding.
func goCold(t *testing.T, store database.Store) {
	t.Helper()
	ps, ok := store.(*database.PebbleStore)
	require.True(t, ok, "goCold needs the real Pebble store, got %T", store)
	ps.UseMemDB = false
	_, err := database.BookFileRowsAtPathStrict(store, "/tmp/pvc-probe.mp3")
	require.ErrorIs(t, err, database.ErrOwnershipLookupIncomplete, "fixture: the complete lookup must be unavailable")
}

// warmUp undoes goCold: memdb serves again, as it does a couple of minutes after
// a restart.
func warmUp(t *testing.T, store database.Store) {
	t.Helper()
	ps, ok := store.(*database.PebbleStore)
	require.True(t, ok, "warmUp needs the real Pebble store, got %T", store)
	ps.UseMemDB = true
	_, err := database.BookFileRowsAtPathStrict(store, "/tmp/pvc-probe.mp3")
	require.NoError(t, err, "fixture: the complete lookup must be available again")
}

// requireRefusedBeforeJournaling asserts what a combine that refused for want of
// the complete holder list leaves behind: an error that says "retry later" and is
// not a CombineIncompleteError (nothing was written, so there is nothing to
// revert), no journal, no retired book and no file row moved or created.
func requireRefusedBeforeJournaling(t *testing.T, ms *Service, store database.Store, err error, absorbedID, survivorID string) {
	t.Helper()
	require.ErrorIs(t, err, database.ErrOwnershipLookupIncomplete, "the refusal must say the lookup is incomplete, so the caller retries")
	var incomplete *CombineIncompleteError
	assert.False(t, errors.As(err, &incomplete), "a refusal before the journal must not ask the user to revert: %v", err)
	journals, jerr := ms.ListCombineJournals(0)
	require.NoError(t, jerr)
	assert.Empty(t, journals, "a refusal must leave no combine journal behind")
	assert.False(t, viewBook(t, store, absorbedID).SoftDel, "the absorbed book must not be retired")
	assert.Empty(t, viewFiles(t, store, survivorID), "nothing was attached to the survivor")
}

// The single-owner fallback names one row of a path with two live holders, and a
// rowless survivor's own file is that path. The planner used to take that row off
// its book for the survivor, which left a live book without its file and, worse,
// chose the victim by key order. While the complete lookup is unavailable the
// combine refuses before it writes anything (a retry a minute later has the list),
// and with the list the path held by two live books is reported, not guessed.
func TestCombineBooks_ColdLookupSurvivorsHeldPathRefusesBeforeJournaling(t *testing.T) {
	store := setupTestStore(t)
	path := "/tmp/pvc-two-live.mp3"
	h1, row1 := seedHolder(t, store, "Holder one", path)
	h2, row2 := seedHolder(t, store, "Holder two", path)
	survivor, err := store.CreateBook(&database.Book{ID: ulid.Make().String(), Title: "Survivor", Format: "mp3", FilePath: path})
	require.NoError(t, err)
	absorbed, err := store.CreateBook(&database.Book{ID: ulid.Make().String(), Title: "Absorbed", Format: "mp3", FilePath: "/tmp/pvc-two-live-abs.mp3"})
	require.NoError(t, err)
	goCold(t, store)

	ms := NewService(store)
	_, err = ms.CombineBooks([]string{survivor.ID, absorbed.ID}, survivor.ID, nil)
	requireRefusedBeforeJournaling(t, ms, store, err, absorbed.ID, survivor.ID)
	assert.Contains(t, viewFiles(t, store, h1.ID), row1, "the first holder keeps its row")
	assert.Contains(t, viewFiles(t, store, h2.ID), row2, "the second holder keeps its row")

	warmUp(t, store)
	res, err := ms.CombineBooks([]string{survivor.ID, absorbed.ID}, survivor.ID, nil)
	require.NoError(t, err, "the same combine goes through once the list is available")
	require.Len(t, res.Warnings, 1)
	assert.Contains(t, res.Warnings[0], "path held by 2 live book(s)")
	assert.Contains(t, viewFiles(t, store, h1.ID), row1)
	assert.Contains(t, viewFiles(t, store, h2.ID), row2)
}

// The ordinary #1549 case during warmup: a rowless absorbed book's file is held by
// one outside book. The combine used to warn and go on, retiring the absorbed book
// with its FilePath cleared and no row on the survivor, a lasting result of a
// transient condition. It now refuses before anything is written, and the same
// combine a minute later moves the holder's row onto the survivor with no warning.
func TestCombineBooks_ColdLookupAbsorbedPathHeldBySingleBookRefusesBeforeJournaling(t *testing.T) {
	store := setupTestStore(t)
	path := "/tmp/pvc-absorbed-single.mp3"
	holder, row := seedHolder(t, store, "Holder", path)
	survivor, err := store.CreateBook(&database.Book{ID: ulid.Make().String(), Title: "Survivor", Format: "mp3", FilePath: "/tmp/pvc-absorbed-single-s.mp3"})
	require.NoError(t, err)
	absorbed, err := store.CreateBook(&database.Book{ID: ulid.Make().String(), Title: "Absorbed", Format: "mp3", FilePath: path})
	require.NoError(t, err)
	goCold(t, store)

	ms := NewService(store)
	_, err = ms.CombineBooks([]string{survivor.ID, absorbed.ID}, survivor.ID, nil)
	// The survivor's own file is free, so its row would be created; the refusal
	// must still leave no trace of it.
	require.ErrorIs(t, err, database.ErrOwnershipLookupIncomplete)
	var incomplete *CombineIncompleteError
	assert.False(t, errors.As(err, &incomplete), "nothing was written, so there is nothing to revert: %v", err)
	journals, jerr := ms.ListCombineJournals(0)
	require.NoError(t, jerr)
	assert.Empty(t, journals)
	assert.False(t, viewBook(t, store, absorbed.ID).SoftDel)
	assert.Equal(t, path, viewBook(t, store, absorbed.ID).FilePath, "the absorbed book keeps its FilePath")
	assert.Empty(t, viewFiles(t, store, survivor.ID), "the survivor's own row was not created either")
	assert.Contains(t, viewFiles(t, store, holder.ID), row, "the holder keeps its row")

	warmUp(t, store)
	res, err := ms.CombineBooks([]string{survivor.ID, absorbed.ID}, survivor.ID, nil)
	require.NoError(t, err)
	assert.Empty(t, res.Warnings, "with the complete list the single holder's row is taken (#1549) and nothing is left to warn about")
	moved, ok := viewFiles(t, store, survivor.ID)[row]
	require.True(t, ok, "the holder's row is now on the survivor")
	assert.Equal(t, path, moved.Path)
}

// planVirtualFile on its own, cold: a row held by another live book is not taken
// from the single-owner answer. Before the journal exists that is an error (the
// combine refuses); the plan made after the journal turns it into a warning.
func TestPlanVirtualFile_ColdLookupDoesNotTakeAnotherBooksRow(t *testing.T) {
	store := setupTestStore(t)
	path := "/tmp/pvc-plan.mp3"
	_, row := seedHolder(t, store, "Holder", path)
	target, err := store.CreateBook(&database.Book{ID: ulid.Make().String(), Title: "Target", FilePath: path})
	require.NoError(t, err)

	hot, err := NewService(store).planVirtualFile(target, target.ID, nil)
	require.NoError(t, err)
	require.NotNil(t, hot.existing, "fixture: with the complete lookup the single holder's row is taken (#1549)")
	assert.Equal(t, row, hot.existing.ID)

	goCold(t, store)
	before, err := NewService(store).planVirtualFile(target, target.ID, nil)
	require.ErrorIs(t, err, database.ErrOwnershipLookupIncomplete, "before the journal the unanswerable pick refuses")
	_, isUnconfirmed := errors.AsType[*rowTakeUnconfirmedError](err)
	assert.True(t, isUnconfirmed)
	assert.Nil(t, before.existing)
	assert.Nil(t, before.create)

	after, err := NewService(store).planVirtualFileAfterJournal(target, target.ID, nil)
	require.NoError(t, err, "after the journal the combine cannot refuse; it plans nothing and says why")
	assert.Nil(t, after.existing)
	assert.Nil(t, after.create, "and no second row on a file somebody holds")
	assert.False(t, after.attached)
	assert.Contains(t, after.warning, "not available yet")
	assert.Contains(t, after.warning, "not reattached")
}

// The after-journal plan converts only the one transient answer. A holder that
// cannot be read is still an error there, as it is before the journal.
func TestPlanVirtualFileAfterJournal_OtherErrorsStayErrors(t *testing.T) {
	store := setupTestStore(t)
	path := "/tmp/pvc-after-journal-err.mp3"
	holder, _ := seedHolder(t, store, "Holder", path)
	target, err := store.CreateBook(&database.Book{ID: ulid.Make().String(), Title: "Target", FilePath: path})
	require.NoError(t, err)

	boom := errors.New("injected holder read error")
	_, err = NewService(bookReadErrStore{Store: store, failID: holder.ID, err: boom}).planVirtualFileAfterJournal(target, target.ID, nil)
	require.ErrorIs(t, err, boom)
}

// A path nobody holds still gets its row cold: the fallback is trusted for the
// answer "no row", which keeps a combine of rowless books working through the
// warmup after a restart.
func TestPlanVirtualFile_ColdLookupStillCreatesARowForAFreePath(t *testing.T) {
	store := setupTestStore(t)
	target, err := store.CreateBook(&database.Book{ID: ulid.Make().String(), Title: "Target", FilePath: "/tmp/pvc-free.mp3"})
	require.NoError(t, err)
	goCold(t, store)

	plan, err := NewService(store).planVirtualFile(target, target.ID, nil)
	require.NoError(t, err)
	require.NotNil(t, plan.create)
	assert.Empty(t, plan.warning)
}

// The rowless survivor's FilePath is the path of a row an ABSORBED book owns, and a
// bystander created later also holds a row there (so the single-owner index names
// the bystander, not the absorbed book). Which row the bulk move brings is known
// from the absorbed book's own rows, never from a lookup: the combine gives the
// survivor exactly the absorbed book's row, leaves the bystander its row, and
// warns about nothing. Decided from the lookup, the cold store named the
// bystander's row and the combine took it or refused.
func TestCombineBooks_ColdLookupRowlessPrimaryAtAnAbsorbedRowPathWithABystander(t *testing.T) {
	store := setupTestStore(t)
	path := "/tmp/pvc-arriving.mp3"
	survivor, err := store.CreateBook(&database.Book{ID: ulid.Make().String(), Title: "Legacy record", Format: "mp3", FilePath: path})
	require.NoError(t, err)
	absorbed, absRow := seedHolder(t, store, "Absorbed with the row", path)
	bystander, byRow := seedHolder(t, store, "Bystander", path)
	goCold(t, store)
	// The fixture only bites if the single-owner key names the bystander's row (the
	// last row written at a path wins), because that is the row a lookup-driven
	// plan would take. Pin it, so a change in index order cannot turn this into a
	// test that passes on the old code.
	named, err := store.GetBookFileByPath(path)
	require.NoError(t, err)
	require.NotNil(t, named)
	require.Equal(t, byRow, named.ID, "fixture: the single-owner key must name the bystander's row")

	ms := NewService(store)
	res, err := ms.CombineBooks([]string{survivor.ID, absorbed.ID}, survivor.ID, nil)
	var incomplete *CombineIncompleteError
	require.False(t, errors.As(err, &incomplete), "the combine must not stop after its journal: %v", err)
	require.NoError(t, err)
	assert.Empty(t, res.Warnings, "the survivor's file arrives with the bulk move; nothing is left unattached")

	files, err := store.GetBookFiles(survivor.ID)
	require.NoError(t, err)
	require.Len(t, files, 1, "the survivor owns exactly the absorbed book's row")
	assert.Equal(t, absRow, files[0].ID)
	assert.Contains(t, viewFiles(t, store, bystander.ID), byRow, "the bystander keeps its row")
	j, err := ms.GetCombineJournal(res.JournalID)
	require.NoError(t, err)
	assert.Empty(t, j.SurvivorFiles)
	assert.Empty(t, j.Warnings)
}

// bookReadErrStore fails GetBookByID for one book and answers the rest from the
// real store. It opts into Unwrap like the production decorators.
type bookReadErrStore struct {
	database.Store
	failID string
	err    error
}

func (s bookReadErrStore) Unwrap() database.Store { return s.Store }

func (s bookReadErrStore) GetBookByID(id string) (*database.Book, error) {
	if id == s.failID {
		return nil, s.err
	}
	return s.Store.GetBookByID(id)
}

// The holder of an absorbed rowless book's path cannot be read. The pick runs in
// the combine's pre-journal check, so the combine refuses before anything is
// written: no journal, no soft-delete. It used to fail inside the attach, after
// the journal and the bulk move, as a CombineIncompleteError telling the user to
// revert a combine that had already changed things.
func TestCombineBooks_UnreadableHolderOfAnAbsorbedPathRefusesBeforeJournaling(t *testing.T) {
	store := setupTestStore(t)
	path := "/tmp/pvc-unreadable-holder.mp3"
	holder, _ := seedHolder(t, store, "Holder", path)
	survivor, err := store.CreateBook(&database.Book{ID: ulid.Make().String(), Title: "Survivor", Format: "mp3", FilePath: "/tmp/pvc-unreadable-s.mp3"})
	require.NoError(t, err)
	absorbed, err := store.CreateBook(&database.Book{ID: ulid.Make().String(), Title: "Absorbed", Format: "mp3", FilePath: path})
	require.NoError(t, err)

	boom := errors.New("injected holder read error")
	ms := NewService(bookReadErrStore{Store: store, failID: holder.ID, err: boom})
	_, err = ms.CombineBooks([]string{survivor.ID, absorbed.ID}, survivor.ID, nil)
	require.ErrorIs(t, err, boom)
	var incomplete *CombineIncompleteError
	assert.False(t, errors.As(err, &incomplete), "a refusal before the journal must not ask the user to revert: %v", err)

	journals, jerr := ms.ListCombineJournals(0)
	require.NoError(t, jerr)
	assert.Empty(t, journals, "a refusal must leave no combine journal behind")
	assert.False(t, viewBook(t, store, absorbed.ID).SoftDel, "the absorbed book must not be soft-deleted")
	assert.Empty(t, viewFiles(t, store, survivor.ID), "nothing was attached to the survivor")
}
