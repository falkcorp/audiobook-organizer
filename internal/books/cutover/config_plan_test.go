// file: internal/books/cutover/config_plan_test.go
// version: 1.0.0
// guid: e5a1c730-8b42-4d19-a6f3-0d97b2c48e15
// last-edited: 2026-11-11

package cutover

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/storage"
)

// Fake credentials of the planner's shape tests: Batman-themed, never real.
const (
	fxUserinfoPass  = "Alfred-userinfo-passphrase-0201"
	fxWebhookOrphan = "Bane-orphan-hmac-0202"
	fxOperatorOnly  = "Ridler-operator-jwt-0203"
	fxOddCred       = "Catwoman-odd-credential-0204"
)

// mutateBlob is a rig option that edits the blob's tree before the cutover.
func mutateBlob(edit func(tree map[string]any)) func(*rig) {
	return func(r *rig) {
		prev := r.mutate
		r.mutate = func(t testing.TB, lr storage.LegacyRange) {
			if prev != nil {
				prev(t, lr)
			}
			rewriteBlob(t, lr, edit)
		}
	}
}

func stagedValue(snap *snapshot, path string) (string, bool) {
	for _, c := range snap.Calls {
		if c.Path == path {
			return c.Value, true
		}
	}
	return "", false
}

// A URL key's userinfo becomes a secret next to the key, and the key keeps
// the URL without it; the log names the key, never the userinfo.
func TestConfigPlan_URLUserinfoMovesToASecret(t *testing.T) {
	row, ok := Table().Row("whisper_remote_url")
	if !ok || !row.URL {
		t.Fatal("whisper_remote_url is not a URL row of the table")
	}
	path := row.Targets[0].Path
	r := newRig(t, "stage1", mutateBlob(func(tree map[string]any) {
		tree["whisper_remote_url"] = "http://batman:" + fxUserinfoPass + "@192.0.2.20:9000/asr"
	}))
	snap, err := r.run(t)
	if err != nil {
		t.Fatalf("M2: %v", err)
	}
	if got, _ := stagedValue(snap, path); strings.TrimSpace(got) != `"http://192.0.2.20:9000/asr"` {
		t.Errorf("%s staged as %s, want the URL without its userinfo", path, got)
	}
	if got := snap.Secrets[path+".userinfo"]; got != "batman:"+fxUserinfoPass {
		t.Errorf("the userinfo secret %s.userinfo does not read back (set %v)", path, got != "")
	}
	if strings.Contains(r.logs.String(), fxUserinfoPass) {
		t.Error("the logs carry the userinfo")
	}
	if !strings.Contains(r.logs.String(), "userinfo moved out of "+path) {
		t.Errorf("the logs do not report the move:\n%s", r.logs.String())
	}
	for k, v := range snap.RawCore {
		if strings.Contains(string(v), fxUserinfoPass) {
			t.Errorf("core row %s carries the userinfo in plaintext", k)
		}
	}
}

// The OIDC fold: one provider entry per client id when sign-in is on, each
// pointing at its imported client secret; with sign-in off no entry exists
// and the client ids are quarantined instead.
func TestConfigPlan_OIDCFold(t *testing.T) {
	setOAuth := func(enabled bool) func(*rig) {
		return mutateBlob(func(tree map[string]any) {
			tree["oauth_enabled"] = enabled
			tree["oauth_github_client_id"] = "robin-github-client"
			tree["oauth_github_client_secret"] = fxGitHub
			tree["oauth_google_client_id"] = "batgirl-google-client"
			tree["oauth_google_client_secret"] = fxGoogleO
			tree["oauth_allowed_emails"] = "bruce@batcave.example, , dick@batcave.example"
			tree["oauth_default_role"] = "user"
		})
	}
	t.Run("enabled", func(t *testing.T) {
		snap, err := newRig(t, "stage1", setOAuth(true)).run(t)
		if err != nil {
			t.Fatalf("M2: %v", err)
		}
		raw, ok := stagedValue(snap, "core.auth.oidc.providers")
		if !ok {
			t.Fatal("core.auth.oidc.providers was not staged")
		}
		var entries []map[string]any
		if err := json.Unmarshal([]byte(raw), &entries); err != nil || len(entries) != 2 {
			t.Fatalf("providers = %s (err %v), want two entries", raw, err)
		}
		gh, goog := entries[0], entries[1]
		if gh["id"] != "github" || gh["kind"] != "github" || gh["client_id"] != "robin-github-client" {
			t.Errorf("github entry = %v", gh)
		}
		if goog["id"] != "google" || goog["kind"] != "oidc" || goog["issuer"] != "https://accounts.google.com" {
			t.Errorf("google entry = %v", goog)
		}
		for _, e := range entries {
			if emails, _ := e["allowed_emails"].([]any); len(emails) != 2 {
				t.Errorf("%v: allowed_emails = %v, want the two non-blank addresses", e["id"], e["allowed_emails"])
			}
			if e["default_role"] != "user" {
				t.Errorf("%v: default_role = %v", e["id"], e["default_role"])
			}
			ref, _ := e["client_secret"].(map[string]any)
			if want := "core.auth.oidc." + e["id"].(string) + ".client_secret"; ref["secret"] != want {
				t.Errorf("%v: client_secret = %v, want a reference to %s", e["id"], e["client_secret"], want)
			}
		}
		if strings.Contains(raw, fxGitHub) || strings.Contains(raw, fxGoogleO) {
			t.Error("a client secret is in the staged providers value")
		}
	})
	t.Run("disabled", func(t *testing.T) {
		r := newRig(t, "stage1", setOAuth(false))
		snap, err := r.run(t)
		if err != nil {
			t.Fatalf("M2: %v", err)
		}
		if _, ok := stagedValue(snap, "core.auth.oidc.providers"); ok {
			t.Error("a disabled sign-in staged provider entries, which would turn SSO on")
		}
		for _, key := range []string{"setting:config_blob#oauth_github_client_id", "setting:config_blob#oauth_google_client_id"} {
			q, ok := quarantineFor(snap, key)
			if !ok || q.Reason != reasonOAuthDisabled || !q.Sealed {
				t.Errorf("%s: quarantined = %v, reason %q, sealed %v", key, ok, q.Reason, q.Sealed)
			}
		}
		checkQuarantined(t, r, snap)
		if got := snap.Secrets["core.auth.oidc.github.client_secret"]; got != fxGitHub {
			t.Error("the client secrets still move when sign-in is off")
		}
	})
}

