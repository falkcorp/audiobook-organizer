// file: core/ui/handler.go
// version: 0.1.0
// guid: 5e27a9c1-84f6-4d3b-9c0e-2a6f1d8b7e45
// last-edited: 2026-11-20

package ui

import (
	"bytes"
	"io/fs"
	"log/slog"
	"net/http"
	"path"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/metrics"
)

// AppOptions are the app's own contributions to the Handler the kernel
// builds. The app passes them as app.Options.UIOptions (06 §6.4.5), a
// compile-time value like Authenticators, and the kernel merges them into
// Options in P8 (FE3-4). The zero value adds nothing.
type AppOptions struct {
	// PageMiddleware wraps only index.html responses (page navigations), e.g.
	// ABO's Cloudflare service-token recovery (cf_recovery.go:36-60, from
	// internal/cfaccess). AppOptions is built in main before app.New, so a
	// func that needs state built during boot (ABO: the CF config and token
	// store, the Access team host) reads an atomic.Pointer the module fills in
	// its Start hook, and is a pass-through (or returns an empty list) until
	// then; P8 only wraps it (FE4-5).
	PageMiddleware func(http.Handler) http.Handler

	// CSP: ExtraDirectives are appended to the default policy (§7.6.2).
	// ConnectSrcFrom returns extra connect-src hosts (bare DNS names,
	// validated as csp.go does today); it is called for every index.html
	// response and must be cheap (ABO: its Access team host, read from an
	// atomic pointer as csp.go:129-137 does today, so a changed team host
	// takes effect on the next page load with no restart).
	ExtraDirectives []string
	ConnectSrcFrom  func() []string
}

// Options configure Handler.
type Options struct {
	AppOptions // from app.Options.UIOptions

	// NonSPA decides which unmatched paths answer 404 instead of index.html.
	// The kernel passes httpapi's Router.NonSPA() predicate (05-http-api
	// §4.10), which lower-cases the first segment and ignores trailing
	// slashes, so a case or trailing-slash variant of a probe (/health/,
	// /HEALTH, /metrics/, /livez/) never answers 200 HTML (FE2-6). nil = no
	// path is NonSPA.
	NonSPA func(r *http.Request) bool

	Config  *config.Handle[Settings] // core.ui.* (§7.15); hot keys swap the policy atomically. nil = DefaultSettings
	Metrics *metrics.Scope           // the core "ui" scope; nil records nothing
	Logger  *slog.Logger             // a failed response write is logged at Debug, as httpapi does; nil logs nothing
	// There is no redirect recorder and no redirect list (D13): a former
	// page path is served index.html like any other SPA path.
}

// Response kinds of <ns>_ui_responses_total (07 §7.16).
const (
	kindAsset       = "asset"
	kindAsset404    = "asset404"
	kindIndex       = "index"
	kindNonSPA404   = "nonspa404"
	kindMethod404   = "method404"
	kindPlaceholder = "placeholder"
)

// handler is the Handler. Everything but policy is fixed at construction.
type handler struct {
	b       Bundle
	nonSPA  func(*http.Request) bool
	cfg     *config.Handle[Settings]
	connect func() []string
	extra   []string
	met     *uiMetrics
	log     *slog.Logger
	index   http.Handler // serveIndex wrapped in PageMiddleware
	policy  atomic.Pointer[policy]
}

// policy is one computed index.html CSP, keyed by the inputs it came from:
// the settings value (the Handle publishes a new pointer on every change) and
// the extra hosts ConnectSrcFrom returned.
type policy struct {
	settings *Settings
	extra    []string
	header   string // header name: Content-Security-Policy or its -Report-Only twin
	value    string
}

