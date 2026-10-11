// file: internal/config/initconfig_reads_every_key_test.go
// version: 1.0.0
// guid: 9d3f6a21-5c87-4e0b-b2d4-8f1a7c6e0b39
// last-edited: 2026-10-11

// InitConfig builds AppConfig from a hand-written struct literal, and that
// literal is the ONLY place a viper value (shipped default, config file, env)
// reaches AppConfig. A Config field left out of it is silently zero at every
// boot whatever the operator configures: on 2026-10-11 the #3952 review found
// 30 such fields, among them log_retention_days (default 90), so the
// purge_old_logs task had never been enabled on production.
//
// These tests enumerate the keys automatically, from the Config type's tags
// and from viper's own key list, so a new field or a new default is covered
// the day it is added. Nothing here is a hand-maintained list of fields that
// must be read; the only lists are the short, reasoned exception tables.

package config

import (
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/spf13/viper"
)

// configLeaf is one settable Config field and the viper key that feeds it.
type configLeaf struct {
	key   string       // dotted viper key, lower-case
	index []int        // reflect field index path from Config
	typ   reflect.Type // the field's type
}

// configKeyName is the viper key segment for a struct field: the mapstructure
// tag, else the json tag, else the lower-cased field name (viper's own rule).
// "" means the field is not configurable (tag "-").
func configKeyName(f reflect.StructField) string {
	for _, tag := range []string{"mapstructure", "json"} {
		if v, ok := f.Tag.Lookup(tag); ok {
			name, _, _ := strings.Cut(v, ",")
			if name == "-" {
				return ""
			}
			if name != "" {
				return strings.ToLower(name)
			}
		}
	}
	return strings.ToLower(f.Name)
}

// isLeafType reports whether a field of type t is set as one value rather
// than recursed into: everything except a plain struct is a leaf (scalars,
// slices, maps, durations), and so is a struct with its own decoding.
func isLeafType(t reflect.Type) bool {
	if t.Kind() != reflect.Struct {
		return true
	}
	if t == reflect.TypeFor[time.Time]() {
		return true
	}
	return reflect.PointerTo(t).Implements(reflect.TypeFor[interface{ UnmarshalJSON([]byte) error }]())
}

// configLeaves walks Config and returns every settable leaf.
func configLeaves() []configLeaf {
	var out []configLeaf
	var walk func(t reflect.Type, prefix string, index []int)
	walk = func(t reflect.Type, prefix string, index []int) {
		for i := range t.NumField() {
			f := t.Field(i)
			if !f.IsExported() {
				continue
			}
			name := configKeyName(f)
			if name == "" {
				continue
			}
			key := name
			if prefix != "" {
				key = prefix + "." + name
			}
			idx := append(slices.Clone(index), i)
			if f.Anonymous && f.Type.Kind() == reflect.Struct {
				walk(f.Type, prefix, idx) // embedded: fields promoted to this level
				continue
			}
			if isLeafType(f.Type) {
				out = append(out, configLeaf{key: key, index: idx, typ: f.Type})
				continue
			}
			walk(f.Type, key, idx)
		}
	}
	walk(reflect.TypeFor[Config](), "", nil)
	return out
}

// sentinelFor returns a non-zero viper value that decodes into t. Composite
// values are built the way a config file would spell them (maps of maps,
// lists of maps), so they go through the same decode path InitConfig uses.
func sentinelFor(t reflect.Type) any {
	switch t.Kind() {
	case reflect.String:
		return "sentinel"
	case reflect.Bool:
		return true
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if t == reflect.TypeFor[time.Duration]() {
			return "7m"
		}
		return 7
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return 7
	case reflect.Float32, reflect.Float64:
		return 0.25
	case reflect.Slice:
		return []any{sentinelFor(t.Elem())}
	case reflect.Map:
		return map[string]any{"sentinel": sentinelFor(t.Elem())}
	case reflect.Pointer:
		return sentinelFor(t.Elem())
	case reflect.Struct:
		m := map[string]any{}
		for i := range t.NumField() {
			f := t.Field(i)
			if name := configKeyName(f); f.IsExported() && name != "" {
				m[name] = sentinelFor(f.Type)
			}
		}
		return m
	}
	return "sentinel"
}

// sentinelOverrides are keys whose configured form is not the field's Go
// shape: the environment-oriented string encodings. Each value is a non-zero
// input in the form InitConfig parses.
var sentinelOverrides = map[string]any{
	"whisper_endpoints": `[{"url":"http://192.0.2.1:9000"}]`, // WHISPER_ENDPOINTS: a JSON array in a string
	"whisper_requires":  "gpu",                               // WHISPER_REQUIRES: a comma-separated label list
}

// scalarSentinelValue is the Go value sentinelFor's input must decode to for
// a scalar type, or ok=false for composite types (checked for non-zero only).
func scalarSentinelValue(t reflect.Type) (any, bool) {
	var v reflect.Value
	switch t.Kind() {
	case reflect.String:
		v = reflect.ValueOf("sentinel")
	case reflect.Bool:
		v = reflect.ValueOf(true)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if t == reflect.TypeFor[time.Duration]() {
			return 7 * time.Minute, true
		}
		v = reflect.ValueOf(int64(7))
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v = reflect.ValueOf(uint64(7))
	case reflect.Float32, reflect.Float64:
		v = reflect.ValueOf(0.25)
	default:
		return nil, false
	}
	return v.Convert(t).Interface(), true
}

