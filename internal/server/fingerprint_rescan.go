// file: internal/server/fingerprint_rescan.go
// version: 1.4.0
// guid: e8cf338d-2d99-47ae-a4b8-d31d8772d955
// last-edited: 2026-05-06

package server

import (
	"net/http"

	"github.com/falkcorp/audiobook-organizer/internal/fingerprint"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/gin-gonic/gin"
)

// FingerprintRescanRequest controls the scope of a manual fingerprint rescan.
type FingerprintRescanRequest struct {
	// Scope selects which book_files to (re)fingerprint.
	//   "missing" (default) — only files where acoustid_seg0 is empty
	//   "all"               — every audio file across every book
	//   "books"             — every audio file across the books listed in BookIDs
	Scope string `json:"scope,omitempty"`

	// BookIDs is required when Scope == "books"; ignored otherwise.
	BookIDs []string `json:"book_ids,omitempty"`

	// Force, when true, ignores any existing acoustid_seg0..seg6 values and
	// recomputes them. Default false (existing fingerprints are kept).
	Force bool `json:"force,omitempty"`
}

const (
	scopeMissing = "missing"
	scopeAll     = "all"
	scopeBooks   = "books"
)

// triggerFingerprintRescan handles POST /api/v1/dedup/fingerprint-rescan.
// Delegates to the UOS registry (acoustid.fingerprint-rescan op) since UOS-09.
func (s *Server) triggerFingerprintRescan(c *gin.Context) {
	// Validate request body first so bad input always wins 400 over
	// environment-state errors (503/500). Keeps the contract stable across
	// CI environments where ffmpeg/fpcalc may or may not be installed.
	var req FingerprintRescanRequest
	if err := c.ShouldBindJSON(&req); err != nil && err.Error() != "EOF" {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}

	scope := req.Scope
	if scope == "" {
		scope = scopeMissing
	}
	switch scope {
	case scopeMissing, scopeAll:
		// ok
	case scopeBooks:
		if len(req.BookIDs) == 0 {
			httputil.RespondWithBadRequest(c, "book_ids is required when scope is \"books\"")
			return
		}
	default:
		httputil.RespondWithBadRequest(c, "scope must be one of: missing, all, books")
		return
	}

	if !fingerprint.Available() {
		httputil.RespondWithServiceUnavailable(c, "no fingerprint backend (fpcalc / ffmpeg) found")
		return
	}

	if s.opRegistry == nil {
		httputil.RespondWithInternalError(c, "operation registry not initialized")
		return
	}

	// Convert request to UOS operation params. Pass the MAP directly —
	// Registry.EnqueueOp does its own json.Marshal. If we pre-marshaled
	// to []byte here, EnqueueOp would marshal the byte slice itself,
	// producing a base64 string that fails Unmarshal on the worker
	// side ("failed to unmarshal params").
	params := map[string]interface{}{
		"scope":    scope,
		"book_ids": req.BookIDs,
		"force":    req.Force,
	}

	opID, err := s.opRegistry.EnqueueOp(c.Request.Context(), "acoustid.fingerprint-rescan", params)
	if err != nil {
		httputil.InternalError(c, "failed to enqueue fingerprint-rescan", err)
		return
	}
	httputil.RespondWithSuccess(c, http.StatusAccepted, map[string]string{"op_id": opID})
}
