// file: internal/config/viper_decode_test.go
// version: 1.0.0
// guid: d063ec75-bbf1-4354-9e52-32ede9ef23ec
// last-edited: 2026-10-11

// The list and map settings InitConfig decodes with viperDecodeJSON and
// viperPathList, through each source an operator can use: the environment
// (a string), a YAML config file (whose map-key case viper would otherwise
// lose) and a higher-precedence override.

package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/spf13/viper"
)

// fileInitConfig runs InitConfig with the given YAML as the config file, the
// way cmd/root.go loads one (SetConfigFile + ReadInConfig, AutomaticEnv on).
// before, if non-nil, runs after the file is read and before InitConfig.
func fileInitConfig(t *testing.T, yamlText string, before func()) {
	t.Helper()
	restoreAppConfig(t)
	viper.Reset()
	t.Cleanup(viper.Reset)
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(yamlText), 0o600); err != nil {
		t.Fatal(err)
	}
	viper.SetConfigFile(path)
	viper.AutomaticEnv()
	if err := viper.ReadInConfig(); err != nil {
		t.Fatalf("ReadInConfig: %v", err)
	}
	if before != nil {
		before()
	}
	InitConfig()
}

// An environment variable reaches viper as a string; before 2026-10-11 it was
// marshalled as a JSON string and failed to decode into the list or map, so
// AI_ENDPOINTS and PLUGINS were silently ignored.
func TestViperDecodeJSON_EnvironmentJSONList(t *testing.T) {
	t.Setenv("AI_ENDPOINTS", `[{"id":"Box1","label":"GPU box","protocol":"openai","url":"http://192.0.2.5:8080"}]`)
	envInitConfig(t)
	got := Snapshot().AIEndpoints
	if len(got) != 1 || got[0].ID != "Box1" || got[0].URL != "http://192.0.2.5:8080" || got[0].Protocol != "openai" {
		t.Fatalf("AIEndpoints = %+v, want the one endpoint from AI_ENDPOINTS", got)
	}
}

