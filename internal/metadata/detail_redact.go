// file: internal/metadata/detail_redact.go
// version: 1.0.0
// guid: dbc8ca7a-f131-48c1-91b6-ca76eb082dde
// last-edited: 2026-10-30

package metadata

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// redactedValue replaces every query-parameter value and every bare secret
// value. Values are the secret; names are kept so the message stays useful.
const redactedValue = "***"

// urlTokenRE finds URL-shaped tokens (scheme://...) in free text. Quotes,
// angle brackets and whitespace end a token, which is how a *url.Error
// ("Get \"https://...\": dial ...") presents its URL.
var urlTokenRE = regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.\-]*://[^\s"'<>]+`)

// secretPairRE matches bare name=value pairs whose name marks a credential:
// key, api_key, apikey, token, access_token, secret, password, authorization,
// bearer, and prefixed forms such as client_secret or x-api-key. The value
// runs to the next separator.
var secretPairRE = regexp.MustCompile(
	`(?i)\b((?:[a-z0-9]+[_-])*(?:key|apikey|token|secret|password|authorization|bearer))=([^&\s"'<>;,)\]}]+)`)

// RedactProviderDetail scrubs credentials from provider error text before it
// is stored, logged or served.
//
// Every URL-shaped token loses its userinfo and fragment and keeps its query
// parameter NAMES with every VALUE replaced by "***". There is deliberately no
// allow-list of harmless names: providers put the API key under whatever name
// they like, and a list that is one name short leaks. Bare name=value pairs
// outside any URL are redacted when the name marks a credential.
func RedactProviderDetail(s string) string {
	if s == "" {
		return s
	}
	s = urlTokenRE.ReplaceAllStringFunc(s, func(tok string) string {
		// Trailing sentence punctuation is not part of the URL.
		trimmed := strings.TrimRight(tok, ".,;:)]}")
		return redactURL(trimmed) + tok[len(trimmed):]
	})
	return secretPairRE.ReplaceAllString(s, "$1="+redactedValue)
}

// redactURL rebuilds a URL as scheme://host/path?name=***&name2=***, dropping
// userinfo and fragment. It splits the string by hand rather than through
// url.Parse so a malformed URL (a bad escape, say) is still scrubbed instead of
// being returned verbatim because parsing failed.
func redactURL(raw string) string {
	rest := raw
	if i := strings.IndexByte(rest, '#'); i >= 0 {
		rest = rest[:i]
	}
	query := ""
	hasQuery := false
	if i := strings.IndexByte(rest, '?'); i >= 0 {
		rest, query, hasQuery = rest[:i], rest[i+1:], true
	}
	scheme, afterScheme, found := strings.Cut(rest, "://")
	if !found {
		return raw
	}
	authority, path := afterScheme, ""
	if i := strings.IndexByte(afterScheme, '/'); i >= 0 {
		authority, path = afterScheme[:i], afterScheme[i:]
	}
	if i := strings.LastIndexByte(authority, '@'); i >= 0 {
		authority = authority[i+1:]
	}
	out := scheme + "://" + authority + path
	if !hasQuery {
		return out
	}
	parts := strings.Split(query, "&")
	for i, p := range parts {
		if p == "" {
			continue
		}
		name, _, hasEq := strings.Cut(p, "=")
		if !hasEq {
			// A bare token ("?SECRET") has no name to keep; its text is the value.
			parts[i] = redactedValue
			continue
		}
		parts[i] = name + "=" + redactedValue
	}
	return out + "?" + strings.Join(parts, "&")
}

// DetailForError renders err for storage as a throttle hold's Detail with
// credentials removed. A *url.Error anywhere in the chain is rebuilt from its
// parts (Op, redacted URL, inner error) in place inside the full message, so a
// wrapping prefix survives; the whole result then goes through
// RedactProviderDetail to catch URLs and secrets in the rest of the text.
func DetailForError(err error) string {
	if err == nil {
		return ""
	}
	full := err.Error()
	if ue, ok := errors.AsType[*url.Error](err); ok {
		orig := ue.Error()
		if strings.Contains(full, orig) {
			rebuilt := fmt.Sprintf("%s %q: %s", ue.Op, redactURL(ue.URL), ue.Err)
			full = strings.Replace(full, orig, rebuilt, 1)
		}
	}
	return RedactProviderDetail(full)
}
