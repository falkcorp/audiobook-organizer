// file: core/internal/sdnotify/require_test.go
// version: 1.0.0
// guid: 702e8410-4457-4614-a46e-68fcd3e51e10
// last-edited: 2026-11-08

package sdnotify

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

// require is the subset of testify's require that the moved sdnotify tests
// used. core's tests are standard library only (03 R1(b)), so the move keeps
// the test file's call sites unchanged (it must stay a rename of at least
// 90 %); the next commit rewrites them and deletes this file.
var require requireShim

type requireShim struct{}

func (requireShim) NoError(t *testing.T, err error, msg ...any) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v %v", err, msg)
	}
}

func (requireShim) Error(t *testing.T, err error, msg ...any) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected an error %v", msg)
	}
}

func (requireShim) ErrorIs(t *testing.T, err, target error, msg ...any) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Fatalf("error %v is not %v %v", err, target, msg)
	}
}

func (requireShim) Equal(t *testing.T, want, got any, msg ...any) {
	t.Helper()
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("got %v, want %v %v", got, want, msg)
	}
}

func (requireShim) Empty(t *testing.T, got string, msg ...any) {
	t.Helper()
	if got != "" {
		t.Fatalf("got %q, want empty %v", got, msg)
	}
}

func (requireShim) NotContains(t *testing.T, s, sub string, msg ...any) {
	t.Helper()
	if strings.Contains(s, sub) {
		t.Fatalf("%q contains %q %v", s, sub, msg)
	}
}
