// file: internal/server/handlers/audiobooks/handler_trash_progress_test.go
// version: 1.5.0
// guid: 410c4eb2-df80-4e6c-9f17-d88025337957
// last-edited: 2026-10-26

package audiobookshandler_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/mock"

	audiobookspkg "github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
)

func TestForgetWaitingPurgeFileRecords_StatusMapping(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		want     int
		wantCode string
	}{
		{"ok", nil, http.StatusOK, ""},
		{"no audit log", audiobookspkg.ErrAuditUnavailable, http.StatusServiceUnavailable, ""},
		{"wrapped no audit log", fmt.Errorf("forget: %w", audiobookspkg.ErrAuditUnavailable), http.StatusServiceUnavailable, ""},
		{"nothing shown", audiobookspkg.ErrForgetNothingShown, http.StatusBadRequest, ""},
		{"too many", fmt.Errorf("%w: 501 paths were sent", audiobookspkg.ErrForgetTooMany), http.StatusBadRequest, ""},
		{"purge busy", audiobookspkg.ErrPurgeBusy, http.StatusConflict, "PURGE_BUSY"},
		{"root not ready", fmt.Errorf("nothing forgotten: %w", audiobookspkg.ErrLibraryRootNotReady), http.StatusServiceUnavailable, "ROOT_NOT_READY"},
		{"other", errString("journal unreadable"), http.StatusInternalServerError, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, d := newHandler(t)
			var res *audiobookspkg.ForgetWaitingResult
			if tc.err == nil {
				res = &audiobookspkg.ForgetWaitingResult{Records: 3, Paths: 2, Warnings: []string{"the activity log row was not written"}}
			}
			shown := []string{"/lib/Gotham/Bruce Wayne/hush.m4b", "/lib/Gotham/Arkham/knightfall.m4b"}
			d.svc.EXPECT().ForgetWaitingFileRecords(mock.Anything, "unknown", shown).Return(res, tc.err)
			c, w := newCtx("POST", "/audiobooks/purge-soft-deleted/forget-waiting-file-records", map[string]any{"paths": shown}, nil)
			h.ForgetWaitingPurgeFileRecords(c)
			if w.Code != tc.want {
				t.Fatalf("want %d, got %d (%s)", tc.want, w.Code, w.Body.String())
			}
			if tc.wantCode != "" {
				var eb struct {
					Code string `json:"code"`
				}
				if err := json.Unmarshal(w.Body.Bytes(), &eb); err != nil || eb.Code != tc.wantCode {
					t.Fatalf("code = %q (%v), want %s: %s", eb.Code, err, tc.wantCode, w.Body.String())
				}
			}
			if tc.err != nil {
				return
			}
			var body struct {
				Data struct {
					Records  int      `json:"records_forgotten"`
					Paths    int      `json:"paths_forgotten"`
					Warnings []string `json:"warnings"`
				} `json:"data"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Data.Records != 3 || body.Data.Paths != 2 || len(body.Data.Warnings) != 1 {
				t.Fatalf("result not carried to the client: %s", w.Body.String())
			}
		})
	}
}

// The body must carry the paths the dialog listed: no body, or a body that is
// not {"paths": [...]}, is a 400 and reaches the service not at all (the
// strict mock fails on an unexpected call).
func TestForgetWaitingPurgeFileRecords_RejectsAMissingOrMalformedBody(t *testing.T) {
	for name, body := range map[string]any{"no body": nil, "wrong shape": map[string]any{"paths": "hush.m4b"}} {
		t.Run(name, func(t *testing.T) {
			h, _ := newHandler(t)
			c, w := newCtx("POST", "/audiobooks/purge-soft-deleted/forget-waiting-file-records", body, nil)
			h.ForgetWaitingPurgeFileRecords(c)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("want 400, got %d (%s)", w.Code, w.Body.String())
			}
		})
	}
}

// GET waiting-file-records carries the listing, and a failed read is a 500.
func TestListWaitingPurgeFileRecords(t *testing.T) {
	h, d := newHandler(t)
	d.svc.EXPECT().WaitingFileRecordPaths().Return(audiobookspkg.WaitingFilePaths{
		Paths: []string{"/lib/Gotham/Arkham/knightfall.m4b"}, Total: 7, Limit: 500}, nil).Once()
	c, w := newCtx("GET", "/audiobooks/purge-soft-deleted/waiting-file-records", nil, nil)
	h.ListWaitingPurgeFileRecords(c)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d (%s)", w.Code, w.Body.String())
	}
	var body struct {
		Data struct {
			Paths []string `json:"paths"`
			Total int      `json:"total"`
			Limit int      `json:"limit"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Data.Paths) != 1 || body.Data.Total != 7 || body.Data.Limit != 500 {
		t.Fatalf("listing not carried to the client: %s", w.Body.String())
	}

	d.svc.EXPECT().WaitingFileRecordPaths().Return(audiobookspkg.WaitingFilePaths{}, errString("journal unreadable")).Once()
	c, w = newCtx("GET", "/audiobooks/purge-soft-deleted/waiting-file-records", nil, nil)
	h.ListWaitingPurgeFileRecords(c)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d", w.Code)
	}
}

