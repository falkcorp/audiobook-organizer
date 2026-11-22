// file: internal/books/identity_footprint_boot_test.go
// version: 0.1.0
// guid: c41d8e6a-52b7-4f93-8a06-7e2d9b1f35c8
// last-edited: 2026-11-22

package books

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/mediacore/auth/password"
)

// The "Sign-ins & data" drawer under core identity (F4-08 B10fp). HEAD answered
// GET /api/v1/users/:id/footprint from books' store; the flip answered it 409
// (merge_unavailable_under_identity) because the account, its sign-ins, keys and
// sessions had moved to core and the books handler had no read of them. Here the
// store is a HEAD store on disk that the flipped boot's M3 imports, so the
// account, the key and the sign-in the footprint reports are the ones core really
// holds, and the sessions are real logins through core.

const (
	fpPass    = "correct-horse-arkham-battery-1939"
	fpSubject = "sub-selina-kyle-0042" // the opaque Access sub; the footprint may only show its mask
)

type fpSeed struct {
	admin, viewer, target string
	keyRaw, keyHash       string
	// adminKeyRaw is an unscoped key of the administrator; adminScopedKeyRaw is
	// the same administrator's key scoped to users.manage alone.
	adminKeyRaw, adminScopedKeyRaw string
}

func seedFootprint(t *testing.T, s *database.PebbleStore, out *fpSeed) {
	t.Helper()
	algo, hash, err := password.New(password.Options{}).Hash(fpPass)
	require.NoError(t, err)
	mk := func(name, email string, roles []string) string {
		u, err := s.CreateUser(name, email, algo, hash, roles, "active")
		require.NoError(t, err)
		return u.ID
	}
	out.admin = mk("lucius.fox", "lucius.fox@wayne-enterprises.example.test", []string{"admin"})
	out.viewer = mk("alfred.pennyworth", "alfred.pennyworth@wayne-enterprises.example.test", []string{"viewer"})
	out.target = mk("selina.kyle", "selina.kyle@gcpd.example.test", []string{"viewer"})

	// Books' own families under the target's id: 2 books of listening state, 3
	// position rows, 1 bookmark, 2 preferences, 1 progress row.
	u, t0 := out.target, time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	put := func(key string, v any) {
		raw, err := json.Marshal(v)
		require.NoError(t, err)
		require.NoError(t, s.SetRaw(key, raw))
	}
	for _, book := range []string{"book-gotham-1", "book-gotham-2"} {
		put(database.UserBookStateKey(u, book), database.UserBookState{UserID: u, BookID: book, Status: database.UserBookStatusInProgress, ProgressPct: 10, LastActivityAt: t0})
		put(database.UserBookStateStatusKey(u, database.UserBookStatusInProgress, book), "1")
	}
	for _, seg := range []string{"s1", "s2"} {
		put(database.UserPositionBookPrefix(u, "book-gotham-1")+seg, database.UserPosition{UserID: u, BookID: "book-gotham-1", SegmentID: seg, PositionSeconds: 5, UpdatedAt: t0})
	}
	put(database.UserPositionBookPrefix(u, "book-gotham-2")+"s1", database.UserPosition{UserID: u, BookID: "book-gotham-2", SegmentID: "s1", PositionSeconds: 7, UpdatedAt: t0})
	put("bookmark:"+u+":item1:10", map[string]any{"UserID": u, "ItemID": "item1", "TimeSec": 10, "Title": "the rooftop"})
	put("pref:"+u+":theme", map[string]any{"key": "theme", "value": "dark"})
	put("pref:"+u+":speed", map[string]any{"key": "speed", "value": "1.5"})
	put("playp:"+u+":1", database.PlaybackProgress{UserID: u, BookID: 1, UpdatedAt: t0})

	// The administrator's keys: HEAD's handler admitted a key that may act as
	// admin (unscoped) and refused one scoped short of that (users.manage only).
	for _, k := range []struct {
		name   string
		scopes []string
		raw    *string
	}{
		{"batcomputer", nil, &out.adminKeyRaw},
		{"batsignal", []string{"users.manage"}, &out.adminScopedKeyRaw},
	} {
		raw, khash, err := database.GenerateAPIKeyToken()
		require.NoError(t, err)
		_, err = s.CreateAPIKey(&database.APIKey{UserID: out.admin, Name: k.name, TokenHash: khash, Scopes: k.scopes, Status: "active"})
		require.NoError(t, err)
		*k.raw = raw
	}

	// Core's families: one key and one sign-in (opaque sub) on the target.
	raw, khash, err := database.GenerateAPIKeyToken()
	require.NoError(t, err)
	_, err = s.CreateAPIKey(&database.APIKey{UserID: u, Name: "catwoman-claws", TokenHash: khash, Status: "active"})
	require.NoError(t, err)
	out.keyRaw, out.keyHash = raw, khash
	_, err = s.CreateOAuthIdentity(&database.OAuthIdentity{Provider: cfaccessProvider, Subject: fpSubject, Email: "selina.kyle@gcpd.example.test", UserID: u})
	require.NoError(t, err)
}

type fpBody struct {
	Data struct {
		User struct {
			ID, Username, Email string
			Roles               []string
			Status              string
			HasPassword         bool `json:"has_password"`
		} `json:"user"`
		Identities []struct {
			ID, Provider, Subject, Email string
			OwnerEmail                   bool `json:"owner_email"`
		} `json:"identities"`
		Counts         map[string]int `json:"counts"`
		ActiveSessions *int           `json:"active_sessions"`
		Unreadable     *[]string      `json:"unreadable"`
	} `json:"data"`
}

