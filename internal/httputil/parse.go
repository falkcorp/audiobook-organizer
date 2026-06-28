// file: internal/httputil/parse.go
// version: 1.1.0
// guid: c3d4e5f6-a7b8-9012-cdef-123456789012
// last-edited: 2026-06-28

package httputil

import (
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

// ParseQueryInt returns a query param as int, falling back to defaultValue if
// absent or unparseable.
func ParseQueryInt(c *gin.Context, key string, defaultValue int) int {
	v := c.DefaultQuery(key, "")
	if v == "" {
		return defaultValue
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return defaultValue
	}
	return n
}

// ParseQueryIntPtr returns a query param as *int, returning nil if absent or
// unparseable.
func ParseQueryIntPtr(c *gin.Context, key string) *int {
	v := c.Query(key)
	if v == "" {
		return nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return nil
	}
	return &n
}

// ParseQueryBool returns a query param as bool, falling back to defaultValue.
func ParseQueryBool(c *gin.Context, key string, defaultValue bool) bool {
	v := c.DefaultQuery(key, "")
	if v == "" {
		return defaultValue
	}
	return strings.ToLower(v) == "true" || v == "1"
}

// ParseQueryBoolPtr returns a query param as *bool, returning nil if absent.
func ParseQueryBoolPtr(c *gin.Context, key string) *bool {
	v := c.Query(key)
	if v == "" {
		return nil
	}
	val := strings.ToLower(v) == "true" || v == "1"
	return &val
}

// ParseQueryString returns a query param as string, or empty string if absent.
func ParseQueryString(c *gin.Context, key string) string {
	return c.Query(key)
}

// ParsePaginationParams parses limit, offset, and search from query params.
// Defaults: limit=50, max=1000, offset=0.
//
// The cap exists so a malformed/hostile ?limit= can't force a huge projection,
// but it must be high enough to honor the library "items per page" selector
// (max option 1000). Below 1000 the page would silently return fewer rows than
// the user asked for.
func ParsePaginationParams(c *gin.Context) PaginationParams {
	limit := ParseQueryInt(c, "limit", 50)
	offset := ParseQueryInt(c, "offset", 0)
	search := c.Query("search")
	if limit < 1 {
		limit = 50
	}
	if limit > 1000 {
		limit = 1000
	}
	if offset < 0 {
		offset = 0
	}
	return PaginationParams{Limit: limit, Offset: offset, Search: search}
}

// HandleBindError responds with an appropriate error if err is non-nil and
// returns true, so callers can do: if httputil.HandleBindError(c, err) { return }
func HandleBindError(c *gin.Context, err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	if strings.Contains(msg, "required") || strings.Contains(msg, "binding") {
		RespondWithValidationError(c, "request body", msg)
	} else {
		RespondWithBadRequest(c, "invalid request: "+msg)
	}
	return true
}

// EnsureNotNil converts a nil slice/map to an empty []any so JSON marshals
// as [] instead of null.
func EnsureNotNil(slice any) any {
	if slice == nil {
		return []any{}
	}
	return slice
}
