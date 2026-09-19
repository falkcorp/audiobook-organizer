// file: internal/scanner/ai_parser_pool_routing_test.go
// version: 1.0.0
// guid: e9120432-ec54-4a19-8a0d-2833cdaafa40
// last-edited: 2026-09-19

package scanner

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/ai"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

func setLLMMode(t *testing.T, mode string, routing bool) {
	t.Helper()
	prev := config.AppConfig
	t.Cleanup(func() { config.AppConfig = prev })
	config.AppConfig.EnableAIParsing = true
	config.AppConfig.OpenAIAPIKey = "sk-test-key"
	config.AppConfig.AIBackend.LLMMode = mode
	config.AppConfig.AIBackend.LocalBaseURL = "http://192.0.2.10:11434/v1"
	config.AppConfig.AIBackend.LocalLLMModel = "qwen2.5:7b-instruct"
	config.AppConfig.AIEndpointsRouting = routing
}

// With ai_endpoints_routing on, the local and openai-fallback-local modes
// hand the scan a pool-routed llm.filename_parse parser.
func TestNewAIParser_RoutingOnUsesPool(t *testing.T) {
	for _, mode := range []string{config.AIBackendModeLocal, config.AIBackendModeOpenAIFallbackLocal} {
		t.Run(mode, func(t *testing.T) {
			setLLMMode(t, mode, true)
			p, ok := newAIParser(logger.New("test"))
			if !ok {
				t.Fatal("parser not enabled")
			}
			if _, routed := p.(*ai.RoutedFilenameParser); !routed {
				t.Fatalf("got %T, want *ai.RoutedFilenameParser", p)
			}
		})
	}
}

// With the switch off, every mode resolves exactly as before this switch
// existed: local -> a single local OpenAIParser, fallback-local -> the chain.
func TestNewAIParser_RoutingOffPreservesLegacy(t *testing.T) {
	setLLMMode(t, config.AIBackendModeLocal, false)
	p, ok := newAIParser(logger.New("test"))
	if _, legacy := p.(*ai.OpenAIParser); !ok || !legacy {
		t.Fatalf("local, switch off: got %T (enabled %v), want *ai.OpenAIParser", p, ok)
	}

	setLLMMode(t, config.AIBackendModeOpenAIFallbackLocal, false)
	p, ok = newAIParser(logger.New("test"))
	if _, chain := p.(*parserChain); !ok || !chain {
		t.Fatalf("fallback-local, switch off: got %T (enabled %v), want *parserChain", p, ok)
	}
}

// Explicit openai mode is never routed, switch or not, and disabled stays
// disabled.
func TestNewAIParser_RoutingNeverTouchesOpenAIOrDisabledMode(t *testing.T) {
	setLLMMode(t, config.AIBackendModeOpenAI, true)
	p, ok := newAIParser(logger.New("test"))
	if _, legacy := p.(*ai.OpenAIParser); !ok || !legacy {
		t.Fatalf("openai mode, switch on: got %T (enabled %v), want the legacy *ai.OpenAIParser", p, ok)
	}

	setLLMMode(t, config.AIBackendModeDisabled, true)
	if _, ok := newAIParser(logger.New("test")); ok {
		t.Fatal("disabled mode produced a parser with routing on")
	}
}
