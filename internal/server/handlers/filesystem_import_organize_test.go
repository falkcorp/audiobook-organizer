// file: internal/server/handlers/filesystem_import_organize_test.go
// version: 1.0.0
// guid: 2a6f8c14-9b30-4e75-8d21-c73f0a5e6b19
// last-edited: 2026-08-25

package handlers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/importer"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
)

// ---------------------------------------------------------------------------
// Fakes. autoscanEnqueuer (filesystem_autoscan_test.go) is reused as-is — it
// already records defID and params, which is exactly what these tests assert
// on. Only the importer half is new.
// ---------------------------------------------------------------------------

type fakeFileImporter struct {
	resp   *importer.ImportFileResponse
	err    error
	gotReq *importer.ImportFileRequest
	calls  int
}

func (f *fakeFileImporter) ImportFile(req *importer.ImportFileRequest) (*importer.ImportFileResponse, error) {
	f.calls++
	f.gotReq = req
	if f.err != nil {
		return nil, f.err
	}
	return f.resp, nil
}

// importedBookID is deliberately NOT a substring of importedFilePath, and vice
// versa. If the handler ever enqueues the path where the id belongs, a
// Contains-style assertion must not accidentally pass.
const (
	importedBookID   = "01JBOOKID0000000000000000"
	importedFilePath = "/downloads/incoming/some-audiobook.m4b"
)

// doImportFile drives POST /import/file through the handler.
//
// rootDir and enq are the two things every case here varies; everything else is
// held constant so a failure points at the wiring rather than the fixture.
func doImportFile(t *testing.T, organize bool, rootDir string, enq handlers.SplitBookOpEnqueuer) (*httptest.ResponseRecorder, *fakeFileImporter) {
	t.Helper()
	// AuthorResolved: true is the HAPPY path, and must be stated explicitly.
	// Its zero value is false, which now declines the organize -- so a fixture
	// that forgot this field would make every organize test below assert the
	// refusal path while appearing to test the success path.
	return doImportFileAs(t, organize, rootDir, enq, true)
}

// doImportFileAs is doImportFile with authorResolved under the test's control.
func doImportFileAs(t *testing.T, organize bool, rootDir string, enq handlers.SplitBookOpEnqueuer, authorResolved bool) (*httptest.ResponseRecorder, *fakeFileImporter) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	imp := &fakeFileImporter{resp: &importer.ImportFileResponse{
		ID:             importedBookID,
		Title:          "Some Audiobook",
		FilePath:       importedFilePath,
		AuthorResolved: authorResolved,
	}}
	h := handlers.NewFilesystemHandler(
		nil, nil, nil, imp, enq, nil, rootDir, false,
	)
	body, _ := json.Marshal(map[string]any{"file_path": importedFilePath, "organize": organize})
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/import/file", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	h.ImportFile(c)
	return w, imp
}

// ---------------------------------------------------------------------------

// THE REGRESSION. `organize` was decoded into ImportFileRequest and read by
// NOTHING — not this handler, not importer.ImportFile — while the UI checkbox
// defaulted to ON. Ticking it produced a 201 and no organize, with no warning
// anywhere in the response or the logs.
func TestImportFile_OrganizeTrueEnqueuesLibraryOrganize(t *testing.T) {
	enq := &autoscanEnqueuer{returnID: "01JORGANIZEOP00000000000"}
	w, imp := doImportFile(t, true, "/library", enq)

	if w.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d: %s", w.Code, w.Body.String())
	}
	if imp.calls != 1 {
		t.Fatalf("expected exactly 1 import, got %d", imp.calls)
	}
	if !imp.gotReq.Organize {
		t.Fatalf("the decoded request lost Organize=true before reaching the handler logic")
	}
	if enq.calls != 1 {
		t.Fatalf("expected exactly 1 enqueue, got %d", enq.calls)
	}
	if enq.gotDefID != "library.organize" {
		t.Errorf("wrong def enqueued: %q", enq.gotDefID)
	}
}

// The op must carry the BOOK ID, not the file path.
//
// This is the assertion that earns its keep. OrganizeOneBook os.Renames the
// file under RootDir, so a FilePath captured before the run is stale after it —
// and a call-count assertion passes identically whether the handler plumbs
// result.ID or req.FilePath. Assert the params CONTENT.
func TestImportFile_OrganizeParamsCarryTheBookIDNotThePath(t *testing.T) {
	enq := &autoscanEnqueuer{returnID: "op-1"}
	doImportFile(t, true, "/library", enq)

	raw, err := json.Marshal(enq.gotParams)
	if err != nil {
		t.Fatalf("params must marshal: %v", err)
	}
	if !bytes.Contains(raw, []byte(importedBookID)) {
		t.Fatalf("params must carry the created book id %q, got %s", importedBookID, raw)
	}
	if bytes.Contains(raw, []byte(importedFilePath)) {
		t.Fatalf("params must NOT carry a file path — it is stale the moment organize renames: %s", raw)
	}
	if !bytes.Contains(raw, []byte("book_ids")) {
		t.Fatalf("params lost book_ids, so library.organize would organize the WHOLE LIBRARY: %s", raw)
	}
}

