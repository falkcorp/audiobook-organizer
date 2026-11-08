// file: core/internal/httpmw/crossorigin.go
// version: 1.0.0
// guid: 03f287e3-84eb-434a-81b9-03db6734d39c
// last-edited: 2026-11-08

package httpmw

import (
	"net/http"
	"strings"
	"sync/atomic"
)

// crossOrigin is stage 7 (05-http-api §4.2, SEC-10, FE-5):
// net/http.CrossOriginProtection on unsafe methods, trusted origins =
// core.http.cors.allowed_origins. No credential type is exempt (FE2-23): a
// non-browser client passes because it sends neither Sec-Fetch-Site nor
// Origin. When the TCP peer is a trusted proxy, its X-Forwarded-Host stands
// in for Host in the Origin comparison (a proxy that rewrites Host would
// otherwise refuse every UI POST); from any other peer it is ignored. A
// refusal is 403 cross_origin_rejected, written to the access log as a
// security_event until audit exists (SEC2-20).
func (c *Chain) crossOrigin(next http.Handler) http.Handler {
	var warned atomic.Bool
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cp := c.current()
		check := r
		if xfh := strings.TrimSpace(r.Header.Get("X-Forwarded-Host")); xfh != "" {
			if a, ok := parseTrustAddr(r.RemoteAddr); ok && cp.trusted.contains(a) {
				if i := strings.IndexByte(xfh, ','); i >= 0 {
					xfh = strings.TrimSpace(xfh[:i])
				}
				check = r.WithContext(r.Context())
				check.Host = xfh
			}
		}
		if err := cp.cop.Check(check); err != nil {
			owner := "core"
			if c.deps.OwnerOf != nil {
				owner = c.deps.OwnerOf(r)
			}
			securityEvent(r, "cross_origin_rejected")
			c.inst.crossOrigin(r.Context(), owner)
			if warned.CompareAndSwap(false, true) {
				c.deps.Log.Warn("httpapi: refused an unsafe cross-origin request; if the UI sits behind a proxy that rewrites Host and sends no X-Forwarded-Host, list its public origin in core.http.cors.allowed_origins",
					"origin", r.Header.Get("Origin"), "sec_fetch_site", r.Header.Get("Sec-Fetch-Site"))
			}
			c.deps.Problem(w, r, http.StatusForbidden, "cross_origin_rejected", nil)
			return
		}
		next.ServeHTTP(w, r)
	})
}
