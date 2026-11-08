// file: internal/server/basic_gate_health_token_test.go
// version: 1.1.2
// guid: e4e0550a-a189-4939-ac92-8e07e8a78ac9
// last-edited: 2026-11-08

package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
	"github.com/falkcorp/audiobook-organizer/internal/startup"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const basicGateEnvName = "BASIC_AUTH_PASSWORD"

// setBasicGate sets the gate's config fields and BASIC_AUTH_PASSWORD (nil
// unsets it) and restores both when the test ends. t.Setenv registers the
// restore of the caller's value; os.Unsetenv then expresses "unset", which
// t.Setenv cannot.
func setBasicGate(t *testing.T, enabled bool, user, stored string, env *string) {
	t.Helper()
	prevEnabled := config.AppConfig.BasicAuthEnabled
	prevUser := config.AppConfig.BasicAuthUsername
	prevPass := config.AppConfig.BasicAuthPassword
	t.Cleanup(func() {
		config.AppConfig.BasicAuthEnabled = prevEnabled
		config.AppConfig.BasicAuthUsername = prevUser
		config.AppConfig.BasicAuthPassword = prevPass
	})
	t.Setenv(basicGateEnvName, "")
	if env == nil {
		require.NoError(t, os.Unsetenv(basicGateEnvName))
	} else {
		require.NoError(t, os.Setenv(basicGateEnvName, *env))
	}
	config.AppConfig.BasicAuthEnabled = enabled
	config.AppConfig.BasicAuthUsername = user
	config.AppConfig.BasicAuthPassword = stored
}

func sourceTokens(src interface{ Tokens() []string }) []string {
	if src == nil {
		return nil
	}
	return src.Tokens()
}

// TestBasicGateNoPassword_TokenOnHealthSource: with the gate on, the stored
// password empty and no BASIC_AUTH_PASSWORD, /health's fixed-token source
// reports basic_gate_no_password; once a password is saved it does not, with
// no restart (05-health §5H.3.1; 05-authn §11.1 item 5).
func TestBasicGateNoPassword_TokenOnHealthSource(t *testing.T) {
	t.Run("source before Start", func(t *testing.T) {
		s := &Server{}

		setBasicGate(t, true, "admin", "", nil)
		src := s.startupDegradedSource()
		require.NotNil(t, src, "no runner yet, but the gate has no password: the source must exist")
		assert.Equal(t, []string{servermiddleware.BasicGateNoPasswordToken}, src.Tokens())

		// Saving a password clears it on the next call, no restart.
		config.AppConfig.BasicAuthPassword = "secret"
		assert.Nil(t, s.startupDegradedSource(), "no runner and no token: nil, as before the hotfix")

		// The env recovery supplies a password, so no token.
		config.AppConfig.BasicAuthPassword = ""
		require.NoError(t, os.Setenv(basicGateEnvName, "recover"))
		assert.Nil(t, s.startupDegradedSource())

		// A gate that is off raises nothing.
		require.NoError(t, os.Unsetenv(basicGateEnvName))
		config.AppConfig.BasicAuthEnabled = false
		assert.Nil(t, s.startupDegradedSource())
	})

	t.Run("source merges the startup runner", func(t *testing.T) {
		s := &Server{}
		steps := startup.New()
		require.NoError(t, steps.Run("searchindex", func() error { return errors.New("index missing") }))
		s.startupStepsFor(ServerConfig{StartupSteps: steps})

		setBasicGate(t, true, "admin", "", nil)
		assert.ElementsMatch(t,
			[]string{startup.TokenSearchIndex, servermiddleware.BasicGateNoPasswordToken},
			sourceTokens(s.startupDegradedSource()))

		config.AppConfig.BasicAuthPassword = "secret"
		assert.Equal(t, []string{startup.TokenSearchIndex}, sourceTokens(s.startupDegradedSource()))
	})

	t.Run("through the real /health wiring", func(t *testing.T) {
		srv, cleanup := setupTestServer(t)
		defer cleanup()
		// After setupTestServer, which replaces config.AppConfig wholesale.
		setBasicGate(t, true, "admin", "", nil)

		health := func() []string {
			t.Helper()
			w := httptest.NewRecorder()
			srv.router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/health", nil))
			require.Equal(t, http.StatusOK, w.Code, "/api/v1/health is exempt from the gate: %s", w.Body.String())
			var resp struct {
				Data struct {
					Degraded []string `json:"degraded"`
				} `json:"data"`
			}
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp), w.Body.String())
			return resp.Data.Degraded
		}
		// basicRefused reports whether the Basic gate itself refused the
		// request: a 401 carrying its challenge. Whatever the route does after
		// the gate (its own auth, its own status) is not this test's concern.
		basicRefused := func(user, pass string) bool {
			t.Helper()
			req := httptest.NewRequest(http.MethodGet, "/api/v1/audiobooks", nil)
			req.SetBasicAuth(user, pass)
			w := httptest.NewRecorder()
			srv.router.ServeHTTP(w, req)
			return w.Code == http.StatusUnauthorized &&
				strings.HasPrefix(w.Header().Get("WWW-Authenticate"), "Basic ")
		}

		assert.Contains(t, health(), servermiddleware.BasicGateNoPasswordToken)
		assert.True(t, basicRefused("admin", ""), "the gate must refuse admin: with no password")

		// Saving a password clears the token without a restart.
		config.AppConfig.BasicAuthPassword = "secret"
		assert.NotContains(t, health(), servermiddleware.BasicGateNoPasswordToken)
		assert.False(t, basicRefused("admin", "secret"))

		// The env recovery opens the gate and raises no token.
		config.AppConfig.BasicAuthPassword = ""
		require.NoError(t, os.Setenv(basicGateEnvName, "recover"))
		assert.NotContains(t, health(), servermiddleware.BasicGateNoPasswordToken)
		assert.False(t, basicRefused("admin", "recover"))
		assert.True(t, basicRefused("admin", ""))
		assert.Equal(t, "", config.AppConfig.BasicAuthPassword, "the env value must never be written")
	})
}

