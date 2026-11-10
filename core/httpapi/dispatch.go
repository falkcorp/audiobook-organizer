// file: core/httpapi/dispatch.go
// version: 1.1.1
// guid: 6a494f53-6f15-4cca-bfe7-f575ea6913a4
// last-edited: 2026-11-10

package httpapi

import (
	"log/slog"
	"net/http"
	"path"
	"strings"

	"github.com/falkcorp/mediacore/internal/httpmw"
)

// dispatcher wraps the ServeMux with 05-http-api §4.3, §4.8 and §4.10: an
// unclean path never reaches the mux (no 301), nor does a path the mux would
// answer with its trailing-slash redirect (no 307), an unmatched path gets
// problem 404/405 (with the Allow header the mux computed) instead of the
// mux's plain-text body, an unmatched path under a legacy owner's claim gets
// 404 in the legacy shape whatever its method (HEAD's gin has no 405), a
// legacy mount's Redirects answer the unmatched paths they cover, and the "/"
// owner receives only what NonSPA lets through. Each answer it writes
// itself records its owner's policy first (stage 5's CSP): core's for a
// problem, the legacy mount's for a legacy-shape 404 or a legacy redirect.
type dispatcher struct {
	mux            *http.ServeMux
	root           http.Handler // the "/" owner, or nil
	nonSPA         func(*http.Request) bool
	legacyClaims   []claim
	legacyPolicies map[string]*httpmw.Policy // by mount owner
	// redirect is the legacy mounts' Redirects consult (legacy.go), nil when
	// no mount has one; redirectPolicy is that mount's policy.
	redirect       func(*http.Request) (location string, covered bool)
	redirectPolicy *httpmw.Policy
	chain          *httpmw.Chain
	log            *slog.Logger
}

func (d *dispatcher) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if !isClean(r.URL.Path) {
		// An unclean /api/ path is a problem 404; any other goes to the "/"
		// owner unchanged, whose own normalisation decides (§4.8).
		if d.root == nil || firstSegment(path.Clean("/"+r.URL.Path)) == "api" {
			d.chain.Owned(r, nil)
			writeProblem(ctx, w, r, Problem{Status: http.StatusNotFound, Code: "not_found"}, nil, d.log)
			return
		}
		d.root.ServeHTTP(w, r)
		return
	}
	h, pattern := d.mux.Handler(r)
	if pattern != "" && !slashRedirect(pattern, r) {
		// ServeHTTP, not h: only the mux's own dispatch sets r.PathValue.
		d.mux.ServeHTTP(w, r)
		return
	}
	// From here the request is unmatched: no pattern, or only the canonical
	// form with a trailing slash matched, which is a miss here as it is in
	// gin with RedirectTrailingSlash off.
	status, allow := http.StatusNotFound, ""
	if pattern == "" {
		status, allow = capture(h, r)
	}
	if status == http.StatusMethodNotAllowed && d.legacyOwner(r.URL.Path) == "" && (d.root == nil || d.nonSPA(r)) {
		d.chain.Owned(r, nil)
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
			d.chain.Owned(r, d.redirectPolicy)
			if loc != "" {
				http.Redirect(w, r, loc, http.StatusMovedPermanently)
				return
			}
			writeLegacyNotFound(ctx, w, d.log)
			return
		}
	}
	if d.root != nil && !d.nonSPA(r) {
		// Before the legacy claims: a SPAShared segment ("/login") is under
		// its owner's claim, yet a GET or HEAD no route matched is the
		// shell's page, as in HEAD's gin (06 §6.10, FE2-7).
		d.root.ServeHTTP(w, r)
		return
	}
	if owner := d.legacyOwner(r.URL.Path); owner != "" {
		d.chain.Owned(r, d.legacyPolicies[owner])
		writeLegacyNotFound(ctx, w, d.log)
		return
	}
	d.chain.Owned(r, nil)
	writeProblem(ctx, w, r, Problem{Status: http.StatusNotFound, Code: "not_found"}, nil, d.log)
}

// slashRedirect reports the mux's trailing-slash redirect: Handler returns
// the pattern that matched path+"/" (a subtree "/x/", an exact "/x/{$}" or a
// rest wildcard "/x/{rest...}", other wildcards allowed) for a path without
// the slash. A pattern ending in "/" matches a path without one only with
// more segments below it, so the same segment count without the final empty
// segment is the redirect. A final "{rest...}" may match the empty rest, so
// it counts as the "/" before it: GET /files is the redirect of
// "GET /files/{rest...}", never a match.
func slashRedirect(pattern string, r *http.Request) bool {
	_, pat := splitPattern(pattern)
	if i := strings.IndexByte(pat, '/'); i > 0 { // a host-qualified pattern
		pat = pat[i:]
	}
	pat = strings.TrimSuffix(pat, "{$}")
	if i := strings.LastIndexByte(pat, '/'); strings.HasPrefix(pat[i+1:], "{") && strings.HasSuffix(pat, "...}") {
		pat = pat[:i+1]
	}
	p := r.URL.EscapedPath()
	return strings.HasSuffix(pat, "/") && !strings.HasSuffix(p, "/") &&
		strings.Count(p, "/") == strings.Count(pat, "/")-1
}

// legacyOwner is the legacy mount owner whose claim covers p, "" for none.
func (d *dispatcher) legacyOwner(p string) string {
	for _, c := range d.legacyClaims {
		if p == strings.TrimSuffix(c.prefix, "/") || (strings.HasSuffix(c.prefix, "/") && strings.HasPrefix(p, c.prefix)) || p == c.prefix {
			return c.owner
		}
	}
	return ""
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
