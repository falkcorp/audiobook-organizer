// file: internal/config/invite_config_test.go
// version: 1.6.2
// guid: 8c2f4a19-7d3e-4b65-9e10-a5b7c3d9e2f1
// last-edited: 2026-10-30

package config

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/spf13/viper"
)

// Sentinel values: if any of them shows up in serialized output, a secret leaked.
const (
	testSMTPPassword  = "smtp-pass-SENTINEL-1f2e"
	testCFPolicyToken = "policy-token-SENTINEL-7e8f"
	testSMTPUsername  = "mailer-SENTINEL@example.com"
)

func setInviteEnv(t *testing.T) {
	t.Helper()
	t.Setenv("SMTP_HOST", "127.0.0.1")
	t.Setenv("SMTP_PORT", "25")
	t.Setenv("SMTP_USERNAME", testSMTPUsername)
	t.Setenv("SMTP_PASSWORD", testSMTPPassword)
	t.Setenv("SMTP_FROM", "Books <books@example.com>")
	t.Setenv("PUBLIC_BASE_URL", "https://books.example.com/")
	t.Setenv("CF_ACCOUNT_ID", "0123456789abcdef0123456789abcdef")
	t.Setenv("CF_GUESTS_POLICY_ID", "11111111-2222-3333-4444-555555555555")
	t.Setenv("CF_POLICY_API_TOKEN", testCFPolicyToken)
	t.Setenv("TUNNEL_CONNECTOR_ADDRS", " 192.0.2.10, 198.51.100.7 ")
	t.Setenv("ALLOWED_PEER_CIDRS", " 192.0.2.0/24, 198.51.100.7 ")
}

func TestInviteConfig_DefaultsAreInert(t *testing.T) {
	resetViper(t)
	registerInviteDefaults(viper.GetViper())
	var c Config
	applyInviteEnv(&c, viper.GetViper())
	if c.InviteMailConfigured() || c.GuestPolicySyncConfigured() {
		t.Fatalf("an unconfigured server must have every invite feature off: %+v", c)
	}
	if c.SMTPPort != DefaultSMTPPort {
		t.Fatalf("smtp_port default = %d, want %d", c.SMTPPort, DefaultSMTPPort)
	}
	if c.TunnelConnectorAddrs != "" {
		t.Fatalf("tunnel_connector_addrs default = %q, want empty (no trusted connector)", c.TunnelConnectorAddrs)
	}
	if c.AllowedPeerCIDRs != "" {
		t.Fatalf("allowed_peer_cidrs default = %q, want empty (allow every peer, with a startup warning)", c.AllowedPeerCIDRs)
	}
}

func TestInviteConfig_EnvReachesConfigAndSurvivesBlobOverlay(t *testing.T) {
	resetViper(t)
	registerInviteDefaults(viper.GetViper())
	setInviteEnv(t)

	// applyEnvAuthoritativeConfig runs after the blob load replaced the struct.
	c := Config{}
	applyEnvAuthoritativeConfig(&c)
	if c.SMTPHost != "127.0.0.1" || c.SMTPPort != 25 || c.SMTPPassword != testSMTPPassword {
		t.Fatalf("SMTP settings not applied: host=%q port=%d", c.SMTPHost, c.SMTPPort)
	}
	if !c.InviteMailConfigured() || !c.GuestPolicySyncConfigured() {
		t.Fatal("both invite features must report configured")
	}
	if got := c.InviteBaseURL("http://fallback"); got != "https://books.example.com" {
		t.Fatalf("InviteBaseURL = %q", got)
	}
	if c.TunnelConnectorAddrs != "192.0.2.10, 198.51.100.7" {
		t.Fatalf("TunnelConnectorAddrs = %q, want the trimmed env value", c.TunnelConnectorAddrs)
	}
	if c.AllowedPeerCIDRs != "192.0.2.0/24, 198.51.100.7" {
		t.Fatalf("AllowedPeerCIDRs = %q, want the trimmed env value", c.AllowedPeerCIDRs)
	}
}

func TestInviteConfig_PartialSettingsStayInert(t *testing.T) {
	c := Config{CFAccountID: "acct", CFGuestsPolicyID: "pol"}
	if c.GuestPolicySyncConfigured() {
		t.Fatal("policy sync without its API token must be inert")
	}
	if (&Config{SMTPHost: "h"}).InviteMailConfigured() {
		t.Fatal("mail without a From address must be unconfigured")
	}
}

