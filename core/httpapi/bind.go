// file: core/httpapi/bind.go
// version: 1.0.2
// guid: 9ce63bfe-21b4-463d-ac55-8db492ac4b2c
// last-edited: 2026-11-07

package httpapi

import (
	"bytes"
	"encoding"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"mime"
	"net/http"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
)

// defaultJSONMaxBytes is core.http.body.json_max_bytes' default (05-http-api
// §10), the cap of a body:"json" field when Route.MaxBody is 0. Stage 11
// (R1-17) applies the configured value in front of it.
const defaultJSONMaxBytes = 1 << 20

// Page limits (05-http-api §4.4; HEAD's internal/httputil/parse.go:74-90).
const (
	defaultPageLimit = 50
	maxPageLimit     = 1000
)

// snakeCase is the one wire-name rule (05-http-api §3.4, FE-12).
func snakeCase() *regexp.Regexp { return regexp.MustCompile(`^[a-z][a-z0-9]*(_[a-z0-9]+)*$`) }

// fieldKind is a bindable field type.
type fieldKind uint8

const (
	kindString fieldKind = iota + 1
	kindInt
	kindUint
	kindBool
	kindStrings
	kindTime
	kindText
)

// boundField is one tagged field of a request type.
type boundField struct {
	index  []int
	source string // "path", "query", "header" or "body"
	name   string
	kind   fieldKind
	goName string
}

// binding is the compiled binding of one request type (05-http-api §3.2):
// built once at registration, so a bad tag is a boot error.
type binding struct {
	typ    reflect.Type
	fields []boundField
	body   *boundField
	pages  [][]int // index paths of embedded PageParams
	cursor [][]int // index paths of embedded CursorParams
}

func textUnmarshaler() reflect.Type { return reflect.TypeFor[encoding.TextUnmarshaler]() }

// compileBinding reads the path, query, header and body tags of t. Every error
// wraps ErrBadBinding, or ErrJSONName for a query name that is not snake_case.
func compileBinding(t reflect.Type, method string) (*binding, error) {
	b := &binding{typ: t}
	if t.Kind() != reflect.Struct {
		return nil, fmt.Errorf("%w: request type %s is not a struct", ErrBadBinding, t)
	}
	var errs []error
	b.walk(t, nil, &errs)
	if b.body != nil && (method == http.MethodGet || method == http.MethodHead) {
		errs = append(errs, fmt.Errorf("%w: %s: body:\"json\" on a %s route", ErrBadBinding, t, method))
	}
	return b, errors.Join(errs...)
}

func (b *binding) walk(t reflect.Type, prefix []int, errs *[]error) {
	snake := snakeCase()
	for i := range t.NumField() {
		f := t.Field(i)
		index := append(append([]int(nil), prefix...), i)
		var tags []string
		for _, src := range []string{"path", "query", "header", "body"} {
			if _, ok := f.Tag.Lookup(src); ok {
				tags = append(tags, src)
			}
		}
		if len(tags) == 0 {
			switch {
			case f.Anonymous && f.Type == reflect.TypeFor[PageParams]():
				b.pages = append(b.pages, index)
				b.walk(f.Type, index, errs)
			case f.Anonymous && f.Type == reflect.TypeFor[CursorParams]():
				b.cursor = append(b.cursor, index)
				b.walk(f.Type, index, errs)
			case f.Anonymous && f.Type.Kind() == reflect.Struct:
				b.walk(f.Type, index, errs)
			}
			continue
		}
		where := t.String() + "." + f.Name
		if len(tags) > 1 {
			*errs = append(*errs, fmt.Errorf("%w: %s has more than one binding tag %v", ErrBadBinding, where, tags))
			continue
		}
		if !f.IsExported() {
			*errs = append(*errs, fmt.Errorf("%w: %s is unexported", ErrBadBinding, where))
			continue
		}
		src := tags[0]
		name := f.Tag.Get(src)
		bf := boundField{index: index, source: src, name: name, goName: f.Name}
		if src == "body" {
			if name != "json" {
				*errs = append(*errs, fmt.Errorf("%w: %s: body:%q (only body:\"json\")", ErrBadBinding, where, name))
				continue
			}
			if b.body != nil {
				*errs = append(*errs, fmt.Errorf("%w: %s: a second body:\"json\" field", ErrBadBinding, where))
				continue
			}
			b.body = &bf
			continue
		}
		if name == "" || strings.ContainsAny(name, " ,;") {
			*errs = append(*errs, fmt.Errorf("%w: %s: %s:%q is not a name", ErrBadBinding, where, src, name))
			continue
		}
		if src == "query" && !snake.MatchString(name) {
			*errs = append(*errs, fmt.Errorf("%w: %s: query parameter %q", ErrJSONName, where, name))
			continue
		}
		k, ok := kindOf(f.Type)
		if !ok || (src != "query" && k == kindStrings) {
			*errs = append(*errs, fmt.Errorf("%w: %s: %s tag on unsupported type %s", ErrBadBinding, where, src, f.Type))
			continue
		}
		bf.kind = k
		b.fields = append(b.fields, bf)
	}
}

