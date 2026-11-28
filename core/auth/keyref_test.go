// file: core/auth/keyref_test.go
// version: 1.0.0
// guid: 5b8e2d47-1c93-4f06-a7d1-93e0c6b2f458
// last-edited: 2026-11-28

package auth_test

import (
	"context"
	"testing"

	"github.com/falkcorp/mediacore/auth"
)

// TestPrincipalKeyRef pins what KeyRef reports for every shape of principal:
// the key's id for core's own key principal and for a compat surface's
// principal built over a key (whose Method is the surface's, so the Method
// alone never says it is a key), and nothing for a session, the
// auth-disabled principal or no principal. The compat case sets CredentialID
// to something else on purpose: KeyRef answers from the key the principal was
// built over, not from a field a holder can edit.
func TestPrincipalKeyRef(t *testing.T) {
	f, c := compatFx(t)
	ctx := context.Background()
	dick := f.user("dick-grayson", "gotham.detective")
	sess := f.session(dick)
	raw, k := f.mint(sess, dick.ID, "gotham:patrol")

	if id, ok := sess.KeyRef(); ok || id != "" {
		t.Errorf("session KeyRef = %q, %v; want no key", id, ok)
	}

	apiKey := f.keyPrincipal(dick, k)
	if id, ok := apiKey.KeyRef(); !ok || id != k.ID {
		t.Errorf("api_key KeyRef = %q, %v; want %q, true", id, ok, k.ID)
	}

	compat, err := c.AuthenticateKey(ctx, raw, from("192.0.2.73"))
	if err != nil {
		t.Fatalf("AuthenticateKey: %v", err)
	}
	if compat.Method == auth.MethodAPIKey {
		t.Fatalf("compat principal has core's method %q; the cell needs a surface's", compat.Method)
	}
	compat.CredentialID = "not-the-key"
	if id, ok := compat.KeyRef(); !ok || id != k.ID {
		t.Errorf("compat key KeyRef = %q, %v; want %q, true", id, ok, k.ID)
	}

	// An api_key principal that lost its id is still a key principal, with no id.
	noID := f.keyPrincipal(dick, k)
	noID.CredentialID = ""
	if id, ok := noID.KeyRef(); !ok || id != "" {
		t.Errorf("api_key with no id KeyRef = %q, %v; want \"\", true", id, ok)
	}

	if id, ok := (&auth.Principal{Method: auth.MethodAuthDisabled}).KeyRef(); ok || id != "" {
		t.Errorf("auth-disabled KeyRef = %q, %v; want no key", id, ok)
	}
	var nilP *auth.Principal
	if id, ok := nilP.KeyRef(); ok || id != "" {
		t.Errorf("nil KeyRef = %q, %v; want no key", id, ok)
	}
}
