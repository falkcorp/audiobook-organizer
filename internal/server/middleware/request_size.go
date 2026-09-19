// file: internal/server/middleware/request_size.go
// version: 1.3.0
// guid: f2129ae7-cf11-4888-bd4f-ab4b578f8f18
// last-edited: 2026-09-19

package middleware

import (
	"errors"
	"net/http"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/fingerprint/workerapi"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/gin-gonic/gin"
)

func methodHasBody(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch:
		return true
	default:
		return false
	}
}

// fingerprintWorkerPathPrefix is the remote fingerprint worker API. Its
// result batches (up to 50 files x 3 base64 prints) can outgrow the 1 MB JSON
// default, so the class is pinned at workerapi.MaxBodyBytes (8 MB) whatever
// json_body_limit_mb says; the handler enforces the same cap again.
const fingerprintWorkerPathPrefix = "/api/v1/fingerprint/worker/"

func selectBodyLimit(path string, jsonLimitBytes, uploadLimitBytes int64) int64 {
	if strings.HasPrefix(path, fingerprintWorkerPathPrefix) {
		return workerapi.MaxBodyBytes
	}
	if strings.Contains(path, "/import/") || strings.Contains(path, "/backup/") {
		return uploadLimitBytes
	}
	// OL dump uploads can be multi-GB — no practical limit
	if strings.Contains(path, "/openlibrary/upload") {
		return 20 * 1024 * 1024 * 1024 // 20GB
	}
	return jsonLimitBytes
}

// MaxRequestBodySize enforces request body limits by route class.
func MaxRequestBodySize(jsonLimitBytes, uploadLimitBytes int64) gin.HandlerFunc {
	if jsonLimitBytes < 1 {
		jsonLimitBytes = 1 << 20
	}
	if uploadLimitBytes < jsonLimitBytes {
		uploadLimitBytes = jsonLimitBytes
	}

	return func(c *gin.Context) {
		if !methodHasBody(c.Request.Method) {
			c.Next()
			return
		}

		limit := selectBodyLimit(c.Request.URL.Path, jsonLimitBytes, uploadLimitBytes)
		if c.Request.ContentLength > limit && c.Request.ContentLength > 0 {
			httputil.RespondWithError(c, http.StatusRequestEntityTooLarge, "request body too large", "REQUEST_TOO_LARGE")
			c.Abort()
			return
		}

		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, limit)
		c.Next()

		// Detect MaxBytesError from oversized chunked/HTTP/2 bodies that skip the
		// early Content-Length check (MED-1). Map to a clear 413 error response.
		if c.IsAborted() {
			return // Already handled by handler or other middleware
		}

		// Check if the context has an error from MaxBytesReader
		// (This happens during request reading in c.Next() or subsequent handlers)
		for _, err := range c.Errors {
			if _, ok := errors.AsType[*http.MaxBytesError](err.Err); ok {
				httputil.RespondWithError(c, http.StatusRequestEntityTooLarge, "request body too large", "REQUEST_TOO_LARGE")
				c.Abort()
				return
			}
		}
	}
}
