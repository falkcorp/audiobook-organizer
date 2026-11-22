// file: core/archtest/inviterule_test.go
// version: 0.1.0
// guid: bf7f1d05-bdb1-4608-aefe-30f797efcfc6
// last-edited: 2026-11-22

package archtest

import (
	"reflect"
	"testing"
)

func TestLegacyInviteRefDetection(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want []string
	}{
		{"a list through the legacy method", `package p
func f(a A) { for range a.AdminInvites(ctx) {} }`, []string{"2: .AdminInvites"}},
		{"a delete through the legacy method", `package p
func f(a A) error { return a.Invites.AdminDeleteInvite(ctx, "x") }`, []string{"2: .AdminDeleteInvite"}},
		{"a method value", `package p
func f(a A) any { return a.AdminDeleteInvite }`, []string{"2: .AdminDeleteInvite"}},
		{"core's own rule", `package p
func f(a A) error { _ = a.Invites(ctx); return a.DeleteInvite(ctx, p, "x") }`, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := legacyInviteRefs("x.go", tc.src)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("hits = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestInviteRuleLegacyOnlyOverCore: no core package names the legacy-only
// invite methods (F4-08 B10y): they are defined in core/auth and called only
// by the app's legacy surface.
func TestInviteRuleLegacyOnlyOverCore(t *testing.T) {
	InviteRuleLegacyOnly(t, coreRoot(t))
}
