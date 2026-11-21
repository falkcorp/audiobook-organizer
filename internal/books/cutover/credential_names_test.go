// file: internal/books/cutover/credential_names_test.go
// version: 1.0.0
// guid: 8e3a6c12-5d49-4b07-a1f8-2c9b7d4e0a63
// last-edited: 2026-11-19

package cutover

import (
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/secrets"
)

// TestCredentialNames_RoundTripTheKeysHEADReads: the two credential keys HEAD's
// providers read (internal/metafetch) survive name and back, the names are valid
// secret names, and a plugin settings name gives its id back.
func TestCredentialNames_RoundTripTheKeysHEADReads(t *testing.T) {
	for _, k := range []string{"apiKey", "api_token"} {
		n := MetadataCredentialName("hardcover", k)
		if err := n.Validate(); err != nil {
			t.Errorf("%s is not a valid secret name: %v", n, err)
		}
		seg := strings.TrimPrefix(string(n), MetadataCredentialPrefix("hardcover"))
		if got := MetadataCredentialKey(seg); got != k {
			t.Errorf("key %q -> segment %q -> %q", k, seg, got)
		}
	}
	if got := MetadataCredentialKey("custom_thing"); got != "custom_thing" {
		t.Errorf("an unknown segment must pass through, got %q", got)
	}
	for _, id := range []string{"webhook", "bat-signal"} {
		got, ok := PluginIDOf(PluginSettingsName(id))
		if !ok || got != id {
			t.Errorf("PluginIDOf(%s) = %q, %v", PluginSettingsName(id), got, ok)
		}
	}
	for _, bad := range []secrets.Name{"modules.books.plugins..settings", "modules.books.plugins.a.b.settings", "modules.books.other.x.settings"} {
		if id, ok := PluginIDOf(bad); ok {
			t.Errorf("PluginIDOf(%s) = %q, true", bad, id)
		}
	}
	if WebhookSecretName(3) != "integrations.notify.webhook-3.secret" || !strings.HasPrefix(string(WebhookSecretName(1)), WebhookSecretPrefix) {
		t.Errorf("webhook name = %s", WebhookSecretName(3))
	}
}
