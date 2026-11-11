// file: core/auth/brief_test.go
// version: 1.0.0
// guid: 068d005d-674b-4819-93ce-0a8377ecd7b3
// last-edited: 2026-11-10

package auth_test

import (
	"context"
	"encoding/base64"
	"errors"
	"hash/crc32"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
)

// The 05-authn §10 rows F4-02a owns that the gate, middleware, login and user
// test files do not already carry, and the service-level guards the F4-02a
// mutant sweep found unpinned.

var nativeKinds = []auth.CredentialKind{auth.KindSession, auth.KindAPIKey, auth.KindBootstrap, auth.KindInvite, auth.KindTempLogin}

// crcOracle is the checksum written out independently of credential.go, as a
// secret scanner would compute it: the CRC32 (IEEE) of everything before the
// last "_", in base62, six characters, most significant first.
func crcOracle(body string) string {
	const alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	n := uint64(crc32.ChecksumIEEE([]byte(body)))
	out := make([]byte, 6)
	for i := 5; i >= 0; i-- {
		out[i] = alphabet[n%62]
		n /= 62
	}
	return string(out)
}

// wellFormed reports whether raw is a well-formed credential of kind by the
// oracle: prefix, 32 strict base64url bytes, "_", the checksum.
func wellFormed(kind auth.CredentialKind, raw string) bool {
	if len(raw) != 54 || !strings.HasPrefix(raw, string(kind)) || raw[47] != '_' {
		return false
	}
	dec, err := base64.RawURLEncoding.Strict().DecodeString(raw[4:47])
	return err == nil && len(dec) == 32 && raw[48:] == crcOracle(raw[:47])
}

// resum replaces raw's checksum with the oracle's for its (edited) body, so a
// case isolates the one rule it breaks.
func resum(body string) string { return body + "_" + crcOracle(body) }

// §4.1: a credential is its prefix, 32 random bytes base64url, "_" and a
// base62 CRC32 of the rest. A value without the prefix is not this kind's
// credential (ErrNoCredential: the next authenticator may try); a value with
// the prefix that breaks any other rule is a presented-but-invalid one
// (ErrInvalidCredentials: it never falls through to a weaker credential).
func TestCredentialFormat_ChecksumAndPrefix(t *testing.T) {
	if errors.Is(auth.ErrInvalidCredentials, auth.ErrNoCredential) || errors.Is(auth.ErrNoCredential, auth.ErrInvalidCredentials) {
		t.Fatal("ErrNoCredential and ErrInvalidCredentials must be distinct")
	}
	const b64url = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	for _, kind := range nativeKinds {
		raw, err := auth.MintCredential(kind)
		if err != nil {
			t.Fatal(err)
		}
		if !wellFormed(kind, raw) {
			t.Fatalf("%s: minted %q does not match the format a scanner checks", kind, raw)
		}
		if err := auth.ParseCredential(kind, raw); err != nil {
			t.Fatalf("%s: a minted credential does not parse: %v", kind, err)
		}
		if again, _ := auth.MintCredential(kind); again == raw {
			t.Fatalf("%s: two mints gave the same value", kind)
		}
		for _, other := range nativeKinds {
			if other != kind {
				if err := auth.ParseCredential(other, raw); !errors.Is(err, auth.ErrNoCredential) {
					t.Fatalf("a %s value parsed as %s: %v, want ErrNoCredential", kind, other, err)
				}
			}
		}

		body := raw[:47]
		last := strings.IndexByte(b64url, body[46])
		// The 43rd character of 32 bytes carries two padding bits that must
		// be zero; setting one is a different string for the same bytes.
		nonCanonical := body[:46] + string(b64url[last|1])
		sumChar := raw[53]
		other := byte('0')
		if sumChar == '0' {
			other = '1'
		}
		for name, bad := range map[string]string{
			"prefix only":       string(kind),
			"one short":         raw[:53],
			"one long":          raw + "A",
			"wrong checksum":    raw[:53] + string(other),
			"secret edited":     body[:10] + string(b64url[(strings.IndexByte(b64url, body[10])+1)%64]) + body[11:] + raw[47:],
			"no separator":      body + "-" + raw[48:],
			"outside base64url": resum(body[:10] + "+" + body[11:]),
			"padding bits set":  resum(nonCanonical),
		} {
			err := auth.ParseCredential(kind, bad)
			if !errors.Is(err, auth.ErrInvalidCredentials) || errors.Is(err, auth.ErrNoCredential) {
				t.Fatalf("%s %s (%q): %v, want ErrInvalidCredentials", kind, name, bad, err)
			}
		}
	}
	key, _ := auth.MintCredential(auth.KindAPIKey)
	for name, raw := range map[string]string{
		"empty":             "",
		"short of a prefix": "mc",
		"upper-case prefix": strings.ToUpper(key[:4]) + key[4:],
		"an imported key":   "abk_batcave",
		"a bearer header":   "Bearer " + key,
	} {
		if err := auth.ParseCredential(auth.KindAPIKey, raw); !errors.Is(err, auth.ErrNoCredential) {
			t.Fatalf("%s: %v, want ErrNoCredential", name, err)
		}
	}
	if _, err := auth.MintCredential("abk_"); !errors.Is(err, auth.ErrBadInput) {
		t.Fatalf("minting an imported prefix: %v", err)
	}
	// A value in the native shape under a non-native prefix is refused too:
	// imported keys are verified by their stored hash, never parsed.
	for _, raw := range []string{"abk_batcave", resum("abk_" + key[4:47])} {
		if err := auth.ParseCredential("abk_", raw); !errors.Is(err, auth.ErrInvalidCredentials) {
			t.Fatalf("parsing %q under a non-native prefix: %v", raw, err)
		}
	}
}

