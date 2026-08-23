// file: internal/config/aibackend_test.go
// version: 1.2.0
// last-edited: 2026-08-23

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
		{
			// TASK-018: AIBackend.LocalBaseURL is not read by
			// EffectiveEmbeddingMode at all (it derives solely from
			// Embedding.BaseURL), so leaving it empty after removing the
			// hardcoded LAN-IP default cannot change this outcome — openai
			// still wins on key + enabled, matching "openai when key set and
			// no base url" above but with LocalBaseURL set explicitly to
			// document the invariant.
			name: "openai when local_base_url empty, key set",
			mut: func(c *Config) {
				c.AIBackend.LocalBaseURL = ""
				c.Embedding.Enabled = true
				c.OpenAIAPIKey = "sk-x"
			},
			want: AIBackendModeOpenAI,
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
			// CONTRACT CHANGE (TASK-018). These two cases previously asserted
			// AIBackendModeOpenAI: a bare key plus any enabled LLM consumer
			// derived the paid backend. That fallback was deliberate and
			// documented ("falling back to OpenAI is still possible, but only
			// when there is no local option at all"), but it was reachable
			// only in theory, because ai_backend.local_base_url shipped a
			// hardcoded LAN address that made the local branch always win.
			// Removing that address made this path live for every install
			// with a key and no local endpoint -- the 2026-08-16
			// credit_balance_exhausted shape. Derivation now stops at
			// disabled; ai_backend.llm_mode = "openai" is the opt-in.
			name: "bare key + enable_ai_parsing no longer derives openai",
			mut:  func(c *Config) { c.OpenAIAPIKey = "sk-x"; c.EnableAIParsing = true },
			want: AIBackendModeDisabled,
		},
		{
			name: "bare key + metadata llm enabled no longer derives openai",
			mut:  func(c *Config) { c.OpenAIAPIKey = "sk-x"; c.MetadataScoring.LLMEnabled = true },
			want: AIBackendModeDisabled,
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
		{
			// TASK-018: with the hardcoded LAN-IP default removed,
			// LocalBaseURL="" (the fresh-install state) must fall past the
			// `c.AIBackend.LocalBaseURL != "" || c.Embedding.BaseURL != ""`
			// local-mode check without selecting local mode against an empty
			// URL. It must then land on DISABLED, not openai: derivation does
			// not opt an operator in to a paid backend. The duplicate cases
			// below cover that rule directly.
			name: "local_base_url empty with key set falls past local, not into openai",
			mut: func(c *Config) {
				c.AIBackend.LocalBaseURL = ""
				c.OpenAIAPIKey = "sk-x"
				c.EnableAIParsing = true
			},
			want: AIBackendModeDisabled,
		},
		{
			// Same fall-through, but with nothing else configured either —
			// must land on disabled, never local or a panic.
			name: "disabled when local_base_url empty and nothing else configured",
			mut: func(c *Config) {
				c.AIBackend.LocalBaseURL = ""
			},
			want: AIBackendModeDisabled,
		},
		{
			// THE REGRESSION TASK-018 WOULD OTHERWISE HAVE SHIPPED.
			//
			// This is the fresh-install state after the hardcoded LAN IP was
			// removed: no local endpoint anywhere, a key present, and
			// enable_ai_parsing at its default of TRUE. Before the derivation
			// was closed this returned "openai" — a paid backend chosen by a
			// config the operator never wrote. That is the 2026-08-16
			// credit_balance_exhausted incident, which the hardcoded address
			// had been masking.
			name: "fresh install with a bare key must NOT derive a paid backend",
			mut: func(c *Config) {
				c.AIBackend.LocalBaseURL = ""
				c.Embedding.BaseURL = ""
				c.OpenAIAPIKey = "sk-user-key"
				c.EnableAIParsing = true
			},
			want: AIBackendModeDisabled,
		},
		{
			// Same, via the other LLM consumer, so closing one route does not
			// leave the other open.
			name: "bare key with metadata_scoring.llm_enabled must NOT derive openai",
			mut: func(c *Config) {
				c.AIBackend.LocalBaseURL = ""
				c.OpenAIAPIKey = "sk-user-key"
				c.MetadataScoring.LLMEnabled = true
			},
			want: AIBackendModeDisabled,
		},
		{
			// POSITIVE CONTROL. Without it, a change that disabled OpenAI
			// unconditionally would pass every case above while quietly
			// removing the backend entirely.
			name: "explicit llm_mode=openai still selects openai",
			mut: func(c *Config) {
				c.AIBackend.LLMMode = AIBackendModeOpenAI
				c.AIBackend.LocalBaseURL = ""
				c.OpenAIAPIKey = "sk-user-key"
			},
			want: AIBackendModeOpenAI,
		},
		{
			// A configured local endpoint must still win on its own.
			name: "local endpoint still derives local",
			mut: func(c *Config) {
				c.AIBackend.LocalBaseURL = "http://localhost:11434/v1"
			},
			want: AIBackendModeLocal,
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
