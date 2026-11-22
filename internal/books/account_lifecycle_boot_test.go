// file: internal/books/account_lifecycle_boot_test.go
// version: 0.1.0
// guid: b5e91c40-3d76-4a28-8f0e-61a7d4c92b13
// last-edited: 2026-11-22

package books

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	coreauth "github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/module"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
)

// Books' ONE auth.Lifecycle drives both concerns, on production wiring (F4-08
// B10r + B10s). The acceptance test boots the composed server with the flip on:
// the real books Register, the real kernel, no wrapper module and no registrant of
// the tests' own. A guest accepts a mailed invitation (the sync puts their address
// on the Cloudflare guest policy and the registered lifecycle offers the phone-app
// key), claims the key, and is then disabled: the key is revoked and the address
// leaves the policy, from one core event.
//
// Cloudflare is faked below both clients at http.DefaultTransport: the service
// token API and the token policy (cloudflareFake, B10s) and the guests' e-mail
// policy (gpCF), told apart by policy id.

const gpRobin = "robin@wayne.example"

// gpRouter sends the guests' e-mail policy to gpCF and everything else to the
// B10s fake of the service-token API.
type gpRouter struct {
	tokens *cloudflareFake
	policy *gpCF
}

func (g gpRouter) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Host == "api.cloudflare.com" && strings.Contains(r.URL.Path, "/access/policies/"+claimGuestsPolicyID) {
		rec := httptest.NewRecorder()
		g.policy.ServeHTTP(rec, r)
		res := rec.Result()
		res.Request = r
		res.Body = io.NopCloser(strings.NewReader(rec.Body.String()))
		return res, nil
	}
	return g.tokens.RoundTrip(r)
}

// gpInstallRouter puts the router in front of the B10s fake (installed by the boot).
func gpInstallRouter(t *testing.T, tokens *cloudflareFake, policy *gpCF) {
	t.Helper()
	orig := http.DefaultTransport
	http.DefaultTransport = gpRouter{tokens: tokens, policy: policy}
	t.Cleanup(func() { http.DefaultTransport = orig })
}

// tokenRuleHeld reports whether the service-auth policy still lists the guest's
// service token, the rule that lets the key through Cloudflare Access.
func (f *cloudflareFake) tokenRuleHeld() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	raw, _ := json.Marshal(f.include)
	return strings.Contains(string(raw), claimTokenID)
}

// lifecycleScenario is one guest from invitation to key, ready to be acted on.
type lifecycleScenario struct {
	r      *runningApp
	cf     *cloudflareFake
	policy *gpCF
	admin  *http.Cookie
	guest  *http.Cookie
	id     string
	invite string
}

// bootLifecycleScenario boots with wrap (none for production wiring) and takes a
// guest through a mailed accept and the claim of the phone-app key. Both Cloudflare
// halves are asserted to have happened before anything is revoked, so a revoke
// that never ran cannot read as one that had nothing to do.
func bootLifecycleScenario(t *testing.T, wrap ...func(module.Module) module.Module) *lifecycleScenario {
	t.Helper()
	r, cf, smtp := bootGuestKeyMailed(t, wrap...)
	// The policy never goes empty (the client refuses to remove its only rule):
	// one address put there by hand stays.
	policy := newGPCFFor(claimGuestsPolicyID, "jim.gordon@wayne.example")
	gpInstallRouter(t, cf, policy)

	admin := bootstrapAdmin(t, r)
	guest, id, invite := mailedGuest(t, r, admin, smtp, "robin", gpRobin)
	require.Eventually(t, func() bool { return policy.has(gpRobin) }, 10*time.Second, 20*time.Millisecond,
		"the accept did not put the address on the guest policy")
	require.Equal(t, []string{gpRobin}, policy.additions())

	w := claimKey(t, r, guest)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, 1, cf.minted())
	require.Equal(t, database.GuestTokenActive, tokenRow(t, r, id).Status)
	require.True(t, cf.tokenRuleHeld(), "the key's rule is not in the token policy")
	return &lifecycleScenario{r: r, cf: cf, policy: policy, admin: admin, guest: guest, id: id, invite: invite}
}

// assertBothConcernsEnded is what one event must have done to both halves.
func (s *lifecycleScenario) assertBothConcernsEnded(t *testing.T, reason string) {
	t.Helper()
	row := waitRevoked(t, s.r, s.id)
	assert.Equal(t, reason, row.RevokeReason)
	assert.Equal(t, 1, s.cf.revoked(), "the service token was not deleted at Cloudflare: the key still works")
	assert.False(t, s.cf.tokenRuleHeld(), "the key's rule is still in the token policy")
	require.Eventually(t, func() bool { return !s.policy.has(gpRobin) }, 10*time.Second, 20*time.Millisecond,
		"the guest's address is still on the Cloudflare guest policy")
	assert.Equal(t, []string{gpRobin}, s.policy.removals())
	assert.True(t, s.policy.has("jim.gordon@wayne.example"), "an address added by hand was removed")
}

// ACCEPTANCE: production wiring, one boot. Disabling a guest who holds a claimed
// phone-app key revokes the key (Cloudflare deletes the service token, so the
// key is refused on its next request) and removes the guest's address from the
// guest policy.
func TestAccountLifecycle_ProductionWiring_DisablingAGuestRevokesTheKeyAndTheCloudflareEntry(t *testing.T) {
	s := bootLifecycleScenario(t)
	adminPost(t, s.r, s.admin, "/api/core/v1/users/"+s.id+"/disable")
	s.assertBothConcernsEnded(t, "user deactivated")

	// The disabled guest's own sign-in is core's to refuse too.
	rec := doJSON(t, s.r.app.Handler(), http.MethodGet, "/api/core/v1/me", nil, s.guest)
	assert.Equal(t, http.StatusUnauthorized, rec.Code, rec.Body.String())
}

