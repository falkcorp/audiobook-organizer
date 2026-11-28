// file: internal/books/cutover/registered_harness_flows_test.go
// version: 0.1.0
// guid: 2f6d9b41-8a07-4c35-b1e2-70c4d8a5f396
// last-edited: 2026-11-22

package cutover_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/mediacore/storage"
)

// ---- seeding a HEAD account ----

// UserSeed is one HEAD account, for registeredConfig.Seed. ID is the HEAD user ID,
// which M3 keeps as core's. EmailSource is HEAD's: "setup" and "admin" prove the
// address, "" does not. Link, when set, is the Cloudflare Access subject HEAD's
// identity link holds for the account. Every address belongs on example.test and
// every person is a Batman character.
type UserSeed struct {
	ID, Username, Email, EmailSource string
	Roles                            []string
	Link                             string
}

// SeedUser writes a HEAD account in the shape HEAD's own store writes (the u: row,
// the username and address indexes, the identity link), raw, so a shape the store
// itself refuses (two proven holders of one address) can be seeded. M3 imports it
// at the boot. A HEAD user has no password here: it signs in through Access only.
func SeedUser(ctx context.Context, lr storage.LegacyRange, us UserSeed) error {
	now := time.Now().UTC().Add(-time.Hour)
	u := store.User{
		ID: us.ID, Username: us.Username, Email: us.Email, PasswordHashAlgo: "oauth",
		Roles: us.Roles, Status: "active", CreatedAt: now, UpdatedAt: now, Version: 1,
		EmailSource: us.EmailSource,
	}
	row, err := json.Marshal(u)
	if err != nil {
		return err
	}
	kv := map[string][]byte{
		"u:" + us.ID: row,
		"idx:user:username:" + strings.ToLower(us.Username): []byte(us.ID),
	}
	if us.Email != "" {
		kv["idx:user:email:"+strings.ToLower(us.Email)] = []byte(us.ID)
	}
	if us.Link != "" {
		linkID := "01M7SD" + strings.ToUpper(fmt.Sprintf("%020x", len(us.Link)*7919+int(us.Link[0])))[:20]
		idRow, err := json.Marshal(store.OAuthIdentity{ID: linkID, Provider: "cfaccess", Subject: us.Link, Email: us.Email, UserID: us.ID, CreatedAt: now})
		if err != nil {
			return err
		}
		kv["oauthid:"+linkID] = idRow
		kv["idx:oauth:sub:cfaccess:"+us.Link] = []byte(linkID)
		kv["idx:oauth:user:"+us.ID+":"+linkID] = []byte("1")
	}
	for k, v := range kv {
		if err := lr.Set(ctx, []byte(k), v); err != nil {
			return fmt.Errorf("seed %s: %w", k, err)
		}
	}
	return nil
}

// ---- the ABS surface ----

// absRequest is one request to the ABS surface as a bearer or an Access assertion,
// from its own peer address (the sign-in paths are rate limited per address).
func (h *Registered) absRequest(method, path, bearer, assertion string) Result {
	h.t.Helper()
	req := httptest.NewRequest(method, path, nil)
	req.RemoteAddr = h.nextPeer()
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if assertion != "" {
		req.Header.Set("Cf-Access-Jwt-Assertion", assertion)
	}
	return h.Raw(req)
}

// absUserID is the "id" an ABS /api/me body carries, empty unless it answered 200.
func absUserID(t *testing.T, res Result) string {
	t.Helper()
	if res.Status != http.StatusOK {
		return ""
	}
	var me struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(res.Body, &me); err != nil {
		t.Fatalf("ABS /api/me: %v: %s", err, res.excerpt())
	}
	return me.ID
}

// coreUsers is core's account list as session:admin reads it: ID to email.
func coreUsers(t *testing.T, h *Registered) map[string]string {
	t.Helper()
	var page struct {
		Items []struct {
			ID    string `json:"id"`
			Email string `json:"email"`
		} `json:"items"`
	}
	h.coreJSON(h.Principal("session:admin"), http.MethodGet, "/api/core/v1/users", nil, &page)
	out := map[string]string{}
	for _, u := range page.Items {
		out[u.ID] = u.Email
	}
	return out
}

// The ids and subjects of the same-address fixture.
const (
	feBruceEmail   = "bruce.wayne@example.test" // held by both administrators
	feBruceLinked  = "01M7SD00000000000000BRUCE1"
	feBruceTwin    = "01M7SD00000000000000BRUCE2"
	feBruceSub     = "d3a91b6c-7f20-4e58-b1c4-0a52e8f61937" // the linked admin's Access sub
	feAlfredEmail  = "alfred.pennyworth@example.test"       // one plain proven holder: the control
	feAlfred       = "01M7SD0000000000000ALFRED1"
	feAlfredSub    = "sub-alfred"
	feJokerEmail   = "joker@example.test" // on nobody's list
	feFreshSub     = "0f6e2d84-1b73-4c95-a8d0-93e17b5c4a26"
	feListedNewbie = "harleen.quinn@example.test" // on the list, holds no account
)

