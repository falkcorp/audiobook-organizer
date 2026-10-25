// file: internal/merge/combine_attach_failure_test.go
// version: 1.0.0
// guid: d540b8fe-78f6-4317-af34-77988b64e1b4
// last-edited: 2026-10-25

package merge

import (
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
	trashed, trashedRow := seedHolder(t, real, "Trashed holder", path)
	require.NoError(t, SoftDeleteBook(real, trashed.ID))
	survivor, err := real.CreateBook(&database.Book{ID: ulid.Make().String(), Title: "Survivor", Format: "mp3", FilePath: "/tmp/caf-trashed-holder-s.mp3"})
	require.NoError(t, err)
	absorbed, err := real.CreateBook(&database.Book{ID: ulid.Make().String(), Title: "Rowless absorbed", Format: "mp3", FilePath: path})
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
	survivor, _ := seedHolder(t, real, "Survivor with a row", "/tmp/caf-create-s.mp3")
	path := "/tmp/caf-create-abs.mp3"
	absorbed, err := real.CreateBook(&database.Book{ID: ulid.Make().String(), Title: "Rowless absorbed", Format: "mp3", FilePath: path})
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
