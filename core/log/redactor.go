// file: core/log/redactor.go
// version: 1.0.0
// guid: 44521f1f-a2ba-4b3c-a385-eb582b10e892
// last-edited: 2026-11-07

package log

import (
	"log/slog"
	"net/url"
	"strings"
)

// The replacement texts. redactedText is the same "[REDACTED]" the invite
// path rule writes.
const (
	redactedText  = InviteTokenRedacted
	redactedPanic = "[REDACTED:panic]"
)

// maxResolve bounds how many LogValue calls one value may chain, as
// slog.Value.Resolve does. A value still unresolved after that fails closed.
const maxResolve = 100

// RedactionKind is a set of redaction kinds: what a redaction replaced. The
// kinds are the "kind" label values of mediacore_log_redactions_total
// (05-logging §9) and mediacore_metrics_span_redactions_total (05-metrics
// §11).
type RedactionKind uint8

const (
	// RedactKey: the value of a deny-listed key.
	RedactKey RedactionKind = 1 << iota
	// RedactURL: URL userinfo, or a credential query or fragment value.
	RedactURL
	// RedactPath: a path rule rewrote a path.
	RedactPath
	// RedactValuer: a LogValuer that panicked (or never resolved) was
	// replaced with "[REDACTED:panic]".
	RedactValuer
)

// String is the kind's label ("key", "url", "path", "valuer"); a set of
// several joins them with "|" in that order, and the empty set is "".
func (k RedactionKind) String() string {
	var parts []string
	for _, b := range [...]struct {
		bit   RedactionKind
		label string
	}{{RedactKey, "key"}, {RedactURL, "url"}, {RedactPath, "path"}, {RedactValuer, "valuer"}} {
		if k&b.bit != 0 {
			parts = append(parts, b.label)
		}
	}
	return strings.Join(parts, "|")
}

// PathRule redacts a token-bearing path segment. Rules are pure functions of
// the path (no route table).
type PathRule struct {
	Name string                   // "invite", or a module-registered name
	Fn   func(path string) string // must fail closed (return a redacted path) when unsure
}

// DefaultPathRules covers core's own token-bearing routes: one rule, "invite",
// which is RedactInvitePath (core's /api/core/v1/auth/invites/{token} and
// HEAD's books invite routes and /invite/{token} page, after percent-decoding
// and dot-walk checks; fails closed to "/[REDACTED]"). Fragment-form links
// (/invite#token=, /login/temp#token=) never reach the server. A fresh slice
// on every call.
func DefaultPathRules() []PathRule {
	return []PathRule{{Name: "invite", Fn: RedactInvitePath}}
}

// DefaultRedactKeys is the attribute-key deny-list, matched case-insensitively
// after stripping '-', '_' and '.'. A fresh slice on every call.
func DefaultRedactKeys() []string {
	return []string{
		"password", "passwd", "secret", "token", "apikey", "authorization", "cookie", "setcookie",
		"privatekey", "clientsecret", "credential", "credentials", "accesstoken", "refreshtoken", "sessionid",
	}
}

// DefaultRedactQuery is the query parameters whose values are always replaced,
// matched case-insensitively after one round of percent-decoding of the name.
// A superset of HEAD's credentialQueryKeys
// (internal/server/middleware/log_redact.go), so OAuth authorization codes,
// PKCE verifiers and refresh tokens never reach a log or a span. 05-http-api's
// stages reference this list instead of restating it. A fresh slice on every
// call.
func DefaultRedactQuery() []string {
	return []string{
		"token", "access_token", "refresh_token", "api_key", "apikey", "key", "password", "secret",
		"code", "code_verifier", "sig", "signature",
	}
}

// URLKeys is the URL-keyed attributes whose string values always get URL and
// path handling, whatever they look like (a bare path is not URL-shaped). A
// fresh slice on every call.
func URLKeys() []string {
	return []string{
		"path", "url", "uri", "request_uri", "referer", "url.full", "url.path", "url.query", "http.target", "http.url",
	}
}

// Redactor is the one redaction type of core (05-logging §3, decision L4): the
// logging pipeline redacts every record through it once, before fan-out, and
// the metrics Provider's span exporter redacts span names and attributes with
// the same value (05-metrics §3). It is immutable after NewRedactor (its maps
// and rule slice are never written again), so one value is safe for
// concurrent use without a lock.
type Redactor struct {
	keys    map[string]struct{} // normalised deny-listed keys (normKey)
	query   map[string]struct{} // lower-case credential query names
	urlKeys map[string]struct{} // lower-case URL keys
	rules   []PathRule
}

