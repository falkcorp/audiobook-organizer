// file: internal/server/handlers/dedup/malformed_body_test.go
// version: 1.0.0
// guid: 8c4f0b62-19ae-4d37-95e1-7f2a6d0c3b84
// last-edited: 2026-08-11

package deduphandler_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/mock"
)

// Wave 2 of the silent-failure sweep. BulkMergeDedupCandidates discarded the
// error from ShouldBindJSON. Every field in its body NARROWS what gets merged,
// so a malformed body zeroed all of them, the handler's own defaults filled in
// status=pending / entity_type=book, and the query went out with Limit: 100000 —
// turning "merge this one narrow layer" into "bulk-merge every pending book
// candidate in the library". Merges are the hardest operation here to undo.
//
// The fix must distinguish two cases that ShouldBindJSON reports differently:
//   - EMPTY body      → io.EOF → still valid, this endpoint's bulk default
//   - MALFORMED body  → some other error → 400
//
// Both directions are tested. A fix that simply rejected everything would pass a
// malformed-body test while breaking every legitimate no-body caller.

// w2rawReq posts a RAW body. The package's existing doReq helper marshals its
// input through encoding/json, which by construction cannot produce the
// malformed payloads these tests need.
func w2rawReq(t *testing.T, fn gin.HandlerFunc, url, rawBody string, withContentType bool) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	req := httptest.NewRequest(http.MethodPost, url, bytes.NewReader([]byte(rawBody)))
	if withContentType {
		req.Header.Set("Content-Type", "application/json")
	}
	c.Request = req
	fn(c)
	return w
}

// w2malformedBodies are payloads a real client produces by accident: a trailing
// comma, a truncated upload, a bool sent as a string, an array where an object
// belongs.
func w2malformedBodies() map[string]string {
	return map[string]string{
		"trailing_comma":  `{"layer":"exact",}`,
		"truncated":       `{"layer":"exact"`,
		"wrong_type":      `{"min_similarity":"0.9"}`,
		"array_not_objec": `["layer"]`,
		"garbage":         `not json at all`,
	}
}

func TestBulkMerge_MalformedBodyIsRefused(t *testing.T) {
	for name, body := range w2malformedBodies() {
		t.Run(name, func(t *testing.T) {
			h, _ := newHandler(t)
			w := w2rawReq(t, h.BulkMergeDedupCandidates, "/api/v1/dedup/candidates/bulk-merge", body, true)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status=%d want 400 — an unreadable filter must NOT widen to every pending candidate; body=%s",
					w.Code, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), "invalid request body") {
				t.Fatalf("400 returned but not for the body parse: %s", w.Body.String())
			}
		})
	}
}

// TestBulkMerge_EmptyBodyStillAccepted is the guard against "fixing" this by
// rejecting everything. An absent body is this endpoint's documented bulk
// behaviour and must keep working.
func TestBulkMerge_EmptyBodyStillAccepted(t *testing.T) {
	h, _ := newHandler(t)
	w := w2rawReq(t, h.BulkMergeDedupCandidates, "/api/v1/dedup/candidates/bulk-merge", "", false)
	if w.Code == http.StatusBadRequest && strings.Contains(w.Body.String(), "invalid request body") {
		t.Fatalf("empty body was rejected at the parse; io.EOF must stay valid. body=%s", w.Body.String())
	}
}

// TestRescoreAndPurge_KeepTheirDiscards pins a DELIBERATE non-change.
//
// RescoreDedupCandidates and PurgeAcoustIDConflicts also discard their bind
// error, and Wave 2 leaves them alone: their only field is an `apply` gate whose
// zero value is dry-run, i.e. the SAFE direction — the exact inverse of
// BulkMerge. Blanket-converting every discard in this file would have turned a
// correct fail-safe into a 400 for callers who send no body.
//
// This test exists so that non-change is recorded as a decision rather than an
// oversight: if someone later "finishes the job" on this file, this test tells
// them why these two were skipped.
// Both assertions below use a body that ASKS TO APPLY but is malformed
// (`{"apply":true,}` — trailing comma). The mock expectation is the assertion:
// it only matches if the engine was called with the DRY-RUN value, proving the
// discarded parse error left the safe default in place rather than honouring
// the "apply" the caller appeared to request.
func TestRescoreAndPurge_KeepTheirDiscards(t *testing.T) {
	const applyButMalformed = `{"apply":true,}`

	t.Run("rescore_falls_back_to_dry_run", func(t *testing.T) {
		h, d := newHandler(t)
		// Rescore(ctx, apply) — apply MUST be false.
		d.engine.EXPECT().Rescore(mock.Anything, false).
			Return(dedup.RescoreResult{Inspected: 1}, nil).Once()

		w := w2rawReq(t, h.RescoreDedupCandidates, "/api/v1/dedup/rescore", applyButMalformed, true)
		if w.Code == http.StatusBadRequest && strings.Contains(w.Body.String(), "invalid request body") {
			t.Fatalf("rescore now 400s on a malformed body. That may be an improvement, but it is a "+
				"BEHAVIOUR CHANGE Wave 2 deliberately did not make — the zero value here is "+
				"apply=false (dry run), the safe direction. Update this test consciously. body=%s",
				w.Body.String())
		}
	})

	t.Run("purge_acoustid_falls_back_to_dry_run", func(t *testing.T) {
		h, d := newHandler(t)
		// ReevaluateAcoustIDConflicts(ctx, dryRun) is called with !apply, so
		// dryRun MUST be true.
		d.engine.EXPECT().ReevaluateAcoustIDConflicts(mock.Anything, true).
			Return(&dedup.AcoustIDConflictResult{}, nil).Once()

		w := w2rawReq(t, h.PurgeAcoustIDConflicts, "/api/v1/dedup/purge-acoustid", applyButMalformed, true)
		if w.Code == http.StatusBadRequest && strings.Contains(w.Body.String(), "invalid request body") {
			t.Fatalf("purge_acoustid now 400s on a malformed body — see the rescue note above. body=%s",
				w.Body.String())
		}
	})
}
