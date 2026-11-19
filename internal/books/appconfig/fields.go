// file: internal/books/appconfig/fields.go
// version: 1.0.0
// guid: 9b4e2d70-3a15-4c88-b6f1-5d0a7e93c2e4
// last-edited: 2026-11-19

package appconfig

import (
	"reflect"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/config"
)

// Config fields are addressed by reflection, not through JSON, because the
// environment-only and secret fields are tagged `json:"-"` (SMTP*, PublicBaseURL,
// CF*, ABSJWTSecret ...): the blob must not carry them, but the projection must
// fill them. A rename-table Head names a field by its json name, or by its
// mapstructure name where json is "-"; a dotted Head descends through nested
// structs the same way.

// fieldName is the name the rename table uses for a struct field: the json name,
// else (json "-") the mapstructure name; "" when the field has neither.
func fieldName(f reflect.StructField) string {
	if !f.IsExported() {
		return ""
	}
	if name, _, _ := strings.Cut(f.Tag.Get("json"), ","); name != "" && name != "-" {
		return name
	}
	if name, _, _ := strings.Cut(f.Tag.Get("mapstructure"), ","); name != "" {
		return name
	}
	if f.Tag.Get("json") == "" && !f.Anonymous {
		return f.Name
	}
	return ""
}

// fieldAt is the settable field of cfg that segs names, and whether it exists.
func fieldAt(cfg *config.Config, segs []string) (reflect.Value, bool) {
	v := reflect.ValueOf(cfg).Elem()
	for _, seg := range segs {
		if v.Kind() != reflect.Struct {
			return reflect.Value{}, false
		}
		next, ok := reflect.Value{}, false
		for i := range v.NumField() {
			if fieldName(v.Type().Field(i)) == seg {
				next, ok = v.Field(i), true
				break
			}
		}
		if !ok {
			return reflect.Value{}, false
		}
		v = next
	}
	return v, true
}

// uncovered lists the dotted names of fields in which a and b differ that no
// projected row covers, in field order. covered is the set of Heads the rows
// read; a struct is descended into only when a covered Head lies below it, and
// is otherwise compared whole.
func uncovered(a, b reflect.Value, path []string, covered map[string]bool, below map[string]bool) []string {
	var out []string
	t := a.Type()
	for i := range t.NumField() {
		name := fieldName(t.Field(i))
		if name == "" {
			continue
		}
		p := append(append([]string(nil), path...), name)
		dotted := strings.Join(p, ".")
		af, bf := a.Field(i), b.Field(i)
		switch {
		case covered[dotted]:
		case af.Kind() == reflect.Struct && below[dotted]:
			out = append(out, uncovered(af, bf, p, covered, below)...)
		case !reflect.DeepEqual(af.Interface(), bf.Interface()):
			out = append(out, dotted)
		}
	}
	return out
}