// NewRedactor builds a Redactor from the defaults (fresh copies) plus
// extraKeys (core.log.redact_keys; added to the deny-list, never replacing it)
// and extraPaths (module path rules, applied after the defaults). A rule with
// a nil Fn redacts nothing and is skipped.
func NewRedactor(extraKeys []string, extraPaths ...PathRule) *Redactor {
	r := &Redactor{
		keys:    map[string]struct{}{},
		query:   map[string]struct{}{},
		urlKeys: map[string]struct{}{},
	}
	for _, k := range append(DefaultRedactKeys(), extraKeys...) {
		if n := normKey(k); n != "" {
			r.keys[n] = struct{}{}
		}
	}
	for _, q := range DefaultRedactQuery() {
		r.query[strings.ToLower(q)] = struct{}{}
	}
	for _, k := range URLKeys() {
		r.urlKeys[strings.ToLower(k)] = struct{}{}
	}
	r.rules = DefaultPathRules()
	for _, pr := range extraPaths {
		if pr.Fn != nil {
			r.rules = append(r.rules, pr)
		}
	}
	return r
}

// normKey lower-cases k and drops '-', '_' and '.'.
func normKey(k string) string {
	var b strings.Builder
	b.Grow(len(k))
	for i := 0; i < len(k); i++ {
		c := k[i]
		switch {
		case c == '-' || c == '_' || c == '.':
		case 'A' <= c && c <= 'Z':
			b.WriteByte(c + 'a' - 'A')
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// keyDenied reports whether key is on the deny-list: the whole key, or its
// last dot-separated segment (http.request.header.authorization, db.password),
// normalised.
func (r *Redactor) keyDenied(key string) bool {
	if key == "" {
		return false
	}
	if _, ok := r.keys[normKey(key)]; ok {
		return true
	}
	if i := strings.LastIndexByte(key, '.'); i >= 0 && i < len(key)-1 {
		_, ok := r.keys[normKey(key[i+1:])]
		return ok
	}
	return false
}

// queryDenied reports whether a query (or fragment) parameter name carries a
// credential: one round of percent-decoding, then case-insensitively on
// DefaultRedactQuery, or on the key deny-list (client_secret, session_id).
func (r *Redactor) queryDenied(name string) bool {
	if dec, err := url.QueryUnescape(name); err == nil {
		name = dec
	}
	name = strings.ToLower(name)
	if _, ok := r.query[name]; ok {
		return true
	}
	return r.keyDenied(name)
}

// Attr redacts one attribute (05-logging §3 "Pipeline"): it resolves a
// slog.LogValuer (a panicking or endless one becomes "[REDACTED:panic]"),
// replaces a deny-listed key's value (a whole group included) with
// "[REDACTED]", recurses into groups, and gives values under URLKeys(),
// *url.URL and url.URL values, URL-shaped strings and the text of an error
// the URL and path treatment.
func (r *Redactor) Attr(a slog.Attr) slog.Attr {
	out, _ := r.attr(a)
	return out
}

func (r *Redactor) attr(a slog.Attr) (slog.Attr, RedactionKind) {
	v, ok := resolve(a.Value)
	if !ok {
		return slog.String(a.Key, redactedPanic), RedactValuer
	}
	if r.keyDenied(a.Key) {
		if v.Kind() == slog.KindString && v.String() == redactedText {
			return slog.Attr{Key: a.Key, Value: v}, 0
		}
		return slog.String(a.Key, redactedText), RedactKey
	}
	switch v.Kind() {
	case slog.KindGroup:
		in := v.Group()
		out := make([]slog.Attr, len(in))
		var kinds RedactionKind
		for i, c := range in {
			var k RedactionKind
			out[i], k = r.attr(c)
			kinds |= k
		}
		return slog.Attr{Key: a.Key, Value: slog.GroupValue(out...)}, kinds
	case slog.KindString:
		if s, k := r.StringKinds(a.Key, v.String()); k != 0 {
			return slog.String(a.Key, s), k
		}
	case slog.KindAny:
		if s, k, ok := r.anyText(v.Any()); !ok {
			return slog.String(a.Key, redactedPanic), RedactValuer
		} else if k != 0 {
			return slog.String(a.Key, s), k
		}
	}
	return slog.Attr{Key: a.Key, Value: v}, 0
}

// anyText redacts the URL and error values a KindAny attribute can carry: the
// redacted text and its kinds when something was replaced. ok is false when
// the value's own method panicked.
func (r *Redactor) anyText(x any) (s string, k RedactionKind, ok bool) {
	defer func() {
		if recover() != nil {
			s, k, ok = "", 0, false
		}
	}()
	switch v := x.(type) {
	case *url.URL:
		if v == nil {
			return "", 0, true
		}
		s, k = r.urlKinds(v.String())
	case url.URL:
		s, k = r.urlKinds(v.String())
	case error:
		s, k = r.scanTokens(v.Error())
	}
	return s, k, true
}

// resolve is slog.Value.Resolve that reports a panicking or endless LogValuer
// instead of turning it into an error value carrying a stack.
func resolve(v slog.Value) (out slog.Value, ok bool) {
	defer func() {
		if recover() != nil {
			out, ok = slog.Value{}, false
		}
	}()
	for range maxResolve {
		if v.Kind() != slog.KindLogValuer {
			return v, true
		}
		v = v.LogValuer().LogValue()
	}
	if v.Kind() != slog.KindLogValuer {
		return v, true
	}
	return slog.Value{}, false
}

// URL returns raw with userinfo removed, credential query (and fragment)
// parameter values replaced with "[REDACTED]" and the path rules applied.
func (r *Redactor) URL(raw string) string {
	out, _ := r.urlKinds(raw)
	return out
}

// Path applies the path rules only.
func (r *Redactor) Path(p string) string {
	out, _ := r.pathKinds(p)
	return out
}

// String is StringKinds without the kinds: the key deny-list, then URL and
// path handling for URL-keyed or URL-shaped values.
func (r *Redactor) String(key, value string) string {
	out, _ := r.StringKinds(key, value)
	return out
}

// StringKinds redacts one string value under key and reports what it
// replaced (empty when the value is returned unchanged):
//
//   - a deny-listed key: "[REDACTED]" (RedactKey);
//   - "url.query": the value is a query string; its credential parameters are
//     replaced;
//   - any other key in URLKeys(): the whole value gets URL handling (URL),
//     then every whitespace-separated URL-shaped token in it does too;
//   - any other key: every whitespace-separated token that starts with "/" or
//     contains "://" gets URL handling, so "GET /invite/<token>" (a span
//     name) and an error's "Get \"https://...?token=...\"" are covered.
func (r *Redactor) StringKinds(key, value string) (string, RedactionKind) {
	if r.keyDenied(key) {
		if value == redactedText {
			return value, 0
		}
		return redactedText, RedactKey
	}
	lk := strings.ToLower(key)
	if lk == "url.query" {
		q, hadMark := strings.CutPrefix(value, "?")
		out, k := r.maskQuery(q)
		if hadMark {
			out = "?" + out
		}
		return out, k
	}
	if _, ok := r.urlKeys[lk]; ok {
		out, k := r.urlKinds(value)
		out2, k2 := r.scanTokens(out)
		return out2, k | k2
	}
	return r.scanTokens(value)
}

// pathKinds applies every path rule in order. A rule that panics fails closed
// to "/[REDACTED]".
func (r *Redactor) pathKinds(p string) (string, RedactionKind) {
	out := p
	for _, rule := range r.rules {
		out = applyRule(rule, out)
	}
	if out != p {
		return out, RedactPath
	}
	return p, 0
}

func applyRule(rule PathRule, p string) (out string) {
	defer func() {
		if recover() != nil {
			out = "/" + redactedText
		}
	}()
	return rule.Fn(p)
}

// urlKinds is URL with the kinds it replaced. It is textual, like HEAD's
// RedactQueryCredentials: nothing is parsed into a url.URL and re-encoded, so
// a malformed URL cannot make it drop or corrupt a value, and a decoded "?"
// or "#" in a path cannot hide a token.
//
//  1. A scheme ("<scheme>://", anything before the scheme kept as written) or
//     a network-path reference ("//") starts an authority, which ends at the
//     first '/', '?' or '#'; userinfo (up to its last '@') is removed.
//  2. The rest is cut at the first '#' and then the first '?': the path rules
//     run over the path, and the query and the fragment have their
//     credential parameters replaced.
//  3. When the path rules left that path unchanged but a '?' or '#' follows
//     it, they run again over the whole rest with '?' and '#' as path text (a
//     decoded /invite/?<token> is still a token); if that redacts, it is the
//     result, with anything after a '?' it left still masked as a query.
func (r *Redactor) urlKinds(raw string) (string, RedactionKind) {
	if raw == "" {
		return raw, 0
	}
	var kinds RedactionKind
	prefix, rest := "", raw
	authStart := -1
	if i := strings.Index(raw, "://"); i > 0 {
		s := i
		for s > 0 && isSchemeChar(raw[s-1]) {
			s--
		}
		if s < i && isAlpha(raw[s]) {
			authStart = i + 3
		}
	} else if strings.HasPrefix(raw, "//") {
		authStart = 2
	}
	if authStart >= 0 {
		end := len(raw)
		if j := strings.IndexAny(raw[authStart:], "/?#"); j >= 0 {
			end = authStart + j
		}
		auth := raw[authStart:end]
		if at := strings.LastIndexByte(auth, '@'); at >= 0 {
			auth = auth[at+1:]
			kinds |= RedactURL
		}
		prefix, rest = raw[:authStart]+auth, raw[end:]
	}
	beforeFrag, frag, hasFrag := strings.Cut(rest, "#")
	path, query, hasQuery := strings.Cut(beforeFrag, "?")
	path, k := r.pathKinds(path)
	kinds |= k
	if k == 0 && (hasQuery || hasFrag) {
		// The path before the first '?' or '#' is clean, but those may be
		// path text (a decoded %3F or %23) hiding a token behind them: read
		// the whole remainder as a path. Whatever the rule left after a '?'
		// is still masked as a query.
		if whole, wk := r.pathKinds(rest); wk != 0 {
			kinds |= wk
			path, query, hasQuery = whole, "", false
			hasFrag = false
			if p, q, ok := strings.Cut(whole, "?"); ok {
				path, query, hasQuery = p, q, true
			}
		}
	}
	var b strings.Builder
	b.Grow(len(raw))
	b.WriteString(prefix)
	b.WriteString(path)
	if hasQuery {
		q, k := r.maskQuery(query)
		kinds |= k
		b.WriteByte('?')
		b.WriteString(q)
	}
	if hasFrag {
		f, k := r.maskQuery(frag)
		kinds |= k
		b.WriteByte('#')
		b.WriteString(f)
	}
	if kinds == 0 {
		return raw, 0
	}
	return b.String(), kinds
}

// maskQuery replaces the value of every credential parameter in q (split on
// '&' and ';', which some servers also accept). Order, other parameters, the
// raw spelling of names and separators are kept; a name without '=' has no
// value to replace.
func (r *Redactor) maskQuery(q string) (string, RedactionKind) {
	if strings.IndexByte(q, '=') < 0 {
		return q, 0
	}
	var b strings.Builder
	b.Grow(len(q))
	var kinds RedactionKind
	start := 0
	for i := 0; i <= len(q); i++ {
		if i < len(q) && q[i] != '&' && q[i] != ';' {
			continue
		}
		part := q[start:i]
		if name, val, hasValue := strings.Cut(part, "="); hasValue && val != redactedText && r.queryDenied(name) {
			b.WriteString(name)
			b.WriteByte('=')
			b.WriteString(redactedText)
			kinds = RedactURL
		} else {
			b.WriteString(part)
		}
		if i < len(q) {
			b.WriteByte(q[i])
		}
		start = i + 1
	}
	if kinds == 0 {
		return q, 0
	}
	return b.String(), kinds
}

// scanTokens gives every whitespace-separated URL-shaped token of s (one that
// starts with "/" or contains "://") the URL treatment, keeping the rest of s
// and its whitespace as written.
func (r *Redactor) scanTokens(s string) (string, RedactionKind) {
	if strings.IndexByte(s, '/') < 0 {
		return s, 0
	}
	var b strings.Builder
	var kinds RedactionKind
	last := 0
	for i := 0; i < len(s); {
		if isSpace(s[i]) {
			i++
			continue
		}
		j := i
		for j < len(s) && !isSpace(s[j]) {
			j++
		}
		if tok := s[i:j]; strings.HasPrefix(tok, "/") || strings.Contains(tok, "://") {
			if out, k := r.urlKinds(tok); k != 0 {
				b.WriteString(s[last:i])
				b.WriteString(out)
				last, kinds = j, kinds|k
			}
		}
		i = j
	}
	if kinds == 0 {
		return s, 0
	}
	b.WriteString(s[last:])
	return b.String(), kinds
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\v' || c == '\f'
}

func isAlpha(c byte) bool { return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' }

// isSchemeChar is RFC 3986's scheme alphabet: ALPHA / DIGIT / "+" / "-" / ".".
func isSchemeChar(c byte) bool {
	return isAlpha(c) || '0' <= c && c <= '9' || c == '+' || c == '-' || c == '.'
}
