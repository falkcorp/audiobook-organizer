// file: internal/server/handlers/abs/abs_test.go
// version: 1.5.0
// guid: 2c07b5e9-4d16-48fa-b930-71e5c8a04f6d
// last-edited: 2026-08-12

package abs_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/oauth"
	"github.com/falkcorp/audiobook-organizer/internal/server/absauth"
	abshandler "github.com/falkcorp/audiobook-organizer/internal/server/handlers/abs"
	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
	"github.com/falkcorp/audiobook-organizer/internal/syncapi/conformance"
	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
)

const testSecret = "0123456789abcdef0123456789abcdef"

// fixturesDir is the golden-fixture directory captured from a real ABS 2.36.0 server.
func fixturesDir() string { return filepath.Join("..", "..", "..", "..", "testdata", "abs-fixtures") }

// ── fake store ──────────────────────────────────────────────────────────────

type fakeStore struct {
	mu         sync.Mutex
	users      map[string]*database.User
	byUsername map[string]*database.User
	byEmail    map[string]*database.User
	identities map[string]*database.OAuthIdentity
	sessions   map[string]*database.ABSSession
	byHash     map[string]string
	roles      map[string]*database.Role
	nextID     int

	// failure injection
	sessionLookupErr error
	byHashErr        error
	createSessionErr error
	updateErr        error
	updatedUsers     []string
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		users:      map[string]*database.User{},
		byUsername: map[string]*database.User{},
		byEmail:    map[string]*database.User{},
		identities: map[string]*database.OAuthIdentity{},
		sessions:   map[string]*database.ABSSession{},
		byHash:     map[string]string{},
		roles:      map[string]*database.Role{},
	}
}

func (f *fakeStore) addUser(u *database.User) *database.User {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.users[u.ID] = u
	f.byUsername[strings.ToLower(u.Username)] = u
	if u.Email != "" {
		f.byEmail[strings.ToLower(u.Email)] = u
	}
	return u
}

func (f *fakeStore) CountUsers() (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.users), nil
}

func (f *fakeStore) GetUserByID(id string) (*database.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.users[id], nil
}

func (f *fakeStore) GetUserByUsername(u string) (*database.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.byUsername[strings.ToLower(u)], nil
}

func (f *fakeStore) GetUserByEmail(e string) (*database.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.byEmail[strings.ToLower(e)], nil
}

func (f *fakeStore) CreateUser(username, email, algo, hash string, roles []string, status string) (*database.User, error) {
	f.mu.Lock()
	f.nextID++
	id := fmt.Sprintf("jit-%d", f.nextID)
	f.mu.Unlock()
	return f.addUser(&database.User{
		ID: id, Username: username, Email: email, PasswordHashAlgo: algo, PasswordHash: hash,
		Roles: roles, Status: status, CreatedAt: time.Now(),
	}), nil
}

