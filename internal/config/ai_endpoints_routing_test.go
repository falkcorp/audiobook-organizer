// file: internal/config/ai_endpoints_routing_test.go
// version: 1.0.0
// guid: 960ae30e-e1b9-40b6-9dae-6b15a0307521
// last-edited: 2026-09-19

package config

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func keepRoutingSwitch(t *testing.T) {
	t.Helper()
	orig := Snapshot().AIEndpointsRouting
	t.Cleanup(func() { Mutate(func(c *Config) { c.AIEndpointsRouting = orig }) })
}

// The switch defaults OFF: a zero Config and a blob that predates the key both
// leave routing disabled, so an upgrade changes no call site's backend.
func TestAIEndpointsRouting_DefaultsOff(t *testing.T) {
	var zero Config
	assert.False(t, zero.AIEndpointsRouting)

	var fromOldBlob Config
	require.NoError(t, json.Unmarshal([]byte(`{"ai_endpoints":[]}`), &fromOldBlob))
	assert.False(t, fromOldBlob.AIEndpointsRouting)
}

// PUT /api/v1/config sets and clears it like any other top-level bool, and
// the value round-trips through the persisted blob's JSON shape.
func TestUpdateConfig_AIEndpointsRoutingToggle(t *testing.T) {
	keepRoutingSwitch(t)
	us := newEndpointsUpdateService(t)

	status, resp := putJSON(t, us, `{"ai_endpoints_routing":true}`)
	require.Equal(t, http.StatusOK, status, "resp %v", resp)
	assert.True(t, Snapshot().AIEndpointsRouting)

	blob, err := json.Marshal(Snapshot())
	require.NoError(t, err)
	var reloaded Config
	require.NoError(t, json.Unmarshal(blob, &reloaded))
	assert.True(t, reloaded.AIEndpointsRouting, "switch lost in the blob round-trip")

	status, resp = putJSON(t, us, `{"ai_endpoints_routing":false}`)
	require.Equal(t, http.StatusOK, status, "resp %v", resp)
	assert.False(t, Snapshot().AIEndpointsRouting, "switch must be disableable")
}

// The legacy per-key settings path accepts it too.
func TestApplySetting_AIEndpointsRouting(t *testing.T) {
	keepRoutingSwitch(t)
	require.NoError(t, applySetting("ai_endpoints_routing", "true", "bool"))
	assert.True(t, Snapshot().AIEndpointsRouting)
	require.NoError(t, applySetting("ai_endpoints_routing", "false", "bool"))
	assert.False(t, Snapshot().AIEndpointsRouting)
}
