// file: internal/books/cutover/head_defaults_test.go
// version: 1.0.0
// guid: b04c7e19-6a25-4d83-9f10-3c58e2a7d614
// last-edited: 2026-11-11

package cutover

import (
	"slices"
	"testing"
)

// headDefaultConfig is HEAD's Config as its SetDefault table builds it. The
// AI-endpoints transform starts from it, as HEAD's does (aiEndpointsSeed.Base
// is the live config, which holds the viper defaults at boot), so a field the
// blob leaves out reads its default, not its zero value.
func TestHeadDefaultConfig(t *testing.T) {
	cfg, misfit := headDefaultConfig()
	if !slices.Equal(misfit, []string{"whisper_endpoints"}) {
		t.Errorf("misfit = %v, want only whisper_endpoints (viper's default for it is the empty string of its env form, which configFromViper parses apart)", misfit)
	}
	for name, ok := range map[string]bool{
		"enable_ai_parsing true":                    cfg.EnableAIParsing,
		"embedding.model is text-embedding-3-large": cfg.Embedding.Model == "text-embedding-3-large",
		"embedding.dimensions is 3072":              cfg.Embedding.Dimensions == 3072,
		"concurrent_scans is not zero":              cfg.ConcurrentScans != 0,
		"ai_backend.local_embedding_model is set":   cfg.AIBackend.LocalEmbeddingModel == "bge-m3",
	} {
		if !ok {
			t.Errorf("the defaults do not satisfy: %s", name)
		}
	}
	if len(cfg.WhisperEndpoints) != 0 {
		t.Errorf("whisper_endpoints = %v, want the zero value for the misfit member", cfg.WhisperEndpoints)
	}
}

// With an OpenAI key and a blob that sets nothing else, HEAD's migrated row
// carries the defaults' embedding model and per-capability models; a zero
// config would leave both empty.
func TestHeadMigrateAIEndpointsBlob_StartsFromDefaults(t *testing.T) {
	out, changed := headMigrateAIEndpointsBlob(`{"root_dir":"/mnt/gotham"}`, headAIEndpointsSeed{HasOpenAIKey: true})
	if !changed {
		t.Fatal("not migrated")
	}
	rows := blobMap(t, out)["ai_endpoints"].([]any)
	openai := rows[0].(map[string]any)
	if openai["id"] != "openai" {
		t.Fatalf("first row = %v", openai)
	}
	if openai["embed_model"] != "text-embedding-3-large" {
		t.Errorf("embed_model = %v, want the embedding.model default", openai["embed_model"])
	}
	caps, _ := openai["capability_models"].(map[string]any)
	if caps["llm.filename_parse"] != "gpt-5-mini" {
		t.Errorf("capability_models = %v, want the filename_parse_model default on llm.filename_parse", openai["capability_models"])
	}
}