func kindOf(t reflect.Type) (fieldKind, bool) {
	if t == reflect.TypeFor[time.Time]() {
		return kindTime, true
	}
	if reflect.PointerTo(t).Implements(textUnmarshaler()) {
		return kindText, true
	}
	switch t.Kind() {
	case reflect.String:
		return kindString, true
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return kindInt, true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return kindUint, true
	case reflect.Bool:
		return kindBool, true
	case reflect.Slice:
		if t.Elem().Kind() == reflect.String {
			return kindStrings, true
		}
	}
	return 0, false
}

// pathNames returns the wildcard names of a ServeMux pattern path.
func pathNames(p string) []string {
	var out []string
	for seg := range strings.SplitSeq(p, "/") {
		if strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}") && seg != "{$}" {
			name := strings.TrimSuffix(strings.TrimPrefix(seg, "{"), "}")
			out = append(out, strings.TrimSuffix(name, "..."))
		}
	}
	return out
}

// checkPath reports a path tag naming no wildcard of the route's pattern.
func (b *binding) checkPath(pattern string) error {
	names := map[string]bool{}
	for _, n := range pathNames(pattern) {
		names[n] = true
	}
	var errs []error
	for _, f := range b.fields {
		if f.source == "path" && !names[f.name] {
			errs = append(errs, fmt.Errorf("%w: %s.%s: path:%q is not a wildcard of %q", ErrBadBinding, b.typ, f.goName, f.name, pattern))
		}
	}
	return errors.Join(errs...)
}

// parameters describes the bound path, query and header fields for OpenAPI.
func (b *binding) parameters() []paramDesc {
	var out []paramDesc
	for _, f := range b.fields {
		out = append(out, paramDesc{name: f.name, in: f.source, schema: paramSchema(f.kind)})
	}
	return out
}

type paramDesc struct {
	name, in string
	schema   *jsonschema.Schema
}

func paramSchema(k fieldKind) *jsonschema.Schema {
	switch k {
	case kindInt, kindUint:
		return &jsonschema.Schema{Type: "integer"}
	case kindBool:
		return &jsonschema.Schema{Type: "boolean"}
	case kindStrings:
		return &jsonschema.Schema{Type: "array", Items: &jsonschema.Schema{Type: "string"}}
	case kindTime:
		return &jsonschema.Schema{Type: "string", Format: "date-time"}
	default:
		return &jsonschema.Schema{Type: "string"}
	}
}

// decode builds a request value from r (05-http-api §3.2, §3.4). Every
// failure is an *Error: 415 unsupported_media_type, 413 body_too_large, 400
// malformed_json or unknown_field, 422 validation_failed for a parameter that
// does not parse.
func (b *binding) decode(w http.ResponseWriter, r *http.Request, rt Route) (reflect.Value, error) {
	v := reflect.New(b.typ).Elem()
	var fields []FieldError
	q := r.URL.Query()
	for _, f := range b.fields {
		var raw []string
		switch f.source {
		case "path":
			if s := r.PathValue(f.name); s != "" {
				raw = []string{s}
			}
		case "query":
			raw = q[f.name]
		case "header":
			if s := r.Header.Get(f.name); s != "" {
				raw = []string{s}
			}
		}
		if len(raw) == 0 {
			continue
		}
		if err := setField(v.FieldByIndex(f.index), f.kind, raw); err != nil {
			fields = append(fields, Field("/"+f.source+"/"+f.name, "invalid_type", err.Error()))
		}
	}
	if len(fields) > 0 {
		return v, Invalid(fields...)
	}
	for _, idx := range b.pages {
		p := v.FieldByIndex(idx).Addr().Interface().(*PageParams)
		p.normalize()
	}
	for _, idx := range b.cursor {
		p := v.FieldByIndex(idx).Addr().Interface().(*CursorParams)
		p.normalize()
	}
	if b.body != nil {
		if err := decodeBody(w, r, rt, v.FieldByIndex(b.body.index)); err != nil {
			return v, err
		}
	}
	return v, nil
}

