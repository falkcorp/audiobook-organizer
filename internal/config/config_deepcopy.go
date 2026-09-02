// file: internal/config/config_deepcopy.go
// version: 1.0.0
// guid: 3f0c2b7e-9d41-4c6a-8e5b-2a7f1d9c4b60
// last-edited: 2026-09-02

package config

import (
	"reflect"
	"time"
)

// Clone returns a deep copy of c: every map, slice, pointer and interface
// value reachable from an exported field is duplicated, so mutating the copy
// (or unmarshalling JSON into it) can never touch c.
//
// WHY this exists (PR #3052 follow-up, D1): UpdateService.UpdateConfig used
// to take `prior = *c` and json.Unmarshal straight into the live config. Both
// are SHALLOW for Go maps and slices — `prior` and the live struct shared the
// same Dedup.Signals.Confidence map, and Unmarshal into the live struct wrote
// an unknown-kind key INTO that shared map before validation could reject it.
// So a rejected PUT (400) still left the typo'd key in memory, every later
// PUT then failed validation on it, and the unguarded SaveConfigToDatabase
// callers could persist the poisoned map. Rolling back with `*c = prior` was
// equally inert for map contents. The fix is to unmarshal into a deep copy,
// validate THAT, and only then assign it over the live config; rollbacks
// restore a deep copy too.
//
// WHY reflection and not a JSON round-trip: Config carries `json:"-"` fields
// (e.g. ABSJWTSecret) that a Marshal/Unmarshal clone would silently zero.
//
// Unexported fields cannot be set through reflection; they are copied by
// plain struct assignment (shallow). TestConfigClone_EveryReferenceFieldIsExported
// walks the Config type and fails if an unexported map/slice/pointer/interface
// field ever appears, so the shallow fallback can never quietly widen.
func (c *Config) Clone() *Config {
	if c == nil {
		return nil
	}
	dst := new(Config)
	deepCopyValue(reflect.ValueOf(dst).Elem(), reflect.ValueOf(c).Elem())
	return dst
}

var timeType = reflect.TypeOf(time.Time{})

// deepCopyValue copies src into dst (which must be settable and of the same
// type), allocating fresh backing storage for every map, slice, pointer and
// interface it reaches.
func deepCopyValue(dst, src reflect.Value) {
	switch src.Kind() {
	case reflect.Pointer:
		if src.IsNil() {
			dst.Set(reflect.Zero(src.Type()))
			return
		}
		dst.Set(reflect.New(src.Type().Elem()))
		deepCopyValue(dst.Elem(), src.Elem())
	case reflect.Interface:
		if src.IsNil() {
			dst.Set(reflect.Zero(src.Type()))
			return
		}
		inner := reflect.New(src.Elem().Type()).Elem()
		deepCopyValue(inner, src.Elem())
		dst.Set(inner)
	case reflect.Map:
		if src.IsNil() {
			dst.Set(reflect.Zero(src.Type()))
			return
		}
		dst.Set(reflect.MakeMapWithSize(src.Type(), src.Len()))
		iter := src.MapRange()
		for iter.Next() {
			k := reflect.New(src.Type().Key()).Elem()
			deepCopyValue(k, iter.Key())
			v := reflect.New(src.Type().Elem()).Elem()
			deepCopyValue(v, iter.Value())
			dst.SetMapIndex(k, v)
		}
	case reflect.Slice:
		if src.IsNil() {
			dst.Set(reflect.Zero(src.Type()))
			return
		}
		dst.Set(reflect.MakeSlice(src.Type(), src.Len(), src.Len()))
		for i := 0; i < src.Len(); i++ {
			deepCopyValue(dst.Index(i), src.Index(i))
		}
	case reflect.Array:
		for i := 0; i < src.Len(); i++ {
			deepCopyValue(dst.Index(i), src.Index(i))
		}
	case reflect.Struct:
		// Shallow-assign first so unexported fields (which reflection cannot
		// set) and value-only structs such as time.Time come across, then
		// overwrite every exported field with a deep copy.
		dst.Set(src)
		if src.Type() == timeType {
			return
		}
		for i := 0; i < src.NumField(); i++ {
			f := dst.Field(i)
			if !f.CanSet() {
				continue
			}
			deepCopyValue(f, src.Field(i))
		}
	default:
		dst.Set(src)
	}
}
