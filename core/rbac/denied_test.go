// file: core/rbac/denied_test.go
// version: 0.1.0
// guid: 9d4c1e7a-2b85-4f60-8a3d-6e0f5b2c7d19
// last-edited: 2026-11-20

package rbac_test

import (
	"context"
	"errors"
	"testing"

	"github.com/falkcorp/mediacore/rbac"
)

// deniedOf asserts err is a *rbac.DeniedError that matches rbac.ErrDenied,
// names scope and, when text is not "", reads text.
func deniedOf(t *testing.T, what string, err error, scope rbac.Permission, text string) {
	t.Helper()
	var d *rbac.DeniedError
	if !errors.As(err, &d) {
		t.Fatalf("%s: err = %v (%T), want a *rbac.DeniedError", what, err, err)
	}
	if !errors.Is(err, rbac.ErrDenied) {
		t.Errorf("%s: errors.Is(err, ErrDenied) = false", what)
	}
	if d.Permission != scope {
		t.Errorf("%s: Permission = %q, want %q", what, d.Permission, scope)
	}
	if text != "" && err.Error() != text {
		t.Errorf("%s: text = %q, want %q", what, err.Error(), text)
	}
}

// TestDeniedError_RequireKeepsItsText (§3.4): Require and RequireAll return
// the typed denial, which still matches ErrDenied and reads exactly as their
// wrapped ErrDenied did, so no caller of either sees a change beyond the
// scope it can now read.
func TestDeniedError_RequireKeepsItsText(t *testing.T) {
	f := boot(t, newEnv(t), setup{modules: []module{gotham()}})
	bare := context.Background()
	deniedOf(t, "Require without a checker", rbac.Require(bare, "gotham:signal"), "gotham:signal",
		"rbac: permission denied: gotham:signal")
	deniedOf(t, "RequireAll()", rbac.RequireAll(bare), "", "rbac: permission denied: no valid permission named")
	deniedOf(t, "RequireAll(malformed)", rbac.RequireAll(bare, "gotham:Signal"), "", "rbac: permission denied: no valid permission named")
	ctx := rbac.WithChecker(bare, f.as("gotham:signal"))
	deniedOf(t, "RequireAll(held, unheld)", rbac.RequireAll(ctx, "gotham:signal", "gotham:settings.manage"), "gotham:settings.manage",
		"rbac: permission denied: gotham:settings.manage")
}

// TestRequireRequirement_DeniesByDefault (§3.4, COR6-1): a requirement is
// checked only when it is an AllOf, AnyOf or AnyAuthenticated and a bound
// checker Satisfies it. Every other kind is refused even by a checker that
// satisfies everything, no checker is refused, and a bound service records
// each refusal.
func TestRequireRequirement_DeniesByDefault(t *testing.T) {
	f := boot(t, newEnv(t), setup{modules: []module{gotham()}})
	bare := context.Background()
	lax := rbac.WithChecker(bare, halfChecker{has: true, satisfies: true})

	for name, r := range map[string]rbac.Requirement{
		"AllOf":            rbac.AllOf("gotham:signal", "gotham:patrol"),
		"AnyOf":            rbac.AnyOf("gotham:signal", "gotham:patrol"),
		"AnyAuthenticated": rbac.AnyAuthenticated,
	} {
		if err := rbac.RequireRequirement(lax, r); err != nil {
			t.Errorf("%s under a checker that satisfies it: %v", name, err)
		}
		deniedOf(t, name+" without a checker", rbac.RequireRequirement(bare, r), firstOf(r), "")
		deniedOf(t, name+" under a checker that satisfies nothing",
			rbac.RequireRequirement(rbac.WithChecker(bare, halfChecker{has: true}), r), firstOf(r), "")
	}
	for name, r := range map[string]rbac.Requirement{
		"Public":      rbac.Public,
		"Delegated":   rbac.Delegated("gotham relay"),
		"LegacyAtom":  rbac.LegacyAtom("library.view"),
		"LegacyCheck": rbac.LegacyCheck("users.manage", true),
		"zero":        {},
	} {
		deniedOf(t, name+" under a checker that satisfies everything", rbac.RequireRequirement(lax, r), "", "")
	}

	ctx := f.svc.Bind(bare, f.as("gotham:signal", "gotham:patrol"))
	if err := rbac.RequireRequirement(ctx, rbac.AllOf("gotham:signal", "gotham:patrol")); err != nil {
		t.Errorf("a held AllOf is refused: %v", err)
	}
	if err := rbac.RequireRequirement(ctx, rbac.AnyOf("gotham:settings.manage", "gotham:patrol")); err != nil {
		t.Errorf("an AnyOf with one held scope is refused: %v", err)
	}
	before := len(f.audit.denials())
	deniedOf(t, "AllOf(held, unheld)", rbac.RequireRequirement(ctx, rbac.AllOf("gotham:signal", "gotham:settings.manage")),
		"gotham:settings.manage", "rbac: permission denied: all(gotham:signal,gotham:settings.manage)")
	d := f.audit.denials()
	if len(d) != before+1 || d[len(d)-1].Action != "core.permission.deny" || d[len(d)-1].Permission != "gotham:settings.manage" {
		t.Errorf("denials = %+v, want one more core.permission.deny of gotham:settings.manage", d)
	}
}

// firstOf is the scope a refusal of r names when no checker holds any of it.
func firstOf(r rbac.Requirement) rbac.Permission {
	if ps := r.Permissions(); len(ps) > 0 {
		return ps[0]
	}
	return ""
}