func TestInviteConfig_BaseURLFallbacks(t *testing.T) {
	c := Config{OAuthRedirectBaseURL: "https://sso.example.com/"}
	if got := c.InviteBaseURL("http://ext"); got != "https://sso.example.com" {
		t.Fatalf("got %q, want the oauth redirect base", got)
	}
	if got := (&Config{}).InviteBaseURL("http://ext/"); got != "http://ext" {
		t.Fatalf("got %q, want the external URL", got)
	}
	if got := (&Config{}).InviteBaseURL(""); got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}

// TestInviteConfig_NeverSerialized proves no invite value reaches JSON: not the
// GET /config response (which marshals Config, then masks), not the blob.
func TestInviteConfig_NeverSerialized(t *testing.T) {
	resetViper(t)
	registerInviteDefaults(viper.GetViper())
	setInviteEnv(t)
	c := Config{}
	applyInviteEnv(&c, viper.GetViper())
	us := &UpdateService{}
	for name, v := range map[string]Config{"raw": c, "masked": us.MaskSecrets(c)} {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		out := string(b)
		for _, secret := range []string{testSMTPPassword, testCFPolicyToken, testSMTPUsername} {
			if strings.Contains(out, secret) {
				t.Fatalf("%s config JSON contains an invite secret", name)
			}
		}
		for _, s := range inviteEnvOnlySettings {
			if strings.Contains(out, `"`+s.key+`"`) {
				t.Fatalf("%s config JSON carries key %s", name, s.key)
			}
		}
	}
	// Clone (the reflection deep copy) must keep json:"-" fields.
	if cl := c.Clone(); cl.CFPolicyAPIToken != testCFPolicyToken || cl.SMTPPassword != testSMTPPassword {
		t.Fatal("Clone dropped an invite field")
	}
}

func TestInviteConfig_UpdateRefusesEnvOnlyKeys(t *testing.T) {
	restoreAppConfig(t)
	Mutate(func(c *Config) { c.EnableAuth = false })
	ms, blobs := ladderTestStore(t)
	svc := NewUpdateService(ms)

	for _, key := range []string{"smtp_password", "SMTP_Host", "cf_policy_api_token", "tunnel_connector_addrs", "allowed_peer_cidrs", "login_saturated_rate", "LOGIN_SATURATED_BURST"} {
		status, resp := svc.UpdateConfig(context.Background(), map[string]any{
			key:         "x",
			"log_level": "warn",
		})
		if status != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, want 400; resp = %v", key, status, resp)
		}
		msg, _ := resp["error"].(string)
		if !strings.Contains(msg, EnvOnlySettingsRefusedMessage) {
			t.Fatalf("%s: error must be the env-only refusal: %q", key, msg)
		}
		if Snapshot().LogLevel == "warn" {
			t.Fatalf("%s: refused PUT applied log_level", key)
		}
	}
	if len(*blobs) != 0 {
		t.Fatal("refused PUT persisted a blob")
	}
}

// The shared guest service token was removed (2026-10-21). Its two names are
// no longer env-only settings: a PUT naming one gets the removal message, not
// the env-only refusal, and a stale value in the environment is warned about
// at startup and never reaches Config.
func TestInviteConfig_GuestServiceTokenIsGone(t *testing.T) {
	restoreAppConfig(t)
	Mutate(func(c *Config) { c.EnableAuth = false })
	ms, blobs := ladderTestStore(t)
	svc := NewUpdateService(ms)
	for _, key := range []string{"invite_cf_access_client_id", "invite_cf_access_client_secret"} {
		status, resp := svc.UpdateConfig(context.Background(), map[string]any{key: "x", "log_level": "warn"})
		if status != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, want 400; resp = %v", key, status, resp)
		}
		msg, _ := resp["error"].(string)
		if !strings.Contains(msg, "was removed") || strings.Contains(msg, EnvOnlySettingsRefusedMessage) {
			t.Fatalf("%s: want the removal message, got %q", key, msg)
		}
	}
	if len(*blobs) != 0 {
		t.Fatal("refused PUT persisted a blob")
	}
	for _, s := range inviteEnvOnlySettings {
		if strings.HasPrefix(s.key, "invite_cf_access") {
			t.Fatalf("%s is still bound as an invite setting", s.key)
		}
	}
}

