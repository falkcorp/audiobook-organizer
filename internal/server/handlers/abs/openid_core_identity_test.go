// file: internal/server/handlers/abs/openid_core_identity_test.go
// version: 0.1.0
// guid: 7c2e9d54-1a68-4b3f-8e05-9d4a6f1b3c27
// last-edited: 2026-11-21

package abs_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	coreauth "github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"

	"github.com/falkcorp/audiobook-organizer/internal/server/absauth"
	"github.com/falkcorp/audiobook-organizer/internal/server/legacyauth"
)

// coreLikeCompat answers PrincipalFor as core's compat does, over the
// harness's own user rows: the user must exist and be active (standing), and
// only then is the epoch compared with the one the surface passed. It records
// every call so a test can see what the handler asked.
type coreLikeCompat struct {
	h *harness

	mu      sync.Mutex
	calls   []coreCall
	failure error // when set, returned instead of the check

	verifies      []verifyCall
	verifyFailure error // when set, VerifyPassword returns it instead of the check
	verifyNil     bool  // when set, VerifyPassword answers (nil, nil)

	// throttle is core's own failure throttle as Compat.VerifyPassword applies
	// it: keyed on the client the identity names, checked before the password,
	// charged once per wrong password inside the call and cleared by a success.
	// It is what makes "the surface does not charge a second time" observable.
	throttle *coreauth.Throttle

	// core, when non-nil, is core's own record of each account (by user ID)
	// and the compat answers from it alone, never from the harness's books
	// rows. A test that moves core's record without moving books' (or the
	// reverse) can then tell which of the two the handler asked.
	core map[string]*coreRecord
}

type coreRecord struct {
	status string
	epoch  int64
	// username, password, email and created are core's own record of the
	// account's sign-in: VerifyPassword answers from them when core owns the
	// accounts. resetRequired is core's PasswordResetRequired.
	username      string
	password      string
	email         string
	created       time.Time
	resetRequired bool
}

type coreCall struct {
	user   coreauth.UserID
	epoch  int64
	client coreauth.ClientInfo
}

// verifyCall is one VerifyPassword call as the handler made it.
type verifyCall struct {
	username string
	password string
	client   coreauth.ClientInfo
}

// VerifyPassword answers as core's compat does, in core's order: the
// password (an unknown name is a wrong password), then status, then reset.
// With core's records set it answers from them alone; without, from the
// harness's books rows.
func (c *coreLikeCompat) VerifyPassword(ctx context.Context, username, password string, client coreauth.ClientInfo) (*coreauth.User, error) {
	if c.throttle == nil {
		return c.verifyPassword(ctx, username, password, client)
	}
	source, account := client.Addr.String(), coreauth.AccountKey(username)
	if c.throttle.SourceBlocked(source) {
		return nil, coreauth.ErrThrottled
	}
	u, err := c.verifyPassword(ctx, username, password, client)
	switch {
	case errors.Is(err, coreauth.ErrInvalidCredentials):
		c.throttle.RecordFailure(account, source)
	case err == nil && u != nil:
		c.throttle.Clear(account, source)
	}
	return u, err
}

// charges is the failures core's throttle counted against the account.
func (c *coreLikeCompat) charges(username string) int {
	return c.throttle.AccountFailures(coreauth.AccountKey(username))
}

func (c *coreLikeCompat) verifyPassword(_ context.Context, username, password string, client coreauth.ClientInfo) (*coreauth.User, error) {
	c.mu.Lock()
	c.verifies = append(c.verifies, verifyCall{username: username, password: password, client: client})
	failure, nilUser := c.verifyFailure, c.verifyNil
	c.mu.Unlock()
	if failure != nil {
		return nil, failure
	}
	if nilUser {
		return nil, nil
	}
	if c.core != nil {
		return c.verifyFromCore(username, password)
	}
	cur, _ := c.h.store.GetUserByUsername(username)
	if cur == nil {
		return nil, fmt.Errorf("%w: unknown", coreauth.ErrInvalidCredentials)
	}
	c.h.store.mu.Lock()
	hash, algo, status, epoch := cur.PasswordHash, cur.PasswordHashAlgo, cur.Status, cur.CredentialEpoch
	c.h.store.mu.Unlock()
	if ok, _ := absauth.Passwords.VerifyLogin(algo, hash, password); !ok {
		return nil, fmt.Errorf("%w: bad password", coreauth.ErrInvalidCredentials)
	}
	if status != "active" {
		return nil, fmt.Errorf("%w: %s", coreauth.ErrInactiveUser, cur.ID)
	}
	return &coreauth.User{
		ID: coreauth.UserID(cur.ID), Username: cur.Username, Email: cur.Email,
		Status: coreauth.StatusActive, CredentialEpoch: epoch, CreatedAt: cur.CreatedAt,
	}, nil
}

