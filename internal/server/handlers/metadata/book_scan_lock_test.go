// file: internal/server/handlers/metadata/book_scan_lock_test.go
// version: 1.0.0
// guid: 85df29b0-0d44-40e8-bbab-726ab6928927
// last-edited: 2026-09-30

package metadatahandler_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	metadatahandler "github.com/falkcorp/audiobook-organizer/internal/server/handlers/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/scanlock"
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

			w := doReq(tc.run(h), http.MethodPost, tc.target, tc.body, id)
			if w.Code != http.StatusAccepted {
				t.Fatalf("status = %d, want 202; body=%s", w.Code, w.Body.String())
			}
			if strings.Contains(w.Body.String(), "SCAN_RUNNING") || strings.Contains(w.Body.String(), "try again") {
				t.Fatalf("the response still carries a scan warning: %s", w.Body.String())
			}
			var resp map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Fatal(err)
			}
			if resp["queued"] != true || resp["operation_id"] != "op-queued-1" || resp["book"] == nil {
				t.Fatalf("202 body missing queued/operation_id/book: %v", resp)
			}
			if len(q.calls) != 1 || q.calls[0].Kind != tc.kind || q.calls[0].BookID != "b1" {
				t.Fatalf("enqueued %+v, want one %s for b1", q.calls, tc.kind)
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
	go func() { time.Sleep(50 * time.Millisecond); scan.Release() }()

	w := doReq(h.WriteBackAudiobookMetadata, http.MethodPost, "/api/v1/audiobooks/b1/write-back",
		map[string]any{"segment_ids": []string{"s1"}}, gin.Params{{Key: "id", Value: "b1"}})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if len(q.calls) != 0 {
		t.Fatalf("queued %d applies; the scanner released the book within the bound", len(q.calls))
	}
	if n := scanlock.Books.Held(); n != 0 {
		t.Fatalf("the request left %d scan lock(s) held", n)
	}
}
