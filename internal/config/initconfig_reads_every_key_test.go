// file: internal/config/initconfig_reads_every_key_test.go
// version: 1.2.0
// guid: 9d3f6a21-5c87-4e0b-b2d4-8f1a7c6e0b39
// last-edited: 2026-10-30

// InitConfig builds AppConfig from a hand-written struct literal, and that
// literal is the ONLY place a viper value (shipped default, config file, env)
// reaches AppConfig. A Config field left out of it is silently zero at every
// boot whatever the operator configures: on 2026-10-11 the #3952 review found
// 29 Config fields missing from the literal (among them log_retention_days,
// default 90, so the purge_old_logs task had never been enabled on
// production), plus the 7 leaves of the tools.* block, which were assigned
// constants instead of read.
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
// "" means the field is not configurable. hidden reports a field whose only
// opt-out is json:"-" (no mapstructure tag): it is invisible to the stored
// blob and the API, but viper could still feed it, so whether InitConfig must
// read it is a choice the author has to make explicitly (see
// jsonHiddenFieldExemptions).
func configKeyName(f reflect.StructField) (name string, hidden bool) {
	if v, ok := f.Tag.Lookup("mapstructure"); ok {
		n, _, _ := strings.Cut(v, ",")
		if n == "-" {
			return "", false // explicit: not a viper key
		}
		if n != "" {
			return strings.ToLower(n), false
		}
	}
	if v, ok := f.Tag.Lookup("json"); ok {
		n, _, _ := strings.Cut(v, ",")
		if n == "-" {
			return "", true
		}
		if n != "" {
			return strings.ToLower(n), false
		}
	}
	return strings.ToLower(f.Name), false
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

// configLeaves walks Config and returns every settable leaf, plus the Go path
// (Type.Field) of every json:"-" field without a mapstructure tag.
func configLeaves() (leaves []configLeaf, hidden []string) {
	var walk func(t reflect.Type, prefix string, index []int)
	walk = func(t reflect.Type, prefix string, index []int) {
		for i := range t.NumField() {
			f := t.Field(i)
			if !f.IsExported() {
				continue
			}
			name, isHidden := configKeyName(f)
			if isHidden {
				hidden = append(hidden, t.Name()+"."+f.Name)
			}
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
				leaves = append(leaves, configLeaf{key: key, index: idx, typ: f.Type})
				continue
			}
			walk(f.Type, key, idx)
		}
	}
	walk(reflect.TypeFor[Config](), "", nil)
	return leaves, hidden
}

// jsonHiddenFieldExemptions are Config fields tagged json:"-" without a
// mapstructure tag (Type.Field) that InitConfig deliberately does not read,
// with the reason. Such a field is skipped by the leaf walk, so without this
// table a future env-only json:"-" field that InitConfig never reads would
// slip through: tag it mapstructure:"<key>" (then it is a leaf and must be
// read) or mapstructure:"-" (not configurable), or list it here.
var jsonHiddenFieldExemptions = map[string]string{}

