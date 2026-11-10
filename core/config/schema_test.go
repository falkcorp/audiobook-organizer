// file: core/config/schema_test.go
// version: 1.0.0
// guid: 98e8ff97-c46c-401e-9ff4-256bdd4ca770
// last-edited: 2026-11-10

package config_test

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/config/configtest"
	"github.com/falkcorp/mediacore/internal/jsonschemax"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/secrets"
)

var update = flag.Bool("update", false, "rewrite the golden files under testdata/")

// golden compares got with the body of testdata/name, or rewrites the body
// under -update. The file opens with a "# " header block (file, version,
// guid, last-edited: JSON holds no comments), which the comparison skips and
// -update keeps.
func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	old, err := os.ReadFile(path)
	if err != nil && !*update {
		t.Fatalf("%v (run with -update to create it)", err)
	}
	header, want := splitHeader(old)
	if *update {
		if err := os.WriteFile(path, append(header, got...), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s differs (run with -update after checking the change):\n--- got\n%s\n--- want\n%s", path, got, want)
	}
}

// splitHeader splits b's leading "#" lines from the rest.
func splitHeader(b []byte) (header, body []byte) {
	rest := b
	for len(rest) > 0 && rest[0] == '#' {
		i := bytes.IndexByte(rest, '\n')
		if i < 0 {
			return b, nil
		}
		rest = rest[i+1:]
	}
	return b[:len(b)-len(rest)], rest
}

// TestSchema_Golden (05-config §1.3.4): the settings document over the
// fixture sections, byte for byte: dialect, $id, one property per section
// path, vendor keywords for reload and protection, secrets write-only.
func TestSchema_Golden(t *testing.T) {
	r := started(t, rigOpts{modules: map[string]configtest.Module{"batman": {Enabled: true}}})
	doc, err := r.st.Schema(nil)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, doc, "", "  "); err != nil {
		t.Fatal(err)
	}
	buf.WriteByte('\n')
	golden(t, "schema.golden", buf.Bytes())

	only, err := r.st.Schema(func(sec config.Path) bool { return sec == configtest.WatchPath })
	if err != nil {
		t.Fatal(err)
	}
	var d struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(only, &d); err != nil || len(d.Properties) != 1 || d.Properties[string(configtest.WatchPath)] == nil {
		t.Errorf("include filter: %s (%v)", only, err)
	}
}

// durationAndRef holds the two types config contributes to the reflector.
type durationAndRef struct {
	Every config.Duration `json:"every"`
	Key   secrets.Ref     `json:"key"`
}

// TestSchema_SameOverridesAsOpenAPI (05-config §1.10.2): config renders its
// types through the one table httpapi composes,
// jsonschemax.Base().With(config.Overrides()), so the settings document and
// the OpenAPI document cannot disagree on config.Duration or secrets.Ref.
func TestSchema_SameOverridesAsOpenAPI(t *testing.T) {
	ours, err := config.SchemaFor[durationAndRef]()
	if err != nil {
		t.Fatal(err)
	}
	theirs, err := jsonschemax.For[durationAndRef](jsonschemax.Base().With(config.Overrides()))
	if err != nil {
		t.Fatal(err)
	}
	a := ours.JSON()
	b, err := json.Marshal(theirs)
	if err != nil {
		t.Fatal(err)
	}
	var pa, pb struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(a, &pa); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &pb); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"every", "key"} {
		if string(pa.Properties[f]) != string(pb.Properties[f]) || pa.Properties[f] == nil {
			t.Errorf("%s: config %s, composed table %s", f, pa.Properties[f], pb.Properties[f])
		}
	}
	// A fresh table on every call: a caller's edit never leaks.
	o := config.Overrides()
	o[reflect.TypeFor[config.Duration]()].Format = "joker"
	delete(o, reflect.TypeFor[secrets.Ref]())
	again := config.Overrides()
	if again[reflect.TypeFor[config.Duration]()].Format != "duration" || again[reflect.TypeFor[secrets.Ref]()] == nil {
		t.Error("Overrides returned a shared table")
	}
}

// TestSchema_OverrideSetsDisjoint (05-config §1.10.2): config's entries
// share no type with jsonschemax.Base or with httpapi's own (rbac.Permission,
// json.RawMessage), so the composition never hits With's duplicate panic.
func TestSchema_OverrideSetsDisjoint(t *testing.T) {
	ours := config.Overrides()
	for _, other := range []reflect.Type{reflect.TypeFor[time.Time](), reflect.TypeFor[rbac.Permission](), reflect.TypeFor[json.RawMessage]()} {
		if _, dup := ours[other]; dup {
			t.Errorf("config.Overrides holds %v, which another owner contributes", other)
		}
	}
	for t2 := range jsonschemax.Base() {
		if _, dup := ours[t2]; dup {
			t.Errorf("config.Overrides duplicates jsonschemax.Base's %v", t2)
		}
	}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("composing the tables panicked: %v", r)
		}
	}()
	jsonschemax.Base().With(config.Overrides()).With(jsonschemax.Overrides{
		reflect.TypeFor[rbac.Permission](): {Type: "string"},
		reflect.TypeFor[json.RawMessage](): {},
	})
}

// TestSchema_SecretIsWriteOnly: a secrets.Ref key is a write-only string
// marked x-mediacore-secret in the document and in KeyState.Schema, with no
// default that could carry a value.
func TestSchema_SecretIsWriteOnly(t *testing.T) {
	r := started(t, rigOpts{})
	ks, ok := r.st.Lookup(kAPIKey)
	if !ok {
		t.Fatal("no api_key")
	}
	doc, err := r.st.Schema(nil)
	if err != nil {
		t.Fatal(err)
	}
	var d struct {
		Properties map[string]struct {
			Properties map[string]json.RawMessage `json:"properties"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(doc, &d); err != nil {
		t.Fatal(err)
	}
	for name, raw := range map[string]json.RawMessage{"KeyState.Schema": ks.Schema, "document": d.Properties[string(configtest.BatcavePath)].Properties["api_key"]} {
		var s jsonschema.Schema
		if err := json.Unmarshal(raw, &s); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if s.Type != "string" || !s.WriteOnly || s.Extra["x-mediacore-secret"] != true || s.Default != nil {
			t.Errorf("%s = %s, want a write-only string marked x-mediacore-secret with no default", name, raw)
		}
	}
}