// FuzzParseCredential: whatever the input, ParseCredential answers
// ErrNoCredential exactly when the prefix is absent, nil only for a value the
// oracle calls well formed, and ErrInvalidCredentials otherwise.
func FuzzParseCredential(f *testing.F) {
	for i, k := range nativeKinds {
		raw, err := auth.MintCredential(k)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(uint8(i), raw)
		f.Add(uint8(i), raw[:53])
		f.Add(uint8(i), raw[:47]+"_000000")
		f.Add(uint8((i+1)%len(nativeKinds)), raw)
	}
	f.Add(uint8(0), "")
	f.Add(uint8(len(nativeKinds)), "abk_batcave")
	f.Fuzz(func(t *testing.T, ki uint8, raw string) {
		kinds := append(slices.Clone(nativeKinds), "abk_")
		kind := kinds[int(ki)%len(kinds)]
		err := auth.ParseCredential(kind, raw)
		switch {
		case !strings.HasPrefix(raw, string(kind)):
			if !errors.Is(err, auth.ErrNoCredential) || errors.Is(err, auth.ErrInvalidCredentials) {
				t.Fatalf("%s %q without the prefix: %v, want ErrNoCredential", kind, raw, err)
			}
		case err == nil:
			if !slices.Contains(nativeKinds, kind) || !wellFormed(kind, raw) {
				t.Fatalf("%s %q accepted but not well formed", kind, raw)
			}
		default:
			if !errors.Is(err, auth.ErrInvalidCredentials) || errors.Is(err, auth.ErrNoCredential) {
				t.Fatalf("%s %q: %v, want ErrInvalidCredentials", kind, raw, err)
			}
			if slices.Contains(nativeKinds, kind) && wellFormed(kind, raw) {
				t.Fatalf("%s %q is well formed but refused: %v", kind, raw, err)
			}
		}
	})
}

