// file: core/internal/jsonschemax/tags.go
// version: 1.0.0
// guid: 34130108-2c62-47fc-9987-bf1907794075
// last-edited: 2026-11-06

package jsonschemax

import (
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
)

// The vendor keywords the tag pass writes into Schema.Extra (05-config
// §1.3). x-mediacore-secret and x-mediacore-fail-closed are not tag-driven:
// the first comes from the secrets.Ref override, the second from config's own
// registration.
const (
	extReload    = "x-mediacore-reload"
	extProtect   = "x-mediacore-protect"
	extSensitive = "x-mediacore-sensitive"
)

// applyTags is the tag pass (05-config §1.3 field-metadata table).
// jsonschema-go reads only its own jsonschema:"..." description tag, so this
// pass maps the rest:
//
//	reload:"restart|boot"                          -> x-mediacore-reload
//	protect:"interactive|admin|owner|operator"     -> x-mediacore-protect
//	sensitive:"true"                               -> x-mediacore-sensitive: true
//	schema:"minimum=,maximum=,enum=a|b,format=,pattern="
//	                                               -> the standard keywords
//
// A schema tag is a comma-separated list of key=value pairs. enum separates
// its values with "|" and types them by the field's schema type (integer,
// number, boolean or string). pattern takes the rest of the tag, commas
// included, so it must be the last pair. format is one of duration, uri,
// cidr, path, email. minimum and maximum need an integer or number field;
// enum a scalar field; format and pattern a string field. A malformed tag, an
// unknown key or value, or a keyword on a field of the wrong type is an error
// naming the field: a tag the pass cannot honour never silently drops.
//
// The pass walks the reflected type beside the schema (struct fields by JSON
// name, slice and array elements, map values, pointers dereferenced) and
// applies a field's tags to that field's own property schema.
func applyTags(t reflect.Type, s *jsonschema.Schema) error {
	return walk(t.String(), t, s)
}

func walk(path string, t reflect.Type, s *jsonschema.Schema) error {
	if s == nil {
		return nil
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Struct:
		// A type with an override schema (time.Time, config.Duration, ...)
		// has no properties: there is nothing to walk.
		if len(s.Properties) == 0 {
			return nil
		}
		for _, f := range reflect.VisibleFields(t) {
			// jsonschema-go flattens an embedded struct into its parent: the
			// embedded field itself carries no property, its promoted fields do.
			if f.Anonymous || !f.IsExported() {
				continue
			}
			name, ok := jsonName(f)
			if !ok {
				continue
			}
			ps := s.Properties[name]
			if ps == nil {
				continue
			}
			fp := path + "." + f.Name
			if err := applyField(fp, f, ps); err != nil {
				return err
			}
			if err := walk(fp, f.Type, ps); err != nil {
				return err
			}
		}
	case reflect.Slice, reflect.Array:
		return walk(path+"[]", t.Elem(), s.Items)
	case reflect.Map:
		return walk(path+"{}", t.Elem(), s.AdditionalProperties)
	}
	return nil
}

// jsonName is encoding/json's name for f, mirroring jsonschema-go's own rule
// so the two passes agree on which property a field is: the name part of the
// json tag when non-empty, else the Go name; "-" (without a comma) omits.
func jsonName(f reflect.StructField) (string, bool) {
	tag, ok := f.Tag.Lookup("json")
	if !ok {
		return f.Name, true
	}
	name, _, found := strings.Cut(tag, ",")
	if name == "-" && !found {
		return "", false
	}
	if name == "" {
		return f.Name, true
	}
	return name, true
}

func reloadValues() []string  { return []string{"restart", "boot"} }
func protectValues() []string { return []string{"interactive", "admin", "owner", "operator"} }
func formatValues() []string  { return []string{"duration", "uri", "cidr", "path", "email"} }

