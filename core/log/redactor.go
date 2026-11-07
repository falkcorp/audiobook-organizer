// file: core/log/redactor.go
// version: 1.1.0
// guid: 44521f1f-a2ba-4b3c-a385-eb582b10e892
// last-edited: 2026-11-07

package log

import (
	"encoding"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"reflect"
	"strings"
)

// The replacement texts. redactedText is the same "[REDACTED]" the invite
// path rule writes.
const (
	redactedText  = InviteTokenRedacted
	redactedPanic = "[REDACTED:panic]"
	redactedDepth = "[REDACTED:depth]"
	// redactedPrefix starts every replacement text. A pair value or auth
	// credential that starts with it was already redacted and is skipped, so
	// redacting twice changes nothing and reports nothing.
	redactedPrefix = "[REDACTED"
)

// Bounds that keep one redaction finite whatever the value holds.
const (
	// maxResolve bounds how many LogValue calls one value may chain, as
	// slog.Value.Resolve does. A value still unresolved after that fails
	// closed.
	maxResolve = 100
	// maxAnyDepth and maxAnyNodes bound the walk of a KindAny value (maps,
	// structs, slices, pointers, nested LogValuers): a cyclic or very deep
	// value cannot hang or overflow the stack, and a part beyond either bound
	// is "[REDACTED:depth]".
	maxAnyDepth = 8
	maxAnyNodes = 4096
	// maxURLDepth bounds URL handling of a URL inside a query value
	// (?next=/invite/<token>, ?redirect_uri=https://...).
	maxURLDepth = 2
	// maxRedactTextBytes bounds the text one StringKinds, URL or text scan
	// examines. The scans are linear; this keeps the constant small for text
	// that comes from a request or an upstream body. Text beyond it is not
	// examined and is replaced with "[REDACTED]" (fail closed).
	maxRedactTextBytes = 64 << 10
	// maxNameBytes bounds how far the pair scan reads back for a name before
	// a separator. A longer run is judged by its last maxNameBytes bytes, so a
	// credential suffix is still seen.
	maxNameBytes = 256
)

// builtinCredentialKeys are credential names ABO already masks beyond the
// spec's DefaultRedactKeys (credentialHeaders in
// internal/server/middleware/recovery_redact.go, secretPairRE in
// internal/metadata/detail_redact.go), normalised as normKey does. They are
// part of every Redactor's deny-list but not of DefaultRedactKeys, which stays
// 05-logging §3's list verbatim. HTTP Digest's response, nonce and cnonce are
// deliberately absent: as keys they would wipe every "response" attribute;
// the auth-scheme scan covers them inside a Digest credential.
func builtinCredentialKeys() []string {
	return []string{"proxyauthorization", "xapikey", "cfaccessjwtassertion", "cfaccessclientsecret", "bearer"}
}

