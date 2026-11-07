// file: core/log/redactor.go
// version: 1.0.0
// guid: 44521f1f-a2ba-4b3c-a385-eb582b10e892
// last-edited: 2026-11-07

package log

import (
	"encoding"
	"fmt"
	"log/slog"
	"net/url"
	"reflect"
	"regexp"
	"slices"
	"strings"
)

// The replacement texts. redactedText is the same "[REDACTED]" the invite
// path rule writes.
const (
	redactedText  = InviteTokenRedacted
	redactedPanic = "[REDACTED:panic]"
	redactedDepth = "[REDACTED:depth]"
)

// Bounds that keep one redaction finite whatever the value holds.
const (
	// maxResolve bounds how many LogValue calls one value may chain, as
	// slog.Value.Resolve does. A value still unresolved after that fails
	// closed.
	maxResolve = 100
	// maxAnyDepth and maxAnyNodes bound the walk of a KindAny value (maps,
	// structs, slices, pointers): a cyclic or very deep value cannot hang or
	// overflow the stack, and a part beyond either bound is
	// "[REDACTED:depth]".
	maxAnyDepth = 8
	maxAnyNodes = 4096
	// maxURLDepth bounds URL handling of a URL inside a query value
	// (?next=/invite/<token>, ?redirect_uri=https://...).
	maxURLDepth = 2
)

// builtinCredentialKeys are credential names ABO already masks beyond the
// spec's DefaultRedactKeys (credentialHeaders in
// internal/server/middleware/recovery_redact.go, secretPairRE in
// internal/metadata/detail_redact.go), normalised as normKey does. They are
// part of every Redactor's deny-list but not of DefaultRedactKeys, which stays
// 05-logging §3's list verbatim.
func builtinCredentialKeys() []string {
	return []string{"proxyauthorization", "xapikey", "cfaccessjwtassertion", "cfaccessclientsecret", "bearer",
		// HTTP Digest's credential parameters (RFC 7616).
		"response", "cnonce", "nonce"}
}

// credentialSuffixes: a normalised key that ends in one of these is denied
// (x-api-key, csrf_token, db.password, http.request.header.authorization).
func credentialSuffixes() []string {
	return []string{"token", "secret", "password", "passwd", "apikey", "authorization", "cookie", "privatekey",
		"credential", "credentials", "sessionid", "bearer", "jwtassertion"}
}

// harmlessSuffixes: a normalised key that ends in one of these names a flag
// about a credential, never the credential, so the suffix rule spares it.
// Exact deny-list entries (DefaultRedactKeys, extra keys) still win.
func harmlessSuffixes() []string {
	return []string{"haspassword", "hassecret", "hastoken", "hasapikey", "hascookie", "hasauthorization",
		"hascredential", "hascredentials"}
}

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
	keys     map[string]struct{} // normalised deny-listed keys (normKey)
	suffixes []string            // credentialSuffixes()
	harmless []string            // harmlessSuffixes()
	query    map[string]struct{} // lower-case credential query names
	urlKeys  map[string]struct{} // lower-case URL keys
	rules    []PathRule
	// authRE finds an HTTP auth scheme word (Bearer, Basic, Digest,
	// Negotiate) followed by a credential in free text; the match ends at the
	// credential's first byte. *regexp.Regexp is safe for concurrent use.
	authRE *regexp.Regexp
}

