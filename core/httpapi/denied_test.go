// file: core/httpapi/denied_test.go
// version: 0.1.0
// guid: 6a2e9c14-8f3b-4d57-a0c6-3e1b7d95f248
// last-edited: 2026-11-20

package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/falkcorp/mediacore/internal/authz"
	"github.com/falkcorp/mediacore/rbac"
)

// TestProblem_DeniedErrorMapping (05-http-api §3.3, 05-rbac §3.4): the denial
// rbac.Require returns, reaching the error writer as it is or wrapped, is a
// 403 permission_denied naming the refused scope as required_permission. A
// denial a handler already wrapped in its own *Error keeps that body
// (rbachttp's mapError), and a bare rbac.ErrDenied is not the typed denial
// and stays a 500 with its text withheld, as before.
func TestProblem_DeniedErrorMapping(t *testing.T) {
	bare := context.Background()
	cases := map[string]struct {
		err      error
		status   int
		code     string
		required any // nil: absent
		detail   any // nil: absent
	}{
		"Require's denial names the scope": {rbac.Require(bare, "batcave:write"), 403, "permission_denied", "batcave:write", nil},
		"a wrapped denial still maps":      {fmt.Errorf("gadget: %w", rbac.Require(bare, "batcave:write")), 403, "permission_denied", "batcave:write", nil},
		"RequireRequirement names the first unheld scope": {
			rbac.RequireRequirement(bare, rbac.AllOf("batcave:write", "batcave:read")), 403, "permission_denied", "batcave:write", nil,
		},
		"a denial naming no scope has no required_permission": {rbac.RequireAll(bare), 403, "permission_denied", nil, nil},
		"a handler's own *Error around a denial keeps its body": {
			&Error{Status: http.StatusForbidden, Code: "permission_denied", Detail: "permission denied", Err: rbac.Require(bare, "batcave:write")},
			403, "permission_denied", nil, "permission denied",
		},
		"a bare ErrDenied is still internal": {rbac.ErrDenied, 500, "internal", nil, nil},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			r, g := newRouter(t, "fail")
			Handle(g, Route{Method: http.MethodGet, Path: "/fail", Name: "fail", Perm: rbac.Public},
				func(context.Context, *In[struct{}]) (Gadget, error) { return Gadget{}, tc.err })
			rec := serve(mustBuild(t, r), http.MethodGet, "/api/batcave/v1/fail", "")
			var p map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
				t.Fatalf("body %q: %v", rec.Body.String(), err)
			}
			if rec.Code != tc.status || p["code"] != tc.code {
				t.Fatalf("got %d %v, want %d %s", rec.Code, p, tc.status, tc.code)
			}
			if p["required_permission"] != tc.required {
				t.Errorf("required_permission = %v, want %v", p["required_permission"], tc.required)
			}
			if p["detail"] != tc.detail {
				t.Errorf("detail = %v, want %v", p["detail"], tc.detail)
			}
		})
	}
}

// TestRequireRequirement_AuthDisabledJudgesTheWholeAllOf (05-rbac §4.11,
// 05-http-api §8.3): the legacy mount's adapters check a route-shaped AllOf
// with RequireRequirement, because the auth-disabled checker drops the
// AuthRequired members of an AllOf and requires the rest, which a
// scope-by-scope RequireAll cannot do. An AllOf made only of AuthRequired
// scopes is still refused. (It lives here, not in rbac's own tests, because
// rbac's tests may not import authz: archtest.NoSystemPrincipalOutsideKernel.)
func TestRequireRequirement_AuthDisabledJudgesTheWholeAllOf(t *testing.T) {
	cat, err := rbac.NewCatalog(append(rbac.CoreScopes(), rbac.PermissionSpec{Name: "batcave:write"})...)
	if err != nil {
		t.Fatal(err)
	}
	ctx := rbac.WithChecker(context.Background(), authz.AuthDisabled(cat))
	mixed := rbac.AllOf("core:system.manage", "batcave:write")
	if err := rbac.RequireRequirement(ctx, mixed); err != nil {
		t.Errorf("the auth-disabled checker refuses %s on its non-AuthRequired member: %v", mixed, err)
	}
	if rbac.RequireAll(ctx, mixed.Permissions()...) == nil {
		t.Error("RequireAll admits an AuthRequired scope to the auth-disabled checker; the cell no longer shows why RequireRequirement exists")
	}
	if err := rbac.RequireRequirement(ctx, rbac.Need("core:system.manage")); !errors.Is(err, rbac.ErrDenied) {
		t.Errorf("an all-AuthRequired requirement is not refused to the auth-disabled checker: %v", err)
	}
}