// GUARD: one registration, and it is the one that drives both concerns. A second
// RegisterLifecycle after books' own is refused (core allows one, and exposes no
// count: the refusal IS the count), and one core event (the disable) reaches both
// the guest-policy revoke and the guest-key revoke through it. A later registrant
// that came first would make books' own Register refuse the boot (TestAccountLifecycle_*).
func TestAccountLifecycle_OneRegistrationDrivesBothConcerns(t *testing.T) {
	tap := &gpTap{}
	s := bootLifecycleScenario(t, func(m module.Module) module.Module { return gpTapModule{Module: m, tap: tap} })
	tap.mu.Lock()
	again, handle := tap.again, tap.lifecycle
	tap.mu.Unlock()
	require.NotNil(t, handle, "core handed no lifecycle handle")
	require.ErrorIs(t, again, coreauth.ErrLifecycleRegistered,
		"a second registrant was accepted: books' lifecycle is not the only one")

	adminPost(t, s.r, s.admin, "/api/core/v1/users/"+s.id+"/disable")
	s.assertBothConcernsEnded(t, "user deactivated")
}

// Core's delete is the same on both halves: neither the key nor the entry
// outlives the account.
func TestAccountLifecycle_DeletingAGuestEndsBothConcerns(t *testing.T) {
	s := bootLifecycleScenario(t)
	rec := doJSON(t, s.r.app.Handler(), http.MethodDelete, "/api/core/v1/users/"+s.id, nil, s.admin)
	require.Less(t, rec.Code, 300, rec.Body.String())
	s.assertBothConcernsEnded(t, "user deleted")
}

// A guest merged into another account loses both. HEAD kept the key of a
// merged-away account; this only takes access away and is a known difference,
// pinned here.
func TestAccountLifecycle_MergedAwayGuestEndsBothConcerns(t *testing.T) {
	s := bootLifecycleScenario(t)
	h := s.r.app.Handler()
	// The survivor has an address of its own, so the merge drops the source's.
	// (A survivor with none adopts the source's verified address, and the entry
	// then stays: an active verified holder still has it.)
	me := doJSON(t, h, http.MethodPatch, "/api/core/v1/me", map[string]string{"email": "bruce.wayne@example.test"}, s.admin)
	require.Equal(t, http.StatusOK, me.Code, me.Body.String())
	survivor := s.survivorID(t)

	path := "/api/v1/users/" + s.id + "/merge-into/" + survivor
	var plan struct {
		Digest string `json:"digest"`
		Data   struct {
			Digest string `json:"digest"`
		} `json:"data"`
	}
	pv := gpDoJSON(t, h, http.MethodPost, path+"/preview", nil, s.admin, &plan)
	require.Equal(t, http.StatusOK, pv.Code, pv.Body.String())
	ap := doJSON(t, h, http.MethodPost, path+"/apply", map[string]any{"digest": plan.Digest + plan.Data.Digest}, s.admin)
	require.Equal(t, http.StatusAccepted, ap.Code, ap.Body.String())
	um := s.r.srv.UserMerge()
	require.NotNil(t, um)
	require.Eventually(t, func() bool {
		js, err := um.ListJournals()
		return err == nil && len(js) == 1 && js[0].Status == "applied"
	}, 60*time.Second, 100*time.Millisecond, "the merge never applied")

	s.assertBothConcernsEnded(t, "user merged away")
}

// survivorID is the signed-in admin's id, read from core.
func (s *lifecycleScenario) survivorID(t *testing.T) string {
	t.Helper()
	rec := doJSON(t, s.r.app.Handler(), http.MethodGet, "/api/core/v1/me", nil, s.admin)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var me struct {
		ID   string `json:"id"`
		User struct {
			ID string `json:"id"`
		} `json:"user"`
		Data struct {
			ID   string `json:"id"`
			User struct {
				ID string `json:"id"`
			} `json:"user"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &me), rec.Body.String())
	id := me.ID + me.User.ID + me.Data.ID + me.Data.User.ID
	require.NotEmpty(t, id, rec.Body.String())
	return id
}

// A guest's own address change ends the Cloudflare guest-policy entry of the old
// address and leaves the key alone: HEAD's e-mail routes never touched the key
// (it belongs to the account, not the address).
func TestAccountLifecycle_AddressChangeEndsTheEntryNotTheKey(t *testing.T) {
	s := bootLifecycleScenario(t)
	// An address change needs an interactive sign-in, not the accept's delegated session.
	login := loginAs(t, s.r, "robin", claimGuestPass)
	rec := doJSON(t, s.r.app.Handler(), http.MethodPatch, "/api/core/v1/me", map[string]string{"email": "robin.new@wayne.example"}, login)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Eventually(t, func() bool { return !s.policy.has(gpRobin) }, 10*time.Second, 20*time.Millisecond,
		"the old address is still on the guest policy")
	assert.Equal(t, []string{gpRobin}, s.policy.removals())
	row := tokenRow(t, s.r, s.id)
	require.NotNil(t, row)
	assert.Equal(t, database.GuestTokenActive, row.Status, "an address change revoked the key")
	assert.Zero(t, s.cf.revoked())
	assert.True(t, s.cf.tokenRuleHeld())
}
