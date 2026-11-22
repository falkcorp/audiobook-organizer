// file: internal/books/identity_authoptions_test.go
// version: 0.1.0
// guid: 8e3b6d12-4f95-4a70-b1c8-6d2a9f07e534
// last-edited: 2026-11-22

package books

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/backup"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/mediacore/app"
	"github.com/falkcorp/mediacore/auth/password"
	"github.com/falkcorp/mediacore/storage"
)

// F4-08 B10v: the four values books hands core that make the identity boot admit
// what HEAD admitted. Each is pinned against HEAD's own behaviour (the citations
// are on identityAuthOptions and archivePrefix), and the three that decide who
// gets in or where a request lands are proved through a real composed boot over a
// store HEAD wrote: M3 imports HEAD's rows, nothing is stubbed. Every name is
// Batman's, every address on the documentation range.

// TestIdentityAuthOptions_AreHEADsAndOnlyUnderTheFlip pins the values by their
// literals, so a change fails here and not on an owner's key.
func TestIdentityAuthOptions_AreHEADsAndOnlyUnderTheFlip(t *testing.T) {
	got := identityAuthOptions()
	require.Equal(t, []string{"abk_"}, got.ImportedKeyPrefixes, "HEAD's API key prefix (store.GenerateAPIKeyToken)")
	require.Equal(t, "/api/v1/auth/oauth", got.OIDCCallbackBase, "HEAD's OAuth callback base (wire_auth_routes.go)")
	require.True(t, got.DefaultDisabledAllowRemote, "HEAD only warns for auth off on a remote-reachable listener")
	require.Equal(t, ".bootstrap-token", got.BootstrapTokenFile)

	t.Run("on", func(t *testing.T) {
		t.Cleanup(SetIdentityFlipForTest(true))
		_, o, _ := composedOptions(t, false, "", nil, nil)
		require.Equal(t, got, o.Auth)
		require.Equal(t, "audiobooks_pebble_", o.ArchivePrefix)
	})
	t.Run("off", func(t *testing.T) {
		t.Cleanup(SetIdentityFlipForTest(false))
		_, o, _ := composedOptions(t, false, "", nil, nil)
		require.Empty(t, o.Auth.ImportedKeyPrefixes)
		require.Empty(t, o.Auth.OIDCCallbackBase)
		require.False(t, o.Auth.DefaultDisabledAllowRemote)
	})
}

// TestArchivePrefix_IsWhatHEADsWriterNames: HEAD's backup writer names an archive
// audiobooks_<type>_<YYYYMMDD_HHMMSS><ext>, and core's catalogue lists and prunes
// only files that carry Options.ArchivePrefix and whose remainder is a timestamp
// and an archive extension. The archive here is written by HEAD's real writer, so
// the constant is tied to what is on disk in an install's backup directory and
// not to a second copy of the format. Serve and the offline commands name the
// same value. (Core's catalogue is not mounted at boot yet, F8-04, so "listed
// and restorable" is core/backup's own test over this prefix; the boot half is
// the journal's name, TestIdentityBoot_ArchivePrefixNamesTheJournal.)
func TestArchivePrefix_IsWhatHEADsWriterNames(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "books.pebble")
	require.NoError(t, os.MkdirAll(db, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(db, "CURRENT"), []byte("MANIFEST-000001\n"), 0o600))
	info, err := backup.CreateBackup(db, "pebble", backup.BackupConfig{BackupDir: filepath.Join(dir, "backups"), MaxBackups: 5, CompressionLevel: 1})
	require.NoError(t, err)

	rest, ok := strings.CutPrefix(info.Filename, archivePrefix)
	require.Truef(t, ok, "HEAD's writer named %q, which does not start with %q: core would not recognise it", info.Filename, archivePrefix)
	// core/backup's archiveExt, which is unexported.
	require.Regexp(t, regexp.MustCompile(`^\d{8}_\d{6}(\.tar|\.tar\.gz|\.tar\.zst)$`), rest)

	opts, err := OfflineOptions(BootInputs{})
	require.NoError(t, err)
	require.Equal(t, archivePrefix, opts.ArchivePrefix, "an offline command would name a different journal than serve")
	t.Cleanup(SetIdentityFlipForTest(true))
	_, o, _ := composedOptions(t, false, "", nil, nil)
	require.Equal(t, opts.ArchivePrefix, o.ArchivePrefix)
}

// ---------------------------------------------------------------- the HEAD store

// headKeys is what the HEAD store holds for the imported-key cases: the raw key
// of each (HEAD showed it once and kept only its hash), and the IDs core must
// hold after M3.
type headKeys struct {
	bruceUser string
	password  string

	full, limited, expired, revoked, disabledOwner headKey
	// never is a well-formed key HEAD never minted.
	never string
}