// §4.4: a key carries at least one scope. A nil or empty list is refused
// before rbac sees it (to Compile it would mean every role permission), and
// nothing is stored.
func TestAPIKey_CreateRequiresScopes(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	dick := f.user("dick", "gotham.detective")
	for name, scopes := range map[string][]rbac.Permission{"nil": nil, "empty": {}, "unknown": {"gotham:joker"}} {
		raw, k, err := f.svc.CreateAPIKey(ctx, f.session(dick), auth.KeySpec{Name: "grapnel", Scopes: scopes})
		if !errors.Is(err, auth.ErrBadInput) || raw != "" || k != nil {
			t.Fatalf("%s scopes: %q %v %v, want ErrBadInput and no key", name, raw, k, err)
		}
	}
	if n := countSeq(t, f.svc.APIKeys(ctx, dick.ID)); n != 0 {
		t.Fatalf("refused creations stored %d keys", n)
	}
	if _, err := f.svc.PrincipalFor(ctx, dick, auth.MethodAPIKey, nil, dick.CredentialEpoch); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Fatalf("a key principal without scopes: %v", err)
	}
	_, k := f.mint(f.session(dick), dick.ID, "gotham:patrol", "gotham:cases.read", "gotham:patrol")
	if want := []rbac.Permission{"gotham:cases.read", "gotham:patrol"}; !slices.Equal(k.Scopes, want) {
		t.Fatalf("stored scopes %v, want %v", k.Scopes, want)
	}
}

// §4.4 (regression for HEAD's handlers/apikeys.go:199-226): a key-
// authenticated caller mints only within its own key's scopes, never an empty
// list, never for another user, and never past its own key's expiry. The same
// user signed in mints what the roles allow, so the refusals are the key's.
func TestAPIKey_KeyCannotWiden(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	robin := f.user("robin", rbac.RoleEditor, "gotham.detective") // holds patrol, signal and cases.read
	dick := f.user("dick", "gotham.detective")
	_, k := f.mint(f.session(robin), robin.ID, "gotham:patrol")
	kp := f.keyPrincipal(robin, k)
	for name, scopes := range map[string][]rbac.Permission{
		"another scope": {"gotham:signal"},
		"a wider set":   {"gotham:patrol", "gotham:signal"},
	} {
		if _, _, err := f.svc.CreateAPIKey(ctx, kp, auth.KeySpec{Name: "grapnel", Scopes: scopes}); !errors.Is(err, auth.ErrScopeEscalation) {
			t.Fatalf("%s: %v, want ErrScopeEscalation", name, err)
		}
	}
	if _, _, err := f.svc.CreateAPIKey(ctx, kp, auth.KeySpec{Name: "grapnel"}); !errors.Is(err, auth.ErrBadInput) {
		t.Fatalf("an empty list from a key: %v, want ErrBadInput (never the key's whole set)", err)
	}
	if _, _, err := f.svc.CreateAPIKey(ctx, kp, auth.KeySpec{Owner: dick.ID, Name: "grapnel", Scopes: []rbac.Permission{"gotham:patrol"}}); !errors.Is(err, auth.ErrDenied) {
		t.Fatalf("a key minting for another user: %v, want ErrDenied", err)
	}
	if n := countSeq(t, f.svc.APIKeys(ctx, "")); n != 1 {
		t.Fatalf("%d keys stored, want only the parent", n)
	}
	f.advance(time.Hour)
	_, child, err := f.svc.CreateAPIKey(ctx, kp, auth.KeySpec{Name: "grapnel", Scopes: []rbac.Permission{"gotham:patrol"}, ExpiresIn: auth.DefaultKeyTTL})
	if err != nil {
		t.Fatalf("a key minting its own scope: %v", err)
	}
	if !child.ExpiresAt.Equal(k.ExpiresAt) {
		t.Fatalf("the child expires %v, past or before its parent's %v", child.ExpiresAt, k.ExpiresAt)
	}
	if _, _, err := f.svc.CreateAPIKey(ctx, f.session(robin), auth.KeySpec{Name: "grapnel", Scopes: []rbac.Permission{"gotham:signal"}}); err != nil {
		t.Fatalf("the signed-in owner minting a role scope: %v", err)
	}
}

