// file: internal/config/mask_metadata_credentials_test.go
// version: 1.3.0
// guid: 2c4f9a71-6b83-4d05-9e12-7a8f3b6c0d54
// last-edited: 2026-09-02

package config

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// realKey is long enough that database.MaskSecret keeps a 3-char prefix and a
// 4-char suffix rather than collapsing to "****", so a test asserting the
// cleartext is gone is actually asserting something.
const realKey = "AIzaSyA91r1mZKQLd1Fof18JDjNoqCeSZ-k35bE"

func sourcesWithKey(key string) []MetadataSource {
	return []MetadataSource{
		{
			ID:          "google-books",
			Name:        "Google Books",
			Enabled:     true,
			Credentials: map[string]string{"apiKey": key},
		},
		{
			ID:          "hardcover",
			Name:        "Hardcover",
			Credentials: map[string]string{"apiKey": ""},
		},
		{
			ID:          "audible",
			Name:        "Audible",
			Credentials: map[string]string{},
		},
		{ID: "wikipedia", Name: "Wikipedia"}, // nil Credentials
	}
}

// TestMaskSecrets_MasksMetadataSourceCredentials is the regression test for the
// leak: GET /api/v1/config masked the scalar google_books_api_key while
// returning the same key in full cleartext at
// metadata_sources[].credentials.apiKey.
func TestMaskSecrets_MasksMetadataSourceCredentials(t *testing.T) {
	us := &UpdateService{}
	cfg := Config{
		GoogleBooksAPIKey: realKey,
		MetadataSources:   sourcesWithKey(realKey),
	}

	masked := us.MaskSecrets(cfg)

	got := masked.MetadataSources[0].Credentials["apiKey"]
	if got == realKey {
		t.Fatalf("credential returned in cleartext: %q", got)
	}
	if !strings.Contains(got, "****") {
		t.Fatalf("credential not masked, got %q", got)
	}

	// The whole serialized response must not contain the secret anywhere --
	// masking one field while another still carries it is the exact bug.
	blob, err := json.Marshal(masked)
	if err != nil {
		t.Fatalf("marshal masked config: %v", err)
	}
	if strings.Contains(string(blob), realKey) {
		t.Fatal("serialized config still contains the cleartext key")
	}
}

// TestMaskSecrets_DoesNotMutateSourceConfig guards the shallow-copy footgun.
// MaskSecrets does `masked := cfg`, which copies the MetadataSources slice
// HEADER but shares the backing array, and each Credentials map is a reference.
// Masking in place would overwrite the live AppConfig's real credentials with
// the mask, after which every provider client would authenticate with
// "AIz****35bE". A fix without a deep copy passes the test above and still
// destroys production credentials on the first GET /api/v1/config.
func TestMaskSecrets_DoesNotMutateSourceConfig(t *testing.T) {
	us := &UpdateService{}
	srcs := sourcesWithKey(realKey)
	cfg := Config{GoogleBooksAPIKey: realKey, MetadataSources: srcs}

	_ = us.MaskSecrets(cfg)

	if got := srcs[0].Credentials["apiKey"]; got != realKey {
		t.Fatalf("MaskSecrets mutated the caller's credentials: got %q, want the original key", got)
	}

	// Masking twice must be stable for the same reason.
	_ = us.MaskSecrets(cfg)
	if got := srcs[0].Credentials["apiKey"]; got != realKey {
		t.Fatalf("second MaskSecrets mutated the caller's credentials: got %q", got)
	}
}

// TestMaskMetadataSourceCredentials_EdgeCases pins the shapes that must not
// panic and must stay distinguishable in the UI.
func TestMaskMetadataSourceCredentials_EdgeCases(t *testing.T) {
	if got := maskMetadataSourceCredentials(nil); got != nil {
		t.Fatalf("nil slice should stay nil, got %#v", got)
	}

	out := maskMetadataSourceCredentials(sourcesWithKey(realKey))

	// An empty value stays empty so the UI can tell "not configured" from
	// "configured but hidden"; masking it would render Hardcover as if a token
	// were set.
	if got := out[1].Credentials["apiKey"]; got != "" {
		t.Fatalf("empty credential should stay empty, got %q", got)
	}
	if out[2].Credentials == nil || len(out[2].Credentials) != 0 {
		t.Fatalf("empty map should stay an empty map, got %#v", out[2].Credentials)
	}
	if out[3].Credentials != nil {
		t.Fatalf("nil map should stay nil, got %#v", out[3].Credentials)
	}

	// Non-secret fields must survive the copy.
	if out[0].ID != "google-books" || out[0].Name != "Google Books" || !out[0].Enabled {
		t.Fatalf("non-secret fields lost in copy: %#v", out[0])
	}
}

