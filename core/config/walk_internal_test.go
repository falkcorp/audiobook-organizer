// file: core/config/walk_internal_test.go
// version: 1.0.1
// guid: 9b2c6f14-3e7a-4d58-8c01-a6f4e2b7d935
// last-edited: 2026-11-10

package config

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

// TestWalk_SameDepthDuplicateRefused (T4 review MAJOR-1): two sibling fields
// with one JSON name, the clash encoding/json resolves by decoding neither,
// are refused with ErrDuplicate naming both. The type is built at run time
// because go vet refuses a repeated json tag on sibling fields, directly or
// through embedded structs, in a declared type.
func TestWalk_SameDepthDuplicateRefused(t *testing.T) {
	typ := reflect.StructOf([]reflect.StructField{
		{Name: "Oracle", Type: reflect.TypeFor[string](), Tag: `json:"frequency" protect:"owner"`},
		{Name: "Robin", Type: reflect.TypeFor[string](), Tag: `json:"frequency"`},
	})
	w := &keyWalker{sec: &section{path: "core.gotham.signal"}}
	err := w.walk(typ, nil, nil, tagSet{}, map[string]string{})
	if !errors.Is(err, ErrDuplicate) || !strings.Contains(err.Error(), ".Oracle") || !strings.Contains(err.Error(), ".Robin") {
		t.Fatalf("walk = %v, want ErrDuplicate naming Oracle and Robin", err)
	}
	if len(w.keys) > 1 {
		t.Errorf("the walker kept %d keys for one JSON name", len(w.keys))
	}
}
