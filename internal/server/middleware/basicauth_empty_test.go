// file: internal/server/middleware/basicauth_empty_test.go
// version: 1.1.0
// guid: 07da5163-5bdc-4fe7-a4a8-af96d141c0c4
// last-edited: 2026-11-05

package middleware

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
)

// saveBasicGateConfig restores the gate's config fields and BASIC_AUTH_PASSWORD
// when the test ends. t.Setenv registers the restore of the caller's value, so
// the rows below may then set and unset the variable with os.Setenv and
// os.Unsetenv (t.Setenv cannot express "unset"), and a value exported in the
// developer's shell never leaks into an "unset" row.
func saveBasicGateConfig(t *testing.T) {
	t.Helper()
	prevEnabled := config.AppConfig.BasicAuthEnabled
	prevUser := config.AppConfig.BasicAuthUsername
	prevPass := config.AppConfig.BasicAuthPassword
	t.Cleanup(func() {
		config.AppConfig.BasicAuthEnabled = prevEnabled
		config.AppConfig.BasicAuthUsername = prevUser
		config.AppConfig.BasicAuthPassword = prevPass
	})
	t.Setenv(basicAuthPasswordEnv, "")
}

// setBasicGateEnv sets BASIC_AUTH_PASSWORD to *v, or unsets it when v is nil.
// Only call it after saveBasicGateConfig.
func setBasicGateEnv(t *testing.T, v *string) {
	t.Helper()
	var err error
	if v == nil {
		err = os.Unsetenv(basicAuthPasswordEnv)
	} else {
		err = os.Setenv(basicAuthPasswordEnv, *v)
	}
	if err != nil {
		t.Fatalf("set %s: %v", basicAuthPasswordEnv, err)
	}
}

func strPtr(s string) *string { return &s }

type basicCreds struct{ user, pass string }

