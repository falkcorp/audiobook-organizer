// file: internal/books/cutover/head_defaults_test.go
// version: 1.1.0
// guid: b04c7e19-6a25-4d83-9f10-3c58e2a7d614
// last-edited: 2026-11-11

package cutover

import (
	"fmt"
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

// concurrent_scans' HEAD default is max(runtime.NumCPU(), 4), computed on the
// host (config.go registerConfigDefaults), not the recorded table's 10: the
// table holds the recording host's value. M2 compares against the machine HEAD
// ran on, through headNumCPU.
func TestHeadDefault_ConcurrentScansFollowsTheHost(t *testing.T) {
	for _, tc := range []struct {
		cpus int
		want float64
	}{{1, 4}, {2, 4}, {4, 4}, {10, 10}, {16, 16}} {
		t.Run(fmt.Sprintf("%d CPUs", tc.cpus), func(t *testing.T) {
			pinHostCPUs(t, tc.cpus)
			if got, ok := headDefault("concurrent_scans"); !ok || got != tc.want {
				t.Fatalf("headDefault = %v, %v; want %v", got, ok, tc.want)
			}
			for _, v := range []any{4.0, 10.0, 16.0, 3.0} {
				if got, want := headIsDefault("concurrent_scans", v), v == tc.want; got != want {
					t.Errorf("headIsDefault(concurrent_scans, %v) = %v on %d CPUs, want %v", v, got, tc.cpus, want)
				}
			}
			cfg, _ := headDefaultConfig()
			if float64(cfg.ConcurrentScans) != tc.want {
				t.Errorf("headDefaultConfig().ConcurrentScans = %d on %d CPUs, want %v", cfg.ConcurrentScans, tc.cpus, tc.want)
			}
		})
	}
	t.Run("the recorded table still says 10, and is not what answers", func(t *testing.T) {
		if rec := headDefaultsTable()["concurrent_scans"]; rec != 10.0 {
			t.Fatalf("the recorded table holds %v, want the recording host's 10", rec)
		}
		pinHostCPUs(t, 16)
		if got, _ := headDefault("concurrent_scans"); got == 10.0 {
			t.Error("headDefault answered from the recorded table on a 16-CPU host")
		}
	})
}

// playlist_dir has no SetDefault: its default is the flag default cmd/root.go
// binds, "playlists", which is what viper returns for a key the file does not
// set. An explicit empty string in the file is a value, not the default.
func TestHeadDefault_PlaylistDirIsTheFlagDefault(t *testing.T) {
	if got, ok := headDefault("playlist_dir"); !ok || got != "playlists" {
		t.Fatalf("headDefault(playlist_dir) = %v, %v; want playlists", got, ok)
	}
	for v, want := range map[string]bool{"playlists": true, "": false, "/mnt/gotham-playlists": false, "Playlists": false} {
		if got := headIsDefault("playlist_dir", v); got != want {
			t.Errorf("headIsDefault(playlist_dir, %q) = %v, want %v", v, got, want)
		}
	}
	if cfg, _ := headDefaultConfig(); cfg.PlaylistDir != "playlists" {
		t.Errorf("headDefaultConfig().PlaylistDir = %q, want playlists", cfg.PlaylistDir)
	}
	if got := headParseFileSettings([]byte("playlist_dir: \"\"\n")); got["playlist_dir"] != "" {
		t.Errorf("an explicit empty playlist_dir read as %v, want the empty string kept", got)
	}
	in := &m2Input{FileSettings: map[string]any{"playlist_dir": ""}}
	if got := fileBlobBound(in); !slices.Equal(got, []string{"playlist_dir"}) {
		t.Errorf("fileBlobBound with an explicit empty playlist_dir = %v, want it named", got)
	}
	in.FileSettings["playlist_dir"] = "playlists"
	if got := fileBlobBound(in); len(got) != 0 {
		t.Errorf("fileBlobBound with playlist_dir at its default = %v, want nothing", got)
	}
}
