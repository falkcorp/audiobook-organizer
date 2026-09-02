// file: internal/server/handlers/audiobooks/handler_applycap_test.go
// version: 1.0.0
// guid: 2e7b9c04-6d1f-4a83-95c2-f0d4a8e6b371
// last-edited: 2026-09-02

package audiobookshandler_test

import (
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/mock"

	"github.com/falkcorp/audiobook-organizer/internal/applycap"
	"github.com/falkcorp/audiobook-organizer/internal/batch"
	"github.com/falkcorp/audiobook-organizer/internal/config"
)

// The bulk apply cap (internal/applycap) on the two audiobooks batch routes.
// POST /audiobooks/batch is what the web UI's bulk edit actually calls, so a
// "select all matching" click reaches this gate before any other in the
// system; /batch-operations carried a fixed 10,000 ceiling that sat ABOVE the
// cap and includes hard_delete. Bogus/known-good pairs: cap+1 is refused with
// the batch service never called (mockery fails the test on an unexpected
// call); exactly cap reaches it once.

func withBulkApplyCap(t *testing.T, n int) {
	t.Helper()
	prev := config.AppConfig.BulkApplyMaxItems
	config.AppConfig.BulkApplyMaxItems = n
	t.Cleanup(func() { config.AppConfig.BulkApplyMaxItems = prev })
}

func nIDs(n int) []string {
	out := make([]string, 0, n)
	for i := range n {
		out = append(out, "b"+strconv.Itoa(i))
	}
	return out
}

func nOps(n int) []map[string]any {
	out := make([]map[string]any, 0, n)
	for i := range n {
		out = append(out, map[string]any{"id": "b" + strconv.Itoa(i), "op": "update", "updates": map[string]any{"title": "T"}})
	}
	return out
}

func requireCapRefusal(t *testing.T, code int, body string, requested, cap int) {
	t.Helper()
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("want 422, got %d: %s", code, body)
	}
	for _, want := range []string{"BULK_APPLY_CAP_EXCEEDED", strconv.Itoa(requested) + " items", "cap is " + strconv.Itoa(cap), "bulk_apply_max_items"} {
		if !strings.Contains(body, want) {
			t.Fatalf("response missing %q: %s", want, body)
		}
	}
}

func TestBatchUpdateAudiobooks_RefusesOverTheBulkApplyCap(t *testing.T) {
	withBulkApplyCap(t, 3)
	h, _ := newHandler(t)
	c, w := newCtx("POST", "/audiobooks/batch", map[string]any{"ids": nIDs(4), "updates": map[string]any{"title": "T"}}, nil)
	h.BatchUpdateAudiobooks(c)
	requireCapRefusal(t, w.Code, w.Body.String(), 4, 3)
}

func TestBatchUpdateAudiobooks_AllowsExactlyTheBulkApplyCap(t *testing.T) {
	withBulkApplyCap(t, 3)
	h, d := newHandler(t)
	d.batchSvc.EXPECT().UpdateAudiobooks(mock.Anything).Return(&batch.BatchResponse{}).Once()
	c, w := newCtx("POST", "/audiobooks/batch", map[string]any{"ids": nIDs(3), "updates": map[string]any{"title": "T"}}, nil)
	h.BatchUpdateAudiobooks(c)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestBatchUpdateAudiobooks_ZeroConfigMeansDefaultNotUnlimited(t *testing.T) {
	withBulkApplyCap(t, 0)
	h, _ := newHandler(t)
	c, w := newCtx("POST", "/audiobooks/batch", map[string]any{"ids": nIDs(applycap.Default + 1), "updates": map[string]any{"title": "T"}}, nil)
	h.BatchUpdateAudiobooks(c)
	requireCapRefusal(t, w.Code, w.Body.String(), applycap.Default+1, applycap.Default)
}

func TestBatchOperations_RefusesOverTheBulkApplyCap(t *testing.T) {
	withBulkApplyCap(t, 3)
	h, _ := newHandler(t)
	c, w := newCtx("POST", "/audiobooks/batch-operations", map[string]any{"operations": nOps(4)}, nil)
	h.BatchOperations(c)
	requireCapRefusal(t, w.Code, w.Body.String(), 4, 3)
}

func TestBatchOperations_AllowsExactlyTheBulkApplyCap(t *testing.T) {
	withBulkApplyCap(t, 3)
	h, d := newHandler(t)
	d.batchSvc.EXPECT().ExecuteOperations(mock.Anything).Return(&batch.BatchResponse{}).Once()
	c, w := newCtx("POST", "/audiobooks/batch-operations", map[string]any{"operations": nOps(3)}, nil)
	h.BatchOperations(c)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
}
