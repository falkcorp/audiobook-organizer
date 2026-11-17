// file: internal/seriesdenumber/journal.go
// version: 1.0.0
// guid: 8a3f6c10-2d95-4be7-b148-d5e07a9c3f61
// last-edited: 2026-11-16

package seriesdenumber

import "strconv"

// The undo-journal vocabulary of a book move. These are the rows
// undo.RestoreBookField and its compare-and-set (CheckBookFieldCurrent) already
// restore, so a revert of the op puts each book back through the existing
// path; the series rows (create, delete) are the types of internal/undo.
const (
	ChangeTypeMetadataUpdate = "metadata_update"
	FieldSeriesID            = "series_id"
	FieldSeriesSequence      = "series_sequence"
)

// Row is one undo-journal row for a book, before the writer stamps its id and
// operation.
type Row struct {
	BookID     string
	ChangeType string
	FieldName  string
	OldValue   string
	NewValue   string
}

// MoveRows are the rows a move writes: the series link, old series id to the
// target's, and the position when the move sets one (an empty OldValue means
// the book had none, which a revert restores to none).
//
// The link row comes first. Written in that order they sit older in the journal
// than the position row, and the revert's paired-row rule
// (undo.CheckSequencePaired) treats the position as numbered in the series
// the link row names.
func MoveRows(m Move, toSeriesID int) []Row {
	rows := []Row{{
		BookID: m.BookID, ChangeType: ChangeTypeMetadataUpdate, FieldName: FieldSeriesID,
		OldValue: strconv.Itoa(m.FromSeriesID), NewValue: strconv.Itoa(toSeriesID),
	}}
	if m.SetSeq {
		rows = append(rows, Row{
			BookID: m.BookID, ChangeType: ChangeTypeMetadataUpdate, FieldName: FieldSeriesSequence,
			OldValue: "", NewValue: strconv.Itoa(m.NewSeq),
		})
	}
	return rows
}
