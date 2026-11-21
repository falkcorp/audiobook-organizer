// file: internal/server/basic_gate_test.go
// version: 1.1.0
// guid: 3b7e1d90-6a42-4c15-8f0e-d2a94c6b7e51
// last-edited: 2026-11-19

package server

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	systemhandlers "github.com/falkcorp/audiobook-organizer/internal/server/handlers/system"
	"github.com/falkcorp/mediacore/secrets"
)

// saveBasicGateConfig restores the gate's config fields when the test ends
// and clears BASIC_AUTH_PASSWORD for it (t.Setenv registers the restore of
// the caller's value, so a value exported in the developer's shell never
// leaks into an "unset" row).
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

// TestBasicGatePassword_Precedence is HEAD's precedence (the removed gin
// middleware's basicGatePassword): a saved password wins, blanks included and
// compared as saved; an empty saved one falls back to BASIC_AUTH_PASSWORD,
// trimmed; an env value that is empty or only blanks is unset.
func TestBasicGatePassword_Precedence(t *testing.T) {
	cases := []struct {
		name     string
		stored   string
		env      *string
		want     string
		wantEnv  bool
		wantNone bool
	}{
		{name: "saved wins over env", stored: "Wayne-Manor", env: new("Arkham"), want: "Wayne-Manor"},
		{name: "saved blanks compared as saved", stored: "  ", env: new("Arkham"), want: "  "},
		{name: "empty saved, env recovers", stored: "", env: new("Arkham"), want: "Arkham", wantEnv: true},
		{name: "empty saved, env trimmed", stored: "", env: new("  Arkham \n"), want: "Arkham", wantEnv: true},
		{name: "empty saved, env blanks only", stored: "", env: new(" \t"), wantNone: true},
		{name: "empty saved, env empty", stored: "", env: new(""), wantNone: true},
		{name: "empty saved, env unset", stored: "", env: nil, wantNone: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			saveBasicGateConfig(t)
			setBasicGateEnv(t, c.env)
			got, fromEnv := basicGatePassword(c.stored)
			if got != c.want || fromEnv != c.wantEnv {
				t.Fatalf("basicGatePassword(%q) = (%q, %v), want (%q, %v)", c.stored, got, fromEnv, c.want, c.wantEnv)
			}
			v, err := basicGatePasswordFunc(func() string { return c.stored })(context.Background())
			if c.wantNone {
				if !errors.Is(err, secrets.ErrNotSet) || !v.IsZero() {
					t.Fatalf("resolver with no password = (%v, %v), want ErrNotSet and a zero Value", v.IsZero(), err)
				}
				return
			}
			if err != nil || string(v.Bytes()) != c.want {
				t.Fatalf("resolver = (%q, %v), want %q", v.Bytes(), err, c.want)
			}
		})
	}
}

// TestBasicGateSettings_ReadsTheEffectiveSwitchAndUsername: the settings
// closure follows the config the settings routes write (config.Mutate), and
// a disabled gate never reads the password.
func TestBasicGateSettings_ReadsTheEffectiveSwitchAndUsername(t *testing.T) {
	saveBasicGateConfig(t)
	config.Mutate(func(c *config.Config) {
		c.BasicAuthEnabled, c.BasicAuthUsername, c.BasicAuthPassword = true, "bwayne", "Wayne-Manor"
	})
	o := LegacyBasicGate()
	if s := o.Settings(); !s.Enabled || s.Username != "bwayne" {
		t.Fatalf("settings = %+v", s)
	}
	if o.Realm != "Audiobook Organizer" {
		t.Fatalf("realm = %q, want HEAD's", o.Realm)
	}
	config.Mutate(func(c *config.Config) { c.BasicAuthEnabled = false })
	if s := o.Settings(); s.Enabled {
		t.Fatalf("settings after disabling = %+v", s)
	}
}

