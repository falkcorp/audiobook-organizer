// file: internal/server/handlers/organize_scan_lock_test.go
// version: 1.5.0
// guid: 0e8b5c27-4a13-4f96-b7d2-c93a61e8f405
// last-edited: 2026-11-18

package handlers_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
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
	mu    sync.Mutex
	ids   []string
}

func (s *countingOrganizeSvc) OrganizeOneBook(o *organizer.Organizer, b *database.Book, l logger.Logger) (*organizer.Landing, error) {
	s.calls.Add(1)
	s.mu.Lock()
	s.ids = append(s.ids, b.ID)
	s.mu.Unlock()
	return s.organizeSvcSpy.OrganizeOneBook(o, b, l)
}

// organized is the ID of every row OrganizeOneBook was called on, in order.
func (s *countingOrganizeSvc) organized() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.ids...)
}

// copyAppearsAtCoreCheck is a resolver for a book that has no library copy when
// the lock step looks (its two resolutions per attempt) and gains one, "s1",
// just before the core's own re-check: the copy "appears after the lock set
// settled". With everyAttempt the third resolution of EVERY attempt finds it
// and the book never settles; without, it appears once and the next attempt's
// lock step finds it and holds it.
func copyAppearsAtCoreCheck(everyAttempt bool) func(*database.Book) (*database.Book, bool, *database.Book) {
	var calls atomic.Int32
	return func(b *database.Book) (*database.Book, bool, *database.Book) {
		n := calls.Add(1)
		appeared := n > 2
		if everyAttempt {
			appeared = n%3 == 0
		}
		if !appeared {
			return nil, false, nil
		}
		cp := *b
		cp.ID = "s1"
		return &cp, true, nil
	}
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
	return &countingOrganizeSvc{landing: &organizer.Landing{Path: "/lib/New/New.m4b", InPlace: true}}
}