// TestStart_LogsBasicGatePosture: Start names the setting right after it
// stores its runner, before any step can fail: at error when the gate is on
// with no password at all, at warn when BASIC_AUTH_PASSWORD stands in for an
// empty saved one, and not at all otherwise. The value is never logged.
func TestStart_LogsBasicGatePosture(t *testing.T) {
	const noPasswordLine = "Basic authentication is on (basic_auth_enabled) but no password is saved (basic_auth_password): every route except health answers 401. Set BASIC_AUTH_PASSWORD and restart, then save a password in Settings."
	const recovery = "alfred-pennyworth-recovery"

	tests := []struct {
		name      string
		enabled   bool
		stored    string
		env       *string
		wantLevel string // "" means neither line
		wantLine  string
	}{
		{name: "on, nothing saved, env unset", enabled: true, stored: "", env: nil,
			wantLevel: "level=ERROR", wantLine: noPasswordLine},
		{name: "on, nothing saved, env blank", enabled: true, stored: "", env: func() *string { s := "  "; return &s }(),
			wantLevel: "level=ERROR", wantLine: noPasswordLine},
		{name: "on, nothing saved, env set", enabled: true, stored: "", env: func() *string { s := recovery; return &s }(),
			wantLevel: "level=WARN", wantLine: servermiddleware.BasicGateEnvPasswordWarning},
		{name: "on, password saved", enabled: true, stored: "secret", env: nil},
		{name: "off", enabled: false, stored: "", env: nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			setBasicGate(t, tc.enabled, "admin", tc.stored, tc.env)

			var mu sync.Mutex
			var buf bytes.Buffer
			prev := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&lockedBuffer{mu: &mu, b: &buf}, nil)))
			t.Cleanup(func() { slog.SetDefault(prev) })

			// A fatal first step stops Start right after the posture line.
			s := &Server{opRegistrationErrs: []error{errors.New("registry: RegisterOp(x): boom")}}
			require.Error(t, s.Start(ServerConfig{StartupSteps: startup.New()}))

			mu.Lock()
			out := buf.String()
			mu.Unlock()
			assert.NotContains(t, out, recovery, "the BASIC_AUTH_PASSWORD value must never be logged")
			if tc.wantLine == "" {
				assert.NotContains(t, out, noPasswordLine)
				assert.NotContains(t, out, servermiddleware.BasicGateEnvPasswordWarning)
				return
			}
			var line string
			for _, l := range strings.Split(out, "\n") {
				if strings.Contains(l, tc.wantLine) {
					line = l
					break
				}
			}
			require.NotEmpty(t, line, "startup log does not carry %q; log:\n%s", tc.wantLine, out)
			assert.Contains(t, line, tc.wantLevel)
		})
	}
}

// lockedBuffer serialises writes into a shared buffer for the slog capture.
type lockedBuffer struct {
	mu *sync.Mutex
	b  *bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}
