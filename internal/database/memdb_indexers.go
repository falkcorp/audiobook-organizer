// file: internal/database/memdb_indexers.go
// version: 1.1.0
// guid: a1b2c3d4-mema-aaaa-aaaa-000000000001
// last-edited: 2026-06-23

package database

import (
	"encoding/binary"
	"fmt"
	"reflect"

	"github.com/hashicorp/go-memdb"

	"github.com/falkcorp/audiobook-organizer/internal/util"
)

// Custom go-memdb indexers for our types. memdb ships with StringFieldIndex,
// IntFieldIndex, UUIDFieldIndex, BoolFieldIndex, but they do not handle Go
// pointer fields (*int, *bool, *string) or nil-valued fields cleanly. These
// indexers do, and treat nil as "not indexed for this row" (AllowMissing must
// also be true on the IndexSchema).

// nullableIntFieldIndex indexes a struct field of type *int. Returns
// ErrMissingField when the pointer is nil, which memdb treats as "skip
// indexing this row for this index" when AllowMissing=true.
type nullableIntFieldIndex struct{ Field string }

func (i *nullableIntFieldIndex) FromObject(obj interface{}) (bool, []byte, error) {
	v := reflect.ValueOf(obj)
	if v.Kind() == reflect.Ptr {
		v = v.Elem()
	}
	fv := v.FieldByName(i.Field)
	if !fv.IsValid() {
		return false, nil, fmt.Errorf("nullableIntFieldIndex: field %q missing", i.Field)
	}
	if fv.Kind() != reflect.Ptr {
		return false, nil, fmt.Errorf("nullableIntFieldIndex: field %q is not a pointer", i.Field)
	}
	if fv.IsNil() {
		return false, nil, nil
	}
	return true, encodeInt64(fv.Elem().Int()), nil
}

func (i *nullableIntFieldIndex) FromArgs(args ...interface{}) ([]byte, error) {
	if len(args) != 1 {
		return nil, fmt.Errorf("nullableIntFieldIndex: must provide exactly one arg")
	}
	switch v := args[0].(type) {
	case int:
		return encodeInt64(int64(v)), nil
	case int64:
		return encodeInt64(v), nil
	case *int:
		if v == nil {
			return nil, fmt.Errorf("nullableIntFieldIndex: cannot query with nil")
		}
		return encodeInt64(int64(*v)), nil
	default:
		return nil, fmt.Errorf("nullableIntFieldIndex: unsupported arg type %T", args[0])
	}
}

// nullableBoolFieldIndex indexes a struct field of type *bool.
type nullableBoolFieldIndex struct{ Field string }

func (i *nullableBoolFieldIndex) FromObject(obj interface{}) (bool, []byte, error) {
	v := reflect.ValueOf(obj)
	if v.Kind() == reflect.Ptr {
		v = v.Elem()
	}
	fv := v.FieldByName(i.Field)
	if !fv.IsValid() {
		return false, nil, fmt.Errorf("nullableBoolFieldIndex: field %q missing", i.Field)
	}
	if fv.Kind() != reflect.Ptr {
		return false, nil, fmt.Errorf("nullableBoolFieldIndex: field %q is not a pointer", i.Field)
	}
	if fv.IsNil() {
		return false, nil, nil
	}
	return true, encodeBool(fv.Elem().Bool()), nil
}

func (i *nullableBoolFieldIndex) FromArgs(args ...interface{}) ([]byte, error) {
	if len(args) != 1 {
		return nil, fmt.Errorf("nullableBoolFieldIndex: must provide exactly one arg")
	}
	switch v := args[0].(type) {
	case bool:
		return encodeBool(v), nil
	case *bool:
		if v == nil {
			return nil, fmt.Errorf("nullableBoolFieldIndex: cannot query with nil")
		}
		return encodeBool(*v), nil
	default:
		return nil, fmt.Errorf("nullableBoolFieldIndex: unsupported arg type %T", args[0])
	}
}

// nullableStringFieldIndex indexes a struct field of type *string.
type nullableStringFieldIndex struct{ Field string }

func (i *nullableStringFieldIndex) FromObject(obj interface{}) (bool, []byte, error) {
	v := reflect.ValueOf(obj)
	if v.Kind() == reflect.Ptr {
		v = v.Elem()
	}
	fv := v.FieldByName(i.Field)
	if !fv.IsValid() {
		return false, nil, fmt.Errorf("nullableStringFieldIndex: field %q missing", i.Field)
	}
	if fv.Kind() != reflect.Ptr {
		return false, nil, fmt.Errorf("nullableStringFieldIndex: field %q is not a pointer", i.Field)
	}
	if fv.IsNil() {
		return false, nil, nil
	}
	s := fv.Elem().String()
	if s == "" {
		return false, nil, nil
	}
	return true, []byte(s + "\x00"), nil
}

