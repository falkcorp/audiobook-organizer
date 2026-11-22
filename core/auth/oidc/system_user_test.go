// file: core/auth/oidc/system_user_test.go
// version: 0.1.0
// guid: 9a52c7d1-6e3b-4f80-84d9-0b1f7a3e5c62
// last-edited: 2026-11-22

package oidc

import (
	"testing"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"
)

// TestOIDC_SystemUserIsNeverResolved: the Cloudflare Access extension resolves a
// verified address to a local user through this resolver. The system user
// ("_system", status system, address _system@local) is not admitted, whether the
// identity is linked already or it would be linked by address, and nothing is
// linked or created.
func TestOIDC_SystemUserIsNeverResolved(t *testing.T) {
	pol := Policy{Provider: "gotham", AllowedDomains: []string{"local"}, LinkExistingByEmail: true}
	system := func() *auth.User {
		return &auth.User{ID: auth.SystemUserID, Username: "_system", Email: "_system@local", EmailVerified: true,
			Status: auth.StatusSystem, Roles: []rbac.RoleName{rbac.RoleViewer}}
	}
	t.Run("by address", func(t *testing.T) {
		f := newFx(t)
		f.put(system())
		_, err := resolve(t, f, pol, claims("sub-system", "_system@local"))
		mustIs(t, err, auth.ErrInactiveUser)
		if _, err := f.identity("gotham", "sub-system"); err == nil {
			t.Fatal("an identity was linked to the system user")
		}
		if n := f.countUsers(); n != 1 {
			t.Fatalf("users = %d, want 1: the refusal created an account", n)
		}
	})
	t.Run("by an identity linked while the user was active", func(t *testing.T) {
		f := newFx(t)
		active := system()
		active.Status = auth.StatusActive
		f.put(active)
		linked, err := resolve(t, f, pol, claims("sub-system", "_system@local"))
		if err != nil || linked.ID != auth.SystemUserID {
			t.Fatalf("linking while active: %v, %v", linked, err)
		}
		u := f.user(string(auth.SystemUserID))
		u.Status, u.Version = auth.StatusSystem, u.Version+1
		f.put(u)
		_, err = resolve(t, f, pol, claims("sub-system", "_system@local"))
		mustIs(t, err, auth.ErrInactiveUser)
	})
}