func (f *fakeStore) UpdateUser(u *database.User) error {
	if f.updateErr != nil {
		return f.updateErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.users[u.ID] = u
	f.updatedUsers = append(f.updatedUsers, u.ID)
	return nil
}

func (f *fakeStore) GetOAuthIdentityByProviderSubject(p, s string) (*database.OAuthIdentity, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.identities[p+"|"+s], nil
}

func (f *fakeStore) CreateOAuthIdentity(i *database.OAuthIdentity) (*database.OAuthIdentity, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	i.ID = i.Provider + "|" + i.Subject
	f.identities[i.ID] = i
	return i, nil
}

func (f *fakeStore) GetRoleByID(id string) (*database.Role, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.roles[id], nil
}

func (f *fakeStore) CreateABSSession(s *database.ABSSession) error {
	if f.createSessionErr != nil {
		return f.createSessionErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := *s
	f.sessions[s.ID] = &cp
	if s.RefreshTokenHash != "" {
		f.byHash[s.RefreshTokenHash] = s.ID
	}
	return nil
}

func (f *fakeStore) GetABSSession(id string) (*database.ABSSession, error) {
	if f.sessionLookupErr != nil {
		return nil, f.sessionLookupErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.sessions[id]
	if !ok {
		return nil, nil
	}
	cp := *s
	return &cp, nil
}

func (f *fakeStore) GetABSSessionByRefreshHash(h string) (*database.ABSSession, error) {
	if f.byHashErr != nil {
		return nil, f.byHashErr
	}
	f.mu.Lock()
	id, ok := f.byHash[h]
	f.mu.Unlock()
	if !ok {
		return nil, nil
	}
	s, err := f.GetABSSession(id)
	if err != nil || s == nil {
		return nil, err
	}
	if h != s.RefreshTokenHash && h != s.PrevRefreshTokenHash {
		return nil, nil
	}
	return s, nil
}

func (f *fakeStore) UpdateABSSession(s *database.ABSSession) error {
	if f.updateErr != nil {
		return f.updateErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := *s
	f.sessions[s.ID] = &cp
	for _, h := range []string{s.RefreshTokenHash, s.PrevRefreshTokenHash} {
		if h != "" {
			f.byHash[h] = s.ID
		}
	}
	return nil
}

func (f *fakeStore) ListABSSessionsForUser(userID string) ([]database.ABSSession, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []database.ABSSession{}
	for _, s := range f.sessions {
		if s.UserID == userID {
			out = append(out, *s)
		}
	}
	return out, nil
}

func (f *fakeStore) RevokeABSSession(id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if s, ok := f.sessions[id]; ok {
		s.Revoked = true
	}
	return nil
}

func (f *fakeStore) RevokeAllABSSessionsForUser(userID string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, s := range f.sessions {
		if s.UserID == userID && !s.Revoked {
			s.Revoked = true
			n++
		}
	}
	return n, nil
}

// ── fake CF verifier ────────────────────────────────────────────────────────

type fakeVerifier struct {
	byToken map[string]*oauth.IdentityClaims
}

func (f *fakeVerifier) Verify(_ context.Context, raw string) (*oauth.IdentityClaims, error) {
	if c, ok := f.byToken[raw]; ok {
		return c, nil
	}
	return nil, errors.New("cfaccess: verify jwt: signature is invalid")
}

// ── fake user-data provider (Phase 6 stand-in) ──────────────────────────────

type fakeUserData struct {
	progress        []any
	bookmarks       []any
	progressFor     map[string]any
	listenedSeconds float64
	err             error
}

func (f *fakeUserData) MediaProgress(string) ([]any, error) { return f.progress, f.err }
func (f *fakeUserData) Bookmarks(string) ([]any, error)     { return f.bookmarks, f.err }

// MediaProgressFor mirrors the real provider closely enough for handler tests: the
// SAME error is reported (so a broken provider still yields a 5xx rather than a 404,
// which is the distinction GET /api/me/progress/:id exists to preserve), and a
// missing row is reported as ok=false rather than as an empty object.
// ListenedSeconds mirrors the real provider's forgiving contract: statistics are
// cosmetic, so a broken store reports 0 rather than failing the request.
func (f *fakeUserData) ListenedSeconds(string) (float64, error) {
	if f.err != nil {
		return 0, f.err
	}
	return f.listenedSeconds, nil
}

func (f *fakeUserData) MediaProgressFor(_, bookID string) (any, bool, error) {
	if f.err != nil {
		return nil, false, f.err
	}
	if f.progressFor == nil {
		return nil, false, nil
	}
	row, ok := f.progressFor[bookID]
	return row, ok, nil
}

// ── harness ─────────────────────────────────────────────────────────────────

type harness struct {
	router   *gin.Engine
	store    *fakeStore
	cfg      *absauth.Config
	verifier *fakeVerifier
	handler  *abshandler.Handler
}

type harnessOpt func(*abshandler.Options)

func withUserData(p abshandler.UserDataProvider) harnessOpt {
	return func(o *abshandler.Options) { o.UserData = p }
}

func newHarness(t *testing.T, modes string, allowed []string, opts ...harnessOpt) *harness {
	t.Helper()
	gin.SetMode(gin.TestMode)
	cfg, err := absauth.Load(absauth.Settings{Enabled: true, JWTSecret: testSecret, AuthModes: modes})
	if err != nil {
		t.Fatalf("absauth.Load: %v", err)
	}
	store := newFakeStore()
	verifier := &fakeVerifier{byToken: map[string]*oauth.IdentityClaims{}}
	resolver := servermiddleware.NewABSIdentityResolver(cfg, verifier, oauth.New(oauth.Config{AllowedEmails: allowed, DefaultRole: "viewer"}), store)

	o := abshandler.Options{Config: cfg, Store: store, Resolver: resolver}
	for _, fn := range opts {
		fn(&o)
	}
	h, err := abshandler.New(o)
	if err != nil {
		t.Fatalf("abs.New: %v", err)
	}
	h.SetSleep(func(time.Duration) {})

	r := gin.New()
	h.Register(r)
	return &harness{router: r, store: store, cfg: cfg, verifier: verifier, handler: h}
}

type request struct {
	method  string
	path    string
	body    any
	headers map[string]string
}

func (h *harness) do(t *testing.T, req request) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	var rdr *bytes.Reader
	if req.body != nil {
		raw, err := json.Marshal(req.body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		rdr = bytes.NewReader(raw)
	} else {
		rdr = bytes.NewReader(nil)
	}
	r := httptest.NewRequest(req.method, req.path, rdr)
	// §1.8.8 item 9: these clients send Content-Type: application/json on EVERY
	// request, including bodyless GET and DELETE. Reproduce that faithfully.
	r.Header.Set("Content-Type", "application/json")
	for k, v := range req.headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.router.ServeHTTP(w, r)

	var decoded map[string]any
	if w.Body.Len() > 0 {
		_ = json.Unmarshal(w.Body.Bytes(), &decoded)
	}
	return w, decoded
}

func (h *harness) seedPasswordUser(t *testing.T, id, username, password string) *database.User {
	t.Helper()
	return h.seedUser(t, id, username, username+"@example.com", password)
}

func (h *harness) seedUser(t *testing.T, id, username, email, password string) *database.User {
	t.Helper()
	algo, hash, err := absauth.HashPassword(password)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	return h.store.addUser(&database.User{
		ID: id, Username: username, Email: email, Status: "active",
		PasswordHashAlgo: algo, PasswordHash: hash, Roles: []string{"viewer"},
		// A fixed, plausible creation time so the ms-epoch assertions are stable.
		CreatedAt: time.UnixMilli(1785370133398),
	})
}

// fixtureUserData mirrors the single progress row and single bookmark the ABS 2.36.0
// oracle held when the golden fixtures were captured, so a conformance diff compares
// like with like (the differ checks array lengths as well as element types).
func fixtureUserData() *fakeUserData {
	return &fakeUserData{
		progress: []any{map[string]any{
			"id":            "537554d4-87c4-420e-ad67-256cf29d8f86",
			"libraryItemId": "68929fc9-e296-4d25-b3aa-1c2930efd00d",
			"userId":        "e1c3fe66-990b-44f0-8ed6-2bf73cc8cd86",
			"mediaItemId":   "a0b6c2b4-1c38-494c-b793-ce131d283855",
			"mediaItemType": "book", "duration": 9975.48, "progress": 0.0012530786750876493,
			"currentTime": 42, "isFinished": false, "hideFromContinueListening": false,
			"ebookLocation": nil, "ebookProgress": 0, "episodeId": nil,
			"lastUpdate": int64(1785370430282), "startedAt": int64(1785370279345), "finishedAt": nil,
		}},
		bookmarks: []any{map[string]any{
			"libraryItemId": "68929fc9-e296-4d25-b3aa-1c2930efd00d",
			"title":         "conformance bookmark", "time": 100, "createdAt": int64(1785370279374),
		}},
	}
}

// newConformanceHarness matches the oracle's own state: the fixture user has a NULL
// email and one progress row plus one bookmark. Our code returns a real string when
// the user has an email, which is also what ABS does — the fixture just happens to
// have captured an email-less account.
func newConformanceHarness(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t, "cf,jwt", nil, withUserData(fixtureUserData()))
	h.seedUser(t, "u1", "oracle", "", "pw-pw-pw-pw")
	return h
}

// login performs a password login and returns the decoded body.
func (h *harness) login(t *testing.T, username, password string) map[string]any {
	t.Helper()
	w, body := h.do(t, request{
		method: http.MethodPost, path: "/login",
		body:    map[string]any{"username": username, "password": password},
		headers: map[string]string{"x-return-tokens": "true"},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("login failed: %d %s", w.Code, w.Body.String())
	}
	return body
}

func userObj(t *testing.T, body map[string]any) map[string]any {
	t.Helper()
	u, ok := body["user"].(map[string]any)
	if !ok {
		t.Fatalf("response has no user object: %v", body)
	}
	return u
}

func str(t *testing.T, m map[string]any, key string) string {
	t.Helper()
	v, ok := m[key].(string)
	if !ok || v == "" {
		t.Fatalf("%s must be a non-empty string, got %#v", key, m[key])
	}
	return v
}

// assertConformant diffs a response body against the golden ABS fixture, checking
// presence, type AND VALUE of every field the real server returned.
//
// CompareValues was off until 2026-08-12, which made this a shape check: a handler
// that returned every correct key with entirely wrong data passed. Twenty-two call
// sites read as a conformance suite and gated almost nothing.
//
// Strictness is now the DEFAULT, and weakening it is what costs effort — a call site
// that cannot meet the oracle yet must say so by name via assertConformantPending.
// The old arrangement had that backwards: a new endpoint got the weak gate for free,
// and turning it up required someone to remember. Nobody was going to.
func assertConformant(t *testing.T, fixture string, got any) {
	t.Helper()
	f, err := conformance.LoadFixture(filepath.Join(fixturesDir(), fixture))
	if err != nil {
		t.Fatalf("LoadFixture(%s): %v", fixture, err)
	}
	findings := f.CompareBody(got, conformance.Options{IgnoreExtra: true, CompareValues: true})
	if len(findings) > 0 {
		for _, fi := range findings {
			t.Errorf("%s: %s", fixture, fi)
		}
		t.Fatalf("%s: %d conformance findings against the real ABS 2.36.0 response", fixture, len(findings))
	}
}

// ── /ping and /status ───────────────────────────────────────────────────────

// TestPing_200WithoutAuth pins §1.7.3 item 14: /ping gates Absorb's whole
// online/offline state machine and must answer 200 with no credential.
func TestPing_200WithoutAuth(t *testing.T) {
	h := newHarness(t, "cf,jwt", nil)
	w, body := h.do(t, request{method: http.MethodGet, path: "/ping"})
	if w.Code != http.StatusOK {
		t.Fatalf("got %d want 200", w.Code)
	}
	if body["success"] != true {
		t.Fatalf(`/ping must return {"success": true} with a real JSON boolean, got %#v`, body["success"])
	}
	assertConformant(t, "get_ping.json", body)
}

func TestStatus_ShapeAndVersion(t *testing.T) {
	h := newHarness(t, "cf,jwt", nil)
	w, body := h.do(t, request{method: http.MethodGet, path: "/status"})
	if w.Code != http.StatusOK {
		t.Fatalf("got %d want 200", w.Code)
	}
	// §1.8.8 item 6: >= 2.22.0 suppresses AudioBooth's nag banner.
	if body["serverVersion"] != "2.36.0" {
		t.Fatalf("serverVersion must be 2.36.0, got %#v", body["serverVersion"])
	}
	if body["isInit"] != true {
		t.Fatalf("isInit must be the boolean true, got %#v", body["isInit"])
	}
	if _, ok := body["authMethods"].([]any); !ok {
		t.Fatalf("authMethods must be an array, got %#v", body["authMethods"])
	}
	assertConformant(t, "get_status.json", body)
}

// TestNoRouteIsNotHTML: every ABS route must be a real registered route, because the
// SPA NoRoute catch-all would otherwise answer 200 with index.html, which is fatal
// (§1.8.6, §1.7.3 item 11).
func TestRegisteredRoutesAreReal(t *testing.T) {
	h := newHarness(t, "cf,jwt", nil)
	want := map[string][]string{
		"GET":    {"/ping", "/status", "/api/me", "/api/me/sessions"},
		"POST":   {"/login", "/auth/refresh", "/logout"},
		"DELETE": {"/api/me/sessions/:id"},
	}
	registered := map[string]bool{}
	for _, ri := range h.router.Routes() {
		registered[ri.Method+" "+ri.Path] = true
	}
	for method, paths := range want {
		for _, p := range paths {
			if !registered[method+" "+p] {
				t.Errorf("%s %s is not registered — the SPA NoRoute catch-all would swallow it", method, p)
			}
		}
	}
}

// ── /login, Mode B (password) ───────────────────────────────────────────────

func TestLogin_PasswordSuccessConformsToFixture(t *testing.T) {
	h := newConformanceHarness(t)
	body := h.login(t, "oracle", "pw-pw-pw-pw")
	assertConformantExcept(t, "post_login.json", body,
		mergeAllowances(t, identityAllowances(), sourceAllowance()))
}

// TestLogin_UserDefaultLibraryIdIsNonNullString is a LOGIN BLOCKER (§1.8.2):
// AudioBooth decodes userDefaultLibraryId non-optionally, so null makes the app
// unable to log in at all. abs-shim emits null here; we must not copy it.
func TestLogin_UserDefaultLibraryIdIsNonNullString(t *testing.T) {
	h := newHarness(t, "cf,jwt", nil)
	h.seedPasswordUser(t, "u1", "owner", "pw-pw-pw-pw")
	body := h.login(t, "owner", "pw-pw-pw-pw")
	v, ok := body["userDefaultLibraryId"]
	if !ok {
		t.Fatal("userDefaultLibraryId is absent — AudioBooth cannot log in")
	}
	s, ok := v.(string)
	if !ok || s == "" {
		t.Fatalf("userDefaultLibraryId must be a non-null String, got %#v", v)
	}
	// §1.7.1: 36-char UUID, never a 26-char ULID.
	if len(s) != 36 {
		t.Fatalf("userDefaultLibraryId must be a 36-char UUID, got %q (%d chars)", s, len(s))
	}
}

// TestLogin_TokensNestInsideUser pins §3.1: clients read user.accessToken,
// user.refreshToken and the legacy user.token.
func TestLogin_TokensNestInsideUser(t *testing.T) {
	h := newHarness(t, "cf,jwt", nil)
	h.seedPasswordUser(t, "u1", "owner", "pw-pw-pw-pw")
	body := h.login(t, "owner", "pw-pw-pw-pw")
	u := userObj(t, body)
	access := str(t, u, "accessToken")
	refresh := str(t, u, "refreshToken")
	legacy := str(t, u, "token")
	if legacy != access {
		t.Fatalf("the legacy user.token must be the access token (got %q vs %q)", legacy, access)
	}
	if !strings.HasPrefix(refresh, absauth.RefreshTokenPrefix) {
		t.Fatalf("refreshToken must carry the %q prefix, got %q", absauth.RefreshTokenPrefix, refresh)
	}
	if _, err := h.cfg.ParseAccessToken(access); err != nil {
		t.Fatalf("accessToken must be a real parseable JWT: %v", err)
	}
	// Top-level tokens must NOT be where clients look, but their absence inside user
	// would break both clients — assert nesting explicitly.
	if _, atTop := body["accessToken"]; atTop {
		t.Log("note: accessToken also present at top level; harmless, clients read user.accessToken")
	}
}

// TestLogin_RefreshTokenAlwaysPresent: §1.7.2 — omitting refreshToken sets Absorb's
// isLegacy flag and disables refresh PERMANENTLY for that server. Always emit it,
// whether or not x-return-tokens was sent.
func TestLogin_RefreshTokenAlwaysPresent(t *testing.T) {
	h := newHarness(t, "cf,jwt", nil)
	h.seedPasswordUser(t, "u1", "owner", "pw-pw-pw-pw")
	for _, hdr := range []map[string]string{nil, {"x-return-tokens": "true"}, {"x-return-tokens": "false"}} {
		w, body := h.do(t, request{
			method: http.MethodPost, path: "/login",
			body:    map[string]any{"username": "owner", "password": "pw-pw-pw-pw"},
			headers: hdr,
		})
		if w.Code != http.StatusOK {
			t.Fatalf("headers %v: got %d", hdr, w.Code)
		}
		u := userObj(t, body)
		if _, ok := u["refreshToken"].(string); !ok {
			t.Fatalf("headers %v: refreshToken must always be present (omitting it permanently disables refresh in Absorb)", hdr)
		}
	}
}

// TestLogin_DatesAreIntegerMsEpoch pins §1.8.5 item 1: AudioBooth installs
// `try container.decode(Int64.self)` for every Date. ISO-8601 strings and fractional
// floats are FATAL.
func TestLogin_DatesAreIntegerMsEpoch(t *testing.T) {
	h := newHarness(t, "cf,jwt", nil)
	h.seedPasswordUser(t, "u1", "owner", "pw-pw-pw-pw")
	body := h.login(t, "owner", "pw-pw-pw-pw")
	u := userObj(t, body)
	createdAt, ok := u["createdAt"].(float64)
	if !ok {
		t.Fatalf("createdAt must be a JSON number, got %T", u["createdAt"])
	}
	if createdAt != float64(int64(createdAt)) {
		t.Fatalf("createdAt must be an integer, got %v", createdAt)
	}
	// ms epoch, not seconds: 2001-09-09 in seconds is ~1e9, in ms ~1e12.
	if createdAt < 1e12 {
		t.Fatalf("createdAt must be a MILLISECOND epoch, got %v (looks like seconds)", createdAt)
	}
}

// TestLogin_BooleansAreRealJSONBooleans pins §1.8.5 item 10 / §1.7.3 item 6:
// 0/1/"true" throws in Swift and reads as false in Dart.
func TestLogin_BooleansAreRealJSONBooleans(t *testing.T) {
	h := newHarness(t, "cf,jwt", nil)
	h.seedPasswordUser(t, "u1", "owner", "pw-pw-pw-pw")
	body := h.login(t, "owner", "pw-pw-pw-pw")
	u := userObj(t, body)

	for _, key := range []string{"isActive", "isLocked", "hasOpenIDLink"} {
		if _, ok := u[key].(bool); !ok {
			t.Errorf("user.%s must be a real JSON boolean, got %T (%#v)", key, u[key], u[key])
		}
	}
	perms, ok := u["permissions"].(map[string]any)
	if !ok {
		t.Fatalf("user.permissions must be an object, got %T", u["permissions"])
	}
	// §1.8.2 / §1.7.3 item 6: update, delete and download are all REQUIRED.
	for _, key := range []string{"update", "delete", "download", "upload", "accessAllLibraries", "accessAllTags", "accessExplicitContent"} {
		v, present := perms[key]
		if !present {
			t.Errorf("permissions.%s is required and missing", key)
			continue
		}
		if _, ok := v.(bool); !ok {
			t.Errorf("permissions.%s must be a real JSON boolean, got %T (%#v)", key, v, v)
		}
	}

	settings, ok := body["serverSettings"].(map[string]any)
	if !ok {
		t.Fatalf("serverSettings must be an object, got %T", body["serverSettings"])
	}
	// §1.8.2: serverSettings.{id, version, sortingIgnorePrefix} are non-optional.
	if _, ok := settings["id"].(string); !ok {
		t.Errorf("serverSettings.id must be a String, got %#v", settings["id"])
	}
	if _, ok := settings["version"].(string); !ok {
		t.Errorf("serverSettings.version must be a String, got %#v", settings["version"])
	}
	if _, ok := settings["sortingIgnorePrefix"].(bool); !ok {
		t.Errorf("serverSettings.sortingIgnorePrefix must be a real JSON boolean, got %#v", settings["sortingIgnorePrefix"])
	}
}

// TestLogin_EreaderDevicesPresent: §1.8.2 lists ereaderDevices as non-optional on
// the authorize/login response.
func TestLogin_EreaderDevicesPresent(t *testing.T) {
	h := newHarness(t, "cf,jwt", nil)
	h.seedPasswordUser(t, "u1", "owner", "pw-pw-pw-pw")
	body := h.login(t, "owner", "pw-pw-pw-pw")
	if _, ok := body["ereaderDevices"].([]any); !ok {
		t.Fatalf("ereaderDevices must be an array, got %#v", body["ereaderDevices"])
	}
}

func TestLogin_WrongPasswordIs401AndMintsNoSession(t *testing.T) {
	h := newHarness(t, "cf,jwt", nil)
	h.seedPasswordUser(t, "u1", "owner", "pw-pw-pw-pw")
	w, _ := h.do(t, request{method: http.MethodPost, path: "/login",
		body: map[string]any{"username": "owner", "password": "wrong"}})
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("got %d want 401", w.Code)
	}
	if len(h.store.sessions) != 0 {
		t.Fatalf("a failed login must not create a session, got %+v", h.store.sessions)
	}
}

func TestLogin_UnknownUserIs401(t *testing.T) {
	h := newHarness(t, "cf,jwt", nil)
	w, _ := h.do(t, request{method: http.MethodPost, path: "/login",
		body: map[string]any{"username": "ghost", "password": "whatever"}})
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("got %d want 401", w.Code)
	}
}

// TestLogin_CredentiallessUserCannotPasswordLogin: a Cloudflare-JIT user has no
// password. Logging in as them with any password (including "") must fail.
func TestLogin_CredentiallessUserCannotPasswordLogin(t *testing.T) {
	h := newHarness(t, "cf,jwt", nil)
	h.store.addUser(&database.User{
		ID: "cfu", Username: "cfonly", Email: "cf@example.com", Status: "active",
		PasswordHashAlgo: absauth.AlgoOAuth, PasswordHash: "", CreatedAt: time.Now(),
	})
	for _, pw := range []string{"", " ", "anything"} {
		w, _ := h.do(t, request{method: http.MethodPost, path: "/login",
			body: map[string]any{"username": "cfonly", "password": pw}})
		if w.Code == http.StatusOK {
			t.Fatalf("password %q logged in a credential-less user", pw)
		}
	}
}

// TestLogin_BcryptUserIsRehashedToArgon2id pins spec §3.5's rehash-on-login
// migration, which must work without a flag day or a password reset.
func TestLogin_BcryptUserIsRehashedToArgon2id(t *testing.T) {
	h := newHarness(t, "cf,jwt", nil)
	raw, err := bcrypt.GenerateFromPassword([]byte("legacy-pw"), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("bcrypt: %v", err)
	}
	h.store.addUser(&database.User{
		ID: "legacy", Username: "legacy", Email: "legacy@example.com", Status: "active",
		PasswordHashAlgo: absauth.AlgoBcrypt, PasswordHash: string(raw),
		Roles: []string{"viewer"}, CreatedAt: time.Now(),
	})
	h.login(t, "legacy", "legacy-pw")

	u, _ := h.store.GetUserByID("legacy")
	if u.PasswordHashAlgo != absauth.AlgoArgon2id {
		t.Fatalf("expected rehash to argon2id, algo is %q", u.PasswordHashAlgo)
	}
	if ok, rehash := absauth.VerifyPassword(u.PasswordHashAlgo, u.PasswordHash, "legacy-pw"); !ok || rehash {
		t.Fatalf("rehashed credential must verify cleanly (ok=%v rehash=%v)", ok, rehash)
	}
	// And the user can still log in afterwards.
	h.login(t, "legacy", "legacy-pw")
}

// TestLogin_RehashFailureStillLetsTheUserIn: the rehash is an optimisation, not a
// gate. A store hiccup while re-storing must not deny a correct password.
func TestLogin_RehashFailureStillLetsTheUserIn(t *testing.T) {
	h := newHarness(t, "cf,jwt", nil)
	raw, _ := bcrypt.GenerateFromPassword([]byte("legacy-pw"), bcrypt.MinCost)
	h.store.addUser(&database.User{
		ID: "legacy", Username: "legacy", Status: "active",
		PasswordHashAlgo: absauth.AlgoBcrypt, PasswordHash: string(raw), CreatedAt: time.Now(),
	})
	h.store.updateErr = errors.New("pebble: write stalled")
	w, _ := h.do(t, request{method: http.MethodPost, path: "/login",
		body: map[string]any{"username": "legacy", "password": "legacy-pw"}})
	if w.Code != http.StatusOK {
		t.Fatalf("a failed rehash must not deny a correct password, got %d: %s", w.Code, w.Body.String())
	}
}

// TestLogin_ThrottleIs429AndNeverLocksTheAccount pins §1.9.4 item 3.
func TestLogin_ThrottleIs429AndNeverLocksTheAccount(t *testing.T) {
	h := newHarness(t, "cf,jwt", nil)
	h.seedPasswordUser(t, "u1", "owner", "pw-pw-pw-pw")
	for i := 0; i < absauth.MaxFailuresPerIP; i++ {
		w, _ := h.do(t, request{method: http.MethodPost, path: "/login",
			body: map[string]any{"username": "owner", "password": "wrong"}})
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: got %d want 401", i, w.Code)
		}
	}
	w, _ := h.do(t, request{method: http.MethodPost, path: "/login",
		body: map[string]any{"username": "owner", "password": "wrong"}})
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("an exhausted source IP must get 429, got %d", w.Code)
	}
	// The correct password from a DIFFERENT source must still work: the account is
	// never hard-locked.
	req := httptest.NewRequest(http.MethodPost, "/login",
		bytes.NewReader([]byte(`{"username":"owner","password":"pw-pw-pw-pw"}`)))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "203.0.113.9:5555"
	rec := httptest.NewRecorder()
	h.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("the real user from a clean IP must still log in, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestLogin_MalformedBodyIs400NotPanic(t *testing.T) {
	h := newHarness(t, "cf,jwt", nil)
	req := httptest.NewRequest(http.MethodPost, "/login", bytes.NewReader([]byte(`{not json`)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("got %d want 400", w.Code)
	}
	if w.Body.Len() == 0 {
		t.Fatal("an empty body is fatal to these decoders")
	}
}

// ── /login, Mode C (verified Cloudflare assertion skips the password) ────────

func TestLogin_VerifiedAssertionSkipsPasswordCheck(t *testing.T) {
	h := newHarness(t, "cf,jwt", []string{"owner@example.com"})
	h.verifier.byToken["good"] = &oauth.IdentityClaims{
		Provider: oauth.ProviderCFAccess, Subject: "cf|owner",
		Email: "owner@example.com", EmailVerified: true,
	}
	w, body := h.do(t, request{
		method: http.MethodPost, path: "/login",
		// No password at all: the edge already authenticated a real person.
		body:    map[string]any{},
		headers: map[string]string{oauth.CFAccessHeader: "good"},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("got %d want 200: %s", w.Code, w.Body.String())
	}
	u := userObj(t, body)
	// Token-shaped response in BOTH modes so no client can tell them apart (§3.0.1).
	str(t, u, "accessToken")
	str(t, u, "refreshToken")
	str(t, u, "token")
	if len(h.store.users) != 1 {
		t.Fatalf("expected one JIT-provisioned user, got %d", len(h.store.users))
	}
}

// TestLogin_InvalidAssertionIsHard401EvenWithGoodPassword: fail-closed. On the ABS
// surface a bad assertion is terminal, never a pass-through to the password path.
func TestLogin_InvalidAssertionIsHard401EvenWithGoodPassword(t *testing.T) {
	h := newHarness(t, "cf,jwt", []string{"owner@example.com"})
	h.seedPasswordUser(t, "u1", "owner", "pw-pw-pw-pw")
	w, _ := h.do(t, request{
		method: http.MethodPost, path: "/login",
		body:    map[string]any{"username": "owner", "password": "pw-pw-pw-pw"},
		headers: map[string]string{oauth.CFAccessHeader: "forged"},
	})
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("got %d want 401 — a forged assertion must not fall through to password login", w.Code)
	}
}

func TestLogin_NonAllowlistedAssertionIs403AndProvisionsNothing(t *testing.T) {
	h := newHarness(t, "cf,jwt", []string{"owner@example.com"})
	h.verifier.byToken["stranger"] = &oauth.IdentityClaims{
		Provider: oauth.ProviderCFAccess, Subject: "cf|stranger",
		Email: "stranger@example.com", EmailVerified: true,
	}
	w, _ := h.do(t, request{method: http.MethodPost, path: "/login",
		headers: map[string]string{oauth.CFAccessHeader: "stranger"}})
	if w.Code != http.StatusForbidden {
		t.Fatalf("got %d want 403", w.Code)
	}
	if len(h.store.users) != 0 {
		t.Fatalf("an unlisted email must never be auto-provisioned, got %+v", h.store.users)
	}
}

// TestLogin_CFOnlyModeRefusesPasswordLogin: hardening to ABS_AUTH_MODES=cf must
// actually close the password door.
func TestLogin_CFOnlyModeRefusesPasswordLogin(t *testing.T) {
	h := newHarness(t, "cf", []string{"owner@example.com"})
	h.seedPasswordUser(t, "u1", "owner", "pw-pw-pw-pw")
	w, _ := h.do(t, request{method: http.MethodPost, path: "/login",
		body: map[string]any{"username": "owner", "password": "pw-pw-pw-pw"}})
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("got %d want 401 in cf-only mode", w.Code)
	}
}

// ── /auth/refresh ───────────────────────────────────────────────────────────

func TestRefresh_RotatesAndConformsToFixture(t *testing.T) {
	h := newConformanceHarness(t)
	first := userObj(t, h.login(t, "oracle", "pw-pw-pw-pw"))
	oldRefresh := str(t, first, "refreshToken")

	w, body := h.do(t, request{method: http.MethodPost, path: "/auth/refresh",
		headers: map[string]string{"x-refresh-token": oldRefresh}})
	if w.Code != http.StatusOK {
		t.Fatalf("got %d: %s", w.Code, w.Body.String())
	}
	assertConformantExcept(t, "post_auth_refresh.json", body,
		mergeAllowances(t, identityAllowances(), sourceAllowance()))

	u := userObj(t, body)
	newAccess := str(t, u, "accessToken")
	newRefresh := str(t, u, "refreshToken")
	if newRefresh == oldRefresh {
		t.Fatal("refresh must rotate the refresh token")
	}
	// §1.8.8 item 5: accessToken must be a real parseable JWT with a numeric exp.
	claims, err := h.cfg.ParseAccessToken(newAccess)
	if err != nil {
		t.Fatalf("refreshed accessToken must be a parseable JWT: %v", err)
	}
	if claims.ExpiresAt.IsZero() {
		t.Fatal("refreshed accessToken must carry an exp")
	}
}

// TestRefresh_GraceWindowIsIdempotent is the subtle concurrency requirement of §3.4
// step 3: a concurrent or replayed refresh from the same device that never saw the
// new token must get the ALREADY-MINTED pair back, not a second rotation and not a
// 401.
func TestRefresh_GraceWindowIsIdempotent(t *testing.T) {
	h := newHarness(t, "cf,jwt", nil)
	h.seedPasswordUser(t, "u1", "owner", "pw-pw-pw-pw")
	first := userObj(t, h.login(t, "owner", "pw-pw-pw-pw"))
	oldRefresh := str(t, first, "refreshToken")

	_, body1 := h.do(t, request{method: http.MethodPost, path: "/auth/refresh",
		headers: map[string]string{"x-refresh-token": oldRefresh}})
	minted := str(t, userObj(t, body1), "refreshToken")

	// Replay the SAME old token.
	w2, body2 := h.do(t, request{method: http.MethodPost, path: "/auth/refresh",
		headers: map[string]string{"x-refresh-token": oldRefresh}})
	if w2.Code != http.StatusOK {
		t.Fatalf("a replay inside the grace window must succeed, got %d: %s", w2.Code, w2.Body.String())
	}
	replayed := str(t, userObj(t, body2), "refreshToken")
	if replayed != minted {
		t.Fatalf("grace replay must return the already-minted token %q, got %q (a second rotation orphans the other request)", minted, replayed)
	}
	// The already-minted token must still work afterwards.
	w3, _ := h.do(t, request{method: http.MethodPost, path: "/auth/refresh",
		headers: map[string]string{"x-refresh-token": minted}})
	if w3.Code != http.StatusOK {
		t.Fatalf("the current token must still refresh after a grace replay, got %d", w3.Code)
	}
}

// TestRefresh_ConcurrentRefreshesConverge exercises the per-session single-flight
// lock: N simultaneous refreshes of the same session must all succeed and must all
// agree on the resulting refresh token.
func TestRefresh_ConcurrentRefreshesConverge(t *testing.T) {
	h := newHarness(t, "cf,jwt", nil)
	h.seedPasswordUser(t, "u1", "owner", "pw-pw-pw-pw")
	first := userObj(t, h.login(t, "owner", "pw-pw-pw-pw"))
	oldRefresh := str(t, first, "refreshToken")

	const n = 8
	type result struct {
		code    int
		refresh string
	}
	results := make([]result, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			req := httptest.NewRequest(http.MethodPost, "/auth/refresh", bytes.NewReader(nil))
			req.Header.Set("x-refresh-token", oldRefresh)
			w := httptest.NewRecorder()
			h.router.ServeHTTP(w, req)
			var body map[string]any
			_ = json.Unmarshal(w.Body.Bytes(), &body)
			r := result{code: w.Code}
			if u, ok := body["user"].(map[string]any); ok {
				r.refresh, _ = u["refreshToken"].(string)
			}
			results[i] = r
		}(i)
	}
	close(start)
	wg.Wait()

	var token string
	for i, r := range results {
		if r.code != http.StatusOK {
			t.Fatalf("concurrent refresh %d got %d — simultaneous refreshes of one session must not fail", i, r.code)
		}
		if r.refresh == "" {
			t.Fatalf("concurrent refresh %d returned no refresh token", i)
		}
		if token == "" {
			token = r.refresh
			continue
		}
		if r.refresh != token {
			t.Fatalf("concurrent refreshes minted divergent tokens (%q vs %q) — the single-flight lock is not holding", token, r.refresh)
		}
	}
}

