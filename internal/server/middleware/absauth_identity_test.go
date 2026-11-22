// file: internal/server/middleware/absauth_identity_test.go
// version: 0.1.0
// guid: c7e2a490-1b6d-4f38-9a05-d4b8e3c1f927
// last-edited: 2026-11-21

package middleware

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	coreauth "github.com/falkcorp/mediacore/auth"
	coreoidc "github.com/falkcorp/mediacore/auth/oidc"
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
	// method, when set, is the method of the principals it builds (default
	// books.abs); perms, when set, is the scopes it grants a user.
	method coreauth.Method
	perms  []rbac.Permission
}

func (f *coreFake) methodOrDefault() coreauth.Method {
	if f.method != "" {
		return f.method
	}
	return legacyauth.MethodBooksABS
}

func (f *coreFake) scopes() []rbac.Permission {
	if f.perms != nil {
		return f.perms
	}
	return []rbac.Permission{"books:view", "core:tasks.view"}
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
		Method: f.methodOrDefault(),
		User:   &coreauth.User{ID: u.ID, Username: "bruce", Status: coreauth.StatusActive},
		Perms:  coreChecker{perms: f.scopes()},
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
func (a absAtoms) Atoms() []string {
	out := make([]string, 0, len(a))
	for k := range a {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// idHarness is an ABS surface under core identity beside a books store the
// tests keep deliberately out of step with core.
type idHarness struct {
	*absHarness
	books *apiKeyStore
	core  *coreFake
	// access is core's account resolver as the Access sign-in reaches it
	// (legacyauth.Identity.Access); identity is the Identity the router binds.
	access   *accessFake
	identity *legacyauth.Identity
	// coreUsers is core's user table as the bridge's lookup reads it (books'
	// store holds none of these).
	coreUsers map[string]*database.User
}

// accessFake is legacyauth.Identity.Access: it answers what the test programs
// and records every call, so a test can say what core was asked.
type accessFake struct {
	mu    sync.Mutex
	fn    func(legacyauth.AccessClaims) (*coreauth.User, error)
	calls []legacyauth.AccessClaims
}

func (a *accessFake) resolve(_ context.Context, c legacyauth.AccessClaims) (*coreauth.User, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls = append(a.calls, c)
	if a.fn == nil {
		return nil, errors.New("accessFake: nothing programmed")
	}
	return a.fn(c)
}

func (a *accessFake) answers(u *coreauth.User, err error) {
	a.fn = func(legacyauth.AccessClaims) (*coreauth.User, error) { return u, err }
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

	coreUsers := map[string]*database.User{}
	lookup := func(_ context.Context, id coreauth.UserID) (*database.User, error) {
		if u, ok := coreUsers[string(id)]; ok {
			return u, nil
		}
		return books.GetUserByID(string(id))
	}
	access := &accessFake{}
	bridge, err := legacyauth.New(absAtoms{
		"library.view":                     rbac.AllOf("books:view", "core:tasks.view"),
		string(auth.PermFingerprintWorker): rbac.Need("books:fpworker"),
	}, lookup, nil)
	if err != nil {
		t.Fatal(err)
	}
	id := &legacyauth.Identity{
		Bridge: bridge, Compat: core,
		Client:  func(r *http.Request) coreauth.ClientInfo { return coreauth.ClientInfo{UserAgent: r.UserAgent()} },
		Refresh: func(_ context.Context, p *coreauth.Principal) (*coreauth.Principal, error) { return p, nil },
		Access:  access.resolve,
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
		books:      books, core: core, access: access, identity: id, coreUsers: coreUsers,
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

// cfClaims is a verified Access assertion for a person.
func cfClaims(sub, email string, verified bool) *legacyoauth.IdentityClaims {
	return &legacyoauth.IdentityClaims{Provider: legacyoauth.ProviderCFAccess, Subject: sub, Email: email, EmailVerified: verified}
}

// reasonOf runs ResolveCFAssertion for the assertion under id (the harness's
// Identity unless one is given) and returns the answer's status and audit
// reason: the reason is what a 500 from a recovered panic would not carry.
func (h *idHarness) reasonOf(t *testing.T, assertion string, id *legacyauth.Identity) (int, string, *ABSIdentity) {
	t.Helper()
	if id == nil {
		id = h.identity
	}
	var (
		status int
		reason string
		got    *ABSIdentity
	)
	r := gin.New()
	r.Use(id.Bind())
	r.GET("/x", func(c *gin.Context) {
		var e *ABSAuthError
		got, e = h.resolver.ResolveCFAssertion(c)
		if e != nil {
			status, reason = e.Status, e.Reason
		}
		c.Status(http.StatusNoContent)
	})
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set(cfaccess.CFAccessHeader, assertion)
	r.ServeHTTP(httptest.NewRecorder(), req)
	return status, reason, got
}

// TestABSIdentity_CFPersonResolvesThroughCoreNotBooksStore: a verified person is
// resolved by core's account resolver, never through books' store. Books' store
// holds a person with the same address under another ID and core resolves to a
// different user, so a path that read books' store gives the other answer; the
// response, the epoch core is asked at and the session user are core's, and
// books' store gains no user, identity or link.
func TestABSIdentity_CFPersonResolvesThroughCoreNotBooksStore(t *testing.T) {
	h := newIdentityHarness(t, []string{"alfred@example.com", "bruce@example.com"})
	h.store.addUser(&database.User{ID: idUser, Username: "bruce", Email: "bruce@example.com", Status: "active"})
	const coreID, coreEpoch = "u-core-bruce", int64(7)
	h.coreUsers[coreID] = &database.User{ID: coreID, Username: "bruce-core", Email: "bruce@example.com", Status: "active"}
	h.core.accounts[coreID] = coreAccount{status: "active", epoch: coreEpoch}
	h.access.answers(&coreauth.User{ID: coreID, Username: "bruce-core", Email: "bruce@example.com", Status: coreauth.StatusActive, CredentialEpoch: coreEpoch}, nil)
	h.verifier.byToken["bruce"] = cfClaims("cf|bruce", "bruce@example.com", true)
	usersBefore := len(h.store.users)

	w := h.get(map[string]string{cfaccess.CFAccessHeader: "bruce"})
	if w.Code != http.StatusOK {
		t.Fatalf("status %d (%s), want 200", w.Code, w.Body.String())
	}
	for _, want := range []string{`"user":"` + coreID + `"`, `"mode":"cf"`, `"method":"abs"`} {
		if !strings.Contains(w.Body.String(), want) {
			t.Errorf("body %s lacks %s", w.Body.String(), want)
		}
	}
	// What core was asked: the subject and e-mail of the assertion, exactly,
	// with the verifier's word on the address.
	if len(h.access.calls) != 1 || h.access.calls[0] != (legacyauth.AccessClaims{Subject: "cf|bruce", Email: "bruce@example.com", EmailVerified: true}) {
		t.Errorf("core's resolver was asked %+v", h.access.calls)
	}
	// The principal is core's at the epoch of the record core resolved: a
	// credential that stamped 0 would be stale against core's 7.
	if !slices.Equal(h.core.epochs, []int64{coreEpoch}) {
		t.Errorf("PrincipalFor was given epochs %v, want [%d]", h.core.epochs, coreEpoch)
	}
	if len(h.store.users) != usersBefore || len(h.store.identities) != 0 {
		t.Errorf("books' store changed: %d users (was %d), identities %v", len(h.store.users), usersBefore, h.store.identities)
	}
}

// TestABSIdentity_CFAnswersMapToHEADs: each of core's answers keeps the status
// HEAD gave the same situation (ResolveUser's ErrEmailNotAllowed, its not-verified
// and conflict refusals, absCheckUserActive), and anything core did not name is
// a 500, never a 401 or 403: the assertion is not known to be bad.
func TestABSIdentity_CFAnswersMapToHEADs(t *testing.T) {
	wrap := func(err error) error { return fmt.Errorf("resolve: %w", err) }
	for name, tc := range map[string]struct {
		user   *coreauth.User
		err    error
		status int
		reason string
		body   string
	}{
		"not on the allow-list":        {nil, wrap(coreoidc.ErrEmailNotAllowed), 403, "email-not-allowed", "identity is not authorized for this server"},
		"e-mail not verified":          {nil, wrap(coreoidc.ErrEmailNotVerified), 403, "email-not-allowed", "identity is not authorized for this server"},
		"e-mail not plain":             {nil, wrap(coreauth.ErrEmailNotPlain), 403, "email-not-allowed", "identity is not authorized for this server"},
		"e-mail held by another":       {nil, wrap(coreauth.ErrEmailInUse), 403, "email-not-allowed", "identity is not authorized for this server"},
		"account inactive":             {nil, wrap(coreauth.ErrInactiveUser), 403, "user-inactive", "account is not active"},
		"account restricted or reset":  {nil, wrap(coreauth.ErrResetRequired), 403, "reset-required", "account is not active"},
		"a store error":                {nil, errors.New("pebble: write stalled"), 500, "identity-resolve-error", "could not resolve identity"},
		"the store unavailable":        {nil, wrap(coreauth.ErrStoreUnavailable), 500, "identity-resolve-error", "could not resolve identity"},
		"a commit that kept losing":    {nil, wrap(coreauth.ErrContended), 500, "identity-resolve-error", "could not resolve identity"},
		"an unbound core resolver":     {nil, wrap(coreoidc.ErrResolverUnbound), 500, "identity-resolve-error", "could not resolve identity"},
		"no user and no error":         {nil, nil, 500, "identity-resolve-error", "could not resolve identity"},
		"a user core records inactive": {&coreauth.User{ID: "u-x", Status: coreauth.StatusDisabled}, nil, 403, "user-inactive", "account is not active"},
	} {
		t.Run(name, func(t *testing.T) {
			h := newIdentityHarness(t, []string{"alfred@example.com"})
			h.access.answers(tc.user, tc.err)
			h.verifier.byToken["a"] = cfClaims("cf|alfred", "alfred@example.com", true)
			usersBefore := len(h.store.users)
			w := h.get(map[string]string{cfaccess.CFAccessHeader: "a"})
			if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.body) {
				t.Errorf("status %d (%s), want %d %q", w.Code, w.Body.String(), tc.status, tc.body)
			}
			if st, reason, got := h.reasonOf(t, "a", nil); st != tc.status || reason != tc.reason || got != nil {
				t.Errorf("answer %d %q (identity %v), want %d %q and none", st, reason, got, tc.status, tc.reason)
			}
			// Terminal: a valid bearer riding along does not rescue a refused
			// assertion, and core's Compat was never asked to admit anyone.
			bearer := h.signedIn(t, 0, 0, "active")
			if w := h.get(map[string]string{cfaccess.CFAccessHeader: "a", "Authorization": bearer}); w.Code != tc.status {
				t.Errorf("beside a valid bearer: status %d, want %d", w.Code, tc.status)
			}
			if h.core.asked.users+h.core.asked.keys != 0 {
				t.Error("core's Compat was asked after the assertion was refused")
			}
			if len(h.store.users) != usersBefore+1 { // signedIn adds one books user
				t.Errorf("books' store has %d users, want %d: a refusal provisioned one", len(h.store.users), usersBefore+1)
			}
		})
	}
}

// TestABSIdentity_CFUnverifiedAddressIsHandedOnAsUnverified: the verifier's word
// on the address reaches core as it is. A claim hard-coded to verified would
// admit what HEAD refused.
func TestABSIdentity_CFUnverifiedAddressIsHandedOnAsUnverified(t *testing.T) {
	h := newIdentityHarness(t, []string{"alfred@example.com"})
	h.access.fn = func(c legacyauth.AccessClaims) (*coreauth.User, error) {
		if !c.EmailVerified {
			return nil, coreoidc.ErrEmailNotVerified
		}
		return &coreauth.User{ID: "u-core-alfred", Status: coreauth.StatusActive}, nil
	}
	h.verifier.byToken["unverified"] = cfClaims("cf|alfred", "alfred@example.com", false)
	if w := h.get(map[string]string{cfaccess.CFAccessHeader: "unverified"}); w.Code != http.StatusForbidden {
		t.Fatalf("an unverified address: status %d (%s), want 403", w.Code, w.Body.String())
	}
	if len(h.access.calls) != 1 || h.access.calls[0].EmailVerified {
		t.Errorf("core was handed %+v, want EmailVerified false", h.access.calls)
	}
}

// TestABSIdentity_CFWithoutAResolverIsTerminal: an Identity with no Access
// resolver answers a verified person 500 access-resolver-missing, on the audit
// reason (a recovered panic is a 500 too, and carries none). It never reaches
// books' store, so no person is provisioned there.
func TestABSIdentity_CFWithoutAResolverIsTerminal(t *testing.T) {
	h := newIdentityHarness(t, []string{"alfred@example.com"})
	h.verifier.byToken["a"] = cfClaims("cf|alfred", "alfred@example.com", true)
	bare := *h.identity
	bare.Access = nil
	usersBefore := len(h.store.users)
	st, reason, got := h.reasonOf(t, "a", &bare)
	if st != http.StatusInternalServerError || reason != "access-resolver-missing" || got != nil {
		t.Errorf("answer %d %q (identity %v), want 500 access-resolver-missing and none", st, reason, got)
	}
	if len(h.store.users) != usersBefore || len(h.store.identities) != 0 {
		t.Error("books' store was written without an Access resolver")
	}
}

// TestABSIdentity_CFOffIdentityIsHEADsPath: with no Identity bound, a verified
// person is resolved by HEAD's ResolveUser over books' store (provisioned there)
// and core's resolver is never asked.
func TestABSIdentity_CFOffIdentityIsHEADsPath(t *testing.T) {
	h := newIdentityHarness(t, []string{"alfred@example.com"})
	h.verifier.byToken["a"] = cfClaims("cf|alfred", "alfred@example.com", true)
	h.verifier.byToken["stranger"] = cfClaims("cf|joker", "joker@example.com", true)
	r := gin.New() // no id.Bind()
	r.GET("/api/me", ABSRequireAuth(h.resolver), func(c *gin.Context) {
		u, _ := CurrentUser(c)
		c.JSON(http.StatusOK, gin.H{"email": u.Email})
	})
	get := func(token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
		req.Header.Set(cfaccess.CFAccessHeader, token)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	usersBefore := len(h.store.users)
	if w := get("a"); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "alfred@example.com") {
		t.Fatalf("an allowed person off identity: status %d (%s), want 200", w.Code, w.Body.String())
	}
	if len(h.store.users) != usersBefore+1 {
		t.Errorf("books' store has %d users, want %d: HEAD provisions the person there", len(h.store.users), usersBefore+1)
	}
	if w := get("stranger"); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "identity is not authorized for this server") {
		t.Errorf("a stranger off identity: status %d (%s), want HEAD's 403", w.Code, w.Body.String())
	}
	if len(h.access.calls) != 0 {
		t.Errorf("core's resolver was asked %d times off identity", len(h.access.calls))
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
	r := gin.New()                            // no id.Bind()
	r.GET("/api/me", ABSRequireAuth(h.resolver), func(c *gin.Context) { c.String(http.StatusOK, "reached") })
	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	req.Header.Set("Authorization", bearer)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK || h.core.asked.users != 0 {
		t.Fatalf("status %d, core asked %d times: a server built without an identity must stay on HEAD's code", w.Code, h.core.asked.users)
	}
}

// TestABSIdentity_WorkerOnlyUserStaysOnTheWorkerAPI: a user whose only grant is
// the remote fingerprint worker's (a role, not a confined key) is refused 403
// outside the worker API, as HEAD's post-Bind confinement refuses them.
func TestABSIdentity_WorkerOnlyUserStaysOnTheWorkerAPI(t *testing.T) {
	h := newIdentityHarness(t, nil)
	bearer := h.signedIn(t, 0, 0, "active")
	h.core.perms = []rbac.Permission{"books:fpworker"}
	w := h.get(map[string]string{"Authorization": bearer})
	if w.Code != http.StatusForbidden || strings.Contains(w.Body.String(), `"user"`) {
		t.Fatalf("status %d (%s), want 403 limited to the fingerprint worker API", w.Code, w.Body.String())
	}
}

// TestABSIdentity_AnAdmittedPrincipalTheBridgeCannotMapIsRefused: core admitted
// the credential but its principal is of a method the bridge does not map (or
// names a user books' lookup cannot find): the request is a 401, and the
// handler is never reached with a half-bound identity.
func TestABSIdentity_AnAdmittedPrincipalTheBridgeCannotMapIsRefused(t *testing.T) {
	t.Run("an unmapped method", func(t *testing.T) {
		h := newIdentityHarness(t, nil)
		bearer := h.signedIn(t, 0, 0, "active")
		h.core.method = "books.riddler"
		w := h.get(map[string]string{"Authorization": bearer})
		if w.Code != http.StatusUnauthorized || strings.Contains(w.Body.String(), `"user"`) {
			t.Fatalf("status %d (%s), want 401", w.Code, w.Body.String())
		}
	})
	t.Run("a user the bridge's lookup cannot find", func(t *testing.T) {
		h := newIdentityHarness(t, nil)
		bearer := h.signedIn(t, 0, 0, "active")
		delete(h.store.users, idUser)
		w := h.get(map[string]string{"Authorization": bearer})
		if w.Code != http.StatusUnauthorized || strings.Contains(w.Body.String(), `"user"`) {
			t.Fatalf("status %d (%s), want 401", w.Code, w.Body.String())
		}
	})
}

// TestABSIdentity_SessionRecordRefusalsEndBeforeCoreIsAsked: every check the
// surface itself owns (HEAD's Resolve order) refuses with HEAD's status and
// never reaches core, so a core that would admit cannot admit these.
func TestABSIdentity_SessionRecordRefusalsEndBeforeCoreIsAsked(t *testing.T) {
	cases := map[string]struct {
		mutate func(h *idHarness)
		status int
	}{
		"a session recorded for another user": {func(h *idHarness) { h.store.sessions["s1"].UserID = "u-bane" }, http.StatusUnauthorized},
		"an expired session":                  {func(h *idHarness) { h.store.sessions["s1"].ExpiresAt = time.Now().Add(-time.Minute) }, http.StatusUnauthorized},
		"a session past the absolute cap": {func(h *idHarness) {
			h.cfg.SessionMaxTTL = time.Hour
			h.store.sessions["s1"].CreatedAt = time.Now().Add(-2 * time.Hour)
		}, http.StatusUnauthorized},
		"a session lookup that fails":       {func(h *idHarness) { h.store.sessionErr = errors.New("store down") }, http.StatusInternalServerError},
		"a bearer when the JWT mode is off": {func(h *idHarness) { h.cfg.Modes.JWT = false }, http.StatusUnauthorized},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			h := newIdentityHarness(t, nil)
			bearer := h.signedIn(t, 0, 0, "active")
			// Core agrees to everything, so only the surface's own check can refuse.
			if w := h.get(map[string]string{"Authorization": bearer}); w.Code != http.StatusOK {
				t.Fatalf("control: status %d (%s), want 200", w.Code, w.Body.String())
			}
			h.core.asked.users = 0
			tc.mutate(h)
			w := h.get(map[string]string{"Authorization": bearer})
			if w.Code != tc.status || strings.Contains(w.Body.String(), `"user"`) {
				t.Fatalf("status %d (%s), want %d", w.Code, w.Body.String(), tc.status)
			}
			if h.core.asked.users != 0 {
				t.Error("core was asked about a credential the surface had already refused")
			}
		})
	}
}
