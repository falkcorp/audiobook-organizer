// file: internal/books/identity_wire_test.go
// version: 0.1.0
// guid: 7b3e9d15-4a60-4c28-9f71-d5e0a26c8b43
// last-edited: 2026-11-21

package books

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	coreconfig "github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/module"
)

// The fixtures are Batman's: Wayne's e-mail is the owner, Gordon is the other
// address, on the documentation range.
const (
	waynE  = "bruce.wayne@wayne-enterprises.example"
	gordon = "james.gordon@gcpd.example"
)

// TestIdentityOptions_ShapeUnderTheFlip: with the predicate on, Options hands core
// the identity boot, the atom table, the account resolver, the Access extension
// and the bootstrap token's name, and pins the session cookie; with it off, none
// of them (HEAD's Options).
func TestIdentityOptions_ShapeUnderTheFlip(t *testing.T) {
	t.Run("on", func(t *testing.T) {
		t.Cleanup(SetIdentityFlipForTest(true))
		mod, o, _ := composedOptions(t, false, "", nil, nil)
		require.True(t, o.IdentityBoot, "the identity boot is not asked for")
		require.NotNil(t, o.ResolveAtom, "stage 13 has no atom table")
		require.NotEmpty(t, o.DisplayName)
		require.NotNil(t, mod.idw)
		require.Same(t, mod.idw.resolver, o.AccountResolver, "core binds a resolver books does not hold")
		require.Len(t, o.Authenticators, 1)
		require.Equal(t, "ext:cfaccess", string(o.Authenticators[0].Spec.Method))
		require.Equal(t, ".bootstrap-token", o.Auth.BootstrapTokenFile)
		require.Equal(t, "session_id", o.ConfigFlags["core.auth.session.cookie_name"])

		// Core derives core.auth.owner.required from this extension's OwnerSet,
		// so the extension Options hands core must read the configured owner,
		// the same value the bridge reads (a fake wired in only here would leave
		// the real wiring unpinned).
		ext := o.Authenticators[0]
		prev := config.Snapshot().OwnerEmail
		t.Cleanup(func() { config.Mutate(func(c *config.Config) { c.OwnerEmail = prev }) })
		config.Mutate(func(c *config.Config) { c.OwnerEmail = waynE })
		require.True(t, ext.OwnerSet(), "an owner is configured but core's owner gate would be off")
		config.Mutate(func(c *config.Config) { c.OwnerEmail = "" })
		require.False(t, ext.OwnerSet())
	})
	t.Run("off", func(t *testing.T) {
		t.Cleanup(SetIdentityFlipForTest(false))
		mod, o, _ := composedOptions(t, false, "", nil, nil)
		require.False(t, o.IdentityBoot)
		require.Nil(t, o.ResolveAtom)
		require.Nil(t, o.AccountResolver)
		require.Empty(t, o.Authenticators)
		require.Empty(t, o.Auth.BootstrapTokenFile)
		require.NotContains(t, o.ConfigFlags, coreconfig.Path("core.auth.session.cookie_name"))
		require.Nil(t, mod.idw)
	})
}

// TestIdentityWire_CompatMethodNamesAreExact: the audit and the session list
// carry the method names; books.abs is HEAD's ABS hand-off and ext:cfaccess is
// core's extension prefix with cfaccess's name.
func TestIdentityWire_CompatMethodNamesAreExact(t *testing.T) {
	require.Equal(t, "books.abs", string(absAuthMethod))
	w, err := newIdentityWiring(func() string { return waynE })
	require.NoError(t, err)
	require.Equal(t, "ext:cfaccess", string(w.ext.Spec.Method))
}

