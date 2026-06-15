// file: internal/tools/config_types.go
// version: 1.0.0
// guid: e5f6a7b8-c9d0-1234-efab-234567890123
// last-edited: 2026-06-15

// Package tools manages the lifecycle of external binaries (Ollama daemon,
// fpcalc CLI) used by the audiobook organizer pipeline.
package tools

// ToolMode controls how a tool binary is located.
type ToolMode string

const (
	ToolModeManaged  ToolMode = "managed"
	ToolModeSystem   ToolMode = "system"
	ToolModeCustom   ToolMode = "custom"
	ToolModeDisabled ToolMode = "disabled"
)

// ToolConfig holds per-tool mode and optional custom path.
type ToolConfig struct {
	Mode       ToolMode `json:"mode"`
	CustomPath string   `json:"custom_path"`
}

// ToolsConfig is the nested config block for all managed external tools.
type ToolsConfig struct {
	ManagedDir          string     `json:"managed_dir"`
	Ollama              ToolConfig `json:"ollama"`
	Fpcalc              ToolConfig `json:"fpcalc"`
	AllowPeriodicOllama bool       `json:"allow_periodic_ollama"`
	OllamaDebounceMin   int        `json:"ollama_debounce_min"`
}
