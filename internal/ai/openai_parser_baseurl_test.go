// file: internal/ai/openai_parser_baseurl_test.go
// version: 1.0.0
// last-edited: 2026-07-03

package ai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestNewOpenAIParserWithBaseURL_ModelOverride verifies the explicit-base-URL
// constructor pins the fallback model and is independent of the process-wide
// OPENAI_BASE_URL env (which must never influence this constructor).
func TestNewOpenAIParserWithBaseURL_ModelOverride(t *testing.T) {
	// Set a bogus OPENAI_BASE_URL; the WithBaseURL constructor must ignore it.
	t.Setenv("OPENAI_BASE_URL", "http://should-not-be-read.invalid/v1")

	p := NewOpenAIParserWithBaseURL(nil, "ollama", "http://192.168.0.20:11434/v1", "qwen2.5:7b-instruct", true)
	if !p.IsEnabled() {
		t.Fatal("expected enabled parser")
	}
	if got := p.fallbackModel(); got != "qwen2.5:7b-instruct" {
		t.Fatalf("fallbackModel() = %q, want qwen2.5:7b-instruct", got)
	}
	// Per-feature helpers fall back to the override when cfg is nil.
	if got := p.filenameParseModel(); got != "qwen2.5:7b-instruct" {
		t.Fatalf("filenameParseModel() = %q, want qwen2.5:7b-instruct", got)
	}

	// Disabled construction still records the model override, so re-enabling a
	// derived client would use the right fallback.
	pd := NewOpenAIParserWithBaseURL(nil, "", "", "qwen2.5:7b-instruct", false)
	if pd.IsEnabled() {
		t.Fatal("expected disabled parser when apiKey empty")
	}
	if got := pd.fallbackModel(); got != "qwen2.5:7b-instruct" {
		t.Fatalf("disabled parser fallbackModel() = %q, want qwen2.5:7b-instruct", got)
	}
}

// TestNewOpenAIParser_DefaultModelFallback verifies the backward-compatible
// wrapper keeps the OpenAI default model when no per-feature field is set.
func TestNewOpenAIParser_DefaultModelFallback(t *testing.T) {
	p := NewOpenAIParser(nil, "sk-real", true)
	if got := p.fallbackModel(); got != defaultModel {
		t.Fatalf("fallbackModel() = %q, want %q", got, defaultModel)
	}
}

// TestProbeOllamaAvailable exercises the probe: 2xx on /api/tags -> true,
// non-2xx -> false, unreachable host -> false, "/v1" suffix stripped so the
// request targets /api/tags.
func TestProbeOllamaAvailable(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// Pass the OpenAI-compatible "/v1" form; probe must hit /api/tags.
	if !ProbeOllamaAvailable(context.Background(), srv.URL+"/v1", time.Second) {
		t.Fatal("expected available for a 2xx endpoint")
	}
	if gotPath != "/api/tags" {
		t.Fatalf("probe requested %q, want /api/tags", gotPath)
	}

	// Non-2xx -> unavailable.
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer bad.Close()
	if ProbeOllamaAvailable(context.Background(), bad.URL, time.Second) {
		t.Fatal("expected unavailable for a 5xx endpoint")
	}

	// Empty base URL -> unavailable, no panic.
	if ProbeOllamaAvailable(context.Background(), "", time.Second) {
		t.Fatal("expected unavailable for empty base URL")
	}
}