// TestLegacyBasicGate_EmptyPasswordRefuses is early fix 5 (05-authn §11.1
// item 5) in HEAD's code: an enabled gate whose effective password is empty
// refuses every non-exempt route, whatever credentials the request sends, and
// BASIC_AUTH_PASSWORD supplies the password, in memory only, when the saved
// one is empty.
func TestLegacyBasicGate_EmptyPasswordRefuses(t *testing.T) {
	saveBasicGateConfig(t)

	tests := []struct {
		name    string
		enabled bool
		user    string
		stored  string
		env     *string // nil: unset
		path    string
		creds   *basicCreds // nil: no Authorization header
		want    int
		// wantNoPassword is BasicGateNoPassword() for the row's state;
		// wantFromEnv is BasicGateUsesEnvPassword().
		wantNoPassword bool
		wantFromEnv    bool
	}{
		// The brief's table (05-authn §11.1 item 5).
		{name: "empty stored, env unset, empty password refused",
			enabled: true, user: "admin", stored: "", env: nil, path: "/api/v1/audiobooks",
			creds: &basicCreds{"admin", ""}, want: http.StatusUnauthorized, wantNoPassword: true},
		{name: "empty stored, env unset, no credentials refused",
			enabled: true, user: "admin", stored: "", env: nil, path: "/api/v1/audiobooks",
			creds: nil, want: http.StatusUnauthorized, wantNoPassword: true},
		{name: "empty stored, env unset, any password refused",
			enabled: true, user: "admin", stored: "", env: nil, path: "/api/v1/audiobooks",
			creds: &basicCreds{"admin", "anything"}, want: http.StatusUnauthorized, wantNoPassword: true},
		{name: "empty stored, env set empty counts as unset",
			enabled: true, user: "admin", stored: "", env: strPtr(""), path: "/api/v1/audiobooks",
			creds: &basicCreds{"admin", ""}, want: http.StatusUnauthorized, wantNoPassword: true},
		{name: "empty stored, env recovers in memory",
			enabled: true, user: "admin", stored: "", env: strPtr("recover"), path: "/api/v1/audiobooks",
			creds: &basicCreds{"admin", "recover"}, want: http.StatusOK, wantFromEnv: true},
		{name: "empty stored, env set, empty password still refused",
			enabled: true, user: "admin", stored: "", env: strPtr("recover"), path: "/api/v1/audiobooks",
			creds: &basicCreds{"admin", ""}, want: http.StatusUnauthorized, wantFromEnv: true},
		{name: "saved password beats env",
			enabled: true, user: "admin", stored: "secret", env: strPtr("recover"), path: "/api/v1/audiobooks",
			creds: &basicCreds{"admin", "recover"}, want: http.StatusUnauthorized},
		{name: "saved password, env unset, accepted",
			enabled: true, user: "admin", stored: "secret", env: nil, path: "/api/v1/audiobooks",
			creds: &basicCreds{"admin", "secret"}, want: http.StatusOK},
		{name: "empty stored, env unset, health stays exempt",
			enabled: true, user: "admin", stored: "", env: nil, path: "/api/v1/health",
			creds: nil, want: http.StatusOK, wantNoPassword: true},

		// Beyond the brief's table: the rest of the state space it implies.
		{name: "env of blanks counts as unset",
			enabled: true, user: "admin", stored: "", env: strPtr(" \t\n"), path: "/api/v1/audiobooks",
			creds: &basicCreds{"admin", ""}, want: http.StatusUnauthorized, wantNoPassword: true},
		{name: "env of blanks, blank password refused",
			enabled: true, user: "admin", stored: "", env: strPtr(" \t\n"), path: "/api/v1/audiobooks",
			creds: &basicCreds{"admin", " \t\n"}, want: http.StatusUnauthorized, wantNoPassword: true},
		{name: "env value is compared trimmed",
			enabled: true, user: "admin", stored: "", env: strPtr("  recover\n"), path: "/api/v1/audiobooks",
			creds: &basicCreds{"admin", "recover"}, want: http.StatusOK, wantFromEnv: true},
		// Blank counts as unset for the stored value too. With no saved row
		// the loader puts BASIC_AUTH_PASSWORD into basic_auth_password
		// untrimmed, so a blank variable arrives as a blank stored value.
		{name: "no saved row, env of only spaces loaded as the stored value, refused",
			enabled: true, user: "admin", stored: "   ", env: strPtr("   "), path: "/api/v1/audiobooks",
			creds: &basicCreds{"admin", "   "}, want: http.StatusUnauthorized, wantNoPassword: true},
		{name: "saved value of only spaces, env unset, refused",
			enabled: true, user: "admin", stored: "   ", env: nil, path: "/api/v1/audiobooks",
			creds: &basicCreds{"admin", "   "}, want: http.StatusUnauthorized, wantNoPassword: true},
		{name: "saved value of only blanks, empty password refused",
			enabled: true, user: "admin", stored: " \t", env: nil, path: "/api/v1/audiobooks",
			creds: &basicCreds{"admin", ""}, want: http.StatusUnauthorized, wantNoPassword: true},
		{name: "saved value of only spaces, env recovers",
			enabled: true, user: "admin", stored: "   ", env: strPtr("recover"), path: "/api/v1/audiobooks",
			creds: &basicCreds{"admin", "recover"}, want: http.StatusOK, wantFromEnv: true},
		{name: "saved value of only spaces, env set, the spaces refused",
			enabled: true, user: "admin", stored: "   ", env: strPtr("recover"), path: "/api/v1/audiobooks",
			creds: &basicCreds{"admin", "   "}, want: http.StatusUnauthorized, wantFromEnv: true},
		{name: "saved password with surrounding spaces is compared exactly",
			enabled: true, user: "admin", stored: " secret ", env: nil, path: "/api/v1/audiobooks",
			creds: &basicCreds{"admin", " secret "}, want: http.StatusOK},
		{name: "saved password with surrounding spaces is not trimmed",
			enabled: true, user: "admin", stored: " secret ", env: nil, path: "/api/v1/audiobooks",
			creds: &basicCreds{"admin", "secret"}, want: http.StatusUnauthorized},
		{name: "env recovery still checks the username",
			enabled: true, user: "admin", stored: "", env: strPtr("recover"), path: "/api/v1/audiobooks",
			creds: &basicCreds{"joker", "recover"}, want: http.StatusUnauthorized, wantFromEnv: true},
		{name: "empty stored, env unset, static asset stays exempt",
			enabled: true, user: "admin", stored: "", env: nil, path: "/assets/main.js",
			creds: nil, want: http.StatusOK, wantNoPassword: true},
		{name: "empty username and empty password refused",
			enabled: true, user: "", stored: "", env: nil, path: "/api/v1/audiobooks",
			creds: &basicCreds{"", ""}, want: http.StatusUnauthorized, wantNoPassword: true},
		// An enabled gate with an empty username keeps HEAD's behaviour and
		// raises no token (05-health §5H.3.1).
		{name: "empty username with a saved password keeps HEAD's behaviour",
			enabled: true, user: "", stored: "secret", env: nil, path: "/api/v1/audiobooks",
			creds: &basicCreds{"", "secret"}, want: http.StatusOK},
		{name: "gate off with no password serves and raises nothing",
			enabled: false, user: "admin", stored: "", env: nil, path: "/api/v1/audiobooks",
			creds: nil, want: http.StatusOK},
		{name: "gate off with env set raises nothing",
			enabled: false, user: "admin", stored: "", env: strPtr("recover"), path: "/api/v1/audiobooks",
			creds: nil, want: http.StatusOK},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			config.AppConfig.BasicAuthEnabled = tc.enabled
			config.AppConfig.BasicAuthUsername = tc.user
			config.AppConfig.BasicAuthPassword = tc.stored
			setBasicGateEnv(t, tc.env)

			r := setupBasicAuthRouter()
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			if tc.creds != nil {
				req.SetBasicAuth(tc.creds.user, tc.creds.pass)
			}
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			if w.Code != tc.want {
				t.Fatalf("GET %s = %d, want %d", tc.path, w.Code, tc.want)
			}
			if tc.want == http.StatusUnauthorized && !basicChallenged(w) {
				t.Errorf("401 without HEAD's challenge: WWW-Authenticate = %q", w.Header().Get("WWW-Authenticate"))
			}
			if got := BasicGateNoPassword(); got != tc.wantNoPassword {
				t.Errorf("BasicGateNoPassword() = %v, want %v", got, tc.wantNoPassword)
			}
			if got := BasicGateUsesEnvPassword(); got != tc.wantFromEnv {
				t.Errorf("BasicGateUsesEnvPassword() = %v, want %v", got, tc.wantFromEnv)
			}
			// The env fallback is read, never written (invariant 2).
			if config.AppConfig.BasicAuthPassword != tc.stored {
				t.Errorf("config.AppConfig.BasicAuthPassword = %q after the request, want %q (the env value must stay in memory)",
					config.AppConfig.BasicAuthPassword, tc.stored)
			}
		})
	}
}

