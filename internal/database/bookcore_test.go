// file: internal/database/bookcore_test.go
// version: 1.0.0
// guid: b2d9a610-4f37-4a8e-9c15-bookcoretest01
// last-edited: 2026-07-05

package database

import (
	"reflect"
	"testing"
	"time"
)

// heavyFields are the nine Book fields that BookCore intentionally omits — the
// exact set that stripBookForMemdb clears (see memdb_strip.go). This is the
// authoritative partition; the tests below lock BookCore to it.
var heavyFields = map[string]struct{}{
	"Description":        {},
	"VersionNotes":       {},
	"BookSigV1":          {},
	"BookSigV1Mask":      {},
	"BookSigSegments":    {},
	"BookSigBuiltAt":     {},
	"BookSigCoveragePct": {},
	"Author":             {},
	"Series":             {},
}

func structFieldNames(t reflect.Type) map[string]struct{} {
	names := make(map[string]struct{}, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		names[t.Field(i).Name] = struct{}{}
	}
	return names
}

// TestBookCoreIsBookMinusHeavyFields locks the field partition: BookCore must be
// Book minus exactly the nine heavy fields, and it must carry every shared
// field's struct tag over verbatim.
func TestBookCoreIsBookMinusHeavyFields(t *testing.T) {
	bookT := reflect.TypeOf(Book{})
	coreT := reflect.TypeOf(BookCore{})

	bookNames := structFieldNames(bookT)
	coreNames := structFieldNames(coreT)

	// (1) BookCore must not introduce any field that Book lacks.
	for name := range coreNames {
		if _, ok := bookNames[name]; !ok {
			t.Errorf("BookCore has field %q not present on Book", name)
		}
	}

	// (2) The Book fields absent from BookCore must be EXACTLY the 9 heavy ones.
	missing := map[string]struct{}{}
	for name := range bookNames {
		if _, ok := coreNames[name]; !ok {
			missing[name] = struct{}{}
		}
	}
	if !reflect.DeepEqual(missing, heavyFields) {
		t.Errorf("Book\\BookCore field-name diff = %v, want exactly the 9 heavy fields %v", keys(missing), keys(heavyFields))
	}

	// (3) Every field shared by both structs must carry the identical struct tag
	// (json + db) verbatim, so the projection can never quietly diverge.
	for name := range coreNames {
		bookField, ok := bookT.FieldByName(name)
		if !ok {
			continue // already reported by check (1)
		}
		coreField, _ := coreT.FieldByName(name)
		if bookField.Tag != coreField.Tag {
			t.Errorf("field %q struct tag mismatch:\n  Book:     %q\n  BookCore: %q", name, bookField.Tag, coreField.Tag)
		}
	}
}

// TestBookCoreCopiesAllFields populates every Book field with a non-zero value,
// projects to BookCore via Core(), and asserts every BookCore field was copied.
// This catches a Core() that forgets a field (a silent-drop bug): the dropped
// field would stay zero while its Book counterpart is non-zero.
//
// Iterating reflectively means new fields are covered automatically. Crucially,
// the test first asserts the Book source field is itself non-zero — otherwise a
// setNonZero gap would let a zero==zero comparison pass a genuine drop.
func TestBookCoreCopiesAllFields(t *testing.T) {
	var b Book
	setNonZero(reflect.ValueOf(&b).Elem())

	core := b.Core()
	coreV := reflect.ValueOf(core)
	coreT := coreV.Type()
	bookV := reflect.ValueOf(b)

	for i := 0; i < coreT.NumField(); i++ {
		name := coreT.Field(i).Name

		src := bookV.FieldByName(name)
		if !src.IsValid() {
			t.Errorf("BookCore field %q has no matching Book field", name)
			continue
		}
		// Guard the guard: the source must be non-zero, or the copy assertion
		// below is vacuous (zero == zero would pass a real drop).
		if src.IsZero() {
			t.Errorf("test setup: Book.%s was not populated non-zero by setNonZero; the copy check for it would be vacuous", name)
			continue
		}
		got := coreV.Field(i)
		if !reflect.DeepEqual(got.Interface(), src.Interface()) {
			t.Errorf("Core() dropped or miscopied field %q:\n  Book:     %#v\n  BookCore: %#v", name, src.Interface(), got.Interface())
		}
	}
}

// setNonZero recursively fills v with non-zero values so that every leaf field
// is distinguishable from its zero value. Handles pointers, structs (with
// time.Time special-cased so its unexported fields aren't touched), slices,
// maps, interfaces, and scalars.
func setNonZero(v reflect.Value) {
	switch v.Kind() {
	case reflect.Ptr:
		v.Set(reflect.New(v.Type().Elem()))
		setNonZero(v.Elem())
	case reflect.Struct:
		if v.Type() == reflect.TypeOf(time.Time{}) {
			v.Set(reflect.ValueOf(time.Unix(1_730_000_000, 0).UTC()))
			return
		}
		for i := 0; i < v.NumField(); i++ {
			f := v.Field(i)
			if f.CanSet() {
				setNonZero(f)
			}
		}
	case reflect.Slice:
		s := reflect.MakeSlice(v.Type(), 1, 1)
		setNonZero(s.Index(0))
		v.Set(s)
	case reflect.Map:
		m := reflect.MakeMap(v.Type())
		k := reflect.New(v.Type().Key()).Elem()
		setNonZero(k)
		val := reflect.New(v.Type().Elem()).Elem()
		setNonZero(val)
		m.SetMapIndex(k, val)
		v.Set(m)
	case reflect.Interface:
		v.Set(reflect.ValueOf("nonzero"))
	case reflect.String:
		v.SetString("nonzero")
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(1)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v.SetUint(1)
	case reflect.Float32, reflect.Float64:
		v.SetFloat(1.5)
	}
}

func keys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