func TestRefresh_UnknownTokenIs401(t *testing.T) {
	h := newHarness(t, "cf,jwt", nil)
	w, _ := h.do(t, request{method: http.MethodPost, path: "/auth/refresh",
		headers: map[string]string{"x-refresh-token": "abr_nonexistent"}})
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("got %d want 401 for a genuinely dead refresh token", w.Code)
	}
}

func TestRefresh_MissingTokenIs401(t *testing.T) {
	h := newHarness(t, "cf,jwt", nil)
	w, _ := h.do(t, request{method: http.MethodPost, path: "/auth/refresh"})
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("got %d want 401", w.Code)
	}
}

func TestRefresh_RevokedSessionIs401(t *testing.T) {
	h := newHarness(t, "cf,jwt", nil)
	h.seedPasswordUser(t, "u1", "owner", "pw-pw-pw-pw")
	u := userObj(t, h.login(t, "owner", "pw-pw-pw-pw"))
	refresh := str(t, u, "refreshToken")
	for id := range h.store.sessions {
		_ = h.store.RevokeABSSession(id)
	}
	w, _ := h.do(t, request{method: http.MethodPost, path: "/auth/refresh",
		headers: map[string]string{"x-refresh-token": refresh}})
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("got %d want 401 after logout", w.Code)
	}
}

