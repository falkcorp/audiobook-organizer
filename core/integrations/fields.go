// file: core/integrations/fields.go
// version: 1.1.0
// guid: cbc1be32-ff91-4789-be1d-70da64f29e97
// last-edited: 2026-11-11

package integrations

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"net"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/secrets"
)

// What the Registry reads from an adapter's Meta.Settings (config.Schema,
// 05-config §1.10.2). The schema is the one field list (M1.3 Meta), so every
// field class is read from it, never declared twice:
//
//   - a secret field is a property marked x-mediacore-secret (a secrets.Ref
//     field, reflected as a write-only string);
//   - a URL-typed field is a string property of format "uri" (config's
//     schema:"format=uri" tag); its origin is scheme://host:port;
//   - a host-and-port adapter (core.email, 05-notify §3.1) has properties
//     named "host" and "port" and no URL field named host; the pair is one
//     destination, keyed "host" as M1.3's StoredInstance comment keys it, of
//     origin smtp://host:port (M1.3, M1.4 step 2: the one host-and-port
//     adapter the spec names is core.email; only equality of origins is
//     ever compared). Its origin is set when either part is set, and a part
//     that is unset renders empty (the adapter's default for that part), so
//     a change of the port alone is a change of the destination.
//
// Every other property whose name reads as a destination (url, *_url, uri,
// endpoint, host, ...; urlLike) is refused, as is a secret, URL-typed or
// destination-named property below the top level: the Registry binds
// secrets and Local only to the destinations it can see, so a destination
// it cannot see would let a stored secret follow a URL change (SEC-15).
//
// The adapter's built-in default of a destination field is "the field is
// unset": with neither a stored nor an operator value the adapter dials its
// own default, so the unset origin "" stands for it.

// hostField and portField are the host-and-port destination's properties.
const (
	hostField = "host"
	portField = "port"
)

// hostPortScheme is the scheme of a host-and-port destination's origin
// (M1.3 StoredInstance: core.email's origin is smtp://host:port).
const hostPortScheme = "smtp"

// prop is one settings property.
type prop struct {
	types  []string // JSON Schema type(s)
	format string
	secret bool
}

// fieldSet is one adapter's settings, classified.
type fieldSet struct {
	props    map[string]prop
	secrets  []string // sorted
	urls     []string // sorted
	hostPort bool
	required map[string]bool // the schema's top-level "required" list
}

// schemaNode is the part of one JSON Schema object the Registry reads: a
// property's class, and every place a nested property can hide.
type schemaNode struct {
	Type       json.RawMessage            `json:"type"`
	Format     string                     `json:"format"`
	Secret     bool                       `json:"x-mediacore-secret"`
	Properties map[string]json.RawMessage `json:"properties"`
	Required   []string                   `json:"required"`
	// Nested schemas: an object's or array's members and the subschemas.
	Items                json.RawMessage            `json:"items"`
	PrefixItems          []json.RawMessage          `json:"prefixItems"`
	AdditionalProperties json.RawMessage            `json:"additionalProperties"`
	PatternProperties    map[string]json.RawMessage `json:"patternProperties"`
	Defs                 map[string]json.RawMessage `json:"$defs"`
	AnyOf                []json.RawMessage          `json:"anyOf"`
	OneOf                []json.RawMessage          `json:"oneOf"`
	AllOf                []json.RawMessage          `json:"allOf"`
}

// subschemas are n's nested schemas (not its own properties).
func (n schemaNode) subschemas() []json.RawMessage {
	all := slices.Concat(n.PrefixItems, n.AnyOf, n.OneOf, n.AllOf, []json.RawMessage{n.Items, n.AdditionalProperties})
	for _, m := range []map[string]json.RawMessage{n.PatternProperties, n.Defs} {
		for _, k := range slices.Sorted(maps.Keys(m)) {
			all = append(all, m[k])
		}
	}
	out := all[:0]
	for _, raw := range all {
		if len(bytes.TrimSpace(raw)) > 0 {
			out = append(out, raw)
		}
	}
	return out
}

// decodeNode decodes raw as a schema object; a boolean schema, null or an
// absent schema is the zero node.
func decodeNode(raw json.RawMessage) (schemaNode, error) {
	var n schemaNode
	t := bytes.TrimSpace(raw)
	if len(t) == 0 || t[0] != '{' {
		return n, nil
	}
	err := json.Unmarshal(t, &n)
	return n, err
}

