// file: internal/server/middleware/log_redact.go
// version: 1.3.3
// guid: f83a7613-f71d-4a0c-ae37-7f7f3ccf8929
// last-edited: 2026-11-08

package middleware

import (
	"net/url"
	"strings"

	logging "github.com/falkcorp/mediacore/log"
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
// query, a RequestURI such as the HTTPS redirect log's:
// credential query values (RedactQueryCredentials) and the invite token path
// segment (logging.RedactInvitePathWithQuery, which cuts at the first "?").
// Invite tokens are bearer secrets that live in the PATH, so the query-only
// redaction above cannot see them. A caller that holds the path and the query
// apart, or only a decoded URL.Path, must not use it (a decoded %3F is a "?" in
// the path that is not a query delimiter): core's access log does that split
// itself (core/internal/httpmw, loggedTarget), and logging.RedactInvitePath
// covers a lone decoded path.
func RedactLogPath(pathWithQuery string) string {
	return RedactQueryCredentials(logging.RedactInvitePathWithQuery(pathWithQuery))
}