// The scanner holds the book: the organize waits (touching no file), then
// organizes as soon as the scanner releases it.
func TestOrganizeBook_WaitsForTheScannerThenOrganizes(t *testing.T) {
	store := &organizeStoreFake{book: &database.Book{ID: "b1", FilePath: "/lib/Old/Old.m4b"}}
	svc := inPlaceSvc()
	h := handlers.NewOrganizeHandler(store, nil, svc, nil, false)
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
	h := handlers.NewOrganizeHandler(store, nil, svc, nil, false)
	h.SetLibraryCopyResolver(func(b *database.Book) (*database.Book, bool, *database.Book) {
		cp := *b
		cp.ID = "s1"
		return &cp, true, nil
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
	h := handlers.NewOrganizeHandler(store, nil, svc, nil, false)
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
	h := handlers.NewOrganizeHandler(store, nil, svc, nil, false)
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
	h := handlers.NewOrganizeHandler(store, nil, svc, nil, false)
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

	failing := &countingOrganizeSvc{err: errors.New("disk on fire")}
	h2 := handlers.NewOrganizeHandler(store, nil, failing, nil, false)
	err := h2.RunQueuedOrganize(context.Background(), "b1", nil)
	if err == nil || !strings.Contains(err.Error(), "disk on fire") {
		t.Fatalf("want the organize failure reported, got %v", err)
	}
}

// unsettledResolver resolves the book to a DIFFERENT library copy on every
// call: the lock set never settles.
func unsettledResolver() func(*database.Book) (*database.Book, bool, *database.Book) {
	var n atomic.Int32
	return func(b *database.Book) (*database.Book, bool, *database.Book) {
		cp := *b
		cp.ID = "copy-" + string(rune('a'+n.Add(1)%26))
		return &cp, true, nil
	}
}

// A copy that keeps changing while the organize locks it: the request never
// organizes a copy it does not hold. It holds nothing, touches no file, and
// queues the organize (202) for later.
func TestOrganizeBook_UnsettledLockSetQueuesAndTouchesNothing(t *testing.T) {
	store := &organizeStoreFake{book: &database.Book{ID: "b1", FilePath: "/lib/Old/Old.m4b"}}
	svc := inPlaceSvc()
	h := handlers.NewOrganizeHandler(store, nil, svc, nil, false)
	h.SetLibraryCopyResolver(unsettledResolver())
	q := &fakeOrganizeQueuer{}
	h.SetOrganizeQueuer(q)

	w := organizeRequest(h, "b1")
	if w.Code != http.StatusAccepted {
		t.Fatalf("status %d, want 202: %s", w.Code, w.Body.String())
	}
	if svc.calls.Load() != 0 {
		t.Fatalf("organized %d time(s) with a lock set that never settled", svc.calls.Load())
	}
	if len(q.ids) != 1 || q.ids[0] != "b1" {
		t.Fatalf("queued %v", q.ids)
	}
	if n := scanlock.Books.Held(); n != 0 {
		t.Fatalf("left %d scan lock(s) held", n)
	}
}

// The queued organize retries an unsettled set a bounded number of times,
// then fails without organizing anything.
func TestRunQueuedOrganize_UnsettledLockSetFailsWithoutOrganizing(t *testing.T) {
	defer handlers.SetOrganizeUnsettledRetryForTest(2, time.Millisecond)()
	store := &organizeStoreFake{book: &database.Book{ID: "b1", FilePath: "/lib/Old/Old.m4b"}}
	svc := inPlaceSvc()
	h := handlers.NewOrganizeHandler(store, nil, svc, nil, false)
	h.SetLibraryCopyResolver(unsettledResolver())

	err := h.RunQueuedOrganize(context.Background(), "b1", nil)
	if !handlers.IsOrganizeSetUnsettled(err) {
		t.Fatalf("want the unsettled refusal, got %v", err)
	}
	if svc.calls.Load() != 0 {
		t.Fatalf("organized %d time(s) with a lock set that never settled", svc.calls.Load())
	}
	if n := scanlock.Books.Held(); n != 0 {
		t.Fatalf("left %d scan lock(s) held", n)
	}
}

// A library copy that appears AFTER the lock set settled is a row the hold does
// not cover. The core must not swap it in and organize it without its scan
// lock. It is the same condition the lock step retries, so the request retries
// too: the second lock step resolves the copy, holds it, and organizes THAT row
// exactly once (200), where the first attempt wrote nothing.
func TestOrganizeBook_CopyAppearingAfterTheLockSetSettledRetriesAndOrganizesTheCopy(t *testing.T) {
	store := &organizeStoreFake{book: &database.Book{ID: "b1", FilePath: "/lib/Old/Old.m4b"}}
	svc := inPlaceSvc()
	h := handlers.NewOrganizeHandler(store, nil, svc, nil, false)
	h.SetLibraryCopyResolver(copyAppearsAtCoreCheck(false))

	w := organizeRequest(h, "b1")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d, want 200 (the retry organizes the copy): %s", w.Code, w.Body.String())
	}
	if got := svc.organized(); len(got) != 1 || got[0] != "s1" {
		t.Fatalf("organized %v; want exactly [s1], the copy the retry held (never the original, never twice)", got)
	}
	if n := scanlock.Books.Held(); n != 0 {
		t.Fatalf("left %d scan lock(s) held", n)
	}
}

// When the core finds a copy it does not hold on every attempt, the request
// stops after organizeLockTries attempts and answers like a lock set that never
// settles: 409 with no queuer wired, nothing organized, no lock left held.
func TestOrganizeBook_CoreUnsettledOnEveryAttemptAnswers409(t *testing.T) {
	store := &organizeStoreFake{book: &database.Book{ID: "b1", FilePath: "/lib/Old/Old.m4b"}}
	svc := inPlaceSvc()
	h := handlers.NewOrganizeHandler(store, nil, svc, nil, false)
	h.SetLibraryCopyResolver(copyAppearsAtCoreCheck(true))

	w := organizeRequest(h, "b1")
	if w.Code != http.StatusConflict {
		t.Fatalf("status %d, want 409: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "kept changing") {
		t.Fatalf("409 is not the unsettled refusal: %s", w.Body.String())
	}
	if svc.calls.Load() != 0 {
		t.Fatalf("organized %d time(s) a copy that was never in the lock hold", svc.calls.Load())
	}
	if n := scanlock.Books.Held(); n != 0 {
		t.Fatalf("left %d scan lock(s) held", n)
	}
}

// With a queuer wired, the same never-settling core is queued (202), exactly as
// the lock step queues a set that never settles.
func TestOrganizeBook_CoreUnsettledOnEveryAttemptIsQueued(t *testing.T) {
	store := &organizeStoreFake{book: &database.Book{ID: "b1", FilePath: "/lib/Old/Old.m4b"}}
	svc := inPlaceSvc()
	h := handlers.NewOrganizeHandler(store, nil, svc, nil, false)
	h.SetLibraryCopyResolver(copyAppearsAtCoreCheck(true))
	q := &fakeOrganizeQueuer{}
	h.SetOrganizeQueuer(q)

	w := organizeRequest(h, "b1")
	if w.Code != http.StatusAccepted {
		t.Fatalf("status %d, want 202: %s", w.Code, w.Body.String())
	}
	if len(q.ids) != 1 || q.ids[0] != "b1" {
		t.Fatalf("queued %v", q.ids)
	}
	if svc.calls.Load() != 0 {
		t.Fatalf("organized %d time(s) a copy that was never in the lock hold", svc.calls.Load())
	}
	if n := scanlock.Books.Held(); n != 0 {
		t.Fatalf("left %d scan lock(s) held", n)
	}
}

// The lock step held a library copy: that copy is in the hold, so the core
// acts on it as it stands and does not resolve again. A resolver that would
// answer differently now (the copy went away) is not consulted, and the row
// organized is the one whose scan lock is held.
func TestOrganizeBook_HeldLibraryCopyIsTheSubjectWithoutAnotherResolution(t *testing.T) {
	store := &organizeStoreFake{book: &database.Book{ID: "b1", FilePath: "/lib/Old/Old.m4b"}}
	svc := inPlaceSvc()
	h := handlers.NewOrganizeHandler(store, nil, svc, nil, false)
	var calls atomic.Int32
	h.SetLibraryCopyResolver(func(b *database.Book) (*database.Book, bool, *database.Book) {
		if calls.Add(1) > 2 {
			return nil, false, nil
		}
		cp := *b
		cp.ID = "s1"
		return &cp, true, nil
	})

	w := organizeRequest(h, "b1")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d, want 200: %s", w.Code, w.Body.String())
	}
	if calls.Load() != 2 {
		t.Fatalf("resolver ran %d time(s); want 2 (the lock step's, none in the core for a held copy)", calls.Load())
	}
	if got := svc.organized(); len(got) != 1 || got[0] != "s1" {
		t.Fatalf("organized %v; want exactly [s1], the copy the hold covers", got)
	}
}

// A queued organize that meets the same moving copy retries, as it does for a
// lock set that will not settle: the next lock step holds the copy, and the op
// organizes that row once and succeeds.
func TestRunQueuedOrganize_CopyAppearingAfterTheLockSetSettledRetriesThenOrganizes(t *testing.T) {
	defer handlers.SetOrganizeUnsettledRetryForTest(5, time.Millisecond)()
	store := &organizeStoreFake{book: &database.Book{ID: "b1", FilePath: "/lib/Old/Old.m4b"}}
	svc := inPlaceSvc()
	h := handlers.NewOrganizeHandler(store, nil, svc, nil, false)
	h.SetLibraryCopyResolver(copyAppearsAtCoreCheck(false))

	if err := h.RunQueuedOrganize(context.Background(), "b1", nil); err != nil {
		t.Fatalf("the retry should have organized the copy, got %v", err)
	}
	if got := svc.organized(); len(got) != 1 || got[0] != "s1" {
		t.Fatalf("organized %v; want exactly [s1]", got)
	}
	if n := scanlock.Books.Held(); n != 0 {
		t.Fatalf("left %d scan lock(s) held", n)
	}
}

// A core that finds a copy it does not hold on every attempt shares the lock
// set's retry budget: after organizeUnsettledRetries the op fails with the
// unsettled message and nothing was organized.
func TestRunQueuedOrganize_CoreUnsettledOnEveryAttemptFailsTheOp(t *testing.T) {
	defer handlers.SetOrganizeUnsettledRetryForTest(3, time.Millisecond)()
	store := &organizeStoreFake{book: &database.Book{ID: "b1", FilePath: "/lib/Old/Old.m4b"}}
	svc := inPlaceSvc()
	h := handlers.NewOrganizeHandler(store, nil, svc, nil, false)
	h.SetLibraryCopyResolver(copyAppearsAtCoreCheck(true))

	err := h.RunQueuedOrganize(context.Background(), "b1", nil)
	if !handlers.IsOrganizeSetUnsettled(err) {
		t.Fatalf("want the unsettled refusal, got %v", err)
	}
	if svc.calls.Load() != 0 {
		t.Fatalf("organized %d time(s) a copy that was never in the lock hold", svc.calls.Load())
	}
	if n := scanlock.Books.Held(); n != 0 {
		t.Fatalf("left %d scan lock(s) held", n)
	}
}

// The lock step saw the book as having no library copy (so the hold is just the
// book), and by the core's re-check the book's only copy has been merged away.
// That is a refusal, not an unsettled retry: the core answers the retired 409
// and writes nothing. The branch at the core's re-check, not the lock step's.
func TestOrganizeBook_CoreRecheckFindingARetiredCopyRefuses(t *testing.T) {
	store := &organizeStoreFake{book: &database.Book{ID: "b1", FilePath: "/lib/Old/Old.m4b"}}
	svc := inPlaceSvc()
	h := handlers.NewOrganizeHandler(store, nil, svc, nil, false)
	var calls atomic.Int32
	h.SetLibraryCopyResolver(func(b *database.Book) (*database.Book, bool, *database.Book) {
		if calls.Add(1) <= 2 {
			return nil, false, nil
		}
		return nil, false, &database.Book{ID: "s1-the-copy", FilePath: "/lib/Merged/Merged.m4b"}
	})

	w := organizeRequest(h, "b1")
	if w.Code != http.StatusConflict {
		t.Fatalf("status %d, want 409: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, organizer.CollisionLibraryCopyRetired) || !strings.Contains(body, "s1-the-copy") {
		t.Fatalf("409 is not the retired-copy refusal naming the copy: %s", body)
	}
	if strings.Contains(body, "kept changing") {
		t.Fatalf("a retired copy must not be answered as unsettled: %s", body)
	}
	if calls.Load() != 3 {
		t.Fatalf("resolver ran %d time(s); want 3 (two in the lock step, the core's re-check)", calls.Load())
	}
	if svc.calls.Load() != 0 {
		t.Fatalf("organized %d time(s)", svc.calls.Load())
	}
	if n := scanlock.Books.Held(); n != 0 {
		t.Fatalf("left %d scan lock(s) held", n)
	}
}

// The refusal the lock step computed under its hold stands: a book whose only
// library copy was retired at that moment is refused even if the core's own
// re-check no longer sees the retired copy, rather than organizing the
// original on the strength of a later, unheld answer.
func TestOrganizeBook_RetiredCopyAnsweredUnderTheHoldStands(t *testing.T) {
	store := &organizeStoreFake{book: &database.Book{ID: "b1", FilePath: "/lib/Old/Old.m4b"}}
	svc := inPlaceSvc()
	h := handlers.NewOrganizeHandler(store, nil, svc, nil, false)
	var calls atomic.Int32
	h.SetLibraryCopyResolver(func(b *database.Book) (*database.Book, bool, *database.Book) {
		if calls.Add(1) <= 2 {
			return nil, false, &database.Book{ID: "s1", FilePath: "/lib/Merged/Merged.m4b"}
		}
		return nil, false, nil
	})

	w := organizeRequest(h, "b1")
	if w.Code != http.StatusConflict {
		t.Fatalf("status %d, want 409: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), organizer.CollisionLibraryCopyRetired) {
		t.Fatalf("409 is not the retired-copy refusal: %s", w.Body.String())
	}
	// The lookup never returns a trashed row, so the text names only a merged
	// copy and agrees with `reason` ("was merged away").
	if strings.Contains(w.Body.String(), "marked for deletion") || !strings.Contains(w.Body.String(), "merged into another book, or into this one") {
		t.Fatalf("409 text does not describe a merged copy only: %s", w.Body.String())
	}
	if svc.calls.Load() != 0 {
		t.Fatalf("organized %d time(s)", svc.calls.Load())
	}
}

// The queued organize keeps only a string for its failure, so a retired-copy
// refusal names the copy in it (the 409 body's copy_book_id).
func TestRunQueuedOrganize_RetiredCopyFailureNamesTheCopy(t *testing.T) {
	store := &organizeStoreFake{book: &database.Book{ID: "b1", FilePath: "/lib/Old/Old.m4b"}}
	svc := inPlaceSvc()
	h := handlers.NewOrganizeHandler(store, nil, svc, nil, false)
	h.SetLibraryCopyResolver(func(b *database.Book) (*database.Book, bool, *database.Book) {
		return nil, false, &database.Book{ID: "s1-the-copy", FilePath: "/lib/Merged/Merged.m4b"}
	})

	err := h.RunQueuedOrganize(context.Background(), "b1", nil)
	if err == nil {
		t.Fatal("a retired-only library copy must fail the queued organize")
	}
	if !strings.Contains(err.Error(), "s1-the-copy") || !strings.Contains(err.Error(), "409") {
		t.Fatalf("failure does not name the copy and status: %v", err)
	}
	if svc.calls.Load() != 0 {
		t.Fatalf("organized %d time(s)", svc.calls.Load())
	}
}