// TestRefresh_TransientErrorIs5xxNotUnauthorized is §1.7.3 item 3, the single most
// dangerous status-code rule on this endpoint: 401/403 forces a logout, so a 5xx or
// timeout MUST preserve the session.
func TestRefresh_TransientErrorIs5xxNotUnauthorized(t *testing.T) {
	h := newHarness(t, "cf,jwt", nil)
	h.seedPasswordUser(t, "u1", "owner", "pw-pw-pw-pw")
	u := userObj(t, h.login(t, "owner", "pw-pw-pw-pw"))
	refresh := str(t, u, "refreshToken")

	h.store.byHashErr = errors.New("pebble: temporarily unavailable")
	w, _ := h.do(t, request{method: http.MethodPost, path: "/auth/refresh",
		headers: map[string]string{"x-refresh-token": refresh}})
	if w.Code < 500 {
		t.Fatalf("got %d — a transient store failure must be 5xx, never 401/403 (that would log the user out)", w.Code)
	}

	// And the session survives: once the store recovers, the same token still works.
	h.store.byHashErr = nil
	w2, _ := h.do(t, request{method: http.MethodPost, path: "/auth/refresh",
		headers: map[string]string{"x-refresh-token": refresh}})
	if w2.Code != http.StatusOK {
		t.Fatalf("the session must survive a transient error, got %d: %s", w2.Code, w2.Body.String())
	}
}

