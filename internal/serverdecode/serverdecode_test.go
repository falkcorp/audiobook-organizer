// file: internal/serverdecode/serverdecode_test.go
// version: 1.0.0
// guid: 0b9d6f24-81ce-4a73-b5e0-2f4a7c19d6e5
// last-edited: 2026-10-09

package serverdecode

import (
	"errors"
	"strings"
	"testing"
)

func TestAllowed(t *testing.T) {
	cases := []struct {
		name string
		set  bool
		val  string
		want bool
	}{
		{"unset", false, "", false},
		{"empty", true, "", false},
		{"one", true, "1", true},
		{"true", true, "true", true},
		{"zero", true, "0", false},
		{"garbage", true, "garbage", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.set {
				t.Setenv(EnvVar, tc.val)
			} else {
				t.Setenv(EnvVar, "")
			}
			if got := Allowed(); got != tc.want {
				t.Fatalf("Allowed() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestCheck(t *testing.T) {
	t.Setenv(EnvVar, "")
	err := Check("some.op")
	if !errors.Is(err, ErrRefused) {
		t.Fatalf("Check with env unset = %v, want ErrRefused", err)
	}
	if !strings.Contains(err.Error(), "some.op") || !strings.Contains(err.Error(), EnvVar) {
		t.Fatalf("error should name op and env var: %v", err)
	}
	t.Setenv(EnvVar, "1")
	if err := Check("some.op"); err != nil {
		t.Fatalf("Check with env=1 = %v, want nil", err)
	}
}
