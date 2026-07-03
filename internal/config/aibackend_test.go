// file: internal/config/aibackend_test.go
// version: 1.0.0
// last-edited: 2026-07-03

package config

import "testing"

// TestEffectiveEmbeddingMode covers the derivation branches plus the explicit
// override, matching the rule mirrored by migrateAIBackendBlob.
func TestEffectiveEmbeddingMode(t *testing.T) {
	tests := []struct {
		name string
		mut  func(c *Config)
		want string
	}{
		{
			name: "explicit mode wins",
			mut:  func(c *Config) { c.AIBackend.EmbeddingMode = AIBackendModeLocal; c.Embedding.Enabled = false },
			want: AIBackendModeLocal,
		},
		{
			name: "disabled when embedding off",
			mut:  func(c *Config) { c.Embedding.Enabled = false; c.OpenAIAPIKey = "sk-x" },
			want: AIBackendModeDisabled,
		},
		{
			name: "local when base url set",
			mut:  func(c *Config) { c.Embedding.Enabled = true; c.Embedding.BaseURL = "http://192.168.0.20:11434/v1" },
			want: AIBackendModeLocal,
		},
		{
			name: "openai when key set and no base url",
			mut:  func(c *Config) { c.Embedding.Enabled = true; c.OpenAIAPIKey = "sk-x" },
			want: AIBackendModeOpenAI,
		},
		{
			name: "disabled when enabled but nothing configured",
			mut:  func(c *Config) { c.Embedding.Enabled = true },
			want: AIBackendModeDisabled,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var c Config
			tt.mut(&c)
			if got := c.EffectiveEmbeddingMode(); got != tt.want {
				t.Fatalf("EffectiveEmbeddingMode() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestEffectiveLLMMode covers the LLM derivation branches plus override.
func TestEffectiveLLMMode(t *testing.T) {
	tests := []struct {
		name string
		mut  func(c *Config)
		want string
	}{
		{
			name: "explicit mode wins",
			mut:  func(c *Config) { c.AIBackend.LLMMode = AIBackendModeLocal },
			want: AIBackendModeLocal,
		},
		{
			name: "openai via enable_ai_parsing",
			mut:  func(c *Config) { c.OpenAIAPIKey = "sk-x"; c.EnableAIParsing = true },
			want: AIBackendModeOpenAI,
		},
		{
			name: "openai via metadata llm enabled",
			mut:  func(c *Config) { c.OpenAIAPIKey = "sk-x"; c.MetadataScoring.LLMEnabled = true },
			want: AIBackendModeOpenAI,
		},
		{
			name: "disabled without a key",
			mut:  func(c *Config) { c.EnableAIParsing = true },
			want: AIBackendModeDisabled,
		},
		{
			name: "disabled with key but no consumer enabled",
			mut:  func(c *Config) { c.OpenAIAPIKey = "sk-x" },
			want: AIBackendModeDisabled,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var c Config
			tt.mut(&c)
			if got := c.EffectiveLLMMode(); got != tt.want {
				t.Fatalf("EffectiveLLMMode() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestEffectiveMode_PureNoMutation asserts the helpers never write back to the
// config (they must stay safe for concurrent readers of the global AppConfig).
func TestEffectiveMode_PureNoMutation(t *testing.T) {
	c := Config{}
	c.Embedding.Enabled = true
	c.Embedding.BaseURL = "http://192.168.0.20:11434/v1"
	_ = c.EffectiveEmbeddingMode()
	_ = c.EffectiveLLMMode()
	if c.AIBackend.LocalBaseURL != "" {
		t.Fatalf("EffectiveEmbeddingMode mutated LocalBaseURL to %q; helpers must be pure", c.AIBackend.LocalBaseURL)
	}
	if c.AIBackend.EmbeddingMode != "" || c.AIBackend.LLMMode != "" {
		t.Fatal("effective-mode helpers must not write derived modes back into config")
	}
}