func TestInitConfig_StaleGuestServiceTokenEnvWarns(t *testing.T) {
	restoreAppConfig(t)
	viper.Reset()
	t.Cleanup(viper.Reset)
	logs := captureRemovedKeyLogs(t)

	t.Setenv("INVITE_CF_ACCESS_CLIENT_ID", "stale-id.access")
	t.Setenv("INVITE_CF_ACCESS_CLIENT_SECRET", "stale-secret-SENTINEL")
	viper.AutomaticEnv() // same order as cmd/root.go initConfig

	InitConfig()

	out := logs.String()
	for _, k := range []string{"key=invite_cf_access_client_id ", "key=invite_cf_access_client_secret "} {
		if !strings.Contains(out, k) || !strings.Contains(out, "removed setting is ignored") {
			t.Errorf("expected a removed-setting WARN containing %q, got:\n%s", k, out)
		}
	}
	if strings.Contains(out, "stale-secret-SENTINEL") || strings.Contains(out, "stale-id.access") {
		t.Errorf("the warning must name the key, never log its value:\n%s", out)
	}
}

func TestLoginSaturatedLimits_DefaultsAndEnv(t *testing.T) {
	resetViper(t)
	registerInviteDefaults(viper.GetViper())
	var c Config
	applyInviteEnv(&c, viper.GetViper())
	rate, burst, err := parseLoginSaturatedLimits(c.LoginSaturatedRate, c.LoginSaturatedBurst)
	if err != nil || rate != DefaultLoginSaturatedRate || burst != DefaultLoginSaturatedBurst {
		t.Fatalf("unset keys: rate=%v burst=%d err=%v, want the defaults %v/%d", rate, burst, err, DefaultLoginSaturatedRate, DefaultLoginSaturatedBurst)
	}
	if DefaultLoginSaturatedRate != 2 || DefaultLoginSaturatedBurst != 8 {
		t.Fatalf("defaults are documented as 2/s burst 8, got %v/%d", DefaultLoginSaturatedRate, DefaultLoginSaturatedBurst)
	}

	t.Setenv("LOGIN_SATURATED_RATE", " 0.5 ")
	t.Setenv("LOGIN_SATURATED_BURST", " 3 ")
	resetViper(t)
	registerInviteDefaults(viper.GetViper())
	restoreAppConfig(t)
	var c2 Config
	applyInviteEnv(&c2, viper.GetViper())
	Mutate(func(cfg *Config) {
		cfg.LoginSaturatedRate, cfg.LoginSaturatedBurst = c2.LoginSaturatedRate, c2.LoginSaturatedBurst
	})
	rate, burst, err = LoginSaturatedLimits()
	if err != nil || rate != 0.5 || burst != 3 {
		t.Fatalf("env values: rate=%v burst=%d err=%v, want 0.5/3", rate, burst, err)
	}
}

