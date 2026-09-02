// file: internal/config/config_deepcopy_test.go
// version: 1.0.0
// guid: 8e4b1c05-3a97-42d6-9f18-7c206ba4e35d
// last-edited: 2026-09-02

package config

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/dedup/unified"
)

// TestConfigClone_MapsAndSlicesAreIndependent is the unit-level statement of
// D1: mutating a clone (or the original) must not be visible through the
// other. A plain `copy := *c` passes nothing here.
func TestConfigClone_MapsAndSlicesAreIndependent(t *testing.T) {
	orig := &Config{
		RootDir:  "/lib",
		LogLevel: "info",
	}
	orig.Dedup.Signals.Confidence = map[string]DedupKindConfidence{
		string(unified.SigEmbedMedium): {MinConfidence: 0.7, MaxConfidence: 0.8},
	}
	orig.MetadataSources = []MetadataSource{{ID: "openlibrary", Name: "OpenLibrary", Enabled: true,
		Credentials: map[string]string{"api_key": "real-key"}}}

	clone := orig.Clone()
	if !reflect.DeepEqual(*orig, *clone) {
		t.Fatalf("clone is not equal to the original")
	}

	clone.Dedup.Signals.Confidence["embeding_medium"] = DedupKindConfidence{MinConfidence: 0.1}
	clone.Dedup.Signals.Confidence[string(unified.SigEmbedMedium)] = DedupKindConfidence{MinConfidence: 0.99}
	clone.MetadataSources[0].Enabled = false
	clone.MetadataSources[0].Credentials["api_key"] = "clobbered"
	clone.RootDir = "/other"

	if _, leaked := orig.Dedup.Signals.Confidence["embeding_medium"]; leaked {
		t.Errorf("writing a NEW key to the clone's map reached the original: %+v", orig.Dedup.Signals.Confidence)
	}
	if got := orig.Dedup.Signals.Confidence[string(unified.SigEmbedMedium)].MinConfidence; got != 0.7 {
		t.Errorf("overwriting an EXISTING key in the clone reached the original: min_confidence = %v, want 0.7", got)
	}
	if !orig.MetadataSources[0].Enabled {
		t.Errorf("mutating the clone's slice element reached the original")
	}
	if got := orig.MetadataSources[0].Credentials["api_key"]; got != "real-key" {
		t.Errorf("mutating a map INSIDE a cloned slice element reached the original: %q", got)
	}
	if orig.RootDir != "/lib" {
		t.Errorf("scalar write leaked: RootDir = %q", orig.RootDir)
	}

	// And the reverse direction: the original must not be able to reach into
	// the clone either.
	orig.Dedup.Signals.Confidence["only_in_orig"] = DedupKindConfidence{}
	if _, leaked := clone.Dedup.Signals.Confidence["only_in_orig"]; leaked {
		t.Errorf("writing to the original's map reached the clone")
	}
}

// TestConfigClone_PreservesJSONDashFields: Clone must not be reimplemented as
// a Marshal/Unmarshal round-trip — Config carries `json:"-"` fields that such
// a clone silently zeroes, which for ABSJWTSecret would log every ABS user out
// on the next config PUT.
func TestConfigClone_PreservesJSONDashFields(t *testing.T) {
	orig := &Config{ABSJWTSecret: "s3cret-signing-key"}
	if b, err := json.Marshal(orig); err == nil {
		var viaJSON Config
		_ = json.Unmarshal(b, &viaJSON)
		if viaJSON.ABSJWTSecret != "" {
			t.Skip("ABSJWTSecret is no longer json:\"-\"; this guard needs a new field")
		}
	}
	if got := orig.Clone().ABSJWTSecret; got != "s3cret-signing-key" {
		t.Fatalf("Clone dropped a json:\"-\" field: ABSJWTSecret = %q", got)
	}
}

// TestConfigClone_EveryReferenceFieldIsExported guards the one hole in the
// reflect-based clone: reflection cannot set unexported fields, so they are
// copied by plain struct assignment — shallow. That is harmless for the value
// types Config actually holds unexported (none today, plus time.Time inside
// nested structs), and wrong for a map, slice, pointer or channel. If someone
// adds an unexported reference-typed field anywhere under Config, this fails
// with its path instead of the clone quietly sharing state again.
func TestConfigClone_EveryReferenceFieldIsExported(t *testing.T) {
	seen := map[reflect.Type]bool{}
	var walk func(t reflect.Type, path string)
	walk = func(rt reflect.Type, path string) {
		for rt.Kind() == reflect.Pointer || rt.Kind() == reflect.Slice || rt.Kind() == reflect.Array {
			rt = rt.Elem()
		}
		if rt.Kind() == reflect.Map {
			walk(rt.Elem(), path+"[]")
			return
		}
		if rt.Kind() != reflect.Struct || seen[rt] || rt == reflect.TypeOf(time.Time{}) {
			return
		}
		seen[rt] = true
		for i := range rt.NumField() {
			f := rt.Field(i)
			fp := path + "." + f.Name
			if !f.IsExported() {
				switch f.Type.Kind() {
				case reflect.Map, reflect.Slice, reflect.Pointer, reflect.Chan, reflect.Interface, reflect.Func:
					t.Errorf("%s is an UNEXPORTED %s: Config.Clone copies it shallowly, so the clone would share it with the live config (that sharing is exactly the D1 bug). Export it, or teach Clone about it.", fp, f.Type.Kind())
				}
				continue
			}
			walk(f.Type, fp)
		}
	}
	walk(reflect.TypeOf(Config{}), "Config")
}
