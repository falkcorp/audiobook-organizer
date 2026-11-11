// file: core/integrations/providerhttp/redact.go
// version: 1.0.0
// guid: 8397d44b-b492-4bf1-aa16-cf162225a337
// last-edited: 2026-11-11

package providerhttp

import (
	"net/url"
	"strings"
)

// redacted replaces a secret's value.
const redacted = "REDACTED"

// secretNames are the query, header and JSON-field names whose value is always
// replaced, compared case-insensitively (M2.1: apikey, api_key, token,
// password, Authorization, X-Api-Key and Cookie, plus the other names the
// provider APIs of HEAD put a credential in: Google Books' key, OAuth's
// access_token and client_secret, signed URLs' signature).
func secretNames(extra []string) []string {
	base := []string{"apikey", "api_key", "api-key", "key", "token", "access_token", "auth", "password",
		"secret", "client_secret", "signature", "sig", "authorization", "x-api-key", "cookie", "set-cookie"}
	for _, e := range extra {
		if e = strings.ToLower(strings.TrimSpace(e)); e != "" {
			base = append(base, e)
		}
	}
	return base
}

func isSecretName(name string, names []string) bool {
	name = strings.ToLower(name)
	for _, n := range names {
		if name == n {
			return true
		}
	}
	return false
}

// redactURL removes every credential from raw: userinfo, and the value of each
// query parameter named in secretNames (and extra). A URL that does not parse
// loses its whole query and fragment instead: a value of unknown shape is not
// kept.
func redactURL(raw string, extra []string) string {
	u, err := url.Parse(raw)
	if err != nil {
		if i := strings.IndexAny(raw, "?#"); i >= 0 {
			raw = raw[:i]
		}
		return raw
	}
	if u.User != nil {
		u.User = url.User(redacted)
	}
	if u.RawQuery != "" {
		names := secretNames(extra)
		parts := strings.Split(u.RawQuery, "&")
		for i, p := range parts {
			name, _, hasValue := strings.Cut(p, "=")
			dec, derr := url.QueryUnescape(name)
			if derr != nil {
				dec = name
			}
			if isSecretName(dec, names) {
				if hasValue {
					parts[i] = name + "=" + redacted
				} else {
					parts[i] = name
				}
			}
		}
		u.RawQuery = strings.Join(parts, "&")
	}
	u.Fragment, u.RawFragment = "", ""
	return u.String()
}

// scrubText replaces the value of every "name=value" and "name": "value" pair
// in s whose name is a secret name, for text that may echo a request (a remote
// error body, a transport error). It is a best-effort filter: a secret the
// remote reformats is not found, which is why error and log paths also never
// carry the body of a response (Detail is bounded, M1.3).
func scrubText(s string, extra []string) string {
	low := strings.ToLower(s)
	var b strings.Builder
	i := 0
	for i < len(s) {
		n, ok := secretAt(low, i, extra)
		if !ok {
			b.WriteByte(s[i])
			i++
			continue
		}
		// s[i:i+n] is "name" followed by the separator; the value follows.
		b.WriteString(s[i : i+n])
		j := i + n
		quoted := j < len(s) && (s[j] == '"' || s[j] == '\'')
		if quoted {
			q := s[j]
			k := j + 1
			for k < len(s) && s[k] != q {
				k++
			}
			b.WriteByte(q)
			b.WriteString(redacted)
			if k < len(s) {
				b.WriteByte(q)
				k++
			}
			i = k
			continue
		}
		k := j
		for k < len(s) && !strings.ContainsRune("& \t\r\n\"',;<>)", rune(s[k])) {
			k++
		}
		b.WriteString(redacted)
		i = k
	}
	return b.String()
}

// secretAt reports whether a secret name starts at low[i] (preceded by a
// non-name character) and is followed by '=' or a JSON colon; n is the length
// of the name plus the separator (and the spaces of a JSON colon).
func secretAt(low string, i int, extra []string) (n int, ok bool) {
	if i > 0 && isNameByte(low[i-1]) {
		return 0, false
	}
	for _, name := range secretNames(extra) {
		if !strings.HasPrefix(low[i:], name) {
			continue
		}
		j := i + len(name)
		if j < len(low) && low[j] == '"' { // "name":
			j++
		}
		for j < len(low) && low[j] == ' ' {
			j++
		}
		if j < len(low) && (low[j] == '=' || low[j] == ':') {
			j++
			for j < len(low) && low[j] == ' ' {
				j++
			}
			return j - i, true
		}
	}
	return 0, false
}

func isNameByte(c byte) bool {
	return c == '_' || c == '-' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// scrubbedError carries a scrubbed message over an error whose chain callers
// still need for errors.Is and errors.As.
type scrubbedError struct {
	msg string
	err error
}

func (e *scrubbedError) Error() string { return e.msg }
func (e *scrubbedError) Unwrap() error { return e.err }

// redactError returns err with credentials removed from the request URL a
// *url.Error carries (the form net/http gives every transport failure) and
// from its text. It keeps the chain, so classification and errors.Is still
// work. The *http.Client that Client() returns builds that *url.Error itself,
// so a caller that uses it directly (an SDK) sees the URL net/http wrote; Do
// and GetJSON redact it (a limit recorded in seams.d/F7-02.md).
func redactError(err error, extra []string) error {
	if err == nil {
		return nil
	}
	if ue, ok := err.(*url.Error); ok { //nolint:errorlint // the top-level wrapper net/http itself returns
		c := *ue
		c.URL = redactURL(ue.URL, extra)
		if inner := redactError(ue.Err, extra); inner != ue.Err {
			c.Err = inner
		}
		return &c
	}
	msg := err.Error()
	if clean := scrubText(msg, extra); clean != msg {
		return &scrubbedError{msg: clean, err: err}
	}
	return err
}
