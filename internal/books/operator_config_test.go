// file: internal/books/operator_config_test.go
// version: 0.1.0
// guid: 9e2c5a17-4d83-4f6b-b1a0-3c7e8d2f5b94
// last-edited: 2026-11-20

package books

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
)

// operatorEnv isolates OperatorAppConfig from the host: a temporary HOME and
// state directory.
func operatorEnv(t *testing.T) string {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	state := filepath.Join(t.TempDir(), "state")
	t.Setenv(config.SecureStateDirEnv, state)
	return state
}

// TestOperatorAppConfig_HEADFormWhisperEndpoints (F3-07 rehearsal finding 2):
// a WHISPER_ENDPOINTS value in HEAD's form (url alone, no label) is accepted
// under its canonical name, as HEAD accepted it, and nothing is created.
func TestOperatorAppConfig_HEADFormWhisperEndpoints(t *testing.T) {
	state := operatorEnv(t)
	t.Setenv("ABO_BOOKS_WHISPER_ENDPOINTS", `[{"url":"http://192.0.2.10:9000"},{"url":"http://192.0.2.11:9000","concurrency":2,"label":"batcave"}]`)
	cfg, err := OperatorAppConfig(t.Context(), Module(), BootInputs{})
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.WhisperEndpoints) != 2 || cfg.WhisperEndpoints[0].URL != "http://192.0.2.10:9000" || cfg.WhisperEndpoints[1].Label != "batcave" {
		t.Errorf("WhisperEndpoints = %+v", cfg.WhisperEndpoints)
	}
	if _, err := os.Stat(state); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("state dir: stat err = %v, want it never created", err)
	}
}

// TestOperatorAppConfig_RefusesRetiredAndBase: a boot-refusing retired name
// refuses; with none, the base fields (no row stores them) hold their values.
func TestOperatorAppConfig_RefusesRetiredAndBase(t *testing.T) {
	operatorEnv(t)
	cfg, err := OperatorAppConfig(t.Context(), Module(), BootInputs{})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DatabaseType != "pebble" || !cfg.AIEndpointsRouting {
		t.Errorf("base fields: DatabaseType %q, AIEndpointsRouting %v", cfg.DatabaseType, cfg.AIEndpointsRouting)
	}
	t.Setenv("PLAYLIST_DIR", "/srv/gotham/playlists")
	if _, err := OperatorAppConfig(t.Context(), Module(), BootInputs{}); err == nil {
		t.Fatal("a boot-refusing retired name was accepted")
	}
}

