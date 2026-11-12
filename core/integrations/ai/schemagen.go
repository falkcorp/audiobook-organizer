// file: core/integrations/ai/schemagen.go
// version: 1.0.0
// guid: 5a9d2e84-3f16-4c07-b8a1-9e4c6d0f2b53
// last-edited: 2026-11-12

package ai

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"slices"
	"strings"
	"time"
)

// This file is core's restricted JSON Schema generator and strict decoder for
// GenerateJSON (05-ai section 3). The generator reads structs, slices, maps
// with string keys, strings, numbers, bools and time.Time, and `enum:"a,b"`
// tags on string fields; anything else (an interface, a channel, a recursive
// type, a map with another key type) is refused when the schema is derived,
// not at the first call.
//
// The schema is shaped for strict structured outputs: every struct property is
// listed in "required" and additionalProperties is false, so a field a Go type
// leaves optional (a pointer, omitempty or omitzero) is typed nullable instead
// of left out. The decoder is strict the other way: unknown fields are
// refused, a required field must be present, a non-nullable field must not be
// null, and an enum value must be one the tag lists.

// fieldInfo is one JSON property of a struct type.
type fieldInfo struct {
	name     string
	typ      reflect.Type
	optional bool
	enum     []string
	index    []int
}

// isTime reports time.Time, which encodes as an RFC 3339 string.
func isTime(t reflect.Type) bool { return t == reflect.TypeFor[time.Time]() }

// structFields lists t's JSON properties in declaration order, flattening an
// embedded struct that carries no json tag, as encoding/json does.
func structFields(t reflect.Type) []fieldInfo {
	var out []fieldInfo
	for f := range t.Fields() {
		tag, hasTag := f.Tag.Lookup("json")
		if tag == "-" {
			continue
		}
		name, opts, _ := strings.Cut(tag, ",")
		if f.Anonymous && name == "" && !hasTag {
			ft := f.Type
			if ft.Kind() == reflect.Pointer {
				ft = ft.Elem()
			}
			if ft.Kind() == reflect.Struct {
				out = append(out, structFields(ft)...)
				continue
			}
		}
		if !f.IsExported() {
			continue
		}
		if name == "" {
			name = f.Name
		}
		var enum []string
		if e := f.Tag.Get("enum"); e != "" {
			enum = strings.Split(e, ",")
		}
		optional := f.Type.Kind() == reflect.Pointer
		for o := range strings.SplitSeq(opts, ",") {
			if o == "omitempty" || o == "omitzero" {
				optional = true
			}
		}
		out = append(out, fieldInfo{name: name, typ: f.Type, optional: optional, enum: enum, index: f.Index})
	}
	return out
}

// schemaOf builds the JSON Schema of t. visiting guards against recursive
// types.
func schemaOf(t reflect.Type, enum []string, visiting map[reflect.Type]bool) (map[string]any, error) {
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if isTime(t) {
		return map[string]any{"type": "string", "format": "date-time"}, nil
	}
	switch t.Kind() {
	case reflect.String:
		s := map[string]any{"type": "string"}
		if len(enum) > 0 {
			s["enum"] = slices.Clone(enum)
		}
		return s, nil
	case reflect.Bool:
		return map[string]any{"type": "boolean"}, nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return map[string]any{"type": "integer"}, nil
	case reflect.Float32, reflect.Float64:
		return map[string]any{"type": "number"}, nil
	case reflect.Slice, reflect.Array:
		if t.Elem().Kind() == reflect.Uint8 {
			return nil, fmt.Errorf("ai: schema: %s: a byte slice has no JSON Schema here", t)
		}
		items, err := schemaOf(t.Elem(), nil, visiting)
		if err != nil {
			return nil, err
		}
		return map[string]any{"type": "array", "items": items}, nil
	case reflect.Map:
		if t.Key().Kind() != reflect.String {
			return nil, fmt.Errorf("ai: schema: %s: map keys must be strings", t)
		}
		v, err := schemaOf(t.Elem(), nil, visiting)
		if err != nil {
			return nil, err
		}
		return map[string]any{"type": "object", "additionalProperties": v}, nil
	case reflect.Struct:
		if visiting[t] {
			return nil, fmt.Errorf("ai: schema: %s is recursive", t)
		}
		visiting[t] = true
		defer delete(visiting, t)
		props := map[string]any{}
		required := []string{}
		for _, f := range structFields(t) {
			s, err := schemaOf(f.typ, f.enum, visiting)
			if err != nil {
				return nil, fmt.Errorf("%s.%s: %w", t.Name(), f.name, err)
			}
			if f.optional {
				s = map[string]any{"anyOf": []any{s, map[string]any{"type": "null"}}}
			}
			props[f.name] = s
			required = append(required, f.name)
		}
		return map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}, nil
	}
	return nil, fmt.Errorf("ai: schema: %s (%s) has no JSON Schema here", t, t.Kind())
}

