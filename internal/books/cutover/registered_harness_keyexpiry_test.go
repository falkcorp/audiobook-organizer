// file: internal/books/cutover/registered_harness_keyexpiry_test.go
// version: 0.1.0
// guid: 5d0e7b34-9a62-4f1c-8c57-2b6f3a91e4d8
// last-edited: 2026-11-22

package cutover_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/storage"
)

// The expiry cell (F4-08 B10ke).
//
// HEAD never writes a key's expiry back into its status: a production snapshot
// held 590 apikey rows, 586 of them past their expires_at and still "active". M3
// imports them with their expires_at carried over (mapKey) and stamps Now+KeyTTL only
// on a live key that has none, and core refuses an expired key in keyUsable. Each is
// right when read alone; this cell proves the whole path. A key past its expiry that
// HEAD calls active must be refused on every surface after the flip, as an expired
// key and not as an unknown one (an unknown key means the import dropped it, which
// proves nothing about the expiry). If the carry were lost, or the stamp applied to an
// expired key, the 586 dead keys would answer again at the flip.
//
// Three keys of one ordinary HEAD user (a viewer), all status "active":
//
//	expired    expires_at 30 days ago
//	boundary   expires_at one minute ago
//	live       expires_at 30 days from now (the control: the same path must admit it)
//
// each is presented as a raw abk_ Bearer on a books route, an ABS route and a core
// route.

const (
	expiryUser = "barbara.gordon" // a viewer of the corpus

	expiredKeyName  = "Batcave night-watch key (expired)"
	boundaryKeyName = "Batcave night-watch key (one minute past)"
	liveKeyName     = "Batcave night-watch key (live)"

	expiryBooksRoute = "/api/v1/audiobooks"
	expiryABSRoute   = "/api/me"
	expiryCoreRoute  = "/api/core/v1/me"
)

