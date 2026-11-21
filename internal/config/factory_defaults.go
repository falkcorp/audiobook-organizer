// file: internal/config/factory_defaults.go
// version: 0.1.0
// guid: 4c8e2a19-7b35-4d60-9f1e-a2d7c5b8e043
// last-edited: 2026-11-21

package config

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"runtime"
	"slices"
	"strings"
)

// factoryDefaultsJSON is the Config a fresh install projects from the shipped
// settings defaults alone: books' appconfig Engine over core's and books'
// registered defaults, with no operator layer and no stored value. It is
// generated, never hand-edited: internal/books TestFactoryDefaults_Golden
// rebuilds it from the projection and fails when the two differ, so a default
// changed in a settings section reaches every test that resets to defaults
// (go test ./internal/books -run TestFactoryDefaults_Golden -update-factory-defaults).
// Until F3-07 the defaults came from registerConfigDefaults through viper;
// the projection replaced that reader, and this file replaced the second
// copy of the defaults it was.
//
//go:embed factory_defaults.json
var factoryDefaultsJSON []byte

// factoryComputed are the defaults the projection computes on the machine it
// runs on, so the golden file cannot hold them: the generator writes them
// zero and factoryDefaultConfigForTest computes each one as books' settings
// section does.
var factoryComputed = map[string]func(*Config){
	// books scan section: max(runtime.NumCPU(), 4).
	"ConcurrentScans": func(c *Config) { c.ConcurrentScans = max(runtime.NumCPU(), 4) },
}

// FactoryComputedFieldsForTest names the fields factoryComputed fills, for the
// generator, which writes them zero.
func FactoryComputedFieldsForTest() []string {
	return slices.Sorted(maps.Keys(factoryComputed))
}

// FactoryCarriedFields are the fields ResetToDefaultsForTest carries over from
// the running config instead of resetting; the generator writes them zero.
var FactoryCarriedFields = []string{"RootDir", "DatabasePath", "PlaylistDir", "EnabledSortIndexes"}

// factoryDefaultConfigForTest is the Config factoryDefaultsJSON holds, with
// the computed defaults filled in.
func factoryDefaultConfigForTest() Config {
	c, err := DecodeFactoryDefaultsForTest(factoryDefaultsJSON)
	if err != nil {
		panic("config: factory_defaults.json does not decode (regenerate it: go test ./internal/books -run TestFactoryDefaults_Golden -update-factory-defaults): " + err.Error())
	}
	for _, fill := range factoryComputed {
		fill(&c)
	}
	return c
}

// ResetToDefaultsForTest resets the AppConfig to factory defaults. It is a
// test seam: HEAD's gin system and factory reset routes, its production
// callers, are gone (R1-25), and the offline reset that replaces them never
// resets a running config. It replaces the store with the service stopped,
// so the next boot reads the shipped defaults from an empty settings store.
//
// The defaults are the shipped settings defaults only (factoryDefaultsJSON):
// not the operator file, and not the process environment. The library paths
// and the boot-only sort indexes are carried over from the running config.
//
// WHY Mutate: whole-struct reset; concurrent readers must not see a torn state.
func ResetToDefaultsForTest() {
	// Snapshot current paths before acquiring the write lock to avoid a
	// deadlock (Mutate is not re-entrant).
	cur := Snapshot()
	defaults := factoryDefaultConfigForTest()
	Mutate(func(c *Config) {
		*c = defaults

		// Core paths: preserve the existing ones; everything else resets.
		c.RootDir = cur.RootDir
		c.DatabasePath = cur.DatabasePath
		c.PlaylistDir = cur.PlaylistDir

		// Boot-only: the indexes the store built at open stay built until
		// restart, so GET must keep reporting them (pinBootOnlySettings).
		c.EnabledSortIndexes = slices.Clone(cur.EnabledSortIndexes)
	}) // end Mutate
}

// EncodeFactoryDefaultsForTest encodes c as factory_defaults.json holds it:
// every exported field by its Go field path ("Dedup.Signals.BandHighMin"), a
// struct field walked into, any other value as its JSON. Go names, not json
// tags: the environment-only fields carry json:"-" and still have defaults.
// Every function here is a test seam (the generator test and
// ResetToDefaultsForTest are its only callers), hence the ForTest names.
func EncodeFactoryDefaultsForTest(c Config) ([]byte, error) {
	out := map[string]json.RawMessage{}
	if err := encodeFieldsForTest(reflect.ValueOf(c), "", out); err != nil {
		return nil, err
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// DecodeFactoryDefaultsForTest is EncodeFactoryDefaultsForTest's inverse. A
// path that names no field of Config is an error: the file is stale.
func DecodeFactoryDefaultsForTest(b []byte) (Config, error) {
	var in map[string]json.RawMessage
	if err := json.Unmarshal(b, &in); err != nil {
		return Config{}, err
	}
	var c Config
	root := reflect.ValueOf(&c).Elem()
	for _, path := range slices.Sorted(maps.Keys(in)) {
		f := root
		for _, seg := range strings.Split(path, ".") {
			if f.Kind() != reflect.Struct {
				return Config{}, fmt.Errorf("%s: %s is not a struct", path, seg)
			}
			f = f.FieldByName(seg)
			if !f.IsValid() {
				return Config{}, fmt.Errorf("%s names no field of Config", path)
			}
		}
		if err := json.Unmarshal(in[path], f.Addr().Interface()); err != nil {
			return Config{}, fmt.Errorf("%s: %w", path, err)
		}
	}
	return c, nil
}

func encodeFieldsForTest(v reflect.Value, prefix string, out map[string]json.RawMessage) error {
	t := v.Type()
	for i := range t.NumField() {
		sf := t.Field(i)
		if !sf.IsExported() {
			continue
		}
		fv := v.Field(i)
		path := prefix + sf.Name
		if walkableForTest(sf.Type) {
			if err := encodeFieldsForTest(fv, path+".", out); err != nil {
				return err
			}
			continue
		}
		raw, err := json.Marshal(fv.Interface())
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		out[path] = raw
	}
	return nil
}

// walkableForTest reports whether a field of type t is walked into rather than
// encoded whole: a struct that does not marshal itself.
func walkableForTest(t reflect.Type) bool {
	if t.Kind() != reflect.Struct {
		return false
	}
	m := reflect.TypeFor[json.Marshaler]()
	return !t.Implements(m) && !reflect.PointerTo(t).Implements(m)
}
