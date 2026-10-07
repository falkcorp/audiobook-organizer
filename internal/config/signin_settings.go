// file: internal/config/signin_settings.go
// version: 1.0.0
// guid: b1e50a18-62a6-413d-ab8c-90d9d5bd6e06
// last-edited: 2026-10-07

package config

import (
	"encoding/json"
	"reflect"
	"sort"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// signInSettingKeys are the top-level config keys that decide who can sign in
// and with what. Changing them is a credential change: with
// oauth_allowed_emails and oauth_default_role alone, a caller can let an
// email it controls sign in through SSO as a new admin. PUT /config refuses a
// change to any of them from an API key (handlers/system UpdateConfig).
var signInSettingKeys = []string{
	"enable_auth",
	"basic_auth_enabled",
	"basic_auth_username",
	"basic_auth_password",
	"oauth_enabled",
	"oauth_github_client_id",
	"oauth_github_client_secret",
	"oauth_google_client_id",
	"oauth_google_client_secret",
	"oauth_redirect_base_url",
	"oauth_allowed_emails",
	"oauth_default_role",
	"cf_access_team_domain",
	"cf_access_aud",
	"abs_api_enabled",
	"abs_auth_modes",
	"abs_access_token_ttl",
	"abs_refresh_token_ttl",
	"abs_refresh_grace",
	"write_startup_readonly_key",
}

// signInSecretKeys are masked by GET /config, so a round-tripped mask (or an
// empty value, which keeps the stored secret) is not a change.
var signInSecretKeys = map[string]bool{
	"basic_auth_password":        true,
	"oauth_github_client_secret": true,
	"oauth_google_client_secret": true,
}

// ChangedSignInSettingKeys returns, sorted, the sign-in settings that payload
// (a PUT /config body) would change relative to cur. A key whose value equals
// the current one is not a change, so a GET-then-PUT round trip of the whole
// config (settings export/import) reports nothing.
func ChangedSignInSettingKeys(cur *Config, payload map[string]any) []string {
	if cur == nil || len(payload) == 0 {
		return nil
	}
	var current map[string]any
	if raw, err := json.Marshal(cur); err == nil {
		_ = json.Unmarshal(raw, &current)
	}
	var changed []string
	for _, key := range signInSettingKeys {
		incoming, present := payload[key]
		if !present {
			continue
		}
		existing := current[key]
		if signInSecretKeys[key] {
			in, _ := incoming.(string)
			was, _ := existing.(string)
			if in == "" || in == was || (was != "" && in == database.MaskSecret(was)) {
				continue
			}
			changed = append(changed, key)
			continue
		}
		if !reflect.DeepEqual(normalizeJSONValue(incoming), existing) {
			changed = append(changed, key)
		}
	}
	sort.Strings(changed)
	return changed
}

// normalizeJSONValue round-trips v through JSON so a Go value built in code
// (an int, a typed string) compares equal to the decoded current config.
func normalizeJSONValue(v any) any {
	raw, err := json.Marshal(v)
	if err != nil {
		return v
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		return v
	}
	return out
}
