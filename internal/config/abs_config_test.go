// file: internal/config/abs_config_test.go
// version: 1.2.1
// guid: 71c4e830-6b29-4d5f-8a03-92e7b1c6df45
// last-edited: 2026-10-30

package config

import "testing"

// TestABSConfig_DefaultsAreOffAndSafe pins the feature flag: with nothing set the ABS
// API is DISABLED, so an existing deployment that pulls this change registers no ABS
// route and behaves exactly as before.
func TestABSConfig_DefaultsAreOffAndSafe(t *testing.T) {
	d := factoryDefaultConfigForTest()
	if d.ABSAPIEnabled {
		t.Fatal("ABS_API_ENABLED must default to false")
	}
	if got := d.ABSAuthModes; got != "cf,jwt" {
		t.Fatalf("abs_auth_modes must default to cf,jwt, got %q", got)
	}
	if got := d.ABSJWTSecret; got != "" {
		t.Fatalf("abs_jwt_secret must default to empty (never auto-generated), got %q", got)
	}

	// §1.6 item 1: 30 DAYS, not 1h.
	if got := d.ABSAccessTokenTTL; got != "720h" {
		t.Fatalf("abs_access_token_ttl must default to 720h (30d), got %q", got)
	}
	// The session idle TTL must outlive the access token (2026-10-19 forced-logout
	// fix): 60d against the 30d access token.
	if got := d.ABSRefreshTokenTTL; got != "1440h" {
		t.Fatalf("abs_refresh_token_ttl must default to 1440h (60d), got %q", got)
	}
	if got := d.ABSSessionMaxTTL; got != "0" {
		t.Fatalf("abs_session_max_ttl must default to 0 (no absolute cap), got %q", got)
	}
	if got := d.ABSRefreshGrace; got != "10m" {
		t.Fatalf("abs_refresh_grace must default to 10m, got %q", got)
	}
}