// seedAbsFixtures is the Seed of the ABS Access cells: two proven administrators of
// one address, only one linked to Access by sub, and a plain account with its own
// address (the control that proves the refusals are the rule's).
func seedAbsFixtures(ctx context.Context, lr storage.LegacyRange) error {
	for _, us := range []UserSeed{
		{ID: feBruceLinked, Username: "bw.linked", Email: feBruceEmail, EmailSource: "setup", Roles: []string{"admin"}, Link: feBruceSub},
		{ID: feBruceTwin, Username: "bw.twin", Email: feBruceEmail, EmailSource: "admin", Roles: []string{"admin"}},
		{ID: feAlfred, Username: "af.plain", Email: feAlfredEmail, EmailSource: "setup", Roles: []string{"viewer"}},
	} {
		if err := SeedUser(ctx, lr, us); err != nil {
			return err
		}
	}
	return nil
}

// ---- the six smoke cells ----

// smokeRoutes are the seven routes the smoke cells read; each is also a row of
// verdicts.json, and the sweep holds the whole table to R0 (the sweep proves the
// parity, the cells below say in plain numbers what a person sees).
var smokeRoutes = [7][2]string{
	{http.MethodGet, "/api/v1/audiobooks"},
	{http.MethodGet, "/api/v1/users"},
	{http.MethodGet, "/api/v1/config"},
	{http.MethodPost, "/api/v1/fingerprint/worker/lease"},
	{http.MethodGet, "/api/v1/auth/me"},
	{http.MethodPost, "/api/v1/audiobooks/r2reach-missing/organize"},
	{http.MethodGet, "/api/v1/system/status"},
}

// smokeStatuses are the statuses the registered path answers, per principal; R0
// recorded the same except the lease route, noted below. A 200 is a person reading; a
// 400 or 404 is past authorization (the probe sends no body and names no book); a 401
// or 403 is a refusal. The lease route answers 404 where R0 answered 400: both are
// past the authorization decision (the verdict, which the sweep compares, is the
// same), it is the handler's answer to a lease request with no body.
var smokeStatuses = map[string][7]int{
	"session:admin":        {200, 200, 200, 404, 200, 404, 200},
	"session:editor":       {200, 403, 403, 403, 200, 404, 403},
	"session:viewer":       {200, 403, 403, 403, 200, 403, 403},
	"key:admin:view":       {200, 403, 403, 403, 200, 403, 403},
	"key:fp-worker:worker": {403, 403, 403, 404, 403, 403, 403},
	// Auth off admits every route, and /auth/me describes a user it has none of:
	// 403 FORBIDDEN, not the 401 it answered until F4-08 ruling G5(a) (B10sr),
	// because a 401 there ends a no-auth install's SPA session.
	Authoff: {200, 200, 200, 404, 403, 404, 200},
	// The owner (Access-verified, an ordinary viewer account of its own) reads
	// the library and its own profile and nothing the administrator's role gates.
	Owner: {200, 403, 403, 403, 200, 403, 403},
}

// smokeLegacyCode is the one refusal here that a legacy handler answers, not stage 13:
// /auth/me with no user bound (auth off) is HEAD's own {"error", "code"} envelope,
// 403 FORBIDDEN under F4-08 ruling G5(a) (was 401 UNAUTHORIZED).
const smokeLegacyCode = "FORBIDDEN"

func smoke(t *testing.T, h *Registered, p *Principal, want [7]int) {
	t.Helper()
	for i, r := range smokeRoutes {
		res, err := h.Do(p, r[0], r[1], nil)
		if err != nil {
			t.Fatalf("%s %s as %s: %v", r[0], r[1], p.Name, err)
		}
		switch want[i] {
		case http.StatusUnauthorized:
			code := "authentication_required"
			if p.Kind == "authoff" {
				code = smokeLegacyCode
			}
			h.Refused(res, want[i], code)
		case http.StatusForbidden:
			code := "permission_denied"
			if p.Kind == "authoff" {
				code = smokeLegacyCode
			}
			h.Refused(res, want[i], code)
		default:
			if res.Status != want[i] {
				t.Errorf("%s %s %s: %d %s, want %d", p.Name, r[0], r[1], res.Status, res.excerpt(), want[i])
			}
		}
	}
}

