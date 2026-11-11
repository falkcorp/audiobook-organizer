// file: internal/books/cutover/head_bind_test.go
// version: 1.0.0
// guid: e33ae35e-b405-4f99-aeba-3cf8ac6c0168
// last-edited: 2026-11-11

package cutover

import (
	"encoding/json"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// recordedBindCount is the number of BindEnv calls the recorder saw at HEAD:
// 130 in config.go registerConfigDefaults, 9 in abs_config.go
// registerABSDefaults and 16 in invite_config.go registerInviteDefaults.
const (
	recordedConfigBinds = 130
	recordedABSBinds    = 9
	recordedInviteBinds = 16
	recordedBindCount   = recordedConfigBinds + recordedABSBinds + recordedInviteBinds
)

func TestHeadBindingsCount(t *testing.T) {
	if len(headBindings) != recordedBindCount {
		t.Fatalf("len = %d, recorded %d", len(headBindings), recordedBindCount)
	}
	if recordedBindCount != 155 {
		t.Fatalf("recorded total = %d", recordedBindCount)
	}
}

func TestHeadBindingsNamesWellFormedAndUnique(t *testing.T) {
	fields := map[string]bool{}
	envs := map[string]string{}
	for i, b := range headBindings {
		if b.Field == "" {
			t.Fatalf("binding %d has an empty Field", i)
		}
		if fields[b.Field] {
			t.Errorf("Field %q bound twice", b.Field)
		}
		fields[b.Field] = true
		if len(b.Env) == 0 {
			t.Errorf("%q has no env name", b.Field)
		}
		for _, e := range b.Env {
			if e == "" {
				t.Errorf("%q has an empty env name", b.Field)
			}
			if prev, dup := envs[e]; dup {
				t.Errorf("env %q bound by both %q and %q", e, prev, b.Field)
			}
			envs[e] = b.Field
		}
	}
}

func tagNames(f reflect.StructField) []string {
	var out []string
	for _, tag := range []string{"json", "mapstructure"} {
		if v, ok := f.Tag.Lookup(tag); ok {
			if name, _, _ := strings.Cut(v, ","); name != "" && name != "-" {
				out = append(out, name)
			}
		}
	}
	return out
}

// resolveKey walks a dotted viper key through nested struct tags.
func resolveKey(t reflect.Type, key string) bool {
	head, rest, more := strings.Cut(key, ".")
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return false
	}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		for _, n := range tagNames(f) {
			if n != head {
				continue
			}
			if !more {
				return true
			}
			if resolveKey(f.Type, rest) {
				return true
			}
		}
	}
	return false
}

// providerBaseURLBindings are the four bound keys that are not headConfig
// tags: HEAD binds <provider>_base_url so the environment can backfill
// MetadataSource.BaseURL of the source with that id (configFromViper), not a
// top-level Config field. They are listed, not hidden: anything else
// unresolved fails the test.
var providerBaseURLBindings = []string{"audible_base_url", "openlibrary_base_url", "audnexus_base_url", "google_books_base_url"}

func TestHeadBindingsFieldsAreHeadConfigTags(t *testing.T) {
	var unresolved []string
	for _, b := range headBindings {
		if !resolveKey(reflect.TypeOf(headConfig{}), b.Field) {
			unresolved = append(unresolved, b.Field)
		}
	}
	if !slices.Equal(unresolved, providerBaseURLBindings) {
		t.Fatalf("bound Field(s) that are not a json or mapstructure tag of headConfig: %v, want exactly %v", unresolved, providerBaseURLBindings)
	}
}

func TestResolveKeyNested(t *testing.T) {
	typ := reflect.TypeOf(headConfig{})
	for _, k := range []string{"root_dir", "embedding.base_url", "metadata_fetch.negative_ttl_days.google", "scheduled.library_scan.enabled"} {
		if !resolveKey(typ, k) {
			t.Errorf("%q should resolve", k)
		}
	}
	for _, k := range []string{"nope", "embedding.nope", "root_dir.x"} {
		if resolveKey(typ, k) {
			t.Errorf("%q should not resolve", k)
		}
	}
}

func TestHeadBindingsKnownEntries(t *testing.T) {
	by := map[string][]string{}
	for _, b := range headBindings {
		by[b.Field] = b.Env
	}
	if got := by["abs_api_enabled"]; len(got) != 1 || got[0] != "ABS_API_ENABLED" {
		t.Fatalf("abs_api_enabled = %v", got)
	}
	if got := by["smtp_port"]; len(got) != 1 || got[0] != "SMTP_PORT" {
		t.Fatalf("smtp_port = %v", got)
	}
	multi := 0
	for _, b := range headBindings {
		if len(b.Env) > 1 {
			multi++
		}
	}
	if multi != 1 {
		t.Fatalf("%d bindings carry more than one env name, recorded 1 (the LIST_WARMER alias)", multi)
	}
}

func TestHeadEnvConstants(t *testing.T) {
	if headStateDirEnv != "ABK_STATE_DIR" || headAllowKeyRegenEnv != "ABK_ALLOW_ENCRYPTION_KEY_REGEN" {
		t.Fatal("env constants changed")
	}
}

func TestHeadDefaultsFile(t *testing.T) {
	data, err := os.ReadFile("testdata/head_defaults.json")
	if err != nil {
		t.Fatal(err)
	}
	var defaults map[string]json.RawMessage
	if err := json.Unmarshal(data, &defaults); err != nil {
		t.Fatalf("head_defaults.json: %v", err)
	}
	delete(defaults, "_header") // the file's version header, not a default
	if len(defaults) != 272 {
		t.Fatalf("defaults = %d keys, recorded 272", len(defaults))
	}
	if string(defaults["database_type"]) != `"pebble"` || string(defaults["smtp_port"]) != "587" {
		t.Fatalf("spot check: database_type=%s smtp_port=%s", defaults["database_type"], defaults["smtp_port"])
	}
	// Eight bound keys have no SetDefault at HEAD (bound for the environment
	// only); every other bound key has a recorded default.
	var noDefault []string
	for _, b := range headBindings {
		if _, ok := defaults[b.Field]; !ok {
			noDefault = append(noDefault, b.Field)
		}
	}
	want := []string{"deluge_web_url", "deluge_web_password", "deluge_discovery_label", "deluge_discovery_enabled", "deluge_move_enabled", "whisper_requires", "bootstrap_key_ttl", "bootstrap_key_ttl_days"}
	if !slices.Equal(noDefault, want) {
		t.Fatalf("bound keys without a default = %v, want %v", noDefault, want)
	}
}