// checkNested refuses a secret, URL-typed or destination-named property
// anywhere below the top level of the schema node at path (M1.3: secret
// fields and destinations are top-level settings properties).
func checkNested(path string, n schemaNode) error {
	for _, name := range slices.Sorted(maps.Keys(n.Properties)) {
		child, err := decodeNode(n.Properties[name])
		if err != nil {
			return invalidf("settings property %s.%s: %v", path, name, err)
		}
		if child.Secret || secretLike(name) || urlLike(name) || child.Format == "uri" {
			return invalidf("settings property %s.%s is a secret or destination field below the top level; secret and destination fields are top-level settings properties", path, name)
		}
		if err := checkNested(path+"."+name, child); err != nil {
			return err
		}
	}
	for _, raw := range n.subschemas() {
		child, err := decodeNode(raw)
		if err != nil {
			return invalidf("settings property %s: %v", path, err)
		}
		if child.Secret || child.Format == "uri" {
			return invalidf("settings property %s holds a secret or URL-typed member; secret and destination fields are top-level string properties", path)
		}
		if err := checkNested(path, child); err != nil {
			return err
		}
	}
	return nil
}

// analyze classifies s's top-level properties. A secret-named property that
// is not a secret field fails (M1.4 step 1: "a Settings schema with a secret
// field lacking Secret: true fails boot"), as does a secret field whose name
// cannot be a secret name's last segment.
func analyze(s config.Schema) (fieldSet, error) {
	raw := s.JSON()
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return fieldSet{}, invalidf("Meta.Settings is the zero Schema; an adapter without settings uses config.SchemaFor[struct{}]()")
	}
	var doc schemaNode
	if err := json.Unmarshal(raw, &doc); err != nil {
		return fieldSet{}, invalidf("Meta.Settings is not a JSON Schema object: %v", err)
	}
	fs := fieldSet{props: make(map[string]prop, len(doc.Properties)), required: make(map[string]bool, len(doc.Required))}
	for _, name := range doc.Required {
		fs.required[name] = true
	}
	// The document's own subschemas ($defs and the like) hold no top-level
	// property: whatever they declare is nested.
	if err := checkNested("$", schemaNode{AnyOf: doc.AnyOf, OneOf: doc.OneOf, AllOf: doc.AllOf, Defs: doc.Defs, PatternProperties: doc.PatternProperties, AdditionalProperties: doc.AdditionalProperties}); err != nil {
		return fieldSet{}, err
	}
	for _, name := range slices.Sorted(maps.Keys(doc.Properties)) {
		p, err := decodeNode(doc.Properties[name])
		if err != nil {
			return fieldSet{}, invalidf("settings property %q: %v", name, err)
		}
		if err := checkNested(name, p); err != nil {
			return fieldSet{}, err
		}
		pr := prop{format: p.Format, secret: p.Secret}
		if len(p.Type) > 0 {
			var one string
			if json.Unmarshal(p.Type, &one) == nil {
				pr.types = []string{one}
			} else if err := json.Unmarshal(p.Type, &pr.types); err != nil {
				return fieldSet{}, invalidf("settings property %q: type: %v", name, err)
			}
		}
		switch {
		case pr.secret:
			if !validSecretSegment(name) {
				return fieldSet{}, invalidf("secret field %q: a secret name's last segment must match [a-z][a-z0-9_-]{0,63}", name)
			}
			fs.secrets = append(fs.secrets, name)
		case secretLike(name):
			return fieldSet{}, invalidf("settings property %q looks like a secret but is not a secrets.Ref field (x-mediacore-secret)", name)
		case pr.format == "uri":
			fs.urls = append(fs.urls, name)
		}
		fs.props[name] = pr
	}
	if h, ok := fs.props[hostField]; ok && !h.secret && h.format != "uri" {
		if p, ok := fs.props[portField]; ok && !p.secret {
			fs.hostPort = true
		}
	}
	// A destination-named property is a URL-typed string field or the host
	// of the host-and-port pair; anything else would be a destination the
	// Registry cannot see (SEC-15), refused like a secret-named property
	// that is not a secret field.
	for _, name := range slices.Sorted(maps.Keys(fs.props)) {
		p := fs.props[name]
		switch {
		case !urlLike(name) || p.secret:
		case p.format == "uri" && slices.Contains(p.types, "string"):
		case name == hostField && fs.hostPort:
		default:
			return fieldSet{}, invalidf("settings property %q reads as a destination but is not a URL-typed string field (schema:\"format=uri\") nor the host of a host-and-port pair; the Registry binds secrets and Local only to destinations it can see", name)
		}
	}
	slices.Sort(fs.secrets)
	slices.Sort(fs.urls)
	return fs, nil
}