func (i *nullableStringFieldIndex) FromArgs(args ...interface{}) ([]byte, error) {
	if len(args) != 1 {
		return nil, fmt.Errorf("nullableStringFieldIndex: must provide exactly one arg")
	}
	switch v := args[0].(type) {
	case string:
		return []byte(v + "\x00"), nil
	case *string:
		if v == nil {
			return nil, fmt.Errorf("nullableStringFieldIndex: cannot query with nil")
		}
		return []byte(*v + "\x00"), nil
	default:
		return nil, fmt.Errorf("nullableStringFieldIndex: unsupported arg type %T", args[0])
	}
}

// effectiveBoolFieldIndex indexes a *bool field with a default for nil.
// Use this when nil should be treated as a real indexable value (e.g. nil
// MarkedForDeletion should be treated as "false").
type effectiveBoolFieldIndex struct {
	Field   string
	Default bool
}

func (i *effectiveBoolFieldIndex) FromObject(obj interface{}) (bool, []byte, error) {
	v := reflect.ValueOf(obj)
	if v.Kind() == reflect.Ptr {
		v = v.Elem()
	}
	fv := v.FieldByName(i.Field)
	if !fv.IsValid() {
		return false, nil, fmt.Errorf("effectiveBoolFieldIndex: field %q missing", i.Field)
	}
	if fv.Kind() != reflect.Ptr {
		return false, nil, fmt.Errorf("effectiveBoolFieldIndex: field %q is not a pointer", i.Field)
	}
	if fv.IsNil() {
		return true, encodeBool(i.Default), nil
	}
	return true, encodeBool(fv.Elem().Bool()), nil
}

func (i *effectiveBoolFieldIndex) FromArgs(args ...interface{}) ([]byte, error) {
	if len(args) != 1 {
		return nil, fmt.Errorf("effectiveBoolFieldIndex: must provide exactly one arg")
	}
	switch v := args[0].(type) {
	case bool:
		return encodeBool(v), nil
	default:
		return nil, fmt.Errorf("effectiveBoolFieldIndex: unsupported arg type %T", args[0])
	}
}

// effectiveIntFieldIndex indexes a *int field with a default for nil.
type effectiveIntFieldIndex struct {
	Field   string
	Default int64
}

func (i *effectiveIntFieldIndex) FromObject(obj interface{}) (bool, []byte, error) {
	v := reflect.ValueOf(obj)
	if v.Kind() == reflect.Ptr {
		v = v.Elem()
	}
	fv := v.FieldByName(i.Field)
	if !fv.IsValid() {
		return false, nil, fmt.Errorf("effectiveIntFieldIndex: field %q missing", i.Field)
	}
	if fv.Kind() != reflect.Ptr {
		return false, nil, fmt.Errorf("effectiveIntFieldIndex: field %q is not a pointer", i.Field)
	}
	if fv.IsNil() {
		return true, encodeInt64(i.Default), nil
	}
	return true, encodeInt64(fv.Elem().Int()), nil
}

func (i *effectiveIntFieldIndex) FromArgs(args ...interface{}) ([]byte, error) {
	if len(args) != 1 {
		return nil, fmt.Errorf("effectiveIntFieldIndex: must provide exactly one arg")
	}
	switch v := args[0].(type) {
	case int:
		return encodeInt64(int64(v)), nil
	case int64:
		return encodeInt64(v), nil
	default:
		return nil, fmt.Errorf("effectiveIntFieldIndex: unsupported arg type %T", args[0])
	}
}

// nonEmptyStringFieldIndex indexes a struct field of type string, skipping
// rows where the value is the empty string. The schema must set
// AllowMissing=true. Useful for sparse predicates like "DelugeHash != \"\""
// where we want an index over only the small subset of matching rows rather
// than scanning every row in the table.
type nonEmptyStringFieldIndex struct{ Field string }

func (i *nonEmptyStringFieldIndex) FromObject(obj interface{}) (bool, []byte, error) {
	v := reflect.ValueOf(obj)
	if v.Kind() == reflect.Ptr {
		v = v.Elem()
	}
	fv := v.FieldByName(i.Field)
	if !fv.IsValid() {
		return false, nil, fmt.Errorf("nonEmptyStringFieldIndex: field %q missing", i.Field)
	}
	if fv.Kind() != reflect.String {
		return false, nil, fmt.Errorf("nonEmptyStringFieldIndex: field %q is not a string", i.Field)
	}
	s := fv.String()
	if s == "" {
		return false, nil, nil
	}
	return true, []byte(s + "\x00"), nil
}

func (i *nonEmptyStringFieldIndex) FromArgs(args ...interface{}) ([]byte, error) {
	if len(args) != 1 {
		return nil, fmt.Errorf("nonEmptyStringFieldIndex: must provide exactly one arg")
	}
	switch v := args[0].(type) {
	case string:
		if v == "" {
			return nil, fmt.Errorf("nonEmptyStringFieldIndex: cannot query with empty string")
		}
		return []byte(v + "\x00"), nil
	default:
		return nil, fmt.Errorf("nonEmptyStringFieldIndex: unsupported arg type %T", args[0])
	}
}

// plainBoolFieldIndex indexes a struct field of type bool (not pointer).
type plainBoolFieldIndex struct{ Field string }

