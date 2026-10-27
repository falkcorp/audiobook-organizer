// file: internal/plugins/maintenance/journal_and_delete_book_files_test.go
// version: 1.1.1
// guid: 9a4d6c1e-2b57-4f0a-8c3e-7e1d5b9a0f42
// last-edited: 2026-10-26

package maintenance

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// ledgerStore is a MockStore whose operation-change writes land in a map keyed
// by change id, as the real store's do (a second write under the same id
// replaces the row), and whose bulk delete answers what the test says.
func ledgerStore(deleteResult func(refs []database.BookFileRef) ([]database.BookFile, error)) (*database.MockStore, map[string]database.OperationChange) {
	ledger := map[string]database.OperationChange{}
	m := &database.MockStore{}
	m.CreateOperationChangeFunc = func(c *database.OperationChange) error {
		ledger[c.ID] = *c
		return nil
	}
	m.DeleteBookFilesByIDsFunc = deleteResult
	return m, ledger
}

func gothamRows(bookID string) []database.BookFile {
	return []database.BookFile{
		{ID: "01BATMANFILE0001", BookID: bookID, FilePath: "/lib/Gotham/Year-One/01.m4b", FileHash: "gotham-hash-1", Duration: 100},
		{ID: "01BATMANFILE0002", BookID: bookID, FilePath: "/lib/Gotham/Year-One/02.m4b", FileHash: "gotham-hash-2", Duration: 200},
	}
}

func liveLedgerRows(ledger map[string]database.OperationChange) int {
	n := 0
	for _, c := range ledger {
		if !c.Voided {
			n++
		}
	}
	return n
}

// R4: a delete that deleted nothing must not leave live "book_file_delete" rows.
func TestJournalAndDeleteBookFiles_VoidsItsRowsWhenNothingWasDeleted(t *testing.T) {
	for name, cause := range map[string]error{
		"rows kept changing":    fmt.Errorf("gave up: %w", database.ErrBookFileConflict),
		"a row moved books":     fmt.Errorf("moved: %w", database.ErrBookFileOwnerChanged),
		"an id did not resolve": errors.New("2 book_file id(s) did not resolve, nothing deleted"),
	} {
		t.Run(name, func(t *testing.T) {
			store, ledger := ledgerStore(func([]database.BookFileRef) ([]database.BookFile, error) { return nil, cause })

			gone, err := journalAndDeleteBookFiles(store, "op-1", "book-1", gothamRows("book-1"))
			require.ErrorIs(t, err, cause)
			assert.Empty(t, gone, "no row was deleted")
			assert.Len(t, ledger, 2, "one row per file was journaled before the delete")
			assert.Zero(t, liveLedgerRows(ledger), "every journaled row of a delete that did not happen is voided")
			for _, c := range ledger {
				assert.NotNil(t, c.RevertedAt)
			}
		})
	}
}

// ... but not when only the fsync failed: that delete was applied, and its rows
// are what undoes it.
func TestJournalAndDeleteBookFiles_KeepsItsRowsWhenOnlyTheFsyncFailed(t *testing.T) {
	rows := gothamRows("book-1")
	store, ledger := ledgerStore(func([]database.BookFileRef) ([]database.BookFile, error) {
		return rows, fmt.Errorf("%w: injected", database.ErrBookFileDurabilityUnknown)
	})

	gone, err := journalAndDeleteBookFiles(store, "op-1", "book-1", rows)
	require.ErrorIs(t, err, database.ErrBookFileDurabilityUnknown)
	assert.Len(t, gone, 2, "the applied delete's rows are reported deleted")
	assert.Equal(t, 2, liveLedgerRows(ledger), "an applied delete keeps its journal rows")
}

// The rows are journaled from the caller's read; the store deletes the row it
// re-reads at its commit. When a writer changed a row in between, the journal
// row is rewritten (same id) to the bytes that were deleted.
func TestJournalAndDeleteBookFiles_JournalsTheBytesActuallyDeleted(t *testing.T) {
	rows := gothamRows("book-1")
	changed := rows[1]
	changed.FileHash, changed.Duration = "gotham-hash-rewritten", 250
	store, ledger := ledgerStore(func(refs []database.BookFileRef) ([]database.BookFile, error) {
		require.Equal(t, []database.BookFileRef{{BookID: "book-1", FileID: rows[0].ID}, {BookID: "book-1", FileID: rows[1].ID}}, refs,
			"each row is named under the book it was judged under")
		return []database.BookFile{rows[0], changed}, nil
	})

	gone, err := journalAndDeleteBookFiles(store, "op-1", "book-1", rows)
	require.NoError(t, err)
	assert.Len(t, gone, 2)
	require.Len(t, ledger, 2, "a rewrite replaces the row, it does not add one")
	for _, c := range ledger {
		var got database.BookFile
		require.NoError(t, json.Unmarshal([]byte(c.OldValue), &got))
		assert.False(t, c.Voided)
		switch got.ID {
		case rows[0].ID:
			assert.Equal(t, "gotham-hash-1", got.FileHash)
		case rows[1].ID:
			assert.Equal(t, "gotham-hash-rewritten", got.FileHash, "the pre-image is the row as deleted")
			assert.Equal(t, 250, got.Duration)
		default:
			t.Errorf("unexpected journal row for %s", got.ID)
		}
	}
}

// A call over the store's commit chunk can fail in a later chunk after an earlier
// one committed; the store then returns the rows that are gone with the error.
// Their journal rows stay (a replay is the only way back), the others are voided.
func TestJournalAndDeleteBookFiles_AFailureAfterAnEarlierChunkKeepsTheDeletedRowsJournal(t *testing.T) {
	rows := gothamRows("book-1")
	cause := fmt.Errorf("gave up: %w", database.ErrBookFileConflict)
	store, ledger := ledgerStore(func([]database.BookFileRef) ([]database.BookFile, error) {
		return rows[:1], cause // the first chunk's row is gone, the second row is not
	})

	gone, err := journalAndDeleteBookFiles(store, "op-1", "book-1", rows)
	require.ErrorIs(t, err, cause)
	assert.Equal(t, map[string]bool{rows[0].ID: true}, gone, "only the first chunk's row is reported deleted")
	require.Len(t, ledger, 2)
	assert.Equal(t, 1, liveLedgerRows(ledger), "only the deleted row's journal row stands")
	for _, c := range ledger {
		if c.FieldName == rows[0].ID {
			assert.False(t, c.Voided, "the deleted row's journal row must stand")
		} else {
			assert.True(t, c.Voided, "the untouched row's journal row must be voided")
		}
	}
}
