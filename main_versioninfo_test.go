// file: main_versioninfo_test.go
// version: 0.1.0
// guid: 7d2b9e41-5c68-4f03-a1e7-3b94d60c8a25
// last-edited: 2026-11-20

package main

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/updater"
)

// jsonFieldNames lists the json tag names of v's struct fields in order, so a
// field with no tag, a "-" tag or an omitempty tag is visible to the caller.
func jsonFieldNames(t *testing.T, v any) []string {
	t.Helper()
	typ := reflect.TypeOf(v)
	names := make([]string, 0, typ.NumField())
	for field := range typ.Fields() {
		tag, ok := field.Tag.Lookup("json")
		if !ok {
			t.Fatalf("field %s has no json tag", field.Name)
		}
		name, opts, _ := strings.Cut(tag, ",")
		if name == "" || name == "-" || opts != "" {
			t.Fatalf("field %s json tag %q: want a plain name (every field is always present)", field.Name, tag)
		}
		names = append(names, name)
	}
	return names
}

// TestVersionInfoFieldsMatchUpdaterSelfTest pins that the names --version-json
// emits are exactly the set the updater's self-test (core/updater) requires,
// in the same order.
func TestVersionInfoFieldsMatchUpdaterSelfTest(t *testing.T) {
	got := jsonFieldNames(t, versionInfo{})
	want := updater.VersionFields()
	if !slices.Equal(got, want) {
		t.Fatalf("versionInfo json fields = %v, updater self-test expects %v", got, want)
	}
}

// TestVersionFieldsAccessorReturnsCopy pins that mutating the accessor's slice
// cannot change what the next caller sees.
func TestVersionFieldsAccessorReturnsCopy(t *testing.T) {
	first := updater.VersionFields()
	first[0] = "mutated"
	if updater.VersionFields()[0] == "mutated" {
		t.Fatal("VersionFields returned shared storage")
	}
}