func (i *plainBoolFieldIndex) FromObject(obj interface{}) (bool, []byte, error) {
	v := reflect.ValueOf(obj)
	if v.Kind() == reflect.Ptr {
		v = v.Elem()
	}
	fv := v.FieldByName(i.Field)
	if !fv.IsValid() {
		return false, nil, fmt.Errorf("plainBoolFieldIndex: field %q missing", i.Field)
	}
	if fv.Kind() != reflect.Bool {
		return false, nil, fmt.Errorf("plainBoolFieldIndex: field %q is not a bool", i.Field)
	}
	return true, encodeBool(fv.Bool()), nil
}

func (i *plainBoolFieldIndex) FromArgs(args ...interface{}) ([]byte, error) {
	if len(args) != 1 {
		return nil, fmt.Errorf("plainBoolFieldIndex: must provide exactly one arg")
	}
	switch v := args[0].(type) {
	case bool:
		return encodeBool(v), nil
	default:
		return nil, fmt.Errorf("plainBoolFieldIndex: unsupported arg type %T", args[0])
	}
}

// Encoding helpers — bytes are used as radix-tree keys.

func encodeInt64(n int64) []byte {
	// Use big-endian + flip sign bit so negative values sort before positive,
	// which matches numeric ordering needed for range queries.
	buf := make([]byte, 8)
	un := uint64(n) ^ (1 << 63)
	binary.BigEndian.PutUint64(buf, un)
	return buf
}

func encodeBool(b bool) []byte {
	if b {
		return []byte{1}
	}
	return []byte{0}
}

// titleSortIndex indexes Book.Title for sorted iteration, with a fallback so
// every book has a key (even those scanned without enrichment). Order:
//   1. Title (lowercased, trimmed) if non-empty
//   2. OriginalFilename (lowercased) if Title empty
//   3. "~" sentinel — sorts after all printable ASCII so titleless+filename-less
//      books cluster at the end of asc iteration.
//
// Without this fallback, books with empty Title would be dropped from the
// title index entirely, vanishing from the library list when sort_by=title.
type titleSortIndex struct{}

func (titleSortIndex) FromObject(obj interface{}) (bool, []byte, error) {
	b, ok := obj.(*Book)
	if !ok {
		return false, nil, fmt.Errorf("titleSortIndex: expected *Book, got %T", obj)
	}
	key := util.NormalizeTitle(b.Title)
	if key == "" && b.OriginalFilename != nil {
		key = util.NormalizeTitle(*b.OriginalFilename)
	}
	if key == "" {
		key = "~" // sort to end
	}
	// memdb convention: null-terminate for prefix-iteration correctness
	return true, append([]byte(key), 0), nil
}

func (titleSortIndex) FromArgs(args ...interface{}) ([]byte, error) {
	if len(args) != 1 {
		return nil, fmt.Errorf("titleSortIndex: expected 1 arg, got %d", len(args))
	}
	s, ok := args[0].(string)
	if !ok {
		return nil, fmt.Errorf("titleSortIndex: arg must be string, got %T", args[0])
	}
	return append([]byte(util.NormalizeTitle(s)), 0), nil
}

func (titleSortIndex) PrefixFromArgs(args ...interface{}) ([]byte, error) {
	b, err := titleSortIndex{}.FromArgs(args...)
	if err != nil {
		return nil, err
	}
	// drop trailing null for prefix matching
	if len(b) > 0 && b[len(b)-1] == 0 {
		b = b[:len(b)-1]
	}
	return b, nil
}

// Compile-time assertions that custom indexers satisfy memdb interfaces.
var (
	_ memdb.SingleIndexer = (*nullableIntFieldIndex)(nil)
	_ memdb.Indexer       = (*nullableIntFieldIndex)(nil)
	_ memdb.SingleIndexer = (*nullableBoolFieldIndex)(nil)
	_ memdb.Indexer       = (*nullableBoolFieldIndex)(nil)
	_ memdb.SingleIndexer = (*nullableStringFieldIndex)(nil)
	_ memdb.Indexer       = (*nullableStringFieldIndex)(nil)
	_ memdb.SingleIndexer = (*effectiveBoolFieldIndex)(nil)
	_ memdb.Indexer       = (*effectiveBoolFieldIndex)(nil)
	_ memdb.SingleIndexer = (*effectiveIntFieldIndex)(nil)
	_ memdb.Indexer       = (*effectiveIntFieldIndex)(nil)
	_ memdb.SingleIndexer = (*plainBoolFieldIndex)(nil)
	_ memdb.Indexer       = (*plainBoolFieldIndex)(nil)
	_ memdb.SingleIndexer = (*nonEmptyStringFieldIndex)(nil)
	_ memdb.Indexer       = (*nonEmptyStringFieldIndex)(nil)
	_ memdb.SingleIndexer = titleSortIndex{}
	_ memdb.Indexer       = titleSortIndex{}
	_ memdb.PrefixIndexer = titleSortIndex{}
)