type headKey struct {
	raw, id string
}

const wayne = "bruce.wayne"

// seedHEAD writes HEAD's identity rows through HEAD's own store calls: an admin
// who can sign in, an editor owning four keys (no scope list, so the owner's whole
// set; one atom; already expired; revoked) and a viewer HEAD has since
// deactivated who still owns a live key.
func seedHEAD(t *testing.T, out *headKeys) func(*database.PebbleStore) {
	t.Helper()
	return func(s *database.PebbleStore) {
		t.Helper()
		// The ABS surface is on in HEAD's stored settings as well as in the
		// environment, which is authoritative for it (errABSFlagDrift).
		require.NoError(t, s.SetSetting("config_blob", `{"abs_api_enabled":true,"embedding_enabled":true,"embedding_model":"batcomputer"}`, "json", false))
		out.password = "correct-horse-battery-staple-gotham"
		algo, hash, err := password.New(password.Options{}).Hash(out.password)
		require.NoError(t, err)
		bruce, err := s.CreateUser(wayne, "bruce.wayne@wayne-enterprises.example.test", algo, hash, []string{"admin"}, "active")
		require.NoError(t, err)
		out.bruceUser = bruce.ID
		lucius, err := s.CreateUser("lucius.fox", "lucius.fox@wayne-enterprises.example.test", algo, hash, []string{"editor"}, "active")
		require.NoError(t, err)
		harvey, err := s.CreateUser("harvey.dent", "harvey.dent@gcpd.example.test", algo, hash, []string{"viewer"}, "inactive")
		require.NoError(t, err)

		now := time.Now()
		future, past := now.Add(30*24*time.Hour), now.Add(-time.Hour)
		mint := func(owner, name string, scopes []string, status string, expires *time.Time, revoked *time.Time) headKey {
			raw, hash, err := database.GenerateAPIKeyToken()
			require.NoError(t, err)
			k, err := s.CreateAPIKey(&database.APIKey{UserID: owner, Name: name, TokenHash: hash, Scopes: scopes, Status: status, ExpiresAt: expires, RevokedAt: revoked})
			require.NoError(t, err)
			return headKey{raw: raw, id: k.ID}
		}
		out.full = mint(lucius.ID, "batcomputer", nil, "active", &future, nil)
		out.limited = mint(bruce.ID, "batsignal", []string{"library.view"}, "active", &future, nil)
		out.expired = mint(lucius.ID, "old-batmobile", nil, "active", &past, nil)
		out.revoked = mint(lucius.ID, "stolen-utility-belt", nil, "revoked", &future, &past)
		out.disabledOwner = mint(harvey.ID, "two-face-coin", nil, "active", &future, nil)
		raw, _, err := database.GenerateAPIKeyToken()
		require.NoError(t, err)
		out.never = raw
	}
}

// bootOverHEADStore runs the identity flip over fx's HEAD store with sign-in
// required and the ABS surface on in its token mode (the one an abk_ key rides),
// and waits for ready. M3 imports the rows during the boot.
func bootOverHEADStore(t *testing.T, fx *flipFixture) *runningApp {
	t.Helper()
	t.Cleanup(SetIdentityFlipForTest(true))
	// o.Env below is the whole environment core's config reads, so the ABS
	// settings (environment-authoritative, errABSFlagDrift) ride on the fixture.
	fx.env = append(fx.env,
		"ABO_CORE_HTTP_LISTEN=127.0.0.1:0",
		"ABO_CORE_HTTP_BASIC_GATE_ENABLED=false",
		"ABO_BOOKS_ABS_API_ENABLED=true",
		"ABO_BOOKS_ABS_AUTH_MODES=jwt",
		"ABO_BOOKS_ABS_JWT_SECRET=synthetic-composed-app-secret-0123456789abcdef",
	)
	r := runComposedIn(t, fx.stateDir, func(c *config.Config) {
		c.DatabasePath = fx.dbPath
		c.RootDir = fx.dbDir
		c.EnableAuth = true
		c.ABSAPIEnabled = true
		c.ABSJWTSecret = "synthetic-composed-app-secret-0123456789abcdef"
		c.ABSAuthModes = "jwt"
	}, nil, func(o *app.Options) {
		o.StorageFS = storage.FS{}
		o.StateDir = fx.stateDir
		o.Env = append([]string{storagePathEnv + "=" + fx.dbPath}, fx.env...)
		// sandboxStateDirAt wrote a fixed key; the store's rows are sealed under this one.
		require.NoError(t, os.WriteFile(database.EncryptionKeyPath(fx.stateDir), fx.key, 0o600))
	}, nil)
	deadline := time.Now().Add(2 * time.Minute)
	for {
		ready, err := shapedReady(r.composed)
		require.NoError(t, err)
		if ready {
			break
		}
		require.True(t, time.Now().Before(deadline), "/health never reported ready")
		time.Sleep(50 * time.Millisecond)
	}
	return r
}