// hookStore runs a hook around the store's last-use writes, so a revocation
// lands at a chosen point of the middleware's touch: before the touch is
// staged (between the request's read of the credential and its write) or
// after it (between the touch's stage and its commit).
type hookStore struct {
	auth.Store
	before, after func()
}

func (h *hookStore) TouchAPIKey(b *storage.Batch, id string, at time.Time, ip netip.Addr) error {
	if h.before != nil {
		h.before()
	}
	err := h.Store.TouchAPIKey(b, id, at, ip)
	if h.after != nil {
		h.after()
	}
	return err
}

func (h *hookStore) TouchSession(b *storage.Batch, id string, at time.Time) error {
	if h.before != nil {
		h.before()
	}
	err := h.Store.TouchSession(b, id, at)
	if h.after != nil {
		h.after()
	}
	return err
}

// over builds a service over st (a wrapper of f.st) with f's other deps.
func (f *fx) over(st auth.Store) *auth.Service {
	f.t.Helper()
	svc, err := auth.New(context.Background(), auth.Options{}, auth.Deps{
		Store: st, NewBatch: f.st.NewBatch, RBAC: f.rb, Audit: f.audit, Revocations: f.jrnl, Clock: f.clock, Hasher: f.hasher,
	})
	if err != nil {
		f.t.Fatal(err)
	}
	return svc
}

// hookAt sets the one-shot hook fn before or after the touch.
func hookAt(st *hookStore, before bool, fn func()) {
	var once sync.Once
	h := func() { once.Do(fn) }
	if before {
		st.before = h
	} else {
		st.after = h
	}
}

// §4.4, DATA6-8: a key's coalesced last-use write that races a revoke, on
// either side of its stage, leaves the key revoked with no use recorded, and
// the next request presenting it is refused as revoked. (The store-level
// halves are authtest.StoreSuite's TouchAPIKeyNeverUnrevokes and
// TouchAPIKeyLosesToRevoke.)
func TestKeyLastUseNeverRevertsRevoke(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	dick := f.user("dick", "gotham.detective")
	for _, before := range []bool{true, false} {
		raw, k := f.mint(f.session(dick), dick.ID, "gotham:patrol")
		st := &hookStore{Store: f.st}
		hookAt(st, before, func() {
			if err := f.svc.RevokeAPIKey(ctx, f.session(dick), k.ID); err != nil {
				t.Errorf("revoke: %v", err)
			}
		})
		svc := f.over(st)
		if _, got := serve(t, svc, request(raw, "")); got.p == nil || got.p.CredentialID != k.ID {
			t.Fatalf("before=%v: the in-flight request read the key live and should bind (failure %+v)", before, got.fail)
		}
		after := f.key(k.ID)
		if after.Status != auth.KeyRevoked || after.UseCount != 0 || !after.LastUsedAt.IsZero() || after.LastUsedIP.IsValid() {
			t.Fatalf("before=%v: after the raced touch the key is %+v", before, after)
		}
		f.advance(2 * time.Minute) // past the coalescer, so a touch would be attempted again
		if _, got := serve(t, svc, request(raw, "")); got.p != nil || got.fail.Code != auth.CodeRevoked {
			t.Fatalf("before=%v: the revoked key's next request: principal %v failure %+v", before, got.p, got.fail)
		}
		if got := f.key(k.ID); got.Status != auth.KeyRevoked || !got.RevokedAt.Equal(after.RevokedAt) || got.UseCount != 0 {
			t.Fatalf("before=%v: the key moved after its revoke: %+v", before, got)
		}
	}
}