// TestOperatorAppConfig_RenameMapForms (F3-07 rehearsal findings 1 and 2):
// every live HEAD name the flip retires, set under its canonical name in the
// typed form the operator rename map gives (true/false for a bool, a JSON list
// for a csv-list key, a number for an int), projects into the HEAD Config
// field HEAD read it into. A name whose form does not decode refuses here, as
// the boot would.
func TestOperatorAppConfig_RenameMapForms(t *testing.T) {
	operatorEnv(t)
	for name, val := range map[string]string{
		"ABO_BOOKS_ABS_ACCESS_TOKEN_TTL":            "2h",
		"ABO_BOOKS_ABS_API_ENABLED":                 "true",
		"ABO_BOOKS_ABS_AUTH_MODES":                  "jwt,apikey",
		"ABO_BOOKS_ABS_AUTH_PROBE_ENABLED":          "true",
		"ABO_BOOKS_ABS_JWT_SECRET":                  "Lucius-abs-jwt-secret-0901",
		"ABO_BOOKS_ABS_REFRESH_TOKEN_TTL":           "48h",
		"ABO_BOOKS_ABS_SESSION_MAX_TTL":             "96h",
		"ABO_BOOKS_ACTIVITY_BACKEND":                "pebble",
		"ABO_CORE_HTTP_PEER_ALLOWLIST":              `["192.0.2.0/24","198.51.100.0/24"]`,
		"ABO_BOOKS_IDENTITY_CF_ACCESS_AUD":          "batcave-aud",
		"ABO_BOOKS_IDENTITY_CF_ACCESS_TEAM_DOMAIN":  "batcave.example",
		"ABO_BOOKS_CFGUESTS_POLICY_API_TOKEN":       "Fox-policy-token-0902",
		"ABO_BOOKS_CFGUESTS_ACCOUNT_ID":             "wayne-account",
		"ABO_BOOKS_CFGUESTS_GUESTS_POLICY_ID":       "arkham-guests",
		"ABO_BOOKS_CFGUESTS_GUEST_SERVICE_TOKENS":   "true",
		"ABO_BOOKS_CFGUESTS_SERVICE_AUTH_POLICY_ID": "arkham-service",
		"ABO_BOOKS_IDENTITY_OWNER_EMAIL":            "bruce@batcave.example",
		"ABO_CORE_HTTP_PUBLIC_BASE_URL":             "https://batcave.example",
		"ABO_INTEGRATIONS_NOTIFY_EMAIL_HOST":        "mail.batcave.example",
		"ABO_INTEGRATIONS_NOTIFY_EMAIL_PORT":        "587",
		"ABO_INTEGRATIONS_NOTIFY_EMAIL_FROM":        "alfred@batcave.example",
		"ABO_CORE_HTTP_TRUSTED_PROXIES":             `["192.0.2.30"]`,
		"ABO_BOOKS_WHISPER_REQUIRES":                `["gpu","en"]`,
		"ABO_BOOKS_CFACCESS_ALLOWED_EMAILS":         "bruce@batcave.example,alfred@batcave.example",
	} {
		t.Setenv(name, val)
	}
	cfg, err := OperatorAppConfig(t.Context(), Module(), BootInputs{})
	if err != nil {
		t.Fatal(err)
	}
	for field, ok := range map[string]bool{
		"ABSAccessTokenTTL":     cfg.ABSAccessTokenTTL == "2h",
		"ABSAPIEnabled":         cfg.ABSAPIEnabled,
		"ABSAuthModes":          cfg.ABSAuthModes == "jwt,apikey",
		"ABSAuthProbeEnabled":   cfg.ABSAuthProbeEnabled,
		"ABSRefreshTokenTTL":    cfg.ABSRefreshTokenTTL == "48h",
		"ABSSessionMaxTTL":      cfg.ABSSessionMaxTTL == "96h",
		"ActivityBackend":       cfg.ActivityBackend == "pebble",
		"AllowedPeerCIDRs":      cfg.AllowedPeerCIDRs == "192.0.2.0/24,198.51.100.0/24",
		"CFAccessAud":           cfg.CFAccessAud == "batcave-aud",
		"CFAccessTeamDomain":    cfg.CFAccessTeamDomain == "batcave.example",
		"CFAccountID":           cfg.CFAccountID == "wayne-account",
		"CFGuestsPolicyID":      cfg.CFGuestsPolicyID == "arkham-guests",
		"CFGuestServiceTokens":  cfg.CFGuestServiceTokens,
		"CFServiceAuthPolicyID": cfg.CFServiceAuthPolicyID == "arkham-service",
		"OwnerEmail":            cfg.OwnerEmail == "bruce@batcave.example",
		"PublicBaseURL":         cfg.PublicBaseURL == "https://batcave.example",
		"SMTPHost":              cfg.SMTPHost == "mail.batcave.example",
		"SMTPPort":              cfg.SMTPPort == 587,
		"SMTPFrom":              cfg.SMTPFrom == "alfred@batcave.example",
		"TunnelConnectorAddrs":  cfg.TunnelConnectorAddrs == "192.0.2.30",
		"WhisperRequires":       len(cfg.WhisperRequires) == 2 && cfg.WhisperRequires[0] == "gpu",
		"OAuthAllowedEmails":    cfg.OAuthAllowedEmails == "bruce@batcave.example,alfred@batcave.example",
	} {
		if !ok {
			t.Errorf("%s did not project from its canonical name", field)
		}
	}
}

// TestOperatorAppConfig_PassthroughPortIsANumber: an environment value for a
// passthrough path reaches config as text, a JSON string; the projection reads
// it into HEAD's int field as the number, and refuses text that is not a whole
// number, naming the key and the layer and never the value.
func TestOperatorAppConfig_PassthroughPortIsANumber(t *testing.T) {
	operatorEnv(t)
	t.Setenv("ABO_INTEGRATIONS_NOTIFY_EMAIL_HOST", "mail.batcave.example")
	t.Setenv("ABO_INTEGRATIONS_NOTIFY_EMAIL_PORT", "2525")
	cfg, err := OperatorAppConfig(t.Context(), Module(), BootInputs{})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SMTPPort != 2525 {
		t.Errorf("SMTPPort = %d, want 2525", cfg.SMTPPort)
	}
	t.Setenv("ABO_INTEGRATIONS_NOTIFY_EMAIL_PORT", "joker-port")
	_, err = OperatorAppConfig(t.Context(), Module(), BootInputs{})
	if err == nil {
		t.Fatal("a non-numeric port was accepted")
	}
	for _, want := range []string{"integrations.notify.email.port", "ABO_INTEGRATIONS_NOTIFY_EMAIL_PORT"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %q: %v", want, err)
		}
	}
	if strings.Contains(err.Error(), "joker-port") {
		t.Errorf("the refusal carries the value: %v", err)
	}
}