// withKey is one request with raw as the bearer credential.
func withKey(r *runningApp, method, path, raw string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	req.RemoteAddr = "192.0.2.10:41000"
	req.Header.Set("Authorization", "Bearer "+raw)
	rec := httptest.NewRecorder()
	r.app.Handler().ServeHTTP(rec, req)
	return rec
}

// bodyOf is a response's body, for an assertion message.
func bodyOf(rec *httptest.ResponseRecorder) string { return rec.Body.String() }

// TestIdentityBoot_ImportedAPIKeysSurvive is D12 (b) through a real boot: every
// "abk_" key HEAD minted keeps working after the flip, by its hash, under core's
// rules, and nothing that was not a live HEAD key does. One boot holds every case:
// each refusal is judged beside the imported row it is about (the key list shows
// it imported and in the state that refuses it), so a refusal that came from the
// key never having been imported would not pass.
func TestIdentityBoot_ImportedAPIKeysSurvive(t *testing.T) {
	var h headKeys
	fx := newFlipFixture(t, seedHEAD(t, &h), "")
	r := bootOverHEADStore(t, fx)
	admin := loginAs(t, r, wayne, h.password)

	// Core's list of every key: all five are imported, in the state HEAD held.
	listed := map[string]coreKey{}
	for _, k := range admin.keys() {
		listed[k.ID] = k
	}
	for name, k := range map[string]headKey{"full": h.full, "limited": h.limited, "expired": h.expired, "revoked": h.revoked, "disabled owner": h.disabledOwner} {
		got, ok := listed[k.id]
		require.Truef(t, ok, "%s: M3 did not import the key", name)
		require.Truef(t, got.Imported, "%s: core does not hold it as imported", name)
	}
	require.Equal(t, "revoked", listed[h.revoked.id].Status)

	t.Run("an imported key authenticates with its M3-mapped scopes", func(t *testing.T) {
		rec := withKey(r, http.MethodGet, "/api/core/v1/me", h.full.raw)
		require.Equal(t, http.StatusOK, rec.Code, bodyOf(rec))
		// The owner is an editor: the key holds the editor set and the ambient
		// scopes, never core:admin.
		full := listed[h.full.id]
		require.NotContains(t, full.Scopes, "core:admin")
		require.Contains(t, full.Scopes, "core:self")
		require.Contains(t, full.Scopes, "books:edit")
		require.Equal(t, http.StatusForbidden, withKey(r, http.MethodGet, "/api/core/v1/users", h.full.raw).Code,
			"an editor's key reached the user list, which HEAD's users.manage atom gated")
	})
	t.Run("a one-atom key is admitted inside its scopes and refused outside them", func(t *testing.T) {
		// HEAD's "library.view" atom on an admin's key: books:view and
		// core:tasks.view, plus the ambient list, and not core:admin although the owner
		// holds it.
		lim := listed[h.limited.id]
		require.NotContains(t, lim.Scopes, "core:admin")
		require.Contains(t, lim.Scopes, "books:view")
		require.Equal(t, http.StatusOK, withKey(r, http.MethodGet, "/api/core/v1/me", h.limited.raw).Code)
		rec := withKey(r, http.MethodGet, "/api/core/v1/users", h.limited.raw)
		require.Equal(t, http.StatusForbidden, rec.Code, "the key was admitted outside its scopes: "+bodyOf(rec))
		// The same owner's session reaches it: the refusal is the key's scope.
		require.Equal(t, http.StatusOK, admin.get("/api/core/v1/users").Code)
	})
	t.Run("a key that was never imported refuses", func(t *testing.T) {
		rec := withKey(r, http.MethodGet, "/api/core/v1/me", h.never)
		require.Equal(t, http.StatusUnauthorized, rec.Code, bodyOf(rec))
	})
	t.Run("an expired imported key refuses", func(t *testing.T) {
		rec := withKey(r, http.MethodGet, "/api/core/v1/me", h.expired.raw)
		require.Equal(t, http.StatusUnauthorized, rec.Code, bodyOf(rec))
	})
	t.Run("a revoked imported key refuses", func(t *testing.T) {
		rec := withKey(r, http.MethodGet, "/api/core/v1/me", h.revoked.raw)
		require.Equal(t, http.StatusUnauthorized, rec.Code, bodyOf(rec))
	})
	t.Run("a key whose owner is disabled refuses", func(t *testing.T) {
		rec := withKey(r, http.MethodGet, "/api/core/v1/me", h.disabledOwner.raw)
		require.Contains(t, []int{http.StatusUnauthorized, http.StatusForbidden}, rec.Code, bodyOf(rec))
	})

	t.Run("the same keys on the ABS surface's AuthenticateKey path", func(t *testing.T) {
		// The ABS surface hands a presented key to Compat.AuthenticateKey, which
		// applies the same rules.
		require.Equal(t, http.StatusOK, withKey(r, http.MethodGet, "/api/me", h.full.raw).Code)
		require.Equal(t, http.StatusOK, withKey(r, http.MethodGet, "/api/me", h.limited.raw).Code)
		for name, raw := range map[string]string{"never imported": h.never, "expired": h.expired.raw, "revoked": h.revoked.raw, "disabled owner": h.disabledOwner.raw} {
			rec := withKey(r, http.MethodGet, "/api/me", raw)
			require.Containsf(t, []int{http.StatusUnauthorized, http.StatusForbidden}, rec.Code, "%s was admitted by the ABS surface: %s", name, bodyOf(rec))
		}
	})

	t.Run("a key minted under identity is mck_", func(t *testing.T) {
		raw, id := admin.mintKey("robin-grapple", []string{"core:self"})
		require.True(t, strings.HasPrefix(raw, "mck_"), "a new key is core's, not HEAD's abk_")
		require.NotEmpty(t, id)
		require.Equal(t, http.StatusOK, withKey(r, http.MethodGet, "/api/core/v1/me", raw).Code)
	})
}