// TestRefresh_AcceptsBodyAndHeaderForms: AudioBooth uses the x-refresh-token header;
// accept the body form too so a client that differs still works (superset rule).
func TestRefresh_AcceptsBodyForm(t *testing.T) {
	h := newHarness(t, "cf,jwt", nil)
	h.seedPasswordUser(t, "u1", "owner", "pw-pw-pw-pw")
	u := userObj(t, h.login(t, "owner", "pw-pw-pw-pw"))
	refresh := str(t, u, "refreshToken")
	w, _ := h.do(t, request{method: http.MethodPost, path: "/auth/refresh",
		body: map[string]any{"refreshToken": refresh}})
	if w.Code != http.StatusOK {
		t.Fatalf("got %d: %s", w.Code, w.Body.String())
	}
}

// TestRefresh_WithVerifiedAssertionAlwaysSucceeds pins §3.0.1: in Mode C identity
// arrives with every request, so ABS's "Session not found" logout class cannot occur
// — even with no refresh token at all.
func TestRefresh_WithVerifiedAssertionAlwaysSucceeds(t *testing.T) {
	h := newHarness(t, "cf,jwt", []string{"owner@example.com"})
	h.verifier.byToken["good"] = &oauth.IdentityClaims{
		Provider: oauth.ProviderCFAccess, Subject: "cf|owner",
		Email: "owner@example.com", EmailVerified: true,
	}
	for _, hdrs := range []map[string]string{
		{oauth.CFAccessHeader: "good"},
		{oauth.CFAccessHeader: "good", "x-refresh-token": "abr_totally-bogus"},
	} {
		w, body := h.do(t, request{method: http.MethodPost, path: "/auth/refresh", headers: hdrs})
		if w.Code != http.StatusOK {
			t.Fatalf("headers %v: got %d, want 200 — CF-backed refresh must always succeed: %s", hdrs, w.Code, w.Body.String())
		}
		u := userObj(t, body)
		str(t, u, "accessToken")
		str(t, u, "refreshToken")
	}
}

