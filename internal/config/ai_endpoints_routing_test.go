// file: internal/config/ai_endpoints_routing_test.go
// version: 1.0.0
// guid: 960ae30e-e1b9-40b6-9dae-6b15a0307521
// last-edited: 2026-09-19

package config

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The switch defaults OFF: a zero Config and a blob that predates the key both
// leave routing disabled, so an upgrade changes no call site's backend.
func TestAIEndpointsRouting_DefaultsOff(t *testing.T) {
	var zero Config
	assert.False(t, zero.AIEndpointsRouting)

	var fromOldBlob Config
	require.NoError(t, json.Unmarshal([]byte(`{"ai_endpoints":[]}`), &fromOldBlob))
	assert.False(t, fromOldBlob.AIEndpointsRouting)
}
