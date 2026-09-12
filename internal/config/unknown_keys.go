// file: internal/config/unknown_keys.go
// version: 1.1.0
// guid: 6a4394fd-1b48-48cc-9b88-0642eff0623f
// last-edited: 2026-09-12

package config

import (
	"bytes"
	"encoding"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// readOnlyConfigKeys are the keys GET /config adds to its response that are not
// Config fields: withEnvLocks in internal/server/handlers/system computes them
// per request. UpdateConfig drops them from a PUT so a client can send a GET
// response back unchanged. Keep this list in sync with withEnvLocks.
var readOnlyConfigKeys = []string{"env_locked", "setting_locks", "activity_db_resolved_path"}

var (
	jsonUnmarshalerType = reflect.TypeFor[json.Unmarshaler]()
	textUnmarshalerType = reflect.TypeFor[encoding.TextUnmarshaler]()
)

// unknownConfigKeys returns, sorted, the dotted path of every key in payload
// that json.Unmarshal would silently ignore when decoding onto a Config — a key
// with no matching field at its level. Nested objects are walked through struct
// fields, map values and slice elements, so {"dedup":{"typo":1}} reports
// "dedup.typo" and {"metadata_sources":[{"typo":1}]} reports
// "metadata_sources[0].typo". Map-typed fields (credentials, plugins, ...)
// accept any key, as the decoder does. A value of the wrong JSON type is not
// reported here; the decoder rejects that with its own error.
func unknownConfigKeys(payload map[string]any) []string {
	var out []string
	walkUnknownKeys(reflect.TypeFor[Config](), payload, "", &out)
	sort.Strings(out)
	return out
}

func walkUnknownKeys(t reflect.Type, v any, path string, out *[]string) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	// A type that decodes itself decides which keys it accepts.
	if pt := reflect.PointerTo(t); pt.Implements(jsonUnmarshalerType) || pt.Implements(textUnmarshalerType) {
		return
	}
	switch t.Kind() {
	case reflect.Struct:
		obj, ok := v.(map[string]any)
		if !ok {
			return
		}
		fields := jsonFieldTypes(t)
		for k, sub := range obj {
			ft, ok := lookupJSONField(fields, k)
			if !ok {
				*out = append(*out, joinKeyPath(path, k))
				continue
			}
			walkUnknownKeys(ft, sub, joinKeyPath(path, k), out)
		}
	case reflect.Map:
		obj, ok := v.(map[string]any)
		if !ok {
			return
		}
		for k, sub := range obj {
			walkUnknownKeys(t.Elem(), sub, joinKeyPath(path, k), out)
		}
	case reflect.Slice, reflect.Array:
		arr, ok := v.([]any)
		if !ok {
			return
		}
		for i, sub := range arr {
			walkUnknownKeys(t.Elem(), sub, fmt.Sprintf("%s[%d]", path, i), out)
		}
	}
}

// jsonFieldTypes maps each JSON object key a struct decodes to the type of the
// field behind it, following encoding/json: the tag name, else the Go field
// name; a "-" tag skips the field; untagged embedded structs are promoted, and
// an outer field wins over a promoted one of the same name.
func jsonFieldTypes(t reflect.Type) map[string]reflect.Type {
	fields := make(map[string]reflect.Type)
	promoted := make(map[string]reflect.Type)
	for i := range t.NumField() {
		f := t.Field(i)
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		if f.Anonymous && name == "" {
			et := f.Type
			if et.Kind() == reflect.Pointer {
				et = et.Elem()
			}
			if et.Kind() == reflect.Struct {
				for k, v := range jsonFieldTypes(et) {
					promoted[k] = v
				}
				continue
			}
		}
		if !f.IsExported() {
			continue
		}
		if name == "" {
			name = f.Name
		}
		fields[name] = f.Type
	}
	for k, v := range promoted {
		if _, taken := fields[k]; !taken {
			fields[k] = v
		}
	}
	return fields
}

// lookupJSONField matches key the way encoding/json does: an exact name first,
// then a case-insensitive one.
func lookupJSONField(fields map[string]reflect.Type, key string) (reflect.Type, bool) {
	if t, ok := fields[key]; ok {
		return t, true
	}
	for name, t := range fields {
		if strings.EqualFold(name, key) {
			return t, true
		}
	}
	return nil, false
}

func joinKeyPath(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

// unknownKeysMessage is the 400 error for a PUT carrying keys Config lacks.
func unknownKeysMessage(keys []string) string {
	return "unknown setting key(s): " + strings.Join(keys, ", ") +
		` — nothing was saved. Nested settings take the nested form, e.g. {"dedup":{"auto_merge_enabled":false}}, not a flat dedup_auto_merge_enabled`
}

// decodeConfigPayload decodes a PUT payload onto candidate with unknown fields
// disallowed. It backs up the unknownConfigKeys check: if the walker and the
// decoder ever disagree about a key, the request still fails instead of
// silently dropping the key.
func decodeConfigPayload(payloadJSON []byte, candidate *Config) error {
	dec := json.NewDecoder(bytes.NewReader(payloadJSON))
	dec.DisallowUnknownFields()
	return dec.Decode(candidate)
}
