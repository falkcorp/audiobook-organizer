// file: internal/ai/register_test.go
// version: 1.0.0
// last-edited: 2026-07-03

package ai

import "testing"

// TestResolveAIEndpointKey covers the 4 key/base-URL combinations documented
// on resolveAIEndpointKey: a real key always wins, a dummy key is substituted
// when only a base URL is present, and construction is skipped when neither
// is configured.
func TestResolveAIEndpointKey(t *testing.T) {
	tests := []struct {
		name       string
		apiKey     string
		baseURL    string
		wantKey    string
		wantOK     bool
		keyIsDummy bool
	}{
		{
			name:    "real key, no base URL",
			apiKey:  "sk-real-key",
			baseURL: "",
			wantKey: "sk-real-key",
			wantOK:  true,
		},
		{
			name:    "real key, with base URL (key takes priority)",
			apiKey:  "sk-real-key",
			baseURL: "http://localhost:11434/v1",
			wantKey: "sk-real-key",
			wantOK:  true,
		},
		{
			name:       "no key, base URL configured (dummy key substituted)",
			apiKey:     "",
			baseURL:    "http://localhost:11434/v1",
			wantOK:     true,
			keyIsDummy: true,
		},
		{
			name:    "no key, no base URL (construction skipped)",
			apiKey:  "",
			baseURL: "",
			wantKey: "",
			wantOK:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotKey, gotOK := resolveAIEndpointKey(tt.apiKey, tt.baseURL)
			if gotOK != tt.wantOK {
				t.Fatalf("resolveAIEndpointKey(%q, %q) ok = %v, want %v", tt.apiKey, tt.baseURL, gotOK, tt.wantOK)
			}
			if tt.keyIsDummy {
				if gotKey == "" {
					t.Fatalf("resolveAIEndpointKey(%q, %q) key = %q, want non-empty dummy key", tt.apiKey, tt.baseURL, gotKey)
				}
				return
			}
			if gotKey != tt.wantKey {
				t.Fatalf("resolveAIEndpointKey(%q, %q) key = %q, want %q", tt.apiKey, tt.baseURL, gotKey, tt.wantKey)
			}
		})
	}
}