func setField(fv reflect.Value, k fieldKind, raw []string) error {
	s := raw[0]
	switch k {
	case kindString:
		fv.SetString(s)
	case kindInt:
		n, err := strconv.ParseInt(s, 10, fv.Type().Bits())
		if err != nil {
			return fmt.Errorf("must be an integer")
		}
		fv.SetInt(n)
	case kindUint:
		n, err := strconv.ParseUint(s, 10, fv.Type().Bits())
		if err != nil {
			return fmt.Errorf("must be a non-negative integer")
		}
		fv.SetUint(n)
	case kindBool:
		bv, err := strconv.ParseBool(s)
		if err != nil {
			return fmt.Errorf("must be true or false")
		}
		fv.SetBool(bv)
	case kindStrings:
		fv.Set(reflect.ValueOf(append([]string(nil), raw...)).Convert(fv.Type()))
	case kindTime:
		tv, err := time.Parse(time.RFC3339, s)
		if err != nil {
			return fmt.Errorf("must be an RFC 3339 time")
		}
		fv.Set(reflect.ValueOf(tv))
	case kindText:
		u := fv.Addr().Interface().(encoding.TextUnmarshaler)
		if err := u.UnmarshalText([]byte(s)); err != nil {
			return fmt.Errorf("invalid value")
		}
	}
	return nil
}