// TestIdentityWire_OwnerRequiredFollowsTheOwnerEmail: the extension's OwnerSet and
// the bridge's reader are one rule, "an owner e-mail is configured". With one set,
// core.auth.owner.required is true (core derives it from an OwnerVerifier
// extension whose OwnerSet is true); with none it is not.
func TestIdentityWire_OwnerRequiredFollowsTheOwnerEmail(t *testing.T) {
	for _, tc := range []struct {
		name, email string
		want        bool
	}{
		{"set", waynE, true},
		{"set with spaces and case", "  " + strings.ToUpper(waynE) + " ", true},
		{"empty", "", false},
		{"blank", "   ", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, err := newIdentityWiring(func() string { return tc.email })
			require.NoError(t, err)
			require.True(t, w.ext.Spec.OwnerVerifier, "the Access extension must be the owner verifier")
			require.Equal(t, tc.want, w.ext.OwnerSet())
		})
	}
	t.Run("the bridge reads the configured owner per call", func(t *testing.T) {
		prev := config.Snapshot().OwnerEmail
		t.Cleanup(func() { config.Mutate(func(c *config.Config) { c.OwnerEmail = prev }) })
		config.Mutate(func(c *config.Config) { c.OwnerEmail = "" })
		require.False(t, ownerRequiredNow())
		config.Mutate(func(c *config.Config) { c.OwnerEmail = waynE })
		require.True(t, ownerRequiredNow())
	})
}

type fakeConfigReads struct {
	module.ConfigReads
	ks coreconfig.KeyState
}

func (f fakeConfigReads) Lookup(coreconfig.Path) (coreconfig.KeyState, bool) { return f.ks, true }

// TestIdentityWire_RefusesAnExplicitOwnerRequiredFalse: an operator's
// core.auth.owner.required=false switches the owner gate off on an install whose
// owner is set, which HEAD never allowed; the boot refuses it. A default or an
// explicit true is accepted.
func TestIdentityWire_RefusesAnExplicitOwnerRequiredFalse(t *testing.T) {
	ks := func(src coreconfig.Source, v string) fakeConfigReads {
		return fakeConfigReads{ks: coreconfig.KeyState{Source: src, Effective: json.RawMessage(v)}}
	}
	require.NoError(t, refuseOwnerRequiredOff(nil))
	require.NoError(t, refuseOwnerRequiredOff(ks(coreconfig.SourceDefault, "false")), "a default is not an operator's choice")
	require.NoError(t, refuseOwnerRequiredOff(ks(coreconfig.SourceEnv, "true")))
	require.ErrorContains(t, refuseOwnerRequiredOff(ks(coreconfig.SourceEnv, "false")), "core.auth.owner.required")
	require.ErrorContains(t, refuseOwnerRequiredOff(ks(coreconfig.SourceFile, " false ")), "Remove the key")
}

// TestIdentityBoot_RefusesOwnerRequiredFalse is the same refusal through a real
// boot: Register returns it, the server is never built.
func TestIdentityBoot_RefusesOwnerRequiredFalse(t *testing.T) {
	t.Cleanup(SetIdentityFlipForTest(true))
	t.Setenv("ABO_CORE_AUTH_OWNER_REQUIRED", "false")
	mod, opts, _ := composedOptions(t, false, "", func(c *config.Config) { c.EnableAuth = true; c.OwnerEmail = waynE }, nil)
	err := bootFails(t, mod, opts)
	require.ErrorContains(t, err, "core.auth.owner.required")
}

// stateDirOf is the sandboxed secure state directory of this test's boot.
func stateDirOf(t *testing.T) string {
	t.Helper()
	d := os.Getenv(config.SecureStateDirEnv)
	require.NotEmpty(t, d)
	return d
}

