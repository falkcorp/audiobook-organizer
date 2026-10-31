// file: internal/metadata/detail_redact.go
// version: 1.1.0
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

// redactedValue replaces userinfo, a URL query and every bare secret value.
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
// Every URL-shaped token keeps only scheme, host and path. Userinfo is replaced
// by "***@", and a query is replaced WHOLESALE by "?***" -- parameter names are
// not kept, because names are attacker- and provider-chosen and a value can sit
// in any shape the splitter does not anticipate. A fragment is dropped. Bare
// name=value pairs outside any URL are masked when the name marks a credential.
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

// redactURL reduces a URL token to scheme://host/path[?***]. It works on the
// raw string rather than through url.Parse so a malformed URL (a bad escape, a
// password containing "/" or "?") is still scrubbed instead of being returned
// verbatim because parsing failed. It fails closed:
//
//   - If an "@" appears anywhere after "://", everything between "://" and the
//     LAST "@" is replaced by "***@". This runs first, before any splitting, so
//     a password containing "/", "?" or "#" cannot hide from it. The cost is
//     that an "@" in a query is also read as userinfo, which only over-redacts.
//   - From the first "?", the rest of the token is replaced by "?***".
//   - From a "#" that comes before any "?", the rest is dropped.
func redactURL(raw string) string {
	scheme, rest, found := strings.Cut(raw, "://")
	if !found {
		return raw
	}
	if i := strings.LastIndexByte(rest, '@'); i >= 0 {
		rest = redactedValue + rest[i:]
	}
	if i := strings.IndexAny(rest, "?#"); i >= 0 {
		if rest[i] == '?' {
			return scheme + "://" + rest[:i] + "?" + redactedValue
		}
		return scheme + "://" + rest[:i]
	}
	return scheme + "://" + rest
}

// redactedError carries a redacted message while keeping the original error on
// the Unwrap chain, so errors.Is / errors.As / ClassifyProviderError (which
// uses errors.AsType, never the message) see exactly what they saw before.
type redactedError struct {
	msg string
	err error
}

func (e *redactedError) Error() string { return e.msg }
func (e *redactedError) Unwrap() error { return e.err }

// RedactProviderError returns err unchanged when its message carries nothing to
// scrub (so identity and type assertions on ordinary errors are untouched), and
// otherwise a wrapper whose Error() is the redacted text and whose Unwrap() is
// the original. Use it at the boundary where a provider's error leaves the
// provider layer.
func RedactProviderError(err error) error {
	if err == nil {
		return nil
	}
	msg := DetailForError(err)
	if msg == err.Error() {
		return err
	}
	return &redactedError{msg: msg, err: err}
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
