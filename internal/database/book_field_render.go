// file: internal/database/book_field_render.go
// version: 1.0.0
// guid: 7c1e4b2a-93d5-4f60-8a1b-5e2d9c0f7a34
// last-edited: 2026-09-13

package database

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"
)

// Book fields are addressed here by their JSON name, the name the store
// persists them under. These helpers are the shared vocabulary of the metadata
// apply history (metafetch records one row per field an apply changed) and of
// "undo last apply" (which restores those rows): both sides render a field the
// same way, so a compare-and-set on the rendered value is exact.
//
// Rendering: a nil pointer is "", a string or *string is its value, an integer
// is decimal, a bool is "true"/"false", a float is its shortest decimal form,
// and anything else (a time, a slice, a struct) is its JSON encoding. This is
// the same shape the history rows had before, where every value was a plain
// string, so readers of older rows (the revert-metadata-fetch job, the UI) see
// no difference.

// bookFieldSkip lists the Book fields that are never history-tracked: the
// store owns ID and the timestamps, and the db:"-" fields are joins or caches
// that are not columns of the book row.
func bookFieldSkip(f reflect.StructField) bool {
	switch f.Name {
	case "ID", "CreatedAt", "UpdatedAt":
		return true
	}
	return f.Tag.Get("db") == "-" || !f.IsExported()
}

func bookFieldJSONName(f reflect.StructField) string {
	name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
	if name == "" {
		return f.Name
	}
	return name
}

// bookFieldByJSON returns the addressable Book field with that JSON name.
func bookFieldByJSON(b *Book, jsonName string) (reflect.Value, error) {
	if b == nil {
		return reflect.Value{}, fmt.Errorf("book field %s: nil book", jsonName)
	}
	v := reflect.ValueOf(b).Elem()
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if bookFieldSkip(f) {
			continue
		}
		if bookFieldJSONName(f) == jsonName {
			return v.Field(i), nil
		}
	}
	return reflect.Value{}, fmt.Errorf("book has no tracked field %q", jsonName)
}

// RenderBookField renders the Book field named by its JSON name (see above).
func RenderBookField(b *Book, jsonName string) (string, error) {
	f, err := bookFieldByJSON(b, jsonName)
	if err != nil {
		return "", err
	}
	return renderValue(f)
}

func renderValue(f reflect.Value) (string, error) {
	if f.Kind() == reflect.Pointer {
		if f.IsNil() {
			return "", nil
		}
		f = f.Elem()
	}
	switch f.Kind() {
	case reflect.String:
		return f.String(), nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.FormatInt(f.Int(), 10), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return strconv.FormatUint(f.Uint(), 10), nil
	case reflect.Bool:
		return strconv.FormatBool(f.Bool()), nil
	case reflect.Float32, reflect.Float64:
		return strconv.FormatFloat(f.Float(), 'g', -1, 64), nil
	}
	if (f.Kind() == reflect.Slice || f.Kind() == reflect.Map) && f.Len() == 0 {
		return "", nil
	}
	data, err := json.Marshal(f.Interface())
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// RestoreRenderedBookField sets the Book field named by its JSON name from a
// rendered value. "" restores the empty state (nil for a pointer, the zero
// value otherwise). A value that does not parse as the field's type is an
// error and the book is left untouched.
func RestoreRenderedBookField(b *Book, jsonName, rendered string) error {
	f, err := bookFieldByJSON(b, jsonName)
	if err != nil {
		return err
	}
	if rendered == "" {
		// A *string is emptied to a pointer to "", not nil: updateBookLocked
		// reads a nil Description/VersionNotes as "stripped by the memdb
		// projection" and puts the stored value back, so nil cannot clear
		// them. "" renders the same as nil, so the compare-and-set still holds.
		if f.Kind() == reflect.Pointer && f.Type().Elem().Kind() == reflect.String {
			empty := ""
			f.Set(reflect.ValueOf(&empty))
			return nil
		}
		f.Set(reflect.Zero(f.Type()))
		return nil
	}
	target := f.Type()
	isPtr := target.Kind() == reflect.Pointer
	if isPtr {
		target = target.Elem()
	}
	val := reflect.New(target).Elem()
	switch target.Kind() {
	case reflect.String:
		val.SetString(rendered)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n, perr := strconv.ParseInt(rendered, 10, 64)
		if perr != nil {
			return fmt.Errorf("book field %s: %q is not an integer", jsonName, rendered)
		}
		val.SetInt(n)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		n, perr := strconv.ParseUint(rendered, 10, 64)
		if perr != nil {
			return fmt.Errorf("book field %s: %q is not an unsigned integer", jsonName, rendered)
		}
		val.SetUint(n)
	case reflect.Bool:
		bv, perr := strconv.ParseBool(rendered)
		if perr != nil {
			return fmt.Errorf("book field %s: %q is not a bool", jsonName, rendered)
		}
		val.SetBool(bv)
	case reflect.Float32, reflect.Float64:
		fv, perr := strconv.ParseFloat(rendered, 64)
		if perr != nil {
			return fmt.Errorf("book field %s: %q is not a number", jsonName, rendered)
		}
		val.SetFloat(fv)
	default:
		if uerr := json.Unmarshal([]byte(rendered), val.Addr().Interface()); uerr != nil {
			return fmt.Errorf("book field %s: %w", jsonName, uerr)
		}
	}
	if isPtr {
		f.Set(val.Addr())
	} else {
		f.Set(val)
	}
	return nil
}

// ChangedBookFields returns the JSON names of the tracked Book fields whose
// persisted (JSON) value differs between before and after, in struct order.
// Nil and empty collections compare equal, as in MergeBookChanges.
func ChangedBookFields(before, after *Book) ([]string, error) {
	if before == nil || after == nil {
		return nil, fmt.Errorf("changed book fields: nil book")
	}
	bv := reflect.ValueOf(before).Elem()
	av := reflect.ValueOf(after).Elem()
	t := bv.Type()
	var changed []string
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if bookFieldSkip(f) {
			continue
		}
		if emptyCollections(bv.Field(i), av.Field(i)) {
			continue
		}
		bj, err := json.Marshal(bv.Field(i).Interface())
		if err != nil {
			return nil, fmt.Errorf("changed book fields: encode %s: %w", f.Name, err)
		}
		aj, err := json.Marshal(av.Field(i).Interface())
		if err != nil {
			return nil, fmt.Errorf("changed book fields: encode %s: %w", f.Name, err)
		}
		if !bytes.Equal(bj, aj) {
			changed = append(changed, bookFieldJSONName(f))
		}
	}
	return changed, nil
}

// CopyBookFields sets each named field (JSON name, as MergeBookChanges
// returns them) of dst from src. A name that is not a tracked field (a db:"-"
// join MergeBookChanges also reports) is skipped.
func CopyBookFields(dst, src *Book, jsonNames []string) error {
	for _, name := range jsonNames {
		d, err := bookFieldByJSON(dst, name)
		if err != nil {
			continue
		}
		sv, err := bookFieldByJSON(src, name)
		if err != nil {
			return err
		}
		d.Set(sv)
	}
	return nil
}
