// file: internal/httputil/rangeserve_gin.go
// version: 1.0.0
// guid: cedd03da-7d9a-4619-9cf1-6568c8c357f3
// last-edited: 2026-07-29

package httputil

import "github.com/gin-gonic/gin"

// ServeFileWithRangeGin is a thin Gin adapter over ServeFileWithRange, for
// handlers that hold a *gin.Context rather than the raw
// http.ResponseWriter/*http.Request pair. See ServeFileWithRange for the
// full contract (path must be absolute, caller-authorized, and already
// confined to an allowed root) and behavior (Range/conditional-request
// support, error semantics).
func ServeFileWithRangeGin(c *gin.Context, path string, opts Options) error {
	return ServeFileWithRange(c.Writer, c.Request, path, opts)
}