// TestBasicGateEnvRecovery_WarnsOnceWithoutValue: while BASIC_AUTH_PASSWORD
// stands in for an empty saved password, the first request to reach the
// comparison logs one warn line per process naming the setting and the
// variable, never the value.
func TestBasicGateEnvRecovery_WarnsOnceWithoutValue(t *testing.T) {
	saveBasicGateConfig(t)
	const recovery = "alfred-pennyworth-recovery"
	config.Mutate(func(c *config.Config) {
		c.BasicAuthEnabled, c.BasicAuthUsername, c.BasicAuthPassword = true, "bwayne", ""
	})
	setBasicGateEnv(t, new(recovery))

	var mu sync.Mutex
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&lockedWriter{mu: &mu, w: &buf}, &slog.HandlerOptions{Level: slog.LevelDebug})))
	basicGateEnvWarnOnce = sync.Once{}
	t.Cleanup(func() {
		slog.SetDefault(prev)
		basicGateEnvWarnOnce = sync.Once{}
	})

	resolve := LegacyBasicGate().Password
	for range 3 {
		v, err := resolve.BasicGatePassword(context.Background())
		if err != nil || string(v.Bytes()) != recovery {
			t.Fatalf("resolver = (%q, %v), want the recovery password", v.Bytes(), err)
		}
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

// TestBasicGate_NoRaceWithConfigMutate: the gate's closures, the health probe
// and the startup line read through one config.Snapshot(), so they never race
// a PUT /config, which writes through config.Mutate. Under -race this fails
// if any of them reads config.AppConfig directly.
func TestBasicGate_NoRaceWithConfigMutate(t *testing.T) {
	saveBasicGateConfig(t)
	config.Mutate(func(c *config.Config) {
		c.BasicAuthEnabled, c.BasicAuthUsername, c.BasicAuthPassword = true, "bwayne", "Wayne-Manor"
	})
	setBasicGateEnv(t, new("recover"))
	o := LegacyBasicGate()
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		for i := 0; ; i++ {
			select {
			case <-done:
				return
			default:
			}
			config.Mutate(func(c *config.Config) {
				c.BasicAuthEnabled, c.BasicAuthUsername = true, "bwayne"
				if i%2 == 0 {
					c.BasicAuthPassword = ""
				} else {
					c.BasicAuthPassword = "Wayne-Manor"
				}
			})
		}
	})
	for range 200 {
		_ = o.Settings()
		_, _ = o.Password.BasicGatePassword(context.Background())
		_ = basicGateNoPassword()
		_ = basicGateUsesEnvPassword()
	}
	close(done)
	wg.Wait()
}

// TestBasicGateHealthToken: /health's basic_gate_no_password token and the
// startup line follow the effective password: saved, env recovery, none.
func TestBasicGateHealthToken(t *testing.T) {
	saveBasicGateConfig(t)
	set := func(enabled bool, pass string) {
		config.Mutate(func(c *config.Config) { c.BasicAuthEnabled, c.BasicAuthPassword = enabled, pass })
	}
	set(true, "")
	setBasicGateEnv(t, nil)
	if !basicGateNoPassword() || basicGateUsesEnvPassword() {
		t.Fatal("enabled, nothing saved, no env: want no password")
	}
	setBasicGateEnv(t, new("recover"))
	if basicGateNoPassword() || !basicGateUsesEnvPassword() {
		t.Fatal("env recovery: want a password, from the env")
	}
	set(true, "Wayne-Manor")
	if basicGateNoPassword() || basicGateUsesEnvPassword() {
		t.Fatal("saved password: want a password, not from the env")
	}
	set(false, "")
	setBasicGateEnv(t, nil)
	if basicGateNoPassword() {
		t.Fatal("a disabled gate has no missing password")
	}
}

// TestBasicGateNoPasswordToken_SystemHandlerMatchesCore: the system handler
// keeps its own copy of the token (it may not import core's httpapi) to let
// it through /health's unauthenticated filter; a rename in core would
// silently drop it from the answer, so the two are pinned equal here.
func TestBasicGateNoPasswordToken_SystemHandlerMatchesCore(t *testing.T) {
	if systemhandlers.DegradedBasicGateNoPassword != BasicGateNoPasswordToken {
		t.Fatalf("system handler token %q != core's %q", systemhandlers.DegradedBasicGateNoPassword, BasicGateNoPasswordToken)
	}
}