func applyField(path string, f reflect.StructField, ps *jsonschema.Schema) error {
	if v, ok := f.Tag.Lookup("reload"); ok {
		if !slices.Contains(reloadValues(), v) {
			return fmt.Errorf("%s: reload:%q: want one of %s", path, v, strings.Join(reloadValues(), ", "))
		}
		setExtra(ps, extReload, v)
	}
	if v, ok := f.Tag.Lookup("protect"); ok {
		if !slices.Contains(protectValues(), v) {
			return fmt.Errorf("%s: protect:%q: want one of %s", path, v, strings.Join(protectValues(), ", "))
		}
		setExtra(ps, extProtect, v)
	}
	if v, ok := f.Tag.Lookup("sensitive"); ok {
		switch v {
		case "true":
			setExtra(ps, extSensitive, true)
		case "false":
		default:
			return fmt.Errorf("%s: sensitive:%q: want \"true\" or \"false\"", path, v)
		}
	}
	if v, ok := f.Tag.Lookup("schema"); ok {
		if err := applySchemaTag(ps, v); err != nil {
			return fmt.Errorf("%s: schema:%q: %w", path, v, err)
		}
	}
	return nil
}

func setExtra(s *jsonschema.Schema, key string, v any) {
	if s.Extra == nil {
		s.Extra = map[string]any{}
	}
	s.Extra[key] = v
}

// schemaType is the schema's one non-null type: Type, or the non-"null"
// member of Types (a pointer field renders as ["null", T]).
func schemaType(s *jsonschema.Schema) string {
	if s.Type != "" {
		return s.Type
	}
	for _, t := range s.Types {
		if t != "null" {
			return t
		}
	}
	return ""
}

func applySchemaTag(ps *jsonschema.Schema, tag string) error {
	typ := schemaType(ps)
	seen := map[string]bool{}
	for rest := tag; rest != ""; {
		key, after, ok := strings.Cut(rest, "=")
		if !ok || key == "" {
			return fmt.Errorf("want key=value pairs, got %q", rest)
		}
		var val string
		if key == "pattern" {
			val, rest = after, "" // the rest of the tag, commas included
		} else {
			val, rest, _ = strings.Cut(after, ",")
		}
		if seen[key] {
			return fmt.Errorf("duplicate key %q", key)
		}
		seen[key] = true
		if val == "" {
			return fmt.Errorf("%s: empty value", key)
		}
		if err := applyKeyword(ps, typ, key, val); err != nil {
			return err
		}
	}
	return nil
}

func applyKeyword(ps *jsonschema.Schema, typ, key, val string) error {
	switch key {
	case "minimum", "maximum":
		if typ != "integer" && typ != "number" {
			return fmt.Errorf("%s needs an integer or number field, this one is %q", key, typ)
		}
		n, err := strconv.ParseFloat(val, 64)
		if err != nil {
			return fmt.Errorf("%s: %w", key, err)
		}
		if key == "minimum" {
			ps.Minimum = &n
		} else {
			ps.Maximum = &n
		}
	case "enum":
		vals, err := enumValues(typ, val)
		if err != nil {
			return err
		}
		ps.Enum = vals
	case "format":
		if typ != "string" {
			return fmt.Errorf("format needs a string field, this one is %q", typ)
		}
		if !slices.Contains(formatValues(), val) {
			return fmt.Errorf("format %q: want one of %s", val, strings.Join(formatValues(), ", "))
		}
		ps.Format = val
	case "pattern":
		if typ != "string" {
			return fmt.Errorf("pattern needs a string field, this one is %q", typ)
		}
		ps.Pattern = val
	default:
		return fmt.Errorf("unknown key %q (want minimum, maximum, enum, format, pattern)", key)
	}
	return nil
}

// enumValues parses the "|"-separated enum list into values of the field's
// JSON type.
func enumValues(typ, val string) ([]any, error) {
	out := []any{}
	for item := range strings.SplitSeq(val, "|") {
		if item == "" {
			return nil, fmt.Errorf("enum %q has an empty value", val)
		}
		switch typ {
		case "string":
			out = append(out, item)
		case "integer":
			n, err := strconv.ParseInt(item, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("enum: %w", err)
			}
			out = append(out, n)
		case "number":
			n, err := strconv.ParseFloat(item, 64)
			if err != nil {
				return nil, fmt.Errorf("enum: %w", err)
			}
			out = append(out, n)
		case "boolean":
			b, err := strconv.ParseBool(item)
			if err != nil {
				return nil, fmt.Errorf("enum: %w", err)
			}
			out = append(out, b)
		default:
			return nil, fmt.Errorf("enum needs a string, integer, number or boolean field, this one is %q", typ)
		}
	}
	return out, nil
}