// TestLegacyBasicGate_EmptyPasswordKeepsABSExemption: with no password the ABS
// routes that run ABSRequireAuth stay exempt (invariant 4), so ABS token auth,
// not the Basic gate, refuses a credential-less request, and the ABS routes
// with no token auth stay challenged.
func TestLegacyBasicGate_EmptyPasswordKeepsABSExemption(t *testing.T) {
	saveBasicGateConfig(t)
	config.AppConfig.BasicAuthEnabled = true
	config.AppConfig.BasicAuthUsername = "admin"
	config.AppConfig.BasicAuthPassword = ""
	setBasicGateEnv(t, nil)

	r := setupABSBasicAuthRouter(nil)
	w := serveBasic(r, http.MethodGet, "/api/me", nil)
	if w.Code != http.StatusUnauthorized || basicChallenged(w) {
		t.Fatalf("GET /api/me = %d (Basic challenge %v), want 401 from ABS token auth with no Basic challenge",
			w.Code, basicChallenged(w))
	}
	if w := serveBasic(r, http.MethodGet, "/ping", nil); !basicChallenged(w) {
		t.Fatalf("GET /ping = %d, want 401 Basic challenge", w.Code)
	}
}

// TestBasicGateEnvRecovery_WarnsOnceWithoutValue: while BASIC_AUTH_PASSWORD
// stands in for an empty saved password, the gate logs one warn line per
// process naming the setting and the variable, and never the value.
func TestBasicGateEnvRecovery_WarnsOnceWithoutValue(t *testing.T) {
	saveBasicGateConfig(t)
	const recovery = "alfred-pennyworth-recovery"
	config.AppConfig.BasicAuthEnabled = true
	config.AppConfig.BasicAuthUsername = "admin"
	config.AppConfig.BasicAuthPassword = ""
	setBasicGateEnv(t, strPtr(recovery))

	var mu sync.Mutex
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&lockedWriter{mu: &mu, w: &buf}, &slog.HandlerOptions{Level: slog.LevelDebug})))
	basicGateEnvWarnOnce = sync.Once{}
	t.Cleanup(func() {
		slog.SetDefault(prev)
		basicGateEnvWarnOnce = sync.Once{}
	})

	r := setupBasicAuthRouter()
	for _, pass := range []string{"wrong", recovery, recovery} {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/audiobooks", nil)
		req.SetBasicAuth("admin", pass)
		r.ServeHTTP(httptest.NewRecorder(), req)
	}

	mu.Lock()
	out := buf.String()
	mu.Unlock()
	if n := strings.Count(out, BasicGateEnvPasswordWarning); n != 1 {
		t.Fatalf("warn line logged %d times, want once per process; log:\n%s", n, out)
	}
	if !strings.Contains(out, "level=WARN") {
		t.Errorf("warn line not at warn level; log:\n%s", out)
	}
	for _, name := range []string{"basic_auth_password", "BASIC_AUTH_PASSWORD"} {
		if !strings.Contains(BasicGateEnvPasswordWarning, name) {
			t.Errorf("warn line does not name %s: %q", name, BasicGateEnvPasswordWarning)
		}
	}
	if strings.Contains(out, recovery) {
		t.Fatalf("the log carries the BASIC_AUTH_PASSWORD value; log:\n%s", out)
	}
}