// urlLike reports a property name that reads as a destination: a name, or
// a '_'/'-' separated part of one, that is url, uri, endpoint, host,
// hostname, webhook, href or server (plurals included), or that ends in
// "url" or "uri" (baseurl, webhookurl).
func urlLike(name string) bool {
	for _, part := range strings.FieldsFunc(name, func(r rune) bool { return r == '_' || r == '-' }) {
		switch strings.TrimSuffix(part, "s") {
		case "url", "uri", "endpoint", "host", "hostname", "webhook", "href", "server":
			return true
		}
		if strings.HasSuffix(part, "url") || strings.HasSuffix(part, "uri") {
			return true
		}
	}
	return false
}

// isSecret reports whether field is a secret field.
func (fs fieldSet) isSecret(field string) bool { return fs.props[field].secret }

// destinationKeys are the destination fields' keys, sorted.
func (fs fieldSet) destinationKeys() []string {
	keys := slices.Clone(fs.urls)
	if fs.hostPort {
		keys = append(keys, hostField)
		slices.Sort(keys)
	}
	return keys
}

// origins returns the origin of every destination set in settings; an unset
// destination is absent (the adapter default). The Registry passes it an
// effective view (the stored layer with the operator overlay applied), so
// every layer's scheme, host and port count. The host-and-port destination
// is set when either part is, an unset part rendering empty.
func (fs fieldSet) origins(settings map[string]any) map[string]string {
	out := map[string]string{}
	for _, f := range fs.urls {
		if s, ok := stringValue(settings[f]); ok && s != "" {
			out[f] = originOf(s)
		}
	}
	if fs.hostPort {
		h, _ := stringValue(settings[hostField])
		if p := portString(settings[portField]); h != "" || p != "" {
			out[hostField] = hostPortOrigin(h, p)
		}
	}
	return out
}

// hostPortOrigin renders a host-and-port destination's origin.
func hostPortOrigin(host, port string) string {
	return hostPortScheme + "://" + net.JoinHostPort(strings.ToLower(host), port)
}

// wellFormedHostPort reports whether o is a host-and-port origin as
// hostPortOrigin renders it, either part possibly empty but not both.
func wellFormedHostPort(o string) bool {
	rest, ok := strings.CutPrefix(o, hostPortScheme+"://")
	if !ok {
		return false
	}
	h, p, err := net.SplitHostPort(rest)
	return err == nil && (h != "" || p != "") && hostPortOrigin(h, p) == o
}

// stringValue is v as a string when it is one.
func stringValue(v any) (string, bool) {
	s, ok := v.(string)
	return s, ok
}

// portString renders a port value (a JSON number, an int or a string).
func portString(v any) string {
	switch p := v.(type) {
	case nil:
		return ""
	case string:
		return p
	case float64:
		return strconv.FormatFloat(p, 'f', -1, 64)
	case int:
		return strconv.Itoa(p)
	case int64:
		return strconv.FormatInt(p, 10)
	case json.Number:
		return p.String()
	}
	return fmt.Sprint(v)
}

// defaultPort is the port of a URL scheme that names none.
func defaultPort(scheme string) string {
	switch scheme {
	case "http", "ws":
		return "80"
	case "https", "wss":
		return "443"
	case "smtp":
		return "25"
	case "smtps":
		return "465"
	case "ftp":
		return "21"
	}
	return ""
}

// originOf is raw's origin, scheme://host:port with the scheme's default
// port filled in and scheme and host lower-cased. A value that is not an
// absolute URL with a host is "!" + raw, which equals no real origin.
func originOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Hostname() == "" {
		return "!" + raw
	}
	scheme := strings.ToLower(u.Scheme)
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if port == "" {
		port = defaultPort(scheme)
	}
	if port == "" {
		if strings.Contains(host, ":") {
			return scheme + "://[" + host + "]"
		}
		return scheme + "://" + host
	}
	return scheme + "://" + net.JoinHostPort(host, port)
}

// checkURL refuses a URL-typed value that is not an absolute http(s)-shaped
// URL with a host, or that carries userinfo: a credential belongs in a
// secret field, never in a stored URL (05-config's format=uri rule).
func checkURL(field, raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Hostname() == "" {
		return invalidf("settings field %q: %q is not an absolute URL with a host", field, raw)
	}
	if u.User != nil {
		return invalidf("settings field %q: a URL must not carry userinfo; put the credential in the instance's secret field", field)
	}
	return nil
}