// ── /logout ─────────────────────────────────────────────────────────────────

func TestLogout_RevokesCurrentSessionOnly(t *testing.T) {
	h := newHarness(t, "cf,jwt", nil)
	h.seedPasswordUser(t, "u1", "owner", "pw-pw-pw-pw")
	a := userObj(t, h.login(t, "owner", "pw-pw-pw-pw"))
	b := userObj(t, h.login(t, "owner", "pw-pw-pw-pw"))
	accessA := str(t, a, "accessToken")
	refreshB := str(t, b, "refreshToken")

	w, body := h.do(t, request{method: http.MethodPost, path: "/logout",
		headers: map[string]string{"Authorization": "Bearer " + accessA}})
	if w.Code != http.StatusOK {
		t.Fatalf("got %d: %s", w.Code, w.Body.String())
	}
	if len(body) == 0 {
		t.Fatal("an empty 200 body is fatal to these decoders")
	}
	// Session A is dead...
	if w, _ := h.do(t, request{method: http.MethodGet, path: "/api/me",
		headers: map[string]string{"Authorization": "Bearer " + accessA}}); w.Code != http.StatusUnauthorized {
		t.Fatalf("logged-out session still authenticates: %d", w.Code)
	}
	// ...but the other device is untouched.
	if w, _ := h.do(t, request{method: http.MethodPost, path: "/auth/refresh",
		headers: map[string]string{"x-refresh-token": refreshB}}); w.Code != http.StatusOK {
		t.Fatalf("logging out one device must not affect another, got %d", w.Code)
	}
}

func TestLogout_AllDevicesRevokesEverySession(t *testing.T) {
	h := newHarness(t, "cf,jwt", nil)
	h.seedPasswordUser(t, "u1", "owner", "pw-pw-pw-pw")
	h.seedPasswordUser(t, "u2", "other", "pw-pw-pw-pw")
	a := userObj(t, h.login(t, "owner", "pw-pw-pw-pw"))
	b := userObj(t, h.login(t, "owner", "pw-pw-pw-pw"))
	stranger := userObj(t, h.login(t, "other", "pw-pw-pw-pw"))

	w, _ := h.do(t, request{method: http.MethodPost, path: "/logout?allDevices=1",
		headers: map[string]string{"Authorization": "Bearer " + str(t, a, "accessToken")}})
	if w.Code != http.StatusOK {
		t.Fatalf("got %d", w.Code)
	}
	for _, tok := range []string{str(t, a, "refreshToken"), str(t, b, "refreshToken")} {
		if w, _ := h.do(t, request{method: http.MethodPost, path: "/auth/refresh",
			headers: map[string]string{"x-refresh-token": tok}}); w.Code != http.StatusUnauthorized {
			t.Fatalf("allDevices logout left a session alive: %d", w.Code)
		}
	}
	// Another user is untouched.
	if w, _ := h.do(t, request{method: http.MethodPost, path: "/auth/refresh",
		headers: map[string]string{"x-refresh-token": str(t, stranger, "refreshToken")}}); w.Code != http.StatusOK {
		t.Fatalf("allDevices logout must be scoped to the calling user, got %d", w.Code)
	}
}

func TestLogout_RequiresAuth(t *testing.T) {
	h := newHarness(t, "cf,jwt", nil)
	w, _ := h.do(t, request{method: http.MethodPost, path: "/logout"})
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("got %d want 401", w.Code)
	}
}

// ── /api/me ─────────────────────────────────────────────────────────────────

func TestMe_ConformsToFixture(t *testing.T) {
	h := newConformanceHarness(t)
	access := str(t, userObj(t, h.login(t, "oracle", "pw-pw-pw-pw")), "accessToken")

	w, body := h.do(t, request{method: http.MethodGet, path: "/api/me?populated=1",
		headers: map[string]string{"Authorization": "Bearer " + access}})
	if w.Code != http.StatusOK {
		t.Fatalf("got %d: %s", w.Code, w.Body.String())
	}
	assertConformantExcept(t, "get_api_me.json", body, identityAllowances())
}