// credentialSuffixes: a normalised key that ends in one of these is denied
// (x-api-key, csrf_token, db.password, http.request.header.authorization,
// aws_secret_access_key), plurals included (cookies, tokens).
func credentialSuffixes() []string {
	return []string{"token", "tokens", "secret", "secrets", "password", "passwords", "passwd", "passphrase",
		"apikey", "secretkey", "accesskey", "authorization", "cookie", "cookies", "privatekey", "credential",
		"credentials", "sessionid", "bearer", "jwtassertion", "jwt"}
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
	// RedactKey: the value of a deny-listed key, a credential in free text,
	// or text beyond maxRedactTextBytes.
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

// keyMatch is how a key matched the deny-list.
type keyMatch uint8

const (
	keyClear  keyMatch = iota // not denied
	keyExact                  // the whole key or its last dotted segment is on the deny-list
	keySuffix                 // only a credential suffix matched
)

// matchKey reports how key matches the deny-list, normalised (normKey): the
// whole key or its last dot-separated segment is on it (keyExact), or the key
// ends in a credential suffix (credentialSuffixes) and not in a harmless one
// (harmlessSuffixes) (keySuffix).
func (r *Redactor) matchKey(key string) keyMatch {
	n := normKey(key)
	if n == "" {
		return keyClear
	}
	if _, ok := r.keys[n]; ok {
		return keyExact
	}
	if i := strings.LastIndexByte(key, '.'); i >= 0 && i < len(key)-1 {
		if _, ok := r.keys[normKey(key[i+1:])]; ok {
			return keyExact
		}
	}
	for _, h := range r.harmless {
		if strings.HasSuffix(n, h) {
			return keyClear
		}
	}
	for _, s := range r.suffixes {
		if strings.HasSuffix(n, s) {
			return keySuffix
		}
	}
	return keyClear
}

// keyDenied reports whether key names a credential at all (any match).
func (r *Redactor) keyDenied(key string) bool { return r.matchKey(key) != keyClear }

// KeyDenied reports whether the key rule replaces a value of kind under key
// whole. An exact deny-list match (the whole normalised key, or its last
// dotted segment) replaces a value of any kind. A match on a credential
// suffix alone (prompt_tokens, cf_cookie, is_secret) does not replace a
// scalar: a bool, a number, a duration or a time carries no credential, and
// those keys are counts and presence flags. The span exporter uses it for
// the attribute types that have no text.
func (r *Redactor) KeyDenied(key string, kind slog.Kind) bool {
	switch r.matchKey(key) {
	case keyExact:
		return true
	case keySuffix:
		return !scalarKind(kind)
	}
	return false
}

// scalarKind reports whether a slog kind is a scalar a suffix match spares.
func scalarKind(k slog.Kind) bool {
	switch k {
	case slog.KindBool, slog.KindInt64, slog.KindUint64, slog.KindFloat64, slog.KindDuration, slog.KindTime:
		return true
	}
	return false
}

// scalarReflect is scalarKind for a reflected value (an interface is looked
// through).
func scalarReflect(v reflect.Value) bool {
	if v.Kind() == reflect.Interface && !v.IsNil() {
		v = v.Elem()
	}
	switch v.Kind() {
	case reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Float32, reflect.Float64:
		return true
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
// replaces a deny-listed key's value (a whole group included; see KeyDenied
// for scalars) with "[REDACTED]", recurses into groups, and gives values
// under URLKeys(), *url.URL and url.URL values, URL-shaped strings and the
// text of an error the URL and path treatment. Any other KindAny value is
// walked (anyValue).
func (r *Redactor) Attr(a slog.Attr) slog.Attr {
	out, _ := r.attr(a)
	return out
}

func (r *Redactor) attr(a slog.Attr) (slog.Attr, RedactionKind) {
	v, ok := resolve(a.Value)
	if !ok {
		return slog.String(a.Key, redactedPanic), RedactValuer
	}
	if r.KeyDenied(a.Key, v.Kind()) {
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
		out, k, changed, ok := r.anyValue(v.Any())
		if !ok {
			return slog.String(a.Key, redactedPanic), RedactValuer
		}
		if changed {
			return slog.Attr{Key: a.Key, Value: out}, k
		}
	}
	return slog.Attr{Key: a.Key, Value: v}, 0
}

// anyValue redacts a KindAny value and reports what it replaced; changed is
// false when the caller should keep the original. ok is false when one of
// the value's own methods (String, Error, MarshalText, MarshalJSON, ...)
// panicked.
//
// The value is walked (walk) to maxAnyDepth and maxAnyNodes. When the walk
// changed nothing, every text a handler may print for the value instead
// (printedTexts, or %+v for a composite) still goes through the text scan,
// and a redacted text replaces the value.
func (r *Redactor) anyValue(x any) (out slog.Value, k RedactionKind, changed, ok bool) {
	defer func() {
		if recover() != nil {
			out, k, changed, ok = slog.Value{}, 0, false, false
		}
	}()
	rv := reflect.ValueOf(x)
	if !rv.IsValid() || (rv.Kind() == reflect.Pointer || rv.Kind() == reflect.Interface) && rv.IsNil() {
		return slog.AnyValue(x), 0, false, true
	}
	budget := maxAnyNodes
	if w, k, ch := r.walk(rv, 0, &budget); ch {
		if s, isText := w.(string); isText {
			return slog.StringValue(s), k, true, true
		}
		return slog.AnyValue(w), k, true, true
	}
	if s, k, ch := r.leafText(x, true); ch {
		return slog.StringValue(s), k, true, true
	}
	return slog.AnyValue(x), 0, false, true
}

// printedTexts is every text a handler may print for x through x's own
// methods, the TextHandler's choice first: AppendText, MarshalText (the
// TextHandler prefers these, and encoding/json uses MarshalText too), a
// Format, Error or String result (what %+v prints), and MarshalJSON (the
// JSONHandler prefers it even over Error). A method that returns an error
// contributes the error's text, which a handler prints in its place. With
// composite set and no such method, a struct, pointer, map, slice, array or
// interface contributes its %+v text, which the TextHandler prints.
func printedTexts(x any, composite bool) []string {
	var out []string
	add := func(b []byte, err error) {
		if err != nil {
			out = append(out, err.Error())
			return
		}
		out = append(out, string(b))
	}
	if ta, ok := x.(encoding.TextAppender); ok {
		add(ta.AppendText(nil))
	}
	if tm, ok := x.(encoding.TextMarshaler); ok {
		add(tm.MarshalText())
	}
	if f, ok := x.(fmt.Formatter); ok {
		out = append(out, fmt.Sprintf("%+v", f))
	}
	if e, ok := x.(error); ok {
		out = append(out, e.Error())
	}
	if s, ok := x.(fmt.Stringer); ok {
		out = append(out, s.String())
	}
	if jm, ok := x.(json.Marshaler); ok {
		add(jm.MarshalJSON())
	}
	if len(out) == 0 && composite {
		switch reflect.ValueOf(x).Kind() {
		case reflect.Struct, reflect.Pointer, reflect.Map, reflect.Slice, reflect.Array, reflect.Interface:
			out = append(out, fmt.Sprintf("%+v", x))
		}
	}
	return out
}

// leafText scans every printed text of x (printedTexts). When any of them
// needs redaction, the result is the first (the TextHandler's) text,
// redacted, with the kinds of all of them: replacing the value with that
// string means no handler calls a method that would have printed another.
func (r *Redactor) leafText(x any, composite bool) (string, RedactionKind, bool) {
	texts := printedTexts(x, composite)
	var kinds RedactionKind
	for _, t := range texts {
		_, k := r.scanTokens(t)
		kinds |= k
	}
	if kinds == 0 {
		return "", 0, false
	}
	out, _ := r.scanTokens(texts[0])
	return out, kinds, true
}

// leafOnly reports whether both stdlib handlers print x through its methods
// and never through its fields: an error (Error, or MarshalJSON) or an
// encoding.TextMarshaler (MarshalText, or MarshalJSON). Any other value with
// methods (a fmt.Stringer, a json.Marshaler alone) has its fields printed by
// one of the handlers, so the walk goes on into them.
func leafOnly(x any) bool {
	switch x.(type) {
	case error, encoding.TextMarshaler:
		return true
	}
	return false
}

// logValuerType is slog.LogValuer's interface type.
func logValuerType() reflect.Type { return reflect.TypeFor[slog.LogValuer]() }

// walk redacts rv and returns the rebuilt value, what it replaced, and
// whether it changed anything (a nested LogValuer replaced by its LogValue
// is a change with no kind). With nothing changed the result is discarded
// by the caller. At every level:
//
//   - a slog.LogValuer is resolved (a panicking or endless one fails closed)
//     and replaced by its value, walked in turn: the handlers print a nested
//     value's fields and never call LogValue (a struct holding a
//     secrets.Value); one in an unexported field cannot be called and fails
//     closed to "[REDACTED]";
//   - *url.URL and url.URL get URL handling of their text;
//   - a value with printing methods has every printed text scanned
//     (leafText); when that redacts, the redacted text replaces it; an error
//     or TextMarshaler stops there (leafOnly), anything else is also walked
//     as its kind below;
//   - pointers and interfaces are followed;
//   - strings and []byte get the text scan;
//   - map entries (any key type, keyed by its printed form) and struct fields
//     (exported ones by json name and Go name, unexported and json:"-" ones
//     by Go name, since the TextHandler prints those too) go through the key
//     rule (a suffix-only match spares a scalar);
//   - slice and array elements are walked;
//   - a part beyond maxAnyDepth or maxAnyNodes is "[REDACTED:depth]".
//
// A changed map or struct becomes a map[string]any and a slice a []any, with
// unchanged branches kept as they were (an unexported one as its %+v text).
func (r *Redactor) walk(rv reflect.Value, depth int, budget *int) (any, RedactionKind, bool) {
	if !rv.IsValid() {
		return nil, 0, false
	}
	*budget--
	if depth > maxAnyDepth || *budget < 0 {
		return redactedDepth, RedactValuer, true
	}
	switch rv.Kind() {
	case reflect.Pointer, reflect.Interface, reflect.Map, reflect.Slice:
		if rv.IsNil() {
			return valueOf(rv), 0, false
		}
	}
	if rv.CanInterface() {
		x := rv.Interface()
		if lv, ok := x.(slog.LogValuer); ok {
			return r.walkLogValuer(lv, depth, budget)
		}
		switch u := x.(type) {
		case *url.URL:
			s, k := r.urlKinds(u.String(), 0)
			return s, k, k != 0
		case url.URL:
			s, k := r.urlKinds(u.String(), 0)
			return s, k, k != 0
		}
		if s, k, ch := r.leafText(x, false); ch {
			return s, k, true
		}
		if leafOnly(x) {
			return x, 0, false
		}
	} else if rv.Type().Implements(logValuerType()) {
		return redactedText, RedactValuer, true
	}
	switch rv.Kind() {
	case reflect.Pointer, reflect.Interface:
		return r.walk(rv.Elem(), depth+1, budget)
	case reflect.String:
		if s, k := r.scanTokens(rv.String()); k != 0 {
			return s, k, true
		}
	case reflect.Map:
		m := make(map[string]any, rv.Len())
		var kinds RedactionKind
		changed := false
		for it := rv.MapRange(); it.Next(); {
			k, ch := r.walkEntry(m, keyName(it.Key()), "", it.Value(), depth, budget)
			kinds, changed = kinds|k, changed || ch
		}
		if changed {
			return m, kinds, true
		}
	case reflect.Struct:
		t := rv.Type()
		m := make(map[string]any, t.NumField())
		var kinds RedactionKind
		changed := false
		for i := range t.NumField() {
			f := t.Field(i)
			name, alt := f.Name, ""
			if tag, _, _ := strings.Cut(f.Tag.Get("json"), ","); f.IsExported() && tag != "" && tag != "-" {
				name, alt = tag, f.Name
			}
			k, ch := r.walkEntry(m, name, alt, rv.Field(i), depth, budget)
			kinds, changed = kinds|k, changed || ch
		}
		if changed {
			return m, kinds, true
		}
	case reflect.Slice, reflect.Array:
		if rv.Type().Elem().Kind() == reflect.Uint8 {
			b := make([]byte, rv.Len())
			for i := range b {
				b[i] = byte(rv.Index(i).Uint())
			}
			if s, k := r.scanTokens(string(b)); k != 0 {
				return s, k, true
			}
			break
		}
		out := make([]any, rv.Len())
		var kinds RedactionKind
		changed := false
		for i := range out {
			w, k, ch := r.walk(rv.Index(i), depth+1, budget)
			if !ch {
				w = valueOf(rv.Index(i))
			}
			out[i], kinds, changed = w, kinds|k, changed || ch
		}
		if changed {
			return out, kinds, true
		}
	}
	return valueOf(rv), 0, false
}

// walkLogValuer replaces a nested LogValuer with its resolved value, redacted
// (slogValue). It always reports a change: the handlers would otherwise
// print the LogValuer's own fields.
func (r *Redactor) walkLogValuer(lv slog.LogValuer, depth int, budget *int) (any, RedactionKind, bool) {
	v, ok := resolve(slog.AnyValue(lv))
	if !ok {
		return redactedPanic, RedactValuer, true
	}
	out, k := r.slogValue(v, depth+1, budget)
	return out, k, true
}

// slogValue is a resolved slog.Value as a plain value for a rebuilt map or
// slice, redacted: a string gets the text scan, a group becomes a
// map[string]any whose attributes go through the key rule, a KindAny value is
// walked, and other kinds are their value.
func (r *Redactor) slogValue(v slog.Value, depth int, budget *int) (any, RedactionKind) {
	*budget--
	if depth > maxAnyDepth || *budget < 0 {
		return redactedDepth, RedactValuer
	}
	switch v.Kind() {
	case slog.KindString:
		s, k := r.scanTokens(v.String())
		return s, k
	case slog.KindGroup:
		var kinds RedactionKind
		m := map[string]any{}
		for _, a := range v.Group() {
			av, ok := resolve(a.Value)
			switch {
			case !ok:
				m[a.Key], kinds = redactedPanic, kinds|RedactValuer
			case r.KeyDenied(a.Key, av.Kind()):
				m[a.Key], kinds = redactedText, kinds|RedactKey
			default:
				w, k := r.slogValue(av, depth+1, budget)
				m[a.Key], kinds = w, kinds|k
			}
		}
		return m, kinds
	case slog.KindAny:
		x := v.Any()
		if w, k, ch := r.walk(reflect.ValueOf(x), depth, budget); ch {
			return w, k
		}
		return x, 0
	}
	return v.Any(), 0
}

// keyName is a map key's printed form.
func keyName(key reflect.Value) string {
	if key.Kind() == reflect.String {
		return key.String()
	}
	return fmt.Sprint(valueOf(key))
}

// walkEntry sets m[key] to v redacted and returns what it replaced and
// whether it changed anything. A non-zero value is "[REDACTED]" when key, or
// alt (a struct field's Go name beside its json name), is denied for its kind
// (a suffix-only match spares a scalar); a zero value hides nothing; an
// unchanged value is kept as it was.
func (r *Redactor) walkEntry(m map[string]any, key, alt string, v reflect.Value, depth int, budget *int) (RedactionKind, bool) {
	if (r.deniedReflect(key, v) || alt != "" && r.deniedReflect(alt, v)) && !v.IsZero() {
		m[key] = redactedText
		return RedactKey, true
	}
	w, k, ch := r.walk(v, depth+1, budget)
	if !ch {
		w = valueOf(v)
	}
	m[key] = w
	return k, ch
}

// deniedReflect is KeyDenied for a reflected value.
func (r *Redactor) deniedReflect(key string, v reflect.Value) bool {
	switch r.matchKey(key) {
	case keyExact:
		return true
	case keySuffix:
		return !scalarReflect(v)
	}
	return false
}

// valueOf is rv as an interface value; a value read through an unexported
// field, which cannot be one, is its %+v text (what the TextHandler prints);
// an invalid value is nil.
func valueOf(rv reflect.Value) any {
	if !rv.IsValid() {
		return nil
	}
	if rv.CanInterface() {
		return rv.Interface()
	}
	return fmt.Sprintf("%+v", rv)
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
// Text beyond maxRedactTextBytes is replaced with "[REDACTED]".
func (r *Redactor) URL(raw string) string {
	if len(raw) > maxRedactTextBytes {
		head, _ := r.urlKinds(raw[:maxRedactTextBytes], 0)
		return head + redactedText
	}
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
//     replaced, then the text scan;
//   - any other key in URLKeys(): the whole value gets URL handling (URL),
//     then the text scan;
//   - any other key: the text scan (scanTokens), which covers a span name
//     "GET /invite/<token>", an error's "Get \"https://...?token=...\"",
//     "token=<t>" and JSON "token":"<t>" pairs and "Bearer <t>".
//
// Text beyond maxRedactTextBytes is not examined and is replaced with
// "[REDACTED]" (RedactKey).
func (r *Redactor) StringKinds(key, value string) (string, RedactionKind) {
	if r.keyDenied(key) {
		if value == redactedText {
			return value, 0
		}
		return redactedText, RedactKey
	}
	if len(value) > maxRedactTextBytes {
		head, k := r.StringKinds(key, value[:maxRedactTextBytes])
		return head + redactedText, k | RedactKey
	}
	lk := strings.ToLower(key)
	if lk == "url.query" {
		q, hadMark := strings.CutPrefix(value, "?")
		out, k := r.maskQuery(q, 0)
		if hadMark {
			out = "?" + out
		}
		out2, k2 := r.scanTokens(out)
		return out2, k | k2
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
//     first '/', '?' or '#'; userinfo (up to its last '@') is removed. When
//     the authority has no '@' but a port that is not a number
//     ("bob:pa/x@host"), a password holding an unencoded '/', '?' or '#'
//     ended it early: everything up to the last '@' is userinfo.
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
		end := authEnd(raw, authStart)
		auth := raw[authStart:end]
		if at := strings.LastIndexByte(auth, '@'); at >= 0 {
			auth = auth[at+1:]
			kinds |= RedactURL
		} else if badPort(auth) {
			if at := strings.LastIndexByte(raw[authStart:], '@'); at >= 0 {
				host := authStart + at + 1
				end = authEnd(raw, host)
				auth = raw[host:end]
				kinds |= RedactURL
			}
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

// authEnd is where the authority that starts at from ends: the first '/',
// '?' or '#', or the end.
func authEnd(raw string, from int) int {
	if j := strings.IndexAny(raw[from:], "/?#"); j >= 0 {
		return from + j
	}
	return len(raw)
}

// badPort reports whether an authority without userinfo has a port that is
// not all digits ("bob:pa"), which no real host has.
func badPort(auth string) bool {
	if strings.HasPrefix(auth, "[") {
		// An IPv6 literal: the port follows "]:".
		j := strings.IndexByte(auth, ']')
		if j < 0 {
			return false
		}
		auth = auth[j+1:]
	}
	i := strings.LastIndexByte(auth, ':')
	if i < 0 {
		return false
	}
	for _, c := range []byte(auth[i+1:]) {
		if c < '0' || c > '9' {
			return true
		}
	}
	return false
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
	for rest := q; ; {
		part, sep, more := rest, "", false
		if j := strings.IndexAny(rest, "&;"); j >= 0 {
			part, sep, rest, more = rest[:j], rest[j:j+1], rest[j+1:], true
		}
		name, val, hasValue := strings.Cut(part, "=")
		switch {
		case hasValue && val != redactedText && r.queryDenied(name):
			b.WriteString(name)
			b.WriteByte('=')
			b.WriteString(redactedText)
			kinds |= RedactURL
		case hasValue:
			out, k := r.nestedValue(val, depth)
			b.WriteString(name)
			b.WriteByte('=')
			b.WriteString(out)
			kinds |= k
		default:
			b.WriteString(part)
		}
		if !more {
			break
		}
		b.WriteString(sep)
	}
	if kinds == 0 {
		return q, 0
	}
	return b.String(), kinds
}

// decodeRounds percent-decodes s up to maxDecodeRounds times while it
// changes (leniently: an invalid escape stays as written, so it cannot hide
// a path) and reports whether the result would still decode, which means
// the escaping is nested deeper than the rounds allow.
func decodeRounds(s string) (dec string, deeper bool) {
	dec = s
	for range maxDecodeRounds {
		if !strings.ContainsAny(dec, "%+") {
			return dec, false
		}
		d, err := url.QueryUnescape(dec)
		if err != nil {
			d = unescapeLenient(dec)
		}
		if d == dec {
			return dec, false
		}
		dec = d
	}
	return dec, strings.IndexByte(dec, '%') >= 0 && unescapeLenient(dec) != dec
}

// nestedValue redacts a query value that holds a URL or a path (?next=,
// ?redirect_uri=) and reports what it replaced. The value is percent-decoded
// up to maxDecodeRounds times first (decodeRounds), so an encoded
// %2Finvite%2F<token> or a double-encoded %252F is seen; a value that would
// still decode after the last round fails closed to "[REDACTED]". A plain
// value gets URL handling in place; an encoded one that needed any redaction
// is replaced whole with "[REDACTED]" (re-encoding a redacted URL is not
// worth the risk). At maxURLDepth a value that still holds a URL or a path
// fails closed to "[REDACTED]".
func (r *Redactor) nestedValue(val string, depth int) (string, RedactionKind) {
	if val == "" || val == redactedText {
		return val, 0
	}
	dec, deeper := decodeRounds(val)
	if deeper {
		return redactedText, RedactURL
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
// such as a span name, an error message, a JSON body or %+v output. It keeps
// s as written except for what it replaces:
//
//  1. URLs and paths: in every whitespace-separated token, the URL or path
//     that starts at the token, at a "<scheme>://", or at a '/' right after
//     one of `"'<([{=:` and a backtick (a quoted, bracketed or key=/path
//     form) gets URL handling. Trailing punctuation stays with the last
//     segment or query value, which is replaced whole. A token with no URL
//     that percent-decodes (up to maxDecodeRounds) to one that needs
//     redaction is replaced whole.
//  2. Pairs and auth credentials (scanPairs).
//
// Text beyond maxRedactTextBytes is not examined and is replaced with
// "[REDACTED]".
func (r *Redactor) scanTokens(s string) (string, RedactionKind) {
	if len(s) > maxRedactTextBytes {
		head, k := r.scanTokens(s[:maxRedactTextBytes])
		return head + redactedText, k | RedactKey
	}
	out, kinds := r.scanURLs(s)
	out, k := r.scanPairs(out)
	return out, kinds | k
}

func (r *Redactor) scanURLs(s string) (string, RedactionKind) {
	if strings.IndexByte(s, '/') < 0 && strings.IndexByte(s, '%') < 0 {
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
		at := urlStart(tok)
		if at >= 0 {
			if out, k := r.urlKinds(tok[at:], 0); k != 0 {
				b.WriteString(s[last : i+at])
				b.WriteString(out)
				last, kinds = j, kinds|k
				i = j
				continue
			}
		}
		if k := r.encodedToken(tok); k != 0 {
			b.WriteString(s[last:i])
			b.WriteString(redactedText)
			last, kinds = j, kinds|k
		}
		i = j
	}
	if kinds == 0 {
		return s, 0
	}
	b.WriteString(s[last:])
	return b.String(), kinds
}

// encodedToken reports what a percent-encoded token hides: decoded up to
// maxDecodeRounds times, a URL or path in it that needs redaction (an error
// quoting %2Finvite%2F<token>), or escaping nested deeper than the rounds
// allow around a URL or path. The caller replaces the whole token.
func (r *Redactor) encodedToken(tok string) RedactionKind {
	if strings.IndexByte(tok, '%') < 0 {
		return 0
	}
	dec, deeper := decodeRounds(tok)
	if dec == tok {
		return 0
	}
	at := urlStart(dec)
	if at < 0 {
		return 0
	}
	if deeper {
		return RedactURL
	}
	_, k := r.urlKinds(dec[at:], 0)
	return k
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

// scanPairs replaces credentials in free text and reports RedactKey when it
// did (see scanPairsN).
func (r *Redactor) scanPairs(s string) (string, RedactionKind) {
	var steps int
	return r.scanPairsN(s, &steps)
}

// scanPairsN is scanPairs counting its steps: every byte any of its loops
// reads. It is one forward pass. At each '=' or ':' (pairAt) and at each
// HTTP auth scheme word (authAt) a candidate either yields a span to
// replace, and the pass resumes after that span, or it does not, and the
// pass moves on one byte; a candidate reads at most its name, one word of
// its value and its own context before it is discarded. So the pass is
// linear in len(s) and the spans come out ordered and disjoint.
//
//   - Pairs: "name=value", "name: value", "name = value", JSON
//     "name":"value" (a quote before the separator is stepped over to find
//     the name, and \"name\":\"value\" inside a quoted string too), whose
//     name is denied by the key rule.
//   - Auth credentials: "Bearer <credential>" anywhere; "Basic", "Negotiate"
//     and "Digest" only in header context (authAt).
func (r *Redactor) scanPairsN(s string, steps *int) (string, RedactionKind) {
	var spans [][2]int
	for i := 0; i < len(s); i++ {
		*steps++
		var sp [2]int
		ok := false
		switch c := s[i]; {
		case c == '=' || c == ':':
			sp, ok = r.pairAt(s, i, steps)
		case (c|0x20 == 'b' || c|0x20 == 'd' || c|0x20 == 'n') && (i == 0 || !isWordChar(s[i-1])):
			sp, ok = r.authAt(s, i, steps)
		}
		if ok {
			spans = append(spans, sp)
			i = sp[1] - 1
		}
	}
	if len(spans) == 0 {
		return s, 0
	}
	var b strings.Builder
	b.Grow(len(s))
	last := 0
	for _, sp := range spans {
		b.WriteString(s[last:sp[0]])
		b.WriteString(redactedText)
		last = sp[1]
	}
	b.WriteString(s[last:])
	return b.String(), RedactKey
}

// pairAt reads the pair whose separator ('=' or ':') is s[i] and returns the
// value's span when the name before it is denied by the key rule.
//
// The name: back over spaces and tabs, then one quote (and a backslash
// before it), then name characters (isNameChar, at most maxNameBytes). A
// doubled separator ("==", "::") is not a pair.
//
// The value: after any whitespace (a newline too, as in YAML), in order:
//   - text starting "[REDACTED" was already redacted: no span;
//   - a quote (", ', a backtick, or \"): up to the matching unescaped quote;
//   - '[', '{', '(' or '<': up to the matching closer, nesting counted (the
//     %v of an http.Header or url.Values is map[token:[<t>]]);
//   - a name ending in cookie or cookies, or in authorization: to the end of
//     the line (a cookie list, or "<scheme> <credential>");
//   - otherwise up to whitespace, a quote, or a closer that closes nothing
//     opened in the value (so "password=ab,<t>" and "ab&<t>" are whole).
//
// An unterminated quote or bracket runs to the end of the line. After
// whitespace, a plain first word that is itself "name:" or "name=..."
// (looksLikePair) is the next pair, as an empty field in %+v is, and no span
// is returned.
func (r *Redactor) pairAt(s string, i int, steps *int) ([2]int, bool) {
	var none [2]int
	if i+1 < len(s) && s[i+1] == s[i] {
		return none, false
	}
	e := i
	for e > 0 && (s[e-1] == ' ' || s[e-1] == '\t') {
		e--
		*steps++
	}
	if e > 0 && isQuote(s[e-1]) {
		e--
		if e > 0 && s[e-1] == '\\' {
			e--
		}
	}
	n := e
	for n > 0 && e-n < maxNameBytes && isNameChar(s[n-1]) {
		n--
		*steps++
	}
	if n == e {
		return none, false
	}
	nk := normKey(s[n:e])
	if !r.keyDenied(s[n:e]) {
		return none, false
	}
	v := i + 1
	spaced := false
	for v < len(s) && isSpace(s[v]) {
		v, spaced = v+1, true
		*steps++
	}
	if v >= len(s) || strings.HasPrefix(s[v:], redactedPrefix) {
		return none, false
	}
	c := s[v]
	start := v
	switch {
	case c == '\\' && v+1 < len(s) && s[v+1] == '"':
		start = v + 2
	case isQuote(c):
		start = v + 1
	}
	// Checked before the value is read, so a run of unterminated
	// "[REDACTED values cannot make the pass read to the end of the line
	// once per candidate.
	if strings.HasPrefix(s[start:], redactedPrefix) {
		return none, false
	}
	cookie := strings.HasSuffix(nk, "cookie") || strings.HasSuffix(nk, "cookies")
	end := start
	switch {
	case start == v+2:
		for end < len(s) && !(s[end] == '\\' && end+1 < len(s) && s[end+1] == '"') {
			end++
			*steps++
		}
	case start == v+1:
		end = closingQuote(s, start, c, steps)
	case closerOf(c) != 0:
		end = matchClose(s, v, c, closerOf(c), steps)
	default:
		end = plainEnd(s, v, steps)
		// A cookie list is pairs by nature ("theme=dark; sid=<t>"), so the
		// empty-field reading never applies to it.
		if spaced && !cookie && looksLikePair(s[v:end]) {
			return none, false
		}
		if cookie || strings.HasSuffix(nk, "authorization") {
			end = lineEnd(s, end, steps)
		}
	}
	if end <= start {
		return none, false
	}
	return [2]int{start, end}, true
}

// closingQuote is the index of the first q at or after from that no
// backslash escapes, or the end of the line when there is none.
func closingQuote(s string, from int, q byte, steps *int) int {
	for j := from; j < len(s); j++ {
		*steps++
		switch s[j] {
		case '\\':
			j++ // the escaped byte is part of the value
		case q:
			return j
		case '\n', '\r':
			return j
		}
	}
	return len(s)
}

// matchClose is the index just past the closer that matches the opener at
// s[at], nesting counted, or the end of the line when there is none.
func matchClose(s string, at int, open, close byte, steps *int) int {
	depth := 0
	for j := at; j < len(s); j++ {
		*steps++
		switch s[j] {
		case open:
			depth++
		case close:
			depth--
			if depth == 0 {
				return j + 1
			}
		case '\n', '\r':
			return j
		}
	}
	return len(s)
}

// plainEnd is where an unquoted value starting at from ends: whitespace, a
// quote, or a closer that closes nothing opened in the value.
func plainEnd(s string, from int, steps *int) int {
	depth := 0
	j := from
	for ; j < len(s); j++ {
		*steps++
		c := s[j]
		if isSpace(c) || isQuote(c) {
			break
		}
		if closerOf(c) != 0 {
			depth++
		} else if isCloser(c) {
			if depth == 0 {
				break
			}
			depth--
		}
	}
	return j
}

// lineEnd is the index of the first '\r' or '\n' at or after from, or the
// end.
func lineEnd(s string, from int, steps *int) int {
	j := from
	for j < len(s) && s[j] != '\n' && s[j] != '\r' {
		j++
		*steps++
	}
	return j
}

// authSchemes are the HTTP auth scheme words authAt reads, lower-case.
func authSchemes() [4]string { return [4]string{"bearer", "basic", "digest", "negotiate"} }

// authAt reads an HTTP auth scheme word that starts at s[i] (at a word
// boundary) followed by whitespace and a credential, and returns the
// credential's span: up to the end of the line, or up to the closing quote
// or bracket when the word follows an opening one. The word itself stays.
//
//   - Bearer fires anywhere: the word is rare in prose and a bearer token
//     has no shape to check.
//   - Basic, Negotiate and Digest fire only in header context: at the start
//     of the text, right after a quote or '[', or after an Authorization,
//     Proxy-Authorization, WWW-Authenticate or Proxy-Authenticate name and
//     ':' or '='. The credential must also be credential-shaped: for Basic
//     and Negotiate a token68 of at least 8 characters that is not a plain
//     word (a capital, digit or symbol after its first letter) and ends at
//     whitespace, ',', ';', a quote, a closer or the end; for Digest an
//     auth-param ("username=..."). So "Basic Economics: A Citizen's Guide",
//     "How to Negotiate Anything" and "failed to compute digest of ..." pass.
func (r *Redactor) authAt(s string, i int, steps *int) ([2]int, bool) {
	var none [2]int
	scheme := ""
	for _, w := range authSchemes() {
		if len(s)-i > len(w) && strings.EqualFold(s[i:i+len(w)], w) && isSpace(s[i+len(w)]) {
			scheme = w
			break
		}
	}
	if scheme == "" {
		return none, false
	}
	c := i + len(scheme)
	for c < len(s) && (s[c] == ' ' || s[c] == '\t') {
		c++
		*steps++
	}
	if c >= len(s) || isSpace(s[c]) || strings.HasPrefix(s[c:], redactedPrefix) {
		return none, false
	}
	opener, ctx := r.authContext(s, i, steps)
	if scheme != "bearer" {
		if !ctx {
			return none, false
		}
		shaped := false
		if scheme == "digest" {
			shaped = authParamAt(s, c, steps)
		} else {
			shaped = token68At(s, c, steps)
		}
		if !shaped {
			return none, false
		}
	}
	end := lineEnd(s, c, steps)
	if cl := closerFor(opener); cl != 0 {
		if j := strings.IndexByte(s[c:end], cl); j >= 0 {
			end = c + j
		}
	}
	if end <= c {
		return none, false
	}
	return [2]int{c, end}, true
}

// authContext reports whether the scheme word at s[i] is in header context
// (see authAt) and the opening quote or bracket right before it, if any.
func (r *Redactor) authContext(s string, i int, steps *int) (opener byte, ok bool) {
	p := i
	for p > 0 && (s[p-1] == ' ' || s[p-1] == '\t') {
		p--
		*steps++
	}
	if p == 0 {
		return 0, true
	}
	switch c := s[p-1]; {
	case isQuote(c) || c == '[':
		return c, true
	case c == ':' || c == '=':
		e := p - 1
		for e > 0 && (s[e-1] == ' ' || s[e-1] == '\t') {
			e--
			*steps++
		}
		if e > 0 && isQuote(s[e-1]) {
			e--
		}
		n := e
		for n > 0 && e-n < maxNameBytes && isNameChar(s[n-1]) {
			n--
			*steps++
		}
		nk := normKey(s[n:e])
		return 0, strings.HasSuffix(nk, "authorization") || strings.HasSuffix(nk, "authenticate")
	}
	return 0, false
}

// token68At reports whether s[c:] starts with a Basic or Negotiate
// credential: RFC 7235's token68 (ALPHA DIGIT - . _ ~ + /, then '='
// padding), at least 8 characters before the padding, not a plain word
// (some character after the first is not a lower-case letter), ending at
// whitespace, ',', ';', a quote, a closer or the end.
func token68At(s string, c int, steps *int) bool {
	j := c
	plain := isAlpha(s[c])
	for j < len(s) && (isAlpha(s[j]) || '0' <= s[j] && s[j] <= '9' || strings.IndexByte("-._~+/", s[j]) >= 0) {
		if j > c && !('a' <= s[j] && s[j] <= 'z') {
			plain = false
		}
		j++
		*steps++
	}
	if j-c < 8 || plain {
		return false
	}
	for j < len(s) && s[j] == '=' {
		j++
		*steps++
	}
	return j == len(s) || isSpace(s[j]) || s[j] == ',' || s[j] == ';' || isQuote(s[j]) || isCloser(s[j])
}

// authParamAt reports whether s[c:] starts with an auth-param, a token and
// '=' ("username=", "realm="), as a Digest credential does.
func authParamAt(s string, c int, steps *int) bool {
	j := c
	for j < len(s) && (isAlpha(s[j]) || '0' <= s[j] && s[j] <= '9' || s[j] == '-' || s[j] == '_') {
		j++
		*steps++
	}
	return j > c && j < len(s) && s[j] == '='
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

// isWordChar is \w: letters, digits and '_'.
func isWordChar(c byte) bool { return isAlpha(c) || '0' <= c && c <= '9' || c == '_' }

func isQuote(c byte) bool { return c == '"' || c == '\'' || c == '`' }

// closerOf is the closer of an opening bracket, or 0.
func closerOf(c byte) byte {
	switch c {
	case '[':
		return ']'
	case '{':
		return '}'
	case '(':
		return ')'
	case '<':
		return '>'
	}
	return 0
}

// closerFor is what ends a credential that follows opener: the same quote,
// or ']' after '[', or 0.
func closerFor(opener byte) byte {
	if isQuote(opener) {
		return opener
	}
	return closerOf(opener)
}

func isCloser(c byte) bool { return c == ']' || c == '}' || c == ')' || c == '>' }

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\v' || c == '\f'
}

func isAlpha(c byte) bool { return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' }

// isSchemeChar is RFC 3986's scheme alphabet: ALPHA / DIGIT / "+" / "-" / ".".
func isSchemeChar(c byte) bool {
	return isAlpha(c) || '0' <= c && c <= '9' || c == '+' || c == '-' || c == '.'
}
