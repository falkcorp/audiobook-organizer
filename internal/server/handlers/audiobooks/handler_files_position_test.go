// file: internal/server/handlers/audiobooks/handler_files_position_test.go
// version: 1.0.0
// guid: 3b0e6f5a-2c41-4d8e-9a7b-6f1c2d9e8a53
// last-edited: 2026-09-13

package audiobookshandler_test

import (
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/mock"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

func fileParams() gin.Params {
	return gin.Params{{Key: "id", Value: "b1"}, {Key: "file_id", Value: "f1"}}
}

// A manual track/disc edit lands on the row and is recorded in the book's
// change history under a field that names the file.
func TestPatchBookFile_SetsTrackAndDiscAndRecordsHistory(t *testing.T) {
	h, d := newHandler(t)
	d.store.EXPECT().GetBookFileByID("b1", "f1").Return(&database.BookFile{ID: "f1", BookID: "b1", TrackNumber: 3, DiscNumber: 1}, nil)
	d.store.EXPECT().UpsertBookFile(mock.MatchedBy(func(f *database.BookFile) bool {
		return f.TrackNumber == 0 && f.DiscNumber == 2
	})).Return(nil)
	var recs []*database.MetadataChangeRecord
	d.store.EXPECT().RecordMetadataChange(mock.Anything).RunAndReturn(func(r *database.MetadataChangeRecord) error {
		recs = append(recs, r)
		return nil
	}).Times(2)

	c, w := newCtx("PATCH", "/audiobooks/b1/files/f1", map[string]any{"track_number": 0, "disc_number": 2}, fileParams())
	h.PatchBookFile(c)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	want := map[string][2]string{
		"book_file:f1:track_number": {"3", "0"},
		"book_file:f1:disc_number":  {"1", "2"},
	}
	if len(recs) != len(want) {
		t.Fatalf("want %d history rows, got %d", len(want), len(recs))
	}
	for _, r := range recs {
		exp, ok := want[r.Field]
		if !ok {
			t.Fatalf("unexpected history field %q", r.Field)
		}
		if r.BookID != "b1" || r.PreviousValue == nil || r.NewValue == nil ||
			*r.PreviousValue != exp[0] || *r.NewValue != exp[1] {
			t.Fatalf("history row %q = %+v, want %v", r.Field, r, exp)
		}
	}
}

// An unchanged number writes the row (other fields may be in the body) but
// records no history.
func TestPatchBookFile_SameTrackRecordsNoHistory(t *testing.T) {
	h, d := newHandler(t)
	d.store.EXPECT().GetBookFileByID("b1", "f1").Return(&database.BookFile{ID: "f1", TrackNumber: 4}, nil)
	d.store.EXPECT().UpsertBookFile(mock.Anything).Return(nil)
	c, w := newCtx("PATCH", "/audiobooks/b1/files/f1", map[string]any{"track_number": 4}, fileParams())
	h.PatchBookFile(c)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
}

func TestPatchBookFile_RejectsNegativePositions(t *testing.T) {
	for _, body := range []map[string]any{{"track_number": -1}, {"disc_number": -2}} {
		h, _ := newHandler(t)
		c, w := newCtx("PATCH", "/audiobooks/b1/files/f1", body, fileParams())
		h.PatchBookFile(c)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%v: want 400, got %d", body, w.Code)
		}
	}
}

// Undo of a file position edit reverts the file row, never a book override.
func TestUndoMetadataChange_RevertsBookFilePosition(t *testing.T) {
	h, d := newHandler(t)
	prev, next := "3", "0"
	field := "book_file:f1:track_number"
	d.store.EXPECT().GetMetadataChangeHistory("b1", field, 1).Return([]database.MetadataChangeRecord{
		{BookID: "b1", Field: field, PreviousValue: &prev, NewValue: &next, ChangeType: "manual"},
	}, nil)
	d.store.EXPECT().GetBookFileByID("b1", "f1").Return(&database.BookFile{ID: "f1", TrackNumber: 0}, nil)
	d.store.EXPECT().UpsertBookFile(mock.MatchedBy(func(f *database.BookFile) bool { return f.TrackNumber == 3 })).Return(nil)
	d.store.EXPECT().RecordMetadataChange(mock.MatchedBy(func(r *database.MetadataChangeRecord) bool {
		return r.ChangeType == "undo" && r.Field == field
	})).Return(nil)
	d.metaFetch.EXPECT().InvalidateCachedCandidates("b1").Return(nil).Maybe()

	c, w := newCtx("POST", "/audiobooks/b1/metadata-history/"+field+"/undo", nil,
		gin.Params{{Key: "id", Value: "b1"}, {Key: "field", Value: field}})
	h.UndoMetadataChange(c)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
}

// The row no longer holds the number the edit set: undo refuses (409) rather
// than overwrite the later change.
func TestUndoMetadataChange_BookFilePositionChangedSince(t *testing.T) {
	h, d := newHandler(t)
	prev, next := "3", "0"
	field := "book_file:f1:track_number"
	d.store.EXPECT().GetMetadataChangeHistory("b1", field, 1).Return([]database.MetadataChangeRecord{
		{BookID: "b1", Field: field, PreviousValue: &prev, NewValue: &next},
	}, nil)
	d.store.EXPECT().GetBookFileByID("b1", "f1").Return(&database.BookFile{ID: "f1", TrackNumber: 7}, nil)

	c, w := newCtx("POST", "/audiobooks/b1/metadata-history/"+field+"/undo", nil,
		gin.Params{{Key: "id", Value: "b1"}, {Key: "field", Value: field}})
	h.UndoMetadataChange(c)
	if w.Code != http.StatusConflict {
		t.Fatalf("want 409, got %d: %s", w.Code, w.Body.String())
	}
}
