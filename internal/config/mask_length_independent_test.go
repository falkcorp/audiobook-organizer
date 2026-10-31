// file: internal/config/mask_length_independent_test.go
// version: 1.1.0
// guid: 3f0c9a52-7d1e-4b8a-9c64-5a2e81d7b0f3
// last-edited: 2026-10-30

package config

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// maskedFieldCase is one secret-bearing field that GET /api/v1/config masks and
// PUT /api/v1/config must therefore tolerate being echoed back.
type maskedFieldCase struct {
	name    string
	seed    func(c *Config, v string)
	get     func(c Config) string
	payload func(v string) map[string]any
}

func maskedFieldCases() []maskedFieldCase {
	scalar := func(key string, seed func(*Config, string), get func(Config) string) maskedFieldCase {
		return maskedFieldCase{
			name: key, seed: seed, get: get,
			payload: func(v string) map[string]any { return map[string]any{key: v} },
		}
	}
	return []maskedFieldCase{
		scalar("openai_api_key", func(c *Config, v string) { c.OpenAIAPIKey = v }, func(c Config) string { return c.OpenAIAPIKey }),
		scalar("acoustid_api_key", func(c *Config, v string) { c.AcoustIDAPIKey = v }, func(c Config) string { return c.AcoustIDAPIKey }),
		scalar("google_books_api_key", func(c *Config, v string) { c.GoogleBooksAPIKey = v }, func(c Config) string { return c.GoogleBooksAPIKey }),
		scalar("hardcover_api_token", func(c *Config, v string) { c.HardcoverAPIToken = v }, func(c Config) string { return c.HardcoverAPIToken }),
		scalar("basic_auth_password", func(c *Config, v string) { c.BasicAuthPassword = v }, func(c Config) string { return c.BasicAuthPassword }),
		scalar("oauth_github_client_secret", func(c *Config, v string) { c.OAuthGithubClientSecret = v }, func(c Config) string { return c.OAuthGithubClientSecret }),
		scalar("oauth_google_client_secret", func(c *Config, v string) { c.OAuthGoogleClientSecret = v }, func(c Config) string { return c.OAuthGoogleClientSecret }),
		scalar("deluge_web_password", func(c *Config, v string) { c.DelugeWebPassword = v }, func(c Config) string { return c.DelugeWebPassword }),
		{
			name: "download_client.torrent.deluge.password",
			seed: func(c *Config, v string) { c.DownloadClient.Torrent.Deluge.Password = v },
			get:  func(c Config) string { return c.DownloadClient.Torrent.Deluge.Password },
			payload: func(v string) map[string]any {
				return map[string]any{"download_client": map[string]any{"torrent": map[string]any{
					"deluge": map[string]any{"password": v},
				}}}
			},
		},
	}
}

