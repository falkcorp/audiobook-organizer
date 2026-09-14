// file: internal/server/handlers/metadata/handler_writeback_rename_test.go
// version: 1.1.0
// guid: 9a4c6e8b-0d2f-4b1a-8e3c-5f7a9b1d3e25
// last-edited: 2026-09-14

package metadatahandler_test

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/mock"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/internal/organizer"
)

var renameBody = map[string]any{"rename": true}

// The preflight predicts the rename fails: 409 file_work_would_fail, and
// nothing moves and no tag is written (neither mock call is expected).
func TestWriteBack_RenamePreflightRefuses409(t *testing.T) {
	h, d := newHandler(t)
	d.store.EXPECT().GetBookByID("b1").Return(&database.Book{ID: "b1", Title: "T"}, nil)
	d.mfs.EXPECT().RenameOnlyPreflight("b1").
		Return(fmt.Errorf("%w: compute target paths failed: dup", metafetch.ErrApplyFileWorkWouldFail))

	w := doReq(h.WriteBackAudiobookMetadata, http.MethodPost, "/audiobooks/b1/write-back", renameBody, idParam("b1"))
	if w.Code != http.StatusConflict {
		t.Fatalf("want 409, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), metafetch.ApplyRefusedReasonFileWorkWouldFail) {
		t.Fatalf("body does not name %s: %s", metafetch.ApplyRefusedReasonFileWorkWouldFail, w.Body.String())
	}
}

// The rename fails part-way on a duplicate target: 409 with the reason, and
// no tags are written onto the half-moved book.
func TestWriteBack_RenameDuplicateTarget409NoTags(t *testing.T) {
	h, d := newHandler(t)
	d.store.EXPECT().GetBookByID("b1").Return(&database.Book{ID: "b1", Title: "T"}, nil)
	d.mfs.EXPECT().RenameOnlyPreflight("b1").Return(nil)
	d.mfs.EXPECT().RunApplyPipelineRenameOnly(mock.Anything, "b1", mock.Anything).
		Return(fmt.Errorf("compute target paths for book b1: %w", organizer.ErrDuplicateRenameTarget))

	w := doReq(h.WriteBackAudiobookMetadata, http.MethodPost, "/audiobooks/b1/write-back", renameBody, idParam("b1"))
	if w.Code != http.StatusConflict {
		t.Fatalf("want 409, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "no tags were written") {
		t.Fatalf("body does not say tags were not written: %s", w.Body.String())
	}
}

// Any other rename failure is a 500 with the reason, still with no tag write.
func TestWriteBack_RenameFailure500NoTags(t *testing.T) {
	h, d := newHandler(t)
	d.store.EXPECT().GetBookByID("b1").Return(&database.Book{ID: "b1", Title: "T"}, nil)
	d.mfs.EXPECT().RenameOnlyPreflight("b1").Return(nil)
	d.mfs.EXPECT().RunApplyPipelineRenameOnly(mock.Anything, "b1", mock.Anything).
		Return(errors.New("rename files: link tmp dest: file exists"))

	w := doReq(h.WriteBackAudiobookMetadata, http.MethodPost, "/audiobooks/b1/write-back", renameBody, idParam("b1"))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "file exists") {
		t.Fatalf("body does not carry the rename error: %s", w.Body.String())
	}
}

// A clean rename still proceeds to the tag write and reports renamed.
func TestWriteBack_RenameThenTags200(t *testing.T) {
	h, d := newHandler(t)
	d.store.EXPECT().GetBookByID("b1").Return(&database.Book{ID: "b1", Title: "T"}, nil)
	d.mfs.EXPECT().RenameOnlyPreflight("b1").Return(nil)
	d.mfs.EXPECT().RunApplyPipelineRenameOnly(mock.Anything, "b1", mock.Anything).Return(nil)
	d.mfs.EXPECT().WriteBackMetadataForBook("b1").Return(3, nil)

	w := doReq(h.WriteBackAudiobookMetadata, http.MethodPost, "/audiobooks/b1/write-back", renameBody, idParam("b1"))
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"renamed":true`) {
		t.Fatalf("want renamed:true: %s", w.Body.String())
	}
}
