// file: internal/server/middleware/log_redact.go
// version: 1.0.0
// guid: f83a7613-f71d-4a0c-ae37-7f7f3ccf8929
// last-edited: 2026-09-19

package middleware

import (
	"fmt"
	"net/url"
	"strings"
	"time"

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

// RedactingLogFormatter is gin's default request-log format with credential
// query values redacted. Use it as gin.LoggerConfig.Formatter.
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
		RedactQueryCredentials(param.Path),
		param.ErrorMessage,
	)
}
