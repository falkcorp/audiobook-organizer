// file: core/config/schema.go
// version: 1.0.0
// guid: 4c5389d7-95a5-4629-befb-000b41028790
// last-edited: 2026-11-10

package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/falkcorp/mediacore/internal/jsonschemax"
	"github.com/falkcorp/mediacore/secrets"
)

// schemaDialect is the JSON Schema dialect of Store.Schema (05-config §1.3.4).
const schemaDialect = "https://json-schema.org/draft/2020-12/schema"

// Overrides returns config's static contribution to the shared reflector
// (05-config §1.10.2): config.Duration renders as a string of format
// "duration", secrets.Ref as a write-only string marked x-mediacore-secret
// (a Patch carries the secret value as a JSON string; the value is never
// read back). A fresh table on every call (03 R5), so httpapi can compose
// jsonschemax.Base().With(config.Overrides()).With(its own) without a store.
func Overrides() jsonschemax.Overrides {
	return jsonschemax.Overrides{
		reflect.TypeFor[Duration](): {Type: "string", Format: "duration"},
		reflect.TypeFor[secrets.Ref](): {
			Type: "string", WriteOnly: true,
			Extra: map[string]any{"x-mediacore-secret": true},
		},
	}
}

// storeOverrides is what config reflects section types with: Base, config's
// own, and json.RawMessage as "any JSON value" (jsonschema-go would render a
// RawMessage as an array of integers). json.RawMessage is a standard-library
// type with no owner in 05-config §1.10.2's table; the seam names
// jsonschemax.Base as its rightful owner, so it is store-internal here and
// stays out of Overrides, which httpapi composes with its own set.
func storeOverrides() jsonschemax.Overrides {
	return jsonschemax.Base().With(Overrides()).With(jsonschemax.Overrides{
		reflect.TypeFor[json.RawMessage](): {},
	})
}

// Schema is a JSON Schema 2020-12 document plus mediacore vendor keywords,
// produced by the shared reflector (05-config §1.10.2). integrations
// descriptors use it for instance settings (05-metadata M1). The zero Schema
// validates nothing and renders as null.
type Schema struct {
	s        *jsonschema.Schema
	resolved *jsonschema.Resolved
}

// SchemaFor reflects T with config's overrides and the tag pass.
func SchemaFor[T any]() (Schema, error) {
	s, err := jsonschemax.For[T](storeOverrides())
	if err != nil {
		return Schema{}, fmt.Errorf("config: schema of %v: %w", reflect.TypeFor[T](), err)
	}
	s.Schema = schemaDialect
	r, err := s.CloneSchemas().Resolve(nil)
	if err != nil {
		return Schema{}, fmt.Errorf("config: resolve the schema of %v: %w", reflect.TypeFor[T](), err)
	}
	return Schema{s: s, resolved: r}, nil
}

// JSON is the schema document. Each call returns a fresh copy.
func (s Schema) JSON() json.RawMessage {
	if s.s == nil {
		return json.RawMessage("null")
	}
	b, err := json.Marshal(s.s)
	if err != nil {
		// A schema SchemaFor built always marshals; a failure is a bug in
		// the reflector, reported as a schema that accepts nothing.
		return json.RawMessage("false")
	}
	return b
}

// Validate checks v against the schema: errors.Join of *FieldError.
func (s Schema) Validate(v json.RawMessage) error {
	if s.resolved == nil {
		return errors.Join(&FieldError{Err: errors.New("config: the zero Schema validates nothing")})
	}
	var inst any
	if err := json.Unmarshal(v, &inst); err != nil {
		return errors.Join(&FieldError{Err: fmt.Errorf("not JSON: %w", err)})
	}
	if err := s.resolved.Validate(inst); err != nil {
		return errors.Join(&FieldError{Err: err})
	}
	return nil
}

// Schema renders one JSON Schema 2020-12 document: one property per
// registered section for which include is true (nil includes every one),
// keyed by the section path, each the section's reflected schema with the
// vendor keywords x-mediacore-reload, -protect, -secret, -sensitive and
// -fail-closed, its defaults (never a secret's) and its Docs. $id is
// Options.AppID. The settings routes (F4-05) filter by read permission.
func (s *Store) Schema(include func(section Path) bool) (json.RawMessage, error) {
	s.mu.Lock()
	secs := make([]*section, 0, len(s.sections))
	for _, sec := range s.sections {
		if include == nil || include(sec.path) {
			secs = append(secs, sec)
		}
	}
	s.mu.Unlock()
	props := make(map[string]json.RawMessage, len(secs))
	for _, sec := range secs {
		b, err := json.Marshal(sec.schema)
		if err != nil {
			return nil, fmt.Errorf("config: render the schema of %s: %w", sec.path, err)
		}
		props[string(sec.path)] = b
	}
	doc := struct {
		Schema     string                     `json:"$schema"`
		ID         string                     `json:"$id"`
		Type       string                     `json:"type"`
		Properties map[string]json.RawMessage `json:"properties"`
	}{Schema: schemaDialect, ID: s.o.AppID, Type: "object", Properties: props}
	b, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("config: render the settings schema: %w", err)
	}
	return b, nil
}