// TestUpdateConfig_MaskedEchoKeepsSecretAcrossLengths drives the length-
// independent mask through every masked field: an 8-character password (the
// case the old 3+4 mask exposed 7/8 of), a 15-character one (still fully
// hidden), a 16-character one (last two characters shown) and a long one. In
// each case the client echoes exactly what GET handed it and the stored secret
// must survive; a different value must replace it.
func TestUpdateConfig_MaskedEchoKeepsSecretAcrossLengths(t *testing.T) {
	secrets := map[string]string{
		"len8":  "Alfred88",
		"len15": "WayneManorGate1",
		"len16": "WayneManorGate16",
		"len40": "ArkhamAsylumJokerHarleyQuinnPenguinRiddl",
	}
	for _, fc := range maskedFieldCases() {
		for label, secret := range secrets {
			t.Run(fc.name+"/"+label+"/echo", func(t *testing.T) {
				var base Config
				base.RootDir = t.TempDir()
				fc.seed(&base, secret)
				withAppConfig(t, base)
				us := NewUpdateService(newMockSettingsStore())

				masked := us.MaskSecrets(Snapshot())
				echoed := fc.get(masked)
				if echoed != database.MaskSecret(secret) {
					t.Fatalf("GET returned %q, want MaskSecret(secret)=%q", echoed, database.MaskSecret(secret))
				}
				if label == "len8" && echoed != "********" {
					t.Fatalf("8-character secret masked to %q, want exactly eight asterisks", echoed)
				}
				if echoed == secret {
					t.Fatalf("GET returned the cleartext secret")
				}

				if status, resp := us.UpdateConfig(context.Background(), fc.payload(echoed)); status != http.StatusOK {
					t.Fatalf("UpdateConfig returned %d: %v", status, resp)
				}
				if got := fc.get(Snapshot()); got != secret {
					t.Fatalf("echoed mask %q destroyed the secret: got %q, want the original", echoed, got)
				}
			})
			t.Run(fc.name+"/"+label+"/replace", func(t *testing.T) {
				var base Config
				base.RootDir = t.TempDir()
				fc.seed(&base, secret)
				withAppConfig(t, base)
				us := NewUpdateService(newMockSettingsStore())

				const rotated = "GothamRotatedSecret-9001"
				if status, resp := us.UpdateConfig(context.Background(), fc.payload(rotated)); status != http.StatusOK {
					t.Fatalf("UpdateConfig returned %d: %v", status, resp)
				}
				if got := fc.get(Snapshot()); got != rotated {
					t.Fatalf("changed value was not written: got %q, want %q", got, rotated)
				}
			})
		}
	}
}

// TestUpdateConfig_LiteralMaskStringSecretIsKept pins the existing treatment of
// a stored secret that happens to equal the mask: MaskSecret of it is the same
// string, so an echo of it is "unchanged" and the value is kept (not an error,
// not blanked).
func TestUpdateConfig_LiteralMaskStringSecretIsKept(t *testing.T) {
	const literal = "********"
	if database.MaskSecret(literal) != literal {
		t.Fatalf("precondition: MaskSecret(%q) = %q", literal, database.MaskSecret(literal))
	}
	for _, fc := range maskedFieldCases() {
		t.Run(fc.name, func(t *testing.T) {
			var base Config
			base.RootDir = t.TempDir()
			fc.seed(&base, literal)
			withAppConfig(t, base)
			us := NewUpdateService(newMockSettingsStore())

			if status, resp := us.UpdateConfig(context.Background(), fc.payload(literal)); status != http.StatusOK {
				t.Fatalf("UpdateConfig returned %d: %v", status, resp)
			}
			if got := fc.get(Snapshot()); got != literal {
				t.Fatalf("literal-mask secret changed: got %q", got)
			}
		})
	}
}

// TestUpdateConfig_ShortSecretsDoNotCrossRestore guards the weakness a
// length-independent mask introduces: every secret under 16 characters masks to
// the same "********", so a restore must come from the field's own prior value.
func TestUpdateConfig_ShortSecretsDoNotCrossRestore(t *testing.T) {
	withAppConfig(t, Config{
		RootDir:                 t.TempDir(),
		OAuthGithubClientSecret: "robin-gh-01",
		OAuthGoogleClientSecret: "joker-go-02",
		DelugeWebPassword:       "batman-dw-03",
	})
	us := NewUpdateService(newMockSettingsStore())

	payload := map[string]any{
		"oauth_github_client_secret": "********",
		"oauth_google_client_secret": "********",
		"deluge_web_password":        "********",
	}
	if status, resp := us.UpdateConfig(context.Background(), payload); status != http.StatusOK {
		t.Fatalf("UpdateConfig returned %d: %v", status, resp)
	}
	got := Snapshot()
	if got.OAuthGithubClientSecret != "robin-gh-01" || got.OAuthGoogleClientSecret != "joker-go-02" || got.DelugeWebPassword != "batman-dw-03" {
		t.Fatalf("secrets crossed or were destroyed: %q %q %q", got.OAuthGithubClientSecret, got.OAuthGoogleClientSecret, got.DelugeWebPassword)
	}
}

