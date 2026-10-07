// file: internal/config/signin_settings_test.go
// version: 1.0.0
// guid: 2d7b4e91-6c3a-4f18-9e52-a1b8c0d3f467
// last-edited: 2026-10-07

package config

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

func signInFixture() Config {
	return Config{
		EnableAuth:         true,
		OAuthAllowedEmails: "owner@example.com",
		OAuthDefaultRole:   "viewer",
		BasicAuthPassword:  "synthetic-basic-pass",
		CFAccessAUD:        "aud-synthetic",
	}
}

func TestChangedSignInSettingKeys(t *testing.T) {
	cur := signInFixture()
	cases := []struct {
		name    string
		payload map[string]any
		want    []string
	}{
		{"unrelated keys", map[string]any{"concurrent_scans": 4.0}, nil},
		{"same values", map[string]any{"enable_auth": true, "oauth_default_role": "viewer", "oauth_allowed_emails": "owner@example.com"}, nil},
		{"allowlist + admin default role", map[string]any{"oauth_allowed_emails": "owner@example.com,x@example.org", "oauth_default_role": "admin"}, []string{"oauth_allowed_emails", "oauth_default_role"}},
		{"turning auth off", map[string]any{"enable_auth": false}, []string{"enable_auth"}},
		{"masked secret round trip", map[string]any{"basic_auth_password": database.MaskSecret("synthetic-basic-pass")}, nil},
		{"empty secret keeps the stored one", map[string]any{"basic_auth_password": ""}, nil},
		{"new secret", map[string]any{"basic_auth_password": "another-synthetic"}, []string{"basic_auth_password"}},
		{"cf access aud", map[string]any{"cf_access_aud": "other"}, []string{"cf_access_aud"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ChangedSignInSettingKeys(&cur, tc.payload)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// A whole-config GET-then-PUT round trip (what the settings export/import
// does) changes nothing and must not be refused.
func TestChangedSignInSettingKeys_FullRoundTrip(t *testing.T) {
	cur := signInFixture()
	masked := cur
	masked.BasicAuthPassword = database.MaskSecret(cur.BasicAuthPassword)
	raw, err := json.Marshal(masked)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if got := ChangedSignInSettingKeys(&cur, payload); len(got) != 0 {
		t.Errorf("round trip reported changes: %v", got)
	}
}

// Every key in the list must be a real top-level Config key, or the guard
// silently never fires for it.
func TestSignInSettingKeysExistOnConfig(t *testing.T) {
	for _, k := range signInSettingKeys {
		if unknown := unknownConfigKeys(map[string]any{k: nil}); len(unknown) > 0 {
			t.Errorf("sign-in setting %q is not a Config key", k)
		}
	}
}