// The trash listing carries has_progress / progress_summary per row, and a
// row whose state could not be read says progress_unknown rather than
// reading as "no progress".
func TestListSoftDeletedAudiobooks_FlagsProgress(t *testing.T) {
	h, d := newHandler(t)
	d.svc.EXPECT().GetSoftDeletedBooks(mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return([]database.Book{{ID: "held", Title: "Held"}, {ID: "clean"}, {ID: "unread"}}, nil)
	d.svc.EXPECT().CountSoftDeletedBooks(mock.Anything, mock.Anything).Return(3, nil)
	d.svc.EXPECT().LastPurgeBreakdown().Return(nil)
	d.svc.EXPECT().PendingFileRemovals().Return(audiobookspkg.PendingFileCounts{}, nil)
	d.svc.EXPECT().TrashProgress(mock.Anything, []string{"held", "clean", "unread"}).
		Return(map[string]audiobookspkg.TrashProgressInfo{
			"held":  {HasProgress: true, Summary: "reader: finished"},
			"clean": {},
		}, nil)
	c, w := newCtx("GET", "/audiobooks/soft-deleted", nil, nil)
	h.ListSoftDeletedAudiobooks(c)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
	var body struct {
		Data struct {
			Items []map[string]any `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Data.Items) != 3 {
		t.Fatalf("want 3 items, got %d", len(body.Data.Items))
	}
	held, clean, unread := body.Data.Items[0], body.Data.Items[1], body.Data.Items[2]
	if held["id"] != "held" || held["title"] != "Held" {
		t.Fatalf("the book's own fields must stay on the row: %v", held)
	}
	if held["has_progress"] != true || held["progress_summary"] != "reader: finished" {
		t.Fatalf("held row: %v", held)
	}
	if clean["has_progress"] != false || clean["progress_unknown"] != nil {
		t.Fatalf("clean row: %v", clean)
	}
	if unread["progress_unknown"] != true {
		t.Fatalf("a row with no answer must say progress_unknown: %v", unread)
	}
}

func TestDiscardProgressAndPurge_StatusMapping(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		want     int
		wantCode string
	}{
		{"ok", nil, http.StatusOK, ""},
		{"not found", audiobookspkg.ErrAudiobookNotFound, http.StatusNotFound, ""},
		{"not in trash", fmt.Errorf("%w: b1", audiobookspkg.ErrNotInTrash), http.StatusConflict, "CONFLICT"},
		{"owns files", fmt.Errorf("%w: b1", database.ErrBookOwnsFiles), http.StatusConflict, "OWNS_FILES"},
		{"refused", fmt.Errorf("%w: pending repair", audiobookspkg.ErrDiscardRefused), http.StatusConflict, "CONFLICT"},
		// The progress WAS erased and the delete then refused: a different
		// code from OWNS_FILES ("nothing was deleted"), so a client can say so.
		{"progress erased, book kept on file rows", &merge.DiscardedBookKeptError{BookID: "b1", Users: 1, Bookmarks: 2,
			Err: fmt.Errorf("delete book b1: %w (1 row(s))", database.ErrBookOwnsFiles)},
			http.StatusConflict, "PROGRESS_DISCARDED_BOOK_KEPT"},
		{"progress erased, book kept on a store failure", &merge.DiscardedBookKeptError{BookID: "b1", Users: 1,
			Err: errString("pebble closed")},
			http.StatusInternalServerError, "PROGRESS_DISCARDED_BOOK_KEPT"},
		{"undo possible", fmt.Errorf("discard progress and purge b1: %w", audiobookspkg.ErrPurgeUndoPossible), http.StatusConflict, "UNDO_POSSIBLE"},
		{"merge busy", fmt.Errorf("discard: %w", merge.ErrMergeBusy), http.StatusConflict, "MERGE_BUSY"},
		{"no audit", audiobookspkg.ErrAuditUnavailable, http.StatusServiceUnavailable, ""},
		{"other", errString("pebble closed"), http.StatusInternalServerError, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, d := newHandler(t)
			var res *audiobookspkg.DiscardProgressResult
			if tc.err == nil {
				res = &audiobookspkg.DiscardProgressResult{BookID: "b1", UsersCleared: 1}
			}
			d.svc.EXPECT().DiscardProgressAndPurge(mock.Anything, "b1", "unknown").Return(res, tc.err)
			c, w := newCtx("POST", "/audiobooks/b1/discard-progress-and-purge", nil, p("id", "b1"))
			h.DiscardProgressAndPurge(c)
			if w.Code != tc.want {
				t.Fatalf("want %d, got %d (%s)", tc.want, w.Code, w.Body.String())
			}
			if tc.wantCode != "" {
				var body struct {
					Code string `json:"code"`
				}
				if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
					t.Fatal(err)
				}
				if body.Code != tc.wantCode {
					t.Fatalf("code = %q, want %q", body.Code, tc.wantCode)
				}
			}
		})
	}
}

// A: DELETE /audiobooks/:id answers a refusal for listening progress with
// 409 and the stable HAS_PROGRESS code (the trash page then offers Discard
// progress and purge), a failed carry and any other failure with 500 and
// the reason, and only a missing book with 404 -- every other error used to
// read as "not found".
func TestDeleteAudiobook_StatusMapping(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		want     int
		wantCode string
	}{
		{"ok", nil, http.StatusOK, ""},
		{"has progress", fmt.Errorf("purge b1: %w. Restore it", audiobookspkg.ErrBookHasProgress), http.StatusConflict, "HAS_PROGRESS"},
		{"undo possible", fmt.Errorf("purge b1: %w", audiobookspkg.ErrPurgeUndoPossible), http.StatusConflict, "UNDO_POSSIBLE"},
		{"merge busy", merge.ErrMergeBusy, http.StatusConflict, "MERGE_BUSY"},
		{"carry failed", fmt.Errorf("purge b1: %w: boom", audiobookspkg.ErrPurgeCarryFailed), http.StatusInternalServerError, "CARRY_FAILED"},
		// #3777 review: the live hard delete returns the merge carry errors
		// themselves (merge.HardDeleteKeepingUserStateLocked), wrapped.
		{"live carry incomplete", fmt.Errorf("hard delete b1: %w: b1 -> b2: boom", merge.ErrStateCarryIncomplete), http.StatusInternalServerError, "CARRY_FAILED"},
		{"live carry target unreadable", fmt.Errorf("hard delete b1: %w: boom", merge.ErrCarryTargetUnreadable), http.StatusInternalServerError, "CARRY_FAILED"},
		{"owns files", fmt.Errorf("%w: b1", database.ErrBookOwnsFiles), http.StatusConflict, "OWNS_FILES"},
		{"live delete owns files after carry", fmt.Errorf("hard delete b1: delete book b1: %w (1 row(s))", database.ErrBookOwnsFiles), http.StatusConflict, "OWNS_FILES"},
		// The state WAS moved and the delete then failed: not the OWNS_FILES
		// "nothing was deleted", so a client can tell the two apart.
		{"state moved, book kept on file rows", &merge.StateMovedBookKeptError{BookID: "b1", MovedTo: "b2",
			Err: fmt.Errorf("hard delete b1 after its state moved to b2: delete book b1: %w (1 row(s))", database.ErrBookOwnsFiles)},
			http.StatusConflict, "STATE_MOVED_BOOK_KEPT"},
		{"state moved, book kept on a store failure", &merge.StateMovedBookKeptError{BookID: "b1", MovedTo: "b2",
			Err: fmt.Errorf("hard delete b1 after its state moved to b2: pebble closed")},
			http.StatusInternalServerError, "STATE_MOVED_BOOK_KEPT"},
		{"not found", errString("audiobook not found"), http.StatusNotFound, ""},
		// N-6: a root that is not mounted is not a failure of the book.
		{"library root not ready", fmt.Errorf("purge b1: %w: only 0 of 1 sampled live book files can be seen", audiobookspkg.ErrLibraryRootNotReady), http.StatusServiceUnavailable, "ROOT_NOT_READY"},
		{"other", errString("pebble closed"), http.StatusInternalServerError, "DELETE_FAILED"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, d := newHandler(t)
			var res map[string]any
			if tc.err == nil {
				res = map[string]any{"message": "audiobook purged"}
			}
			d.svc.EXPECT().DeleteAudiobook(mock.Anything, "b1", mock.Anything).Return(res, tc.err)
			c, w := newCtx("DELETE", "/audiobooks/b1", nil, p("id", "b1"))
			h.DeleteAudiobook(c)
			if w.Code != tc.want {
				t.Fatalf("want %d, got %d (%s)", tc.want, w.Code, w.Body.String())
			}
			if tc.wantCode != "" {
				var body struct {
					Code  string `json:"code"`
					Error string `json:"error"`
				}
				if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
					t.Fatal(err)
				}
				if body.Code != tc.wantCode || body.Error == "" {
					t.Fatalf("body = %+v, want code %s and the reason", body, tc.wantCode)
				}
			}
		})
	}
}
