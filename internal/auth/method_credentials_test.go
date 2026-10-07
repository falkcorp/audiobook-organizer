// file: internal/auth/method_credentials_test.go
// version: 1.0.0
// guid: 5f1c8b3e-7a24-4d96-8e0b-c4a9d2f6b713
// last-edited: 2026-10-07

package auth

import "testing"

func TestMethodMayChangeCredentials(t *testing.T) {
	cases := []struct {
		m    Method
		want bool
	}{
		{MethodSession, true},
		{MethodSessionDelegated, true},
		{MethodCFAccess, true},
		{MethodAPIKey, false},
		{MethodABS, false},
		{MethodNone, false},
		{Method("bearer_from_the_future"), false}, // unclassified fails closed
	}
	for _, tc := range cases {
		if got := tc.m.MayChangeCredentials(); got != tc.want {
			t.Errorf("Method(%q).MayChangeCredentials() = %v, want %v", tc.m, got, tc.want)
		}
	}
}