// ---------------------------------------------------------------- core's side

type coreKey struct {
	ID       string   `json:"id"`
	Status   string   `json:"status"`
	Imported bool     `json:"imported"`
	Scopes   []string `json:"scopes"`
}

// loginAs signs in through core's route (plain HTTP) and returns the session.
func loginAs(t *testing.T, r *runningApp, user, pass string) *coreAdmin {
	t.Helper()
	rec := post(t, r.app.Handler(), "/api/core/v1/auth/login", map[string]string{"username": user, "password": pass}, false)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	a := &coreAdmin{t: t, r: r}
	for _, ck := range rec.Result().Cookies() {
		if strings.HasSuffix(ck.Name, "session_id") {
			a.cookie = ck
		}
	}
	require.NotNil(t, a.cookie, "no session cookie")
	return a
}

func (a *coreAdmin) get(path string) *httptest.ResponseRecorder {
	a.t.Helper()
	return a.do(http.MethodGet, path)
}

// keys is every API key core holds, as the administrator sees them.
func (a *coreAdmin) keys() []coreKey {
	a.t.Helper()
	rec := a.get("/api/core/v1/api-keys")
	require.Equal(a.t, http.StatusOK, rec.Code, rec.Body.String())
	var env struct {
		Items []coreKey `json:"items"`
	}
	require.NoError(a.t, json.Unmarshal(rec.Body.Bytes(), &env), rec.Body.String())
	require.NotEmpty(a.t, env.Items, "core holds no key: %s", rec.Body.String())
	return env.Items
}

