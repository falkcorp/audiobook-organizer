// file: internal/config/viper_decode_test.go
// version: 1.1.0
// guid: d063ec75-bbf1-4354-9e52-32ede9ef23ec
// last-edited: 2026-10-11

// The list and map settings InitConfig decodes with viperDecodeJSON and
// viperPathList, through each source an operator can use: the environment
// (a string), a YAML config file (whose map-key case viper would otherwise
// lose) and a higher-precedence override.

package config

import (
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/viper"

	"github.com/falkcorp/audiobook-organizer/internal/logger/logtest"
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

// defaultFileInitConfig runs InitConfig with the config file loaded exactly
// the way cmd/root.go's initConfig does when no --config flag is given:
// AddConfigPath(dir) + SetConfigType("yaml") + SetConfigName(".audiobook-organizer"),
// so the file is extensionless as far as filepath.Ext is concerned
// (Ext returns ".audiobook-organizer").
func defaultFileInitConfig(t *testing.T, yamlText string) {
	t.Helper()
	restoreAppConfig(t)
	viper.Reset()
	t.Cleanup(viper.Reset)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".audiobook-organizer"), []byte(yamlText), 0o600); err != nil {
		t.Fatal(err)
	}
	viper.AddConfigPath(dir)
	viper.SetConfigType("yaml")
	viper.SetConfigName(".audiobook-organizer")
	viper.AutomaticEnv()
	if err := viper.ReadInConfig(); err != nil {
		t.Fatalf("ReadInConfig: %v", err)
	}
	if got := filepath.Base(viper.ConfigFileUsed()); got != ".audiobook-organizer" {
		t.Fatalf("ConfigFileUsed = %q, want the extensionless default file", got)
	}
	InitConfig()
}

// An environment variable reaches viper as a string, which is read as JSON
// text for a list or map setting (AI_ENDPOINTS, PLUGINS).
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

// With no lower-case spelling among the colliders, the byte-wise first key
// wins ("EXACT_FILE" < "Exact_File" < "exact_File"), whatever order the map
// is iterated in. Go randomises that order, so repeat enough to catch a
// regression to last-writer-wins.
func TestLowerMapKeys_CollisionWithoutLowerCaseKeyIsDeterministic(t *testing.T) {
	for i := 0; i < 200; i++ {
		got := lowerMapKeys(map[string]int{"Exact_File": 1, "EXACT_FILE": 2, "exact_File": 3, "Other": 4})
		if want := map[string]int{"exact_file": 2, "other": 4}; !reflect.DeepEqual(got, want) {
			t.Fatalf("iteration %d: lowerMapKeys = %v, want %v", i, got, want)
		}
	}
}

// cmd/root.go's default config file is ~/.audiobook-organizer, read with
// SetConfigType("yaml"). filepath.Ext of that returns ".audiobook-organizer",
// so an extension switch alone sent it down the "other format" path: map keys
// came out lower-cased, with a Warn telling the operator to use a YAML file.
func TestViperDecodeJSON_DefaultConfigFileKeepsMapKeyCase(t *testing.T) {
	buf := logtest.Capture(t, slog.LevelWarn)
	defaultFileInitConfig(t, `
plugins:
  MyPlugin:
    enabled: true
    settings:
      ApiKey: abc
dedup:
  embedding_thresholds_by_model:
    BAAI/bge-M3.v1.5:
      high: 0.91
      low: 0.8
`)
	c := Snapshot()
	wantPlugins := map[string]PluginConfig{"MyPlugin": {Enabled: true, Settings: map[string]string{"ApiKey": "abc"}}}
	if !reflect.DeepEqual(c.Plugins, wantPlugins) {
		t.Errorf("Plugins = %+v, want %+v (the default config file must keep map-key case)", c.Plugins, wantPlugins)
	}
	wantModels := map[string]EmbeddingModelThresholds{"BAAI/bge-M3.v1.5": {High: 0.91, Low: 0.8}}
	if !reflect.DeepEqual(c.Dedup.EmbeddingThresholdsByModel, wantModels) {
		t.Errorf("EmbeddingThresholdsByModel = %+v, want %+v", c.Dedup.EmbeddingThresholdsByModel, wantModels)
	}
	if lines := buf.Lines("config: "); len(lines) != 0 {
		t.Errorf("a YAML config file must not draw a config Warn, got %q", lines)
	}
}

