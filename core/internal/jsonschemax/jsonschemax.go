// file: core/internal/jsonschemax/jsonschemax.go
// version: 1.0.1
// guid: 6bd61d18-b410-4f45-9106-70a9efcae5a3
// last-edited: 2026-11-10

// Package jsonschemax is core's one JSON Schema reflector (05-config §1.10.2):
// a thin wrapper over github.com/google/jsonschema-go that adds the tag pass
// (tags.go) and an extensible table of per-type overrides. It is an L0 leaf
// (03 R4): it imports only the standard library and jsonschema-go, so every
// layer may use it.
//
// Each package contributes the overrides for its own types at its own layer,
// through a function, so no exported package-level map exists (03 R5), and no
// type appears in two sets: Base owns time.Time, config.Overrides adds
// config.Duration and secrets.Ref, httpapi adds rbac.Permission. The one
// composition, Base().With(config.Overrides()).With(httpapi's own), is used
// by both the settings schema and OpenAPI, so a DTO renders identically in
// both.
package jsonschemax

import (
	"fmt"
	"reflect"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
)

// Overrides maps a Go type to the schema it renders as, replacing
// jsonschema-go's default translation of that type.
type Overrides map[reflect.Type]*jsonschema.Schema

// With returns the union of o and more as a new table; neither operand is
// modified and no schema is shared with them. A type present in both panics,
// naming the type: a type has one owner, and two owners would make a DTO
// render differently depending on composition order.
func (o Overrides) With(more Overrides) Overrides {
	out := make(Overrides, len(o)+len(more))
	for t, s := range o {
		out[t] = cloneSchema(s)
	}
	for t, s := range more {
		if _, dup := out[t]; dup {
			panic(fmt.Sprintf("jsonschemax: duplicate override for type %v", t))
		}
		out[t] = cloneSchema(s)
	}
	return out
}

// For reflects T into a JSON Schema: jsonschema.For[T] with o as its type
// overrides, then the tag pass over the result (tags.go). A nil o is an empty
// table, so time.Time renders as plain "string" unless the caller passes
// Base().
func For[T any](o Overrides) (*jsonschema.Schema, error) {
	return ForType(reflect.TypeFor[T](), o)
}

// ForType is For for a type known only at run time (httpapi.HandleRaw's
// RawDoc names its bodies by reflect.Type).
func ForType(t reflect.Type, o Overrides) (*jsonschema.Schema, error) {
	s, err := jsonschema.ForType(t, &jsonschema.ForOptions{TypeSchemas: o})
	if err != nil {
		return nil, fmt.Errorf("jsonschemax: %w", err)
	}
	if err := applyTags(t, s); err != nil {
		return nil, fmt.Errorf("jsonschemax: %w", err)
	}
	return s, nil
}

// Base returns the overrides core itself owns: time.Time renders as a string
// with format "date-time". It is the ONLY owner of time.Time. A fresh table
// on every call (03 R5: a function, not an exported map variable).
func Base() Overrides {
	return Overrides{
		reflect.TypeFor[time.Time](): {Type: "string", Format: "date-time"},
	}
}

func cloneSchema(s *jsonschema.Schema) *jsonschema.Schema {
	if s == nil {
		return nil
	}
	return s.CloneSchemas()
}