// lockedWriter serialises writes into a shared buffer for the slog capture.
type lockedWriter struct {
	mu *sync.Mutex
	w  *bytes.Buffer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// TestBasicGate_NoRaceWithConfigMutate: the gate, BasicGateNoPassword and
// BasicGateUsesEnvPassword read their fields from one config.Snapshot(), so
// they never race a PUT /config, which writes through config.Mutate. Under
// -race this fails if any of them reads config.AppConfig directly.
func TestBasicGate_NoRaceWithConfigMutate(t *testing.T) {
	saveBasicGateConfig(t)
	config.AppConfig.BasicAuthEnabled = true
	config.AppConfig.BasicAuthUsername = "admin"
	config.AppConfig.BasicAuthPassword = "secret"
	setBasicGateEnv(t, strPtr("recover"))

	r := setupBasicAuthRouter()
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-done:
				return
			default:
			}
			config.Mutate(func(c *config.Config) {
				c.BasicAuthEnabled = true
				c.BasicAuthUsername = "admin"
				if i%2 == 0 {
					c.BasicAuthPassword = ""
				} else {
					c.BasicAuthPassword = "secret"
				}
			})
		}
	}()
	for i := 0; i < 200; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/audiobooks", nil)
		req.SetBasicAuth("admin", "secret")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK && w.Code != http.StatusUnauthorized {
			t.Fatalf("GET = %d, want 200 or 401", w.Code)
		}
		_ = BasicGateNoPassword()
		_ = BasicGateUsesEnvPassword()
	}
	close(done)
	wg.Wait()
}
