// file: internal/server/handlers/organize_scan_lock_test.go
// version: 1.1.0
// guid: 0e8b5c27-4a13-4f96-b7d2-c93a61e8f405
// last-edited: 2026-09-30

package handlers_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/organizer"
	"github.com/falkcorp/audiobook-organizer/internal/scanlock"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
)

// countingOrganizeSvc counts OrganizeOneBook calls: the moment the organize
// touches files.
type countingOrganizeSvc struct {
	organizeSvcSpy
	calls atomic.Int32
}

func (s *countingOrganizeSvc) OrganizeOneBook(o *organizer.Organizer, b *database.Book, l logger.Logger) (*organizer.Landing, error) {
	s.calls.Add(1)
	return s.organizeSvcSpy.OrganizeOneBook(o, b, l)
}

type fakeOrganizeQueuer struct{ ids []string }

func (f *fakeOrganizeQueuer) EnqueueOrganizeWhenScanned(_ context.Context, id string) (string, error) {
	f.ids = append(f.ids, id)
	return "op-organize-1", nil
}

func organizeRequest(h *handlers.OrganizeHandler, id string) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/audiobooks/"+id+"/organize", nil)
	c.Params = gin.Params{{Key: "id", Value: id}}
	h.OrganizeBook(c)
	return w
}

func lockBook(t *testing.T, id string) *scanlock.Hold {
	t.Helper()
	h, err := scanlock.Books.LockSet(context.Background(), []string{id})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.Release)
	return h
}

func inPlaceSvc() *countingOrganizeSvc {
	return &countingOrganizeSvc{organizeSvcSpy: organizeSvcSpy{landing: &organizer.Landing{Path: "/lib/New/New.m4b", InPlace: true}}}
}

// The scanner holds the book: the organize waits (touching no file), then
// organizes as soon as the scanner releases it.
func TestOrganizeBook_WaitsForTheScannerThenOrganizes(t *testing.T) {
	store := &organizeStoreFake{book: &database.Book{ID: "b1", FilePath: "/lib/Old/Old.m4b"}}
	svc := inPlaceSvc()
	h := handlers.NewOrganizeHandler(store, nil, nil, svc, nil, nil, false)
	scan := lockBook(t, "b1")

	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- organizeRequest(h, "b1") }()
	time.Sleep(100 * time.Millisecond)
	if n := svc.calls.Load(); n != 0 {
		t.Fatalf("organize touched the files %d time(s) while the scanner held the book", n)
	}
	scan.Release()
	select {
	case w := <-done:
		if w.Code != http.StatusOK {
			t.Fatalf("status %d: %s", w.Code, w.Body.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("organize never ran after the scanner released the book")
	}
	if svc.calls.Load() != 1 {
		t.Fatalf("OrganizeOneBook calls = %d, want 1", svc.calls.Load())
	}
	if n := scanlock.Books.Held(); n != 0 {
		t.Fatalf("organize left %d scan lock(s) held", n)
	}
}

// The organize of a protected original X acts on its library copy S. It locks
// {X, S}: a scanner holding only S (a version group over the scanner's cap)
// still keeps the organize off S's files.
func TestOrganizeBook_LocksTheLibraryCopyItActsOn(t *testing.T) {
	store := &organizeStoreFake{book: &database.Book{ID: "b1", FilePath: "/lib/Old/Old.m4b"}}
	svc := inPlaceSvc()
	h := handlers.NewOrganizeHandler(store, nil, nil, svc, nil, nil, false)
	h.SetLibraryCopyResolver(func(b *database.Book) (*database.Book, bool) {
		cp := *b
		cp.ID = "s1"
		return &cp, true
	})
	scan := lockBook(t, "s1")

	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- organizeRequest(h, "b1") }()
	time.Sleep(100 * time.Millisecond)
	if n := svc.calls.Load(); n != 0 {
		t.Fatalf("organize touched the library copy %d time(s) while the scanner held it", n)
	}
	scan.Release()
	select {
	case w := <-done:
		if w.Code != http.StatusOK {
			t.Fatalf("status %d: %s", w.Code, w.Body.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("organize never ran after the scanner released the copy")
	}
	if n := scanlock.Books.Held(); n != 0 {
		t.Fatalf("organize left %d scan lock(s) held", n)
	}
}

// The scanner holds a DIFFERENT book: this organize does not wait at all.
func TestOrganizeBook_OtherBookHeldDoesNotWait(t *testing.T) {
	defer handlers.SetOrganizeBookLockWaitForTest(5 * time.Second)()
	store := &organizeStoreFake{book: &database.Book{ID: "b1", FilePath: "/lib/Old/Old.m4b"}}
	svc := inPlaceSvc()
	h := handlers.NewOrganizeHandler(store, nil, nil, svc, nil, nil, false)
	lockBook(t, "other")

	start := time.Now()
	w := organizeRequest(h, "b1")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if el := time.Since(start); el > time.Second {
		t.Fatalf("organize of b1 waited %s for another book", el)
	}
}

// Held past the bound: 202 queued, never 409, and no file is touched now.
func TestOrganizeBook_HeldPastTheBoundIsQueuedNot409(t *testing.T) {
	defer handlers.SetOrganizeBookLockWaitForTest(30 * time.Millisecond)()
	store := &organizeStoreFake{book: &database.Book{ID: "b1", FilePath: "/lib/Old/Old.m4b"}}
	svc := inPlaceSvc()
	h := handlers.NewOrganizeHandler(store, nil, nil, svc, nil, nil, false)
	q := &fakeOrganizeQueuer{}
	h.SetOrganizeQueuer(q)
	lockBook(t, "b1")

	w := organizeRequest(h, "b1")
	if w.Code != http.StatusAccepted {
		t.Fatalf("status %d, want 202: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"queued":true`) || !strings.Contains(w.Body.String(), "op-organize-1") {
		t.Fatalf("202 body: %s", w.Body.String())
	}
	if len(q.ids) != 1 || q.ids[0] != "b1" || svc.calls.Load() != 0 {
		t.Fatalf("queued %v, organize calls %d", q.ids, svc.calls.Load())
	}
}

// The queued organize waits for the book, then runs the same core; a refusal
// the request would have answered with an error status fails the op.
func TestRunQueuedOrganize_WaitsThenRunsAndReportsFailure(t *testing.T) {
	store := &organizeStoreFake{book: &database.Book{ID: "b1", FilePath: "/lib/Old/Old.m4b"}}
	svc := inPlaceSvc()
	h := handlers.NewOrganizeHandler(store, nil, nil, svc, nil, nil, false)
	scan := lockBook(t, "b1")

	done := make(chan error, 1)
	go func() { done <- h.RunQueuedOrganize(context.Background(), "b1", nil) }()
	time.Sleep(50 * time.Millisecond)
	if svc.calls.Load() != 0 {
		t.Fatal("queued organize ran while the scanner held the book")
	}
	scan.Release()
	if err := <-done; err != nil {
		t.Fatalf("RunQueuedOrganize: %v", err)
	}
	if svc.calls.Load() != 1 {
		t.Fatalf("OrganizeOneBook calls = %d", svc.calls.Load())
	}

	failing := &countingOrganizeSvc{organizeSvcSpy: organizeSvcSpy{err: errors.New("disk on fire")}}
	h2 := handlers.NewOrganizeHandler(store, nil, nil, failing, nil, nil, false)
	err := h2.RunQueuedOrganize(context.Background(), "b1", nil)
	if err == nil || !strings.Contains(err.Error(), "disk on fire") {
		t.Fatalf("want the organize failure reported, got %v", err)
	}
}