// §4.3, DATA6-8: a session's LastSeen flush that races a logout or an admin's
// revoke, on either side of its stage, re-creates no session, and the token
// stops working. (The store-level halves are authtest.StoreSuite's
// TouchSessionNeverRecreates and TouchSessionLosesToDelete.)
func TestLastSeenFlushNeverResurrectsRevokedSession(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	bruce := f.user("bruce", rbac.RoleAdmin)
	dick := f.user("dick", "gotham.detective")
	for _, tc := range []struct {
		name   string
		by     *auth.User
		before bool
	}{
		{"logout before the flush is staged", dick, true},
		{"logout between the flush's stage and commit", dick, false},
		{"admin revoke before the flush is staged", bruce, true},
		{"admin revoke between the flush's stage and commit", bruce, false},
	} {
		is := f.issue(t, dick)
		st := &hookStore{Store: f.st}
		hookAt(st, tc.before, func() {
			if err := f.svc.RevokeSession(ctx, f.session(tc.by), is.PublicID); err != nil {
				t.Errorf("%s: revoke: %v", tc.name, err)
			}
		})
		svc := f.over(st)
		if _, got := serve(t, svc, request("", is.Token)); got.p == nil || got.p.CredentialID != is.PublicID {
			t.Fatalf("%s: the in-flight request read the session live and should bind (failure %+v)", tc.name, got.fail)
		}
		if _, err := f.st.SessionByPublicID(ctx, is.PublicID); !errors.Is(err, auth.ErrNotFound) {
			t.Fatalf("%s: the revoked session is back: %v", tc.name, err)
		}
		if n := countSeq(t, f.svc.Sessions(ctx, dick.ID)); n != 0 {
			t.Fatalf("%s: %d sessions listed after the revoke", tc.name, n)
		}
		f.advance(2 * time.Minute)
		if _, got := serve(t, svc, request("", is.Token)); got.p != nil || got.fail.Code != auth.CodeInvalidCredential || !got.fail.Cookie {
			t.Fatalf("%s: the revoked token's next request: principal %v failure %+v", tc.name, got.p, got.fail)
		}
	}
}

// §4.3, SEC-11, FE-6: two apps on one host get distinct default cookie
// names, so one app's cookie never reaches the other's stage 12. A foreign
// cookie, live or stale, is not read at all: the request is anonymous with no
// failure and reaches the (Public) route. The app's own stale cookie leaves
// the request anonymous too, and flags the failure as a cookie's, which is
// what stage 13 (F4-05) clears with SessionCookie's clearing form, under the
// app's own name.
func TestCookie_SiblingAppCookieDoesNotBlockLogin(t *testing.T) {
	f := newFx(t)
	dick := f.user("dick", "gotham.detective")
	gotham := f.withSettings(t, auth.Settings{SessionCookie: "gotham_session"})
	arkham := f.withSettings(t, auth.Settings{SessionCookie: "arkham_session"})
	is := f.issue(t, dick)
	stale, _ := auth.MintCredential(auth.KindSession)
	for https, prefix := range map[bool]string{true: "__Host-", false: ""} {
		c := auth.ClientInfo{HTTPS: https}
		g, a := gotham.SessionCookie(c, is.Token, is.ExpiresAt), arkham.SessionCookie(c, is.Token, is.ExpiresAt)
		if g.Name != prefix+"gotham_session" || a.Name != prefix+"arkham_session" {
			t.Fatalf("https=%v: cookie names %q and %q", https, g.Name, a.Name)
		}
	}
	for _, tc := range []struct {
		name   string
		svc    *auth.Service
		r      *http.Request
		bind   bool
		failed bool
	}{
		{"a sibling's live cookie is not read", gotham, cookieRequest(false, cookie("arkham_session", is.Token)), false, false},
		{"a sibling's stale cookie is not read", gotham, cookieRequest(false, cookie("arkham_session", stale)), false, false},
		{"a sibling's stale cookie beside the app's own", gotham, cookieRequest(false, cookie("arkham_session", stale), cookie("gotham_session", is.Token)), true, false},
		{"https: a sibling's stale __Host- cookie beside the app's own", gotham, cookieRequest(true, cookie("__Host-arkham_session", stale), cookie("__Host-gotham_session", is.Token)), true, false},
		{"the other way round", arkham, cookieRequest(true, cookie("__Host-gotham_session", stale), cookie("__Host-arkham_session", is.Token)), true, false},
		{"the core default name is not this app's", gotham, cookieRequest(false, cookie(plainCookie, is.Token)), false, false},
		{"the app's own stale cookie", gotham, cookieRequest(false, cookie("gotham_session", stale)), false, true},
	} {
		_, got := serve(t, tc.svc, tc.r)
		if !got.called {
			t.Fatalf("%s: the request did not reach the route", tc.name)
		}
		if tc.bind != (got.p != nil) || (tc.bind && got.p.CredentialID != is.PublicID) {
			t.Fatalf("%s: principal %+v, want bound=%v", tc.name, got.p, tc.bind)
		}
		if got.failed != tc.failed || (tc.failed && (got.fail.Code != auth.CodeInvalidCredential || !got.fail.Cookie)) {
			t.Fatalf("%s: failure %+v (recorded %v), want recorded=%v as a cookie's", tc.name, got.fail, got.failed, tc.failed)
		}
	}
	if gone := gotham.SessionCookie(auth.ClientInfo{}, "", time.Time{}); gone.Name != "gotham_session" || gone.Value != "" || gone.MaxAge >= 0 {
		t.Fatalf("the clearing cookie %+v does not clear the app's own name", gone)
	}
}

