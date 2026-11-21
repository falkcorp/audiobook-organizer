// file: internal/server/middleware/absauth_identity_test.go
// version: 0.1.0
// guid: c7e2a490-1b6d-4f38-9a05-d4b8e3c1f927
// last-edited: 2026-11-21

package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	coreauth "github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/cfaccess"
	"github.com/falkcorp/audiobook-organizer/internal/legacyoauth"
	"github.com/falkcorp/audiobook-organizer/internal/server/absauth"
	"github.com/falkcorp/audiobook-organizer/internal/server/legacyauth"
)

// The ABS surface under core identity (F4-08 B10b1). Every test here makes core
// and books' store DISAGREE about the account or the key, so a path that reads
// books' store (the HEAD resolver) gives the opposite answer to the one core
// gives, and only the path that asks core passes.

// coreAccount is core's record of a user: what a core Compat answers from.
type coreAccount struct {
	status string
	epoch  int64
}

// coreKey is core's record of an abk_ key.
type coreKey struct {
	id, user string
	revoked  bool
	confined bool
}

// coreFake is a coreauth.Compat that answers from CORE's state only, with
// core's refusal order (standing, then epoch; a revoked key; a confined key
// outside its scopes at CheckCredentialFor).
type coreFake struct {
	mu       sync.Mutex
	accounts map[string]coreAccount
	keys     map[string]coreKey
	asked    struct{ keys, users int }
	epochs   []int64
}

func newCoreFake() *coreFake {
	return &coreFake{accounts: map[string]coreAccount{}, keys: map[string]coreKey{}}
}

// coreChecker holds the scopes it is given; a confined one satisfies nothing
// outside them (and a Delegated requirement is outside every scope).
type coreChecker struct {
	perms    []rbac.Permission
	confined bool
}

func (c coreChecker) Has(p rbac.Permission) bool { return slices.Contains(c.perms, p) }
func (c coreChecker) Satisfies(r rbac.Requirement) bool {
	if r.Kind() == rbac.KindDelegated {
		return !c.confined
	}
	ps := r.Permissions()
	if len(ps) == 0 {
		return false
	}
	if r.Kind() == rbac.KindAnyOf {
		return slices.ContainsFunc(ps, c.Has)
	}
	for _, p := range ps {
		if !c.Has(p) {
			return false
		}
	}
	return true
}
func (c coreChecker) Confined() bool              { return c.confined }
func (c coreChecker) Concrete() []rbac.Permission { return slices.Clone(c.perms) }

func (f *coreFake) VerifyPassword(context.Context, string, string, coreauth.ClientInfo) (*coreauth.User, error) {
	return nil, coreauth.ErrInvalidCredentials
}

func (f *coreFake) PrincipalFor(_ context.Context, u *coreauth.User, _ []rbac.Permission, epoch int64, _ coreauth.ClientInfo) (*coreauth.Principal, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked.users++
	f.epochs = append(f.epochs, epoch)
	a, ok := f.accounts[string(u.ID)]
	switch {
	case !ok:
		return nil, coreauth.ErrInvalidCredentials
	case a.status != "active":
		return nil, coreauth.ErrInactiveUser
	case a.epoch != epoch:
		return nil, coreauth.ErrStaleEpoch
	}
	return &coreauth.Principal{
		Method: legacyauth.MethodBooksABS,
		User:   &coreauth.User{ID: u.ID, Username: "bruce", Status: coreauth.StatusActive},
		Perms:  coreChecker{perms: []rbac.Permission{"books:view", "core:tasks.view"}},
	}, nil
}

func (f *coreFake) AuthenticateKey(_ context.Context, raw string, _ coreauth.ClientInfo) (*coreauth.Principal, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked.keys++
	k, ok := f.keys[raw]
	switch {
	case !ok:
		return nil, coreauth.ErrInvalidCredentials
	case k.revoked:
		return nil, coreauth.ErrRevoked
	}
	return &coreauth.Principal{
		Method:       legacyauth.MethodBooksABS,
		CredentialID: k.id,
		User:         &coreauth.User{ID: coreauth.UserID(k.user), Username: "bruce", Status: coreauth.StatusActive},
		Perms:        coreChecker{perms: []rbac.Permission{"books:view", "core:tasks.view"}, confined: k.confined},
	}, nil
}

