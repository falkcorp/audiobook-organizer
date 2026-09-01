// file: internal/watcher/audio_extensions_test.go
// version: 1.0.0
// guid: b4d363f2-b451-456e-9d03-871a8af40414
// last-edited: 2026-09-01

package watcher

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
)

// This test binary never calls config.InitConfig, so AppConfig.SupportedExtensions
// is nil throughout — the fail-open path. That is deliberate: it is the state
// the watcher would be in if it ever started before config init, and the
// consequence of getting it wrong is that the watcher silently stops watching.
func TestIsAudioFileFollowsTheConfiguredExtensions(t *testing.T) {
	// The extensions the watcher's old private list did not know about. A
	// fixture that only used .mp3 could not observe this at all: .mp3 was in
	// both lists.
	widened := []string{
		"/library/Author/Book.aax",
		"/library/Author/Book.aaxc",
		"/library/Author/Book.aiff",
		"/library/Author/Book.aif",
		"/library/Author/Book.mka",
		"/library/Author/Book.oga",
		"/library/Author/Book.wav",
	}
	for _, p := range widened {
		if !IsAudioFile(p) {
			t.Errorf("IsAudioFile(%q) = false; the watcher would never rescan for this file", p)
		}
	}

	// Still true for what it always knew.
	for _, p := range []string{"a.mp3", "b.M4B", "c.flac"} {
		if !IsAudioFile(p) {
			t.Errorf("IsAudioFile(%q) = false", p)
		}
	}

	// Still false for non-audio.
	for _, p := range []string{"", "cover.jpg", "metadata.json", "Author/Book", "trailer.mp4"} {
		if IsAudioFile(p) {
			t.Errorf("IsAudioFile(%q) = true", p)
		}
	}
}

// When a user narrows supported_extensions, the watcher must narrow with it —
// otherwise the setting is decorative for this subsystem, which was the whole
// defect.
func TestIsAudioFileNarrowsWithTheConfig(t *testing.T) {
	prev := config.Snapshot()
	config.Mutate(func(c *config.Config) { c.SupportedExtensions = []string{".mp3"} })
	t.Cleanup(func() { config.Mutate(func(c *config.Config) { *c = prev }) })

	if !IsAudioFile("a.mp3") {
		t.Error("IsAudioFile(a.mp3) = false with .mp3 configured")
	}
	if IsAudioFile("a.aax") {
		t.Error("IsAudioFile(a.aax) = true even though the user configured only .mp3")
	}
}