// A config file in a format this cannot re-read still warns, once per map
// setting, and still loads viper's lower-cased value.
func TestViperDecodeJSON_NonYAMLConfigFileWarnsAndLowerCases(t *testing.T) {
	buf := logtest.Capture(t, slog.LevelWarn)
	restoreAppConfig(t)
	viper.Reset()
	t.Cleanup(viper.Reset)
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[plugins.MyPlugin]\nenabled = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	viper.SetConfigFile(path)
	if err := viper.ReadInConfig(); err != nil {
		t.Fatalf("ReadInConfig: %v", err)
	}
	InitConfig()
	if _, ok := Snapshot().Plugins["myplugin"]; !ok {
		t.Errorf("Plugins = %+v, want viper's lower-cased myplugin", Snapshot().Plugins)
	}
	if lines := buf.Lines("lower-cased"); len(lines) != 1 || !strings.Contains(lines[0], "plugins") {
		t.Errorf("want exactly one lower-case Warn naming plugins, got %q", lines)
	}
}

// PluginConfig.Settings is map[string]string. YAML's natural `Port: 8080` and
// `Enabled: true` are a number and a bool; strict JSON refused them and the
// whole plugins map was discarded, every other plugin included. viper's own
// decoder converted scalars, so this does too.
func TestViperDecodeJSON_ScalarPluginSettingsAreStringified(t *testing.T) {
	fileInitConfig(t, `
plugins:
  Alpha:
    enabled: true
    settings:
      Port: 8080
      Enabled: true
      Ratio: 0.5
      Name: plain
  Beta:
    enabled: false
`, nil)
	want := map[string]PluginConfig{
		"Alpha": {Enabled: true, Settings: map[string]string{"Port": "8080", "Enabled": "true", "Ratio": "0.5", "Name": "plain"}},
		"Beta":  {Enabled: false},
	}
	if got := Snapshot().Plugins; !reflect.DeepEqual(got, want) {
		t.Fatalf("Plugins = %+v, want %+v", got, want)
	}
}

// The same tolerance applies to an environment JSON value.
func TestViperDecodeJSON_EnvironmentScalarPluginSettingsAreStringified(t *testing.T) {
	t.Setenv("PLUGINS", `{"Alpha":{"enabled":true,"settings":{"Port":8080,"Debug":false}},"Beta":{"enabled":true}}`)
	envInitConfig(t)
	want := map[string]PluginConfig{
		"Alpha": {Enabled: true, Settings: map[string]string{"Port": "8080", "Debug": "false"}},
		"Beta":  {Enabled: true},
	}
	if got := Snapshot().Plugins; !reflect.DeepEqual(got, want) {
		t.Fatalf("Plugins = %+v, want %+v", got, want)
	}
}

// A value that no weak conversion can fix is still dropped and logged.
func TestViperDecodeJSON_UnfixableTypeStillWarnsAndKeepsDefault(t *testing.T) {
	buf := logtest.Capture(t, slog.LevelWarn)
	t.Setenv("PLUGINS", `{"Alpha":{"enabled":{"nested":true}}}`)
	envInitConfig(t)
	if got := Snapshot().Plugins; len(got) != 0 {
		t.Fatalf("Plugins = %+v, want the empty default", got)
	}
	if lines := buf.Lines("ignoring plugins"); len(lines) != 1 {
		t.Fatalf("want one 'ignoring plugins' Warn, got %q", lines)
	}
}

// A flat dotted top-level key is a valid spelling viper finds by prefix. Its
// map keys keep their case too: the lookup tries the dotted remainder as one key.
func TestViperDecodeJSON_FlatDottedConfigKeyKeepsMapKeyCase(t *testing.T) {
	fileInitConfig(t, `
dedup.embedding_thresholds_by_model:
  BAAI/Bge:
    high: 0.9
    low: 0.7
`, nil)
	want := map[string]EmbeddingModelThresholds{"BAAI/Bge": {High: 0.9, Low: 0.7}}
	if got := Snapshot().Dedup.EmbeddingThresholdsByModel; !reflect.DeepEqual(got, want) {
		t.Fatalf("EmbeddingThresholdsByModel = %+v, want %+v", got, want)
	}
}

// Half flat, half nested: dedup.signals: {confidence: ...} under a flat "dedup" prefix.
func TestWalkConfigKey_PrefersLongestKeyAndFallsBack(t *testing.T) {
	root := map[string]any{
		"a.b": map[string]any{"c": 1},
		"A":   map[string]any{"b": map[string]any{"c": 2}, "x": 3},
	}
	if v, ok := walkConfigKey(root, []string{"a", "b", "c"}); !ok || v != 1 {
		t.Errorf("a.b.c = %v, %v; want the flat a.b key's 1 (longest prefix first)", v, ok)
	}
	if v, ok := walkConfigKey(root, []string{"a", "x"}); !ok || v != 3 {
		t.Errorf("a.x = %v, %v; want 3 via the nested A map after the flat prefix misses", v, ok)
	}
	if _, ok := walkConfigKey(root, []string{"a", "nope"}); ok {
		t.Error("a.nope must not be found")
	}
}
