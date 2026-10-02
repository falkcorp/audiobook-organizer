// file: internal/database/bookfilecore_asbookfile_test.go
// version: 1.0.0
// guid: 4e8a1d2c-6b3f-4a9e-8c71-5d2f0e9b3a64
// last-edited: 2026-10-02

package database

import (
	"reflect"
	"testing"
	"time"
)

// TestAsBookFile_RoundTripsEveryCoreField fills every BookFileCore field with a
// non-zero value and asserts Core(AsBookFile(c)) == c. A field added to
// BookFileCore and Core() but forgotten in AsBookFile fails here.
func TestAsBookFile_RoundTripsEveryCoreField(t *testing.T) {
	var c BookFileCore
	v := reflect.ValueOf(&c).Elem()
	now := time.Date(2026, 10, 2, 1, 2, 3, 0, time.UTC)
	for i := 0; i < v.NumField(); i++ {
		fillCoreNonZero(t, v.Field(i), v.Type().Field(i).Name, now)
	}
	bf := c.AsBookFile()
	got := bf.Core()
	if !reflect.DeepEqual(got, c) {
		t.Fatalf("AsBookFile lost a field:\n got %+v\nwant %+v", got, c)
	}
}

func fillCoreNonZero(t *testing.T, f reflect.Value, name string, now time.Time) {
	t.Helper()
	switch f.Kind() {
	case reflect.String:
		f.SetString("x-" + name)
	case reflect.Int, reflect.Int64, reflect.Int32:
		f.SetInt(7)
	case reflect.Float64, reflect.Float32:
		f.SetFloat(0.5)
	case reflect.Bool:
		f.SetBool(true)
	case reflect.Map:
		f.Set(reflect.MakeMap(f.Type()))
		f.SetMapIndex(reflect.ValueOf("k"), reflect.ValueOf("v"))
	case reflect.Pointer:
		p := reflect.New(f.Type().Elem())
		fillCoreNonZero(t, p.Elem(), name, now)
		f.Set(p)
	case reflect.Struct:
		if f.Type() == reflect.TypeOf(now) {
			f.Set(reflect.ValueOf(now))
			return
		}
		for i := 0; i < f.NumField(); i++ {
			if f.Field(i).CanSet() {
				fillCoreNonZero(t, f.Field(i), name+"."+f.Type().Field(i).Name, now)
			}
		}
	default:
		t.Fatalf("field %s: unhandled kind %s", name, f.Kind())
	}
}