// signature renders a destination origin map as one string: the one origin
// when there is one destination, else "key=origin" pairs in key order.
// LocalOrigin and SecretsOrigin hold it.
func signature(origins map[string]string) string {
	if len(origins) == 0 {
		return ""
	}
	if len(origins) == 1 {
		for _, o := range origins {
			return o
		}
	}
	keys := make([]string, 0, len(origins))
	for k := range origins {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + "=" + origins[k]
	}
	return strings.Join(parts, " ")
}

// sameOrigins reports whether a and b hold the same origin for every key of
// keys (an unset destination is "").
func sameOrigins(keys []string, a, b map[string]string) bool {
	for _, k := range keys {
		if a[k] != b[k] {
			return false
		}
	}
	return true
}

// secretName is the secret name of field of ref (05-config §2.3):
// integrations.<kind>.<instance>.<field>.
func secretName(ref Ref, field string) secrets.Name {
	return secrets.Name("integrations." + string(ref.Kind) + "." + string(ref.Instance) + "." + field)
}

// validSecretSegment reports whether s matches [a-z][a-z0-9_-]{0,63}.
func validSecretSegment(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z':
		case i > 0 && (c >= '0' && c <= '9' || c == '_' || c == '-'):
		default:
			return false
		}
	}
	return true
}

// secretLike reports a property name that names a credential.
func secretLike(name string) bool {
	switch name {
	case "password", "secret", "token", "api_key", "apikey", "passphrase", "client_secret":
		return true
	}
	for _, suf := range []string{"_password", "_secret", "_token", "_api_key", "_apikey", "_passphrase"} {
		if strings.HasSuffix(name, suf) {
			return true
		}
	}
	return false
}

// resupplied reports whether v, sent for the secret named n, counts as a
// re-supplied secret (M1.3 Put, SEC3-4): neither empty nor an echoed or
// mask-shaped value. Echoed: the redaction text of a secrets.Value, the
// secret's own name, or a Ref's JSON form. Mask-shaped: a run of four or
// more mask characters (ABO's mask is "********" plus at most two
// characters, internal/books/store/settings.go MaskSecret).
func resupplied(v secrets.Value, n secrets.Name) bool {
	s := strings.TrimSpace(v.Reveal())
	if s == "" {
		return false
	}
	low := strings.ToLower(s)
	switch low {
	case "[redacted]", "redacted", "<redacted>", "secrets.value([redacted])", strings.ToLower(string(n)):
		return false
	}
	if strings.HasPrefix(low, "{") && strings.Contains(low, `"secret"`) {
		return false
	}
	for _, mask := range []string{"****", "••••", "●●●●", "∙∙∙∙", "····"} {
		if strings.Contains(s, mask) {
			return false
		}
	}
	return true
}

// equalValue reports whether two settings values are the same JSON value.
func equalValue(a, b any) bool {
	ja, ea := json.Marshal(a)
	jb, eb := json.Marshal(b)
	return ea == nil && eb == nil && bytes.Equal(ja, jb)
}

// coerce decodes an operator value for p: env values arrive as JSON strings
// (config does not own a passthrough path's type), so a string bound for an
// integer, number or boolean property is parsed.
func coerce(p prop, raw json.RawMessage) (any, error) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, fmt.Errorf("not JSON: %w", err)
	}
	s, isString := v.(string)
	if !isString {
		return v, nil
	}
	switch {
	case slices.Contains(p.types, "integer"):
		n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("want an integer: %w", err)
		}
		return float64(n), nil
	case slices.Contains(p.types, "number"):
		f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
		if err != nil {
			return nil, fmt.Errorf("want a number: %w", err)
		}
		return f, nil
	case slices.Contains(p.types, "boolean"):
		b, err := strconv.ParseBool(strings.TrimSpace(s))
		if err != nil {
			return nil, fmt.Errorf("want a boolean: %w", err)
		}
		return b, nil
	}
	return s, nil
}

// validateSettings checks the non-secret settings against the schema. Every
// secret field of the schema is given a placeholder string, so a required
// secret neither fails nor passes on its value: secrets are judged by the
// secret rules, and a missing one is a health finding, not a 422 (M1.5).
func validateSettings(s config.Schema, fs fieldSet, settings map[string]any) error {
	doc := make(map[string]any, len(settings)+len(fs.secrets))
	for k, v := range settings {
		if !fs.isSecret(k) {
			doc[k] = v
		}
	}
	for _, k := range fs.secrets {
		doc[k] = "secret"
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return invalidf("settings do not encode: %v", err)
	}
	if err := s.Validate(raw); err != nil {
		return fmt.Errorf("%w: settings: %w", ErrInvalid, err)
	}
	return nil
}
