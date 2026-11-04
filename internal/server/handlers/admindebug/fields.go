// file: internal/server/handlers/admindebug/fields.go
// version: 1.0.2
// guid: 61820a5b-9226-486c-b5cd-efc491764ce4
// last-edited: 2026-11-04

package admindebug

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// fieldIndex maps a struct's top-level JSON tag names to their field index.
// database.Book and database.BookFile have no embedded structs, no json:"-"
// fields and no custom (Un)MarshalJSON; if one gains any of those, this
// mapping (and therefore the PATCH surface) must be revisited.
type fieldIndex map[string]int

func buildFieldIndex(t reflect.Type) fieldIndex {
	idx := make(fieldIndex, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "-" {
			continue
		}
		if name == "" {
			name = f.Name
		}
		idx[name] = i
	}
	return idx
}

// entitySpec describes what a PATCH may touch on one record type.
type entitySpec struct {
	name   string
	fields fieldIndex
	// immutable fields are rejected with 400: identity, ownership and the
	// store-maintained timestamps.
	immutable map[string]string
	// localPaths are fields holding a path on this host. They pass through the
	// iTunes guard (see guardPaths).
	localPaths map[string]bool
	// itunesOwned fields are values copied from the iTunes library itself;
	// they are refused outright.
	itunesOwned map[string]bool
	// zeroIsUnset names pointer fields whose nil is the record's "not set", and
	// that this API has always shown and accepted as 0 (book_file.duration:
	// nil is not measured, and a stored value is always > 0). Reads render nil
	// as 0, and a value written as 0 (or below) is stored as nil, never as a
	// pointer to 0. Without it the PATCH diff would change from 0 to null, and a
	// PATCH of 0 would store the "duration":0 that no row may carry.
	zeroIsUnset map[string]bool
}

// encodeField is the canonical JSON of a field value of this spec's record
// type: json.Marshal, except a zeroIsUnset field that is nil encodes as 0.
func (s *entitySpec) encodeField(name string, v reflect.Value) (json.RawMessage, error) {
	if s.zeroIsUnset[name] && v.Kind() == reflect.Pointer && v.IsNil() {
		return json.RawMessage("0"), nil
	}
	return json.Marshal(v.Interface())
}

// decodeField decodes raw into a FRESH value of the field's type (a pointer
// field never aliases or merges into a value the record held before). A
// zeroIsUnset pointer that decodes to a value <= 0 becomes nil.
func (s *entitySpec) decodeField(t reflect.Type, name string, raw json.RawMessage) (reflect.Value, error) {
	fresh := reflect.New(t)
	if err := json.Unmarshal(raw, fresh.Interface()); err != nil {
		return reflect.Value{}, err
	}
	v := fresh.Elem()
	if s.zeroIsUnset[name] && v.Kind() == reflect.Pointer && !v.IsNil() && v.Elem().CanInt() && v.Elem().Int() <= 0 {
		v = reflect.Zero(t)
	}
	return v, nil
}

// patchPlan is a validated PATCH: the requested fields in a stable order and
// the decoded value of each.
type patchPlan struct {
	fields []string
	raw    map[string]json.RawMessage
}

// validatePatch rejects unknown, immutable and iTunes-owned fields, and checks
// each value decodes into the field's Go type.
func (s *entitySpec) validatePatch(zero any, raw map[string]json.RawMessage) (*patchPlan, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("empty patch: send a JSON object of field -> value")
	}
	t := reflect.TypeOf(zero).Elem()
	plan := &patchPlan{raw: raw}
	for name, val := range raw {
		i, ok := s.fields[name]
		if !ok {
			return nil, fmt.Errorf("unknown %s field %q (field names are the JSON tags of the record)", s.name, name)
		}
		if why, bad := s.immutable[name]; bad {
			return nil, fmt.Errorf("%s field %q cannot be edited here: %s", s.name, name, why)
		}
		if s.itunesOwned[name] {
			return nil, fmt.Errorf("%s field %q is copied from the iTunes library and cannot be edited here", s.name, name)
		}
		probe := reflect.New(t.Field(i).Type)
		if err := json.Unmarshal(val, probe.Interface()); err != nil {
			return nil, fmt.Errorf("%s field %q: value does not fit type %s: %v", s.name, name, t.Field(i).Type, err)
		}
		plan.fields = append(plan.fields, name)
	}
	sort.Strings(plan.fields)
	return plan, nil
}

// fieldJSON returns the canonical JSON of the named field of rec (a struct
// pointer).
func (s *entitySpec) fieldJSON(rec any, name string) (json.RawMessage, error) {
	v := reflect.ValueOf(rec).Elem().Field(s.fields[name])
	b, err := s.encodeField(name, v)
	if err != nil {
		return nil, fmt.Errorf("encode %s.%s: %w", s.name, name, err)
	}
	return b, nil
}

// setField decodes raw into a FRESH value of the field's type and assigns it,
// so a pointer, slice or map field never aliases (or merges into) the value
// the record held before.
func (s *entitySpec) setField(rec any, name string, raw json.RawMessage) error {
	f := reflect.ValueOf(rec).Elem().Field(s.fields[name])
	v, err := s.decodeField(f.Type(), name, raw)
	if err != nil {
		return fmt.Errorf("decode %s.%s: %w", s.name, name, err)
	}
	f.Set(v)
	return nil
}

// canonical re-encodes raw so two encodings of one value compare equal
// (whitespace, and a client's `1.0` vs the store's `1`, are normalised by
// decoding into the field's type first).
func (s *entitySpec) canonical(zero any, name string, raw json.RawMessage) (json.RawMessage, error) {
	t := reflect.TypeOf(zero).Elem().Field(s.fields[name]).Type
	v, err := s.decodeField(t, name, raw)
	if err != nil {
		return nil, err
	}
	return s.encodeField(name, v)
}

func jsonEqual(a, b json.RawMessage) bool {
	var ca, cb bytes.Buffer
	if json.Compact(&ca, a) != nil || json.Compact(&cb, b) != nil {
		return bytes.Equal(a, b)
	}
	return bytes.Equal(ca.Bytes(), cb.Bytes())
}