func decodeBody(w http.ResponseWriter, r *http.Request, rt Route, dst reflect.Value) error {
	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mt != "application/json" {
		return &Error{Status: http.StatusUnsupportedMediaType, Code: "unsupported_media_type",
			Detail: "Content-Type must be application/json"}
	}
	// Handle refuses a negative MaxBody on a body:"json" route; a value <= 0
	// still gets the default here, so a decoded body is never unbounded.
	limit := rt.MaxBody
	if limit <= 0 {
		limit = defaultJSONMaxBytes
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	if err != nil {
		return bodyError(err, nil, nil)
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	if !rt.Lenient {
		dec.DisallowUnknownFields()
	}
	if err := dec.Decode(dst.Addr().Interface()); err != nil {
		var located []byte
		if !rt.Lenient {
			located = body
		}
		return bodyError(err, located, dst.Type())
	}
	if err := dec.Decode(&json.RawMessage{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return &Error{Status: http.StatusBadRequest, Code: "malformed_json", Detail: "trailing data after the JSON value"}
		}
		return bodyError(err, nil, nil)
	}
	return nil
}

// bodyError maps a body read or decode failure to the client's *Error. No
// detail carries err's own text: encoding/json's messages name Go types and
// struct fields, which are not the API's vocabulary. A type mismatch is 422
// validation_failed with an invalid_type field at the property's JSON
// pointer; a syntax error names only its byte offset. An unknown field is
// located by walking the body against t (unknownField), never by parsing
// encoding/json's message: body is the strict route's raw body, nil when
// unknown fields are allowed.
func bodyError(err error, body []byte, t reflect.Type) error {
	var tooBig *http.MaxBytesError
	var syntax *json.SyntaxError
	var mismatch *json.UnmarshalTypeError
	switch {
	case errors.As(err, &tooBig):
		return &Error{Status: http.StatusRequestEntityTooLarge, Code: "body_too_large",
			Detail: fmt.Sprintf("body exceeds %d bytes", tooBig.Limit)}
	case errors.Is(err, io.EOF):
		return &Error{Status: http.StatusBadRequest, Code: "malformed_json", Detail: "empty body"}
	case errors.Is(err, io.ErrUnexpectedEOF):
		return &Error{Status: http.StatusBadRequest, Code: "malformed_json", Detail: "the body ends inside the JSON value"}
	case errors.As(err, &syntax):
		return &Error{Status: http.StatusBadRequest, Code: "malformed_json",
			Detail: fmt.Sprintf("malformed JSON at byte offset %d", syntax.Offset)}
	case errors.As(err, &mismatch):
		return Invalid(Field(typePointer(mismatch.Field), "invalid_type", expectation(mismatch)))
	}
	if body != nil && t != nil {
		if ptr, ok := unknownField(body, t); ok {
			return &Error{Status: http.StatusBadRequest, Code: "unknown_field", Detail: "the body has a property this route does not accept",
				Fields: []FieldError{Field(ptr, "unknown_field", "unknown field")}}
		}
	}
	return &Error{Status: http.StatusBadRequest, Code: "malformed_json", Detail: "the body is not a valid value for this route"}
}

// typePointer turns json.UnmarshalTypeError.Field (the dotted path of JSON
// property names from the root) into a JSON pointer under /body.
func typePointer(field string) string {
	if field == "" {
		return "/body"
	}
	var b strings.Builder
	b.WriteString("/body")
	for seg := range strings.SplitSeq(field, ".") {
		b.WriteString("/" + escapePointer(seg))
	}
	return b.String()
}

// expectation says what JSON value a mismatched property takes, in JSON's
// terms (never a Go type name).
func expectation(e *json.UnmarshalTypeError) string {
	t := e.Type
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == nil {
		return "has the wrong JSON type"
	}
	want := "a different JSON type"
	switch {
	case reflect.PointerTo(t).Implements(reflect.TypeFor[encoding.TextUnmarshaler]()):
		want = "a string"
	default:
		switch t.Kind() {
		case reflect.Bool:
			want = "a boolean"
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
			reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
			if strings.HasPrefix(e.Value, "number") {
				return "is out of range for this property or not an integer"
			}
			want = "an integer"
		case reflect.Float32, reflect.Float64:
			if strings.HasPrefix(e.Value, "number") {
				return "is out of range for this property"
			}
			want = "a number"
		case reflect.String:
			want = "a string"
		case reflect.Slice:
			want = "an array"
			if t.Elem().Kind() == reflect.Uint8 {
				want = "a base64 string"
			}
		case reflect.Array:
			want = "an array"
		case reflect.Map, reflect.Struct:
			want = "an object"
		}
	}
	return "must be " + want
}

// unknownField finds a property of body that t does not accept, as a JSON
// pointer under /body, the way encoding/json matches names: exact first,
// then case-insensitively. Keys are visited in sorted order, so the answer is
// deterministic. ok is false when nothing is found (a custom UnmarshalJSON
// failed instead).
func unknownField(body []byte, t reflect.Type) (string, bool) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var v any
	if dec.Decode(&v) != nil {
		return "", false
	}
	return findUnknown(v, t, "/body")
}

func findUnknown(v any, t reflect.Type, at string) (string, bool) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Implements(reflect.TypeFor[json.Unmarshaler]()) || reflect.PointerTo(t).Implements(reflect.TypeFor[json.Unmarshaler]()) {
		return "", false // its own decoder decides what it accepts
	}
	switch t.Kind() {
	case reflect.Struct:
		obj, ok := v.(map[string]any)
		if !ok {
			return "", false
		}
		fields := jsonFields(t)
		for _, k := range slices.Sorted(maps.Keys(obj)) {
			p := at + "/" + escapePointer(k)
			ft, found := fields[k]
			if !found {
				for name, typ := range fields {
					if strings.EqualFold(name, k) {
						ft, found = typ, true
						break
					}
				}
			}
			if !found {
				return p, true
			}
			if ptr, ok := findUnknown(obj[k], ft, p); ok {
				return ptr, true
			}
		}
	case reflect.Map:
		obj, ok := v.(map[string]any)
		if !ok {
			return "", false
		}
		for _, k := range slices.Sorted(maps.Keys(obj)) {
			if ptr, ok := findUnknown(obj[k], t.Elem(), at+"/"+escapePointer(k)); ok {
				return ptr, true
			}
		}
	case reflect.Slice, reflect.Array:
		arr, ok := v.([]any)
		if !ok {
			return "", false
		}
		for i, e := range arr {
			if ptr, ok := findUnknown(e, t.Elem(), at+"/"+strconv.Itoa(i)); ok {
				return ptr, true
			}
		}
	}
	return "", false
}

// jsonFields is the JSON property name and type of every field
// encoding/json decodes into struct type t, promoted fields of embedded
// structs included. It may list a name encoding/json would not (a field
// shadowed under a tagged embedded struct), never omit one it would accept.
func jsonFields(t reflect.Type) map[string]reflect.Type {
	out := make(map[string]reflect.Type)
	for _, f := range reflect.VisibleFields(t) {
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		if f.Anonymous && name == "" {
			ft := f.Type
			if ft.Kind() == reflect.Pointer {
				ft = ft.Elem()
			}
			if ft.Kind() == reflect.Struct {
				continue // promoted: VisibleFields lists its fields
			}
		}
		if !f.IsExported() {
			continue
		}
		if name == "" {
			name = f.Name
		}
		if _, dup := out[name]; !dup {
			out[name] = f.Type
		}
	}
	return out
}

// escapePointer escapes one JSON pointer segment (RFC 6901).
func escapePointer(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "~", "~0"), "/", "~1")
}
