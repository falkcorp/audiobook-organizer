// file: internal/undo/writer_modify_test.go
// version: 1.0.0
// guid: 637d3886-809d-424c-a5aa-a37e9d01d9f2
// last-edited: 2026-10-30

package undo

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

func fileModifyRow(field, oldV, newV string) *database.OperationChange {
	return &database.OperationChange{ID: "c1", BookID: "b1", ChangeType: database.ChangeTypeBookFileModify,
		FieldName: field, OldValue: oldV, NewValue: newV}
}

func TestBookFileModifyFromField(t *testing.T) {
	key, id, ok := BookFileModifyFromField("file.track_number:f1")
	assert.True(t, ok)
	assert.Equal(t, "track_number", key)
	assert.Equal(t, "f1", id)
	for _, bad := range []string{"", "book_file:f1", "file.:f1", "file.title:", "file.title"} {
		_, _, ok := BookFileModifyFromField(bad)
		assert.False(t, ok, "%q", bad)
	}
}

func TestNotRestorableLabel_BookFileModify(t *testing.T) {
	assert.Empty(t, NotRestorableLabel(fileModifyRow("file.title:f1", `"A"`, `"B"`)))
	assert.Empty(t, NotRestorableLabel(fileModifyRow("file.title:f1", "", `"B"`)), "an absent side is a zero value")
	for name, c := range map[string]*database.OperationChange{
		"unknown field": fileModifyRow("file.no_such_field:f1", `"A"`, `"B"`),
		"identity":      fileModifyRow("file.book_id:f1", `"A"`, `"B"`),
		"wrong shape":   fileModifyRow("file.track_number:f1", `"A"`, `"B"`),
		"no file":       fileModifyRow("book_file:f1", `"A"`, `"B"`),
		"not json":      fileModifyRow("file.title:f1", "A", `"B"`),
	} {
		assert.Equal(t, database.ChangeTypeBookFileModify+":(unparsable)", NotRestorableLabel(c), name)
	}
}

func TestNotRestorableLabel_BookAuthorsModify(t *testing.T) {
	c := &database.OperationChange{ChangeType: database.ChangeTypeBookAuthorsModify, FieldName: "authors",
		OldValue: "null", NewValue: `[{"book_id":"b1","author_id":3,"role":"author","position":0}]`}
	assert.Empty(t, NotRestorableLabel(c))
	c.OldValue = ""
	assert.Equal(t, database.ChangeTypeBookAuthorsModify+":(unparsable)", NotRestorableLabel(c))
}

func TestCheckBookFileModifyCurrent(t *testing.T) {
	m := BookFileModify{Key: "track_number", FileID: "f1", Old: "1", New: "7"}
	f := &database.BookFile{ID: "f1", TrackNumber: 7}
	require.NoError(t, CheckBookFileModifyCurrent(f, m))
	f.TrackNumber = 1
	assert.True(t, errors.Is(CheckBookFileModifyCurrent(f, m), ErrAlreadyRestored))
	f.TrackNumber = 9
	assert.Equal(t, ReasonChangedSince, RefusalReason(CheckBookFileModifyCurrent(f, m)))

	// A zero value is an absent key on both sides.
	cleared := BookFileModify{Key: "title", FileID: "f1", Old: `"A"`, New: ""}
	require.NoError(t, CheckBookFileModifyCurrent(&database.BookFile{ID: "f1"}, cleared))
	assert.Equal(t, ReasonChangedSince, RefusalReason(CheckBookFileModifyCurrent(&database.BookFile{ID: "f1", Title: "Z"}, cleared)))
}

func TestApplyBookFileModifyOld(t *testing.T) {
	f := &database.BookFile{ID: "f1", BookID: "b1", Title: "B", TrackNumber: 7, FileHash: "h"}
	require.NoError(t, ApplyBookFileModifyOld(f, BookFileModify{Key: "title", Old: `"A"`}))
	require.NoError(t, ApplyBookFileModifyOld(f, BookFileModify{Key: "track_number", Old: ""}))
	assert.Equal(t, database.BookFile{ID: "f1", BookID: "b1", Title: "A", FileHash: "h"}, *f)
	assert.Error(t, ApplyBookFileModifyOld(f, BookFileModify{Key: "id", Old: `"x"`}))
}

func TestCheckBookAuthorsModifyCurrent_ComparesTheNormalisedWrite(t *testing.T) {
	// The callback returned positions 5 and 9 and a repeated pair; the store
	// kept 0 and 1.
	m := BookAuthorsModify{
		Before: []database.BookAuthor{{AuthorID: 1, Role: "author", Position: 0}},
		After: []database.BookAuthor{
			{AuthorID: 2, Role: "author", Position: 5},
			{AuthorID: 3, Role: "co-author", Position: 9},
			{AuthorID: 3, Role: "co-author", Position: 9},
		},
	}
	stored := []database.BookAuthor{{AuthorID: 2, Role: "author", Position: 0}, {AuthorID: 3, Role: "co-author", Position: 1}}
	require.NoError(t, CheckBookAuthorsModifyCurrent("b1", stored, nil, m))
	assert.True(t, errors.Is(CheckBookAuthorsModifyCurrent("b1", m.Before, nil, m), ErrAlreadyRestored))
	assert.Equal(t, ReasonChangedSince, RefusalReason(CheckBookAuthorsModifyCurrent("b1", stored[:1], nil, m)))
	assert.Equal(t, []int{1}, BookAuthorsModifyReturned(m))
}
