// file: internal/books/cutover/registered_harness_canary_test.go
// version: 0.1.0
// guid: 5b0c8e42-7d19-4a63-9f58-c1e3a6d70b24
// last-edited: 2026-11-22

package cutover_test

import (
	"encoding/json"
	"net/http"
	"testing"
)

// knownDifference is one cell where the flipped build answers differently from R0
// and the coordinator accepted it.
type knownDifference struct {
	Method, Route string
	Principal     string
	Head, Flip    int
	// Code is the problem code the flipped build answers with.
	Code string
	// Why names the requirement that refuses and the layer that applies it.
	Why string
}

// knownDifferences is the only place the registered path may differ from R0's
// recorded answers on the canary route. Every other cell holds strict HEAD parity.
//
// Coordinator ruling 2026-11-22: a key of the fp-worker user answers 403 on
// /api/v1/auth/me whatever scope it carries, where R0 answered 200 for a key whose
// scope lay outside the worker API. It narrows access (a 200 became a 403), the
// production worker never calls the route (it uses /api/v1/fingerprint/worker), and
// the ruling is to accept it.
//
// The route's requirement is Authenticated (KindAuthenticated), not an atom: stage
// 13 refuses it through the principal's checker, because every credential of the
// fp-worker user is worker-confined (rbac.Confined: the role holds only Confining
// scopes), a confined principal gets no ambient scope (core:self), and a confined
// checker does not satisfy Authenticated. R0 judged the same key by its own scope.
var knownDifferences = []knownDifference{
	{"GET", "/api/v1/auth/me", "key:fp-worker:view", 200, 403, "permission_denied", "Authenticated, refused by the worker-confined checker (no core:self)"},
	{"GET", "/api/v1/auth/me", "key:fp-worker:settings", 200, 403, "permission_denied", "Authenticated, refused by the worker-confined checker (no core:self)"},
	{"GET", "/api/v1/auth/me", "key:fp-worker:users", 200, 403, "permission_denied", "Authenticated, refused by the worker-confined checker (no core:self)"},
}

// TestRegisteredHarness_CanaryMatchesR0 is the canary R0 recorded for each
// principal (GET /api/v1/auth/me, or /api/v1/auth/status for auth-off), asked of the
// flipped build. The set of differences is pinned exactly: a fourth difference, a
// difference that vanishes, a different status or code, and any cell where the
// flipped build admits what R0 refused all fail.
func TestRegisteredHarness_CanaryMatchesR0(t *testing.T) {
	_, canary := loadPrincipalTable(t)
	diff := map[string]knownDifference{}
	for _, d := range knownDifferences {
		diff[d.Principal] = d
	}
	seen := map[string]bool{}

	check := func(t *testing.T, h *Registered, p *Principal, route string) {
		t.Helper()
		want, ok := canary[p.Name]
		if !ok {
			t.Fatalf("verdicts.json holds no canary for %s", p.Name)
		}
		res := h.MustDo(t, p, http.MethodGet, route, nil)
		if d, known := diff[p.Name]; known {
			seen[p.Name] = true
			if want != d.Head || res.Status != d.Flip || res.Code != d.Code {
				t.Errorf("%s %s: R0 %d, flip %d %q; the known difference says R0 %d, flip %d %q", p.Name, route, want, res.Status, res.Code, d.Head, d.Flip, d.Code)
			}
			return
		}
		switch {
		case res.Status == want:
		case want >= 400 && res.Status < 400:
			t.Errorf("WIDENING: %s %s: R0 refused with %d, the flipped build answers %d", p.Name, route, want, res.Status)
		default:
			t.Errorf("%s %s: R0 %d, flip %d (%s): not a known difference", p.Name, route, want, res.Status, res.excerpt())
		}
	}

	// One boot per (sub)test: the process defaults a boot installs are restored
	// when the test that booted ends.
	t.Run("auth on", func(t *testing.T) {
		h := bootRegistered(t, registeredConfig{})
		n := 0
		for _, p := range h.Principals {
			check(t, h, p, "/api/v1/auth/me")
			n++
		}
		if n != 36 {
			t.Fatalf("%d principals checked, want 36", n)
		}
		for _, d := range knownDifferences {
			if !seen[d.Principal] {
				t.Errorf("known difference for %s was never exercised", d.Principal)
			}
		}
	})
	t.Run("auth off", func(t *testing.T) {
		off := bootRegistered(t, registeredConfig{AuthOff: true})
		check(t, off, off.Principal(Authoff), "/api/v1/auth/status")
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
		return p.Role == "fp-worker" || p.Name == "key:admin:worker"
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
				if res.Stage13 != Stage13Refused || res.Status != http.StatusUnauthorized {
					t.Errorf("anonymous: %d %s", res.Status, res.excerpt())
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
		res := off.MustDo(t, off.Principal(Authoff), http.MethodGet, route, nil)
		t.Logf("EXPOFF auth-off GET %s: %d %s", route, res.Status, res.excerpt())
	})
}
