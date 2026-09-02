// file: internal/scanner/ai_parse_series_position_test.go
// version: 1.0.0
// guid: 3f8b2c07-6d15-4e93-a204-9c7e1b5da648
// last-edited: 2026-09-02

package scanner

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/require"
)

// The AI-parse path resolves a series by NAME, and resolveSeriesID strips the
// position out of that name. saveAIFieldsToPrimary is the only place that
// number can be recorded, so these tests exist because the write-back there had
// no coverage at all: mutation testing killed every mutant on the synchronous
// scanner path and both mutants on this one SURVIVED.
//
// TestSaveAIFieldsNeverOverwritesAnExistingValue does not cover this. It asserts
// on Title and Narrator, and the Book it passes has no Series at all, so the
// series block never executes.

// aiSeriesRow saves AI fields onto a freshly created row and returns the stored
// row plus the name of the series it ended up pointing at.
func aiSeriesRow(t *testing.T, seed *database.Book, parsed *Book) (*database.Book, string) {
	t.Helper()
	store := aiSaveStore(t)

	seed.FilePath = "/library/Author/Book/book.m4b"
	row, err := store.CreateBook(seed)
	require.NoError(t, err)

	parsed.FilePath = row.FilePath
	require.NoError(t, saveAI(row.ID, parsed))

	got, err := store.GetBookByID(row.ID)
	require.NoError(t, err)
	require.NotNil(t, got)

	name := ""
	if got.SeriesID != nil {
		all, sErr := store.GetAllSeries()
		require.NoError(t, sErr)
		for _, s := range all {
			if s.ID == *got.SeriesID {
				name = s.Name
			}
		}
	}
	return got, name
}

// The number in the series name is MOVED, not deleted: "Discworld 05" must
// resolve to a series called "Discworld" with the 5 recorded on the book.
func TestSaveAIFields_RecordsThePositionStrippedFromTheSeriesName(t *testing.T) {
	got, name := aiSeriesRow(t,
		&database.Book{Title: "Wyrd Sisters"},
		&Book{Title: "Wyrd Sisters", Series: "Discworld 05"})

	require.Equal(t, "Discworld", name, "the number must not survive in the series name")
	require.NotNil(t, got.SeriesSequence, "the stripped position must be recorded, not dropped")
	require.Equal(t, 5, *got.SeriesSequence)
}

// The model's own position is better evidence than a regex over a name, so it
// wins when both are available.
func TestSaveAIFields_PrefersTheModelsOwnPositionOverTheStrippedOne(t *testing.T) {
	got, name := aiSeriesRow(t,
		&database.Book{Title: "Wyrd Sisters"},
		&Book{Title: "Wyrd Sisters", Series: "Discworld 05", Position: 9})

	require.Equal(t, "Discworld", name)
	require.NotNil(t, got.SeriesSequence)
	require.Equal(t, 9, *got.SeriesSequence)
}

// A sequence the row already carries came from somewhere with more context than
// a regex over a name. Overwriting it is silent, unrecoverable data loss.
func TestSaveAIFields_DoesNotOverwriteAnExistingSequence(t *testing.T) {
	existing := 3
	got, name := aiSeriesRow(t,
		&database.Book{Title: "Wyrd Sisters", SeriesSequence: &existing},
		&Book{Title: "Wyrd Sisters", Series: "Discworld 05"})

	require.Equal(t, "Discworld", name)
	require.NotNil(t, got.SeriesSequence)
	require.Equal(t, 3, *got.SeriesSequence, "an existing sequence must never be overwritten")
}
