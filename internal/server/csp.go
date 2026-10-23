// file: internal/server/csp.go
// version: 1.2.0
// guid: 95fa1ac8-51ae-43a4-8c85-5055eef7c9dd
// last-edited: 2026-10-21

package server

import (
	"crypto/sha256"
	"encoding/base64"
	"regexp"
	"strings"
	"sync/atomic"

	"github.com/falkcorp/audiobook-organizer/internal/config"
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
//   - connect: same-origin fetch and the SSE stream, plus the one host of the
//     configured Cloudflare Access team (cf_access_team_domain, read per
//     response so a change takes effect without a restart; nothing is added
//     when it is unset or is not a plain hostname). When a Cloudflare Access
//     session expires the edge answers an /api fetch with a 302 to the team's
//     cloudflareaccess.com login; apiFetch recognises the expiry by seeing that
//     redirect followed to a cross-origin URL (assertNotAuthRedirect, signal 1).
//     With connect-src 'self' alone the browser would block the redirect and the
//     user would see a generic network error instead of "session expired". A
//     wildcard over cloudflareaccess.com would also allow script injected into a
//     page to send data to ANY Cloudflare team, an attacker's included, so only
//     this deployment's own team host is named. No other third-party call is
//     made from the browser.
//   - reports: violations are sent to cspReportPath (report-uri, and report-to
//     through the Reporting-Endpoints header) and logged, rate limited, by
//     csp_report.go. The policy stays enforcing.
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
	"object-src 'none'; " +
	"base-uri 'none'; " +
	"form-action 'self'; " +
	"frame-ancestors 'self'; " +
	"report-uri " + cspReportPath + "; " +
	"report-to " + cspReportGroup

// permissionsPolicy turns off the browser features the app never uses, so
// injected script cannot ask for them. Clipboard write is left alone: the UI
// copies values to the clipboard.
const permissionsPolicy = "camera=(), microphone=(), geolocation=(), payment=(), usb=()"

// cspState is the policy for one set of inline-script hashes and one Access team.
type cspState struct {
	hashes []string
	team   string // validated host, "" for none
	policy string
}

// cspHeader is the policy served; replaced by setSPAInlineScripts and rebuilt by
// contentSecurityPolicy when the configured team changes.
var cspHeader atomic.Pointer[cspState]

func init() {
	cspHeader.Store(newCSPState(nil, ""))
}

func newCSPState(hashes []string, team string) *cspState {
	return &cspState{hashes: hashes, team: team, policy: buildCSP(hashes, team)}
}

// buildCSP is cspBase with script-src allowing 'self' and each hash source, and
// connect-src allowing 'self' and the Access team host when there is one.
func buildCSP(hashSources []string, team string) string {
	script := "script-src 'self'"
	for _, h := range hashSources {
		script += " " + h
	}
	connect := "connect-src 'self'"
	if team != "" {
		connect += " https://" + team
	}
	return script + "; " + connect + "; " + cspBase
}

// accessTeamHostRE is what a team domain must look like to go into the policy:
// a bare DNS name. Anything else (a scheme, a path, a space, a semicolon, a
// wildcard) is dropped, so a malformed setting can neither widen nor corrupt
// the header.
var accessTeamHostRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)

// cspAccessTeam returns the configured Access team host for the policy: trimmed,
// lowercased, with a leading https:// and trailing slash tolerated, "" when unset
// or not a plain hostname.
func cspAccessTeam(raw string) string {
	h := strings.ToLower(strings.TrimSpace(raw))
	h = strings.TrimPrefix(h, "https://")
	h = strings.TrimSuffix(h, "/")
	if !accessTeamHostRE.MatchString(h) {
		return ""
	}
	return h
}

// contentSecurityPolicy returns the policy for the next response. The team host
// is read from config every time (one read-locked string) and the string is
// rebuilt only when it changed.
func contentSecurityPolicy() string {
	st := cspHeader.Load()
	team := cspAccessTeam(config.CFAccessTeam())
	if st.team == team {
		return st.policy
	}
	next := newCSPState(st.hashes, team)
	cspHeader.CompareAndSwap(st, next) // losing the race just means someone else installed an equivalent state
	return next.policy
}

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
	cspHeader.Store(newCSPState(hashes, cspAccessTeam(config.CFAccessTeam())))
	return len(hashes)
}