// verifyFromCore is VerifyPassword over core's own records.
func (c *coreLikeCompat) verifyFromCore(username, password string) (*coreauth.User, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for id, rec := range c.core {
		if !strings.EqualFold(rec.username, strings.TrimSpace(username)) {
			continue
		}
		switch {
		case rec.password != password:
			return nil, fmt.Errorf("%w: bad password", coreauth.ErrInvalidCredentials)
		case rec.status != "active":
			return nil, fmt.Errorf("%w: %s", coreauth.ErrInactiveUser, id)
		case rec.resetRequired:
			return nil, fmt.Errorf("%w: %s", coreauth.ErrResetRequired, id)
		}
		return &coreauth.User{
			ID: coreauth.UserID(id), Username: rec.username, Email: rec.email,
			Status: coreauth.StatusActive, CredentialEpoch: rec.epoch, CreatedAt: rec.created,
		}, nil
	}
	return nil, fmt.Errorf("%w: unknown", coreauth.ErrInvalidCredentials)
}

func (c *coreLikeCompat) verified() []verifyCall {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]verifyCall(nil), c.verifies...)
}

func (*coreLikeCompat) AuthenticateKey(context.Context, string, coreauth.ClientInfo) (*coreauth.Principal, error) {
	return nil, errors.New("not used")
}

func (*coreLikeCompat) CheckCredentialFor(*coreauth.Principal, rbac.Requirement) error { return nil }

func (c *coreLikeCompat) PrincipalFor(_ context.Context, u *coreauth.User, _ []rbac.Permission, epoch int64, client coreauth.ClientInfo) (*coreauth.Principal, error) {
	c.mu.Lock()
	c.calls = append(c.calls, coreCall{user: u.ID, epoch: epoch, client: client})
	failure := c.failure
	c.mu.Unlock()
	if failure != nil {
		return nil, failure
	}
	if c.core != nil {
		return c.answerFromCore(u, epoch)
	}
	cur, _ := c.h.store.GetUserByID(string(u.ID))
	if cur == nil {
		return nil, fmt.Errorf("%w: %s", coreauth.ErrInactiveUser, u.ID)
	}
	c.h.store.mu.Lock()
	status, current := cur.Status, cur.CredentialEpoch
	c.h.store.mu.Unlock()
	if status != "active" {
		return nil, fmt.Errorf("%w: %s", coreauth.ErrInactiveUser, u.ID)
	}
	if epoch != current {
		return nil, fmt.Errorf("%w: %s", coreauth.ErrStaleEpoch, u.ID)
	}
	return &coreauth.Principal{
		Method: "books.abs",
		User: &coreauth.User{
			ID: u.ID, Username: cur.Username, Email: cur.Email, Status: coreauth.StatusActive,
			CredentialEpoch: current, CreatedAt: cur.CreatedAt,
		},
	}, nil
}

// answerFromCore is PrincipalFor over core's own record: standing, then epoch.
func (c *coreLikeCompat) answerFromCore(u *coreauth.User, epoch int64) (*coreauth.Principal, error) {
	c.mu.Lock()
	rec := c.core[string(u.ID)]
	var status string
	var current int64
	name, email, created := "bruce", "", time.Time{}
	if rec != nil {
		status, current, email, created = rec.status, rec.epoch, rec.email, rec.created
		if rec.username != "" {
			name = rec.username
		}
	}
	c.mu.Unlock()
	switch {
	case rec == nil:
		return nil, fmt.Errorf("%w: %s", coreauth.ErrInvalidCredentials, u.ID)
	case status != "active":
		return nil, fmt.Errorf("%w: %s", coreauth.ErrInactiveUser, u.ID)
	case epoch != current:
		return nil, fmt.Errorf("%w: %s", coreauth.ErrStaleEpoch, u.ID)
	}
	return &coreauth.Principal{
		Method: "books.abs",
		User:   &coreauth.User{ID: u.ID, Username: name, Email: email, Status: coreauth.StatusActive, CredentialEpoch: current, CreatedAt: created},
	}, nil
}

// setCore edits core's record of user id.
func (c *coreLikeCompat) setCore(id string, fn func(*coreRecord)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	fn(c.core[id])
}

