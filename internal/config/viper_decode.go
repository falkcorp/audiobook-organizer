// file: internal/config/viper_decode.go
// version: 1.0.0
// guid: b431724a-2f0b-413b-8704-45a5807bee77
// last-edited: 2026-10-11

package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/viper"
	"gopkg.in/yaml.v3"
)

// viperDecodeJSON decodes the viper value at key into a T through its JSON
// form, so every source spells these settings the way the stored blob and the
// settings API do (the json tags; several of these element types carry no
// mapstructure tags, so viper.UnmarshalKey would silently drop snake_case
// fields). An unset key yields the zero T.
//
// Where the value comes from decides how it is decoded:
//
//   - An environment variable (AutomaticEnv, or BindEnv) reaches viper as a
//     string, so a string value is parsed as JSON text: AI_ENDPOINTS='[{...}]',
//     PLUGINS='{"MyPlugin":{"enabled":true}}'. This is the WHISPER_ENDPOINTS
//     convention. Only top-level keys are reachable from the environment:
//     AutomaticEnv has no key replacer here, so a dotted key such as
//     itunes.path_mappings or dedup.signals.confidence has no env spelling.
//   - A config-file value is re-read from the file itself (configFileValue),
//     because viper lower-cases every map key it loads, including keys of maps
//     nested inside the value: a plugin ID "MyPlugin", a plugin setting
//     "ApiKey" or an embedding model name with capitals would otherwise come
//     out lower-cased and match nothing. (A map keyed by a case-free
//     identifier, dedup.signals.confidence's signal kinds, is folded back to
//     lower case by its caller with lowerMapKeys.) Struct field names survive either way
//     (encoding/json matches them case-insensitively); map keys do not.
//   - Anything else (viper.Set, a default) is decoded from viper's value as is.
//
// A value that does not decode is logged at Warn, once per load, naming the
// key and the error, and yields the zero T (the field keeps its default)
// rather than failing startup, the same rule as ParseWhisperEndpoints: the
// stored blob, which normally owns these UI-managed settings, still loads on
// top.
func viperDecodeJSON[T any](key string) T {
	var out T
	if !viper.IsSet(key) {
		return out
	}
	var raw []byte
	switch v := viper.Get(key).(type) {
	case string:
		text := strings.TrimSpace(v)
		if text == "" {
			return out
		}
		raw = []byte(text)
	default:
		val := any(v)
		if fileVal, ok := configFileValue(key, v); ok {
			val = fileVal
		}
		b, err := json.Marshal(val)
		if err != nil {
			configLog.Warn("config: ignoring %s: cannot encode its value (%v); the setting keeps its default", key, err)
			return out
		}
		raw = b
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		configLog.Warn("config: ignoring %s: not valid JSON for this setting (%v); the setting keeps its default. "+
			"An environment value must be JSON text, e.g. %s='[...]' for a list or '{...}' for a map",
			key, err, strings.ToUpper(key))
		var zero T
		return zero
	}
	return out
}

// configFileValue returns the value at key exactly as the config file spells
// it, map-key case intact, when that is where viper's value came from. It
// re-reads viper.ConfigFileUsed() (YAML or JSON; yaml.v3 parses both) and
// walks the dotted key, matching each path segment case-insensitively the way
// viper does. ok is false when there is no config file, the format is not one
// this can read, the file lacks the key, or the file's value is not viper's
// value: the comparison lower-cases every map key on both sides, so a value
// that a higher-precedence source (viper.Set, a flag, the environment)
// replaced is never swapped for the file's. That keeps viper's precedence
// without having to know which layer won.
//
// For a config file in another format (TOML, HCL, INI...) map keys stay
// lower-cased and this logs a Warn when the value holds a map, naming the
// key, so the operator knows to set it through the settings API instead.
func configFileValue(key string, viperVal any) (any, bool) {
	path := viper.ConfigFileUsed()
	if path == "" {
		return nil, false
	}
	switch strings.ToLower(strings.TrimPrefix(filepath.Ext(path), ".")) {
	case "yaml", "yml", "json", "":
		// "" is cmd/root.go's ".audiobook-organizer", read with SetConfigType("yaml").
	default:
		if containsMap(viperVal) {
			configLog.Warn("config: %s in %s: map keys from this config file format are read lower-cased; "+
				"use a YAML or JSON config file, or set %s through the settings API, to keep key case",
				key, filepath.Base(path), key)
		}
		return nil, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	var root any
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, false
	}
	cur := stringKeyed(root)
	for _, seg := range strings.Split(key, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		k, ok := lookupFold(m, seg)
		if !ok {
			return nil, false
		}
		cur = m[k]
	}
	fileJSON, err1 := json.Marshal(lowerKeys(cur))
	viperJSON, err2 := json.Marshal(lowerKeys(viperVal))
	if err1 != nil || err2 != nil || string(fileJSON) != string(viperJSON) {
		return nil, false
	}
	return cur, true
}