func TestLoginSaturatedLimits_RejectsUnusableValues(t *testing.T) {
	for _, tc := range []struct{ rate, burst, want string }{
		{"abc", "", "LOGIN_SATURATED_RATE"},
		{"0", "", "LOGIN_SATURATED_RATE"},
		{"-1", "", "LOGIN_SATURATED_RATE"},
		{"NaN", "", "LOGIN_SATURATED_RATE"},
		{"Inf", "", "LOGIN_SATURATED_RATE"},
		{"1e9", "", "LOGIN_SATURATED_RATE"},
		{"50.5", "", "LOGIN_SATURATED_RATE"},
		{"0.009", "", "LOGIN_SATURATED_RATE"}, // below the floor: a refused caller would wait minutes
		{"0.0001", "", "LOGIN_SATURATED_RATE"},
		{"", "0", "LOGIN_SATURATED_BURST"},
		{"", "-3", "LOGIN_SATURATED_BURST"},
		{"", "2.5", "LOGIN_SATURATED_BURST"},
		{"", "lots", "LOGIN_SATURATED_BURST"},
		{"", "201", "LOGIN_SATURATED_BURST"},
	} {
		if _, _, err := parseLoginSaturatedLimits(tc.rate, tc.burst); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("rate=%q burst=%q: err=%v, want an error naming %s", tc.rate, tc.burst, err, tc.want)
		}
	}
	// The raw value is operator input read from the environment: the error names
	// the setting and the accepted range, and never echoes what was typed.
	for _, tc := range []struct{ rate, burst string }{{"zz-7.31-rate", ""}, {"", "zz-7.31-burst"}, {"0.0049999", ""}, {"", "4242424"}} {
		_, _, err := parseLoginSaturatedLimits(tc.rate, tc.burst)
		if err == nil {
			t.Fatalf("rate=%q burst=%q: expected an error", tc.rate, tc.burst)
		}
		for _, raw := range []string{tc.rate, tc.burst} {
			if raw != "" && strings.Contains(err.Error(), raw) {
				t.Errorf("error %q echoes the raw value %q", err, raw)
			}
		}
	}
	for _, tc := range []struct {
		rate, burst string
		wantRate    float64
		wantBurst   int
	}{
		{"50", "200", 50, 200}, {"0.01", "1", 0.01, 1}, {"", "", 2, 8},
	} {
		r, b, err := parseLoginSaturatedLimits(tc.rate, tc.burst)
		if err != nil || r != tc.wantRate || b != tc.wantBurst {
			t.Errorf("rate=%q burst=%q: %v/%d err=%v, want %v/%d", tc.rate, tc.burst, r, b, err, tc.wantRate, tc.wantBurst)
		}
	}
}

func TestGuestServiceTokens_FlagAndSettings(t *testing.T) {
	for in, want := range map[string]bool{"": false, "true": true, "TRUE": true, "1": true, " yes ": true, "on": true,
		"false": false, "0": false, "tru": false, "enable": false} {
		if got := parseEnvBool(in); got != want {
			t.Errorf("parseEnvBool(%q) = %v, want %v", in, got, want)
		}
	}
	full := Config{CFGuestServiceTokens: true, CFAccountID: "a", CFPolicyAPIToken: "t", CFServiceAuthPolicyID: "p"}
	if !full.GuestServiceTokensEnabled() || full.GuestServiceTokensMisconfigured() {
		t.Fatal("a complete configuration must be enabled")
	}
	for name, mut := range map[string]func(*Config){
		"flag off":   func(c *Config) { c.CFGuestServiceTokens = false },
		"no account": func(c *Config) { c.CFAccountID = "" },
		"no token":   func(c *Config) { c.CFPolicyAPIToken = "" },
		"no policy":  func(c *Config) { c.CFServiceAuthPolicyID = "" },
		// The books policy must not be the guest email policy.
		"same policy as the guests policy": func(c *Config) { c.CFGuestsPolicyID, c.CFServiceAuthPolicyID = "Pol-1", " pol-1 " },
	} {
		c := full
		mut(&c)
		if c.GuestServiceTokensEnabled() {
			t.Errorf("%s: still enabled", name)
		}
		if name != "flag off" && !c.GuestServiceTokensMisconfigured() {
			t.Errorf("%s: flag on but incomplete must be reported as misconfigured", name)
		}
	}
	if (&Config{}).GuestServiceTokensEnabled() {
		t.Fatal("the zero Config (default) must be off")
	}
}

func TestGuestServiceTokens_EnvBindingsDefaultOff(t *testing.T) {
	resetViper(t)
	registerInviteDefaults(viper.GetViper())
	c := Config{}
	applyInviteEnv(&c, viper.GetViper())
	if c.CFGuestServiceTokens || c.InviteEmailServiceToken || c.CFServiceAuthPolicyID != "" {
		t.Fatalf("defaults must be off/empty: %+v", c)
	}
	t.Setenv("CF_GUEST_SERVICE_TOKENS", "true")
	t.Setenv("CF_SERVICE_AUTH_POLICY_ID", " pol-id ")
	t.Setenv("INVITE_EMAIL_SERVICE_TOKEN", "true")
	applyInviteEnv(&c, viper.GetViper())
	if !c.CFGuestServiceTokens || !c.InviteEmailServiceToken || c.CFServiceAuthPolicyID != "pol-id" {
		t.Fatalf("env not applied: %+v", c)
	}
}