func (c *coreLikeCompat) recorded() []coreCall {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]coreCall(nil), c.calls...)
}

// underCoreIdentity serves h's routes the way a server built with
// server.WithIdentity does: every request is bound to the identity first.
func (h *harness) underCoreIdentity(t *testing.T) *coreLikeCompat {
	t.Helper()
	compat := &coreLikeCompat{h: h, throttle: coreauth.NewThrottle(coreauth.ThrottleLimits{}, nil, nil)}
	id := &legacyauth.Identity{
		Compat: compat,
		// The client is the visitor a test names with visitorHeader, else the
		// TCP peer, as core's stage 0 resolves it.
		Client: func(r *http.Request) coreauth.ClientInfo {
			a, err := netip.ParseAddr(r.Header.Get(visitorHeader))
			if err != nil {
				ap, _ := netip.ParseAddrPort(r.RemoteAddr)
				a = ap.Addr()
			}
			return coreauth.ClientInfo{UserAgent: "batarang", Addr: a}
		},
	}
	r := gin.New()
	if err := r.SetTrustedProxies(nil); err != nil {
		t.Fatal(err)
	}
	r.Use(id.Bind())
	h.handler.Register(r)
	h.router = r
	return compat
}

// A code is minted before the identity is bound: under core identity hop 2
// (the browser's Cloudflare Access assertion) refuses a person, see
// TestOpenIDCoreIdentity_HopTwoRefusesAPersonUnderIdentity.

// signInOnce mints and redeems one code so the person's user exists, then
// moves the account's epoch to want (a user who changed credentials before).
func (h *harness) signInOnce(t *testing.T, bumps int) {
	t.Helper()
	first := h.allow(t, h.hop1(t, s256(oidcVerifier), "s0"), oidcPersonAssertion).Get("code")
	if w := h.redeem(t, first, oidcVerifier, "&state=s0"); w.Code != http.StatusOK {
		t.Fatalf("precondition: first sign-in = %d: %s", w.Code, w.Body.String())
	}
	for range bumps {
		h.bumpAllCredentialEpochs()
	}
}

func (h *harness) mintCode(t *testing.T) string {
	t.Helper()
	code := h.allow(t, h.hop1(t, s256(oidcVerifier), "client-state-1"), oidcPersonAssertion).Get("code")
	if code == "" {
		t.Fatal("precondition: hop 2 delivers a code")
	}
	return code
}

// sessionEpochs are the epochs stamped on the stored sessions.
func (h *harness) sessionEpochs() []int64 {
	h.store.mu.Lock()
	defer h.store.mu.Unlock()
	var out []int64
	for _, s := range h.store.sessions {
		out = append(out, s.CredentialEpoch)
	}
	return out
}

// Under core identity the code's own epoch goes to core, and the session is
// stamped with the epoch core's principal carries. The account is at a
// non-zero epoch so that passing 0 (or anything but the code's epoch) fails.
func TestOpenIDCoreIdentity_AskCoreWithTheCodesEpochAndStampTheSession(t *testing.T) {
	h := newOIDCHarness(t)
	h.signInOnce(t, 2)
	code := h.mintCode(t)
	compat := h.underCoreIdentity(t)
	before := h.sessionCount()

	w := h.redeem(t, code, oidcVerifier, "&state=client-state-1")
	if w.Code != http.StatusOK {
		t.Fatalf("callback = %d, want 200: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "owner@example.com") {
		t.Fatalf("session must be for the browser-hop person: %s", w.Body.String())
	}
	calls := compat.recorded()
	if len(calls) != 1 || calls[0].epoch != 2 || calls[0].client.UserAgent != "batarang" {
		t.Fatalf("core was asked %+v, want one call at epoch 2 with the identity's client", calls)
	}
	if got := h.sessionCount(); got != before+1 {
		t.Fatalf("sessions %d -> %d, want one new session", before, got)
	}
	for _, e := range h.sessionEpochs() {
		if e != 0 && e != 2 {
			t.Fatalf("a session carries epoch %d, want the epoch core resolved (2)", e)
		}
	}
}

// An epoch bump after the code was minted is refused through core and
// creates no session: the read that HEAD made of books' own row is gone.
func TestOpenIDCoreIdentity_EpochBumpAfterMintIsRefused(t *testing.T) {
	h := newOIDCHarness(t)
	h.signInOnce(t, 2)
	code := h.mintCode(t)
	compat := h.underCoreIdentity(t)
	h.bumpAllCredentialEpochs()
	before := h.sessionCount()

	w := h.redeem(t, code, oidcVerifier, "&state=client-state-1")
	if w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), "sign in again") {
		t.Fatalf("callback after a credential change = %d %s, want 401 credentials-changed", w.Code, w.Body.String())
	}
	if len(compat.recorded()) != 1 {
		t.Fatalf("core was not consulted exactly once: %+v", compat.recorded())
	}
	if got := h.sessionCount(); got != before {
		t.Fatalf("a refused code created a session (%d -> %d)", before, got)
	}
}

