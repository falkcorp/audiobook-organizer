// file: internal/server/handlers/metadata/scan_standdown_test.go
// version: 1.0.1
// guid: 7f2c9e04-b6d3-4a81-9e5f-0c4a8d2b61e7
// last-edited: 2026-09-12

package metadatahandler_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// scanRunningGate refuses every request the way the registry does while a
// library.scan is running.
type scanRunningGate struct{ calls int }

func (g *scanRunningGate) TryAcquireScanStandDown(holder, _ string) (func(), error) {
	g.calls++
	if !strings.HasPrefix(holder, "http:") {
		panic("request holder ids must be minted by RequestScanStandDownHolderID")
	}
	return nil, opsregistry.ErrScanRunning
}

func (g *scanRunningGate) RenewScanStandDown(string) bool { return false }

// Every inline metadata-writing handler returns 409 at once while a scan is
// running. The strict mocks (no expectations set) fail the test if any of them
// touches the store or the fetch service, so "nothing was written" is enforced,
// not assumed.
func TestMetadataWriteHandlers_409WhileLibraryScanRuns(t *testing.T) {
	id := gin.Params{{Key: "id", Value: "b1"}}
	cases := []struct {
		name   string
		run    func(h handlerSet) gin.HandlerFunc
		target string
		body   any
		params gin.Params
	}{
		{"apply-metadata", func(h handlerSet) gin.HandlerFunc { return h.ApplyAudiobookMetadata },
			"/api/v1/audiobooks/b1/apply-metadata", map[string]any{"candidate": map[string]any{"title": "T"}}, id},
		{"fetch-metadata", func(h handlerSet) gin.HandlerFunc { return h.FetchAudiobookMetadata },
			"/api/v1/audiobooks/b1/fetch-metadata", nil, id},
		{"write-back", func(h handlerSet) gin.HandlerFunc { return h.WriteBackAudiobookMetadata },
			"/api/v1/audiobooks/b1/write-back", map[string]any{}, id},
		{"batch-update", func(h handlerSet) gin.HandlerFunc { return h.BatchUpdateMetadata },
			"/api/v1/metadata/batch-update", map[string]any{"updates": []any{}}, nil},
		{"bulk-fetch", func(h handlerSet) gin.HandlerFunc { return h.BulkFetchMetadata },
			"/api/v1/metadata/bulk-fetch", map[string]any{"book_ids": []string{"b1"}}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, _ := newHandler(t)
			g := &scanRunningGate{}
			h.SetScanStandDownGate(g)
			w := doReq(tc.run(h), http.MethodPost, tc.target, tc.body, tc.params)
			if w.Code != http.StatusConflict {
				t.Fatalf("status = %d, want 409; body=%s", w.Code, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), "a library scan is running; try again when it finishes") {
				t.Fatalf("409 body does not carry the user-facing message: %s", w.Body.String())
			}
			if g.calls != 1 {
				t.Fatalf("gate consulted %d times, want 1", g.calls)
			}
		})
	}
}

// handlerSet is the exported surface the table needs.
type handlerSet interface {
	ApplyAudiobookMetadata(*gin.Context)
	FetchAudiobookMetadata(*gin.Context)
	WriteBackAudiobookMetadata(*gin.Context)
	BatchUpdateMetadata(*gin.Context)
	BulkFetchMetadata(*gin.Context)
}