// --- round-trip protection -------------------------------------------------
//
// Masking the GET response is only safe if a PUT that echoes the mask back does
// NOT overwrite the stored key. The scalar secrets are protected by
// secretFieldKeys (deleted from the payload before the JSON round-trip);
// metadata_sources is not in that list and json.Unmarshal replaces the whole
// slice, so without restoreMaskedCredentials this fix would turn a disclosure
// bug into permanent credential loss.

func TestRestoreMaskedCredentials_EchoedMaskDoesNotOverwrite(t *testing.T) {
	prior := map[string]map[string]string{
		"google-books": {"apiKey": realKey},
	}
	// What a client sends back after GET handed it the mask.
	srcs := []MetadataSource{
		{ID: "google-books", Credentials: map[string]string{"apiKey": database.MaskSecret(realKey)}},
	}

	restoreMaskedCredentials(srcs, prior)

	if got := srcs[0].Credentials["apiKey"]; got != realKey {
		t.Fatalf("echoed mask overwrote the stored key: got %q, want the original", got)
	}
}

func TestRestoreMaskedCredentials_DroppedMapDoesNotWipe(t *testing.T) {
	prior := map[string]map[string]string{"google-books": {"apiKey": realKey}}
	srcs := []MetadataSource{{ID: "google-books"}} // payload omitted credentials

	restoreMaskedCredentials(srcs, prior)

	if got := srcs[0].Credentials["apiKey"]; got != realKey {
		t.Fatalf("omitted credentials wiped the stored key: got %q", got)
	}
}

func TestRestoreMaskedCredentials_EmptyValueDoesNotWipe(t *testing.T) {
	prior := map[string]map[string]string{"google-books": {"apiKey": realKey}}
	srcs := []MetadataSource{
		{ID: "google-books", Credentials: map[string]string{"apiKey": ""}},
	}

	restoreMaskedCredentials(srcs, prior)

	if got := srcs[0].Credentials["apiKey"]; got != realKey {
		t.Fatalf("empty value wiped the stored key: got %q", got)
	}
}

// A real new value must still be written through -- restoring too eagerly would
// make the key unchangeable, which is its own bug.
func TestRestoreMaskedCredentials_NewValueIsWrittenThrough(t *testing.T) {
	const rotated = "AIzaBRANDNEWKEYVALUE0123456789abcdefXYZ"
	prior := map[string]map[string]string{"google-books": {"apiKey": realKey}}
	srcs := []MetadataSource{
		{ID: "google-books", Credentials: map[string]string{"apiKey": rotated}},
	}

	restoreMaskedCredentials(srcs, prior)

	if got := srcs[0].Credentials["apiKey"]; got != rotated {
		t.Fatalf("rotation blocked: got %q, want %q", got, rotated)
	}
}

// A source that did not exist before must not gain credentials from nowhere.
func TestRestoreMaskedCredentials_UnknownSourceUntouched(t *testing.T) {
	prior := map[string]map[string]string{"google-books": {"apiKey": realKey}}
	srcs := []MetadataSource{
		{ID: "hardcover", Credentials: map[string]string{"apiKey": "brand-new-token-value"}},
	}

	restoreMaskedCredentials(srcs, prior)

	if got := srcs[0].Credentials["apiKey"]; got != "brand-new-token-value" {
		t.Fatalf("unknown source was altered: got %q", got)
	}
}

// --- integration: the round trip through UpdateConfig -----------------------
//
// The unit tests above call restoreMaskedCredentials directly, which leaves the
// CALL SITE untested: deleting the call from UpdateConfig's Mutate closure keeps
// every test above green while production loses the protection entirely.
// Mutation-tested -- removing that call must fail a test in this section.
//
// These assert on the live AppConfig, never on UpdateConfig's response.
// MaskSecret is idempotent (MaskSecret("AIz****35bE") == "AIz****35bE"), so the
// masked response is byte-identical whether the credential survived or was
// destroyed. A test asserting on resp["config"] passes in both worlds.

// withAppConfig installs cfg as the global AppConfig and restores the previous
// value when the test ends.
func withAppConfig(t *testing.T, cfg Config) {
	t.Helper()
	prev := Snapshot()
	t.Cleanup(func() { Mutate(func(c *Config) { *c = prev }) })
	Mutate(func(c *Config) { *c = cfg })
}

