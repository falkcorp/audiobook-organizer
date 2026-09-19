// file: internal/ai/openai_batch_find_test.go
// version: 1.1.0
// guid: b1f418a4-6d9e-4311-aa14-db3c3a38b350
// last-edited: 2026-09-19

package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// batchesServer serves GET /batches in two pages. Page 1 holds recent batches
// of other types and scans; page 2 holds the one owned by scan 7 / full_scan.
func batchesServer(t *testing.T, pages *atomic.Int32) *httptest.Server {
	t.Helper()
	now := time.Now().Unix() // every batch is newer than since, so paging continues
	b := func(id, typ, scan, phase string) map[string]any {
		md := map[string]string{"project": "audiobook-organizer", "type": typ}
		if scan != "" {
			md[BatchMetaScanID] = scan
			md[BatchMetaScanPhase] = phase
		}
		return map[string]any{"id": id, "object": "batch", "status": "in_progress", "created_at": now, "metadata": md,
			"endpoint": "/v1/chat/completions", "input_file_id": "f", "completion_window": "24h"}
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pages.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("after") == "" {
			_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "has_more": true, "last_id": "b2", "data": []any{
				b("b1", "aijobs", "", ""),
				b("b2", "author_dedup", "8", "full_scan"), // another scan
			}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "has_more": false, "data": []any{
			b("b3", "author_review", "7", "groups_scan"), // same scan, other phase
			b("b4", "author_dedup", "7", "full_scan"),
		}})
	}))
}

func TestFindBatchByMetadataMatchesAllKeysAcrossPages(t *testing.T) {
	var pages atomic.Int32
	srv := batchesServer(t, &pages)
	defer srv.Close()
	p := NewOpenAIParserWithBaseURL(nil, "sk-test", srv.URL, "m", true)
	since := time.Now().Add(-time.Hour)

	id, found, err := p.FindBatchByMetadata(context.Background(), map[string]string{BatchMetaScanID: "7", BatchMetaScanPhase: "full_scan"}, since)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "b4", id)
	require.Equal(t, int32(2), pages.Load(), "the owner batch was on page 2")

	_, found, err = p.FindBatchByMetadata(context.Background(), map[string]string{BatchMetaScanID: "9", BatchMetaScanPhase: "full_scan"}, since)
	require.NoError(t, err)
	require.False(t, found, "no batch carries scan 9's metadata")

	// Same scan id and phase but a different nonce is another scan that reused
	// the id (a rebuilt store, or a second install on the same OpenAI project).
	_, found, err = p.FindBatchByMetadata(context.Background(), map[string]string{BatchMetaScanID: "7", BatchMetaScanPhase: "full_scan", BatchMetaScanNonce: "other"}, since)
	require.NoError(t, err)
	require.False(t, found)
}

// TestFindBatchByMetadataTruncatedListingIsUnknown: when the page cap stops the
// walk before reaching since, "not found" would be a guess — and a caller that
// trusts it pays for a second batch. It must be an error instead.
func TestFindBatchByMetadataTruncatedListingIsUnknown(t *testing.T) {
	old := maxBatchListPages
	maxBatchListPages = 1
	t.Cleanup(func() { maxBatchListPages = old })

	var pages atomic.Int32
	srv := batchesServer(t, &pages)
	defer srv.Close()
	p := NewOpenAIParserWithBaseURL(nil, "sk-test", srv.URL, "m", true)

	_, found, err := p.FindBatchByMetadata(context.Background(), map[string]string{BatchMetaScanID: "7", BatchMetaScanPhase: "full_scan"}, time.Now().Add(-time.Hour))
	require.ErrorIs(t, err, ErrBatchListTruncated)
	require.False(t, found)

	batches, err := p.ListProjectBatches(context.Background(), time.Now().Add(-time.Hour))
	require.ErrorIs(t, err, ErrBatchListTruncated, "a truncated listing must say so")
	require.Len(t, batches, 2, "partial results are still returned")
}
