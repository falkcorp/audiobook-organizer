// file: core/httpapi/stage13_methodspec_test.go
// version: 0.1.0
// guid: 5d2c8f17-a64e-4b09-93c1-e7b0a2f5d684
// last-edited: 2026-11-22

package httpapi

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"
)

// s13NoCredential is an extension authenticator that never claims a request:
// these cells build their principals through auth.Service.PrincipalFor, the
// path stage 12 takes for an extension's assertion, so only the registration
// (its MethodSpec) matters.
type s13NoCredential struct{ name string }

func (a s13NoCredential) Name() string { return a.name }
func (s13NoCredential) Authenticate(*http.Request) (*auth.Assertion, error) {
	return nil, auth.ErrNoCredential
}

// s13Extensions registers one extension per combination of the two
// classification fields, so a guard that reads the wrong field, or ignores
// one, flips at least one cell. ext:interact is the shape r11 gave
// ext:cfaccess (Interactive only).
func s13Extensions() []auth.Extension {
	ext := func(name string, interactive, creds bool, order auth.Order) auth.Extension {
		return auth.Extension{
			Authenticator: s13NoCredential{name: name}, Order: order,
			Spec: auth.MethodSpec{Method: auth.Method("ext:" + name), Interactive: interactive, MayChangeCredentials: creds},
		}
	}
	return []auth.Extension{
		ext("both", true, true, 150),
		ext("interact", true, false, 151),
		ext("creds", false, true, 152),
		ext("neither", false, false, 153),
	}
}

// TestAuthorize_GuardsReadTheRegisteredMethodSpec: Guards.Interactive is HEAD's
// credGuard and asks whether the principal's method may change credentials;
// Guards.Session asks whether it is interactive; the owner fallback (owner not
// required) asks what Guards.Interactive asks before core:admin. Each is
// answered by the principal, which carries the method's registered MethodSpec,
// never by auth.Method's core-only list (F4-08 B10y: the Access owner was
// refused interactive_required on every owner route). The principals are
// built by a real auth.Service through PrincipalFor; the user is a viewer, so
// an owner-fallback cell that clears the method check is refused for
// core:admin, and one that does not is refused interactive_required.
func TestAuthorize_GuardsReadTheRegisteredMethodSpec(t *testing.T) {
	svc := s13RealAuthWith(t, s13Extensions()...)
	ctx := context.Background()
	alfred := &auth.User{ID: "u-alfred", Username: "alfred", Roles: []rbac.RoleName{rbac.RoleViewer}, Status: auth.StatusActive}
	built := func(t *testing.T, m auth.Method) *auth.Principal {
		t.Helper()
		p, err := svc.PrincipalFor(ctx, alfred, m, nil, 0)
		if err != nil {
			t.Fatalf("PrincipalFor(%q): %v", m, err)
		}
		return p
	}

	const (
		admit       = ""
		interactive = "interactive_required"
		denied      = "permission_denied"
	)
	cases := []struct {
		method                         auth.Method
		credGuard, sessionGuard, owner string
	}{
		// method                credGuard    sessionGuard  owner (not required)
		{auth.MethodSession, admit, admit, denied},
		{auth.MethodSessionDelegated, admit, interactive, denied},
		{"ext:both", admit, admit, denied},
		{"ext:interact", interactive, admit, interactive},
		{"ext:creds", admit, interactive, denied},
		{"ext:neither", interactive, interactive, interactive},
	}
	for _, tc := range cases {
		t.Run(string(tc.method), func(t *testing.T) {
			p := built(t, tc.method)
			s13AssertCode(t, "Guards.Interactive", Authorize(AuthzRequest{Perm: rbac.AnyAuthenticated, Guards: Guards{Interactive: true}, Principal: p}, AuthzOptions{}), tc.credGuard)
			s13AssertCode(t, "Guards.Session", Authorize(AuthzRequest{Perm: rbac.AnyAuthenticated, Guards: Guards{Session: true}, Principal: p}, AuthzOptions{}), tc.sessionGuard)
			s13AssertCode(t, "Guards.Owner (not required)", Authorize(AuthzRequest{Perm: rbac.AnyAuthenticated, Guards: Guards{Owner: true}, Principal: p}, AuthzOptions{OwnerRequired: false}), tc.owner)
		})
	}

	// A method nobody registered: PrincipalFor refuses to build it, and a
	// principal assembled elsewhere with it, or with a registered principal's
	// Method rewritten to it, clears neither guard (fail closed).
	t.Run("unknown method", func(t *testing.T) {
		if _, err := svc.PrincipalFor(ctx, alfred, "ext:nobody", nil, 0); !errors.Is(err, auth.ErrMethodSpec) {
			t.Fatalf("PrincipalFor(ext:nobody) = %v, want ErrMethodSpec", err)
		}
		forged := built(t, "ext:both")
		forged.Method = "ext:nobody"
		for name, p := range map[string]*auth.Principal{
			"assembled":         {User: alfred, Method: "ext:nobody", Perms: forged.Perms},
			"method rewritten":  forged,
			"rewritten to none": {User: alfred, Method: auth.MethodNone, Perms: forged.Perms},
		} {
			s13AssertCode(t, name+" Guards.Interactive", Authorize(AuthzRequest{Perm: rbac.AnyAuthenticated, Guards: Guards{Interactive: true}, Principal: p}, AuthzOptions{}), interactive)
			s13AssertCode(t, name+" Guards.Session", Authorize(AuthzRequest{Perm: rbac.AnyAuthenticated, Guards: Guards{Session: true}, Principal: p}, AuthzOptions{}), interactive)
		}
	})

	// The Sensitive rule's refusal code: a principal of an interactive
	// extension that lacks a Sensitive scope is told permission_denied, not
	// interactive_required, since signing in interactively would not help.
	t.Run("sensitive refusal code", func(t *testing.T) {
		cat := s13Catalog(t)
		var sensitive rbac.Permission
		for spec := range cat.All() {
			if spec.Sensitive {
				sensitive = spec.Name
				break
			}
		}
		if sensitive == "" {
			t.Fatal("the catalog declares no Sensitive scope; the cell would be vacuous")
		}
		need := rbac.Need(sensitive)
		s13AssertCode(t, "ext:both lacking "+string(sensitive), Authorize(AuthzRequest{Perm: need, Principal: built(t, "ext:both")}, AuthzOptions{Catalog: cat}), denied)
		s13AssertCode(t, "ext:creds lacking "+string(sensitive), Authorize(AuthzRequest{Perm: need, Principal: built(t, "ext:creds")}, AuthzOptions{Catalog: cat}), interactive)
	})
}

func s13AssertCode(t *testing.T, what string, d AuthzDecision, want string) {
	t.Helper()
	got := ""
	if d.Refusal != nil {
		got = d.Refusal.Code
	}
	if got != want {
		t.Errorf("%s: refusal %q, want %q (empty = admitted)", what, got, want)
	}
}