// putSpy records every key write a service stages.
type putSpy struct {
	auth.Store
	mu   sync.Mutex
	puts []auth.APIKey
}

func (p *putSpy) PutAPIKey(b *storage.Batch, k *auth.APIKey) error {
	p.mu.Lock()
	p.puts = append(p.puts, *k)
	p.mu.Unlock()
	return p.Store.PutAPIKey(b, k)
}

// §4.4: the service refuses to move a revoked key before it stages anything.
// The native store refuses the write as well; that is the second line, and a
// store that did not would otherwise un-revoke the key.
func TestAPIKey_RevokedIsRefusedBeforeAnyWrite(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	dick := f.user("dick", "gotham.detective")
	_, k := f.mint(f.session(dick), dick.ID, "gotham:patrol")
	if err := f.svc.RevokeAPIKey(ctx, f.session(dick), k.ID); err != nil {
		t.Fatal(err)
	}
	spy := &putSpy{Store: f.st}
	svc := f.over(spy)
	for _, to := range []auth.KeyStatus{auth.KeyActive, auth.KeyDisabled} {
		wantErr(t, "revoked to "+string(to), svc.SetAPIKeyStatus(ctx, f.session(dick), k.ID, to), auth.ErrKeyRevoked)
	}
	if err := svc.SetAPIKeyStatus(ctx, f.session(dick), k.ID, auth.KeyRevoked); err != nil {
		t.Fatalf("revoking a revoked key again: %v", err)
	}
	if len(spy.puts) != 0 {
		t.Fatalf("the service staged %d writes to a revoked key: %+v", len(spy.puts), spy.puts)
	}
}

