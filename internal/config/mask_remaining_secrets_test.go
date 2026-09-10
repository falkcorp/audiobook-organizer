// file: internal/config/mask_remaining_secrets_test.go
// version: 1.0.0
// guid: 6d1730c3-5896-4dab-8d11-e541faca253f
// last-edited: 2026-09-10

package config

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// The six secrets GET /api/v1/config used to return in full cleartext.
//
// Every fixture is obviously fake and at least 8 characters, so
// database.MaskSecret keeps a 3-char prefix and a 4-char suffix instead of
// collapsing to "****". That matters: a short fixture masks to "****" for
// EVERY field, and a restore test would then pass while proving nothing about
// which field got which value back. These six produce six distinct sentinels
// ("ghs****0001", "goo****0002", ...), so a restore that puts a neighbour's
// value back fails.
const (
	fakeGithubClientSecret = "ghs-test-secret-github-0001"
	fakeGoogleClientSecret = "goo-test-secret-google-0002"
	fakeDelugeWebPassword  = "dlw-test-secret-delugeweb-0003"
	fakeDelugePassword     = "dlp-test-secret-delugerpc-0004"
	fakeQBittorrentPass    = "qbp-test-secret-qbittorrent-0005"
	fakeSABnzbdAPIKey      = "sab-test-secret-sabnzbd-0006"
)

// configWithRemainingSecrets builds a config carrying all six secrets plus the
// non-secret neighbours that must survive every path under test.
func configWithRemainingSecrets(rootDir string) Config {
	return Config{
		RootDir:                 rootDir,
		OAuthGithubClientSecret: fakeGithubClientSecret,
		OAuthGoogleClientSecret: fakeGoogleClientSecret,
		DelugeWebPassword:       fakeDelugeWebPassword,
		DownloadClient: DownloadClientConfig{
			Torrent: TorrentClientConfig{
				Type: "deluge",
				Deluge: DelugeConfig{
					Host:     "192.0.2.10",
					Port:     58846,
					Username: "deluge-user",
					Password: fakeDelugePassword,
				},
				QBittorrent: QBittorrentConfig{
					Host:     "192.0.2.11",
					Port:     8080,
					Username: "qbit-user",
					Password: fakeQBittorrentPass,
				},
			},
			Usenet: UsenetClientConfig{
				Type: "sabnzbd",
				SABnzbd: SABnzbdConfig{
					Host:   "192.0.2.12",
					Port:   8085,
					APIKey: fakeSABnzbdAPIKey,
				},
			},
		},
	}
}

// remainingSecretFixtures pairs each fixture with a reader for the field it
// belongs in, so every assertion covers all six rather than a representative.
var remainingSecretFixtures = []struct {
	name  string
	raw   string
	getFn func(Config) string
}{
	{"oauth_github_client_secret", fakeGithubClientSecret, func(c Config) string { return c.OAuthGithubClientSecret }},
	{"oauth_google_client_secret", fakeGoogleClientSecret, func(c Config) string { return c.OAuthGoogleClientSecret }},
	{"deluge_web_password", fakeDelugeWebPassword, func(c Config) string { return c.DelugeWebPassword }},
	{"download_client.torrent.deluge.password", fakeDelugePassword, func(c Config) string {
		return c.DownloadClient.Torrent.Deluge.Password
	}},
	{"download_client.torrent.qbittorrent.password", fakeQBittorrentPass, func(c Config) string {
		return c.DownloadClient.Torrent.QBittorrent.Password
	}},
	{"download_client.usenet.sabnzbd.api_key", fakeSABnzbdAPIKey, func(c Config) string {
		return c.DownloadClient.Usenet.SABnzbd.APIKey
	}},
}

