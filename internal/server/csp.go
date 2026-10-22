// file: internal/server/csp.go
// version: 1.0.0
// guid: 95fa1ac8-51ae-43a4-8c85-5055eef7c9dd
// last-edited: 2026-10-21

package server

import (
	"crypto/sha256"
	"encoding/base64"
	"regexp"
	"strings"
	"sync/atomic"
)

// Content-Security-Policy for the single-page app.
//
// What the built app needs, measured on web/dist (2026-10-21):
//
//   - scripts: the bundle under /assets (script-src 'self') plus ONE inline
//     script in index.html, the pre-paint colour-scheme setter. It is allowed
//     by hash, never 'unsafe-inline'. The hash is computed from the index.html
//     the binary actually serves (setSPAInlineScripts), so editing that script
//     cannot leave the policy stale and nothing is written down twice.
//   - styles: MUI/emotion inject <style> elements and style attributes at
//     runtime, so style-src needs 'unsafe-inline'. That is the one place this
//     policy is weaker than the audit's ideal; it cannot be removed without
//     giving emotion a nonce, which needs the document rendered per request.
//   - images: same-origin covers, data: and blob: previews, and https: because
//     the metadata search dialogs show provider cover URLs (Open Library,
//     Google Books, Audible CDNs) straight from the candidate rows.
//   - connect: same-origin fetch and the SSE stream. No third-party call is
//     made from the browser.
//   - nothing else: no fonts from a CDN, no workers, no frames, no plugins.
//
// The policy is sent on every response, not only the HTML shell: a browser
// ignores it on JSON, and it covers an HTML file served as a download. A
// handler that sets its own Content-Security-Policy (the OIDC browser hop, the
// Cloudflare recovery page) wins, because the middleware sets the header before
// the handler runs and header Set replaces.
const cspBase = "default-src 'self'; " +
	"style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data: blob: https:; " +
	"font-src 'self' data:; " +
	"media-src 'self' blob:; " +
	"connect-src 'self'; " +
	"object-src 'none'; " +
	"base-uri 'none'; " +
	"form-action 'self'; " +
	"frame-ancestors 'self'"

// permissionsPolicy turns off the browser features the app never uses, so
// injected script cannot ask for them. Clipboard write is left alone: the UI
// copies values to the clipboard.
const permissionsPolicy = "camera=(), microphone=(), geolocation=(), payment=(), usb=()"

// cspHeader is the full policy string served; replaced by setSPAInlineScripts.
var cspHeader atomic.Pointer[string]

func init() {
	s := buildCSP(nil)
	cspHeader.Store(&s)
}

// buildCSP is cspBase with script-src allowing 'self' and each hash source.
func buildCSP(hashSources []string) string {
	script := "script-src 'self'"
	for _, h := range hashSources {
		script += " " + h
	}
	return script + "; " + cspBase
}

// contentSecurityPolicy returns the policy for the next response.
func contentSecurityPolicy() string { return *cspHeader.Load() }

var (
	scriptElementRE = regexp.MustCompile(`(?is)<script\b([^>]*)>(.*?)</script\s*>`)
	scriptSrcAttrRE = regexp.MustCompile(`(?i)(^|\s)src\s*=`)
)

// inlineScriptHashSources returns a CSP hash source ('sha256-...') for every
// inline <script> element in an HTML document: one with no src attribute and
// non-empty text. The hash covers the exact bytes between the tags.
func inlineScriptHashSources(html []byte) []string {
	var out []string
	for _, m := range scriptElementRE.FindAllSubmatch(html, -1) {
		if scriptSrcAttrRE.Match(m[1]) || strings.TrimSpace(string(m[2])) == "" {
			continue
		}
		sum := sha256.Sum256(m[2])
		out = append(out, "'sha256-"+base64.StdEncoding.EncodeToString(sum[:])+"'")
	}
	return out
}

// setSPAInlineScripts installs the policy for the index.html the binary serves
// and returns how many inline scripts it allowed. It is called once from the
// embedded build's static setup; the plain build serves no script, so it keeps
// the hash-free default.
func setSPAInlineScripts(indexHTML []byte) int {
	hashes := inlineScriptHashSources(indexHTML)
	s := buildCSP(hashes)
	cspHeader.Store(&s)
	return len(hashes)
}