// lookupFold finds the key of m equal to seg ignoring case, preferring an
// exact match, then the lexically first fold match so the choice is stable.
func lookupFold(m map[string]any, seg string) (string, bool) {
	if _, ok := m[seg]; ok {
		return seg, true
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if strings.EqualFold(k, seg) {
			return k, true
		}
	}
	return "", false
}

// stringKeyed converts every map[any]any (a YAML mapping with a non-string
// key) under v into map[string]any, keeping key spelling, so it marshals as
// JSON. Keys are rendered with json-compatible text via their YAML scalar form.
func stringKeyed(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[k] = stringKeyed(val)
		}
		return out
	case map[any]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			ks, ok := k.(string)
			if !ok {
				b, err := yaml.Marshal(k)
				if err != nil {
					continue
				}
				ks = strings.TrimSpace(string(b))
			}
			out[ks] = stringKeyed(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = stringKeyed(val)
		}
		return out
	}
	return v
}

// lowerKeys returns v with every map key lower-cased, recursing through maps
// and lists: viper's view of a config-file value.
func lowerKeys(v any) any {
	switch t := stringKeyed(v).(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[strings.ToLower(k)] = lowerKeys(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = lowerKeys(val)
		}
		return out
	default:
		return t
	}
}

// lowerMapKeys returns m with its keys lower-cased, for maps keyed by an
// identifier whose case carries no meaning (signal kinds). When two keys fold
// to the same one, the key already spelled in lower case wins, so the result
// does not depend on map iteration order. nil stays nil.
func lowerMapKeys[V any](m map[string]V) map[string]V {
	if m == nil {
		return nil
	}
	out := make(map[string]V, len(m))
	for k, v := range m {
		if lk := strings.ToLower(k); lk != k {
			out[lk] = v
		}
	}
	for k, v := range m {
		if strings.ToLower(k) == k {
			out[k] = v
		}
	}
	return out
}

// containsMap reports whether v is or holds a map.
func containsMap(v any) bool {
	switch t := v.(type) {
	case map[string]any, map[any]any:
		return true
	case []any:
		for _, e := range t {
			if containsMap(e) {
				return true
			}
		}
	}
	return false
}

// viperPathList reads a list of filesystem paths (protected_paths). A config
// file gives a YAML/JSON list. An environment variable is a string, which is
// read as either a JSON array (PROTECTED_PATHS='["/a","/b, c"]') or a
// comma-separated list (PROTECTED_PATHS=/a,/b); each entry is trimmed and
// empty entries are dropped. Whitespace is NOT a separator, because paths
// contain spaces ("/mnt/My Books" is one path); a path that contains a comma
// needs the JSON form. viper.GetStringSlice split an env string on whitespace
// only, so "/a,/b" became the single entry "/a,/b" and protected neither path.
//
// A JSON array that does not parse is logged at Error, naming the key, and
// yields no entries: the field keeps its default. Guessing at a broken value
// could protect the wrong paths; the log line is the operator's signal.
func viperPathList(key string) []string {
	s, isString := viper.Get(key).(string)
	if !isString {
		return cleanPathList(viper.GetStringSlice(key))
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	if strings.HasPrefix(s, "[") {
		var list []string
		if err := json.Unmarshal([]byte(s), &list); err != nil {
			configLog.Error("config: ignoring %s: it starts with '[' but is not a JSON array of strings (%v); "+
				"no paths from it are protected. Use a JSON array or a comma-separated list", key, err)
			return nil
		}
		return cleanPathList(list)
	}
	return cleanPathList(strings.Split(s, ","))
}

// cleanPathList trims each entry and drops empty ones; nil when none remain.
func cleanPathList(in []string) []string {
	var out []string
	for _, p := range in {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
