// file: internal/books/cutover/head_config_test.go
// version: 1.0.0
// guid: 8e6da0f2-704f-4b05-94cc-d806ec66ea26
// last-edited: 2026-11-11

package cutover

import (
	"reflect"
	"testing"
)

func TestHeadConfigShape(t *testing.T) {
	typ := reflect.TypeOf(headConfig{})
	if typ.NumField() < 150 {
		t.Fatalf("headConfig has %d fields, want at least 150", typ.NumField())
	}
	if typ.NumField() != 173 {
		t.Fatalf("headConfig has %d fields, HEAD has 173", typ.NumField())
	}
	dash := 0
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		js, ok := f.Tag.Lookup("json")
		if !ok {
			t.Errorf("field %s has no json tag", f.Name)
			continue
		}
		if js == "-" {
			dash++
			if ms, ok := f.Tag.Lookup("mapstructure"); !ok || ms == "" {
				t.Errorf("json:\"-\" field %s has no mapstructure tag", f.Name)
			}
		}
	}
	if dash != 19 {
		t.Fatalf("%d json:\"-\" fields, HEAD has 19", dash)
	}
}

func TestHeadConfigJSONTagsUnique(t *testing.T) {
	typ := reflect.TypeOf(headConfig{})
	seen := map[string]string{}
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		js := f.Tag.Get("json")
		if js == "-" {
			continue
		}
		name := js
		for j := 0; j < len(js); j++ {
			if js[j] == ',' {
				name = js[:j]
				break
			}
		}
		if prev, dup := seen[name]; dup {
			t.Errorf("json name %q on both %s and %s", name, prev, f.Name)
		}
		seen[name] = f.Name
	}
}