func (f *coreFake) CheckCredentialFor(p *coreauth.Principal, perm rbac.Requirement) error {
	if p.Perms.Confined() && !p.Perms.Satisfies(perm) {
		return coreauth.ErrConfined
	}
	return nil
}

type absAtoms map[string]rbac.Requirement

func (a absAtoms) Requirement(atom string, _ bool) (rbac.Requirement, bool) {
	r, ok := a[atom]
	return r, ok
}
func (a absAtoms) Atoms() []string { return []string{"library.view"} }

// idHarness is an ABS surface under core identity beside a books store the
// tests keep deliberately out of step with core.
type idHarness struct {
	*absHarness
	books *apiKeyStore
	core  *coreFake
}

const idUser = "u-bruce"

func newIdentityHarness(t *testing.T, allowed []string) *idHarness {
	t.Helper()
	gin.SetMode(gin.TestMode)
	cfg, err := absauth.Load(absauth.Settings{Enabled: true, JWTSecret: absTestSecret, AuthModes: "cf,jwt"})
	if err != nil {
		t.Fatalf("absauth.Load: %v", err)
	}
	books := newAPIKeyStore(newFakeABSStore())
	verifier := &fakeCFVerifier{byToken: map[string]*legacyoauth.IdentityClaims{}}
	resolver := NewABSIdentityResolver(cfg, verifier, legacyoauth.New(legacyoauth.Config{AllowedEmails: allowed, DefaultRole: "viewer"}), books)
	core := newCoreFake()

	lookup := func(_ context.Context, id coreauth.UserID) (*database.User, error) { return books.GetUserByID(string(id)) }
	bridge, err := legacyauth.New(absAtoms{"library.view": rbac.AllOf("books:view", "core:tasks.view")}, lookup, nil)
	if err != nil {
		t.Fatal(err)
	}
	id := &legacyauth.Identity{
		Bridge: bridge, Compat: core,
		Client: func(r *http.Request) coreauth.ClientInfo { return coreauth.ClientInfo{UserAgent: r.UserAgent()} },
		Refresh: func(_ context.Context, p *coreauth.Principal) (*coreauth.Principal, error) { return p, nil },
	}
	r := gin.New()
	r.Use(id.Bind())
	handler := func(c *gin.Context) {
		u, _ := CurrentUser(c)
		ctx := c.Request.Context()
		c.JSON(http.StatusOK, gin.H{
			"user": u.ID, "mode": ABSAuthMode(c), "sid": ABSSessionID(c),
			"method": string(auth.MethodFromContext(ctx)), "email": auth.AccessEmailFromContext(ctx),
			"view": auth.Can(ctx, "library.view"),
		})
	}
	r.GET("/api/me", ABSRequireAuth(resolver), handler)
	return &idHarness{
		absHarness: &absHarness{router: r, store: books.fakeABSStore, cfg: cfg, verifier: verifier, resolver: resolver},
		books:      books, core: core,
	}
}

// signedIn gives books' store a user and a live session at epoch, core an
// account at coreEpoch with the given status, and returns a bearer for it.
func (h *idHarness) signedIn(t *testing.T, epoch, coreEpoch int64, coreStatus string) string {
	t.Helper()
	h.store.addUser(&database.User{ID: idUser, Username: "bruce", Email: "bruce@example.com", Status: "active"})
	s := liveABSSession("s1", idUser)
	s.CredentialEpoch = epoch
	h.store.addSession(s)
	h.core.accounts[idUser] = coreAccount{status: coreStatus, epoch: coreEpoch}
	return "Bearer " + h.mintAccess(t, idUser, "s1")
}

func (h *idHarness) get(headers map[string]string) *httptest.ResponseRecorder {
	return h.do(http.MethodGet, "/api/me", headers)
}

