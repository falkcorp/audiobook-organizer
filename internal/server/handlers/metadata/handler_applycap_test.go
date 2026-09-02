// file: internal/server/handlers/metadata/handler_applycap_test.go
// version: 1.0.0
// guid: b41e7c93-2a58-4f0d-8c6b-3e9a1d7f5028
// last-edited: 2026-09-02

package metadatahandler_test

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/applycap"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/stretchr/testify/mock"
)

// The bulk apply cap (internal/applycap) on /metadata/batch-update. This path
// writes straight through store.UpdateBook with no candidate gating, so it is
// the cheapest way to rewrite the whole library by mistake. A bogus/known-good
// pair: cap+1 updates are refused with ZERO store reads; exactly cap updates
// reach the store once per update.

func withBulkApplyCap(t *testing.T, n int) {
	t.Helper()
	prev := config.AppConfig.BulkApplyMaxItems
	config.AppConfig.BulkApplyMaxItems = n
	t.Cleanup(func() { config.AppConfig.BulkApplyMaxItems = prev })
}

func batchUpdates(n int) []map[string]any {
	out := make([]map[string]any, 0, n)
	for i := range n {
		out = append(out, map[string]any{
			"book_id": "b" + strconv.Itoa(i),
			"updates": map[string]any{"title": "T"},
		})
	}
	return out
}

func TestBatchUpdateMetadata_RefusesOverTheBulkApplyCap(t *testing.T) {
	withBulkApplyCap(t, 3)
	// No store expectations: mockery fails the test on ANY unexpected call, so
	// this doubles as the "zero writes happened" assertion.
	h, _ := newHandler(t)
	w := doReq(h.BatchUpdateMetadata, http.MethodPost, "/metadata/batch-update",
		map[string]any{"updates": batchUpdates(4), "validate": false}, nil)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("want 422, got %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, want := range []string{"BULK_APPLY_CAP_EXCEEDED", "4 items", "cap is 3", "bulk_apply_max_items"} {
		if !strings.Contains(body, want) {
			t.Fatalf("response missing %q: %s", want, body)
		}
	}
}

func TestBatchUpdateMetadata_AllowsExactlyTheBulkApplyCap(t *testing.T) {
	withBulkApplyCap(t, 3)
	h, d := newHandler(t)
	// Each update reaches the store exactly once. Returning not-found keeps the
	// per-item work short; the count is what proves the gate let them through.
	d.store.EXPECT().GetBookByID(mock.Anything).Return(nil, errors.New("not found")).Times(3)
	w := doReq(h.BatchUpdateMetadata, http.MethodPost, "/metadata/batch-update",
		map[string]any{"updates": batchUpdates(3), "validate": false}, nil)
	// 206: every item errored (not-found) but the batch itself ran. The mock's
	// Times(3) is the real assertion — the store was reached once per update.
	if w.Code != http.StatusPartialContent {
		t.Fatalf("want 206, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"total_count":3`) {
		t.Fatalf("want total_count 3: %s", w.Body.String())
	}
}

func TestBatchUpdateMetadata_ZeroConfigMeansDefaultNotUnlimited(t *testing.T) {
	withBulkApplyCap(t, 0)
	h, _ := newHandler(t)
	w := doReq(h.BatchUpdateMetadata, http.MethodPost, "/metadata/batch-update",
		map[string]any{"updates": batchUpdates(applycap.Default + 1), "validate": false}, nil)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("want 422, got %d: %s", w.Code, w.Body.String())
	}
}
