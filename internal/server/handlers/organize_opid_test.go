// file: internal/server/handlers/organize_opid_test.go
// version: 1.0.0
// guid: 4f8b2d61-9c07-4a35-b8e2-6d1a3f70c974
// last-edited: 2026-08-23

package handlers_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/organizer"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
)

// ApplyRename and OrganizeBook used to mint a v1 operations row and pass its id
// down as the correlation key for the OperationChange records undo replays.
// Nothing ever read that row — the v1 list/status/logs routes were retired
// 2026-08-16, the timeline reads v2 only, and CreateOperation stamps "pending",
// which isResumableOpStatus excludes, so the restart sweep skipped it too. Every
// rename and single-book organize left one immortal pending row behind.
//
// The row is gone; the id is not. These tests cover what the row's absence
// removed a backstop for: a store insert would at least have surfaced a reused
// id, and now nothing does.

type renameSpy struct {
	gotOpIDs []string
}

func (r *renameSpy) PreviewRename(string) (*organizer.RenamePreview, error) {
	return &organizer.RenamePreview{}, nil
}

func (r *renameSpy) ApplyRename(_ string, operationID string) (*organizer.RenameApplyResult, error) {
	r.gotOpIDs = append(r.gotOpIDs, operationID)
	return &organizer.RenameApplyResult{}, nil
}

func applyRename(t *testing.T, spy *renameSpy, bookID string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	h := handlers.NewOrganizeHandler(nil, spy, nil, nil, nil, nil, "/library", false)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/audiobooks/"+bookID+"/rename/apply", nil)
	c.Params = gin.Params{{Key: "id", Value: bookID}}
	h.ApplyRename(c)
	return w
}

// An empty correlation key would file every change row under one blank prefix,
// so GetOperationChanges("") would return a pile of unrelated changes and undo
// would revert them together. reconcile.apply guards the same invariant
// explicitly; here the id is minted locally, so the check is that it arrives.
func TestApplyRename_PassesANonEmptyOperationID(t *testing.T) {
	spy := &renameSpy{}
	w := applyRename(t, spy, "book-1")

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	if len(spy.gotOpIDs) != 1 {
		t.Fatalf("expected exactly one rename call, got %d", len(spy.gotOpIDs))
	}
	if spy.gotOpIDs[0] == "" {
		t.Fatal("the operation id is the opchange correlation key; an empty one " +
			"files every change under a single blank key")
	}
}

// Two renames must not share a key. While a v1 row was minted per call, a reused
// id would have collided on insert; with no row there is nothing to notice it,
// and two books' undo records would merge into one operation.
func TestApplyRename_MintsADistinctIDPerCall(t *testing.T) {
	spy := &renameSpy{}
	applyRename(t, spy, "book-1")
	applyRename(t, spy, "book-2")

	if len(spy.gotOpIDs) != 2 {
		t.Fatalf("expected two rename calls, got %d", len(spy.gotOpIDs))
	}
	if spy.gotOpIDs[0] == spy.gotOpIDs[1] {
		t.Fatalf("each rename needs its own correlation key, both got %q", spy.gotOpIDs[0])
	}
}

// The id must not be minted before the request is known to be valid — a bad
// request should reach neither the service nor an id.
func TestApplyRename_MissingBookIDNeverReachesTheService(t *testing.T) {
	spy := &renameSpy{}
	gin.SetMode(gin.TestMode)
	h := handlers.NewOrganizeHandler(nil, spy, nil, nil, nil, nil, "/library", false)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/audiobooks//rename/apply", nil)
	h.ApplyRename(c)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", w.Code)
	}
	if len(spy.gotOpIDs) != 0 {
		t.Fatalf("service must not be called for a request with no book id, got %v", spy.gotOpIDs)
	}
}
