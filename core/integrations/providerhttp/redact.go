// file: core/integrations/providerhttp/redact.go
// version: 2.0.0
// guid: 8397d44b-b492-4bf1-aa16-cf162225a337
// last-edited: 2026-11-11

package providerhttp

import (
	"net/url"
	"slices"
	"strings"
	"unicode/utf8"
)

// redacted replaces a secret's value.
const redacted = "REDACTED"

// unparseableURL is what an unparseable URL with no recognisable scheme and
// host prints as.
const unparseableURL = "(unparseable URL)"

// scrubber removes credentials from URLs, error text and response excerpts.
// The zero value applies the built-in rules; newScrubber adds the adapter's
// own names. It holds no state a request changes, so one is shared by every
// Client of an instance.
//
// A name is a secret by RULE, not by list, so the provider APIs of HEAD and
// the ones a later adapter adds are covered without each being named:
//
//   - it ends in token, key, secret, password, passwd, pwd, signature,
//     credential(s), cookie or auth (access_token, X-Plex-Token, refresh_token,
//     client_secret, api-key, apikey, X-Api-Key, OAuth, Set-Cookie ...);
//   - it starts with session or sig, or contains authoriz, credential,
//     secret, password, passwd or session (Authorization, Proxy-Authorization,
//     JSESSIONID);
//   - it is exactly auth, pass, pwd, sig or sid, or one of the extra names.
//
// "auth" is matched exactly or as a suffix, never as a prefix or substring:
// this is a book library, and author, authors and authority must survive.
// Case and the separators - _ . are ignored, so api_key, api-key and ApiKey
// are one name.
type scrubber struct {
	extra []string // lower-case, separators removed
}

func newScrubber(extra []string) scrubber {
	s := scrubber{}
	for _, e := range extra {
		var buf nameBuf
		if c, _ := compactName(e, &buf); c != "" {
			s.extra = append(s.extra, c)
		}
	}
	return s
}

// withExtra is s with more names.
func (s scrubber) withExtra(names ...string) scrubber {
	t := newScrubber(names)
	t.extra = append(t.extra, s.extra...)
	return t
}

// maxNameWindow bounds how much of a name the rules look at: a longer name is
// judged by its last maxNameWindow characters (the suffix rules), so the cost
// of a hostile body stays linear.
const maxNameWindow = 64

// nameBuf is the scratch compactName works in.
type nameBuf [2 * maxNameWindow]byte

// compactName lower-cases name and drops the separators - _ . and spaces,
// keeping at most the last maxNameWindow characters. whole reports that
// nothing was dropped for length.
func compactName(name string, buf *nameBuf) (c string, whole bool) {
	whole = true
	if len(name) > len(buf) {
		name, whole = name[len(name)-len(buf):], false
	}
	n := 0
	for i := 0; i < len(name); i++ {
		ch := name[i]
		switch {
		case ch == '-' || ch == '_' || ch == '.' || ch == ' ':
			continue
		case ch >= 'A' && ch <= 'Z':
			ch += 'a' - 'A'
		}
		buf[n] = ch
		n++
	}
	if n > maxNameWindow {
		return string(buf[n-maxNameWindow : n]), false
	}
	return string(buf[:n]), whole
}

// secret reports whether a query, header or JSON-field name is a credential.
func (s scrubber) secret(name string) bool {
	var buf nameBuf
	c, whole := compactName(name, &buf)
	if c == "" {
		return false
	}
	if whole {
		if slices.Contains(s.extra, c) {
			return true
		}
		switch c {
		case "auth", "pass", "pwd", "passwd", "sig", "sid":
			return true
		}
		if strings.HasPrefix(c, "session") || strings.HasPrefix(c, "sig") {
			return true
		}
	}
	for _, suf := range [...]string{"token", "key", "secret", "password", "passwd", "pwd", "signature",
		"credential", "credentials", "cookie", "auth"} {
		if strings.HasSuffix(c, suf) {
			return true
		}
	}
	for _, sub := range [...]string{"authoriz", "credential", "secret", "password", "passwd", "session"} {
		if strings.Contains(c, sub) {
			return true
		}
	}
	return false
}

// wholeValue reports a name whose value is a scheme and a token ("Bearer
// abc"), so the redaction takes the spaces inside it.
func wholeValue(name string) bool {
	var buf nameBuf
	c, _ := compactName(name, &buf)
	return strings.Contains(c, "authoriz") || strings.HasSuffix(c, "auth")
}

