// file: internal/config/supported_extensions_test.go
// version: 1.0.0
// guid: 505e0a00-d587-4090-b366-3eaea1af006c
// last-edited: 2026-09-01

package config

import (
	"strings"
	"testing"

	"github.com/spf13/viper"

	"github.com/falkcorp/audiobook-organizer/internal/audioext"
)

// withSupportedExtensions swaps AppConfig for the duration of a test and
// restores it, so these tests do not leak state into the package's suite.
func withSupportedExtensions(t *testing.T, exts []string) {
	t.Helper()
	prev := Snapshot()
	Mutate(func(c *Config) { c.SupportedExtensions = exts })
	t.Cleanup(func() { Mutate(func(c *Config) { *c = prev }) })
}

// 🔴 The nil case. AppConfig is a package-level zero value, so
// SupportedExtensions is nil in any binary that has not run InitConfig. If
// SupportedExtensionSet returned that nil straight through, every caller —
// the watcher, the relink jobs, the provenance capture — would classify every
// file as "not audio" and do nothing, successfully.
func TestSupportedExtensionSetFailsOpenOnNilConfig(t *testing.T) {
	withSupportedExtensions(t, nil)
	got := SupportedExtensionSet()
	if len(got) == 0 {
		t.Fatal("SupportedExtensionSet() is empty with a nil config; it must fall back " +
			"to the compiled-in audioext.Default list")
	}
	for _, ext := range audioext.Default() {
		if !got.Has(ext) {
			t.Errorf("fallback set is missing %q", ext)
		}
	}
}

// The same hole reached the other way: a user writing `supported_extensions: []`.
func TestSupportedExtensionSetFailsOpenOnEmptyConfig(t *testing.T) {
	withSupportedExtensions(t, []string{})
	if !SupportedExtensionSet().Has(".aax") {
		t.Fatal("an explicitly empty supported_extensions must fall back to the default set")
	}
}

func TestSupportedExtensionSetHonoursAConfiguredList(t *testing.T) {
	withSupportedExtensions(t, []string{".mp3"})
	got := SupportedExtensionSet()
	if !got.Has(".mp3") {
		t.Fatal("configured .mp3 not present")
	}
	if got.Has(".aax") {
		t.Fatal("SupportedExtensionSet() fell back to the default despite a configured list")
	}
}

// The shipped default must be the canonical list, not a copy that can drift.
func TestInitConfigSeedsTheCanonicalDefault(t *testing.T) {
	viper.Reset()
	InitConfig()
	got := Snapshot().SupportedExtensions
	if len(got) != len(audioext.Default()) {
		t.Fatalf("InitConfig seeded %d extensions, want %d",
			len(got), len(audioext.Default()))
	}
	joined := strings.Join(got, " ")
	for _, ext := range []string{".aax", ".aaxc", ".aiff", ".mka", ".oga"} {
		if !strings.Contains(joined, ext) {
			t.Errorf("InitConfig default is missing %q", ext)
		}
	}
}

// 🔴 The viper hole this change closed. `supported_extensions: []` in
// config.yaml makes viper.IsSet true and GetStringSlice empty, so the old
// `if viper.IsSet(...)` guard wrote an empty list into AppConfig. Nothing
// errored; the library simply stopped recognising files. The guard is now
// len > 0, matching what internal/config/persistence.go already did.
func TestInitConfigIgnoresAnExplicitlyEmptyExtensionList(t *testing.T) {
	viper.Reset()
	viper.Set("supported_extensions", []string{})
	InitConfig()
	got := Snapshot().SupportedExtensions
	if len(got) == 0 {
		t.Fatal("an empty supported_extensions was written straight into AppConfig; " +
			"the len > 0 guard is gone")
	}
	if !audioext.NewSet(got).Has(".aax") {
		t.Fatalf("expected the compiled-in default after an empty list, got %v", got)
	}
	viper.Reset()
}

func TestInitConfigHonoursANonEmptyExtensionList(t *testing.T) {
	viper.Reset()
	viper.Set("supported_extensions", []string{".mp3", ".m4b"})
	InitConfig()
	got := Snapshot().SupportedExtensions
	if len(got) != 2 {
		t.Fatalf("expected the 2 configured extensions, got %v", got)
	}
	viper.Reset()
}
