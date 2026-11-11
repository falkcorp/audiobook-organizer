// file: internal/books/cutover/fixtures_test.go
// version: 1.0.0
// guid: 91d4a0c2-6a5e-4b1f-8c47-2f6e3a7b8d07
// last-edited: 2026-11-11

package cutover

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/falkcorp/mediacore/storage"
)

// The known-secrets fixture: every credential M2 moves, with a value the test
// knows, written the way HEAD writes it (the four rows sealed with the state
// directory's key, the rest as plaintext in the settings blob), and the
// canonical secret name each lands under. Batman-themed, never a real value.
const (
	fxGate     = "Alfred-gate-passphrase-0001"
	fxOpenAI   = "Oracle-uplink-token-0002"
	fxGoogle   = "Batmobile-atlas-token-0003"
	fxHardcov  = "Hardcover-comics-token-0004"
	fxAcoustid = "Joker-radio-token-0005"
	fxDelugeW  = "Penguin-web-passphrase-0006"
	fxDelugeD  = "Penguin-daemon-passphrase-0007"
	fxGitHub   = "Robin-github-client-secret-0008"
	fxGoogleO  = "Batgirl-google-client-secret-0009"
	fxWebhook  = "Commissioner-signal-hmac-0010"
	fxSource   = "Riddler-source-credential-0011"
	fxPlugin   = "Scarecrow-plugin-token-0012"
)

// knownRows are the four encrypted rows' plaintexts.
func knownRows() map[string]string {
	return map[string]string{
		"basic_auth_password":  fxGate,
		"openai_api_key":       fxOpenAI,
		"google_books_api_key": fxGoogle,
		"hardcover_api_token":  fxHardcov,
	}
}

// knownWant is the canonical secret name of every fixture credential and its value.
func knownWant() map[string]string {
	return map[string]string{
		"core.http.basic_gate.password":                       fxGate,
		"integrations.ai.openai.api_key":                      fxOpenAI,
		"integrations.metadata.google-books.api_key":          fxGoogle,
		"integrations.metadata.hardcover.api_token":           fxHardcov,
		"modules.books.acoustid.api_key":                      fxAcoustid,
		"integrations.download.deluge.password":               fxDelugeW,
		"integrations.download.deluge.daemon_password":        fxDelugeD,
		"core.auth.oidc.github.client_secret":                 fxGitHub,
		"core.auth.oidc.google.client_secret":                 fxGoogleO,
		"integrations.notify.webhook-1.secret":                fxWebhook,
		"integrations.notify.webhook-2.secret":                fxWebhook,
		"integrations.metadata.hardcover.credentials.api_key": fxSource,
		"modules.books.plugins.batsignal.settings":            `{"token":"` + fxPlugin + `"}`,
	}
}

// allFixtureValues is every plaintext the fixture holds, for leak scans.
func allFixtureValues() []string {
	var out []string
	for _, v := range knownWant() {
		out = append(out, v)
	}
	out = append(out, fxPlugin)
	return out
}

// seedKnown makes r's seed hold the known-secrets fixture: the four rows
// sealed with r's state-directory key, and the blob's plaintext credentials.
func seedKnown(r *rig) {
	prev := r.mutate
	r.mutate = func(t testing.TB, lr storage.LegacyRange) {
		if prev != nil {
			prev(t, lr)
		}
		ctx := context.Background()
		for name, plain := range knownRows() {
			if err := lr.Set(ctx, settingKey(name), encryptedRow(t, r.stateDir, name, plain)); err != nil {
				t.Fatal(err)
			}
		}
		rewriteBlob(t, lr, func(tree map[string]any) {
			tree["acoustid_api_key"] = fxAcoustid
			tree["deluge_web_password"] = fxDelugeW
			setAt(tree, "download_client.torrent.deluge.password", fxDelugeD)
			tree["oauth_enabled"] = true
			tree["oauth_github_client_id"] = "robin-github-client"
			tree["oauth_github_client_secret"] = fxGitHub
			tree["oauth_google_client_id"] = "batgirl-google-client"
			tree["oauth_google_client_secret"] = fxGoogleO
			tree["plugins"] = map[string]any{
				"webhook": map[string]any{"enabled": true, "settings": map[string]any{
					"events": "all", "urls": "http://192.0.2.10/gotham, http://192.0.2.11/bludhaven", "secret": fxWebhook}},
				"batsignal": map[string]any{"enabled": true, "settings": map[string]any{"token": fxPlugin}},
			}
			if list, ok := tree["metadata_sources"].([]any); ok {
				for _, el := range list {
					if m, ok := el.(map[string]any); ok && m["id"] == "hardcover" {
						m["credentials"] = map[string]any{"apiKey": fxSource}
					}
				}
			}
		})
	}
}

// blobTree reads the settings blob's JSON members from a legacy dump.
func blobTree(t testing.TB, legacy map[string][]byte) map[string]any {
	t.Helper()
	raw, ok := legacy[string(settingKey(blobRowName))]
	if !ok {
		t.Fatal("the settings blob row is absent")
	}
	row, err := decodeSettingRow(raw)
	if err != nil {
		t.Fatal(err)
	}
	var tree map[string]any
	if err := json.Unmarshal([]byte(row.Value), &tree); err != nil {
		t.Fatal(err)
	}
	return tree
}