// TestRegisteredHarness_Smoke: six readings of the whole registered path, one per
// kind of caller, in numbers. Owner, administrator, editor, viewer, a key with
// scopes (and the core key that mirrors it), and an auth-off install.
func TestRegisteredHarness_Smoke(t *testing.T) {
	t.Run("admin editor viewer and keys", func(t *testing.T) {
		h := bootRegistered(t, registeredConfig{})
		for _, name := range []string{"session:admin", "session:editor", "session:viewer", "key:admin:view", "key:fp-worker:worker"} {
			t.Run(name, func(t *testing.T) { smoke(t, h, h.Principal(name), smokeStatuses[name]) })
		}
		// The key minted through core is the same caller as the imported one.
		t.Run("corekey:admin:view", func(t *testing.T) { smoke(t, h, h.Principal("corekey:admin:view"), smokeStatuses["key:admin:view"]) })
	})
	t.Run("owner", func(t *testing.T) {
		h := bootRegistered(t, registeredConfig{Owner: true})
		smoke(t, h, h.Principal(Owner), smokeStatuses[Owner])
	})
	t.Run("auth off", func(t *testing.T) {
		h := bootRegistered(t, registeredConfig{AuthOff: true})
		smoke(t, h, h.Principal(Authoff), smokeStatuses[Authoff])
	})
}

// ---- M2 + M3 ----

// TestRegisteredHarness_MigratedAccountsSignIn: the boot ran M2, M3 and M8, core
// holds exactly the census of accounts, an imported password hash signs in over
// core's own login route, and a wrong password, an unknown name and a disabled
// account are each refused for their own reason.
func TestRegisteredHarness_MigratedAccountsSignIn(t *testing.T) {
	h := bootRegistered(t, registeredConfig{})
	h.requireCutoverRan()

	raw, err := os.ReadFile(filepath.Join(repoRoot(t), reachRel, "verdicts.json"))
	if err != nil {
		t.Fatal(err)
	}
	var vf struct {
		Census struct {
			Users int `json:"users"`
		} `json:"census"`
	}
	if err := json.Unmarshal(raw, &vf); err != nil {
		t.Fatal(err)
	}
	if got := len(coreUsers(t, h)); got != vf.Census.Users {
		t.Errorf("core holds %d accounts, the corpus census says %d", got, vf.Census.Users)
	}

	login := func(user, password string) Result {
		t.Helper()
		body, err := json.Marshal(map[string]string{"username": user, "password": password})
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, loginRoute, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.RemoteAddr = h.nextPeer()
		return h.Raw(req)
	}
	// The hash HEAD stored, imported by M3, verified by core.
	if res := login("dick.grayson", passwordGotham); res.Status != http.StatusOK {
		t.Errorf("an imported password does not sign in: %d %s", res.Status, res.excerpt())
	}
	for name, c := range map[string]struct {
		user, password, problem string
	}{
		"wrong password":   {"dick.grayson", "wrong-password-0000", "invalid_credentials"},
		"unknown account":  {"nobody.here", passwordGotham, "invalid_credentials"},
		"disabled account": {"jason.todd", passwordGotham, "inactive_user"},
	} {
		t.Logf("%s", name)
		h.Refused(login(c.user, c.password), http.StatusUnauthorized, c.problem)
	}
}

// ---- the ABS surface ----

// TestRegisteredHarness_ABSAccessEntryPoint: the ABS surface's Cloudflare Access
// entry point (the one the B10e1c books-package cells could not reach), on the
// registered path. A listed person is admitted as the core user the Access resolver
// found or created, the same one every time; a person who is not listed is refused
// and creates nothing.
// absNotAuthorized is the ABS surface's refusal of a Cloudflare Access identity that
// maps to no account it may use.
const absNotAuthorized = "identity is not authorized for this server"

func TestRegisteredHarness_ABSAccessEntryPoint(t *testing.T) {
	h := bootRegistered(t, registeredConfig{Owner: true, ABSModes: "cf,jwt", AccessAllowed: []string{feListedNewbie}})
	me := func(sub, email string) Result {
		return h.absRequest(http.MethodGet, "/api/me", "", h.access.assertion(t, sub, email))
	}
	before := coreUsers(t, h)

	res := me("sub-newbie", feListedNewbie)
	id := absUserID(t, res)
	if id == "" {
		t.Fatalf("a listed person: %d %s", res.Status, res.excerpt())
	}
	after := coreUsers(t, h)
	if len(after) != len(before)+1 || after[id] != feListedNewbie {
		t.Errorf("core did not gain exactly the person: %d -> %d accounts, %q for %s", len(before), len(after), after[id], id)
	}
	if again := absUserID(t, me("sub-newbie", feListedNewbie)); again != id {
		t.Errorf("a second sign-in resolved to %q, want %q", again, id)
	}
	if n := len(coreUsers(t, h)); n != len(after) {
		t.Errorf("a second sign-in created an account: %d -> %d", len(after), n)
	}

	res = me("sub-joker", feJokerEmail)
	h.Refused(res, http.StatusForbidden, absNotAuthorized)
	if n := len(coreUsers(t, h)); n != len(after) {
		t.Errorf("a refused person created an account: %d -> %d", len(after), n)
	}
}

