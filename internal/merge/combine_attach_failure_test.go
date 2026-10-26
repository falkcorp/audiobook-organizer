// file: internal/merge/combine_attach_failure_test.go
// version: 1.0.2
// guid: d540b8fe-78f6-4317-af34-77988b64e1b4
// last-edited: 2026-10-25

package merge

import (
	"strings"
	"testing"

	ulid "github.com/oklog/ulid/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// The write that attaches a rowless absorbed book's file fails (here: the move of
// a TRASHED holder's row, which the planner picked). The combine goes on, as it
// does for a path it chose to leave alone, and it now says so: a warning on the
// result and on the persisted journal, not a log line only. The journal must not
// keep the notice for the pick either, since the row never moved: it would claim
// "took the row ... from trashed book T" for a row T still holds.
func TestCombineBooks_FailedReattachOfATrashedHoldersRowIsReportedAndLeavesNoNotice(t *testing.T) {
	real := setupTestStore(t)
	path := "/tmp/caf-trashed-holder.mp3"
	trashed, trashedRow := seedHolder(t, real, "The Killing Joke", path)
	require.NoError(t, SoftDeleteBook(real, trashed.ID))
	survivor, err := real.CreateBook(&database.Book{ID: ulid.Make().String(), Title: "The Long Halloween", Format: "mp3", FilePath: "/tmp/caf-trashed-holder-s.mp3"})
	require.NoError(t, err)
	absorbed, err := real.CreateBook(&database.Book{ID: ulid.Make().String(), Title: "Hush", Format: "mp3", FilePath: path})
	require.NoError(t, err)

	ms := NewService(&b3FaultStore{Store: real, failMoveBookFilesToBookFor: map[string]bool{trashed.ID: true}})
	res, err := ms.CombineBooks([]string{survivor.ID, absorbed.ID}, survivor.ID, nil)
	require.NoError(t, err, "a failed attach does not stop the combine")

	require.Len(t, res.Warnings, 1, "the caller is told the file was not attached")
	assert.Contains(t, res.Warnings[0], path)
	assert.Contains(t, res.Warnings[0], "was not written")
	assert.Contains(t, res.Warnings[0], survivor.ID)

	j, err := ms.GetCombineJournal(res.JournalID)
	require.NoError(t, err)
	assert.Equal(t, res.Warnings, j.Warnings, "the journal carries the warning and no notice for a move that did not happen")
	for _, w := range j.Warnings {
		assert.NotContains(t, w, "took the row")
	}
	assert.Empty(t, j.Absorbed[0].Files, "and no record of a move that did not happen")

	assert.Contains(t, viewFiles(t, real, trashed.ID), trashedRow, "the trashed holder keeps its row")
	assert.NotContains(t, viewFiles(t, real, survivor.ID), trashedRow)
}

// A failed create is reported the same way. The survivor has a row of its own, so
// the failing create is the absorbed book's.
func TestCombineBooks_FailedCreateOfAnAbsorbedBooksRowIsReported(t *testing.T) {
	real := setupTestStore(t)
	survivor, _ := seedHolder(t, real, "Year One", "/tmp/caf-create-s.mp3")
	path := "/tmp/caf-create-abs.mp3"
	absorbed, err := real.CreateBook(&database.Book{ID: ulid.Make().String(), Title: "Hush", Format: "mp3", FilePath: path})
	require.NoError(t, err)

	ms := NewService(&b3FaultStore{Store: real, failCreateBookFile: true})
	res, err := ms.CombineBooks([]string{survivor.ID, absorbed.ID}, survivor.ID, nil)
	require.NoError(t, err)

	require.Len(t, res.Warnings, 1)
	assert.Contains(t, res.Warnings[0], path)
	assert.Contains(t, res.Warnings[0], "was not written")
	j, err := ms.GetCombineJournal(res.JournalID)
	require.NoError(t, err)
	assert.Equal(t, res.Warnings, j.Warnings)
	assert.Empty(t, j.Absorbed[0].Files, "no record of a row that was never created")
}

// The reattach of a trashed holder's row IS applied, and only its fsync fails (what
// the Pebble store reports as ErrBookFileDurabilityUnknown). The combine must treat
// the row as moved: keep the journal record, so undo gives the row back to the
// trashed holder, and say the durability is unknown instead of "nothing changed".
func TestCombineBooks_ReattachWhoseFsyncFailedIsAppliedAndStaysJournaled(t *testing.T) {
	real := setupTestStore(t)
	path := "/tmp/caf-fsync-move.mp3"
	trashed, trashedRow := seedHolder(t, real, "The Killing Joke", path)
	require.NoError(t, SoftDeleteBook(real, trashed.ID))
	survivor, err := real.CreateBook(&database.Book{ID: ulid.Make().String(), Title: "The Long Halloween", Format: "mp3", FilePath: "/tmp/caf-fsync-move-s.mp3"})
	require.NoError(t, err)
	absorbed, err := real.CreateBook(&database.Book{ID: ulid.Make().String(), Title: "Hush", Format: "mp3", FilePath: path})
	require.NoError(t, err)

	ms := NewService(&b3FaultStore{Store: real, unknownMoveBookFilesToBook: true})
	res, err := ms.CombineBooks([]string{survivor.ID, absorbed.ID}, survivor.ID, nil)
	require.NoError(t, err)

	assert.Contains(t, viewFiles(t, real, survivor.ID), trashedRow, "the row moved: the write was applied")
	require.NotEmpty(t, res.Warnings)
	var told bool
	for _, w := range res.Warnings {
		assert.NotContains(t, w, "nothing changed", "the row DID change")
		assert.NotContains(t, w, "has no row for it")
		told = told || strings.Contains(w, "durability unknown")
	}
	assert.True(t, told, "the caller is told the write is unconfirmed: %v", res.Warnings)

	j, err := ms.GetCombineJournal(res.JournalID)
	require.NoError(t, err)
	require.Len(t, j.Absorbed[0].Files, 1, "the journal keeps the record of the move")
	assert.Equal(t, trashedRow, j.Absorbed[0].Files[0].FileID)

	// The undo runs on the plain store: the injected fsync failure was for the combine.
	_, err = NewService(real).UndoCombine(res.JournalID)
	require.NoError(t, err)
	assert.Contains(t, viewFiles(t, real, trashed.ID), trashedRow, "undo puts the row back on its holder")
	assert.NotContains(t, viewFiles(t, real, survivor.ID), trashedRow)
}

// A created row whose fsync failed exists, so its record stays and undo gives it
// back to the absorbed book whose file it is (it would otherwise stay on the survivor).
func TestCombineBooks_CreateWhoseFsyncFailedIsAppliedAndStaysJournaled(t *testing.T) {
	real := setupTestStore(t)
	survivor, _ := seedHolder(t, real, "Year One", "/tmp/caf-fsync-create-s.mp3")
	path := "/tmp/caf-fsync-create-abs.mp3"
	absorbed, err := real.CreateBook(&database.Book{ID: ulid.Make().String(), Title: "Hush", Format: "mp3", FilePath: path})
	require.NoError(t, err)

	ms := NewService(&b3FaultStore{Store: real, unknownCreateBookFile: true})
	res, err := ms.CombineBooks([]string{survivor.ID, absorbed.ID}, survivor.ID, nil)
	require.NoError(t, err)

	created, err := real.GetBookFileByPath(path)
	require.NoError(t, err)
	require.NotNil(t, created, "the row was created: the write was applied")
	assert.Equal(t, survivor.ID, created.BookID)
	for _, w := range res.Warnings {
		assert.NotContains(t, w, "was not written")
	}
	require.NotEmpty(t, res.Warnings)
	assert.Contains(t, strings.Join(res.Warnings, "|"), "durability unknown")

	j, err := ms.GetCombineJournal(res.JournalID)
	require.NoError(t, err)
	require.Len(t, j.Absorbed[0].Files, 1, "the journal keeps the record of the created row")
	assert.True(t, j.Absorbed[0].Files[0].Created)

	_, err = NewService(real).UndoCombine(res.JournalID)
	require.NoError(t, err)
	back, err := real.GetBookFileByPath(path)
	require.NoError(t, err)
	require.NotNil(t, back)
	assert.Equal(t, absorbed.ID, back.BookID, "undo returns the created row to the absorbed book")
}