func TestViperDecodeJSON_EnvironmentJSONMapKeepsKeyCase(t *testing.T) {
	t.Setenv("PLUGINS", `{"MyPlugin":{"enabled":true,"settings":{"ApiKey":"abc"}}}`)
	envInitConfig(t)
	got := Snapshot().Plugins
	want := map[string]PluginConfig{"MyPlugin": {Enabled: true, Settings: map[string]string{"ApiKey": "abc"}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Plugins = %+v, want %+v", got, want)
	}
}

// Invalid JSON is logged and leaves the field at its default; it never
// panics or fails startup.
func TestViperDecodeJSON_EnvironmentInvalidJSONKeepsDefault(t *testing.T) {
	t.Setenv("AI_ENDPOINTS", `[{"id":"broken"`)
	t.Setenv("PLUGINS", `not json`)
	envInitConfig(t)
	c := Snapshot()
	if len(c.AIEndpoints) != 0 || len(c.Plugins) != 0 {
		t.Fatalf("AIEndpoints=%+v Plugins=%+v, want both left at the empty default", c.AIEndpoints, c.Plugins)
	}
}

// viper lower-cases every map key it loads from a config file, at every depth.
// Plugin IDs, plugin setting names and embedding model names are map keys, so
// the case the operator wrote must be read back from the file itself.
func TestViperDecodeJSON_ConfigFileKeepsMapKeyCase(t *testing.T) {
	fileInitConfig(t, `
plugins:
  MyPlugin:
    enabled: true
    settings:
      ApiKey: abc
dedup:
  embedding_thresholds_by_model:
    Text-Embedding-3-Large:
      high: 0.91
      low: 0.8
ai_endpoints:
  - id: Box1
    url: http://192.0.2.5:8080
    protocol: openai
`, nil)
	c := Snapshot()
	wantPlugins := map[string]PluginConfig{"MyPlugin": {Enabled: true, Settings: map[string]string{"ApiKey": "abc"}}}
	if !reflect.DeepEqual(c.Plugins, wantPlugins) {
		t.Errorf("Plugins = %+v, want %+v (config-file map keys must keep their case)", c.Plugins, wantPlugins)
	}
	wantModels := map[string]EmbeddingModelThresholds{"Text-Embedding-3-Large": {High: 0.91, Low: 0.8}}
	if !reflect.DeepEqual(c.Dedup.EmbeddingThresholdsByModel, wantModels) {
		t.Errorf("EmbeddingThresholdsByModel = %+v, want %+v", c.Dedup.EmbeddingThresholdsByModel, wantModels)
	}
	if len(c.AIEndpoints) != 1 || c.AIEndpoints[0].ID != "Box1" {
		t.Errorf("AIEndpoints = %+v, want the one endpoint from the file", c.AIEndpoints)
	}
}

// A higher-precedence source (here viper.Set, which outranks the config file
// like a flag or the environment does) still wins: the file's value is used
// only when it IS viper's value.
func TestViperDecodeJSON_OverrideBeatsConfigFile(t *testing.T) {
	fileInitConfig(t, `
plugins:
  FilePlugin:
    enabled: true
`, func() {
		viper.Set("plugins", map[string]any{"setplugin": map[string]any{"enabled": false}})
	})
	got := Snapshot().Plugins
	if _, ok := got["setplugin"]; !ok || len(got) != 1 {
		t.Fatalf("Plugins = %+v, want only the override's entry", got)
	}
}

// The environment outranks the config file too, and keeps its own case.
func TestViperDecodeJSON_EnvironmentBeatsConfigFile(t *testing.T) {
	t.Setenv("PLUGINS", `{"EnvPlugin":{"enabled":true}}`)
	fileInitConfig(t, `
plugins:
  FilePlugin:
    enabled: true
`, nil)
	got := Snapshot().Plugins
	if _, ok := got["EnvPlugin"]; !ok || len(got) != 1 {
		t.Fatalf("Plugins = %+v, want only the environment's entry", got)
	}
}

// PROTECTED_PATHS guards in-place writes. viper.GetStringSlice split an env
// string on whitespace only, so the natural "/a,/b" became ONE entry that
// protected neither path.
func TestViperPathList_Environment(t *testing.T) {
	cases := []struct {
		name, env string
		want      []string
	}{
		{"comma list", "/a,/b", []string{"/a", "/b"}},
		{"comma list with spaces and empties", " /a , /b ,, ", []string{"/a", "/b"}},
		{"a path with a space is one path", "/mnt/My Books", []string{"/mnt/My Books"}},
		{"comma list of paths with spaces", "/mnt/My Books,/mnt/Other Books", []string{"/mnt/My Books", "/mnt/Other Books"}},
		{"JSON array", `["/a","/b, with comma"]`, []string{"/a", "/b, with comma"}},
		{"invalid JSON array protects nothing and is logged", `["/a",`, nil},
		{"empty", "  ", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("PROTECTED_PATHS", tc.env)
			envInitConfig(t)
			if got := Snapshot().ProtectedPaths; !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ProtectedPaths = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestViperPathList_ConfigFileList(t *testing.T) {
	fileInitConfig(t, `
protected_paths:
  - /mnt/My Books
  - /b
`, nil)
	if got, want := Snapshot().ProtectedPaths, []string{"/mnt/My Books", "/b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ProtectedPaths = %q, want %q", got, want)
	}
}

// dedup.signals.confidence is keyed by signal kind, a lower-case identifier
// that unified.LoadScoreConfig looks up exactly (an unknown kind refuses
// startup). Keeping the file's case there would turn "Exact_File:" into a
// fatal unknown kind, so its keys are folded, like every other
// dedup.signals.* key viper reads.
func TestViperDecodeJSON_SignalKindConfidenceKeysAreFolded(t *testing.T) {
	fileInitConfig(t, `
dedup:
  signals:
    confidence:
      Exact_File:
        min_confidence: 0.3
`, nil)
	got := Snapshot().Dedup.Signals.Confidence
	if len(got) != 1 || got["exact_file"].MinConfidence != 0.3 {
		t.Fatalf("Confidence = %+v, want the one override under exact_file", got)
	}
}

func TestLowerMapKeys_LowerCaseSpellingWinsAFold(t *testing.T) {
	got := lowerMapKeys(map[string]int{"Exact_File": 1, "exact_file": 2, "Duration": 3})
	if want := map[string]int{"exact_file": 2, "duration": 3}; !reflect.DeepEqual(got, want) {
		t.Fatalf("lowerMapKeys = %v, want %v", got, want)
	}
	if lowerMapKeys[int](nil) != nil {
		t.Fatal("lowerMapKeys(nil) must stay nil")
	}
}
