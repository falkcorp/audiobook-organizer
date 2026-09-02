// file: internal/server/handlers/dedup/handler_applycap_test.go
// version: 1.0.0
// guid: 7a4f1d92-3c6e-4b58-8e07-d5b2a9c1f684
// last-edited: 2026-09-02

package deduphandler_test

import (
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/mock"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
)

// The bulk apply cap (internal/applycap) on /dedup/candidates/bulk-merge. An
// empty body resolves to EVERY pending book candidate and MergeBooks each —
// the "filter matched everything" shape the cap exists for, and a merge is
// the hardest write in the system to undo. The gate counts what the filter
// actually resolved: cap+1 candidates are refused with MergeBooks never called
// (mockery fails on an unexpected call); exactly cap are merged.

func withBulkApplyCap(t *testing.T, n int) {
	t.Helper()
	prev := config.AppConfig.BulkApplyMaxItems
	config.AppConfig.BulkApplyMaxItems = n
	t.Cleanup(func() { config.AppConfig.BulkApplyMaxItems = prev })
}

func insertNCandidates(t *testing.T, d testDeps, n int) {
	t.Helper()
	for i := range n {
		insertCandidate(t, d.es, "book-a"+strconv.Itoa(i), "book-b"+strconv.Itoa(i))
	}
}

func TestBulkMergeDedupCandidates_RefusesOverTheBulkApplyCap(t *testing.T) {
	withBulkApplyCap(t, 3)
	h, d := newHandler(t)
	insertNCandidates(t, d, 4)
	w := doReq(t, h.BulkMergeDedupCandidates, http.MethodPost, "/api/v1/dedup/candidates/bulk-merge", map[string]any{}, nil)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("want 422, got %d: %s", w.Code, w.Body.String())
	}
	for _, want := range []string{"BULK_APPLY_CAP_EXCEEDED", "4 items", "cap is 3", "bulk_apply_max_items"} {
		if !strings.Contains(w.Body.String(), want) {
			t.Fatalf("response missing %q: %s", want, w.Body.String())
		}
	}
}

func TestBulkMergeDedupCandidates_AllowsExactlyTheBulkApplyCap(t *testing.T) {
	withBulkApplyCap(t, 3)
	h, d := newHandler(t)
	allowLabelCaptureReads(d)
	insertNCandidates(t, d, 3)
	d.merge.EXPECT().MergeBooks(mock.Anything, "").Return(&merge.Result{PrimaryID: "x"}, nil).Times(3)
	w := doReq(t, h.BulkMergeDedupCandidates, http.MethodPost, "/api/v1/dedup/candidates/bulk-merge", map[string]any{}, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
}
