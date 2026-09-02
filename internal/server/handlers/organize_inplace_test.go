// file: internal/server/handlers/organize_inplace_test.go
// version: 1.0.0
// guid: 9a4c7e21-5d3b-4f80-b6e2-1c8d0a7f3e94
// last-edited: 2026-09-02

package handlers_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/organizer"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
)

// OrganizeBook used to decide in-place vs new-version itself from a RootDir
// snapshot taken at startup, while OrganizeOneBook read the live value; after
// a runtime root_dir change the two disagreed and a file moved in place was
// also given a second book row at the same path. The decision now arrives on
// Landing.InPlace and the handler must follow it — these tests are the only
// observer of that branch.

type organizeStoreFake struct {
	book        *database.Book
	updated     []*database.Book
	changes     []*database.OperationChange
	getBookErr  error
	versionsErr error
}

func (f *organizeStoreFake) GetAuthorByID(int) (*database.Author, error)       { return nil, nil }
func (f *organizeStoreFake) GetSeriesByID(int) (*database.Series, error)       { return nil, nil }
func (f *organizeStoreFake) GetBookByFileHash(string) (*database.Book, error)  { return nil, nil }
func (f *organizeStoreFake) GetBookByFilePath(string) (*database.Book, error)  { return nil, nil }
func (f *organizeStoreFake) AddSystemActivityLog(string, string, string) error { return nil }
func (f *organizeStoreFake) GetBookFiles(string) ([]database.BookFile, error)  { return nil, nil }
func (f *organizeStoreFake) CreateOperationChange(ch *database.OperationChange) error {
	f.changes = append(f.changes, ch)
	return nil
}
func (f *organizeStoreFake) GetBookVersionsByBookID(string) ([]database.BookVersion, error) {
	return nil, f.versionsErr
}
func (f *organizeStoreFake) GetBookByID(id string) (*database.Book, error) {
	if f.getBookErr != nil {
		return nil, f.getBookErr
	}
	cp := *f.book
	return &cp, nil
}
func (f *organizeStoreFake) UpdateBook(id string, b *database.Book) (*database.Book, error) {
	cp := *b
	f.updated = append(f.updated, &cp)
	return &cp, nil
}

// organizeSvcSpy scripts OrganizeOneBook's Landing and records whether the
// handler went on to create a version.
type organizeSvcSpy struct {
	landing     *organizer.Landing
	createCalls int
	created     *database.Book
}

func (s *organizeSvcSpy) OrganizeOneBook(_ *organizer.Organizer, _ *database.Book, _ logger.Logger) (*organizer.Landing, error) {
	return s.landing, nil
}

func (s *organizeSvcSpy) CreateOrganizedVersion(book *database.Book, landing *organizer.Landing, opID string, _ logger.Logger) (*database.Book, error) {
	s.createCalls++
	s.created = &database.Book{ID: "v2-" + book.ID, FilePath: landing.Path}
	return s.created, nil
}

func organizeBook(t *testing.T, store *organizeStoreFake, svc *organizeSvcSpy) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	h := handlers.NewOrganizeHandler(store, nil, nil, svc, nil, nil, false)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/audiobooks/"+store.book.ID+"/organize", nil)
	c.Params = gin.Params{{Key: "id", Value: store.book.ID}}
	h.OrganizeBook(c)
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if data, ok := body["data"].(map[string]any); ok {
		body = data
	}
	return w, body
}

func TestOrganizeBook_InPlaceLanding_StampsBookAndCreatesNoVersion(t *testing.T) {
	store := &organizeStoreFake{book: &database.Book{ID: "b1", FilePath: "/lib/Old/Old.m4b"}}
	svc := &organizeSvcSpy{landing: &organizer.Landing{Path: "/lib/New/New.m4b", InPlace: true}}

	w, body := organizeBook(t, store, svc)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	if svc.createCalls != 0 {
		t.Fatalf("an in-place move must not mint a second book row; CreateOrganizedVersion called %d times", svc.createCalls)
	}
	if len(store.updated) != 1 || store.updated[0].ID != "b1" {
		t.Fatalf("expected the ORIGINAL book stamped once, got %+v", store.updated)
	}
	if store.updated[0].LastOrganizeOperationID == nil || *store.updated[0].LastOrganizeOperationID == "" {
		t.Error("in-place stamp missing LastOrganizeOperationID")
	}
	if len(store.changes) != 1 || store.changes[0].ChangeType != "organize_rename" ||
		store.changes[0].OldValue != "/lib/Old/Old.m4b" || store.changes[0].NewValue != "/lib/New/New.m4b" {
		t.Errorf("expected one organize_rename change row old→new, got %+v", store.changes)
	}
	if body["new_path"] != "/lib/New/New.m4b" || body["book_id"] != "b1" {
		t.Errorf("response should name the same book at its new path: %v", body)
	}
}

func TestOrganizeBook_CopyLanding_CreatesVersionAndStampsIt(t *testing.T) {
	store := &organizeStoreFake{book: &database.Book{ID: "b1", FilePath: "/incoming/Old.m4b"}}
	svc := &organizeSvcSpy{landing: &organizer.Landing{
		Path:  "/lib/New/New.m4b",
		Files: map[string]string{"/incoming/Old.m4b": "/lib/New/New.m4b"},
	}}

	w, body := organizeBook(t, store, svc)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	if svc.createCalls != 1 {
		t.Fatalf("a copy landing must create the organized version exactly once, got %d", svc.createCalls)
	}
	if len(store.updated) != 1 || store.updated[0].ID != "v2-b1" {
		t.Fatalf("expected the NEW version stamped, not the source book: %+v", store.updated)
	}
	for _, ch := range store.changes {
		if ch.ChangeType == "organize_rename" {
			t.Error("a copy landing must not record an in-place rename change row")
		}
	}
	if body["new_path"] != "/lib/New/New.m4b" {
		t.Errorf("response new_path: %v", body)
	}
}

// Same path in and out is neither branch: nothing is stamped, nothing created.
func TestOrganizeBook_AlreadyOrganized_TouchesNothing(t *testing.T) {
	store := &organizeStoreFake{book: &database.Book{ID: "b1", FilePath: "/lib/New/New.m4b"}}
	svc := &organizeSvcSpy{landing: &organizer.Landing{Path: "/lib/New/New.m4b", InPlace: true}}

	w, body := organizeBook(t, store, svc)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	if svc.createCalls != 0 || len(store.updated) != 0 || len(store.changes) != 0 {
		t.Errorf("already-organized must be a no-op: create=%d updates=%d changes=%d", svc.createCalls, len(store.updated), len(store.changes))
	}
	if body["message"] != "already organized" {
		t.Errorf("message: %v", body["message"])
	}
}
