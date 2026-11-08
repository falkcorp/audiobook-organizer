// file: core/internal/httpmw/headers.go
// version: 1.0.2
// guid: f94c1cdb-e7e5-4055-89fe-51f4c971d0ac
// last-edited: 2026-11-08

package httpmw

import (
	"net/http"
	"strings"
)

// Stage 5's fixed values. The four browser headers and HSTS are HEAD's
// (server_middleware.go setSecurityHeaders); defaultCSP is 05-http-api
// §4.2's policy for a response no owner supplied one for.
const (
	// reportGroup is the Reporting-Endpoints group stage 5 names: 05-http-api
	// §4.2's csp="<owner's report URL>". An owner's CSP must name the same
	// group in report-to; ABO's does (internal/server cspReportGroup,
	// TestCSPReportGroupMatchesStage5).
	reportGroup       = "csp"
	defaultCSP        = "default-src 'none'; frame-ancestors 'self'"
	permissionsPolicy = "camera=(), microphone=(), geolocation=(), payment=(), usb=()"
	hstsValue         = "max-age=31536000; includeSubDomains"
	buildHeader       = "X-App-Build"
)

// headers is stage 5 (05-http-api §4.2): the browser security headers and
// X-App-Build before the handler runs (a handler may override any of them,
// as in HEAD), then, when the final status goes out, the owner's CSP if
// the handler set none, with the owner's Reporting-Endpoints.
func (c *Chain) headers(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.setHeaders(w.Header(), r)
		next.ServeHTTP(newHookWriter(w, r, func(int) { c.finishHeaders(w.Header(), r) }), r)
	})
}

// setHeaders writes the headers stage 5 sets before the handler. HSTS only
// over TLS or when a trusted proxy says the client's leg was https: HEAD
// believed X-Forwarded-Proto from any peer.
func (c *Chain) setHeaders(h http.Header, r *http.Request) {
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "SAMEORIGIN")
	h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
	h.Set("X-XSS-Protection", "0")
	h.Set("Permissions-Policy", permissionsPolicy)
	build := c.deps.BuildID
	if build == "" {
		build = "dev"
	}
	h.Set(buildHeader, build)
	if r.TLS != nil || (c.fromTrustedProxy(r) && strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")), "https")) {
		h.Set("Strict-Transport-Security", hstsValue)
	}
}

// finishHeaders writes the owner's CSP when the handler set none, and the
// owner's Reporting-Endpoints (FE-4, FE2-1).
func (c *Chain) finishHeaders(h http.Header, r *http.Request) {
	var p *Policy
	if st := From(r.Context()); st != nil {
		p = st.Policy()
	}
	if p == nil {
		// Answered before the router picked an owner: the path's owner.
		p = c.policyOf(r)
	}
	if p != nil && p.CSPReport != "" {
		h.Set("Reporting-Endpoints", reportGroup+`="`+p.CSPReport+`"`)
	}
	if h.Get("Content-Security-Policy") != "" {
		return
	}
	csp := defaultCSP
	if p != nil && p.CSP != nil {
		if v := p.CSP(r); v != "" {
			csp = v
		}
	}
	h.Set("Content-Security-Policy", csp)
}

// policyOf is Deps.PolicyOf, nil when unset.
func (c *Chain) policyOf(r *http.Request) *Policy {
	if c.deps.PolicyOf == nil {
		return nil
	}
	return c.deps.PolicyOf(r)
}

// fromTrustedProxy reports a TCP peer in the trusted proxies.
func (c *Chain) fromTrustedProxy(r *http.Request) bool {
	a, ok := parseTrustAddr(r.RemoteAddr)
	return ok && c.current().trusted.contains(a)
}

// ownAnswer writes an answer a stage outside stage 5 gives itself (stage 3's
// 500): stage 5's headers first, so it carries them like every response.
func (c *Chain) ownAnswer(w http.ResponseWriter, r *http.Request, status int, code string, header http.Header) {
	c.setHeaders(w.Header(), r)
	c.finishHeaders(w.Header(), r)
	c.deps.Problem(w, r, status, code, header)
}
