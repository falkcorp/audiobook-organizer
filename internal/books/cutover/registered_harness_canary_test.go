// file: internal/books/cutover/registered_harness_canary_test.go
// version: 0.1.0
// guid: 5b0c8e42-7d19-4a63-9f58-c1e3a6d70b24
// last-edited: 2026-11-22

package cutover_test

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
)

// TestRegisteredHarness_CanaryMatchesR0 pins what bootRegistered enforces: every
// principal's canary (GET /api/v1/auth/me; /api/v1/auth/status for auth-off) is what
// R0 recorded, and the only cells that differ are the ones knownDifferences names. The
// boot fails on any other difference, on a cell the flipped build admits where R0
// refused, and on a named difference that does not occur, so reaching the assertions
// below already proves the rest.
func TestRegisteredHarness_CanaryMatchesR0(t *testing.T) {
	var want []string
	for _, d := range knownDifferences {
		want = append(want, d.Principal)
	}
	t.Run("auth on", func(t *testing.T) {
		h := bootRegistered(t, registeredConfig{})
		if !slices.Equal(h.KnownDifferences, want) {
			t.Errorf("known differences applied %v, want %v", h.KnownDifferences, want)
		}
		if len(h.Principals) != 36+18 {
			t.Errorf("%d principals", len(h.Principals))
		}
	})
	t.Run("auth off", func(t *testing.T) {
		h := bootRegistered(t, registeredConfig{AuthOff: true})
		if len(h.KnownDifferences) != 0 {
			t.Errorf("auth-off applied known differences %v", h.KnownDifferences)
		}
		// requireAuthMode already made the boot report auth_disabled; the principal
		// is admitted on a route that needs one.
		if res := h.MustDo(t, h.Principal(Authoff), http.MethodGet, "/api/v1/audiobooks", nil); res.Status != http.StatusOK {
			t.Errorf("authoff GET /api/v1/audiobooks: %d %s", res.Status, res.excerpt())
		}
	})
}

// TestRegisteredHarness_CredentialKindOnCoreMe pins the credential kind core
// reports for each principal on /api/core/v1/me ("method"): a signed-in session is
// "session", a key "api_key", the Access owner "ext:cfaccess", and the anonymous
// caller is refused by stage 13. The fp-worker credentials are confined and are
// refused there (the known difference above); every other principal answers.
func TestRegisteredHarness_CredentialKindOnCoreMe(t *testing.T) {
	const route = "/api/core/v1/me"
	kindOf := func(res Result) (method string, ownerVerified bool) {
		t.Helper()
		var me struct {
			Method        string `json:"method"`
			OwnerVerified bool   `json:"owner_verified"`
		}
		if err := json.Unmarshal(res.Body, &me); err != nil {
			t.Fatalf("%d %s: %v", res.Status, res.excerpt(), err)
		}
		return me.Method, me.OwnerVerified
	}
	confined := func(p *Principal) bool {
		return p.Role == "fp-worker" || p.Recorded() == "key:admin:worker"
	}

	t.Run("auth on", func(t *testing.T) {
		h := bootRegistered(t, registeredConfig{})
		for _, p := range h.Principals {
			res, err := h.Do(p, http.MethodGet, route, nil)
			if err != nil {
				t.Fatalf("%s: %v", p.Name, err)
			}
			switch {
			case p.Kind == "anonymous":
				if res.Stage13 != Stage13Refused {
					t.Errorf("anonymous was not refused by stage 13: %d %s", res.Status, res.excerpt())
				}
				h.Refused(res, http.StatusUnauthorized, "authentication_required")
			case p.Core && (p.Twin == "key:admin:every" || p.Twin == "key:admin:empty"):
				// Their scopes hold core:admin, which carries core:self.
				if got, _ := kindOf(res); res.Status != http.StatusOK || got != "api_key" {
					t.Errorf("%s: %d method %q, want 200 api_key", p.Name, res.Status, got)
				}
			case p.Core:
				// A core-native key cannot carry core:self (an ambient scope no creator
				// holds to give), and /me needs it: the problem document names it. This is
				// how core behaves today, pinned so a change to it is seen.
				if res.Status != http.StatusForbidden || res.Code != "permission_denied" || !strings.Contains(string(res.Body), "core:self") {
					t.Errorf("%s: want 403 permission_denied naming core:self, got %d %s", p.Name, res.Status, res.excerpt())
				}
			case confined(p):
				if res.Status != http.StatusForbidden || res.Code != "permission_denied" {
					t.Errorf("%s is worker-confined and must be refused: %d %s", p.Name, res.Status, res.excerpt())
				}
			default:
				want := map[string]string{"session": "session", "key": "api_key"}[p.Kind]
				if got, _ := kindOf(res); res.Status != http.StatusOK || got != want {
					t.Errorf("%s: %d method %q, want 200 %q", p.Name, res.Status, got, want)
				}
			}
		}

	})
	t.Run("owner", func(t *testing.T) {
		owner := bootRegistered(t, registeredConfig{Owner: true})
		got, verified := kindOf(owner.MustDo(t, owner.Principal(Owner), http.MethodGet, route, nil))
		if got != "ext:cfaccess" || !verified {
			t.Errorf("owner: method %q owner_verified %v, want ext:cfaccess true", got, verified)
		}
	})
	t.Run("auth off", func(t *testing.T) {
		off := bootRegistered(t, registeredConfig{AuthOff: true})
		// /me describes a user and auth-off has none: the route needs a user and
		// answers the auth-off principal 403 interactive_required (F4-08 ruling
		// G5(a), B10sr; it was 401 authentication_required, which ends a no-auth
		// install's SPA session).
		res := off.MustDo(t, off.Principal(Authoff), http.MethodGet, route, nil)
		if res.Status != http.StatusForbidden || res.Code != "interactive_required" {
			t.Errorf("auth-off GET %s: %d %s", route, res.Status, res.excerpt())
		}
	})
}