// NewRedactor builds a Redactor from the defaults (fresh copies) plus
// extraKeys (core.log.redact_keys; added to the deny-list, never replacing it)
// and extraPaths (module path rules, applied after the defaults). A rule with
// a nil Fn redacts nothing and is skipped.
func NewRedactor(extraKeys []string, extraPaths ...PathRule) *Redactor {
	r := &Redactor{
		keys:     map[string]struct{}{},
		suffixes: credentialSuffixes(),
		harmless: harmlessSuffixes(),
		query:    map[string]struct{}{},
		urlKeys:  map[string]struct{}{},
		authRE:   regexp.MustCompile(`(?i)\b(?:bearer|basic|digest|negotiate)\s+\S`),
	}
	keys := append(DefaultRedactKeys(), builtinCredentialKeys()...)
	for _, k := range append(keys, extraKeys...) {
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

// keyDenied reports whether key names a credential, normalised (normKey):
// the whole key or its last dot-separated segment is on the deny-list, or the
// key ends in a credential suffix (credentialSuffixes) and not in a harmless
// one (harmlessSuffixes).
func (r *Redactor) keyDenied(key string) bool {
	n := normKey(key)
	if n == "" {
		return false
	}
	if _, ok := r.keys[n]; ok {
		return true
	}
	if i := strings.LastIndexByte(key, '.'); i >= 0 && i < len(key)-1 {
		if _, ok := r.keys[normKey(key[i+1:])]; ok {
			return true
		}
	}
	for _, h := range r.harmless {
		if strings.HasSuffix(n, h) {
			return false
		}
	}
	for _, s := range r.suffixes {
		if strings.HasSuffix(n, s) {
			return true
		}
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
// the URL and path treatment. Any other KindAny value is walked (anyValue).
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
		if out, k, ok := r.anyValue(v.Any()); !ok {
			return slog.String(a.Key, redactedPanic), RedactValuer
		} else if k != 0 {
			return slog.Attr{Key: a.Key, Value: out}, k
		}
	}
	return slog.Attr{Key: a.Key, Value: v}, 0
}

// anyValue redacts a KindAny value and reports what it replaced; with nothing
// replaced the caller keeps the original. ok is false when one of the value's
// own methods (String, Error, MarshalText) panicked.
//
// The value is walked (walk) to maxAnyDepth and maxAnyNodes. When the walk
// replaced nothing, the text a handler may print instead (String(),
// MarshalText(), or %+v for a composite) still goes through the text scan,
// and a redacted text replaces the value.
func (r *Redactor) anyValue(x any) (out slog.Value, k RedactionKind, ok bool) {
	defer func() {
		if recover() != nil {
			out, k, ok = slog.Value{}, 0, false
		}
	}()
	rv := reflect.ValueOf(x)
	if !rv.IsValid() || (rv.Kind() == reflect.Pointer || rv.Kind() == reflect.Interface) && rv.IsNil() {
		return slog.AnyValue(x), 0, true
	}
	budget := maxAnyNodes
	if w, k := r.walk(rv, 0, &budget); k != 0 {
		if s, isText := w.(string); isText {
			return slog.StringValue(s), k, true
		}
		return slog.AnyValue(w), k, true
	}
	if text, has := printedText(x); has {
		if s, k := r.scanTokens(text); k != 0 {
			return slog.StringValue(s), k, true
		}
	}
	return slog.AnyValue(x), 0, true
}

// printedText is the text a handler may print for x when it is not walked:
// String(), MarshalText() (the text handler prefers it), or %+v for a
// composite.
func printedText(x any) (string, bool) {
	if s, ok := methodText(x); ok {
		return s, true
	}
	switch reflect.ValueOf(x).Kind() {
	case reflect.Struct, reflect.Pointer, reflect.Map, reflect.Slice, reflect.Array, reflect.Interface:
		return fmt.Sprintf("%+v", x), true
	}
	return "", false
}

// methodText is the text x prints through its own method: Error(),
// String() or MarshalText().
func methodText(x any) (string, bool) {
	switch v := x.(type) {
	case error:
		return v.Error(), true
	case fmt.Stringer:
		return v.String(), true
	case encoding.TextMarshaler:
		b, err := v.MarshalText()
		if err != nil {
			return "", false
		}
		return string(b), true
	}
	return "", false
}

// walk redacts rv and returns the rebuilt value and what it replaced; with
// nothing replaced the result is discarded by the caller. At every level:
//
//   - an opaque leaf (*url.URL or url.URL: URL handling of its text; an error,
//     fmt.Stringer or encoding.TextMarshaler: the text scan of what it prints)
//     becomes the redacted text when anything was replaced; otherwise it is
//     walked as its kind below;
//   - pointers and interfaces are followed;
//   - strings and []byte get the text scan;
//   - map entries (any key type, keyed by its printed form) and exported
//     struct fields go through the key rule, a field by its json name and by
//     its Go name; a struct that has unexported fields and redacted nothing
//     else has its %+v text scanned, since a handler prints those fields;
//   - slice and array elements are walked;
//   - a part beyond maxAnyDepth or maxAnyNodes is "[REDACTED:depth]".
//
// A redacted map or struct becomes a map[string]any and a slice a []any, with
// unchanged branches kept as they were.
func (r *Redactor) walk(rv reflect.Value, depth int, budget *int) (any, RedactionKind) {
	if !rv.IsValid() {
		return nil, 0
	}
	*budget--
	if depth > maxAnyDepth || *budget < 0 {
		return redactedDepth, RedactValuer
	}
	switch rv.Kind() {
	case reflect.Pointer, reflect.Interface, reflect.Map, reflect.Slice:
		if rv.IsNil() {
			return valueOf(rv), 0
		}
	}
	if s, k := r.opaqueText(rv); k != 0 {
		return s, k
	}
	switch rv.Kind() {
	case reflect.Pointer, reflect.Interface:
		return r.walk(rv.Elem(), depth+1, budget)
	case reflect.String:
		if s, k := r.scanTokens(rv.String()); k != 0 {
			return s, k
		}
	case reflect.Map:
		m := make(map[string]any, rv.Len())
		var kinds RedactionKind
		for it := rv.MapRange(); it.Next(); {
			key := it.Key()
			name := ""
			if key.Kind() == reflect.String {
				name = key.String()
			} else {
				name = fmt.Sprint(valueOf(key))
			}
			kinds |= r.walkEntry(m, name, "", it.Value(), depth, budget)
		}
		return m, kinds
	case reflect.Struct:
		t := rv.Type()
		m := make(map[string]any, t.NumField())
		var kinds RedactionKind
		unexported := false
		for i := range t.NumField() {
			f := t.Field(i)
			if !f.IsExported() {
				unexported = true
				continue
			}
			name := f.Name
			if tag, _, _ := strings.Cut(f.Tag.Get("json"), ","); tag == "-" {
				continue
			} else if tag != "" {
				name = tag
			}
			kinds |= r.walkEntry(m, name, f.Name, rv.Field(i), depth, budget)
		}
		if kinds == 0 && unexported && rv.CanInterface() {
			if s, k := r.scanTokens(fmt.Sprintf("%+v", rv.Interface())); k != 0 {
				return s, k
			}
		}
		return m, kinds
	case reflect.Slice, reflect.Array:
		if rv.Type().Elem().Kind() == reflect.Uint8 {
			b := make([]byte, rv.Len())
			reflect.Copy(reflect.ValueOf(b), rv)
			if s, k := r.scanTokens(string(b)); k != 0 {
				return s, k
			}
			break
		}
		out := make([]any, rv.Len())
		var kinds RedactionKind
		for i := range out {
			w, k := r.walk(rv.Index(i), depth+1, budget)
			if k == 0 {
				w = valueOf(rv.Index(i))
			}
			out[i], kinds = w, kinds|k
		}
		return out, kinds
	}
	return valueOf(rv), 0
}

// opaqueText redacts what an opaque leaf prints (see walk); the kinds are
// empty when it is not one or nothing was replaced.
func (r *Redactor) opaqueText(rv reflect.Value) (string, RedactionKind) {
	if !rv.CanInterface() {
		return "", 0
	}
	switch v := rv.Interface().(type) {
	case *url.URL:
		return r.urlKinds(v.String(), 0)
	case url.URL:
		return r.urlKinds(v.String(), 0)
	}
	if text, ok := methodText(rv.Interface()); ok {
		return r.scanTokens(text)
	}
	return "", 0
}

// walkEntry sets m[key] to v redacted and returns what it replaced. A
// non-zero value is "[REDACTED]" when key, or alt (a struct field's Go name
// beside its json name), is denied; a zero value hides nothing; an unchanged
// value is kept as it was.
func (r *Redactor) walkEntry(m map[string]any, key, alt string, v reflect.Value, depth int, budget *int) RedactionKind {
	if (r.keyDenied(key) || alt != "" && r.keyDenied(alt)) && !v.IsZero() {
		m[key] = redactedText
		return RedactKey
	}
	w, k := r.walk(v, depth+1, budget)
	if k == 0 {
		w = valueOf(v)
	}
	m[key] = w
	return k
}

// valueOf is rv as an interface value, or nil when it cannot be one.
func valueOf(rv reflect.Value) any {
	if rv.IsValid() && rv.CanInterface() {
		return rv.Interface()
	}
	return nil
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
	out, _ := r.urlKinds(raw, 0)
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
//     then the text scan;
//   - any other key: the text scan (scanTokens), which covers a span name
//     "GET /invite/<token>", an error's "Get \"https://...?token=...\"",
//     "token=<t>" pairs and "Bearer <t>".
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
		out, k := r.maskQuery(q, 0)
		if hadMark {
			out = "?" + out
		}
		return out, k
	}
	if _, ok := r.urlKeys[lk]; ok {
		out, k := r.urlKinds(value, 0)
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
//
// depth counts URLs nested in query values (maskQuery), up to maxURLDepth.
func (r *Redactor) urlKinds(raw string, depth int) (string, RedactionKind) {
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
		// Text before the scheme is kept (a quote, "next="), but not text
		// that is already a path or query: "/c?n=https://..." is a path
		// whose query holds a URL, not a URL.
		if s < i && isAlpha(raw[s]) && !strings.ContainsAny(raw[:s], "/?#") {
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
		q, k := r.maskQuery(query, depth)
		kinds |= k
		b.WriteByte('?')
		b.WriteString(q)
	}
	if hasFrag {
		f, k := r.maskQuery(frag, depth)
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
// value to replace. Another parameter's value that holds a URL or a path
// (?next=/invite/<token>) gets URL handling (nestedValue).
func (r *Redactor) maskQuery(q string, depth int) (string, RedactionKind) {
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
		name, val, hasValue := strings.Cut(part, "=")
		switch {
		case hasValue && val != redactedText && r.queryDenied(name):
			b.WriteString(name)
			b.WriteByte('=')
			b.WriteString(redactedText)
			kinds |= RedactURL
		default:
			if out, k := r.nestedValue(val, depth); hasValue && k != 0 {
				b.WriteString(name)
				b.WriteByte('=')
				b.WriteString(out)
				kinds |= k
				break
			}
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

// nestedValue redacts a query value that holds a URL or a path (?next=,
// ?redirect_uri=) and reports what it replaced. The value is percent-decoded
// once first (leniently: an invalid escape stays as written, so it cannot
// hide a path), so an encoded %2Finvite%2F<token> is seen. A plain value gets
// URL handling in place; an encoded one that needed any redaction is replaced
// whole with "[REDACTED]" (re-encoding a redacted URL is not worth the risk).
// At maxURLDepth a value that still holds a URL or a path fails closed to
// "[REDACTED]".
func (r *Redactor) nestedValue(val string, depth int) (string, RedactionKind) {
	if val == "" || val == redactedText {
		return val, 0
	}
	dec := val
	if strings.ContainsAny(val, "%+") {
		if d, err := url.QueryUnescape(val); err == nil {
			dec = d
		} else {
			dec = unescapeLenient(val)
		}
	}
	at := urlStart(dec)
	if at < 0 {
		return val, 0
	}
	if depth >= maxURLDepth {
		return redactedText, RedactURL
	}
	nested, k := r.urlKinds(dec[at:], depth+1)
	switch {
	case k == 0:
		return val, 0
	case dec != val:
		return redactedText, k
	}
	return val[:at] + nested, k
}

// scanTokens is the text scan for a string that is not URL-keyed: free text
// such as a span name, an error message or %+v output. It keeps s as written
// except for what it replaces:
//
//  1. URLs and paths: in every whitespace-separated token, the URL or path
//     that starts at the token, at a "<scheme>://", or at a '/' right after
//     one of `"'<([{=:` and a backtick (a quoted, bracketed or key=/path
//     form) gets URL handling. Trailing punctuation stays with the last
//     segment or query value, which is replaced whole.
//  2. Pairs: "name=value" and "name:value" (a log line's or %+v's field)
//     whose name is denied by the key rule have the value replaced
//     (RedactKey), as do "Bearer <credential>" values.
func (r *Redactor) scanTokens(s string) (string, RedactionKind) {
	out, kinds := r.scanURLs(s)
	out, k := r.scanPairs(out)
	return out, kinds | k
}

func (r *Redactor) scanURLs(s string) (string, RedactionKind) {
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
		tok := s[i:j]
		if at := urlStart(tok); at >= 0 {
			if out, k := r.urlKinds(tok[at:], 0); k != 0 {
				b.WriteString(s[last : i+at])
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

// urlStart is where the URL or path in tok starts, or -1: 0 when tok starts
// with '/' or holds a "<scheme>://" with no path before it (urlKinds keeps
// any text before the scheme), else the first '/' right after a quote,
// bracket, '=' or ':'.
func urlStart(tok string) int {
	if strings.HasPrefix(tok, "/") {
		return 0
	}
	if i := strings.Index(tok, "://"); i > 0 {
		s := i
		for s > 0 && isSchemeChar(tok[s-1]) {
			s--
		}
		// Not when a path comes first: "next=/invite/<t>?r=https://..." starts
		// at its '/', below.
		if !strings.ContainsAny(tok[:s], "/?#") {
			return 0
		}
	}
	for k := 1; k < len(tok); k++ {
		if tok[k] == '/' && strings.IndexByte("\"'<([{=:`", tok[k-1]) >= 0 {
			return k
		}
	}
	return -1
}

func (r *Redactor) scanPairs(s string) (string, RedactionKind) {
	if strings.IndexAny(s, "=:") < 0 && !r.authRE.MatchString(s) {
		return s, 0
	}
	var spans [][2]int // value ranges to replace
	// Every '=' and ':' is a candidate separator on its own, so one pair
	// cannot hide the next ("1:token=<t>" in a %+v of a map).
	for i := 0; i < len(s); i++ {
		if s[i] != '=' && s[i] != ':' {
			continue
		}
		n := i
		for n > 0 && isNameChar(s[n-1]) {
			n--
		}
		if n == i || !r.keyDenied(s[n:i]) {
			continue
		}
		v := i + 1
		spaced := false
		if s[i] == ':' {
			// "Authorization: <value>", a header written as text.
			for v < len(s) && (s[v] == ' ' || s[v] == '\t') {
				v, spaced = v+1, true
			}
		}
		if v < len(s) && (s[v] == '"' || s[v] == '\'') {
			v++
		}
		end := v
		if strings.HasSuffix(normKey(s[n:i]), "cookie") {
			// A Cookie or Set-Cookie value is a list ("a=1; session=<t>")
			// whose names are not credentials themselves: all of it, to the
			// end of the line.
			for end < len(s) && s[end] != '\n' && s[end] != '\r' {
				end++
			}
		} else {
			for end < len(s) && isValueChar(s[end]) {
				end++
			}
			// After a space, a word that is itself "name:" or "name=..." is
			// the next pair (an empty field in %+v output:
			// "{Password: URL:https://...}"), not this name's value; its own
			// separator is checked on its own. Anything else (base64 padding,
			// a URL-ish secret) is this name's value and is replaced.
			if spaced && looksLikePair(s[v:end]) {
				continue
			}
		}
		if end > v {
			spans = append(spans, [2]int{v, end})
		}
	}
	// "<scheme> <credential>" for the HTTP auth schemes: the scheme word is
	// kept and everything after it is replaced, to the end of the line, or
	// to the closing quote when the scheme word follows a quote. A Digest
	// credential is a list (username=, response=, nonce=, ...), so the
	// first word alone would not do.
	for _, m := range r.authRE.FindAllStringIndex(s, -1) {
		start, end := m[1]-1, len(s)
		if j := strings.IndexAny(s[start:], "\r\n"); j >= 0 {
			end = start + j
		}
		if m[0] > 0 && (s[m[0]-1] == '"' || s[m[0]-1] == '\'' || s[m[0]-1] == '`') {
			if j := strings.IndexByte(s[start:end], s[m[0]-1]); j >= 0 {
				end = start + j
			}
		}
		if s[start:end] != redactedText {
			spans = append(spans, [2]int{start, end})
		}
	}
	if len(spans) == 0 {
		return s, 0
	}
	slices.SortFunc(spans, func(a, b [2]int) int { return a[0] - b[0] })
	var b strings.Builder
	last := 0
	for _, sp := range spans {
		if sp[0] < last {
			// Overlaps one already replaced: extend it.
			last = max(last, sp[1])
			continue
		}
		b.WriteString(s[last:sp[0]])
		b.WriteString(redactedText)
		last = sp[1]
	}
	b.WriteString(s[last:])
	return b.String(), RedactKey
}

// looksLikePair reports whether w starts with a field name and a separator
// ("URL:https://...", "Title:Hush", "page=2"): a letter or '_', name
// characters, then ':' or '=' that is not '=' padding ("abc==", "abc=").
func looksLikePair(w string) bool {
	if w == "" || !(isAlpha(w[0]) || w[0] == '_') {
		return false
	}
	i := 1
	for i < len(w) && isNameChar(w[i]) {
		i++
	}
	if i >= len(w) {
		return false
	}
	switch w[i] {
	case ':':
		return !strings.HasPrefix(w[i+1:], "//") // "https://..." is a URL

	case '=':
		return i+1 < len(w) && w[i+1] != '='
	}
	return false
}

// isNameChar is the alphabet of a pair's name: letters, digits, '_', '.', '-'.
func isNameChar(c byte) bool {
	return isAlpha(c) || '0' <= c && c <= '9' || c == '_' || c == '.' || c == '-'
}

// isValueChar is the alphabet of a pair's value: up to whitespace, a quote,
// an angle bracket, '&', ';', ',' or a bracket. A value never starts with
// '[', so an already-redacted "[REDACTED]" is not replaced again.
func isValueChar(c byte) bool {
	return !isSpace(c) && strings.IndexByte("\"'<>&;,()[]{}", c) < 0
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\v' || c == '\f'
}

func isAlpha(c byte) bool { return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' }

// isSchemeChar is RFC 3986's scheme alphabet: ALPHA / DIGIT / "+" / "-" / ".".
func isSchemeChar(c byte) bool {
	return isAlpha(c) || '0' <= c && c <= '9' || c == '+' || c == '-' || c == '.'
}
