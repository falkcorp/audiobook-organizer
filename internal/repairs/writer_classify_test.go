// file: internal/repairs/writer_classify_test.go
// version: 1.0.0
// guid: 3f0c8e52-6b1d-4a97-9d24-7c5e1a8b4f60
// last-edited: 2026-10-25

package repairs

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// unclassifiedChangeType is a type no package declares: nobody has said
// whether its rows are one user's data, so the Writer must not record it.
const unclassifiedChangeType = "unclassified_batman_edit"

// TestWriter_RefusesUnclassifiedChangeType: every way a fixer journals a row
// (Journal, Step, JournalStep, JournalSteps, RecordChange, ModifyCredits) is
// refused for a change type undo does not classify, before the lease beat, the
// journal and the write, and the error matches both ErrNotJournaled (fixers
// already treat it as "the write was not made") and ErrUnclassifiedChangeType.
func TestWriter_RefusesUnclassifiedChangeType(t *testing.T) {
	wrote := false
	write := func() error { wrote = true; return nil }
	writeSteps := func() ([]bool, error) { wrote = true; return []bool{true, true}, nil }

	cases := map[string]func(w *Writer) error{
		"Journal": func(w *Writer) error {
			return w.Journal("book-year-one", unclassifiedChangeType, "title", "Year One", "Hush")
		},
		"Step": func(w *Writer) error {
			return w.Step("book-year-one", unclassifiedChangeType, "title", "Year One", "Hush", write)
		},
		"JournalStep": func(w *Writer) error {
			return w.JournalStep("book-year-one", UndoEntry{ChangeType: unclassifiedChangeType, Field: "title", Old: "Year One", New: "Hush"}, write)
		},
		"JournalSteps": func(w *Writer) error {
			// The first step is classified and the second is not: the whole call is
			// refused before the first row is journaled.
			return w.JournalSteps([]BookStep{
				{BookID: "book-year-one", Entry: UndoEntry{ChangeType: undo.ChangeTypeBookPathUpdate, Field: "file_path", Old: "/lib/Gotham/year-one.m4b", New: ""}},
				{BookID: "book-hush", Entry: UndoEntry{ChangeType: unclassifiedChangeType, Field: "title", Old: "Hush", New: "Year One"}},
			}, writeSteps)
		},
		"RecordChange": func(w *Writer) error {
			return w.RecordChange("book-year-one", UndoEntry{ChangeType: unclassifiedChangeType, Field: "title", Old: "Year One", New: "Hush"})
		},
		"ModifyCredits": func(w *Writer) error {
			_, err := w.ModifyCredits("book-year-one", func([]database.BookAuthor) ([]database.BookAuthor, UndoEntry, error) {
				return nil, UndoEntry{ChangeType: unclassifiedChangeType, Field: "book_authors", Old: "old", New: "new"}, nil
			})
			return err
		},
	}
	for name, run := range cases {
		t.Run(name, func(t *testing.T) {
			wrote = false
			f := &creditFake{credits: map[string][]database.BookAuthor{"book-year-one": nil}}
			w := credWriter(newMemStore(), f)
			err := run(w)
			require.ErrorIs(t, err, ErrUnclassifiedChangeType)
			require.ErrorIs(t, err, ErrNotJournaled)
			require.Contains(t, err.Error(), unclassifiedChangeType)
			require.False(t, wrote, "the write is not made")
			require.Empty(t, f.journal, "no row is journaled, not even the classified step of a mixed call")
			require.Empty(t, f.order)
			require.Equal(t, 0, w.Writes())
		})
	}
}

// TestWriter_AcceptsClassifiedChangeTypes: a user-data type, a library-data
// type and the library-data literals the fixers write all journal.
func TestWriter_AcceptsClassifiedChangeTypes(t *testing.T) {
	for _, ct := range []string{
		undo.ChangeTypeUserBookStateSet, // user data
		undo.ChangeTypeBookPathUpdate,   // library data, an undo constant
		"metadata_update",               // library data, a literal
		"series_merge",
		"series_delete",
		database.ChangeTypeBookFileRepoint, // library data, declared in database
	} {
		t.Run(ct, func(t *testing.T) {
			f := &creditFake{credits: map[string][]database.BookAuthor{}}
			w := credWriter(newMemStore(), f)
			require.NoError(t, w.Journal("book-hush", ct, "field", "old", "new"))
			require.Len(t, f.journal, 1)
			require.Equal(t, ct, f.journal[0].ChangeType)
		})
	}
}
