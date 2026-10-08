// file: internal/server/handlers/metadata/book_scan_lock_test.go
// version: 1.8.0
// guid: 85df29b0-0d44-40e8-bbab-726ab6928927
// last-edited: 2026-10-07

package metadatahandler_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/mock"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/internal/scanlock"
	metadatahandler "github.com/falkcorp/audiobook-organizer/internal/server/handlers/metadata"
)

type fakeEnqueuer struct {
	mu    sync.Mutex
	calls []metadatahandler.QueuedApply
}

func (f *fakeEnqueuer) EnqueueApplyWhenScanned(_ context.Context, q metadatahandler.QueuedApply) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, q)
	return "op-queued-1", nil
}

// While the library scan holds a book past the bound, every single-book
// write handler answers 202 "queued" and hands the work to the queued op. It
// never answers 409, never carries a scan warning, and never touches the
// fetch service (strict mocks) for the book the scanner holds.
func TestSingleBookHandlers_QueueInsteadOf409WhenTheScannerHoldsTheBook(t *testing.T) {
	defer metadatahandler.SetRequestBookLockWaitForTest(30 * time.Millisecond)()
	id := gin.Params{{Key: "id", Value: "b1"}}
	cases := []struct {
		name   string
		run    func(h *metadatahandler.Handler) gin.HandlerFunc
		target string
		body   any
		kind   string
	}{
		{"apply-metadata", func(h *metadatahandler.Handler) gin.HandlerFunc { return h.ApplyAudiobookMetadata },
			"/api/v1/audiobooks/b1/apply-metadata", map[string]any{"candidate": map[string]any{"title": "T"}}, metadatahandler.QueuedApplyCandidate},
		{"fetch-metadata", func(h *metadatahandler.Handler) gin.HandlerFunc { return h.FetchAudiobookMetadata },
			"/api/v1/audiobooks/b1/fetch-metadata", nil, metadatahandler.QueuedFetch},
		{"write-back", func(h *metadatahandler.Handler) gin.HandlerFunc { return h.WriteBackAudiobookMetadata },
			"/api/v1/audiobooks/b1/write-back", map[string]any{"segment_ids": []string{"s1"}}, metadatahandler.QueuedWriteBack},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, d := newHandler(t)
			q := &fakeEnqueuer{}
			h.SetQueuedApplyEnqueuer(q)
			d.store.EXPECT().GetBookByID("b1").Return(&database.Book{ID: "b1", Title: "Current"}, nil).Maybe()

			scan, err := scanlock.Books.LockSet(context.Background(), []string{"b1"})
			if err != nil {
				t.Fatal(err)
			}
			defer scan.Release()

			if tc.kind == metadatahandler.QueuedApplyCandidate {
				// The enqueue records the book's newest edit, for the queued
				// run's later-edit check.
				d.mfs.EXPECT().ApplyEditMark("b1").Return(int64(42), nil)
			}

			w := doReq(tc.run(h), http.MethodPost, tc.target, tc.body, id)
			if w.Code != http.StatusAccepted {
				t.Fatalf("status = %d, want 202; body=%s", w.Code, w.Body.String())
			}
			if strings.Contains(w.Body.String(), "SCAN_RUNNING") || strings.Contains(w.Body.String(), "try again") {
				t.Fatalf("the response still carries a scan warning: %s", w.Body.String())
			}
			var env struct {
				Data map[string]any `json:"data"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
				t.Fatal(err)
			}
			resp := env.Data
			if resp["queued"] != true || resp["operation_id"] != "op-queued-1" || resp["book"] == nil {
				t.Fatalf("202 body missing queued/operation_id/book: %v", resp)
			}
			if len(q.calls) != 1 || q.calls[0].Kind != tc.kind || q.calls[0].BookID != "b1" {
				t.Fatalf("enqueued %+v, want one %s for b1", q.calls, tc.kind)
			}
			if tc.kind == metadatahandler.QueuedApplyCandidate {
				if c := q.calls[0]; c.EditMark != 42 || !strings.HasPrefix(c.ApplyBatchID, "apply-queued-") {
					t.Fatalf("queued without its edit mark / batch id: mark=%d batch=%q", c.EditMark, c.ApplyBatchID)
				}
			}
		})
	}
}

// A book the scanner releases within the bound is applied in the request, not
// queued: the handler simply waited its turn.
func TestSingleBookHandlers_WaitForTheScannerWithinTheBound(t *testing.T) {
	defer metadatahandler.SetRequestBookLockWaitForTest(5 * time.Second)()
	h, d := newHandler(t)
	q := &fakeEnqueuer{}
	h.SetQueuedApplyEnqueuer(q)
	d.store.EXPECT().GetBookByID("b1").Return(&database.Book{ID: "b1"}, nil)
	d.mfs.EXPECT().WriteBackMetadataForBook("b1", [][]string{{"s1"}}).Return(1, nil)

	scan, err := scanlock.Books.LockSet(context.Background(), []string{"b1"})
	if err != nil {
		t.Fatal(err)
	}
	// released closes once the scanner side's Release has RETURNED. Release
	// hands the token over before it drops its own table entry, so the request
	// can acquire, finish and release while the scanner goroutine is still
	// between the two; the table only empties once both are done. Asserting
	// Held() before that is a race (CI saw 1), not a leak.
	released := make(chan struct{})
	go func() {
		defer close(released)
		time.Sleep(50 * time.Millisecond)
		scan.Release()
	}()

	w := doReq(h.WriteBackAudiobookMetadata, http.MethodPost, "/api/v1/audiobooks/b1/write-back",
		map[string]any{"segment_ids": []string{"s1"}}, gin.Params{{Key: "id", Value: "b1"}})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if len(q.calls) != 0 {
		t.Fatalf("queued %d applies; the scanner released the book within the bound", len(q.calls))
	}
	<-released
	if n := scanlock.Books.Held(); n != 0 {
		t.Fatalf("the request left %d scan lock(s) held", n)
	}
}

// The queued op waits for exactly as long as the scanner holds the book,
// beating so its progress watchdog sees a live wait, and runs the same core
// the request would have the moment the book frees. The strict mock proves
// nothing touches the book's files while the scanner holds it.
func TestRunQueuedApply_WaitsForTheBookThenRuns(t *testing.T) {
	defer metadatahandler.SetQueuedWaitBeatForTest(10 * time.Millisecond)()
	h, d := newHandler(t)

	scan, err := scanlock.Books.LockSet(context.Background(), []string{"b1"})
	if err != nil {
		t.Fatal(err)
	}
	beat := make(chan struct{})
	var once sync.Once
	done := make(chan error, 1)
	go func() {
		done <- h.RunQueuedApply(context.Background(), metadatahandler.QueuedApply{
			Kind: metadatahandler.QueuedWriteBack, BookID: "b1", SegmentIDs: []string{"s1"},
		}, func(string) { once.Do(func() { close(beat) }) })
	}()
	<-beat // at least one beat while the scanner held the book
	select {
	case err := <-done:
		t.Fatalf("RunQueuedApply returned (%v) while the scanner held the book", err)
	default:
	}

	d.store.EXPECT().GetBookByID("b1").Return(&database.Book{ID: "b1"}, nil)
	d.mfs.EXPECT().WriteBackMetadataForBook("b1", [][]string{{"s1"}}).Return(1, nil)
	scan.Release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunQueuedApply: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RunQueuedApply never ran after the scanner released the book")
	}
	if n := scanlock.Books.Held(); n != 0 {
		t.Fatalf("the queued apply left %d scan lock(s) held", n)
	}
}

func queuedCandidate() metadatahandler.QueuedApply {
	return metadatahandler.QueuedApply{
		Kind: metadatahandler.QueuedApplyCandidate, BookID: "b1",
		Candidate: &metafetch.MetadataCandidate{Title: "Cand"},
		EditMark:  42, ApplyBatchID: "apply-queued-own",
	}
}

// (b) Someone applied another candidate after the apply was queued. The run
// refuses (terminal), names the edit, and never calls the apply (strict mock).
func TestRunQueuedApply_CandidateRefusesALaterEdit(t *testing.T) {
	h, d := newHandler(t)
	d.mfs.EXPECT().ApplyEditsSince("b1", int64(42), "apply-queued-own").Return(
		metafetch.QueuedApplyEdits{Others: []string{"title (fetched, audible)"}}, nil)
	err := h.RunQueuedApply(context.Background(), queuedCandidate(), nil)
	if !errors.Is(err, metadatahandler.ErrQueuedApplyStale) || !strings.Contains(err.Error(), "title (fetched, audible)") ||
		!strings.Contains(err.Error(), "Apply the change again") {
		t.Fatalf("want a later-edit refusal naming the edit, got %v", err)
	}
	if n := scanlock.Books.Held(); n != 0 {
		t.Fatalf("left %d scan lock(s) held", n)
	}
}

// An unreadable history refuses: failing closed beats overwriting an edit.
func TestRunQueuedApply_CandidateUnreadableHistoryRefuses(t *testing.T) {
	h, d := newHandler(t)
	d.mfs.EXPECT().ApplyEditsSince("b1", int64(42), "apply-queued-own").Return(metafetch.QueuedApplyEdits{}, errors.New("disk"))
	if err := h.RunQueuedApply(context.Background(), queuedCandidate(), nil); !errors.Is(err, metadatahandler.ErrQueuedApplyStale) {
		t.Fatalf("want a refusal, got %v", err)
	}
}

// (c) The op re-runs after a restart that came after its apply landed: its own
// history rows are there, so it completes "already applied" without calling
// the apply again -- no second history batch.
func TestRunQueuedApply_CandidateReRunAfterTheApplyIsAlreadyApplied(t *testing.T) {
	h, d := newHandler(t)
	d.mfs.EXPECT().ApplyEditsSince("b1", int64(42), "apply-queued-own").Return(metafetch.QueuedApplyEdits{OwnApplied: true}, nil)
	if err := h.RunQueuedApply(context.Background(), queuedCandidate(), nil); !errors.Is(err, metadatahandler.ErrQueuedAlreadyApplied) {
		t.Fatalf("want already-applied, got %v", err)
	}
}

// (a) No edit since it was queued -- the scanner's merge in between writes no
// history -- so it applies, with its fixed batch id. A pool that drops the
// file job (shutdown) does not fail the apply or leave anything held (LOW 7).
func TestRunQueuedApply_CandidateWithNoLaterEditApplies(t *testing.T) {
	h, d := newHandler(t)
	d.mfs.EXPECT().ApplyEditsSince("b1", int64(42), "apply-queued-own").Return(metafetch.QueuedApplyEdits{}, nil)
	d.store.EXPECT().GetBookByID("b1").Return(&database.Book{ID: "b1"}, nil)
	d.mfs.EXPECT().RenamePreflight("b1", mock.Anything, mock.Anything).Return(nil)
	d.mfs.EXPECT().ApplyMetadataCandidateWithOptions("b1", mock.Anything, mock.Anything,
		metafetch.ApplyOptions{BatchID: "apply-queued-own"}).Return(&metafetch.FetchMetadataResponse{}, nil)
	d.pool.EXPECT().Submit("b1", mock.Anything).Return(false)
	if err := h.RunQueuedApply(context.Background(), queuedCandidate(), nil); err != nil {
		t.Fatalf("RunQueuedApply: %v", err)
	}
	if n := scanlock.Books.Held(); n != 0 {
		t.Fatalf("left %d scan lock(s) held", n)
	}
}

// batch-update writes only the database, so it takes no scan lock and never
// waits for, or refuses because of, a scan: it answers at once even for a
// book the scanner holds. (The scanner's merge keeps the fields it wrote.)
func TestBatchUpdate_NeverWaitsOrRefusesForAScan(t *testing.T) {
	h, d := newHandler(t)
	d.store.EXPECT().GetBookByID("b1").Return(nil, errors.New("not found")).Maybe()

	scan, err := scanlock.Books.LockSet(context.Background(), []string{"b1"})
	if err != nil {
		t.Fatal(err)
	}
	defer scan.Release()

	start := time.Now()
	w := doReq(h.BatchUpdateMetadata, http.MethodPost, "/api/v1/metadata/batch-update",
		map[string]any{"updates": []map[string]any{{"book_id": "b1", "updates": map[string]any{"title": "T"}}}}, nil)
	if w.Code == http.StatusConflict || strings.Contains(w.Body.String(), "SCAN_RUNNING") {
		t.Fatalf("batch-update refused for a scan: %d %s", w.Code, w.Body.String())
	}
	if el := time.Since(start); el > 2*time.Second {
		t.Fatalf("batch-update waited %s for the scanner", el)
	}
}