// TestMaskSecrets_MasksRemainingSecretFields is the GET-shaped regression test.
//
// MaskSecrets covered five scalar fields plus metadata_sources credentials.
// These six were returned in full cleartext to every caller of
// GET /api/v1/config: two OAuth client secrets, the Deluge web password, and
// all three download-client credentials. Pre-fix this fails on all six.
func TestMaskSecrets_MasksRemainingSecretFields(t *testing.T) {
	us := &UpdateService{}
	cfg := configWithRemainingSecrets(t.TempDir())

	masked := us.MaskSecrets(cfg)

	for _, f := range remainingSecretFixtures {
		got := f.getFn(masked)
		if got == f.raw {
			t.Errorf("%s returned in cleartext: %q", f.name, got)
			continue
		}
		if !strings.Contains(got, "****") {
			t.Errorf("%s not masked, got %q", f.name, got)
		}
	}

	// The whole serialized response must not carry any of them anywhere --
	// masking the struct field while some other tag re-exports the value is the
	// same bug one level down.
	blob, err := json.Marshal(masked)
	if err != nil {
		t.Fatalf("marshal masked config: %v", err)
	}
	for _, f := range remainingSecretFixtures {
		if strings.Contains(string(blob), f.raw) {
			t.Errorf("serialized config still contains the cleartext %s", f.name)
		}
	}

	// Non-secret neighbours must survive masking, or the UI loses the
	// connection settings it needs to render alongside the hidden password.
	if masked.DownloadClient.Torrent.Deluge.Username != "deluge-user" ||
		masked.DownloadClient.Torrent.Deluge.Host != "192.0.2.10" ||
		masked.DownloadClient.Usenet.SABnzbd.Port != 8085 {
		t.Errorf("non-secret download-client fields lost in masking: %#v", masked.DownloadClient)
	}
}

// TestMaskSecrets_DoesNotMutateRemainingSecretSource guards the shallow-copy
// footgun that bit metadata_sources: MaskSecrets does `masked := cfg`. The six
// fields here all live in value structs so the copy is genuinely deep, but a
// future refactor that moves any of them behind a pointer or map would silently
// start overwriting the live AppConfig's real credentials with the mask.
func TestMaskSecrets_DoesNotMutateRemainingSecretSource(t *testing.T) {
	us := &UpdateService{}
	cfg := configWithRemainingSecrets(t.TempDir())

	_ = us.MaskSecrets(cfg)
	_ = us.MaskSecrets(cfg)

	for _, f := range remainingSecretFixtures {
		if got := f.getFn(cfg); got != f.raw {
			t.Errorf("MaskSecrets mutated the caller's %s: got %q, want the original", f.name, got)
		}
	}
}

// --- round-trip protection -------------------------------------------------
//
// Masking the GET response is only safe if a PUT echoing the mask back does not
// overwrite the stored secret. The pre-existing five scalars are protected by
// secretFieldKeys (removed from the payload) plus acceptSecretUpdate;
// metadata_sources is protected by restoreMaskedCredentials. None of those
// reach these six, and json.Unmarshal writes whatever the payload carried
// straight onto the candidate -- so masking alone would convert a disclosure
// bug into permanent credential loss.
//
// These assert on the live AppConfig, never on UpdateConfig's response.
// MaskSecret is idempotent, so the masked response is byte-identical whether
// the secret survived or was destroyed; a test asserting on resp["config"]
// passes in both worlds.

// maskedRoundTripPayload is what a client sends after GET handed it the masks:
// the full download_client object (a client that sends the object sends all of
// it) plus the three top-level scalars.
func maskedRoundTripPayload() map[string]any {
	return map[string]any{
		"oauth_github_client_secret": database.MaskSecret(fakeGithubClientSecret),
		"oauth_google_client_secret": database.MaskSecret(fakeGoogleClientSecret),
		"deluge_web_password":        database.MaskSecret(fakeDelugeWebPassword),
		"download_client": map[string]any{
			"torrent": map[string]any{
				"type": "deluge",
				"deluge": map[string]any{
					"host":     "192.0.2.10",
					"port":     58846,
					"username": "deluge-user",
					"password": database.MaskSecret(fakeDelugePassword),
				},
				"qbittorrent": map[string]any{
					"host":     "192.0.2.11",
					"port":     8080,
					"username": "qbit-user",
					"password": database.MaskSecret(fakeQBittorrentPass),
				},
			},
			"usenet": map[string]any{
				"type": "sabnzbd",
				"sabnzbd": map[string]any{
					"host":    "192.0.2.12",
					"port":    8085,
					"api_key": database.MaskSecret(fakeSABnzbdAPIKey),
				},
			},
		},
	}
}