// What has no place to go is quarantined under its member, sealed, never
// dropped: a webhook secret with no URL, a plugin id that is no name, an
// operator-only secret found in the blob, a credential key that is no name.
func TestConfigPlan_NoPlaceToGoIsQuarantined(t *testing.T) {
	r := newRig(t, "stage1", mutateBlob(func(tree map[string]any) {
		tree["plugins"] = map[string]any{
			"webhook":         map[string]any{"enabled": true, "settings": map[string]any{"events": "all", "urls": "", "secret": fxWebhookOrphan}},
			"Bad Plugin Name": map[string]any{"enabled": true, "settings": map[string]any{"token": fxPlugin}},
		}
		tree["abs_jwt_secret"] = fxOperatorOnly
		if list, ok := tree["metadata_sources"].([]any); ok {
			for _, el := range list {
				if m, ok := el.(map[string]any); ok && m["id"] == "hardcover" {
					m["credentials"] = map[string]any{"Odd Key!": fxOddCred}
				}
			}
		}
	}))
	snap, err := r.run(t)
	if err != nil {
		t.Fatalf("M2: %v", err)
	}
	want := map[string]struct{ reason, value string }{
		"setting:config_blob#plugins.webhook.settings.secret": {reasonWebhookNoURL, fxWebhookOrphan},
		"setting:config_blob#plugins.Bad Plugin Name":         {reasonPluginName, ""},
		"setting:config_blob#abs_jwt_secret":                  {reasonOperatorSecret, fxOperatorOnly},
	}
	for key, w := range want {
		q, ok := quarantineFor(snap, key)
		if !ok {
			t.Errorf("%s was not quarantined: %+v", key, snap.Quarantine)
			continue
		}
		if q.Reason != w.reason || !q.Sealed {
			t.Errorf("%s: reason %q sealed %v, want %q sealed", key, q.Reason, q.Sealed, w.reason)
		}
		if w.value != "" && strings.Trim(q.Value, `"`) != w.value {
			t.Errorf("%s: the value is not recoverable from the quarantine", key)
		}
	}
	odd := false
	for _, q := range snap.Quarantine {
		if q.Reason == reasonSecretName && strings.Trim(q.Value, `"`) == fxOddCred {
			odd = true
		}
	}
	if !odd {
		t.Errorf("the credential under a key that is no secret name was not quarantined: %+v", snap.Quarantine)
	}
	for name, v := range snap.Secrets {
		if strings.HasPrefix(name, "core.storage.quarantine.") {
			continue // the sealed quarantine records hold exactly these values
		}
		if v == fxOddCred || v == fxOperatorOnly || v == fxWebhookOrphan {
			t.Errorf("a value with no place to go was imported as the secret %s", name)
		}
	}
	tree := blobTree(t, snap.Legacy)
	if _, still := tree["abs_jwt_secret"]; still {
		t.Error("the operator-only secret is still in the blob")
	}
	checkQuarantined(t, r, snap)
}
