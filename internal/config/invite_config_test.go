// file: internal/config/invite_config_test.go
// version: 1.0.0
// guid: 8c2f4a19-7d3e-4b65-9e10-a5b7c3d9e2f1
// last-edited: 2026-10-19

package config

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// Sentinel values: if any of them shows up in serialized output, a secret leaked.
const (
	testSMTPPassword   = "smtp-pass-SENTINEL-1f2e"
	testCFClientID     = "client-id-SENTINEL-3a4b.access"
	testCFClientSecret = "client-secret-SENTINEL-5c6d"
	testCFPolicyToken  = "policy-token-SENTINEL-7e8f"
	testSMTPUsername   = "mailer-SENTINEL@example.com"
)

func setInviteEnv(t *testing.T) {
	t.Helper()
	t.Setenv("SMTP_HOST", "127.0.0.1")
	t.Setenv("SMTP_PORT", "25")
	t.Setenv("SMTP_USERNAME", testSMTPUsername)
	t.Setenv("SMTP_PASSWORD", testSMTPPassword)
	t.Setenv("SMTP_FROM", "Books <books@example.com>")
	t.Setenv("PUBLIC_BASE_URL", "https://books.example.com/")
	t.Setenv("INVITE_CF_ACCESS_CLIENT_ID", testCFClientID)
	t.Setenv("INVITE_CF_ACCESS_CLIENT_SECRET", testCFClientSecret)
	t.Setenv("CF_ACCOUNT_ID", "0123456789abcdef0123456789abcdef")
	t.Setenv("CF_GUESTS_POLICY_ID", "11111111-2222-3333-4444-555555555555")
	t.Setenv("CF_POLICY_API_TOKEN", testCFPolicyToken)
}

func TestInviteConfig_DefaultsAreInert(t *testing.T) {
	resetViper(t)
	registerInviteDefaults()
	var c Config
	applyInviteEnv(&c)
	if c.InviteMailConfigured() || c.InviteHeadersConfigured() || c.GuestPolicySyncConfigured() {
		t.Fatalf("an unconfigured server must have every invite feature off: %+v", c)
	}
	if c.SMTPPort != DefaultSMTPPort {
		t.Fatalf("smtp_port default = %d, want %d", c.SMTPPort, DefaultSMTPPort)
	}
}

func TestInviteConfig_EnvReachesConfigAndSurvivesBlobOverlay(t *testing.T) {
	resetViper(t)
	registerInviteDefaults()
	setInviteEnv(t)

	// applyEnvAuthoritativeConfig runs after the blob load replaced the struct.
	c := Config{}
	applyEnvAuthoritativeConfig(&c)
	if c.SMTPHost != "127.0.0.1" || c.SMTPPort != 25 || c.SMTPPassword != testSMTPPassword {
		t.Fatalf("SMTP settings not applied: host=%q port=%d", c.SMTPHost, c.SMTPPort)
	}
	if !c.InviteMailConfigured() || !c.InviteHeadersConfigured() || !c.GuestPolicySyncConfigured() {
		t.Fatal("all three invite features must report configured")
	}
	if got := c.InviteBaseURL("http://fallback"); got != "https://books.example.com" {
		t.Fatalf("InviteBaseURL = %q", got)
	}
}

func TestInviteConfig_PartialSettingsStayInert(t *testing.T) {
	c := Config{InviteCFAccessClientID: "id-only", CFAccountID: "acct", CFGuestsPolicyID: "pol"}
	if c.InviteHeadersConfigured() {
		t.Fatal("one half of the header pair must not count as configured")
	}
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
	registerInviteDefaults()
	setInviteEnv(t)
	c := Config{}
	applyInviteEnv(&c)
	us := &UpdateService{}
	for name, v := range map[string]Config{"raw": c, "masked": us.MaskSecrets(c)} {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		out := string(b)
		for _, secret := range []string{testSMTPPassword, testCFClientID, testCFClientSecret, testCFPolicyToken, testSMTPUsername} {
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

	for _, key := range []string{"smtp_password", "SMTP_Host", "cf_policy_api_token", "invite_cf_access_client_secret"} {
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
