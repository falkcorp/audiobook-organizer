// file: core/internal/httpmw/bodylimit.go
// version: 1.0.0
// guid: 35498cef-13e7-462b-aaee-f8fbb440b1f3
// last-edited: 2026-11-08

package httpmw

import (
	"mime"
	"net/http"
)

// bodyLimit is stage 11 (05-http-api §4.2): a route's body is read through
// http.MaxBytesReader at its limit, and a declared Content-Length over it is
// refused before the handler with 413 body_too_large. MaxBody 0 is the
// configured class default: core.http.body.upload_max_bytes for a
// multipart or octet-stream body, json_max_bytes otherwise. A negative
// MaxBody is no core limit: a native raw stream the handler bounds itself,
// and in R1 the legacy mount, whose gin groups keep HEAD's per-group limits
// (the 20 GB Open Library upload, the 8 MiB fingerprint worker cap).
func (c *Chain) bodyLimit(spec RouteSpec, next http.Handler) http.Handler {
	if spec.MaxBody < 0 {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		limit := spec.MaxBody
		if limit == 0 {
			cp := c.current()
			limit = cp.jsonMax
			if uploadBody(r) {
				limit = cp.upMax
			}
		}
		if r.ContentLength > limit {
			c.deps.Problem(w, r, http.StatusRequestEntityTooLarge, "body_too_large", nil)
			return
		}
		if r.Body != nil && r.Body != http.NoBody {
			r.Body = http.MaxBytesReader(w, r.Body, limit)
		}
		next.ServeHTTP(w, r)
	})
}

// uploadBody reports a multipart or octet-stream request body.
func uploadBody(r *http.Request) bool {
	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		return false
	}
	return mt == "application/octet-stream" || mt == "multipart/form-data"
}