// book_ids empty/absent means "organize everything" to PerformOrganize
// (service.go:254 branches on len(req.BookIDs) > 0). Enqueueing an
// import-triggered organize with no ids would walk the entire library.
func TestImportFile_OrganizeTargetsExactlyOneBook(t *testing.T) {
	enq := &autoscanEnqueuer{returnID: "op-1"}
	doImportFile(t, true, "/library", enq)

	var decoded struct {
		BookIDs []string `json:"book_ids"`
	}
	raw, _ := json.Marshal(enq.gotParams)
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("params must decode: %v", err)
	}
	if len(decoded.BookIDs) != 1 {
		t.Fatalf("want exactly 1 book id, got %d (%v)", len(decoded.BookIDs), decoded.BookIDs)
	}
	if decoded.BookIDs[0] != importedBookID {
		t.Fatalf("want %q, got %q", importedBookID, decoded.BookIDs[0])
	}
}

// The response must name the op, or a caller cannot distinguish a queued
// organize from a silently dropped one — which is the bug being fixed.
func TestImportFile_ResponseCarriesTheOrganizeOpID(t *testing.T) {
	enq := &autoscanEnqueuer{returnID: "01JORGANIZEOP00000000000"}
	w, _ := doImportFile(t, true, "/library", enq)

	var body struct {
		Data struct {
			ID         string `json:"id"`
			OrganizeOp string `json:"organize_operation_id"`
			Skipped    string `json:"organize_skipped"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("response must decode: %v (%s)", err, w.Body.String())
	}
	if body.Data.OrganizeOp != enq.returnID {
		t.Fatalf("organize_operation_id must be the id EnqueueOp returned (%s), got %q",
			enq.returnID, body.Data.OrganizeOp)
	}
	if body.Data.Skipped != "" {
		t.Fatalf("must not report a skip for an organize that queued: %q", body.Data.Skipped)
	}
	// The import half of the response must survive the added fields.
	if body.Data.ID != importedBookID {
		t.Fatalf("response lost the book id: %s", w.Body.String())
	}
}

// organize:false must enqueue NOTHING. This is the half that was already
// correct, and the half a careless fix breaks by wiring unconditionally.
func TestImportFile_OrganizeFalseEnqueuesNothing(t *testing.T) {
	enq := &autoscanEnqueuer{returnID: "op-1"}
	w, imp := doImportFile(t, false, "/library", enq)

	if w.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d: %s", w.Code, w.Body.String())
	}
	if imp.calls != 1 {
		t.Fatalf("the import itself must still happen, got %d calls", imp.calls)
	}
	if enq.calls != 0 {
		t.Fatalf("organize:false must not enqueue anything, got %d enqueues of %q",
			enq.calls, enq.gotDefID)
	}
	if strings.Contains(w.Body.String(), "organize_") {
		t.Fatalf("organize:false must not mention organize at all: %s", w.Body.String())
	}
}

// No root configured: organizer.ensureUnderRoot fails closed on an empty root,
// so nothing would move — but handing back an op id for a run guaranteed to
// fail every book is a lie. Skip, say why, keep the import.
func TestImportFile_NoRootDirSkipsOrganizeButKeepsTheImport(t *testing.T) {
	enq := &autoscanEnqueuer{returnID: "op-1"}
	w, imp := doImportFile(t, true, "", enq)

	if w.Code != http.StatusCreated {
		t.Fatalf("the import must still succeed, got %d: %s", w.Code, w.Body.String())
	}
	if imp.calls != 1 {
		t.Fatalf("expected the import to run, got %d calls", imp.calls)
	}
	if enq.calls != 0 {
		t.Fatalf("must not enqueue an organize with no root configured, got %d", enq.calls)
	}
	if !strings.Contains(w.Body.String(), "organize_skipped") {
		t.Fatalf("a skipped organize must be reported, not inferred from a bare 201: %s", w.Body.String())
	}
	if strings.Contains(w.Body.String(), "organize_operation_id") {
		t.Fatalf("must not advertise an op id for an organize that never queued: %s", w.Body.String())
	}
}

// A nil enqueuer must not panic, and must not silently swallow the request.
// Deliberately NO synchronous fallback here, unlike AddImportPath.
func TestImportFile_NilEnqueuerSkipsOrganizeWithoutPanicking(t *testing.T) {
	w, imp := doImportFile(t, true, "/library", nil)

	if w.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d: %s", w.Code, w.Body.String())
	}
	if imp.calls != 1 {
		t.Fatalf("expected the import to run, got %d calls", imp.calls)
	}
	if !strings.Contains(w.Body.String(), "organize_skipped") {
		t.Fatalf("a skipped organize must be reported: %s", w.Body.String())
	}
}

// An enqueue failure must not fail the import — the book WAS created, and
// 4xx-ing here would tell the caller to retry an import that already
// succeeded, duplicating the book.
func TestImportFile_EnqueueFailureStillReturnsTheImportedBook(t *testing.T) {
	enq := &autoscanEnqueuer{returnErr: context.DeadlineExceeded}
	w, imp := doImportFile(t, true, "/library", enq)

	if w.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d: %s", w.Code, w.Body.String())
	}
	if imp.calls != 1 {
		t.Fatalf("expected exactly 1 import, got %d", imp.calls)
	}
	if !strings.Contains(w.Body.String(), importedBookID) {
		t.Fatalf("response must still carry the imported book: %s", w.Body.String())
	}
	if strings.Contains(w.Body.String(), "organize_operation_id") {
		t.Fatalf("must not advertise an id for an op that failed to queue: %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "organize_skipped") {
		t.Fatalf("a failed enqueue must be reported: %s", w.Body.String())
	}
}

// A failed import must not enqueue an organize for a book that does not exist.
func TestImportFile_FailedImportEnqueuesNothing(t *testing.T) {
	gin.SetMode(gin.TestMode)
	enq := &autoscanEnqueuer{returnID: "op-1"}
	imp := &fakeFileImporter{err: context.DeadlineExceeded}
	h := handlers.NewFilesystemHandler(nil, nil, nil, imp, enq, nil, "/library", false)

	body, _ := json.Marshal(map[string]any{"file_path": importedFilePath, "organize": true})
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/import/file", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	h.ImportFile(c)

	if w.Code == http.StatusCreated {
		t.Fatalf("a failed import must not answer 201: %s", w.Body.String())
	}
	if enq.calls != 0 {
		t.Fatalf("must not enqueue an organize for a book that was never created, got %d", enq.calls)
	}
}

// THE SECOND GATE. Closing the book_file gate was not enough: an untagged
// import produces a book with no author, and FilterBooksNeedingOrganization
// defers those (internal/organizer/service.go:715) rather than baking the
// placeholder into the path. Queueing an op guaranteed to hit that gate, and
// handing back an op id for it, is the same lie this PR exists to remove --
// worse than the old bare 201, because it actively asserts an organize.
//
// Found by two independent reviewers; neither the handler tests nor the
// importer tests could see it, because it lives in the gap between them.
func TestImportFile_NoResolvedAuthorDeclinesOrganize(t *testing.T) {
	enq := &autoscanEnqueuer{returnID: "op-1"}
	w, imp := doImportFileAs(t, true, "/library", enq, false)

	if w.Code != http.StatusCreated {
		t.Fatalf("the import must still succeed, got %d: %s", w.Code, w.Body.String())
	}
	if imp.calls != 1 {
		t.Fatalf("expected the import to run, got %d calls", imp.calls)
	}
	if enq.calls != 0 {
		t.Fatalf("must not queue an organize the filter is guaranteed to defer, got %d", enq.calls)
	}
	if strings.Contains(w.Body.String(), "organize_operation_id") {
		t.Fatalf("must not advertise an op id for an organize that will not happen: %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "organize_skipped") {
		t.Fatalf("the refusal must be reported: %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "author") {
		t.Fatalf("the reason must name the cause so it is actionable: %s", w.Body.String())
	}
}

// A resolved author must NOT block the organize -- the complement, without
// which the test above passes just as well if the handler refused everything.
func TestImportFile_ResolvedAuthorStillOrganizes(t *testing.T) {
	enq := &autoscanEnqueuer{returnID: "op-1"}
	_, _ = doImportFileAs(t, true, "/library", enq, true)
	if enq.calls != 1 {
		t.Fatalf("a book with a resolved author must still organize, got %d enqueues", enq.calls)
	}
}

// EnqueueOp returning ("", nil) must not be advertised as a queued organize.
func TestImportFile_EmptyOpIDIsNotAdvertised(t *testing.T) {
	enq := &autoscanEnqueuer{returnID: ""}
	w, _ := doImportFile(t, true, "/library", enq)

	if strings.Contains(w.Body.String(), "organize_operation_id") {
		t.Fatalf("an empty op id must not be advertised: %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "organize_skipped") {
		t.Fatalf("must report that no trackable organize was queued: %s", w.Body.String())
	}
}
