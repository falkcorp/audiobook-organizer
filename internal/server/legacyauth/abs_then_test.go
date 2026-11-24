// file: internal/server/legacyauth/abs_then_test.go
// version: 0.1.0
// guid: 8d4a1f73-6b92-4c0e-a5d8-3e7b9c2f1a64
// last-edited: 2026-11-21

package legacyauth

import (
	"errors"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"

	coreauth "github.com/falkcorp/mediacore/auth"
)

// TestABSRequireAuth_InactiveAccountIs403 (F4-08 B10b1): a credential core
// accepts for an account that may not sign in answers 403 "account is not
// active", as HEAD's absCheckUserActive did, not the blanket 401 that tells a
// client to forget the credential.
func TestABSRequireAuth_InactiveAccountIs403(t *testing.T) {
	user := ABSCredential{User: &coreauth.User{ID: bruceID}, Epoch: 3}
	for name, err := range map[string]error{
		"deactivated":    coreauth.ErrInactiveUser,
		"reset required": coreauth.ErrResetRequired,
	} {
		t.Run(name, func(t *testing.T) {
			rec := serveABS(ABSRequireAuth(&fakeCompat{authErr: err}, credOf(user, nil), routePerm))
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d (%s), want 403", rec.Code, rec.Body.String())
			}
		})
	}
	// A stale epoch stays 401: it is not an account standing refusal.
	rec := serveABS(ABSRequireAuth(&fakeCompat{authErr: coreauth.ErrStaleEpoch}, credOf(user, nil), routePerm))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("a stale epoch answered %d, want 401", rec.Code)
	}
}

// TestABSRequireAuthThen_ResolverRefusalKeepsItsStatus: an *ABSRefusal from the
// resolver answers its own status and message (HEAD's 403 for a person who is
// not allowed, 500 for a session lookup that failed); any other error is 401.
func TestABSRequireAuthThen_ResolverRefusalKeepsItsStatus(t *testing.T) {
	f := &fakeCompat{p: absPrincipal()}
	for status, err := range map[int]error{
		http.StatusForbidden:           &ABSRefusal{Status: http.StatusForbidden, Message: "identity is not authorized for this server"},
		http.StatusInternalServerError: &ABSRefusal{Status: http.StatusInternalServerError, Message: "could not verify session"},
		http.StatusUnauthorized:        errors.New("plain error"),
	} {
		rec := serveABS(ABSRequireAuthThen(f, credOf(ABSCredential{}, err), routePerm, nil, nil))
		if rec.Code != status {
			t.Errorf("%v: status %d, want %d", err, rec.Code, status)
		}
	}
	if len(f.keys)+len(f.users)+len(f.checked) != 0 {
		t.Error("a resolver refusal still asked the compat handle")
	}
}

// TestABSRequireAuthThen_Hooks: refused sees every refusal once with its
// status before the body is written; admitted runs only after the principal is
// admitted and bound, can stop the chain, and never runs for a refusal.
func TestABSRequireAuthThen_Hooks(t *testing.T) {
	key := ABSCredential{Key: "abk_riddler"}

	var refusals []int
	refused := func(_ *gin.Context, status int, _ error) { refusals = append(refusals, status) }
	admittedCalls := 0
	admitted := func(*gin.Context, *coreauth.Principal, ABSCredential) bool { admittedCalls++; return true }

	serveABS(ABSRequireAuthThen(&fakeCompat{authErr: coreauth.ErrRevoked}, credOf(key, nil), routePerm, admitted, refused))
	serveABS(ABSRequireAuthThen(&fakeCompat{p: absPrincipal(), checkErr: coreauth.ErrConfined}, credOf(key, nil), routePerm, admitted, refused))
	if len(refusals) != 2 || refusals[0] != 401 || refusals[1] != 403 {
		t.Errorf("refused saw %v, want [401 403]", refusals)
	}
	if admittedCalls != 0 {
		t.Errorf("admitted ran %d times for refused credentials", admittedCalls)
	}

	refusals = nil
	if rec := serveABS(ABSRequireAuthThen(&fakeCompat{p: absPrincipal()}, credOf(key, nil), routePerm, admitted, refused)); rec.Code != 200 {
		t.Fatalf("admitted credential: %d %s", rec.Code, rec.Body.String())
	}
	if admittedCalls != 1 || len(refusals) != 0 {
		t.Errorf("admitted ran %d times, refused %v for an admitted credential", admittedCalls, refusals)
	}

	stop := func(c *gin.Context, _ *coreauth.Principal, _ ABSCredential) bool {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "stopped"})
		return false
	}
	if rec := serveABS(ABSRequireAuthThen(&fakeCompat{p: absPrincipal()}, credOf(key, nil), routePerm, stop, nil)); rec.Code != 403 || rec.Body.String() == "reached" {
		t.Errorf("a hook that stopped the chain: %d %q", rec.Code, rec.Body.String())
	}
}