// Handler serves the SPA (07 §7.6.1):
//   - hashed assets under /assets/: Cache-Control "public, max-age=31536000, immutable"
//   - a missing /assets/* file, or any unmatched path whose last segment has a
//     file extension: 404, never index.html (a stale tab's chunk 404 must not
//     become a MIME error)
//   - a method other than GET/HEAD: 405 problem+json (05-http-api §4.10)
//   - a NonSPA path (after normalisation): 404 problem+json, never HTML
//   - any other existing regular file of the bundle's fsys (Vite's public/
//     files at the dist root, e.g. /favicon.svg, which index.html
//     references): served with Cache-Control "no-cache" (FE3-14); this rule
//     is checked before the extension rule above. index.html itself is never
//     served by this rule: GET /index.html takes the index rule below
//     ("no-store", full CSP, through PageMiddleware), as HEAD runs its SPA
//     middleware before every NoRoute file (static_embed.go:59-66, FE4-11)
//   - every other unmatched GET/HEAD: index.html, "no-store", with the full
//     CSP of §7.6.2, through PageMiddleware
//   - zero Bundle: the API-only placeholder page (today's static_nonembed.go behaviour)
//
// The policy lives in an atomic.Pointer refreshed on config change; nothing
// else is mutable after construction. Goroutine-safe; no package state (R5).
func Handler(b Bundle, opts Options) http.Handler {
	h := &handler{
		b:       b,
		nonSPA:  opts.NonSPA,
		cfg:     opts.Config,
		connect: opts.ConnectSrcFrom,
		extra:   extraDirectives(opts.ExtraDirectives),
		met:     newUIMetrics(opts.Metrics, b),
		log:     opts.Logger,
	}
	h.index = http.HandlerFunc(h.serveIndex)
	if opts.PageMiddleware != nil {
		h.index = opts.PageMiddleware(h.index)
	}
	return h
}

// ServeHTTP applies the rules of Handler in order.
func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rec := &statusWriter{ResponseWriter: w}
	kind := h.serve(rec, r)
	h.met.record(r.Context(), kind, rec.code())
}

// serve answers r and returns the response kind for the counter.
func (h *handler) serve(w http.ResponseWriter, r *http.Request) string {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		httpapi.WriteError(w, r, &httpapi.Error{Status: http.StatusMethodNotAllowed, Code: "method_not_allowed"}, nil)
		return kindMethod404
	}
	if h.nonSPA != nil && h.nonSPA(r) {
		httpapi.WriteError(w, r, &httpapi.Error{Status: http.StatusNotFound, Code: "not_found"}, nil)
		return kindNonSPA404
	}
	if h.b.IsZero() {
		h.servePlaceholder(w, r)
		return kindPlaceholder
	}
	p := path.Clean("/" + r.URL.Path)
	if p == "/" || p == "/index.html" {
		h.index.ServeHTTP(w, r)
		return kindIndex
	}
	name := strings.TrimPrefix(p, "/")
	if p == "/assets" || strings.HasPrefix(p, "/assets/") {
		if h.serveFile(w, r, name, "public, max-age=31536000, immutable") {
			return kindAsset
		}
		httpapi.WriteError(w, r, &httpapi.Error{Status: http.StatusNotFound, Code: "not_found", Detail: "no such asset"}, nil)
		return kindAsset404
	}
	if h.serveFile(w, r, name, "no-cache") {
		return kindAsset
	}
	if path.Ext(path.Base(p)) != "" {
		httpapi.WriteError(w, r, &httpapi.Error{Status: http.StatusNotFound, Code: "not_found", Detail: "no such file"}, nil)
		return kindAsset404
	}
	h.index.ServeHTTP(w, r)
	return kindIndex
}

// serveFile serves the regular file name of the bundle with cacheControl and
// reports whether it existed. Directories and index.html are never served.
func (h *handler) serveFile(w http.ResponseWriter, r *http.Request, name, cacheControl string) bool {
	if name == "" || name == "index.html" || !fs.ValidPath(name) {
		return false
	}
	st, err := fs.Stat(h.b.fsys, name)
	if err != nil || !st.Mode().IsRegular() {
		return false
	}
	data, err := fs.ReadFile(h.b.fsys, name)
	if err != nil {
		return false
	}
	w.Header().Set("Cache-Control", cacheControl)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(data))
	return true
}

// serveIndex writes index.html with no-store and the current policy.
func (h *handler) serveIndex(w http.ResponseWriter, r *http.Request) {
	pol := h.currentPolicy()
	hdr := w.Header()
	hdr.Set("Content-Type", "text/html; charset=utf-8")
	hdr.Set("Cache-Control", "no-store")
	hdr.Set("X-Content-Type-Options", "nosniff")
	hdr.Set(pol.header, pol.value)
	hdr.Set("Reporting-Endpoints", cspReportGroup+`="`+CSPReportPath+`"`)
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		h.write(w, r, h.b.index)
	}
}