// TestMe_MediaProgressIsAlwaysAPresentArray is the DATA-LOSS guard of §1.8.1:
// AudioBooth DELETES every local progress row whose bookID is absent from
// user.mediaProgress. A missing key, a null, or a truncated list silently destroys
// the user's listening positions on every home-screen refresh.
func TestMe_MediaProgressIsAlwaysAPresentArray(t *testing.T) {
	for name, provider := range map[string]abshandler.UserDataProvider{
		"no provider wired": nil,
		"empty list":        &fakeUserData{progress: []any{}},
		"nil list":          &fakeUserData{progress: nil},
	} {
		t.Run(name, func(t *testing.T) {
			opts := []harnessOpt{}
			if provider != nil {
				opts = append(opts, withUserData(provider))
			}
			h := newHarness(t, "cf,jwt", nil, opts...)
			h.seedPasswordUser(t, "u1", "owner", "pw-pw-pw-pw")
			access := str(t, userObj(t, h.login(t, "owner", "pw-pw-pw-pw")), "accessToken")

			w, body := h.do(t, request{method: http.MethodGet, path: "/api/me",
				headers: map[string]string{"Authorization": "Bearer " + access}})
			if w.Code != http.StatusOK {
				t.Fatalf("got %d: %s", w.Code, w.Body.String())
			}
			raw, present := body["mediaProgress"]
			if !present {
				t.Fatal("mediaProgress key is absent — the Swift decoder requires it")
			}
			if raw == nil {
				t.Fatal("mediaProgress must never be null")
			}
			if _, ok := raw.([]any); !ok {
				t.Fatalf("mediaProgress must be an array, got %T", raw)
			}
		})
	}
}

// TestMe_ProviderErrorIs5xxNeverAnEmptyList is the other half of the §1.8.1 data-loss
// guard: when the progress store cannot answer, the ONLY safe response is 5xx. A 200
// carrying an empty mediaProgress we never actually read would make AudioBooth delete
// every local progress row. This applies to /login and /auth/refresh too, which carry
// the same array.
func TestMe_ProviderErrorIs5xxNeverAnEmptyList(t *testing.T) {
	broken := &fakeUserData{err: errors.New("progress store down")}
	h := newHarness(t, "cf,jwt", nil, withUserData(broken))
	h.seedPasswordUser(t, "u1", "owner", "pw-pw-pw-pw")

	// /login refuses first, so no client ever reaches /api/me with a bad provider.
	w, _ := h.do(t, request{method: http.MethodPost, path: "/login",
		body: map[string]any{"username": "owner", "password": "pw-pw-pw-pw"}})
	if w.Code < 500 {
		t.Fatalf("login got %d — must be 5xx rather than serve an unread progress list", w.Code)
	}

	// And once a token exists, /api/me refuses too.
	broken.err = nil
	access := str(t, userObj(t, h.login(t, "owner", "pw-pw-pw-pw")), "accessToken")
	broken.err = errors.New("progress store down")
	w2, body := h.do(t, request{method: http.MethodGet, path: "/api/me",
		headers: map[string]string{"Authorization": "Bearer " + access}})
	if w2.Code < 500 {
		t.Fatalf("/api/me got %d — must be 5xx, never a 200 with a list it could not read", w2.Code)
	}
	if _, leaked := body["mediaProgress"]; leaked {
		t.Fatal("the error response must not carry a mediaProgress array at all")
	}
}

// TestMe_MediaProgressIsNeverPaginated: whatever the provider returns is passed
// through complete. Any limit/page query parameter must be IGNORED here.
func TestMe_MediaProgressIsNeverPaginated(t *testing.T) {
	const n = 250
	rows := make([]any, 0, n)
	for i := 0; i < n; i++ {
		rows = append(rows, map[string]any{"id": fmt.Sprintf("p%d", i)})
	}
	h := newHarness(t, "cf,jwt", nil, withUserData(&fakeUserData{progress: rows}))
	h.seedPasswordUser(t, "u1", "owner", "pw-pw-pw-pw")
	access := str(t, userObj(t, h.login(t, "owner", "pw-pw-pw-pw")), "accessToken")

	for _, path := range []string{"/api/me", "/api/me?limit=10", "/api/me?page=1&limit=10&populated=1"} {
		_, body := h.do(t, request{method: http.MethodGet, path: path,
			headers: map[string]string{"Authorization": "Bearer " + access}})
		got, _ := body["mediaProgress"].([]any)
		if len(got) != n {
			t.Fatalf("%s returned %d of %d progress rows — a truncated list DELETES the user's positions", path, len(got), n)
		}
	}
}

func TestMe_RequiresAuth(t *testing.T) {
	h := newHarness(t, "cf,jwt", nil)
	w, _ := h.do(t, request{method: http.MethodGet, path: "/api/me"})
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("got %d want 401", w.Code)
	}
}

// TestMe_TypeIsUserNotRoot: §1.7.4's cheapest win — user.type "user" makes Absorb
// hide the entire admin UI, which we do not implement.
func TestMe_TypeIsUserNotRoot(t *testing.T) {
	h := newHarness(t, "cf,jwt", nil)
	h.seedPasswordUser(t, "u1", "owner", "pw-pw-pw-pw")
	access := str(t, userObj(t, h.login(t, "owner", "pw-pw-pw-pw")), "accessToken")
	_, body := h.do(t, request{method: http.MethodGet, path: "/api/me",
		headers: map[string]string{"Authorization": "Bearer " + access}})
	if body["type"] != "user" {
		t.Fatalf(`user.type should be "user" so the admin UI stays hidden, got %#v`, body["type"])
	}
}

// ── /api/me/sessions ────────────────────────────────────────────────────────

func TestSessions_ConformsToFixture(t *testing.T) {
	h := newHarness(t, "cf,jwt", nil)
	h.seedPasswordUser(t, "u1", "owner", "pw-pw-pw-pw")

	// Reproduce the oracle's captured state: three sessions, the two newest carrying a
	// deviceInfo object and the oldest none (a plain curl login).
	loginWithDevice := func(device map[string]any) map[string]any {
		t.Helper()
		payload := map[string]any{"username": "owner", "password": "pw-pw-pw-pw"}
		if device != nil {
			payload["deviceInfo"] = device
		}
		w, body := h.do(t, request{method: http.MethodPost, path: "/login", body: payload})
		if w.Code != http.StatusOK {
			t.Fatalf("login failed: %d %s", w.Code, w.Body.String())
		}
		return body
	}
	device := map[string]any{"deviceType": "wearable", "model": "quest", "vendor": "Facebook"}
	loginWithDevice(nil)
	loginWithDevice(device)
	access := str(t, userObj(t, loginWithDevice(device)), "accessToken")

	w, body := h.do(t, request{method: http.MethodGet, path: "/api/me/sessions",
		headers: map[string]string{"Authorization": "Bearer " + access}})
	if w.Code != http.StatusOK {
		t.Fatalf("got %d: %s", w.Code, w.Body.String())
	}
	// No allowance any more: me.go now reports itemsPerPage as a page size (default 10,
	// as the oracle answers) and paginates on ?page=/?itemsPerPage= rather than
	// returning everything under a page-size that was really an item count.
	assertConformant(t, "get_api_me_sessions.json", body)

	// total must be an INTEGER (§1.7.3 item 5: Dart throws on `42.0 as int?`).
	total, ok := body["total"].(float64)
	if !ok || total != float64(int64(total)) {
		t.Fatalf("total must be an integer, got %#v", body["total"])
	}
	if int(total) != 3 {
		t.Fatalf("expected 3 sessions, got %v", total)
	}
	sessions, _ := body["sessions"].([]any)
	current := 0
	for _, raw := range sessions {
		s := raw.(map[string]any)
		if _, ok := s["current"].(bool); !ok {
			t.Fatalf("session.current must be a real JSON boolean, got %#v", s["current"])
		}
		if s["current"] == true {
			current++
		}
		for _, key := range []string{"createdAt", "updatedAt"} {
			v, ok := s[key].(float64)
			if !ok || v != float64(int64(v)) {
				t.Fatalf("session.%s must be an integer ms epoch, got %#v", key, s[key])
			}
		}
	}
	if current != 1 {
		t.Fatalf("exactly one session should be marked current, got %d", current)
	}
}