// sentinelFor returns a viper value that decodes into t, distinct per leaf:
// strings are the key itself, numbers derive from the leaf's index, durations
// from the index in minutes, so a field filled from the WRONG key (a
// copy-paste of the neighbouring line in the literal) comes out with another
// leaf's value and fails the equality check. Bools cannot be distinct in one
// run; TestInitConfigReadsEachBoolFromItsOwnKey covers them. Composite values
// are built the way a config file would spell them (maps of maps, lists of
// maps), with key-derived scalars inside, so they go through the same decode
// path InitConfig uses.
func sentinelFor(t reflect.Type, key string, i int) any {
	switch t.Kind() {
	case reflect.String:
		return key
	case reflect.Bool:
		return true
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if t == reflect.TypeFor[time.Duration]() {
			return fmt.Sprintf("%dm", i+1)
		}
		return 1000 + i
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return 1000 + i
	case reflect.Float32, reflect.Float64:
		return 0.25 + float64(i)/10000
	case reflect.Slice:
		return []any{sentinelFor(t.Elem(), key, i)}
	case reflect.Map:
		return map[string]any{"sentinel": sentinelFor(t.Elem(), key, i)}
	case reflect.Pointer:
		return sentinelFor(t.Elem(), key, i)
	case reflect.Struct:
		m := map[string]any{}
		for f := range t.Fields() {
			if name, _ := configKeyName(f); f.IsExported() && name != "" {
				m[name] = sentinelFor(f.Type, key+"."+name, i)
			}
		}
		return m
	}
	return key
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
func scalarSentinelValue(t reflect.Type, key string, i int) (any, bool) {
	var v reflect.Value
	switch t.Kind() {
	case reflect.String:
		v = reflect.ValueOf(key)
	case reflect.Bool:
		v = reflect.ValueOf(true)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if t == reflect.TypeFor[time.Duration]() {
			return time.Duration(i+1) * time.Minute, true
		}
		v = reflect.ValueOf(int64(1000 + i))
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v = reflect.ValueOf(uint64(1000 + i))
	case reflect.Float32, reflect.Float64:
		v = reflect.ValueOf(0.25 + float64(i)/10000)
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

// setSentinels sets every leaf's key to its distinct sentinel (or its
// sentinelOverrides form), as a config file or environment variable would.
func setSentinels(leaves []configLeaf) {
	for i, l := range leaves {
		if v, ok := sentinelOverrides[l.key]; ok {
			viper.Set(l.key, v)
			continue
		}
		viper.Set(l.key, sentinelFor(l.typ, l.key, i))
	}
}

// TestInitConfigReadsEveryConfigKey sets a distinct non-zero value for every
// Config leaf's key, runs InitConfig, and requires every leaf to come out
// non-zero, and every scalar leaf to come out as exactly its own key's value.
// A field InitConfig never reads stays zero and fails here by name; a field
// read from the wrong key (or assigned a constant) carries another value and
// fails as "wrong".
//
// Composite leaves (lists, maps) are checked for non-zero only: InitConfig
// legitimately passes some through wrappers (label parsing, float-slice
// parsing), and an equality check would need an exception per wrapper. A
// field that is not read at all is always zero, which is the failure this
// guards for them.
func TestInitConfigReadsEveryConfigKey(t *testing.T) {
	restoreAppConfig(t)
	viper.Reset()
	t.Cleanup(viper.Reset)

	leaves, _ := configLeaves()
	if len(leaves) < 300 {
		t.Fatalf("walked only %d Config leaves; the walker is broken, not the config", len(leaves))
	}
	setSentinels(leaves)

	InitConfig()

	cfg := reflect.ValueOf(Snapshot())
	var missing, wrong []string
	for i, l := range leaves {
		if _, ok := initConfigLeafExemptions[l.key]; ok {
			continue
		}
		got := cfg.FieldByIndex(l.index)
		if got.IsZero() {
			missing = append(missing, l.key)
			continue
		}
		if _, overridden := sentinelOverrides[l.key]; overridden {
			continue
		}
		// A scalar must come out as exactly its own key's value: a constant
		// assigned in InitConfig (the tools.* block was one) or a read of a
		// neighbouring key is non-zero but ignores the operator just as
		// completely.
		if want, ok := scalarSentinelValue(l.typ, l.key, i); ok && got.Interface() != want {
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
		t.Errorf("%d Config field(s) do not hold their own key's configured value (InitConfig assigns "+
			"something else, or reads another key):\n  %s", len(wrong), strings.Join(wrong, "\n  "))
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

// TestInitConfigReadsEachBoolFromItsOwnKey is the wrong-key check for bools,
// which cannot carry a distinct value in one run. For each bool leaf it sets
// every bool key false and that one key true, and requires that field to come
// out true. A field read from any other key comes out false; a field another
// field's key feeds is caught in that other field's own pass.
func TestInitConfigReadsEachBoolFromItsOwnKey(t *testing.T) {
	restoreAppConfig(t)
	t.Cleanup(viper.Reset)

	leaves, _ := configLeaves()
	var bools []configLeaf
	for _, l := range leaves {
		if l.typ.Kind() == reflect.Bool {
			if _, ok := initConfigLeafExemptions[l.key]; !ok {
				bools = append(bools, l)
			}
		}
	}
	if len(bools) < 50 {
		t.Fatalf("found only %d bool leaves; the walker is broken, not the config", len(bools))
	}
	var wrong []string
	for _, target := range bools {
		viper.Reset()
		for _, l := range bools {
			viper.Set(l.key, l.key == target.key)
		}
		InitConfig()
		if !reflect.ValueOf(Snapshot()).FieldByIndex(target.index).Bool() {
			wrong = append(wrong, target.key)
		}
	}
	sort.Strings(wrong)
	if len(wrong) > 0 {
		t.Errorf("%d bool Config field(s) stayed false with only their own key set true, so InitConfig "+
			"reads them from another key (or assigns a constant):\n  %s", len(wrong), strings.Join(wrong, "\n  "))
	}
}

// A json:"-" field without a mapstructure tag is skipped by the leaf walk.
// Fail on one rather than let it slip past both tests above: the author tags
// it mapstructure:"<key>" (a leaf InitConfig must read), mapstructure:"-"
// (not configurable), or lists it in jsonHiddenFieldExemptions with a reason.
func TestNoUnreviewedJSONHiddenConfigFields(t *testing.T) {
	_, hidden := configLeaves()
	var unreviewed []string
	for _, h := range hidden {
		if _, ok := jsonHiddenFieldExemptions[h]; !ok {
			unreviewed = append(unreviewed, h)
		}
	}
	if len(unreviewed) > 0 {
		t.Errorf("%d Config field(s) are tagged json:\"-\" with no mapstructure tag, so neither guard test "+
			"checks whether InitConfig reads them. Tag each mapstructure:\"<key>\" or mapstructure:\"-\", "+
			"or add it to jsonHiddenFieldExemptions with the reason:\n  %s",
			len(unreviewed), strings.Join(unreviewed, "\n  "))
	}
	for k := range jsonHiddenFieldExemptions {
		if !slices.Contains(hidden, k) {
			t.Errorf("jsonHiddenFieldExemptions names %q, which is not a json:\"-\" field without a mapstructure tag; remove it", k)
		}
	}
}

// viperKeysReadOutsideConfig are viper keys that carry a default (or BindEnv)
// but deliberately have no Config field, because something reads them from
// viper directly. Each must say who reads it.
var viperKeysReadOutsideConfig = map[string]string{
	"audible_base_url":                "read after the literal in InitConfig to backfill MetadataSources[].BaseURL",
	"openlibrary_base_url":            "read after the literal in InitConfig to backfill MetadataSources[].BaseURL",
	"audnexus_base_url":               "read after the literal in InitConfig to backfill MetadataSources[].BaseURL",
	"google_books_base_url":           "read after the literal in InitConfig to backfill MetadataSources[].BaseURL",
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

	leaves, _ := configLeaves()
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

// resetZeroExemptions are keys whose shipped default is non-zero but whose
// value after ResetToDefaults is deliberately something else, with the reason.
var resetZeroExemptions = map[string]string{}

// TestResetToDefaultsRestoresEveryDefaultedKey is the reset-side twin of the
// tests above. ResetToDefaults used to be a second hand-written Config literal
// that omitted 131 leaves, and FactoryReset persisted the result, so the stored
// blob carried zero for every default it forgot. It now derives from the viper
// defaults; this test enumerates every key that has a non-zero default and
// requires its Config leaf to be non-zero after a reset, so a default that
// cannot reach the reset result fails by name.
func TestResetToDefaultsRestoresEveryDefaultedKey(t *testing.T) {
	restoreAppConfig(t)
	// The process-wide viper holds the registered defaults in a running server;
	// applyEnvAuthoritativeConfig re-applies the env-only invitation keys (e.g.
	// smtp_port) from it after the reset.
	viper.Reset()
	t.Cleanup(viper.Reset)
	InitConfig()
	// A running config with the boot-only sort indexes set, as in production:
	// the reset carries them over rather than restoring the default.
	Mutate(func(c *Config) { c.EnabledSortIndexes = []string{"year"} })

	ResetToDefaults()
	got := reflect.ValueOf(Snapshot())

	v := viper.New()
	registerConfigDefaults(noEnvRegistrar{v})
	leaves, _ := configLeaves()
	byKey := make(map[string]configLeaf, len(leaves))
	for _, l := range leaves {
		byKey[l.key] = l
	}

	checked := 0
	var zero []string
	for _, k := range v.AllKeys() {
		l, ok := byKey[k]
		if !ok {
			continue // under a map/list leaf, or read outside Config
		}
		if rv := reflect.ValueOf(v.Get(k)); !rv.IsValid() || rv.IsZero() ||
			((rv.Kind() == reflect.Slice || rv.Kind() == reflect.Map) && rv.Len() == 0) {
			continue // a zero default: a zero field is correct
		}
		if _, ok := resetZeroExemptions[k]; ok {
			continue
		}
		checked++
		if got.FieldByIndex(l.index).IsZero() {
			zero = append(zero, k)
		}
	}
	if checked < 100 {
		t.Fatalf("checked only %d defaulted keys; the enumeration is broken, not the config", checked)
	}
	sort.Strings(zero)
	if len(zero) > 0 {
		t.Errorf("%d key(s) have a non-zero default but are zero after ResetToDefaults:\n  %s",
			len(zero), strings.Join(zero, "\n  "))
	}
	for k := range resetZeroExemptions {
		if _, ok := byKey[k]; !ok {
			t.Errorf("resetZeroExemptions names %q, which is not a Config leaf; remove it", k)
		}
	}
}

// TestResetToDefaultsIgnoresEnvironment pins the choice that a reset restores
// the shipped defaults, not the process environment: a reset is "back to
// factory", and an env override that happened to be set on the host must not
// be baked into what the operator is told are defaults. (Settings the host
// owns through the environment are re-applied by applyEnvAuthoritativeConfig.)
func TestResetToDefaultsIgnoresEnvironment(t *testing.T) {
	restoreAppConfig(t)
	t.Setenv("FP_PARALLEL_WORKERS", "77")
	t.Setenv("SCHEDULED_ASIN_BACKFILL_INTERVAL", "999")
	ResetToDefaults()
	c := Snapshot()
	if c.FPParallelWorkers != 4 {
		t.Errorf("FPParallelWorkers = %d, want the default 4 (environment must not apply)", c.FPParallelWorkers)
	}
	if c.Scheduled.ASINBackfill.Interval != 360 {
		t.Errorf("asin_backfill interval = %d, want the default 360", c.Scheduled.ASINBackfill.Interval)
	}
}
