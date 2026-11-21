// file: internal/books/cutover/credential_names.go
// version: 1.0.0
// guid: 4b8e1d93-7a20-4c65-8f3b-9d0c5e2a6f17
// last-edited: 2026-11-19

package cutover

import (
	"fmt"
	"strings"

	"github.com/falkcorp/mediacore/secrets"
)

// The secret names M2 gives the secrets that live inside class-i values, shared
// with the layer that reads and writes them after the flip (internal/books
// config_residual.go), so the two cannot drift.

// MetadataCredentialPrefix is the prefix of the secrets that hold the
// credentials of metadata source sourceID.
func MetadataCredentialPrefix(sourceID string) string {
	return "integrations.metadata." + sourceID + ".credentials."
}

// MetadataCredentialName is the secret that holds credential key k of metadata
// source sourceID: HEAD's camel-case key as a name segment.
func MetadataCredentialName(sourceID, k string) secrets.Name {
	return secrets.Name(MetadataCredentialPrefix(sourceID) + snakeSegment(k))
}

// MetadataCredentialKey is the HEAD key a secret segment under
// MetadataCredentialPrefix stands for. A segment is lossy (apiKey and api_key
// both become api_key); HEAD's providers read two keys, "apiKey" and "api_token",
// and this maps the segment of the first back to it. Any other segment is
// returned as it is.
func MetadataCredentialKey(segment string) string {
	if segment == snakeSegment("apiKey") {
		return "apiKey"
	}
	return segment
}

// WebhookSecretName is the secret of the nth (from 1) webhook instance.
func WebhookSecretName(n int) secrets.Name {
	return secrets.Name(fmt.Sprintf("integrations.notify.webhook-%d.secret", n))
}

// WebhookSecretPrefix is the name prefix every webhook instance's secret
// shares.
const WebhookSecretPrefix = "integrations.notify.webhook-"

// PluginSettingsName is the secret that holds the settings map of plugin id.
func PluginSettingsName(id string) secrets.Name {
	return secrets.Name("modules.books.plugins." + id + ".settings")
}

// PluginSettingsPrefix and PluginSettingsSuffix bracket a plugin id in a
// PluginSettingsName.
const (
	PluginSettingsPrefix = "modules.books.plugins."
	PluginSettingsSuffix = ".settings"
)

// PluginIDOf is the plugin id of a PluginSettingsName, and whether name is one.
func PluginIDOf(name secrets.Name) (string, bool) {
	id, ok := strings.CutPrefix(string(name), PluginSettingsPrefix)
	if !ok {
		return "", false
	}
	id, ok = strings.CutSuffix(id, PluginSettingsSuffix)
	return id, ok && id != "" && !strings.Contains(id, ".")
}
