// file: internal/config/viper_decode.go
// version: 1.1.2
// guid: b431724a-2f0b-413b-8704-45a5807bee77
// last-edited: 2026-10-30

package config

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/go-viper/mapstructure/v2"
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
// The decode is strict JSON first. If that fails on a type mismatch, it is
// retried weakly typed (decodeWeakly), the way viper's own decoder treats
// scalars: PluginConfig.Settings is map[string]string, and YAML's natural
// `Port: 8080` or `Enabled: true` is a number or bool, which strict JSON
// refuses and which would otherwise discard EVERY plugin, not just that one.
//
// A value that does not decode is logged at Warn, once per load, naming the
// key and the error, and yields the zero T (the field keeps its default)
// rather than failing startup, the same rule as ParseWhisperEndpoints: the
// stored blob, which normally owns these UI-managed settings, still loads on
// top.
func viperDecodeJSON[T any](vp *viper.Viper, key string) T {
	var out T
	if !vp.IsSet(key) {
		return out
	}
	var raw []byte
	switch v := vp.Get(key).(type) {
	case string:
		text := strings.TrimSpace(v)
		if text == "" {
			return out
		}
		raw = []byte(text)
	default:
		val := any(v)
		if fileVal, ok := configFileValue(vp, key, v); ok {
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
		if weak, werr := decodeWeakly[T](raw); werr == nil {
			return weak
		}
		configLog.Warn("config: ignoring %s: not valid JSON for this setting (%v); the setting keeps its default. "+
			"An environment value must be JSON text, e.g. %s='[...]' for a list or '{...}' for a map",
			key, err, strings.ToUpper(key))
		var zero T
		return zero
	}
	return out
}

// decodeWeakly decodes the JSON text raw into a T the way viper's own decoder
// would have: a scalar of the wrong kind is converted rather than refused. A
// number becomes its decimal text for a string target ("8080"), a bool becomes
// "true"/"false" (mapstructure's own weak rule would give "1"/"0"), and a
// numeric string fills a number. Struct fields are matched by their json tag,
// case-insensitively, the same as encoding/json, and map keys keep their case.
// Numbers are kept as json.Number, so a large integer is not rounded through
// float64.
func decodeWeakly[T any](raw []byte) (T, error) {
	var out, zero T
	var generic any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&generic); err != nil {
		return zero, err
	}
	d, err := mapstructure.NewDecoder(&mapstructure.DecoderConfig{
		Result:           &out,
		TagName:          "json",
		WeaklyTypedInput: true,
		DecodeHook:       mapstructure.ComposeDecodeHookFunc(boolToStringHook),
	})
	if err != nil {
		return zero, err
	}
	if err := d.Decode(generic); err != nil {
		return zero, err
	}
	return out, nil
}

// boolToStringHook renders a bool headed for a string as "true"/"false".
func boolToStringHook(from, to reflect.Type, data any) (any, error) {
	if from.Kind() == reflect.Bool && to.Kind() == reflect.String {
		return strconv.FormatBool(reflect.ValueOf(data).Bool()), nil
	}
	return data, nil
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
// Which files are read as YAML: everything except the viper formats known to
// be something else (nonYAMLConfigExt). The extension cannot be trusted to
// name the format: cmd/root.go's default config file is `~/.audiobook-organizer`
// read with SetConfigType("yaml"), and filepath.Ext of that returns the whole
// ".audiobook-organizer". Viper does not expose the type it was given, so a
// file with an unknown or missing extension is tried as YAML. A wrong guess is
// harmless: it either fails to parse or fails the equality check below.
//
// For a config file in another format (TOML, HCL, INI...), or one that does
// not parse as YAML, map keys stay lower-cased and this logs a Warn when the
// value holds a map, naming the key, so the operator knows to set it through
// the settings API instead.
func configFileValue(vp *viper.Viper, key string, viperVal any) (any, bool) {
	path := vp.ConfigFileUsed()
	if path == "" {
		return nil, false
	}
	warnLowerCased := func(why string) {
		if containsMap(viperVal) {
			configLog.Warn("config: %s in %s: map keys from this config file are read lower-cased (%s); "+
				"use a YAML or JSON config file, or set %s through the settings API, to keep key case",
				key, filepath.Base(path), why, key)
		}
	}
	if nonYAMLConfigExt[strings.ToLower(strings.TrimPrefix(filepath.Ext(path), "."))] {
		warnLowerCased("format is not YAML or JSON")
		return nil, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	var root any
	if err := yaml.Unmarshal(data, &root); err != nil {
		warnLowerCased("file does not parse as YAML or JSON")
		return nil, false
	}
	cur, ok := walkConfigKey(stringKeyed(root), strings.Split(key, "."))
	if !ok {
		return nil, false
	}
	fileJSON, err1 := json.Marshal(lowerKeys(cur))
	viperJSON, err2 := json.Marshal(lowerKeys(viperVal))
	if err1 != nil || err2 != nil || string(fileJSON) != string(viperJSON) {
		return nil, false
	}
	return cur, true
}

// nonYAMLConfigExt is the viper config formats (viper.SupportedExts) that are
// not YAML or JSON, spelled as filepath.Ext gives them, lower-cased.
var nonYAMLConfigExt = map[string]bool{
	"toml": true, "hcl": true, "tfvars": true,
	"ini": true, "properties": true, "props": true, "prop": true,
	"dotenv": true, "env": true,
}

// walkConfigKey finds the value at the dotted path segs under cur, matching
// each key case-insensitively like viper. At every level it tries the longest
// run of remaining segments joined with "." as a single key first, the way
// viper's searchIndexableWithPathPrefixes does, so a flat top-level key such as
// `dedup.embedding_thresholds_by_model:` is found as well as the nested
// `dedup:` / `embedding_thresholds_by_model:` spelling.
func walkConfigKey(cur any, segs []string) (any, bool) {
	m, ok := cur.(map[string]any)
	if !ok {
		return nil, false
	}
	for n := len(segs); n >= 1; n-- {
		k, found := lookupFold(m, strings.Join(segs[:n], "."))
		if !found {
			continue
		}
		if n == len(segs) {
			return m[k], true
		}
		if v, ok := walkConfigKey(m[k], segs[n:]); ok {
			return v, true
		}
	}
	return nil, false
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
// identifier whose case carries no meaning (signal kinds). When several keys
// fold to the same one the winner does not depend on map iteration order: a
// key already spelled in lower case beats every other spelling, and among the
// others the byte-wise first one (so "EXACT_FILE" beats "Exact_File") wins.
// nil stays nil.
func lowerMapKeys[V any](m map[string]V) map[string]V {
	if m == nil {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make(map[string]V, len(m))
	for _, k := range keys {
		if strings.ToLower(k) == k {
			out[k] = m[k]
		}
	}
	for _, k := range keys {
		if lk := strings.ToLower(k); lk != k {
			if _, taken := out[lk]; !taken {
				out[lk] = m[k]
			}
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
		return slices.ContainsFunc(t, containsMap)
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
func viperPathList(vp *viper.Viper, key string) []string {
	s, isString := vp.Get(key).(string)
	if !isString {
		return cleanPathList(vp.GetStringSlice(key))
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
