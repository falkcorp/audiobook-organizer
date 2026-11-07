// file: core/httpapi/dispatch.go
// version: 1.0.1
// guid: 6a494f53-6f15-4cca-bfe7-f575ea6913a4
// last-edited: 2026-11-07

package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"path"
	"strings"
	"sync/atomic"
)

// dispatcher wraps the ServeMux with 05-http-api §4.3, §4.8 and §4.10: an
// unclean path never reaches the mux (no 301), an unmatched path gets problem
// 404/405 (with the Allow header the mux computed) instead of the mux's
// plain-text body, an unmatched path under a legacy owner's claim gets 404 in
// the legacy shape, a legacy mount's Redirects answer the unmatched paths they
// cover, and the "/" owner receives only what NonSPA lets through.
type dispatcher struct {
	mux          *http.ServeMux
	root         http.Handler // the "/" owner, or nil
	nonSPA       func(*http.Request) bool
	legacyClaims []string
	// redirect is the legacy mounts' Redirects consult (legacy.go), nil when
	// no mount has one.
	redirect func(*http.Request) (location string, covered bool)
	log      *slog.Logger
}

func (d *dispatcher) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if !isClean(r.URL.Path) {
		// An unclean /api/ path is a problem 404; any other goes to the "/"
		// owner unchanged, whose own normalisation decides (§4.8).
		if d.root == nil || firstSegment(path.Clean("/"+r.URL.Path)) == "api" {
			writeProblem(ctx, w, r, Problem{Status: http.StatusNotFound, Code: "not_found"}, nil, d.log)
			return
		}
		d.root.ServeHTTP(w, r)
		return
	}
	h, pattern := d.mux.Handler(r)
	if pattern != "" {
		// ServeHTTP, not h: only the mux's own dispatch sets r.PathValue.
		d.mux.ServeHTTP(w, r)
		return
	}
	status, allow := capture(h, r)
	if status == http.StatusMethodNotAllowed && (d.root == nil || d.nonSPA(r)) {
		hdr := http.Header{}
		if allow != "" {
			hdr.Set("Allow", allow)
		}
		writeProblem(ctx, w, r, Problem{Status: http.StatusMethodNotAllowed, Code: "method_not_allowed"}, hdr, d.log)
		return
	}
	if d.redirect != nil {
		// A legacy redirect covers what no pattern and no native claim does
		// (05-http-api §4.9 item 3): a 301, or 404 in the legacy shape for a
		// request the rule excludes.
		if loc, covered := d.redirect(r); covered {
			if loc != "" {
				http.Redirect(w, r, loc, http.StatusMovedPermanently)
				return
			}
			writeLegacyNotFound(ctx, w, d.log)
			return
		}
	}
	if d.underLegacyClaim(r.URL.Path) {
		writeLegacyNotFound(ctx, w, d.log)
		return
	}
	if d.root == nil || d.nonSPA(r) {
		writeProblem(ctx, w, r, Problem{Status: http.StatusNotFound, Code: "not_found"}, nil, d.log)
		return
	}
	d.root.ServeHTTP(w, r)
}

func (d *dispatcher) underLegacyClaim(p string) bool {
	for _, c := range d.legacyClaims {
		if p == strings.TrimSuffix(c, "/") || (strings.HasSuffix(c, "/") && strings.HasPrefix(p, c)) || p == c {
			return true
		}
	}
	return false
}

// isClean reports a path ServeMux would not redirect: path.Clean leaves it
// unchanged, keeping one trailing slash.
func isClean(p string) bool {
	if p == "" || p[0] != '/' {
		return false
	}
	c := path.Clean(p)
	if strings.HasSuffix(p, "/") && c != "/" {
		c += "/"
	}
	return c == p
}

// capture runs the mux's own answer for an unmatched request into a
// header-only recorder and returns its status and Allow header; the body is
// discarded (§4.3).
func capture(h http.Handler, r *http.Request) (status int, allow string) {
	rec := &recorder{header: http.Header{}}
	h.ServeHTTP(rec, r)
	if rec.status == 0 {
		rec.status = http.StatusOK
	}
	return rec.status, rec.header.Get("Allow")
}

type recorder struct {
	header http.Header
	status int
}

func (r *recorder) Header() http.Header { return r.header }
func (r *recorder) WriteHeader(s int) {
	if r.status == 0 {
		r.status = s
	}
}
func (r *recorder) Write(b []byte) (int, error) {
	r.WriteHeader(http.StatusOK)
	return len(b), nil
}

// routeCarrier is the per-request carrier stage 2 owns (05-http-api §4.2
// stage 2): Build's root handler puts it in the context before the mux, each
// route wrapper writes its pattern into it, and the root reads it after the
// handler returned, so a stage in between that clones the request
// (r.WithContext) cannot lose it. R1-17 moves it into core/internal/httpmw
// with the real stage 2.
type routeCarrier struct{ route atomic.Pointer[string] }

func (c *routeCarrier) set(p string) { c.route.Store(&p) }

func (c *routeCarrier) get() string {
	if p := c.route.Load(); p != nil {
		return *p
	}
	return ""
}

type carrierKey struct{}

func withCarrier(ctx context.Context, c *routeCarrier) context.Context {
	return context.WithValue(ctx, carrierKey{}, c)
}

func carrierFrom(ctx context.Context) *routeCarrier {
	c, _ := ctx.Value(carrierKey{}).(*routeCarrier)
	return c
}
