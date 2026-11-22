// file: core/archtest/principalclass_test.go
// version: 0.1.0
// guid: 0c8f5a31-7e24-4b96-a1d3-5f92e6b8c470
// last-edited: 2026-11-22

package archtest

import (
	"reflect"
	"testing"
)

func TestMethodClassCallDetection(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want []string
	}{
		{"credential guard asks the method", `package p
func f(p *P) bool { return p.Method.MayChangeCredentials() }`, []string{"2: .Method.MayChangeCredentials()"}},
		{"session guard asks the method", `package p
func f(p *P) bool { return !p.Method.Interactive() }`, []string{"2: .Method.Interactive()"}},
		{"through a deeper selector", `package p
func f(r *R) bool { return r.p.Method.Interactive() }`, []string{"2: .Method.Interactive()"}},
		{"the principal is asked", `package p
func f(p *P) bool { return p.Interactive() && p.MayChangeCredentials() }`, nil},
		{"a conversion is not a principal's field", `package p
func f(s string) bool { return Method(s).Interactive() }`, nil},
		{"another field's predicate", `package p
func f(p *P) bool { return p.Other.Interactive() }`, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := methodClassCalls("x.go", tc.src)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("hits = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestPrincipalClassFromRegistryOverCore: no core authorization site asks a
// principal's Method for its classification (F4-08 B10y). The exemptions:
//   - auth/method.go: the Principal accessors themselves start from core's
//     own list and add the registry's answer.
//   - realtime/handler.go: bucketOf names a rate bucket class, not an
//     authorization decision; an extension principal gets its own
//     "method:<name>" bucket, which bounds it no less.
//   - kernel/verified_method.go: the verified-method rule asks the Method and
//     then its own map of the registered extensions' Interactive specs, the
//     same answer the principal carries.
func TestPrincipalClassFromRegistryOverCore(t *testing.T) {
	PrincipalClassFromRegistry(t, coreRoot(t), []string{
		"auth/method.go",
		"realtime/handler.go",
		"kernel/verified_method.go",
	})
}
