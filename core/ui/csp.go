// file: core/ui/csp.go
// version: 0.1.0
// guid: 9d41c6e7-2b0a-4f83-a5d6-7e3c1b9f8a24
// last-edited: 2026-11-20

package ui

import (
	"slices"
	"strings"
)

// Settings is the core.ui config section (07 §7.15). The kernel declares the
// section; Handler reads it through Options.Config on every index.html
// response, so the two hot keys, csp.connect_src and csp.report_only, take
// effect on the next page load with no restart (§7.6.2).
type Settings struct {
	Enabled     bool                `json:"enabled" reload:"restart"`
	CSP         CSPSettings         `json:"csp"`
	Diagnostics DiagnosticsSettings `json:"diagnostics"`
}

// CSPSettings is core.ui.csp.
type CSPSettings struct {
	ConnectSrc []string `json:"connect_src"` // bare DNS names
	ReportOnly bool     `json:"report_only"`
}

// DiagnosticsSettings is core.ui.diagnostics.
type DiagnosticsSettings struct {
	Accept bool `json:"accept"`
}

// DefaultSettings are 07 §7.15's defaults.
func DefaultSettings() Settings {
	return Settings{Enabled: true, CSP: CSPSettings{ConnectSrc: []string{}}, Diagnostics: DiagnosticsSettings{Accept: true}}
}

// CSPReportPath is where the shell policy sends violation reports, the
// route internal/uihttp registers (07 §7.6.2).
const CSPReportPath = "/api/core/v1/ui/csp-report"

// cspReportGroup is the Reporting-Endpoints group the report-to directive names.
const cspReportGroup = "csp"

// cspBase is HEAD's cspBase verbatim (internal/server/csp.go), with the
// report URL moved to core's route (07 §7.6.2).
const cspBase = "default-src 'self'; " +
	"style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data: blob: https:; " +
	"font-src 'self' data:; " +
	"media-src 'self' blob:; " +
	"object-src 'none'; " +
	"base-uri 'none'; " +
	"form-action 'self'; " +
	"frame-ancestors 'self'; " +
	"report-uri " + CSPReportPath + "; " +
	"report-to " + cspReportGroup

// placeholderCSP is the policy of the API-only placeholder page: it loads
// nothing and styles itself inline.
const placeholderCSP = "default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; form-action 'none'; frame-ancestors 'self'"

// buildPolicy is the shell policy of §7.6.2: script-src 'self' plus the
// inline-script hashes, connect-src 'self' plus each host over https, then
// cspBase, then the app's extra directives.
func buildPolicy(hashes, hosts, extra []string) string {
	var b strings.Builder
	b.WriteString("script-src 'self'")
	for _, h := range hashes {
		b.WriteString(" " + h)
	}
	b.WriteString("; connect-src 'self'")
	for _, h := range hosts {
		b.WriteString(" https://" + h)
	}
	b.WriteString("; " + cspBase)
	for _, d := range extra {
		b.WriteString("; " + d)
	}
	return b.String()
}

// cspHosts normalises raw connect-src entries: trimmed, lower-cased, a
// leading https:// and a trailing slash tolerated, and kept only when the
// rest is a bare DNS name with at least one dot, as HEAD's cspAccessTeam
// does. Anything else (a scheme, a path, a port, a space, a semicolon, a
// wildcard) is dropped, so a malformed setting can neither widen nor corrupt
// the header. Duplicates are dropped; order is kept.
func cspHosts(raw ...[]string) []string {
	var out []string
	for _, list := range raw {
		for _, r := range list {
			h := strings.ToLower(strings.TrimSpace(r))
			h = strings.TrimPrefix(h, "https://")
			h = strings.TrimSuffix(h, "/")
			if validHost(h) && !slices.Contains(out, h) {
				out = append(out, h)
			}
		}
	}
	return out
}

// validHost reports a bare DNS name: dot-separated labels of [a-z0-9-], each
// starting and ending with a letter or digit, at least two labels.
func validHost(h string) bool {
	if len(h) == 0 || len(h) > 253 {
		return false
	}
	labels := strings.Split(h, ".")
	if len(labels) < 2 {
		return false
	}
	for _, l := range labels {
		if len(l) == 0 || len(l) > 63 || l[0] == '-' || l[len(l)-1] == '-' {
			return false
		}
		for i := 0; i < len(l); i++ {
			c := l[i]
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}

// extraDirectives keeps the app's directives that are one directive each: a
// non-empty token list with no ';' or ',' (which would start another
// directive or policy) and no control character.
func extraDirectives(raw []string) []string {
	var out []string
	for _, d := range raw {
		d = strings.TrimSpace(d)
		if d == "" || strings.ContainsAny(d, ";,") || strings.ContainsFunc(d, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
			continue
		}
		out = append(out, d)
	}
	return out
}
