// file: internal/books/factory_defaults_test.go
// version: 0.1.0
// guid: 8d3f6b21-0a94-4e57-b2c8-61e7a9d4f05c
// last-edited: 2026-11-21

package books

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
)

var updateFactoryDefaults = flag.Bool("update-factory-defaults", false, "rewrite internal/config/factory_defaults.json from the projection")

// factoryDefaultsFile is the golden file internal/config embeds.
const factoryDefaultsFile = "../config/factory_defaults.json"

// TestFactoryDefaults_Golden pins internal/config/factory_defaults.json, the
// defaults config.ResetToDefaultsForTest restores, to what the books Engine
// projects from the shipped settings defaults alone (no operator layer, no
// stored value). The machine's paths never reach it: the state directory and
// HOME are fixed fake paths, and the fields the reset carries over (the
// library paths, the boot-only sort indexes) and the computed defaults
// (config.FactoryComputedFieldsForTest) are written zero.
func TestFactoryDefaults_Golden(t *testing.T) {
	const fakeHome, fakeState = "/home/batman-factory-defaults", "/var/lib/batcave-factory-defaults/state"
	t.Setenv("HOME", fakeHome)
	t.Setenv(config.SecureStateDirEnv, fakeState)
	cfg, err := OperatorAppConfig(t.Context(), Module(), BootInputs{})
	if err != nil {
		t.Fatal(err)
	}
	v := reflect.ValueOf(&cfg).Elem()
	for _, name := range append(config.FactoryComputedFieldsForTest(), config.FactoryCarriedFields...) {
		f := v.FieldByName(name)
		if !f.IsValid() {
			t.Fatalf("%s names no field of config.Config", name)
		}
		f.Set(reflect.Zero(f.Type()))
	}
	got, err := config.EncodeFactoryDefaultsForTest(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{fakeHome, fakeState} {
		if bytes.Contains(got, []byte(p)) {
			t.Fatalf("a default derives from a machine path (%s); add its field to config.FactoryCarriedFields or compute it", p)
		}
	}
	back, err := config.DecodeFactoryDefaultsForTest(got)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(back, cfg) {
		t.Fatal("factory defaults do not round-trip through EncodeFactoryDefaultsForTest/DecodeFactoryDefaultsForTest: a field's JSON drops part of its value")
	}
	if *updateFactoryDefaults {
		if err := os.WriteFile(filepath.FromSlash(factoryDefaultsFile), got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(filepath.FromSlash(factoryDefaultsFile))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("internal/config/factory_defaults.json is stale against the projection; regenerate it:\n"+
			"  go test ./internal/books -run TestFactoryDefaults_Golden -update-factory-defaults\nfirst differing line: %s", firstDiff(string(want), string(got)))
	}
}

func firstDiff(a, b string) string {
	al, bl := strings.Split(a, "\n"), strings.Split(b, "\n")
	for i := range min(len(al), len(bl)) {
		if al[i] != bl[i] {
			return "file " + strings.TrimSpace(al[i]) + " | projection " + strings.TrimSpace(bl[i])
		}
	}
	return "length differs"
}
