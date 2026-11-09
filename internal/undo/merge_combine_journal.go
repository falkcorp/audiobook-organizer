// file: internal/undo/merge_combine_journal.go
// version: 1.1.4
// guid: 5e3a9c17-2d84-4f60-b9a1-7c06e4d2f8b3
// last-edited: 2026-11-09

package undo

import (
	"strings"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
)

// ChangeTypeMergeCombineJournal: books were combined through a merge core
// that keeps its own undo record (merge.CombineJournal), and this row names
// that record. It is journaled on the merge's keep book (BookID) right after
// the merge wrote its journal Pending and before the merge's first mutation:
// by maintenance's dedup.split-books fixer (through its repairs Writer; the fixer
// was retired 2026-11-05 and its journals remain), and by
// every other combine at the chokepoints themselves (merge.Service
// CombineBooksWithOptions and dedup.MergeSplitBookClusterWithOptions, through
// merge.RecordJournalPointer), under the caller's operation id or, for a
// combine no operation runs, a correlation id of its own. FieldName is
// MergeCombineJournalField, NewValue the combine journal id, OldValue "".
//
// Restorable: the revert undoes that combine journal
// (merge.Service.UndoCombine), which moves every file row, external id and
// listening progress the merge recorded back and restores the books it
// retired, refusing as a whole when the library changed since. A journal
// already undone (by an earlier revert; POST /merge/undo/:journal_id is retired)
// counts restored with nothing written.
//
// merge.RecordJournalPointer writes the row and merge cannot import this
// package, so the string is defined there.
const ChangeTypeMergeCombineJournal = merge.CombineJournalPointerType

// MergeCombineJournalField is the FieldName of a
// ChangeTypeMergeCombineJournal row.
const MergeCombineJournalField = merge.JournalPointerField

// validMergeCombineJournalRow reports whether c names a combine journal id
// the merge package can look up (no key separators).
func validMergeCombineJournalRow(c *database.OperationChange) bool {
	return c.FieldName == MergeCombineJournalField && c.NewValue != "" && !strings.ContainsAny(c.NewValue, ":/")
}
