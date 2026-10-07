// file: internal/auth/owner_test.go
// version: 1.0.0
// guid: 5c1e7a93-4d28-4f6b-9a0e-3b8d2f6c1e47
// last-edited: 2026-10-07

package auth

import (
	"context"
	"testing"
)

// TestIsOwnerEmail is the 2026-10-07 review regression: the owner check used
// strings.EqualFold, whose Unicode folding let a different IdP account
// ("Kate@…" with the Kelvin sign, "ownerſ@…" with the long s) pass as the
// owner.
func TestIsOwnerEmail(t *testing.T) {
	const owner = "kate@example.test"
	cases := []struct {
		name, claim, owner string
		want               bool
	}{
		{"exact", "kate@example.test", owner, true},
		{"ASCII case differs", "Kate@Example.TEST", owner, true},
		{"configured value has spaces", "kate@example.test", "  kate@example.test\n", true},
		{"kelvin sign folds to k", "Kate@example.test", owner, false},
		{"long s folds to s", "kate@example.teſt", owner, false},
		{"kelvin sign in the configured value", "kate@example.test", "Kate@example.test", false},
		{"non-ASCII on both sides", "käte@example.test", "käte@example.test", false},
		{"claim padded with a space", " kate@example.test", owner, false},
		{"claim padded with a no-break space", "kate@example.test ", owner, false},
		{"claim with a NUL", "kate@example.test\x00", owner, false},
		{"another mailbox", "kat@example.test", owner, false},
		{"empty claim", "", owner, false},
		{"empty owner", "", "", false},
		{"blank owner", "kate@example.test", "   ", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsOwnerEmail(tc.claim, tc.owner); got != tc.want {
				t.Errorf("IsOwnerEmail(%q, %q) = %v, want %v", tc.claim, tc.owner, got, tc.want)
			}
		})
	}
}

func TestOwnerProofWhyNot_UnicodeLookalikeRefused(t *testing.T) {
	ctx := WithAccessEmail(WithMethod(context.Background(), MethodCFAccess), "Kate@example.test")
	if why := OwnerProofWhyNot(ctx, "kate@example.test", ""); why == "" {
		t.Fatal("a Kelvin-sign lookalike of the owner email passed the owner proof")
	}
	ctx = WithAccessEmail(WithMethod(context.Background(), MethodCFAccess), "kate@example.test")
	if why := OwnerProofWhyNot(ctx, "kate@example.test", ""); why != "" {
		t.Fatalf("the owner was refused: %s", why)
	}
	// An empty claim never matches an unset owner, and an unset owner
	// refuses even a well-formed claim.
	if OwnerProofWhyNot(WithMethod(context.Background(), MethodCFAccess), "", "") == "" {
		t.Fatal("unset owner with no claim passed")
	}
}