// write sends a page body; the status is already out, so a failure (the
// client went away) can only be logged.
func (h *handler) write(w http.ResponseWriter, r *http.Request, body []byte) {
	if _, err := w.Write(body); err != nil && h.log != nil {
		h.log.DebugContext(r.Context(), "ui: write response", "path", r.URL.Path, "error", err)
	}
}

// currentPolicy returns the policy for the current settings and extra hosts,
// rebuilding and swapping it only when either changed (§7.6.2 hot swap).
func (h *handler) currentPolicy() *policy {
	s := h.settings()
	var extra []string
	if h.connect != nil {
		extra = h.connect()
	}
	if cur := h.policy.Load(); cur != nil && cur.settings == s && slices.Equal(cur.extra, extra) {
		return cur
	}
	next := &policy{settings: s, extra: slices.Clone(extra), header: "Content-Security-Policy",
		value: buildPolicy(h.b.scripts, cspHosts(s.CSP.ConnectSrc, extra), h.extra)}
	if s.CSP.ReportOnly {
		next.header = "Content-Security-Policy-Report-Only"
	}
	h.policy.Store(next) // a lost race installs an equivalent policy
	return next
}

// settings is the current core.ui value, or the defaults without a Handle.
// The defaults pointer is cached in the policy, so it is built once.
func (h *handler) settings() *Settings {
	if h.cfg != nil {
		if s := h.cfg.Get(); s != nil {
			return s
		}
	}
	if cur := h.policy.Load(); cur != nil && h.cfg == nil {
		return cur.settings
	}
	d := DefaultSettings()
	return &d
}

// servePlaceholder is the API-only page of a build without an embedded UI.
func (h *handler) servePlaceholder(w http.ResponseWriter, r *http.Request) {
	hdr := w.Header()
	hdr.Set("Content-Type", "text/html; charset=utf-8")
	hdr.Set("Cache-Control", "no-store")
	hdr.Set("X-Content-Type-Options", "nosniff")
	hdr.Set("Content-Security-Policy", placeholderCSP)
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		h.write(w, r, []byte(placeholderHTML))
	}
}

// placeholderHTML is the page a build without the embed_frontend tag serves
// at every SPA path. It names no app and no route.
const placeholderHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>API server</title>
<style>body{font-family:system-ui,sans-serif;margin:3rem auto;max-width:40rem;padding:0 1rem;line-height:1.5}code{background:#eee;padding:0 .25rem}</style>
</head>
<body>
<h1>API server</h1>
<p>This build has no embedded web interface. The API is served under <code>/api/</code>.</p>
<p>Build with <code>-tags embed_frontend</code> after building the web package to embed the interface.</p>
</body>
</html>
`

// statusWriter records the status a response was written with.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (s *statusWriter) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusWriter) Write(p []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	return s.ResponseWriter.Write(p)
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (s *statusWriter) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// code is the written status, 200 when nothing was written.
func (s *statusWriter) code() int {
	if s.status == 0 {
		return http.StatusOK
	}
	return s.status
}

// BasicExemptAssets reports whether path is one of the SPA's static assets
// that the optional Basic gate lets through: HEAD's rule
// (internal/server/middleware/basicauth.go:40-46 @748fd787a: the /assets/
// prefix, /favicon.ico and the .js, .css, .png, .svg, .woff2 suffixes). The
// kernel passes it in P8 as httpapi.SPAOptions.BasicExemptAssets when it
// mounts Handler with Router.SPA (05-http-api §4.10), so stage 8's exempt set
// is unchanged when the books mount stops owning "/" at shell adoption (FE5-10). Pure; no
// state.
func BasicExemptAssets(path string) bool {
	return strings.HasPrefix(path, "/assets/") ||
		path == "/favicon.ico" ||
		strings.HasSuffix(path, ".js") ||
		strings.HasSuffix(path, ".css") ||
		strings.HasSuffix(path, ".png") ||
		strings.HasSuffix(path, ".svg") ||
		strings.HasSuffix(path, ".woff2")
}