// mintKey creates a key for the signed-in user and returns the raw key and its ID.
func (a *coreAdmin) mintKey(name string, scopes []string) (raw, id string) {
	a.t.Helper()
	body, err := json.Marshal(map[string]any{"name": name, "scopes": scopes})
	require.NoError(a.t, err)
	req := httptest.NewRequest(http.MethodPost, "/api/core/v1/me/api-keys", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "192.0.2.10:41000"
	req.AddCookie(a.cookie)
	rec := httptest.NewRecorder()
	a.r.app.Handler().ServeHTTP(rec, req)
	require.Equal(a.t, http.StatusCreated, rec.Code, rec.Body.String())
	var res struct {
		Key    string  `json:"key"`
		APIKey coreKey `json:"api_key"`
	}
	require.NoError(a.t, json.Unmarshal(rec.Body.Bytes(), &res), rec.Body.String())
	return res.Key, res.APIKey.ID
}

// ---------------------------------------------------------------- the OAuth paths

// TestIdentityBoot_OAuthStartAndCallbackAreCoresAtHEADsPaths: HEAD's two OAuth
// paths are core's routes under the flip, so a redirect URI an identity provider
// holds still lands, and the books mount no longer owns either. The route table
// names the owner; a request with no provider configured is answered by core's
// flow (its "no such provider" body), which is not what a path nobody serves
// answers.
func TestIdentityBoot_OAuthStartAndCallbackAreCoresAtHEADsPaths(t *testing.T) {
	c := bootIdentity(t, nil)
	owners := map[string][]string{}
	for ri := range c.app.Routes() {
		owners[ri.Method+" "+ri.Pattern] = append(owners[ri.Method+" "+ri.Pattern], ri.Owner)
	}
	for _, p := range []string{"GET /api/v1/auth/oauth/{provider}/callback", "GET /api/v1/auth/oauth/{provider}/start"} {
		require.Equalf(t, []string{"core"}, owners[p], "%s is not served by core alone", p)
	}
	// HEAD's provider list stays books': core serves only the two exact paths.
	require.NotContains(t, owners, "GET /api/v1/auth/oauth/{provider}/other")

	for _, path := range []string{"/api/v1/auth/oauth/google/callback?code=x&state=y", "/api/v1/auth/oauth/google/start"} {
		rec := c.serve(http.MethodGet, path, nil)
		require.Equalf(t, http.StatusNotFound, rec.Code, "%s: %s", path, rec.Body.String())
		require.Containsf(t, rec.Body.String(), "no such provider", "%s was not answered by core's OIDC flow: %s", path, rec.Body.String())
	}
}

// ---------------------------------------------------------------- auth off

// TestIdentityBoot_AuthOffRemoteAdmitsAsHEADDid: with auth off on a listener a
// remote caller can reach, HEAD warned and served; core's own default would refuse
// to boot. Under the flip books' default admits, health says so (degraded with
// auth_disabled_remote), and an operator who sets
// core.auth.disabled_allow_remote=false still gets core's refusal. Nothing here
// sets the key in the admitting case, so the assertion is on the default.
func TestIdentityBoot_AuthOffRemoteAdmitsAsHEADDid(t *testing.T) {
	remote := func(o *app.Options) {
		o.ConfigFlags["core.auth.mode"] = "disabled"
		o.ConfigFlags["core.http.listen"] = "0.0.0.0:0"
	}
	t.Run("the default admits and health reports it", func(t *testing.T) {
		t.Cleanup(SetIdentityFlipForTest(true))
		r := runComposedWith(t, nil, nil, remote, nil)
		require.Eventually(t, func() bool {
			return slices.Contains(statusDegradedOrNil(t, r.composed), "auth_disabled_remote")
		}, 30*time.Second, 50*time.Millisecond, "an auth-off install a remote caller reaches did not report auth_disabled_remote")
	})
	t.Run("an operator's explicit false still refuses the boot", func(t *testing.T) {
		t.Cleanup(SetIdentityFlipForTest(true))
		mod, opts, _ := composedOptions(t, false, "", nil, nil)
		remote(&opts)
		opts.ConfigFlags["core.auth.disabled_allow_remote"] = "false"
		err := bootFails(t, mod, opts)
		require.ErrorContains(t, err, "disabled_allow_remote")
	})
}

// statusDegradedOrNil is statusDegraded that answers nil while the status route
// is not yet 200, for a poll.
func statusDegradedOrNil(t *testing.T, c *composed) []string {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/system/status", nil)
	req.RemoteAddr = "127.0.0.1:41000"
	c.app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		return nil
	}
	var body struct {
		Data struct {
			Degraded []string `json:"degraded"`
		} `json:"data"`
	}
	if json.Unmarshal(rec.Body.Bytes(), &body) != nil {
		return nil
	}
	return body.Data.Degraded
}

// ---------------------------------------------------------------- the archive prefix

// TestIdentityBoot_ArchivePrefixNamesTheJournal: what a full boot reads of
// Options.ArchivePrefix today is the revocation journal's file, which sits in the
// backup directory under the prefix, so the journal and the archives an install
// already holds share one name space and a restore finds both.
func TestIdentityBoot_ArchivePrefixNamesTheJournal(t *testing.T) {
	t.Cleanup(SetIdentityFlipForTest(true))
	r := runComposedWith(t, func(c *config.Config) { c.EnableAuth = true }, nil, nil, nil)
	dir := filepath.Join(filepath.Dir(config.Snapshot().DatabasePath), "backups")
	require.FileExists(t, filepath.Join(dir, "audiobooks_pebble_revocations.journal"), "the journal does not carry HEAD's archive prefix")
	require.NoFileExists(t, filepath.Join(dir, AppID+"_revocations.journal"), "core's default prefix named a journal beside HEAD's archives")
	_ = r
}