// url removes every credential from raw: userinfo, the value of each query
// parameter whose name is a secret, a path segment shaped like a chat-bot
// token (bot<digits>:<token>) and the fragment. A URL that does not parse is
// never printed: only its scheme and host are, or a constant when even those
// are unclear.
//
// What it cannot see: a credential that is a free-form path segment
// (/api/<key>/search) is not recognisable as one. An adapter whose provider
// does that sends the credential through a header, or through
// Options.QueryCredential where the provider takes a query form, which keeps
// it out of every URL; seams.d/F7-02.md records the limit.
func (s scrubber) url(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return unparseable(raw)
	}
	if u.Opaque != "" {
		return u.Scheme + ":" + redacted
	}
	if u.User != nil {
		u.User = url.User(redacted)
	}
	if p := u.EscapedPath(); strings.Contains(p, ":") {
		segs := strings.Split(p, "/")
		changed := false
		for i, seg := range segs {
			if clean, ok := redactBotToken(seg); ok {
				segs[i], changed = clean, true
			}
		}
		if changed {
			u.RawPath = strings.Join(segs, "/")
			if dec, derr := url.PathUnescape(u.RawPath); derr == nil {
				u.Path = dec
			}
		}
	}
	if u.RawQuery != "" {
		parts := strings.Split(u.RawQuery, "&")
		for i, p := range parts {
			name, _, hasValue := strings.Cut(p, "=")
			dec, derr := url.QueryUnescape(name)
			if derr != nil {
				dec = name
			}
			if s.secret(dec) {
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

const letters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"

// redactBotToken replaces the token of a path segment shaped like
// [letters]<5+ digits>:<16+ token characters> (the form chat-bot APIs put the
// credential in) and reports whether it did.
func redactBotToken(seg string) (string, bool) {
	colon := strings.IndexByte(seg, ':')
	if colon < 0 {
		return seg, false
	}
	head := strings.TrimLeft(seg[:colon], letters)
	if len(head) < 5 || strings.Trim(head, "0123456789") != "" {
		return seg, false
	}
	tail := seg[colon+1:]
	if len(tail) < 16 || strings.Trim(tail, letters+"0123456789_-") != "" {
		return seg, false
	}
	return seg[:colon+1] + redacted, true
}

// unparseable prints only scheme://host of a URL net/url rejects (the
// userinfo, path, query and fragment are dropped), or a constant.
func unparseable(raw string) string {
	i := strings.Index(raw, "://")
	if i <= 0 || i > 16 {
		return unparseableURL
	}
	scheme := raw[:i]
	if strings.Trim(scheme, letters+"0123456789+.-") != "" {
		return unparseableURL
	}
	rest := raw[i+3:]
	if q := strings.IndexAny(rest, "?#"); q >= 0 {
		rest = rest[:q]
	}
	if at := strings.LastIndexByte(rest, '@'); at >= 0 { // userinfo may hold a slash
		rest = rest[at+1:]
	}
	if sl := strings.IndexByte(rest, '/'); sl >= 0 {
		rest = rest[:sl]
	}
	if rest == "" || strings.Trim(rest, letters+"0123456789.-:[]") != "" {
		return unparseableURL
	}
	return scheme + "://" + rest
}

func isNameByte(c byte) bool {
	return c == '_' || c == '-' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// valueStop is the set of bytes that end an unquoted value.
const valueStop = "& \t\r\n\"',;<>)"

// lineStop ends the value of a name whose value holds spaces (Authorization).
const lineStop = "&\r\n\"',;<>"

// text replaces the value of every "name=value", "name: value" and
// "name": "value" pair in in whose name is a secret (quoted with " or ', or
// bare), every "Bearer <token>" and the userinfo of every URL, for text that
// may echo a request (a remote error body, a transport error). One pass,
// linear in len(in): each name is judged once, by at most maxNameWindow
// bytes. It is a best-effort filter: a secret the remote reformats is not
// found, which is why error and log paths also never carry the whole body
// (Detail is bounded, M1.3).
func (s scrubber) text(in string) string {
	var b strings.Builder
	n := len(in)
	i := 0
	for i < n {
		if !isNameByte(in[i]) {
			b.WriteByte(in[i])
			i++
			continue
		}
		j := i
		for j < n && isNameByte(in[j]) {
			j++
		}
		name := in[i:j]
		// userinfo of a URL: scheme://user:pass@host
		if strings.HasPrefix(in[j:], "://") {
			m := j + 3
			for m < n && !strings.ContainsRune("/?# \t\r\n\"'<>", rune(in[m])) {
				m++
			}
			if at := strings.LastIndexByte(in[j+3:m], '@'); at >= 0 {
				b.WriteString(in[i : j+3])
				b.WriteString(redacted + "@")
				i = j + 3 + at + 1
				continue
			}
		}
		// Bearer <token>
		if len(name) == 6 && strings.EqualFold(name, "bearer") && j < n && in[j] == ' ' {
			k := j
			for k < n && in[k] == ' ' {
				k++
			}
			e := k
			for e < n && !strings.ContainsRune(valueStop, rune(in[e])) {
				e++
			}
			b.WriteString(in[i:k])
			if e > k {
				b.WriteString(redacted)
			}
			i = e
			continue
		}
		k := j
		if k < n && (in[k] == '"' || in[k] == '\'') { // "name": or 'name':
			k++
		}
		for k < n && in[k] == ' ' {
			k++
		}
		if k >= n || (in[k] != '=' && in[k] != ':') || !s.secret(name) {
			b.WriteString(name)
			i = j
			continue
		}
		k++
		for k < n && in[k] == ' ' {
			k++
		}
		b.WriteString(in[i:k])
		if k < n && (in[k] == '"' || in[k] == '\'') {
			q := in[k]
			e := k + 1
			for e < n && in[e] != q {
				e++
			}
			b.WriteByte(q)
			b.WriteString(redacted)
			if e < n {
				b.WriteByte(q)
				e++
			}
			i = e
			continue
		}
		stop := valueStop
		if wholeValue(name) {
			stop = lineStop
		}
		e := k
		for e < n && !strings.ContainsRune(stop, rune(in[e])) {
			e++
		}
		// "credentials: Bearer abc": the value is a scheme and a token even
		// where the name is not Authorization.
		if v := in[k:e]; len(v) <= 6 && (strings.EqualFold(v, "bearer") || strings.EqualFold(v, "basic")) && e < n && in[e] == ' ' {
			for e < n && in[e] == ' ' {
				e++
			}
			for e < n && !strings.ContainsRune(valueStop, rune(in[e])) {
				e++
			}
		}
		b.WriteString(redacted)
		i = e
	}
	return b.String()
}

// cut returns s cut to at most max bytes on a rune boundary.
func cut(s string, max int) string {
	if len(s) <= max {
		return s
	}
	s = s[:max]
	for len(s) > 0 {
		r, size := utf8.DecodeLastRuneInString(s)
		if r != utf8.RuneError || size != 1 {
			break
		}
		s = s[:len(s)-1]
	}
	return s
}

// bounded is in cut to max bytes with every credential removed: cut to a
// margin first (so a 16 MiB body costs the same as a 1 KiB one), scrub, then
// cut again. The first cut cannot expose a secret: a value is replaced from
// its name to its end, and a cut name is not a name. The second cut is what
// guarantees the bound.
func (s scrubber) bounded(in string, max int) string {
	in = cut(in, 4*max+256)
	in = strings.ToValidUTF8(in, "")
	return cut(s.text(in), max)
}

// scrubbedError carries a scrubbed message over an error whose chain callers
// still need for errors.Is and errors.As.
type scrubbedError struct {
	msg string
	err error
}

func (e *scrubbedError) Error() string { return e.msg }
func (e *scrubbedError) Unwrap() error { return e.err }

// err returns err with credentials removed from the request URL a *url.Error
// carries (the form net/http gives every transport failure) and from its text.
// It keeps the chain, so classification and errors.Is still work. The
// *http.Client that Client() returns builds that *url.Error itself, so a
// caller that uses it directly (an SDK) sees the URL net/http wrote; Do and
// GetJSON redact it. A credential the transport adds itself
// (Policy.QueryCredential) is never in that URL, which is the supported way to
// keep one out of the raw client's errors.
func (s scrubber) err(err error) error {
	if err == nil {
		return nil
	}
	if ue, ok := err.(*url.Error); ok { //nolint:errorlint // the top-level wrapper net/http itself returns
		c := *ue
		c.URL = s.url(ue.URL)
		if inner := s.err(ue.Err); inner != ue.Err {
			c.Err = inner
		}
		return &c
	}
	msg := err.Error()
	if clean := s.text(msg); clean != msg {
		return &scrubbedError{msg: clean, err: err}
	}
	return err
}

// dropLiteral removes every occurrence of the credential value v from err's
// text, keeping the chain: the last defence for an error that echoes a value
// the transport itself added.
func dropLiteral(err error, v string) error {
	if err == nil || v == "" {
		return err
	}
	msg := err.Error()
	if !strings.Contains(msg, v) {
		return err
	}
	return &scrubbedError{msg: strings.ReplaceAll(msg, v, redacted), err: err}
}