// TestABSIdentity_BearerAnswersFromCore: the epoch and the standing are core's.
func TestABSIdentity_BearerAnswersFromCore(t *testing.T) {
	t.Run("core's epoch moved, books' did not", func(t *testing.T) {
		h := newIdentityHarness(t, nil)
		bearer := h.signedIn(t, 0, 5, "active") // books: session epoch 0 == user epoch 0
		w := h.get(map[string]string{"Authorization": bearer})
		if w.Code != http.StatusUnauthorized || strings.Contains(w.Body.String(), `"user"`) {
			t.Fatalf("status %d (%s), want 401: a password change core made must end the session", w.Code, w.Body.String())
		}
	})
	t.Run("core disabled the user, books' record is active", func(t *testing.T) {
		h := newIdentityHarness(t, nil)
		bearer := h.signedIn(t, 0, 0, "disabled")
		w := h.get(map[string]string{"Authorization": bearer})
		if w.Code != http.StatusForbidden {
			t.Fatalf("status %d (%s), want 403 account is not active", w.Code, w.Body.String())
		}
	})
	t.Run("core agrees: admitted with the session's epoch, never a fresh read", func(t *testing.T) {
		h := newIdentityHarness(t, nil)
		// Books' user epoch is 0 but the session was stamped 5, as core's is.
		bearer := h.signedIn(t, 5, 5, "active")
		w := h.get(map[string]string{"Authorization": bearer})
		if w.Code != http.StatusOK {
			t.Fatalf("status %d (%s), want 200", w.Code, w.Body.String())
		}
		if !slices.Equal(h.core.epochs, []int64{5}) {
			t.Errorf("core was asked for epochs %v, want the session's [5]", h.core.epochs)
		}
		body := w.Body.String()
		for _, want := range []string{`"mode":"jwt"`, `"sid":"s1"`, `"method":"abs"`, `"email":""`, `"view":true`} {
			if !strings.Contains(body, want) {
				t.Errorf("body %s lacks %s", body, want)
			}
		}
	})
}

// TestABSIdentity_KeyAnswersFromCore: a key core does not know is refused
// though books' store holds it; a key core holds is admitted without books'
// store knowing it, as an api_key.
func TestABSIdentity_KeyAnswersFromCore(t *testing.T) {
	h := newIdentityHarness(t, nil)
	h.store.addUser(&database.User{ID: idUser, Username: "bruce", Status: "active"})
	h.books.addKey("abk_only_in_books", &database.APIKey{ID: "k-books", UserID: idUser, Status: "active"})
	h.core.keys["abk_only_in_core"] = coreKey{id: "k-core", user: idUser}
	h.core.keys["abk_revoked_in_core"] = coreKey{id: "k-rev", user: idUser, revoked: true}
	h.books.addKey("abk_revoked_in_core", &database.APIKey{ID: "k-rev", UserID: idUser, Status: "active"})

	if w := h.get(map[string]string{"Authorization": "Bearer abk_only_in_books"}); w.Code != http.StatusUnauthorized {
		t.Errorf("a key only books holds: status %d, want 401", w.Code)
	}
	if w := h.get(map[string]string{"Authorization": "Bearer abk_revoked_in_core"}); w.Code != http.StatusUnauthorized {
		t.Errorf("a key core revoked: status %d (%s), want 401", w.Code, w.Body.String())
	}
	w := h.get(map[string]string{"Authorization": "Bearer abk_only_in_core"})
	if w.Code != http.StatusOK {
		t.Fatalf("a key only core holds: status %d (%s), want 200", w.Code, w.Body.String())
	}
	for _, want := range []string{`"mode":"apikey"`, `"method":"api_key"`} {
		if !strings.Contains(w.Body.String(), want) {
			t.Errorf("body %s lacks %s: a key must be recorded as the key it is", w.Body.String(), want)
		}
	}
}

// TestABSIdentity_ConfinedKeyIsRefusedByCheckCredentialFor: a worker-confined
// key core authenticates is refused on the ABS surface by the compat recheck.
func TestABSIdentity_ConfinedKeyIsRefusedByCheckCredentialFor(t *testing.T) {
	h := newIdentityHarness(t, nil)
	h.store.addUser(&database.User{ID: idUser, Username: "bruce", Status: "active"})
	h.core.keys["abk_fp_worker"] = coreKey{id: "k-fp", user: idUser, confined: true}
	w := h.get(map[string]string{"Authorization": "Bearer abk_fp_worker"})
	if w.Code != http.StatusForbidden || strings.Contains(w.Body.String(), `"user"`) {
		t.Fatalf("status %d (%s), want 403", w.Code, w.Body.String())
	}
}

