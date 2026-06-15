// file: internal/tools/config_types_test.go
// version: 1.0.0
// guid: d4e5f6a7-b8c9-0123-defa-123456789012
// last-edited: 2026-06-15

package tools_test

import (
	"encoding/json"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/tools"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestToolModeConstants(t *testing.T) {
	assert.Equal(t, tools.ToolMode("managed"), tools.ToolModeManaged)
	assert.Equal(t, tools.ToolMode("system"), tools.ToolModeSystem)
	assert.Equal(t, tools.ToolMode("custom"), tools.ToolModeCustom)
	assert.Equal(t, tools.ToolMode("disabled"), tools.ToolModeDisabled)
}

func TestToolsConfigJSONRoundTrip(t *testing.T) {
	cfg := tools.ToolsConfig{
		ManagedDir:          "/var/lib/audiobook-organizer/tools",
		Ollama:              tools.ToolConfig{Mode: tools.ToolModeManaged, CustomPath: ""},
		Fpcalc:              tools.ToolConfig{Mode: tools.ToolModeSystem, CustomPath: ""},
		AllowPeriodicOllama: true,
		OllamaDebounceMin:   10,
	}
	b, err := json.Marshal(cfg)
	require.NoError(t, err)
	var got tools.ToolsConfig
	require.NoError(t, json.Unmarshal(b, &got))
	assert.Equal(t, cfg, got)
}