// legacyMasks are the shapes older builds handed out. They must never be
// accepted as an echo: a stale tab or exported config holding one would
// otherwise be stored as the literal secret.
func legacyMaskFor(secret string) string {
	if len(secret) < 8 {
		return "****"
	}
	return secret[:3] + "****" + secret[len(secret)-4:]
}

// TestUpdateConfig_LegacyMaskEchoIsRejected: a legacy-shaped mask is refused
// with a 400 telling the operator to re-enter the secret, and nothing stored
// changes. The new-shape echo keeps the secret, and a real value replaces it.
func TestUpdateConfig_LegacyMaskEchoIsRejected(t *testing.T) {
	const secret = "WayneManorGate-Alfred-0042"
	for _, fc := range maskedFieldCases() {
		t.Run(fc.name+"/legacy", func(t *testing.T) {
			var base Config
			base.RootDir = t.TempDir()
			fc.seed(&base, secret)
			withAppConfig(t, base)
			us := NewUpdateService(newMockSettingsStore())

			status, resp := us.UpdateConfig(context.Background(), fc.payload(legacyMaskFor(secret)))
			if status != http.StatusBadRequest {
				t.Fatalf("legacy mask: status %d, want 400: %v", status, resp)
			}
			if msg, _ := resp["error"].(string); !strings.Contains(msg, "re-enter the secret") {
				t.Fatalf("error %q does not tell the operator to re-enter the secret", msg)
			}
			if got := fc.get(Snapshot()); got != secret {
				t.Fatalf("rejected legacy mask changed the stored secret: %q", got)
			}
		})
		t.Run(fc.name+"/short-legacy", func(t *testing.T) {
			var base Config
			base.RootDir = t.TempDir()
			fc.seed(&base, secret)
			withAppConfig(t, base)
			us := NewUpdateService(newMockSettingsStore())
			if status, _ := us.UpdateConfig(context.Background(), fc.payload("****")); status != http.StatusBadRequest {
				t.Fatalf("\"****\": status %d, want 400", status)
			}
			if got := fc.get(Snapshot()); got != secret {
				t.Fatalf("stored secret changed: %q", got)
			}
		})
	}
}

func TestUpdateConfig_LegacyMaskCredentialInMetadataSourceIsRejected(t *testing.T) {
	const secret = "GothamBooksKey-Batgirl-77"
	withAppConfig(t, Config{RootDir: t.TempDir(), MetadataSources: sourcesWithKey(secret)})
	us := NewUpdateService(newMockSettingsStore())

	payload := map[string]any{"metadata_sources": []any{map[string]any{
		"id": "google-books", "credentials": map[string]any{"apiKey": legacyMaskFor(secret)},
	}}}
	if status, resp := us.UpdateConfig(context.Background(), payload); status != http.StatusBadRequest {
		t.Fatalf("status %d, want 400: %v", status, resp)
	}
	if got := Snapshot().MetadataSources[0].Credentials["apiKey"]; got != secret {
		t.Fatalf("stored credential changed: %q", got)
	}

	payload["metadata_sources"].([]any)[0].(map[string]any)["credentials"] = map[string]any{"apiKey": database.MaskSecret(secret)}
	if status, resp := us.UpdateConfig(context.Background(), payload); status != http.StatusOK {
		t.Fatalf("current-shape echo: status %d: %v", status, resp)
	}
	if got := Snapshot().MetadataSources[0].Credentials["apiKey"]; got != secret {
		t.Fatalf("current-shape echo destroyed the credential: %q", got)
	}
}

func TestLooksLikeMaskAndIsEchoedMask(t *testing.T) {
	for in, want := range map[string]bool{"": false, "***": false, "****": true, "ab****cd": true, "********ab": true, "plain": false} {
		if got := database.LooksLikeMask(in); got != want {
			t.Errorf("LooksLikeMask(%q) = %v, want %v", in, got, want)
		}
	}
	if database.IsEchoedMask("********", "") {
		t.Error("an empty stored secret has no mask to echo")
	}
	if !database.IsEchoedMask("********", "short") || database.IsEchoedMask("****", "short") {
		t.Error("IsEchoedMask must accept only the current shape")
	}
}