// TestABSIdentity_ABSKeysNeedTheJWTMode: with the jwt mode off a presented key
// is refused, not downgraded, exactly as HEAD refuses it.
func TestABSIdentity_ABSKeysNeedTheJWTMode(t *testing.T) {
	h := newIdentityHarness(t, nil)
	h.cfg.Modes.JWT = false
	h.core.keys["abk_only_in_core"] = coreKey{id: "k-core", user: idUser}
	if w := h.get(map[string]string{"Authorization": "Bearer abk_only_in_core"}); w.Code != http.StatusUnauthorized {
		t.Errorf("status %d, want 401", w.Code)
	}
	if h.core.asked.keys != 0 {
		t.Error("core was asked to authenticate a key the surface does not accept")
	}
}

// TestABSIdentity_NoCredentialAndBadTokens keep HEAD's statuses and never ask core.
func TestABSIdentity_NoCredentialAndBadTokens(t *testing.T) {
	h := newIdentityHarness(t, nil)
	bearer := h.signedIn(t, 0, 0, "active")
	for name, tc := range map[string]struct {
		headers map[string]string
		status  int
	}{
		"nothing":          {nil, 401},
		"a garbage bearer": {map[string]string{"Authorization": "Bearer nonsense"}, 401},
		"a bad assertion":  {map[string]string{cfaccess.CFAccessHeader: "forged", "Authorization": bearer}, 401},
	} {
		if w := h.get(tc.headers); w.Code != tc.status {
			t.Errorf("%s: status %d (%s), want %d", name, w.Code, w.Body.String(), tc.status)
		}
	}
	if h.core.asked.users+h.core.asked.keys != 0 {
		t.Error("core was asked about a request with no verifiable credential")
	}
	// A session record that is gone or revoked ends the session before core is asked.
	h.store.sessions["s1"].Revoked = true
	if w := h.get(map[string]string{"Authorization": bearer}); w.Code != http.StatusUnauthorized {
		t.Errorf("a revoked session: status %d, want 401", w.Code)
	}
	if h.core.asked.users != 0 {
		t.Error("core was asked about a revoked session")
	}
}

// TestABSIdentity_ServiceTokenStillFallsThroughToTheBearer: a valid assertion
// that names no person (a service token) is not a credential, so the bearer decides.
func TestABSIdentity_ServiceTokenStillFallsThroughToTheBearer(t *testing.T) {
	h := newIdentityHarness(t, []string{"owner@example.com"})
	bearer := h.signedIn(t, 0, 0, "active")
	h.verifier.nonIdentity = map[string]bool{"svc": true}
	w := h.get(map[string]string{cfaccess.CFAccessHeader: "svc", "Authorization": bearer})
	if w.Code != http.StatusOK {
		t.Fatalf("status %d (%s), want 200", w.Code, w.Body.String())
	}
}

