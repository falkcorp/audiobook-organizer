// file: core/auth/auth_test.go
// version: 1.0.1
// guid: 8eb27054-dc72-466c-99fe-670b02a386b8
// last-edited: 2026-11-06

package auth

import (
	"context"
	"net/netip"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/rbac"
)

// TestMethod_Classification: among core methods only MethodSession is
// interactive; MethodSession and MethodSessionDelegated may change credentials
// (the reset-link flow; owner decision 2026-11-06); MethodAuthDisabled is explicitly
// non-interactive and may not change credentials (fail closed); unknown
// methods, extension and compat names included, neither (05-authn §3.1).
func TestMethod_Classification(t *testing.T) {
	for _, c := range []struct {
		m           Method
		interactive bool
		mayChange   bool
	}{
		{MethodSession, true, true},
		{MethodSessionDelegated, false, true},
		{MethodAPIKey, false, false},
		{MethodAuthDisabled, false, false},
		{MethodNone, false, false},
		// Extensions and compat surfaces declare their own classification in
		// their MethodSpec; as bare Method values they are unknown.
		{Method("ext:cfaccess"), false, false},
		{Method("books.abs"), false, false},
		{Method("SESSION"), false, false},
		{Method("session "), false, false},
		{Method("unheard_of"), false, false},
	} {
		if got := c.m.Interactive(); got != c.interactive {
			t.Errorf("Method(%q).Interactive() = %v, want %v", c.m, got, c.interactive)
		}
		if got := c.m.MayChangeCredentials(); got != c.mayChange {
			t.Errorf("Method(%q).MayChangeCredentials() = %v, want %v", c.m, got, c.mayChange)
		}
	}
}

// TestMethod_ConstantsAreTheSpecsStrings pins the wire values (they appear in
// audit records and sessions).
func TestMethod_ConstantsAreTheSpecsStrings(t *testing.T) {
	for m, want := range map[Method]string{
		MethodNone:             "",
		MethodSession:          "session",
		MethodSessionDelegated: "session_delegated",
		MethodAPIKey:           "api_key",
		MethodAuthDisabled:     "auth_disabled",
	} {
		if string(m) != want {
			t.Errorf("Method %q, want %q", m, want)
		}
	}
	for s, want := range map[UserStatus]string{
		StatusActive:   "active",
		StatusDisabled: "disabled",
		StatusPending:  "pending",
		StatusSystem:   "system",
	} {
		if string(s) != want {
			t.Errorf("UserStatus %q, want %q", s, want)
		}
	}
}

// TestAssurance_IsABitmask: None is zero and the named assurances are
// distinct single bits that compose and test independently.
func TestAssurance_IsABitmask(t *testing.T) {
	if AssuranceNone != 0 {
		t.Fatalf("AssuranceNone = %d, want 0", AssuranceNone)
	}
	single := func(a Assurance) bool { return a != 0 && a&(a-1) == 0 }
	if !single(AssuranceOwner) || !single(AssuranceMFA) || AssuranceOwner == AssuranceMFA {
		t.Fatalf("AssuranceOwner=%d AssuranceMFA=%d, want distinct single bits", AssuranceOwner, AssuranceMFA)
	}
	both := AssuranceOwner | AssuranceMFA
	if both&AssuranceOwner == 0 || both&AssuranceMFA == 0 {
		t.Fatal("the assurances do not compose")
	}
	if AssuranceOwner&AssuranceMFA != 0 {
		t.Fatal("the assurances overlap")
	}
}

type permsStub struct{ rbac.Checker }

// TestPrincipal_Context: a principal round-trips through the context by
// pointer; none bound reports false; a nil binds none and shadows an outer one.
func TestPrincipal_Context(t *testing.T) {
	if p, ok := PrincipalFrom(context.Background()); ok || p != nil {
		t.Fatalf("PrincipalFrom(empty ctx) = %v, %v", p, ok)
	}

	u := &User{ID: "01J0000000000000000000000", Username: "bruce", Roles: []rbac.RoleName{rbac.RoleAdmin}, Status: StatusActive}
	want := &Principal{
		User:         u,
		Method:       MethodSession,
		CredentialID: "sess_public_id",
		Perms:        permsStub{},
		Assurance:    AssuranceOwner,
		AuthTime:     time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC),
		ClientAddr:   netip.MustParseAddr("192.0.2.7"),
	}
	ctx := WithPrincipal(context.Background(), want)
	got, ok := PrincipalFrom(ctx)
	if !ok || got != want {
		t.Fatalf("PrincipalFrom = %p, %v; want %p, true", got, ok, want)
	}
	// A derived context still sees it.
	child, cancel := context.WithCancel(ctx)
	defer cancel()
	if got, ok := PrincipalFrom(child); !ok || got != want {
		t.Fatal("a derived context lost the principal")
	}

	// A synthetic auth-disabled principal has no user.
	off := &Principal{Method: MethodAuthDisabled}
	if got, ok := PrincipalFrom(WithPrincipal(ctx, off)); !ok || got != off || got.User != nil {
		t.Fatalf("auth-disabled principal = %+v, %v", got, ok)
	}

	// A nil binds none, even over a context that had one.
	if p, ok := PrincipalFrom(WithPrincipal(ctx, nil)); ok || p != nil {
		t.Fatalf("PrincipalFrom(WithPrincipal(ctx, nil)) = %v, %v; want none", p, ok)
	}

	// The key is this package's own: a plain string key does not collide.
	//lint:ignore SA1029 the point is that a foreign plain-string key never binds a principal
	if _, ok := PrincipalFrom(context.WithValue(context.Background(), "principalKey", want)); ok {
		t.Fatal("a foreign context key bound a principal")
	}
}

type handoffStub struct{}

func (handoffStub) Allowed(string) bool { return true }
func (handoffStub) Mint(context.Context, *User, string, string, string, string) (string, error) {
	return "", nil
}

// TestMethodSpec_ZeroIsNonInteractive: a zero MethodSpec grants nothing, and
// carries the app hand-off only when set.
func TestMethodSpec_ZeroIsNonInteractive(t *testing.T) {
	var s MethodSpec
	if s.Method != MethodNone || s.Interactive || s.MayChangeCredentials || s.OwnerVerifier || s.AppHandoff != nil {
		t.Fatalf("zero MethodSpec = %+v", s)
	}
	s = MethodSpec{Method: "books.abs", AppHandoff: handoffStub{}}
	if s.Interactive || s.MayChangeCredentials || s.OwnerVerifier || s.AppHandoff == nil || !s.AppHandoff.Allowed("x") {
		t.Fatalf("compat MethodSpec = %+v", s)
	}
}