// schemaFromType is the JSON Schema of T; T must be an object type (a struct or
// a map), the only root structured outputs accept.
func schemaFromType[T any]() (json.RawMessage, error) {
	t := reflect.TypeFor[T]()
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct && t.Kind() != reflect.Map {
		return nil, fmt.Errorf("ai: GenerateJSON needs a struct or map type, not %s; give the request its own Output.Schema for anything else", t)
	}
	s, err := schemaOf(t, nil, map[reflect.Type]bool{})
	if err != nil {
		return nil, err
	}
	return json.Marshal(s)
}

// stripFence removes a Markdown code fence a model wrapped around its JSON.
func stripFence(s string) string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "```") {
		return s
	}
	s = strings.TrimPrefix(s, "```")
	if nl := strings.IndexByte(s, '\n'); nl >= 0 {
		s = s[nl+1:]
	}
	s = strings.TrimSuffix(strings.TrimSpace(s), "```")
	return strings.TrimSpace(s)
}

// decodeStrict decodes text into a T: no unknown field, nothing after the
// value, and, when shape is true, every required field present, no null in a
// non-nullable place and every enum value listed.
func decodeStrict[T any](text string, shape bool) (T, error) {
	var v T
	body := stripFence(text)
	if body == "" {
		return v, errors.New("the answer is empty")
	}
	dec := json.NewDecoder(strings.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&v); err != nil {
		return v, fmt.Errorf("the answer is not the requested JSON: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return v, errors.New("the answer has text after the JSON value")
	}
	if shape {
		t := reflect.TypeFor[T]()
		if err := checkShape(t, nil, json.RawMessage(body), "$", false); err != nil {
			var zero T
			return zero, err
		}
	}
	return v, nil
}

// checkShape verifies raw against t's schema rules (see the file comment).
// nullable says the position may hold null (a pointer or an optional field).
func checkShape(t reflect.Type, enum []string, raw json.RawMessage, path string, nullable bool) error {
	raw = bytes.TrimSpace(raw)
	isNull := bytes.Equal(raw, []byte("null"))
	if t.Kind() == reflect.Pointer {
		nullable = true
		t = t.Elem()
	}
	if isNull {
		if nullable {
			return nil
		}
		return fmt.Errorf("%s is null but is not optional", path)
	}
	if isTime(t) {
		return nil
	}
	switch t.Kind() {
	case reflect.String:
		if len(enum) == 0 {
			return nil
		}
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		if !slices.Contains(enum, s) {
			return fmt.Errorf("%s is %q, which is not one of %s", path, s, strings.Join(enum, ", "))
		}
	case reflect.Slice, reflect.Array:
		var items []json.RawMessage
		if err := json.Unmarshal(raw, &items); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		for i, it := range items {
			if err := checkShape(t.Elem(), nil, it, fmt.Sprintf("%s[%d]", path, i), false); err != nil {
				return err
			}
		}
	case reflect.Map:
		var entries map[string]json.RawMessage
		if err := json.Unmarshal(raw, &entries); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		for k, it := range entries {
			if err := checkShape(t.Elem(), nil, it, path+"."+k, false); err != nil {
				return err
			}
		}
	case reflect.Struct:
		var entries map[string]json.RawMessage
		if err := json.Unmarshal(raw, &entries); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		for _, f := range structFields(t) {
			it, present := entries[f.name]
			switch {
			case !present && f.optional:
				continue
			case !present:
				return fmt.Errorf("%s.%s is required and missing", path, f.name)
			}
			if err := checkShape(f.typ, f.enum, it, path+"."+f.name, f.optional); err != nil {
				return err
			}
		}
	}
	return nil
}