// TestABSIdentity_CFProvisioningIsRefused: a verified person is never resolved
// through books' store under core identity. A stranger gets HEAD's 403, an
// allowed person a distinct 403, and no user, identity or link is written.
func TestABSIdentity_CFProvisioningIsRefused(t *testing.T) {
	h := newIdentityHarness(t, []string{"alfred@example.com", "bruce@example.com"})
	h.store.addUser(&database.User{ID: idUser, Username: "bruce", Email: "bruce@example.com", Status: "active"})
	h.core.accounts[idUser] = coreAccount{status: "active"}
	h.verifier.byToken["stranger"] = &legacyoauth.IdentityClaims{Provider: legacyoauth.ProviderCFAccess, Subject: "cf|joker", Email: "joker@example.com", EmailVerified: true}
	h.verifier.byToken["new-allowed"] = &legacyoauth.IdentityClaims{Provider: legacyoauth.ProviderCFAccess, Subject: "cf|alfred", Email: "alfred@example.com", EmailVerified: true}
	h.verifier.byToken["linked-allowed"] = &legacyoauth.IdentityClaims{Provider: legacyoauth.ProviderCFAccess, Subject: "cf|bruce", Email: "bruce@example.com", EmailVerified: true}
	h.verifier.byToken["unverified"] = &legacyoauth.IdentityClaims{Provider: legacyoauth.ProviderCFAccess, Subject: "cf|alfred2", Email: "alfred@example.com", EmailVerified: false}
	usersBefore := len(h.store.users)

	for name, tc := range map[string]struct {
		assertion string
		body      string
	}{
		"a stranger":                 {"stranger", "identity is not authorized for this server"},
		"an unverified e-mail":       {"unverified", "identity is not authorized for this server"},
		"an allowed new person":      {"new-allowed", "single sign-on is not available on this surface"},
		"an allowed existing person": {"linked-allowed", "single sign-on is not available on this surface"},
	} {
		w := h.get(map[string]string{cfaccess.CFAccessHeader: tc.assertion})
		if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), tc.body) {
			t.Errorf("%s: status %d (%s), want 403 %q", name, w.Code, w.Body.String(), tc.body)
		}
	}
	if len(h.store.users) != usersBefore {
		t.Errorf("books' store has %d users after, %d before: a user was provisioned outside core", len(h.store.users), usersBefore)
	}
	if len(h.store.identities) != 0 {
		t.Errorf("books' store holds identities %v: a link was written outside core", h.store.identities)
	}
	// A refused assertion is terminal: a valid bearer riding along does not rescue it.
	bearer := h.signedIn(t, 0, 0, "active")
	w := h.get(map[string]string{cfaccess.CFAccessHeader: "new-allowed", "Authorization": bearer})
	if w.Code != http.StatusForbidden {
		t.Errorf("an allowed assertion beside a valid bearer: status %d, want 403", w.Code)
	}
	if h.core.asked.users+h.core.asked.keys != 0 {
		t.Error("core was asked after the assertion was refused")
	}
}

// TestABSIdentity_SessionExtendsOnlyAfterCoreAdmits: the sliding extension is a
// write to the session record, so it must not happen for a request core refuses.
func TestABSIdentity_SessionExtendsOnlyAfterCoreAdmits(t *testing.T) {
	setup := func(t *testing.T, coreStatus string) (*idHarness, string, *time.Time) {
		h := newIdentityHarness(t, nil)
		t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
		now := t0
		h.resolver.UseClock(func() time.Time { return now })
		h.store.addUser(&database.User{ID: idUser, Username: "bruce", Status: "active"})
		s := &database.ABSSession{ID: "s1", UserID: idUser, CreatedAt: t0, LastUsedAt: t0, ExpiresAt: h.cfg.SessionExpiry(t0, t0)}
		h.store.addSession(s)
		h.core.accounts[idUser] = coreAccount{status: coreStatus}
		tok, _, err := h.cfg.MintAccessToken(idUser, "s1", t0)
		if err != nil {
			t.Fatal(err)
		}
		now = t0.Add(3 * h.cfg.SessionExtendWindow())
		return h, "Bearer " + tok, &now
	}
	h, bearer, _ := setup(t, "disabled")
	if w := h.get(map[string]string{"Authorization": bearer}); w.Code != http.StatusForbidden {
		t.Fatalf("status %d, want 403", w.Code)
	}
	if h.store.extendWrites != 0 {
		t.Errorf("a session core refused was extended (%d writes)", h.store.extendWrites)
	}
	h, bearer, _ = setup(t, "active")
	if w := h.get(map[string]string{"Authorization": bearer}); w.Code != http.StatusOK {
		t.Fatalf("status %d, want 200", w.Code)
	}
	if h.store.extendWrites != 1 {
		t.Errorf("an admitted session was extended %d times, want 1", h.store.extendWrites)
	}
}

// TestABSIdentity_WithoutAnIdentityHeadDecides: the same wiring minus the
// identity binding runs HEAD's resolver over books' store.
func TestABSIdentity_WithoutAnIdentityHeadDecides(t *testing.T) {
	h := newIdentityHarness(t, nil)
	bearer := h.signedIn(t, 0, 5, "disabled") // core would refuse; books is fine
	r := gin.New()                           // no id.Bind()
	r.GET("/api/me", ABSRequireAuth(h.resolver), func(c *gin.Context) { c.String(http.StatusOK, "reached") })
	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	req.Header.Set("Authorization", bearer)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK || h.core.asked.users != 0 {
		t.Fatalf("status %d, core asked %d times: a server built without an identity must stay on HEAD's code", w.Code, h.core.asked.users)
	}
}