// The oracle capture holds three sessions, so every fixture-derived assertion above fits
// on a single page and never reaches the clamps in me.go. This seeds past the default page
// size on purpose: it is the only test that can see an off-by-one in the slice bounds.
func TestSessions_PaginatesPastTheFirstPage(t *testing.T) {
	h := newHarness(t, "cf,jwt", nil)
	h.seedPasswordUser(t, "u1", "owner", "pw-pw-pw-pw")

	const seeded = 12
	var access string
	for i := 0; i < seeded; i++ {
		access = str(t, userObj(t, h.login(t, "owner", "pw-pw-pw-pw")), "accessToken")
	}

	get := func(t *testing.T, query string) map[string]any {
		t.Helper()
		w, body := h.do(t, request{method: http.MethodGet, path: "/api/me/sessions" + query,
			headers: map[string]string{"Authorization": "Bearer " + access}})
		if w.Code != http.StatusOK {
			t.Fatalf("GET %s: %d %s", query, w.Code, w.Body.String())
		}
		return body
	}
	num := func(t *testing.T, body map[string]any, key string) int {
		t.Helper()
		v, ok := body[key].(float64)
		if !ok || v != float64(int64(v)) {
			t.Fatalf("%s must be an integer, got %#v", key, body[key])
		}
		return int(v)
	}
	ids := func(t *testing.T, body map[string]any) []string {
		t.Helper()
		raw, _ := body["sessions"].([]any)
		out := make([]string, 0, len(raw))
		for _, r := range raw {
			out = append(out, str(t, r.(map[string]any), "id"))
		}
		return out
	}

	for _, tc := range []struct {
		name              string
		query             string
		wantPage, wantPer int
		wantPages, wantN  int
	}{
		{"default page size", "", 0, 10, 2, 10},
		{"second page is the remainder", "?page=1", 1, 10, 2, seeded - 10},
		{"explicit smaller page size", "?itemsPerPage=5", 0, 5, 3, 5},
		{"last page of an exact multiple", "?itemsPerPage=6&page=1", 1, 6, 2, 6},
		{"page past the end is empty, not an error", "?page=99", 99, 10, 2, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := get(t, tc.query)
			if got := num(t, body, "total"); got != seeded {
				t.Errorf("total = %d, want %d (total is the full count, not the page length)", got, seeded)
			}
			if got := num(t, body, "page"); got != tc.wantPage {
				t.Errorf("page = %d, want %d", got, tc.wantPage)
			}
			if got := num(t, body, "itemsPerPage"); got != tc.wantPer {
				t.Errorf("itemsPerPage = %d, want %d", got, tc.wantPer)
			}
			if got := num(t, body, "numPages"); got != tc.wantPages {
				t.Errorf("numPages = %d, want %d", got, tc.wantPages)
			}
			if got := len(ids(t, body)); got != tc.wantN {
				t.Errorf("returned %d sessions, want %d", got, tc.wantN)
			}
		})
	}

	// The property that catches an off-by-one in either clamp: the pages must partition the
	// set exactly — every session once, none dropped, none repeated.
	seen := map[string]int{}
	for page := 0; page < 2; page++ {
		for _, id := range ids(t, get(t, fmt.Sprintf("?page=%d", page))) {
			seen[id]++
		}
	}
	if len(seen) != seeded {
		t.Fatalf("pages 0+1 covered %d distinct sessions, want %d", len(seen), seeded)
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("session %s appeared on %d pages, want exactly 1", id, n)
		}
	}
}

func TestSessions_ScopedToCallingUser(t *testing.T) {
	h := newHarness(t, "cf,jwt", nil)
	h.seedPasswordUser(t, "u1", "owner", "pw-pw-pw-pw")
	h.seedPasswordUser(t, "u2", "other", "pw-pw-pw-pw")
	access := str(t, userObj(t, h.login(t, "owner", "pw-pw-pw-pw")), "accessToken")
	h.login(t, "other", "pw-pw-pw-pw")

	_, body := h.do(t, request{method: http.MethodGet, path: "/api/me/sessions",
		headers: map[string]string{"Authorization": "Bearer " + access}})
	if body["total"].(float64) != 1 {
		t.Fatalf("a user must only see their own sessions, got %v", body["total"])
	}
}

func TestDeleteSession_RevokesOwnSession(t *testing.T) {
	h := newHarness(t, "cf,jwt", nil)
	h.seedPasswordUser(t, "u1", "owner", "pw-pw-pw-pw")
	victim := userObj(t, h.login(t, "owner", "pw-pw-pw-pw"))
	keeper := userObj(t, h.login(t, "owner", "pw-pw-pw-pw"))
	keeperAccess := str(t, keeper, "accessToken")

	victimClaims, err := h.cfg.ParseAccessToken(str(t, victim, "accessToken"))
	if err != nil {
		t.Fatalf("ParseAccessToken: %v", err)
	}
	w, _ := h.do(t, request{method: http.MethodDelete, path: "/api/me/sessions/" + victimClaims.SessionID,
		headers: map[string]string{"Authorization": "Bearer " + keeperAccess}})
	if w.Code != http.StatusOK {
		t.Fatalf("got %d: %s", w.Code, w.Body.String())
	}
	if w2, _ := h.do(t, request{method: http.MethodPost, path: "/auth/refresh",
		headers: map[string]string{"x-refresh-token": str(t, victim, "refreshToken")}}); w2.Code != http.StatusUnauthorized {
		t.Fatalf("the deleted session must stop refreshing, got %d", w2.Code)
	}
}

// TestDeleteSession_CannotDeleteAnotherUsersSession is an authorization check, not a
// 404 for tidiness: without it any authenticated user could log out anyone.
func TestDeleteSession_CannotDeleteAnotherUsersSession(t *testing.T) {
	h := newHarness(t, "cf,jwt", nil)
	h.seedPasswordUser(t, "u1", "owner", "pw-pw-pw-pw")
	h.seedPasswordUser(t, "u2", "other", "pw-pw-pw-pw")
	target := userObj(t, h.login(t, "other", "pw-pw-pw-pw"))
	attackerAccess := str(t, userObj(t, h.login(t, "owner", "pw-pw-pw-pw")), "accessToken")

	claims, _ := h.cfg.ParseAccessToken(str(t, target, "accessToken"))
	w, _ := h.do(t, request{method: http.MethodDelete, path: "/api/me/sessions/" + claims.SessionID,
		headers: map[string]string{"Authorization": "Bearer " + attackerAccess}})
	if w.Code == http.StatusOK {
		t.Fatal("a user must not be able to delete another user's session")
	}
	if w2, _ := h.do(t, request{method: http.MethodPost, path: "/auth/refresh",
		headers: map[string]string{"x-refresh-token": str(t, target, "refreshToken")}}); w2.Code != http.StatusOK {
		t.Fatalf("the victim's session was revoked by another user, got %d", w2.Code)
	}
}

func TestDeleteSession_UnknownIdIs404(t *testing.T) {
	h := newHarness(t, "cf,jwt", nil)
	h.seedPasswordUser(t, "u1", "owner", "pw-pw-pw-pw")
	access := str(t, userObj(t, h.login(t, "owner", "pw-pw-pw-pw")), "accessToken")
	w, _ := h.do(t, request{method: http.MethodDelete, path: "/api/me/sessions/ghost",
		headers: map[string]string{"Authorization": "Bearer " + access}})
	if w.Code != http.StatusNotFound {
		t.Fatalf("got %d want 404", w.Code)
	}
}

// ── construction guards ─────────────────────────────────────────────────────

func TestNew_RejectsMissingDependencies(t *testing.T) {
	cfg, _ := absauth.Load(absauth.Settings{Enabled: true, JWTSecret: testSecret})
	if _, err := abshandler.New(abshandler.Options{Store: newFakeStore()}); err == nil {
		t.Fatal("a nil config must be rejected")
	}
	if _, err := abshandler.New(abshandler.Options{Config: cfg}); err == nil {
		t.Fatal("a nil store must be rejected")
	}
	disabled, _ := absauth.Load(absauth.Settings{Enabled: false})
	if _, err := abshandler.New(abshandler.Options{Config: disabled, Store: newFakeStore()}); err == nil {
		t.Fatal("a disabled config must be rejected — nothing should be registered")
	}
	// A nil resolver would leave /login working while every authenticated route 401'd.
	if _, err := abshandler.New(abshandler.Options{Config: cfg, Store: newFakeStore()}); err == nil {
		t.Fatal("a nil identity resolver must be rejected")
	}
}
