// file: internal/config/unknown_keys.go
// version: 1.3.0
// guid: 6a4394fd-1b48-48cc-9b88-0642eff0623f
// last-edited: 2026-10-07

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
// per request. UpdateConfig drops them from a PUT, so they never fail one. A raw
// GET response still cannot be PUT back as-is: it also carries database_type,
// which immutableFieldKeys refuses whenever it is present (the web import
// filter drops it). Keep this list in sync with withEnvLocks.
var readOnlyConfigKeys = []string{"env_locked", "setting_locks", "activity_db_resolved_path"}

var (
	jsonUnmarshalerType = reflect.TypeFor[json.Unmarshaler]()
	textUnmarshalerType = reflect.TypeFor[encoding.TextUnmarshaler]()
)

// unknownConfigKeys returns, sorted, the dotted path of every key in payload
// that json.Unmarshal would silently ignore when decoding onto a Config — a key
// with no matching field at its level — or that matches a field only by case
// (see lookupJSONField). Nested objects are walked through struct
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

// lookupJSONField matches key EXACTLY. encoding/json would also accept a key
// that differs only in case ("OAuth_Default_Role" lands on
// oauth_default_role), but every other check in UpdateConfig — the immutable,
// secret and removed-key lists — looks keys up exactly, so a case variant used
// to reach a field those checks never saw (security review, 2026-10-07).
// Refusing the variant here makes the exact spelling the only one that decodes,
// so the key-name checks and the decoder can no longer disagree.
// ChangedProtectedFields compares the decoded structs as well, so the
// interactive-session rule does not depend on this alone.
func lookupJSONField(fields map[string]reflect.Type, key string) (reflect.Type, bool) {
	t, ok := fields[key]
	return t, ok
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
