// file: internal/server/legacyauth/mapping_test.go
// version: 0.1.0
// guid: 2f8d6b41-0c5a-4e93-b7d2-9a1e4c3f6d70
// last-edited: 2026-11-21

package legacyauth

import (
	"context"
	"testing"

	coreauth "github.com/falkcorp/mediacore/auth"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/cfaccess"
)

// bridged runs FromCorePrincipal on p (checker laxChecker) and returns what it
// filled; it fails the test on a refusal.
func bridged(t *testing.T, p *coreauth.Principal) context.Context {
	t.Helper()
	ctx, err := newBridge(t, nil).FromCorePrincipal(bound(p, laxChecker{}))
	if err != nil {
		t.Fatalf("FromCorePrincipal(%q): %v", p.Method, err)
	}
	return ctx
}

// TestLegacyMethod_BooksABS (F4-08 B10b1): an ABS bearer principal is judged as
// HEAD's abs, an ABS-surface key as HEAD's api_key. Neither is interactive,
// neither may change credentials, neither can pass the owner proof. A mapping
// to session or session_delegated (the "session-like" widening) fails here.
func TestLegacyMethod_BooksABS(t *testing.T) {
	bearer := principal(MethodBooksABS, laxChecker{})
	key := principal(MethodBooksABS, laxChecker{})
	key.CredentialID = "key-gotham-1"
	for name, tc := range map[string]struct {
		p    *coreauth.Principal
		want auth.Method
	}{
		"a bearer or refresh session": {bearer, auth.MethodABS},
		"a key principal":             {key, auth.MethodAPIKey},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := bridged(t, tc.p)
			got := auth.MethodFromContext(ctx)
			if got != tc.want {
				t.Fatalf("method = %q, want %q", got, tc.want)
			}
			if got.Interactive() || got.MayChangeCredentials() {
				t.Errorf("%q is interactive=%v mayChangeCredentials=%v: a widening against HEAD's abs/api_key",
					got, got.Interactive(), got.MayChangeCredentials())
			}
			if why := auth.OwnerProofWhyNot(auth.WithAccessEmail(ctx, "owner@example.com"), "owner@example.com", ""); why == "" {
				t.Errorf("owner proof passed for %q even with the owner's e-mail", got)
			}
		})
	}
}

// TestLegacyMethod_ExtCFAccess (F4-08 B10b1): ext:cfaccess is HEAD's cf_access
// (cfaccess/binder.go), no narrower (a Cloudflare Access sign-in changed
// credentials at HEAD) and no wider (the method alone does not prove the
// owner: the proof needs the verified Access e-mail, which the bridge never
// fills from the account record).
func TestLegacyMethod_ExtCFAccess(t *testing.T) {
	p := principal(MethodExtCFAccess, laxChecker{})
	p.User.Email = "owner@example.com" // the ACCOUNT's e-mail: never the proof
	ctx := bridged(t, p)
	got := auth.MethodFromContext(ctx)
	if got != auth.MethodCFAccess {
		t.Fatalf("method = %q, want cf_access", got)
	}
	if !got.Interactive() || !got.MayChangeCredentials() {
		t.Errorf("cf_access interactive=%v mayChangeCredentials=%v, want HEAD's true/true", got.Interactive(), got.MayChangeCredentials())
	}
	if e := auth.AccessEmailFromContext(ctx); e != "" {
		t.Errorf("the bridge filled the Access e-mail %q from the account record", e)
	}
	if why := auth.OwnerProofWhyNot(ctx, "owner@example.com", ""); why == "" {
		t.Error("the owner proof passed on the method and the account's own e-mail alone")
	}
	// The proof is reachable only with the verified assertion's e-mail, as at HEAD.
	if why := auth.OwnerProofWhyNot(auth.WithAccessEmail(ctx, "owner@example.com"), "owner@example.com", ""); why != "" {
		t.Errorf("the verified owner assertion is refused: %s", why)
	}
}

// TestLegacyMethod_FailsClosed: a method the table does not list, however it
// resembles one that is, fills nothing and is refused.
func TestLegacyMethod_FailsClosed(t *testing.T) {
	for _, m := range []coreauth.Method{"ext:cfaccess ", "EXT:cfaccess", "books.abs.", "books", "ext:", "abs", "cf_access", "api_key2"} {
		t.Run(string(m), func(t *testing.T) {
			ctx, err := newBridge(t, nil).FromCorePrincipal(as(m, laxChecker{}))
			if err == nil {
				t.Fatalf("method %q was admitted", m)
			}
			if u, perms, mm := legacyValues(ctx); u != nil || perms != nil || mm != auth.MethodNone {
				t.Errorf("a refusal filled user=%v perms=%v method=%q", u, perms, mm)
			}
		})
	}
}

// TestMethodNamesMatchTheirRegistrations: the two constants are spelled here
// because this package never imports the modules that register them; this
// keeps each equal to what its registrant uses.
func TestMethodNamesMatchTheirRegistrations(t *testing.T) {
	if want := coreauth.Method("ext:" + cfaccess.ExtensionName); MethodExtCFAccess != want {
		t.Errorf("MethodExtCFAccess = %q, cfaccess registers %q", MethodExtCFAccess, want)
	}
}
