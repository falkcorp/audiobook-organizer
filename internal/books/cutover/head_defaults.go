// file: internal/books/cutover/head_defaults.go
// version: 1.1.0
// guid: ea8d2a0e-bf7a-49fb-b3f7-30318998436f
// last-edited: 2026-11-11

// FROZEN COPY of HEAD (748fd787a) registered viper defaults (the key set of registerConfigDefaults, recorded by running it); read-only; deleted in R3 with the other HEAD-format readers (05-config §1.12.2, 03 §3.8).

package cutover

import (
	_ "embed"
	"encoding/json"
	"maps"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"sync"
)

// headDefaultsJSON is HEAD's SetDefault table: viper key (dotted for nested
// keys) to default value, 272 keys. It is data, not behaviour: M2 uses it to
// say which dropped (class x) values were non-default, and the tests' stand-in
// registry takes its defaults from it.
//
//go:embed testdata/head_defaults.json
var headDefaultsJSON []byte

var headDefaultsTable = sync.OnceValue(func() map[string]any {
	var m map[string]any
	if err := json.Unmarshal(headDefaultsJSON, &m); err != nil {
		panic("cutover: testdata/head_defaults.json: " + err.Error())
	}
	delete(m, "_header") // the file's version header, not a default
	return m
})

// headNumCPU is the host's CPU count, which HEAD's registerConfigDefaults
// reads when it registers concurrent_scans. It is a variable so a test can pin
// the host: M2 runs on the machine HEAD ran on, so the default it compares a
// file or blob value against is the one that machine computed.
var headNumCPU = runtime.NumCPU

// headComputedDefaultNames are the keys whose HEAD default is not a constant
// of the recorded table; headComputedDefault answers them.
var headComputedDefaultNames = []string{"concurrent_scans", "playlist_dir"}

// headComputedDefault returns the default of a key the recorded table cannot
// hold, read at call time:
//   - concurrent_scans: max(runtime.NumCPU(), 4) (config.go registerConfigDefaults),
//     so the table's recorded 10 is only the recording host's value.
//   - playlist_dir: "playlists", the flag default cmd/root.go binds (the
//     key has no SetDefault, so the table omits it), which is what viper
//     returns for a key the file does not set. An explicit "" in the file is
//     therefore a value, not the default.
func headComputedDefault(name string) (any, bool) {
	switch name {
	case "concurrent_scans":
		return float64(max(headNumCPU(), 4)), true
	case "playlist_dir":
		return "playlists", true
	}
	return nil, false
}

// headDefault returns HEAD's default for the viper key name.
func headDefault(name string) (any, bool) {
	if v, ok := headComputedDefault(name); ok {
		return v, true
	}
	v, ok := headDefaultsTable()[name]
	return v, ok
}

// headIsDefault reports whether v, a decoded JSON value stored under the
// dotted HEAD name, equals HEAD's default: a leaf against its own default, an
// object against its leaves' (a leaf with no default counts as default only
// when it is the zero value).
func headIsDefault(name string, v any) bool {
	if obj, ok := v.(map[string]any); ok {
		for k, child := range obj {
			if !headIsDefault(name+"."+k, child) {
				return false
			}
		}
		return true
	}
	def, ok := headDefault(name)
	if !ok {
		return reflect.ValueOf(v).IsZero() || v == nil
	}
	return headJSONEqual(def, v)
}

// headJSONEqual compares two decoded JSON values, numbers by value.
func headJSONEqual(a, b any) bool {
	an, aok := headNumber(a)
	bn, bok := headNumber(b)
	if aok && bok {
		return an == bn
	}
	return reflect.DeepEqual(a, b)
}

func headNumber(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	}
	return 0, false
}

// headDefaultConfig is HEAD's Config as its SetDefault table builds it: the
// value an absent field takes. HEAD unmarshals a stored blob onto the live
// config, which at boot holds these defaults, so the AI-endpoints migration
// (headMigrateAIEndpointsBlob), which derives rows from the resolved config
// and not from the blob alone, starts from this and not from a zero config.
// Every other read of the blob stages only what the blob holds
// (headDecodeBlob).
//
// A default that is not its field's JSON type stays at the field's zero value
// and its top-level member is returned in misfit: viper's default for
// whisper_endpoints is the empty string of its environment form, which HEAD's
// configFromViper parses separately (ParseWhisperEndpoints).
func headDefaultConfig() (cfg *headConfig, misfit []string) {
	defaults := maps.Clone(headDefaultsTable())
	for _, name := range headComputedDefaultNames {
		defaults[name], _ = headComputedDefault(name)
	}
	tree := map[string]any{}
	for name, v := range defaults {
		cur := tree
		segs := strings.Split(name, ".")
		for _, s := range segs[:len(segs)-1] {
			next, ok := cur[s].(map[string]any)
			if !ok {
				next = map[string]any{}
				cur[s] = next
			}
			cur = next
		}
		cur[segs[len(segs)-1]] = v
	}
	members := make([]string, 0, len(tree))
	for name := range tree {
		members = append(members, name)
	}
	sort.Strings(members)
	cfg = &headConfig{}
	for _, name := range members {
		one, err := json.Marshal(map[string]any{name: tree[name]})
		if err != nil || json.Unmarshal(one, cfg) != nil {
			misfit = append(misfit, name)
		}
	}
	return cfg, misfit
}
