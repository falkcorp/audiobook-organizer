// file: internal/server/fingerprint_diagnosis_handler.go
// version: 1.0.0
// guid: a1b2c3d4-e5f6-7890-abcd-ef1234567890

package server

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
)

// fingerprintFailureItem is a single row in the fingerprint-failures response.
type fingerprintFailureItem struct {
	BookID     string          `json:"book_id"`
	FileID     string          `json:"file_id"`
	FilePath   string          `json:"file_path"`
	Reason     string          `json:"reason"`
	Detail     string          `json:"detail"`
	Diagnostic json.RawMessage `json:"diagnostic,omitempty"`
	FailedAt   string          `json:"failed_at"`
}

// fingerprintFailuresResponse is the GET /api/v1/diagnostics/fingerprint-failures response.
type fingerprintFailuresResponse struct {
	Total    int64                    `json:"total"`
	ByReason map[string]int64         `json:"by_reason"`
	Files    []fingerprintFailureItem `json:"files"`
}

// getFingerprintFailures handles GET /api/v1/diagnostics/fingerprint-failures
//
//	?reason=<reason>   – filter by failure reason (optional)
//	?limit=<n>         – page size, default 50
//	?offset=<n>        – page offset, default 0
func (s *Server) getFingerprintFailures(c *gin.Context) {
	store := s.Ops()
	if store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "store not ready"})
		return
	}

	reason := c.Query("reason")
	limit := 50
	offset := 0
	if v := c.Query("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	if v := c.Query("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			offset = n
		}
	}

	files, total, err := store.GetFilesWithFingerprintFailures(reason, limit, offset)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Build by_reason tallies from this page (approximate — a full tally would
	// require a separate aggregation query; good enough for the UI summary).
	byReason := make(map[string]int64)
	items := make([]fingerprintFailureItem, 0, len(files))
	for _, f := range files {
		r := ""
		if f.FingerprintFailureReason != nil {
			r = *f.FingerprintFailureReason
		}
		byReason[r]++

		detail := ""
		if f.FingerprintFailureDetail != nil {
			detail = *f.FingerprintFailureDetail
		}
		var diagRaw json.RawMessage
		if f.FingerprintDiagnosticJSON != nil && *f.FingerprintDiagnosticJSON != "" {
			diagRaw = json.RawMessage(*f.FingerprintDiagnosticJSON)
		}
		failedAt := ""
		if f.FingerprintFailedAt != nil {
			failedAt = f.FingerprintFailedAt.UTC().Format("2006-01-02T15:04:05Z")
		}
		items = append(items, fingerprintFailureItem{
			BookID:     f.BookID,
			FileID:     f.ID,
			FilePath:   f.FilePath,
			Reason:     r,
			Detail:     detail,
			Diagnostic: diagRaw,
			FailedAt:   failedAt,
		})
	}

	c.JSON(http.StatusOK, fingerprintFailuresResponse{
		Total:    total,
		ByReason: byReason,
		Files:    items,
	})
}