// A deactivation after the mint is refused through core and creates no
// session. Core judges standing before epoch, so the status is 403 where
// HEAD (epoch first) answers 401: the one status-only difference.
func TestOpenIDCoreIdentity_DeactivationAfterMintIsRefused(t *testing.T) {
	h := newOIDCHarness(t)
	h.signInOnce(t, 0)
	code := h.mintCode(t)
	h.underCoreIdentity(t)
	h.store.mu.Lock()
	for _, u := range h.store.users {
		u.Status = "locked"
		u.CredentialEpoch++
	}
	h.store.mu.Unlock()
	before := h.sessionCount()

	w := h.redeem(t, code, oidcVerifier, "&state=client-state-1")
	if w.Code != http.StatusForbidden {
		t.Fatalf("callback after a deactivation = %d %s, want 403", w.Code, w.Body.String())
	}
	if got := h.sessionCount(); got != before {
		t.Fatalf("a refused code created a session (%d -> %d)", before, got)
	}
}

// Every other core refusal keeps a client-safe status and issues nothing.
func TestOpenIDCoreIdentity_CoreRefusalsMapToStatuses(t *testing.T) {
	for name, tc := range map[string]struct {
		err    error
		status int
		retry  bool
	}{
		"throttled":           {coreauth.ErrThrottled, http.StatusTooManyRequests, true},
		"sign-in restricted":  {coreauth.ErrResetRequired, http.StatusForbidden, false},
		"inactive":            {coreauth.ErrInactiveUser, http.StatusForbidden, false},
		"stale epoch":         {coreauth.ErrStaleEpoch, http.StatusUnauthorized, false},
		"no such user":        {coreauth.ErrInvalidCredentials, http.StatusUnauthorized, false},
		"the store is down":   {coreauth.ErrStoreUnavailable, http.StatusInternalServerError, false},
		"an unclassified one": {errors.New("batcave fire"), http.StatusInternalServerError, false},
	} {
		t.Run(name, func(t *testing.T) {
			h := newOIDCHarness(t)
			h.signInOnce(t, 0)
			code := h.mintCode(t)
			compat := h.underCoreIdentity(t)
			compat.failure = fmt.Errorf("core: %w", tc.err)
			before := h.sessionCount()

			w := h.redeem(t, code, oidcVerifier, "&state=client-state-1")
			if w.Code != tc.status {
				t.Fatalf("status = %d %s, want %d", w.Code, w.Body.String(), tc.status)
			}
			if tc.retry && w.Header().Get("Retry-After") == "" {
				t.Fatal("a throttled refusal must carry Retry-After")
			}
			if got := h.sessionCount(); got != before {
				t.Fatalf("a refused code created a session (%d -> %d)", before, got)
			}
		})
	}
}

// The mode is the wiring's, not the request's: a server built without an
// identity (books today, LegacyAuthGin) answers from HEAD's own store read
// whatever else the request carries. The deactivation case is the probe: HEAD
// answers 401 credentials-changed (epoch first), core would answer 403.
func TestOpenIDCoreIdentity_WithoutAnIdentityHeadDecides(t *testing.T) {
	h := newOIDCHarness(t)
	h.signInOnce(t, 0)
	code := h.mintCode(t)
	h.store.mu.Lock()
	for _, u := range h.store.users {
		u.Status = "locked"
		u.CredentialEpoch++
	}
	h.store.mu.Unlock()

	w := h.redeem(t, code, oidcVerifier, "&state=client-state-1")
	if w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), "sign in again") {
		t.Fatalf("callback = %d %s, want HEAD's 401 credentials-changed", w.Code, w.Body.String())
	}
}