// TestUpdateConfig_EchoedMaskDoesNotDestroyRemainingSecrets is the destructive
// half of the regression: save the settings page unchanged and every one of the
// six secrets is replaced by its own mask. Nothing surfaces until the OAuth
// login or the download client starts returning 401.
func TestUpdateConfig_EchoedMaskDoesNotDestroyRemainingSecrets(t *testing.T) {
	withAppConfig(t, configWithRemainingSecrets(t.TempDir()))
	us := NewUpdateService(newMockSettingsStore())

	payload := maskedRoundTripPayload()
	// Guard against a fixture change that would stop exercising the bug.
	for _, f := range remainingSecretFixtures {
		if strings.Contains(mustJSON(t, payload), f.raw) {
			t.Fatalf("test is not exercising the bug: payload carries cleartext %s", f.name)
		}
	}

	if status, resp := us.UpdateConfig(context.Background(), payload); status != http.StatusOK {
		t.Fatalf("UpdateConfig returned %d: %v", status, resp)
	}

	stored := Snapshot()
	for _, f := range remainingSecretFixtures {
		if got := f.getFn(stored); got != f.raw {
			t.Errorf("settings save destroyed %s: got %q, want the original", f.name, got)
		}
	}
}

// TestUpdateConfig_NewRemainingSecretsAreWrittenThrough is the mutation guard.
// A restore that ignores the mask comparison and puts the prior value back
// unconditionally passes both the masking test and the echoed-mask test above
// while making all six secrets permanently unchangeable.
func TestUpdateConfig_NewRemainingSecretsAreWrittenThrough(t *testing.T) {
	const (
		rotatedGithub = "ghs-test-rotated-github-1001"
		rotatedGoogle = "goo-test-rotated-google-1002"
		rotatedWeb    = "dlw-test-rotated-delugeweb-1003"
		rotatedDeluge = "dlp-test-rotated-delugerpc-1004"
		rotatedQBit   = "qbp-test-rotated-qbittorrent-1005"
		rotatedSAB    = "sab-test-rotated-sabnzbd-1006"
	)
	withAppConfig(t, configWithRemainingSecrets(t.TempDir()))
	us := NewUpdateService(newMockSettingsStore())

	payload := map[string]any{
		"oauth_github_client_secret": rotatedGithub,
		"oauth_google_client_secret": rotatedGoogle,
		"deluge_web_password":        rotatedWeb,
		"download_client": map[string]any{
			"torrent": map[string]any{
				"deluge":      map[string]any{"password": rotatedDeluge},
				"qbittorrent": map[string]any{"password": rotatedQBit},
			},
			"usenet": map[string]any{
				"sabnzbd": map[string]any{"api_key": rotatedSAB},
			},
		},
	}
	if status, resp := us.UpdateConfig(context.Background(), payload); status != http.StatusOK {
		t.Fatalf("UpdateConfig returned %d: %v", status, resp)
	}

	stored := Snapshot()
	for _, tc := range []struct {
		name string
		got  string
		want string
	}{
		{"oauth_github_client_secret", stored.OAuthGithubClientSecret, rotatedGithub},
		{"oauth_google_client_secret", stored.OAuthGoogleClientSecret, rotatedGoogle},
		{"deluge_web_password", stored.DelugeWebPassword, rotatedWeb},
		{"deluge.password", stored.DownloadClient.Torrent.Deluge.Password, rotatedDeluge},
		{"qbittorrent.password", stored.DownloadClient.Torrent.QBittorrent.Password, rotatedQBit},
		{"sabnzbd.api_key", stored.DownloadClient.Usenet.SABnzbd.APIKey, rotatedSAB},
	} {
		if tc.got != tc.want {
			t.Errorf("rotation blocked for %s: got %q, want %q", tc.name, tc.got, tc.want)
		}
	}
}