// initConfigLeafExemptions are Config leaves InitConfig deliberately does not
// fill from viper, with the reason. Adding an entry here needs the reason to
// be true: a field missing from the literal is exactly the bug this test
// exists to catch, and "the consumer falls back on zero" is NOT a reason.
var initConfigLeafExemptions = map[string]string{}

// TestInitConfigReadsEveryConfigKey sets a non-zero value for every Config
// leaf's key, as a config file or environment variable would, runs
// InitConfig, and requires every leaf to come out non-zero. A field InitConfig
// never reads stays zero and fails here by name.
//
// Non-zero rather than equal on purpose: InitConfig legitimately passes some
// values through wrappers (label parsing, float-slice parsing, enum
// normalisation, minimum clamps), and an equality check would need an
// exception per wrapper. A field that is not read at all is always zero, which
// is the failure this guards.
func TestInitConfigReadsEveryConfigKey(t *testing.T) {
	restoreAppConfig(t)
	viper.Reset()
	t.Cleanup(viper.Reset)

	leaves := configLeaves()
	if len(leaves) < 300 {
		t.Fatalf("walked only %d Config leaves; the walker is broken, not the config", len(leaves))
	}
	for _, l := range leaves {
		if v, ok := sentinelOverrides[l.key]; ok {
			viper.Set(l.key, v)
			continue
		}
		viper.Set(l.key, sentinelFor(l.typ))
	}

	InitConfig()

	cfg := reflect.ValueOf(Snapshot())
	var missing, wrong []string
	for _, l := range leaves {
		if _, ok := initConfigLeafExemptions[l.key]; ok {
			continue
		}
		got := cfg.FieldByIndex(l.index)
		if got.IsZero() {
			missing = append(missing, l.key)
			continue
		}
		// A scalar must come out as exactly the configured value: a constant
		// assigned in InitConfig (the tools.* block was one) is non-zero but
		// ignores the operator just as completely.
		if want, ok := scalarSentinelValue(l.typ); ok && got.Interface() != want {
			wrong = append(wrong, fmt.Sprintf("%s = %v, configured %v", l.key, got.Interface(), want))
		}
	}
	sort.Strings(missing)
	sort.Strings(wrong)
	if len(missing) > 0 {
		t.Errorf("%d Config field(s) are never populated by InitConfig, so their default, config-file "+
			"and environment values are silently dropped. Read each one in InitConfig's Config "+
			"literal (or, if it truly must not be read there, add it to initConfigLeafExemptions "+
			"with the reason):\n  %s", len(missing), strings.Join(missing, "\n  "))
	}
	if len(wrong) > 0 {
		t.Errorf("%d Config field(s) ignore the configured value (InitConfig assigns something else):\n  %s",
			len(wrong), strings.Join(wrong, "\n  "))
	}

	// The exemption table must not rot: an exempt key that no longer names a
	// leaf is a stale entry.
	have := make(map[string]bool, len(leaves))
	for _, l := range leaves {
		have[l.key] = true
	}
	for k := range initConfigLeafExemptions {
		if !have[k] {
			t.Errorf("initConfigLeafExemptions names %q, which is not a Config leaf; remove it", k)
		}
	}
}

// viperKeysReadOutsideConfig are viper keys that carry a default (or BindEnv)
// but deliberately have no Config field, because something reads them from
// viper directly. Each must say who reads it.
var viperKeysReadOutsideConfig = map[string]string{
	"audible_base_url":                "metadata clients read viper directly (provider base URL override)",
	"openlibrary_base_url":            "metadata clients read viper directly (provider base URL override)",
	"audnexus_base_url":               "metadata clients read viper directly (provider base URL override)",
	"google_books_base_url":           "read after the literal in InitConfig to build the google-books source",
	"dedup.signals.duration.boost":    "read by unified.LoadScoreConfig via viper",
	"dedup.signals.folder_path.boost": "read by unified.LoadScoreConfig via viper",
}

// TestEveryViperDefaultHasAConfigField is the other direction: every key
// viper knows about after InitConfig (every SetDefault and BindEnv) must map
// to a Config leaf, or sit under a map/list leaf, or be one of the reasoned
// keys read from viper directly. A default set on a key no field reads is a
// default that can never take effect.
func TestEveryViperDefaultHasAConfigField(t *testing.T) {
	restoreAppConfig(t)
	viper.Reset()
	t.Cleanup(viper.Reset)
	InitConfig()

	leaves := configLeaves()
	leafKeys := make(map[string]bool, len(leaves))
	for _, l := range leaves {
		leafKeys[l.key] = true
	}
	underLeaf := func(k string) bool {
		for p := k; ; {
			i := strings.LastIndex(p, ".")
			if i < 0 {
				return false
			}
			p = p[:i]
			if leafKeys[p] {
				return true
			}
		}
	}

	var orphans []string
	for _, k := range viper.AllKeys() {
		if leafKeys[k] || underLeaf(k) {
			continue
		}
		if _, ok := viperKeysReadOutsideConfig[k]; ok {
			continue
		}
		orphans = append(orphans, k)
	}
	sort.Strings(orphans)
	if len(orphans) > 0 {
		t.Fatalf("%d viper key(s) have a default or env binding but no Config field, so the value "+
			"can never reach AppConfig. Add the field (and read it in InitConfig), fix the key's "+
			"spelling, or list it in viperKeysReadOutsideConfig with its reader:\n  %s",
			len(orphans), strings.Join(orphans, "\n  "))
	}

	all := viper.AllKeys()
	for k := range viperKeysReadOutsideConfig {
		if !slices.Contains(all, k) {
			t.Errorf("viperKeysReadOutsideConfig names %q, which viper no longer knows; remove it", k)
		}
	}
}
