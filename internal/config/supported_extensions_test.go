// file: internal/config/supported_extensions_test.go
// version: 1.0.1
// guid: 505e0a00-d587-4090-b366-3eaea1af006c
// last-edited: 2026-10-12

package config

import (
	"strings"
	"testing"

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
		if !got[ext] {
			t.Errorf("fallback set is missing %q", ext)
		}
	}
}

// The same hole reached the other way: a user writing `supported_extensions: []`.
func TestSupportedExtensionSetFailsOpenOnEmptyConfig(t *testing.T) {
	withSupportedExtensions(t, []string{})
	if !SupportedExtensionSet()[".aax"] {
		t.Fatal("an explicitly empty supported_extensions must fall back to the default set")
	}
}

func TestSupportedExtensionSetHonoursAConfiguredList(t *testing.T) {
	withSupportedExtensions(t, []string{".mp3"})
	got := SupportedExtensionSet()
	if !got[".mp3"] {
		t.Fatal("configured .mp3 not present")
	}
	if got[".aax"] {
		t.Fatal("SupportedExtensionSet() fell back to the default despite a configured list")
	}
}

// The shipped default must be the canonical list, not a copy that can drift.
func TestFactoryDefaultsSeedTheCanonicalExtensions(t *testing.T) {
	got := factoryDefaultConfigForTest().SupportedExtensions
	if len(got) != len(audioext.Default()) {
		t.Fatalf("factory defaults seeded %d extensions, want %d",
			len(got), len(audioext.Default()))
	}
	joined := strings.Join(got, " ")
	for _, ext := range []string{".aax", ".aaxc", ".aiff", ".mka", ".oga"} {
		if !strings.Contains(joined, ext) {
			t.Errorf("factory default is missing %q", ext)
		}
	}
}