// TestRegisteredHarness_ABSAccessSameAddress: two proven administrators hold one
// address and only one is linked to Access by sub (the production install's shape).
// On the ABS surface the link wins, the same person under an e-mail-shaped subject
// or a subject core has never seen is refused (the address alone cannot choose
// between the two) and creates nothing, and the control (one plain holder, listed,
// unlinked) does link to its account.
func TestRegisteredHarness_ABSAccessSameAddress(t *testing.T) {
	h := bootRegistered(t, registeredConfig{Owner: true, ABSModes: "cf,jwt",
		AccessAllowed: []string{feBruceEmail, feAlfredEmail}, Seed: seedAbsFixtures})
	me := func(sub, email string) Result {
		return h.absRequest(http.MethodGet, "/api/me", "", h.access.assertion(t, sub, email))
	}
	before := coreUsers(t, h)
	for _, id := range []string{feBruceLinked, feBruceTwin, feAlfred} {
		if _, ok := before[id]; !ok {
			t.Fatalf("the seeded account %s did not arrive in core", id)
		}
	}
	if got := absUserID(t, me(feBruceSub, feBruceEmail)); got != feBruceLinked {
		t.Errorf("the linked subject signed in as %q, want the linked administrator %s", got, feBruceLinked)
	}
	for name, c := range map[string]struct{ sub, email string }{
		"the address as the subject": {feBruceEmail, feBruceEmail},
		"a subject core never saw":   {feFreshSub, feBruceEmail},
	} {
		t.Logf("%s", name)
		res := me(c.sub, c.email)
		if h.Refused(res, http.StatusForbidden, absNotAuthorized) && absUserID(t, res) != "" {
			t.Errorf("%s: a refusal that names a user: %s", name, res.excerpt())
		}
	}
	if got := absUserID(t, me(feAlfredSub, feAlfredEmail)); got != feAlfred {
		t.Errorf("the control signed in as %q, want its one holder %s", got, feAlfred)
	}
	if n := len(coreUsers(t, h)); n != len(before) {
		t.Errorf("a sign-in created or removed an account: %d -> %d", len(before), n)
	}
}

// TestRegisteredHarness_ABSKeys: what the ABS surface does with keys. An imported
// abk_ key is admitted as HEAD admitted it, a Confined one is limited to the worker
// API, a revoked one is refused, and a key core minted after the flip (mck_) is
// admitted, judged by core and never as an ABS token, as HEAD admitted its own
// application keys there.
func TestRegisteredHarness_ABSKeys(t *testing.T) {
	var revoked string
	h := bootRegistered(t, registeredConfig{Seed: func(ctx context.Context, lr storage.LegacyRange) (err error) {
		revoked, err = SeedAPIKey(ctx, lr, KeySeed{User: "lucius.fox", Status: "revoked", Scopes: []string{"library.view"}})
		return err
	}})
	bearer := func(raw string) Result { return h.absRequest(http.MethodGet, "/api/me", raw, "") }
	for name, want := range map[string]struct {
		status int
		code   string // of the refusal; "" for an admit
	}{
		"key:admin:view":       {http.StatusOK, ""},
		"key:viewer:view":      {http.StatusOK, ""},
		"key:admin:every":      {http.StatusOK, ""},
		"key:fp-worker:worker": {http.StatusForbidden, "this credential is limited to the fingerprint worker API"},
		"corekey:admin:view":   {http.StatusOK, ""},
	} {
		t.Logf("%s", name)
		res := bearer(h.Principal(name).bearer)
		if want.code == "" {
			if res.Status != want.status {
				t.Errorf("ABS /api/me as %s: %d %s, want %d", name, res.Status, res.excerpt(), want.status)
			}
		} else {
			h.Refused(res, want.status, want.code)
		}
	}
	t.Logf("revoked")
	h.Refused(bearer(revoked), http.StatusUnauthorized, "authentication required")
	if p := h.Principal("corekey:admin:view"); !strings.HasPrefix(p.bearer, "mck_") {
		t.Errorf("the core key is %q, not an mck_ key: the cell proves nothing", p.bearer[:4])
	}
}