// post is a JSON POST through the root handler, plain HTTP unless secure.
func post(t *testing.T, h http.Handler, path string, body any, secure bool) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "192.0.2.10:41000"
	if secure {
		req.TLS = &tls.ConnectionState{HandshakeComplete: true}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// redeemBootstrap redeems the token file's content through core's route and
// returns the generated account.
func redeemBootstrap(t *testing.T, c *composed) (username, password string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(stateDirOf(t), ".bootstrap-token"))
	require.NoError(t, err, "core wrote no bootstrap token where the runbook reads it")
	// 425 until the app reports ready (Run opens the listener first).
	var rec *httptest.ResponseRecorder
	require.Eventually(t, func() bool {
		rec = post(t, c.app.Handler(), "/api/core/v1/auth/bootstrap", map[string]string{"token": strings.TrimSpace(string(raw))}, false)
		return rec.Code != http.StatusTooEarly
	}, 30*time.Second, 50*time.Millisecond, "the app never became ready")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var res struct {
		GeneratedPassword string `json:"generated_password"`
		User              struct {
			Username string `json:"username"`
		} `json:"user"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &res), rec.Body.String())
	require.NotEmpty(t, res.GeneratedPassword, "the token file's token was not core's: %s", rec.Body.String())
	return res.User.Username, res.GeneratedPassword
}

// TestIdentityBoot_BootstrapTokenIsCoresAtTheRunbooksPath: under the flip the
// emergency token is at <state dir>/.bootstrap-token (the name and directory the
// server-bootstrap tooling and its sudoers rule read), written by core, and HEAD's
// writer does not replace it: the file's token redeems at core's route. Run, so
// the server's start hook (which holds HEAD's writer) has run.
func TestIdentityBoot_BootstrapTokenIsCoresAtTheRunbooksPath(t *testing.T) {
	t.Cleanup(SetIdentityFlipForTest(true))
	r := runComposedWith(t, func(c *config.Config) { c.EnableAuth = true }, nil, nil, nil)
	user, pass := redeemBootstrap(t, r.composed)
	require.NotEmpty(t, user)
	require.NotEmpty(t, pass)
	// HEAD's writer is off: the data directory keeps no second token file.
	_, err := os.Stat(filepath.Join(filepath.Dir(config.Snapshot().DatabasePath), ".bootstrap-token"))
	if filepath.Dir(config.Snapshot().DatabasePath) != stateDirOf(t) {
		require.True(t, os.IsNotExist(err), "a second bootstrap token file beside the database")
	}
}

// TestIdentityBoot_BootstrapTokenNameAndDirectoryArePinned: the two halves of the
// path, each by its literal, so a change to either fails here and not on the
// recovery of a lost admin. The name is Options.Auth.BootstrapTokenFile; the
// directory is bootOptions' StateDir, the constant secure state directory.
func TestIdentityBoot_BootstrapTokenNameAndDirectoryArePinned(t *testing.T) {
	require.Equal(t, ".bootstrap-token", bootstrapTokenName, "the file name the server-bootstrap tooling reads")
	require.Equal(t, ".bootstrap-token", identityAuthOptions().BootstrapTokenFile)
	require.Equal(t, "/var/lib/audiobook-organizer", bootOptions(BootInputs{}).StateDir, "the directory the sudoers rule names")
	require.Equal(t, "/var/lib/audiobook-organizer", config.DefaultSecureStateDir)
}

// TestIdentityBoot_PlainHTTPLANGetsAWorkingSession: the cookie is session_id with
// core's __Host- prefix kept. Over plain HTTP (a LAN address) core issues the
// unprefixed name, which the browser accepts and the session works; over HTTPS the
// prefixed one. The pin on the name is the one HEAD's browsers hold.
func TestIdentityBoot_PlainHTTPLANGetsAWorkingSession(t *testing.T) {
	t.Cleanup(SetIdentityFlipForTest(true))
	c := runComposedWith(t, func(c *config.Config) { c.EnableAuth = true }, nil, nil, nil).composed
	user, pass := redeemBootstrap(t, c)

	login := func(secure bool) *http.Cookie {
		t.Helper()
		rec := post(t, c.app.Handler(), "/api/core/v1/auth/login", map[string]string{"username": user, "password": pass}, secure)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		for _, ck := range rec.Result().Cookies() {
			if strings.HasSuffix(ck.Name, "session_id") {
				return ck
			}
		}
		t.Fatalf("no session cookie: %v", rec.Result().Header["Set-Cookie"])
		return nil
	}
	plain := login(false)
	require.Equal(t, "session_id", plain.Name, "plain HTTP must get the unprefixed name: a __Host- cookie is dropped by the browser without Secure")
	require.False(t, plain.Secure)

	secure := login(true)
	require.Equal(t, "__Host-session_id", secure.Name, "the __Host- prefix stays on an HTTPS session")
	require.True(t, secure.Secure)

	// The plain-HTTP session works: it authenticates a route.
	req := httptest.NewRequest(http.MethodGet, "/api/core/v1/me", nil)
	req.RemoteAddr = "192.0.2.10:41000"
	req.AddCookie(plain)
	rec := httptest.NewRecorder()
	c.app.Handler().ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}
