// file: internal/config/abs_config_test.go
// version: 1.2.1
// guid: 71c4e830-6b29-4d5f-8a03-92e7b1c6df45
// last-edited: 2026-10-30

package config

import (
	"testing"

	"github.com/spf13/viper"
)

// resetViper gives each subtest a clean viper so a SetDefault from one case cannot
// leak into another.
func resetViper(t *testing.T) {
	t.Helper()
	viper.Reset()
	t.Cleanup(viper.Reset)
}

// TestABSConfig_DefaultsAreOffAndSafe pins the feature flag: with nothing set the ABS
// API is DISABLED, so an existing deployment that pulls this change registers no ABS
// route and behaves exactly as before.
func TestABSConfig_DefaultsAreOffAndSafe(t *testing.T) {
	resetViper(t)
	registerABSDefaults(viper.GetViper())

	if viper.GetBool("abs_api_enabled") {
		t.Fatal("ABS_API_ENABLED must default to false")
	}
	if got := viper.GetString("abs_auth_modes"); got != "cf,jwt" {
		t.Fatalf("abs_auth_modes must default to cf,jwt, got %q", got)
	}
	if got := viper.GetString("abs_jwt_secret"); got != "" {
		t.Fatalf("abs_jwt_secret must default to empty (never auto-generated), got %q", got)
	}
	// §1.6 item 1: 30 DAYS, not 1h.
	if got := viper.GetString("abs_access_token_ttl"); got != "720h" {
		t.Fatalf("abs_access_token_ttl must default to 720h (30d), got %q", got)
	}
	// The session idle TTL must outlive the access token (2026-10-19 forced-logout
	// fix): 60d against the 30d access token.
	if got := viper.GetString("abs_refresh_token_ttl"); got != "1440h" {
		t.Fatalf("abs_refresh_token_ttl must default to 1440h (60d), got %q", got)
	}
	if got := viper.GetString("abs_session_max_ttl"); got != "0" {
		t.Fatalf("abs_session_max_ttl must default to 0 (no absolute cap), got %q", got)
	}
	if got := viper.GetString("abs_refresh_grace"); got != "10m" {
		t.Fatalf("abs_refresh_grace must default to 10m, got %q", got)
	}
}

// TestABSConfig_EnvBindings verifies every ABS key is reachable from its environment
// variable. Nested/dotted keys need explicit BindEnv in this codebase (AutomaticEnv is
// not enabled), so a missing binding silently ignores the operator's setting.
func TestABSConfig_EnvBindings(t *testing.T) {
	cases := []struct{ env, key, value string }{
		{"ABS_API_ENABLED", "abs_api_enabled", "true"},
		{"ABS_AUTH_MODES", "abs_auth_modes", "cf"},
		{"ABS_JWT_SECRET", "abs_jwt_secret", "0123456789abcdef0123456789abcdef"},
		{"ABS_ACCESS_TOKEN_TTL", "abs_access_token_ttl", "168h"},
		{"ABS_REFRESH_TOKEN_TTL", "abs_refresh_token_ttl", "2160h"},
		{"ABS_REFRESH_GRACE", "abs_refresh_grace", "5m"},
		{"ABS_SESSION_MAX_TTL", "abs_session_max_ttl", "4320h"},
		{"ABS_SERVER_VERSION", "abs_server_version", "2.36.0"},
		{"ABS_DEFAULT_LIBRARY_ID", "abs_default_library_id", "b5e3a5b2-a76e-471f-b18b-915e4716d053"},
	}
	for _, tc := range cases {
		t.Run(tc.env, func(t *testing.T) {
			resetViper(t)
			registerABSDefaults(viper.GetViper())
			t.Setenv(tc.env, tc.value)
			if got := viper.GetString(tc.key); got != tc.value {
				t.Fatalf("%s=%q did not reach viper key %q (got %q) — check viper.BindEnv", tc.env, tc.value, tc.key, got)
			}
		})
	}
}