// §4.4: a key past its expiry is refused on every path that reads it: stage
// 12 (expired, not invalid), an OwnAuth surface's CheckCredentialFor, and a
// mint by the key.
func TestAPIKey_ExpiredKeyIsRefused(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	dick := f.user("dick", "gotham.detective")
	raw, k, err := f.svc.CreateAPIKey(ctx, f.session(dick), auth.KeySpec{Name: "grapnel", Scopes: []rbac.Permission{"gotham:patrol"}, ExpiresIn: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	kp := f.keyPrincipal(dick, k)
	f.advance(time.Hour - time.Second)
	if err := f.svc.CheckCredentialFor(kp, rbac.Need("gotham:patrol")); err != nil {
		t.Fatalf("a live key: %v", err)
	}
	if _, got := serve(t, f.svc, request(raw, "")); got.p == nil {
		t.Fatalf("a live key did not bind: %+v", got.fail)
	}
	f.advance(time.Second) // exactly at ExpiresAt: expired
	if _, got := serve(t, f.svc, request(raw, "")); got.p != nil || got.fail.Code != auth.CodeExpired {
		t.Fatalf("an expired key: principal %v failure %+v", got.p, got.fail)
	}
	wantErr(t, "CheckCredentialFor", f.svc.CheckCredentialFor(kp, rbac.Need("gotham:patrol")), auth.ErrExpired)
	_, _, err = f.svc.CreateAPIKey(ctx, kp, auth.KeySpec{Name: "grapnel", Scopes: []rbac.Permission{"gotham:patrol"}})
	wantErr(t, "a mint by an expired key", err, auth.ErrExpired)
}

// 05-rbac §8: a revocation is audited as core.apikey.revoke; every other
// status change is core.apikey.set_status. Both name the key and carry the
// transition.
func TestAPIKey_StatusChangesAreAuditedByAction(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	dick := f.user("dick", "gotham.detective")
	_, k := f.mint(f.session(dick), dick.ID, "gotham:patrol")
	if err := f.svc.SetAPIKeyStatus(ctx, f.session(dick), k.ID, auth.KeyDisabled); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.RevokeAPIKey(ctx, f.session(dick), k.ID); err != nil {
		t.Fatal(err)
	}
	f.audit.mu.Lock()
	recs := slices.Clone(f.audit.recorded)
	f.audit.mu.Unlock()
	want := []struct{ action, from, to string }{
		{"core.apikey.create", "", ""},
		{"core.apikey.set_status", "active", "disabled"},
		{"core.apikey.revoke", "disabled", "revoked"},
	}
	if len(recs) != len(want) {
		t.Fatalf("recorded %v, want %d records", f.audit.recordedActions(), len(want))
	}
	for i, w := range want {
		r := recs[i]
		if r.Action != w.action || r.Target.ID != k.ID {
			t.Fatalf("record %d = %s on %q, want %s on %s", i, r.Action, r.Target.ID, w.action, k.ID)
		}
		if w.from != "" && (r.Details["from"] != w.from || r.Details["to"] != w.to) {
			t.Fatalf("record %d details %v, want %s -> %s", i, r.Details, w.from, w.to)
		}
	}
}

// §4.4, §4.13: a key manages only keys it may create, and no key re-enables
// one. Tim's case-files key can neither re-enable nor restrict his broader
// key; it may disable and revoke a case-files key of his, but not re-enable
// that one either. His interactive sign-in re-enables both.
func TestAPIKey_KeyManagesOnlyKeysItMayCreate(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	tim := f.user("tim-drake", "gotham.detective")
	me := f.session(tim)
	_, broad := f.mint(me, tim.ID, "gotham:patrol", "gotham:cases.read")
	_, narrow := f.mint(me, tim.ID, "gotham:cases.read")
	_, twin := f.mint(me, tim.ID, "gotham:cases.read")
	np := f.keyPrincipal(tim, narrow)
	status := func(id string, want auth.KeyStatus) {
		t.Helper()
		if got := f.key(id).Status; got != want {
			t.Fatalf("key %s is %s, want %s", id, got, want)
		}
	}

	if err := f.svc.SetAPIKeyStatus(ctx, me, broad.ID, auth.KeyDisabled); err != nil {
		t.Fatal(err)
	}
	wantErr(t, "the narrow key re-enables the broad one", f.svc.SetAPIKeyStatus(ctx, np, broad.ID, auth.KeyActive), auth.ErrScopeEscalation)
	wantErr(t, "the narrow key revokes the broad one", f.svc.RevokeAPIKey(ctx, np, broad.ID), auth.ErrScopeEscalation)
	status(broad.ID, auth.KeyDisabled)

	if err := f.svc.SetAPIKeyStatus(ctx, np, twin.ID, auth.KeyDisabled); err != nil {
		t.Fatalf("a key disabling a key it could create: %v", err)
	}
	wantErr(t, "a key re-enables a key it could create", f.svc.SetAPIKeyStatus(ctx, np, twin.ID, auth.KeyActive), auth.ErrDenied)
	status(twin.ID, auth.KeyDisabled)

	for _, id := range []string{broad.ID, twin.ID} {
		if err := f.svc.SetAPIKeyStatus(ctx, me, id, auth.KeyActive); err != nil {
			t.Fatalf("the owner's interactive re-enable of %s: %v", id, err)
		}
		status(id, auth.KeyActive)
	}
	if err := f.svc.RevokeAPIKey(ctx, np, twin.ID); err != nil {
		t.Fatalf("a key revoking a key it could create: %v", err)
	}
	status(twin.ID, auth.KeyRevoked)
}

// §4.13: Gordon disables Tim's broad key. Neither a narrower key of Tim's
// nor an equally broad one can undo it; Tim's interactive sign-in can, as at
// HEAD (PATCH /api-keys/:id refused every key).
func TestAPIKey_KeyCannotUndoAManagersDisable(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	gordon := f.user("jim-gordon", roleCommissioner)
	tim := f.user("tim-drake", "gotham.detective")
	me := f.session(tim)
	_, broad := f.mint(me, tim.ID, "gotham:patrol", "gotham:cases.read")
	_, peer := f.mint(me, tim.ID, "gotham:patrol", "gotham:cases.read")
	_, narrow := f.mint(me, tim.ID, "gotham:cases.read")
	if err := f.svc.SetAPIKeyStatus(ctx, f.session(gordon), broad.ID, auth.KeyDisabled); err != nil {
		t.Fatalf("the manager's disable: %v", err)
	}
	wantErr(t, "a narrower key", f.svc.SetAPIKeyStatus(ctx, f.keyPrincipal(tim, narrow), broad.ID, auth.KeyActive), auth.ErrScopeEscalation)
	wantErr(t, "an equally broad key", f.svc.SetAPIKeyStatus(ctx, f.keyPrincipal(tim, peer), broad.ID, auth.KeyActive), auth.ErrDenied)
	if got := f.key(broad.ID).Status; got != auth.KeyDisabled {
		t.Fatalf("a key undid the manager's disable: %s", got)
	}
	if err := f.svc.SetAPIKeyStatus(ctx, me, broad.ID, auth.KeyActive); err != nil {
		t.Fatalf("the owner's interactive re-enable: %v", err)
	}
}

// §4.4: the effective set a key manages with is read now, and the checker it
// presented bounds it too. Once Tim is a viewer, his wide key, through a
// checker compiled while he was a detective, may not revoke a case-files
// key; a checker compiled while he was a viewer may not either after he is
// promoted back. A checker compiled now may.
func TestAPIKey_KeyManagementReadsTheEffectiveSetNow(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	tim := f.user("tim-drake", "gotham.detective")
	_, wide := f.mint(f.session(tim), tim.ID, "gotham:patrol", "gotham:cases.read")
	_, files := f.mint(f.session(tim), tim.ID, "gotham:cases.read")
	stale := f.keyPrincipal(tim, wide)
	viewer := f.setRoles(tim.ID, rbac.RoleViewer)
	wantErr(t, "after a demotion", f.svc.RevokeAPIKey(ctx, stale, files.ID), auth.ErrScopeEscalation)
	narrowed := f.keyPrincipal(viewer, wide)
	detective := f.setRoles(tim.ID, "gotham.detective")
	wantErr(t, "a checker compiled before a promotion", f.svc.RevokeAPIKey(ctx, narrowed, files.ID), auth.ErrScopeEscalation)
	if got := f.key(files.ID).Status; got != auth.KeyActive {
		t.Fatalf("a refused revoke wrote %s", got)
	}
	if err := f.svc.RevokeAPIKey(ctx, f.keyPrincipal(detective, wide), files.ID); err != nil {
		t.Fatalf("a checker compiled now: %v", err)
	}
}
