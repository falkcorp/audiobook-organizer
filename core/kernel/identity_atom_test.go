// file: core/kernel/identity_atom_test.go
// version: 0.1.0
// guid: 7e2a9c15-4d36-4b80-a1f7-c85b30d6e942
// last-edited: 2026-11-21

package kernel

import (
	"net/http"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/rbac"
)

// Batman names only.

// atomPath is the legacy route whose requirement is the atom "library.view".
const atomPath = "/api/v1/atom"

// atomConfig is a boot whose module mounts one legacy route, GET atomPath, that
// requires a legacy atom: stage 13 can only judge it through Config.ResolveAtom.
// A native route cannot carry an atom (the registration refuses it), so the
// mount is the only way in.
func atomConfig(t *testing.T, resolve httpapi.AtomResolver) (*swapHost, Config) {
	t.Helper()
	h, c, _ := mountConfig(t, true)
	c.ResolveAtom = resolve
	c.Modules = []module.Module{storageMod("alfred", func(r *module.Registrar) error {
		r.MountLegacy(httpapi.LegacyMount{
			Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("served")) }),
			Auth:    httpapi.LegacyAuthCore,
			Routes: func(yield func(httpapi.LegacyRoute) bool) {
				yield(httpapi.LegacyRoute{Method: http.MethodGet, GinPath: atomPath, Perm: rbac.LegacyAtom("library.view"), RateClass: httpapi.RateExempt})
			},
		})
		return nil
	}, seedStep(1, "cave")).with(func(m *module.Manifest) {
		m.LegacyPrefixes = []string{"/api/v1/"}
		m.Permissions = []rbac.PermissionSpec{{Name: "alfred:signal", Title: "Signal"}}
		m.Roles = []rbac.RoleSeed{{Name: "relay-worker", Title: "Relay worker", Permissions: []rbac.Permission{"alfred:signal"}}}
		m.ImportedRoleIDs = []rbac.RoleName{"relay-worker"}
	})}
	return h, c
}

// TestIdentityMount_ResolveAtomReachesStage13: Config.ResolveAtom is stage 13's
// atom table. With one that maps the atom onto alfred:signal, the holder of
// that scope is served and a session without it is refused; with none the
// holder is refused too, because an atom nothing resolves is never granted.
func TestIdentityMount_ResolveAtomReachesStage13(t *testing.T) {
	var asked []string
	resolver := func(atom string, readSide bool) (rbac.Requirement, bool) {
		asked = append(asked, atom)
		if atom == "library.view" && !readSide {
			return rbac.Need("alfred:signal"), true
		}
		return rbac.Requirement{}, false
	}
	boot := func(t *testing.T, resolve httpapi.AtomResolver) *mountFx {
		t.Helper()
		h, c := atomConfig(t, resolve)
		k := h.mustBoot(c)
		return &mountFx{k: k, h: k.Handler(), relay: atomPath}
	}

	t.Run("resolver set", func(t *testing.T) {
		fx := boot(t, resolver)
		_, holder := fx.user(t, "u-lucius", "relay-worker")
		_, nobody := fx.user(t, "u-selina")
		if rec := fx.do(http.MethodGet, fx.relay, withCookie(holder)); rec.Code != http.StatusOK {
			t.Fatalf("the holder of the resolved scope = %d %s, want 200", rec.Code, rec.Body)
		}
		if rec := fx.do(http.MethodGet, fx.relay, withCookie(nobody)); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "permission_denied") {
			t.Fatalf("a session without the resolved scope = %d %s, want 403 permission_denied", rec.Code, rec.Body)
		}
		if len(asked) == 0 {
			t.Error("stage 13 never asked the app's resolver")
		}
	})
	t.Run("resolver nil refuses the holder", func(t *testing.T) {
		fx := boot(t, nil)
		_, holder := fx.user(t, "u-lucius", "relay-worker")
		if rec := fx.do(http.MethodGet, fx.relay, withCookie(holder)); rec.Code != http.StatusForbidden {
			t.Fatalf("a legacy atom with no resolver = %d %s, want 403", rec.Code, rec.Body)
		}
	})
}
