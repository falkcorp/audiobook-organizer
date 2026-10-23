// file: internal/server/middleware/log_redact.go
// version: 1.3.0
// guid: f83a7613-f71d-4a0c-ae37-7f7f3ccf8929
// last-edited: 2026-10-23

package middleware

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/logging"
	"github.com/gin-gonic/gin"
)

// credentialQueryKeys are query parameters whose VALUE is a credential. Matched
// case-insensitively.
//
// Why this exists: AudioBooth, Absorb and CarPlay put the bearer credential in
// the URL (?token=) on cover, file and download requests — mandatory for those
// clients (§1.7.2) — and since 2026-09-19 an `abk_` API key is accepted there
// too. gin's default request logger prints the path WITH its query string, so
// every such request wrote a live credential into the journal.
var credentialQueryKeys = map[string]bool{
	"token":         true,
	"access_token":  true,
	"refresh_token": true,
	"api_key":       true,
	"apikey":        true,
	"key":           true,
	"password":      true,
	"secret":        true,
	"code":          true, // OAuth/OIDC authorization codes are one-time credentials too
	"code_verifier": true,
}

// RedactedValue replaces a credential value in logs.
const RedactedValue = "REDACTED"

// RedactQueryCredentials returns pathWithQuery with the value of every
// credential query parameter replaced by RedactedValue. Parameter order, other
// parameters and the raw (still-encoded) form of non-credential values are
// preserved, so the log line stays otherwise identical. It never decodes and
// re-encodes, so a malformed query cannot make it drop or corrupt a value.
func RedactQueryCredentials(pathWithQuery string) string {
	path, query, found := strings.Cut(pathWithQuery, "?")
	if !found || query == "" {
		return pathWithQuery
	}
	parts := strings.Split(query, "&")
	for i, part := range parts {
		k, _, hasValue := strings.Cut(part, "=")
		name := strings.ToLower(k)
		if dec, err := url.QueryUnescape(name); err == nil {
			name = dec
		}
		if credentialQueryKeys[name] && hasValue {
			parts[i] = k + "=" + RedactedValue
		}
	}
	return path + "?" + strings.Join(parts, "&")
}

// RedactLogPath is the redaction for a logged request target that carries a REAL
// query, a RequestURI such as the panic-recovery dump and the HTTPS redirect log:
// credential query values (RedactQueryCredentials) and the invite token path
// segment (logging.RedactInvitePathWithQuery, which cuts at the first "?").
// Invite tokens are bearer secrets that live in the PATH, so the query-only
// redaction above cannot see them. A caller that holds the path and the query
// apart, or only a decoded URL.Path, must not use it (a decoded %3F is a "?" in
// the path that is not a query delimiter): see RedactLogRequestPath and
// logging.RedactInvitePath.
func RedactLogPath(pathWithQuery string) string {
	return RedactQueryCredentials(logging.RedactInvitePathWithQuery(pathWithQuery))
}

// RedactLogRequestPath is RedactLogPath for a path and a raw query held apart, the
// way net/http parses a request: path is URL.Path (decoded once), query is
// URL.RawQuery. The path is redacted with "?" as path text and any "?" left in
// it is written as %3F, so the line's first "?" is always the real query
// delimiter and RedactQueryCredentials sees exactly the query. Credential keys
// after a "?" inside the path are masked too, before it is escaped.
func RedactLogRequestPath(path, rawQuery string) string {
	// A "?" in the decoded path (a %3F on the wire) is not a delimiter, but a
	// client can still put a credential after it (/x/cover%3Ftoken=...), so the
	// text after the first one is masked by key as a query would be before the
	// "?" is escaped.
	p := strings.ReplaceAll(RedactQueryCredentials(logging.RedactInvitePath(path)), "?", "%3F")
	if rawQuery == "" {
		return p
	}
	return RedactQueryCredentials(p + "?" + rawQuery)
}

// logFormatterPath is the redacted request target for param. gin builds
// param.Path as URL.Path, plus "?" and URL.RawQuery when there is one, from the
// URL as it was when the request arrived (a later handler may rewrite URL.Path,
// which is why the request's own URL.RequestURI is not used here). The known
// query suffix is cut off first, so a "?" that came from a decoded %3F stays in
// the path. When the request is not available the string cannot be split from
// its query with certainty (a "?" could be a decoded %3F or the delimiter, and
// reading it either way leaks the other shape), and when its query does not
// match what gin appended the same holds: nothing can be shown to be safe, so
// only "/[REDACTED]" is logged.
func logFormatterPath(param gin.LogFormatterParams) string {
	if param.Request == nil || param.Request.URL == nil {
		return "/" + logging.InviteTokenRedacted
	}
	raw := param.Request.URL.RawQuery
	if raw == "" {
		return RedactLogRequestPath(param.Path, "")
	}
	path, ok := strings.CutSuffix(param.Path, "?"+raw)
	if !ok {
		return "/" + logging.InviteTokenRedacted
	}
	return RedactLogRequestPath(path, raw)
}

// RedactingLogFormatter is gin's default request-log format with credential
// query values and invite tokens redacted. Use it as gin.LoggerConfig.Formatter.
func RedactingLogFormatter(param gin.LogFormatterParams) string {
	var statusColor, methodColor, resetColor string
	if param.IsOutputColor() {
		statusColor = param.StatusCodeColor()
		methodColor = param.MethodColor()
		resetColor = param.ResetColor()
	}
	if param.Latency > time.Minute {
		param.Latency = param.Latency.Truncate(time.Second)
	}
	return fmt.Sprintf("[GIN] %v |%s %3d %s| %13v | %15s |%s %-7s %s %#v\n%s",
		param.TimeStamp.Format("2006/01/02 - 15:04:05"),
		statusColor, param.StatusCode, resetColor,
		param.Latency,
		param.ClientIP,
		methodColor, param.Method, resetColor,
		logFormatterPath(param),
		param.ErrorMessage,
	)
}