// An admin session reads a core account's footprint over the real composed app:
// HEAD's JSON shape, books' counts from books' store, core's counts from core, a
// masked subject, and the live sessions of real logins; and it refuses whoever
// HEAD's handler refused.
func TestIdentityBoot_FootprintAnswersUnderCoreIdentity(t *testing.T) {
	seed := &fpSeed{}
	fx := newFlipFixture(t, func(s *database.PebbleStore) { seedFootprint(t, s, seed) }, "")
	r := bootOverHEADStore(t, fx)
	h := r.app.Handler()

	admin := loginAs(t, r, "lucius.fox", fpPass)
	viewer := loginAs(t, r, "alfred.pennyworth", fpPass)
	// Two live sessions of the target; one more that is signed out again.
	loginAs(t, r, "selina.kyle", fpPass)
	loginAs(t, r, "selina.kyle", fpPass)
	gone := loginAs(t, r, "selina.kyle", fpPass)
	out := doJSON(t, h, http.MethodPost, "/api/core/v1/auth/logout", nil, gone)
	require.Less(t, out.Code, 300, out.Body.String())

	path := "/api/v1/users/" + seed.target + "/footprint"
	rec := doJSON(t, h, http.MethodGet, path, nil, admin)
	require.Equal(t, http.StatusOK, rec.Code, "the footprint must answer under core identity: %s", rec.Body.String())
	var fp fpBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &fp), rec.Body.String())
	d := fp.Data

	// Core's account.
	require.Equal(t, seed.target, d.User.ID)
	require.Equal(t, "selina.kyle", d.User.Username)
	require.Equal(t, "selina.kyle@gcpd.example.test", d.User.Email)
	require.Equal(t, []string{"viewer"}, d.User.Roles)
	require.True(t, d.User.HasPassword)

	// Counts: books' families from books' store, core's from core.
	want := map[string]int{
		"book_state": 2, "positions": 3, "bookmarks": 1, "preferences": 2, "playback_progress": 1, // books'
		"oauth_identities": 1, "api_keys": 1, // core's
	}
	for fam, n := range want {
		got, ok := d.Counts[fam]
		require.True(t, ok, "counts has no %q: %v", fam, d.Counts)
		require.Equal(t, n, got, "counts[%s] in %v", fam, d.Counts)
	}
	for _, fam := range []string{"sync_alias_use", "playback_events", "listen_stats", "playlists", "collections", "invites", "invite_grants"} {
		_, ok := d.Counts[fam]
		require.True(t, ok, "HEAD's shape has a %q count: %v", fam, d.Counts)
	}

	// The sign-in, masked: the provider and a masked subject, never the sub.
	require.Len(t, d.Identities, 1)
	require.Equal(t, cfaccessProvider, d.Identities[0].Provider)
	require.Equal(t, "su…42", d.Identities[0].Subject)
	require.NotEmpty(t, d.Identities[0].ID)

	// Sessions: the two still signed in, not the one signed out (M3 imports no
	// sessions, so every one is a real login).
	require.NotNil(t, d.ActiveSessions, "active_sessions is a member of HEAD's shape")
	require.Equal(t, 2, *d.ActiveSessions)
	require.NotNil(t, d.Unreadable, "unreadable is a member of HEAD's shape")

	// No credential byte: not the sub, not the key's token or hash, not the
	// password hash, not a session id.
	body := rec.Body.String()
	for _, secret := range []string{fpSubject, "selina-kyle-0042", seed.keyRaw, seed.keyHash, "$2a$", "$argon2", "$scrypt", "session_id"} {
		require.NotContains(t, body, secret, "the footprint leaks a credential-shaped value")
	}
	require.False(t, strings.Contains(body, admin.Value) || strings.Contains(body, viewer.Value))

	// The refusals HEAD made: a non-admin session, an API key (the target's own,
	// held by a non-admin), and a request with no credential.
	for _, c := range []struct {
		name string
		do   func() *httpResult
		want int
	}{
		{"a non-admin session", func() *httpResult { return &httpResult{doJSON(t, h, http.MethodGet, path, nil, viewer).Code} }, http.StatusForbidden},
		{"an api key of a non-admin", func() *httpResult { return &httpResult{withKey(r, http.MethodGet, path, seed.keyRaw).Code} }, http.StatusForbidden},
		{"an admin's key scoped to users.manage", func() *httpResult {
			return &httpResult{withKey(r, http.MethodGet, path, seed.adminScopedKeyRaw).Code}
		}, http.StatusForbidden},
		// The admission side: an unscoped key of the administrator may act as
		// admin, as HEAD's handler decided, so a refusal above is the scope's.
		{"an admin's unscoped key", func() *httpResult { return &httpResult{withKey(r, http.MethodGet, path, seed.adminKeyRaw).Code} }, http.StatusOK},
		{"no credential", func() *httpResult { return &httpResult{doJSON(t, h, http.MethodGet, path, nil, nil).Code} }, http.StatusUnauthorized},
	} {
		require.Equal(t, c.want, c.do().code, "%s must be refused as HEAD refused it", c.name)
	}
	// An account that does not exist is the handler's 404, behind the gate.
	nf := doJSON(t, h, http.MethodGet, "/api/v1/users/u-no-such-account/footprint", nil, admin)
	require.Equal(t, http.StatusNotFound, nf.Code, nf.Body.String())
}

type httpResult struct{ code int }