// TestUpdateConfig_EchoedMaskDoesNotDestroyStoredCredential models the real
// trigger: web/src/hooks/useSettingsHandlers.ts:453-461 sends metadata_sources
// on EVERY settings save, copying source.credentials straight out of app state
// -- which was populated from GET /api/v1/config, i.e. the mask. This is not a
// rare race; it is the ordinary Save Settings path.
func TestUpdateConfig_EchoedMaskDoesNotDestroyStoredCredential(t *testing.T) {
	withAppConfig(t, Config{RootDir: t.TempDir(), MetadataSources: sourcesWithKey(realKey)})
	us := NewUpdateService(newMockSettingsStore())

	// Exactly what the browser sends back: the partial payload, carrying the
	// mask GET handed it.
	masked := us.MaskSecrets(Snapshot())
	payload := map[string]any{
		"metadata_sources": []any{
			map[string]any{
				"id":          "google-books",
				"name":        "Google Books",
				"enabled":     true,
				"credentials": map[string]any{"apiKey": masked.MetadataSources[0].Credentials["apiKey"]},
			},
		},
	}
	if got := payload["metadata_sources"].([]any)[0].(map[string]any)["credentials"].(map[string]any)["apiKey"]; got == realKey {
		t.Fatalf("test is not exercising the bug: payload carries the cleartext key %q", got)
	}

	if status, resp := us.UpdateConfig(context.Background(), payload); status != http.StatusOK {
		t.Fatalf("UpdateConfig returned %d: %v", status, resp)
	}

	got := Snapshot().MetadataSources
	if len(got) != 1 {
		t.Fatalf("expected 1 source after update, got %d", len(got))
	}
	if got[0].Credentials["apiKey"] != realKey {
		t.Fatalf("settings save destroyed the stored credential: got %q, want the original key", got[0].Credentials["apiKey"])
	}
}

// A genuinely new credential must still reach the stored config through
// UpdateConfig -- restoring too eagerly would make the key unchangeable.
func TestUpdateConfig_NewCredentialIsWrittenThrough(t *testing.T) {
	const rotated = "AIzaBRANDNEWKEYVALUE0123456789abcdefXYZ"
	withAppConfig(t, Config{RootDir: t.TempDir(), MetadataSources: sourcesWithKey(realKey)})
	us := NewUpdateService(newMockSettingsStore())

	payload := map[string]any{
		"metadata_sources": []any{
			map[string]any{
				"id":          "google-books",
				"credentials": map[string]any{"apiKey": rotated},
			},
		},
	}
	if status, resp := us.UpdateConfig(context.Background(), payload); status != http.StatusOK {
		t.Fatalf("UpdateConfig returned %d: %v", status, resp)
	}

	if got := Snapshot().MetadataSources[0].Credentials["apiKey"]; got != rotated {
		t.Fatalf("rotation blocked through UpdateConfig: got %q, want %q", got, rotated)
	}
}

// --- scalar secrets --------------------------------------------------------
//
// The same round trip destroys the scalar secrets. secretFieldKeys keeps them
// out of the JSON round-trip, but the explicit apply above it writes whatever
// the payload carried -- including an echoed mask. The browser dodges this by
// never seeding the field from the response (Settings.tsx:494), but that is a
// client convention, not an invariant; curl and scripts hit it.

func TestUpdateConfig_EchoedScalarMaskDoesNotDestroySecret(t *testing.T) {
	withAppConfig(t, Config{RootDir: t.TempDir(), GoogleBooksAPIKey: realKey})
	us := NewUpdateService(newMockSettingsStore())

	payload := map[string]any{"google_books_api_key": database.MaskSecret(realKey)}
	if status, resp := us.UpdateConfig(context.Background(), payload); status != http.StatusOK {
		t.Fatalf("UpdateConfig returned %d: %v", status, resp)
	}

	if got := Snapshot().GoogleBooksAPIKey; got != realKey {
		t.Fatalf("echoed mask destroyed the scalar secret: got %q, want the original key", got)
	}
}

// Clearing a scalar secret with "" must keep working -- it is the only way to
// unset one, and unlike a metadata-source credential the intent is unambiguous.
func TestUpdateConfig_EmptyScalarStillClearsSecret(t *testing.T) {
	withAppConfig(t, Config{RootDir: t.TempDir(), GoogleBooksAPIKey: realKey})
	us := NewUpdateService(newMockSettingsStore())

	if status, resp := us.UpdateConfig(context.Background(), map[string]any{"google_books_api_key": ""}); status != http.StatusOK {
		t.Fatalf("UpdateConfig returned %d: %v", status, resp)
	}

	if got := Snapshot().GoogleBooksAPIKey; got != "" {
		t.Fatalf("empty value no longer clears the secret: got %q", got)
	}
}

func TestUpdateConfig_NewScalarSecretIsWrittenThrough(t *testing.T) {
	const rotated = "AIzaROTATEDSCALARKEY0123456789abcdefQ"
	withAppConfig(t, Config{RootDir: t.TempDir(), GoogleBooksAPIKey: realKey})
	us := NewUpdateService(newMockSettingsStore())

	if status, resp := us.UpdateConfig(context.Background(), map[string]any{"google_books_api_key": rotated}); status != http.StatusOK {
		t.Fatalf("UpdateConfig returned %d: %v", status, resp)
	}

	if got := Snapshot().GoogleBooksAPIKey; got != rotated {
		t.Fatalf("scalar rotation blocked: got %q, want %q", got, rotated)
	}
}