// TestUpdateConfig_EmptyNestedDownloadClientSecretDoesNotWipe pins the
// deliberate asymmetry between the two halves of the fix.
//
// The three download-client credentials are in restoreMaskedCredentials'
// situation, not acceptSecretUpdate's: a client that sends the deluge object at
// all sends the whole object, so a blank password box is indistinguishable
// after unmarshal from "field omitted". Treating that as an explicit clear
// would destroy the credential on any partial PUT. They restore on empty.
func TestUpdateConfig_EmptyNestedDownloadClientSecretDoesNotWipe(t *testing.T) {
	withAppConfig(t, configWithRemainingSecrets(t.TempDir()))
	us := NewUpdateService(newMockSettingsStore())

	payload := map[string]any{
		"download_client": map[string]any{
			"torrent": map[string]any{
				"deluge":      map[string]any{"host": "192.0.2.20", "password": ""},
				"qbittorrent": map[string]any{"password": ""},
			},
			"usenet": map[string]any{
				"sabnzbd": map[string]any{"api_key": ""},
			},
		},
	}
	if status, resp := us.UpdateConfig(context.Background(), payload); status != http.StatusOK {
		t.Fatalf("UpdateConfig returned %d: %v", status, resp)
	}

	stored := Snapshot()
	if got := stored.DownloadClient.Torrent.Deluge.Password; got != fakeDelugePassword {
		t.Errorf("empty value wiped deluge.password: got %q", got)
	}
	if got := stored.DownloadClient.Torrent.QBittorrent.Password; got != fakeQBittorrentPass {
		t.Errorf("empty value wiped qbittorrent.password: got %q", got)
	}
	if got := stored.DownloadClient.Usenet.SABnzbd.APIKey; got != fakeSABnzbdAPIKey {
		t.Errorf("empty value wiped sabnzbd.api_key: got %q", got)
	}
	// The non-secret field in the same object must still have been applied --
	// restoring the secret must not restore its neighbours.
	if got := stored.DownloadClient.Torrent.Deluge.Host; got != "192.0.2.20" {
		t.Errorf("non-secret deluge.host was not applied: got %q, want 192.0.2.20", got)
	}
}

// TestUpdateConfig_EmptyScalarClearsRemainingScalarSecrets pins the other half:
// the three top-level scalars keep acceptSecretUpdate's semantics, matching the
// five secrets already masked. The payload key is genuinely present-or-absent
// there, so an explicit "" is unambiguous and is the only way to unset one.
func TestUpdateConfig_EmptyScalarClearsRemainingScalarSecrets(t *testing.T) {
	withAppConfig(t, configWithRemainingSecrets(t.TempDir()))
	us := NewUpdateService(newMockSettingsStore())

	payload := map[string]any{
		"oauth_github_client_secret": "",
		"oauth_google_client_secret": "",
		"deluge_web_password":        "",
	}
	if status, resp := us.UpdateConfig(context.Background(), payload); status != http.StatusOK {
		t.Fatalf("UpdateConfig returned %d: %v", status, resp)
	}

	stored := Snapshot()
	if stored.OAuthGithubClientSecret != "" || stored.OAuthGoogleClientSecret != "" || stored.DelugeWebPassword != "" {
		t.Errorf("empty value no longer clears the scalar secrets: %q %q %q",
			stored.OAuthGithubClientSecret, stored.OAuthGoogleClientSecret, stored.DelugeWebPassword)
	}
}

// TestUpdateConfig_UnrelatedPutLeavesRemainingSecretsAlone covers the most
// common PUT of all: a payload that never mentions any of the six. Absent keys
// must leave the stored secrets exactly as they were.
func TestUpdateConfig_UnrelatedPutLeavesRemainingSecretsAlone(t *testing.T) {
	withAppConfig(t, configWithRemainingSecrets(t.TempDir()))
	us := NewUpdateService(newMockSettingsStore())

	if status, resp := us.UpdateConfig(context.Background(), map[string]any{"root_dir": t.TempDir()}); status != http.StatusOK {
		t.Fatalf("UpdateConfig returned %d: %v", status, resp)
	}

	stored := Snapshot()
	for _, f := range remainingSecretFixtures {
		if got := f.getFn(stored); got != f.raw {
			t.Errorf("unrelated PUT changed %s: got %q, want the original", f.name, got)
		}
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}