// coreOwnsTheAccounts gives compat a record of every books user as it stands
// now, then answers from that record alone. It returns the one user's ID.
func (h *harness) coreOwnsTheAccounts(t *testing.T, compat *coreLikeCompat) string {
	t.Helper()
	compat.core = map[string]*coreRecord{}
	h.store.mu.Lock()
	defer h.store.mu.Unlock()
	var id string
	for _, u := range h.store.users {
		compat.core[u.ID] = &coreRecord{status: u.Status, epoch: u.CredentialEpoch}
		id = u.ID
	}
	if len(compat.core) != 1 {
		t.Fatalf("precondition: %d accounts, want 1", len(compat.core))
	}
	return id
}

// A7 flip gap: the epoch and the standing are core's. Each case moves ONE of
// the two records after the code was minted and leaves the other where it
// was, so a handler that read books' row (HEAD's read) answers the opposite.
func TestOpenIDCoreIdentity_EpochAndStatusAreCores(t *testing.T) {
	t.Run("core's epoch moves, books' does not: refused", func(t *testing.T) {
		h := newOIDCHarness(t)
		h.signInOnce(t, 2)
		code := h.mintCode(t)
		compat := h.underCoreIdentity(t)
		id := h.coreOwnsTheAccounts(t, compat)
		compat.setCore(id, func(r *coreRecord) { r.epoch++ })
		before := h.sessionCount()

		w := h.redeem(t, code, oidcVerifier, "&state=client-state-1")
		if w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), "sign in again") {
			t.Fatalf("callback = %d %s, want 401: core's credential change must end the code", w.Code, w.Body.String())
		}
		if got := h.sessionCount(); got != before {
			t.Fatalf("a code core refused created a session (%d -> %d)", before, got)
		}
	})
	t.Run("core disables the user, books' row is active: refused", func(t *testing.T) {
		h := newOIDCHarness(t)
		h.signInOnce(t, 0)
		code := h.mintCode(t)
		compat := h.underCoreIdentity(t)
		id := h.coreOwnsTheAccounts(t, compat)
		compat.setCore(id, func(r *coreRecord) { r.status = "disabled" })
		before := h.sessionCount()

		w := h.redeem(t, code, oidcVerifier, "&state=client-state-1")
		if w.Code != http.StatusForbidden {
			t.Fatalf("callback = %d %s, want 403", w.Code, w.Body.String())
		}
		if got := h.sessionCount(); got != before {
			t.Fatalf("a code core refused created a session (%d -> %d)", before, got)
		}
	})
	t.Run("books' row moves, core's does not: admitted", func(t *testing.T) {
		h := newOIDCHarness(t)
		h.signInOnce(t, 2)
		code := h.mintCode(t)
		compat := h.underCoreIdentity(t)
		h.coreOwnsTheAccounts(t, compat)
		h.store.mu.Lock()
		for _, u := range h.store.users {
			u.Status = "locked"
			u.CredentialEpoch += 5
		}
		h.store.mu.Unlock()
		before := h.sessionCount()

		w := h.redeem(t, code, oidcVerifier, "&state=client-state-1")
		if w.Code != http.StatusOK {
			t.Fatalf("callback = %d %s, want 200: books' row is not the account's record under core identity", w.Code, w.Body.String())
		}
		if got := h.sessionCount(); got != before+1 {
			t.Fatalf("sessions %d -> %d, want one new session", before, got)
		}
	})
}

// F4-08 B10b1, no shadow users outside core: under core identity the browser
// hop's Cloudflare Access assertion is not resolved through books' store (it
// would create, link or mark a user there). Hop 2 refuses the person and
// delivers no code, so the flow cannot reach redemption. Without an identity
// the same request mints a code (HEAD, every other test in the file).
func TestOpenIDCoreIdentity_HopTwoRefusesAPersonUnderIdentity(t *testing.T) {
	h := newOIDCHarness(t)
	browser := h.hop1(t, s256(oidcVerifier), "client-state-1")
	h.underCoreIdentity(t)
	users := h.userCount()

	w := h.get(t, browser, oidcPersonAssertion)
	if w.Code != http.StatusFound {
		t.Fatalf("hop 2 = %d %s, want a redirect carrying the refusal", w.Code, w.Body.String())
	}
	loc := w.Header().Get("Location")
	if !strings.Contains(loc, "error=access_denied") || strings.Contains(loc, "code=") {
		t.Fatalf("Location %q: want access_denied and no code", loc)
	}
	if got := h.userCount(); got != users {
		t.Fatalf("users %d -> %d: hop 2 provisioned a user outside core", users, got)
	}
}

func (h *harness) userCount() int {
	h.store.mu.Lock()
	defer h.store.mu.Unlock()
	return len(h.store.users)
}
