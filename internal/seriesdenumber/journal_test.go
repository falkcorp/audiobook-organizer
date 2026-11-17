// file: internal/seriesdenumber/journal_test.go
// version: 1.0.0
// guid: d47a0b92-6e15-4c38-8f2d-0b9c5e71a364
// last-edited: 2026-11-16

package seriesdenumber

import (
	"slices"
	"testing"
)

func TestMoveRows(t *testing.T) {
	seven := 7
	t.Run("a book with no position gets a link row then a position row", func(t *testing.T) {
		rows := MoveRows(Move{BookID: "b1", FromSeriesID: 40, NewSeq: 5, SetSeq: true}, 90)
		want := []Row{
			{"b1", "metadata_update", "series_id", "40", "90"},
			{"b1", "metadata_update", "series_sequence", "", "5"},
		}
		if !slices.Equal(rows, want) {
			t.Fatalf("rows = %+v, want %+v", rows, want)
		}
	})
	t.Run("a book that already holds the position gets only the link row", func(t *testing.T) {
		rows := MoveRows(Move{BookID: "b2", FromSeriesID: 41, OldSeq: &seven, NewSeq: 7}, 90)
		want := []Row{{"b2", "metadata_update", "series_id", "41", "90"}}
		if !slices.Equal(rows, want) {
			t.Fatalf("rows = %+v, want %+v", rows, want)
		}
	})
}
