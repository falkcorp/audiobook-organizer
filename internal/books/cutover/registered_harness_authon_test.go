// file: internal/books/cutover/registered_harness_authon_test.go
// version: 0.1.0
// guid: ea238866-f649-457a-9381-c93f83cc54d5
// last-edited: 2026-11-22

package cutover_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// authModeState is core.auth.mode as the settings route reports it.
func authModeState(t *testing.T, h *Registered, p *Principal) (effective, source, lockedBy string) {
	t.Helper()
	var all map[string]struct {
		Effective any    `json:"effective"`
		Source    string `json:"source"`
		LockedBy  string `json:"locked_by"`
	}
	h.coreJSON(p, http.MethodGet, "/api/core/v1/settings", nil, &all)
	m, ok := all["core.auth.mode"]
	if !ok {
		t.Fatal("the settings route does not report core.auth.mode")
	}
	return fmt.Sprint(m.Effective), m.Source, m.LockedBy
}

// TestRegisteredHarness_AuthOnByTheOperatorOverAStoredOff is SEC6-1's operator path
// on the flip (05-authn §4.11 "Who may switch auth"): an install that has auth off
// (its stored settings say enable_auth false, no operator value) is turned on the way
// the documented release note says, ABO_CORE_AUTH_MODE=required and a restart over
// the same store, and the stored false does not keep it off. The reason it matters:
// with auth off the credential routes are refused (SEC5-8, a recorded tightening),
// and Settings cannot turn auth on, so the operator's value is the only way out; if it
// did not take effect, the tightening would lock an auth-off install out of its own
// credential routes.
//
// Boot one is the auth-off install (auth_disabled asserted by the boot), and a
// credential route is refused there. Boot two is over the same store with the
// operator's value: auth is on and locked by the environment, nobody is admitted
// without a credential, an administrator signs in with a password (a session: the
// interactive method) and the same route now reaches HEAD's handler, which creates
// the import path; a viewer is refused by the role.
func TestRegisteredHarness_AuthOnByTheOperatorOverAStoredOff(t *testing.T) {
	dir := t.TempDir()
	body := map[string]any{"path": dir, "name": "Gotham Archive", "enabled": false}
	const route = "/api/v1/import-paths"

	off := bootRegistered(t, registeredConfig{AuthOff: true, StoredAuthOff: true})
	if eff, src, lock := authModeState(t, off, off.Principal(Authoff)); eff != "disabled" || src == "env" || lock != "" {
		t.Errorf("the auth-off install's core.auth.mode is %q from %q (locked by %q): want disabled, from the stored settings", eff, src, lock)
	}
	res, err := off.Do(off.Principal(Authoff), http.MethodPost, route, body)
	if err != nil {
		t.Fatal(err)
	}
	off.Refused(res, http.StatusForbidden, "interactive_required")
	off.Stop()

	on := bootRegistered(t, registeredConfig{Over: off, AuthMode: "required"})
	admin := on.Principal("session:admin")
	var st struct {
		Disabled      bool `json:"auth_disabled"`
		HasUsers      bool `json:"has_users"`
		SetupRequired bool `json:"setup_required"`
	}
	on.coreJSON(admin, http.MethodGet, "/api/core/v1/auth/status", nil, &st)
	if st.Disabled || !st.HasUsers || st.SetupRequired {
		t.Errorf("auth status after the operator value: %+v, want auth on over the same users", st)
	}
	if eff, src, lock := authModeState(t, on, admin); eff != "required" || src != "env" || !strings.Contains(lock, "ABO_CORE_AUTH_MODE") {
		t.Errorf("core.auth.mode is %q from %q (locked by %q): want required, from the environment, locked by it", eff, src, lock)
	}
	anon, err := on.Do(on.Principal(Anonymous), http.MethodGet, "/api/v1/audiobooks", nil)
	if err != nil {
		t.Fatal(err)
	}
	on.Refused(anon, http.StatusUnauthorized, "authentication_required")

	var me struct {
		Method string `json:"method"`
	}
	on.coreJSON(admin, http.MethodGet, "/api/core/v1/me", nil, &me)
	if me.Method != "session" {
		t.Errorf("the administrator's credential method is %q, want an interactive session", me.Method)
	}
	res, err = on.Do(admin, http.MethodPost, route, body)
	if err != nil {
		t.Fatal(err)
	}
	var made struct {
		Data struct {
			ImportPath struct {
				Path string `json:"path"`
				Name string `json:"name"`
			} `json:"import_path"`
		} `json:"data"`
	}
	if err := json.Unmarshal(res.Body, &made); err != nil || res.Status != http.StatusCreated || made.Data.ImportPath.Path != dir || made.Data.ImportPath.Name != "Gotham Archive" {
		t.Errorf("POST %s as the administrator after auth is on: %d %s, want HEAD's 201 with the import path", route, res.Status, res.excerpt())
	}
	viewer, err := on.Do(on.Principal("session:viewer"), http.MethodPost, route, body)
	if err != nil {
		t.Fatal(err)
	}
	on.Refused(viewer, http.StatusForbidden, "permission_denied")
}