// importedKeyView is the part of core's key listing the cell reads.
type importedKeyView struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Status     string     `json:"status"`
	Imported   bool       `json:"imported"`
	ExpiresAt  *time.Time `json:"expires_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
	UseCount   int64      `json:"use_count"`
}

func TestRegisteredHarness_ImportedKeyPastExpiryIsRefusedOnEverySurface(t *testing.T) {
	// The rows hold RFC 3339 seconds, so the instants are cut to the second here
	// and the imported record is compared with exactly what was written.
	now := time.Now().UTC().Truncate(time.Second)
	pastAt := now.AddDate(0, 0, -30)
	boundaryAt := now.Add(-time.Minute)
	liveAt := now.AddDate(0, 0, 30)

	var expired, boundary, live string
	h := bootRegistered(t, registeredConfig{Seed: func(ctx context.Context, lr storage.LegacyRange) (err error) {
		scopes := []string{"library.view"}
		for _, k := range []struct {
			raw  *string
			name string
			at   time.Time
		}{{&expired, expiredKeyName, pastAt}, {&boundary, boundaryKeyName, boundaryAt}, {&live, liveKeyName, liveAt}} {
			// Status is left at its default, "active": the HEAD shape under test.
			if *k.raw, err = SeedAPIKey(ctx, lr, KeySeed{User: expiryUser, Name: k.name, Scopes: scopes, ExpiresAt: k.at}); err != nil {
				return err
			}
		}
		return nil
	}})

	imported := func() map[string]importedKeyView {
		t.Helper()
		admin := h.Principal("session:admin")
		var users struct {
			Items []struct {
				ID       string `json:"id"`
				Username string `json:"username"`
			} `json:"items"`
		}
		h.coreJSON(admin, http.MethodGet, "/api/core/v1/users?limit=100", nil, &users)
		uid := ""
		for _, u := range users.Items {
			if u.Username == expiryUser {
				uid = u.ID
			}
		}
		if uid == "" {
			t.Fatalf("core lists no user %q", expiryUser)
		}
		var keys struct {
			Items []importedKeyView `json:"items"`
		}
		h.coreJSON(admin, http.MethodGet, "/api/core/v1/users/"+uid+"/api-keys?limit=100", nil, &keys)
		byName := map[string]importedKeyView{}
		for _, k := range keys.Items {
			byName[k.Name] = k
		}
		return byName
	}

	// The imported records, before any request: carried, not stamped.
	before := imported()
	for name, want := range map[string]time.Time{expiredKeyName: pastAt, boundaryKeyName: boundaryAt, liveKeyName: liveAt} {
		k, ok := before[name]
		if !ok {
			t.Fatalf("M3 did not import %q (core lists %d keys of %s): the cell proves nothing about its expiry", name, len(before), expiryUser)
		}
		if !k.Imported || k.Status != string(auth.KeyActive) {
			t.Errorf("%s: imported=%v status=%q, want an imported key with status %q (HEAD's status is not rewritten to expired)", name, k.Imported, k.Status, auth.KeyActive)
		}
		if k.ExpiresAt == nil || !k.ExpiresAt.Equal(want) {
			t.Errorf("%s: expires_at %v, want the seeded %s carried over (a key stamped Now+TTL would be alive)", name, k.ExpiresAt, want.Format(time.RFC3339))
		}
		t.Logf("imported %-44q status=%s expires_at=%v use_count=%d", name, k.Status, k.ExpiresAt, k.UseCount)
	}

	books := func(raw string) Result {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, expiryBooksRoute, nil)
		req.Header.Set("Authorization", "Bearer "+raw)
		req.RemoteAddr = h.nextPeer()
		return h.Raw(req)
	}
	absRoute := func(raw string) Result { return h.absRequest(http.MethodGet, expiryABSRoute, raw, "") }
	core := func(raw string) Result {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, expiryCoreRoute, nil)
		req.Header.Set("Authorization", "Bearer "+raw)
		req.RemoteAddr = h.nextPeer()
		return h.Raw(req)
	}

	// The refusal each surface speaks. Books and core are core's stage 12 and 13: 401
	// with auth.CodeExpired. The ABS surface authenticates itself (OwnAuth) and answers
	// every refused key in the same words, a revoked one and an unknown one included,
	// so there it is the 401 against the control's 200 that carries the proof; the
	// expired code on the other two surfaces, for these same keys, says why.
	surfaces := []struct {
		label string
		do    func(string) Result
		code  string
	}{
		{"books", books, auth.CodeExpired},
		{"abs", absRoute, "authentication required"},
		{"core", core, auth.CodeExpired},
	}
	for _, s := range surfaces {
		t.Run(s.label, func(t *testing.T) {
			for _, k := range []struct{ name, raw string }{{"expired", expired}, {"boundary", boundary}} {
				res := s.do(k.raw)
				t.Logf("%-8s -> %d %q", k.name, res.Status, problemCode(res))
				// Refused reports on the harness's own t; the subtest is marked too, so
				// the failure is named by its surface.
				if !h.Refused(res, http.StatusUnauthorized, s.code) {
					t.Fail()
				}
				if got := problemCode(res); got == auth.CodeInvalidCredential {
					t.Errorf("%s was refused as an unknown key: the import dropped it", k.name)
				}
			}
			res := s.do(live)
			t.Logf("%-8s -> %d %q", "live", res.Status, problemCode(res))
			if res.Status != http.StatusOK {
				t.Errorf("the live control: %d %s, want 200 (the path admits a key inside its expiry)", res.Status, res.excerpt())
			}
		})
	}

	// A key HEAD never wrote is refused as unknown on core's surfaces: the expired
	// code above is a different answer, not the one every refusal gets. The ABS
	// surface gives it the very words it gave the expired keys, which is why that
	// surface cannot name expiry and the two cells above do.
	stranger, _, err := store.GenerateAPIKeyToken()
	if err != nil {
		t.Fatal(err)
	}
	h.Refused(books(stranger), http.StatusUnauthorized, auth.CodeInvalidCredential)
	h.Refused(core(stranger), http.StatusUnauthorized, auth.CodeInvalidCredential)
	h.Refused(absRoute(stranger), http.StatusUnauthorized, "authentication required")

	// After the requests: the imported record is as it was. A refused key is not
	// stamped, not rewritten to another status and not counted as used.
	after := imported()
	for name, want := range map[string]time.Time{expiredKeyName: pastAt, boundaryKeyName: boundaryAt} {
		k := after[name]
		if k.Status != string(auth.KeyActive) || k.ExpiresAt == nil || !k.ExpiresAt.Equal(want) {
			t.Errorf("%s after its refusals: status %q expires_at %v, want active and %s untouched", name, k.Status, k.ExpiresAt, want.Format(time.RFC3339))
		}
		if k.UseCount != 0 || k.LastUsedAt != nil {
			t.Errorf("%s: a refused request was counted as a use: use_count=%d last_used_at=%v", name, k.UseCount, k.LastUsedAt)
		}
	}
	lk := after[liveKeyName]
	t.Logf("live key after its 3 admits: use_count=%d last_used_at=%v", lk.UseCount, lk.LastUsedAt)
}
