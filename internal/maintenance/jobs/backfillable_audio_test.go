// file: internal/maintenance/jobs/backfillable_audio_test.go
// version: 1.0.0
// guid: d1055064-d894-46cb-85fe-8b7af9e5e5b2
// last-edited: 2026-09-01

package jobs

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
)

// backfill-book-files is the job that creates the book_file rows a book needs
// before anything can find its audio. Its private 8-extension list meant a
// library holding .aax/.aaxc/.aiff/.aif/.mka/.oga/.wav books got no rows for
// them at all — the same end state as the 12,525-book book_file gap, reached
// by a different route, and just as silent.
func TestIsBackfillableAudioFileFollowsTheConfiguredExtensions(t *testing.T) {
	for _, p := range []string{
		"/library/A/Book.aax",
		"/library/A/Book.aaxc",
		"/library/A/Book.aiff",
		"/library/A/Book.aif",
		"/library/A/Book.mka",
		"/library/A/Book.oga",
		"/library/A/Book.wav",
		"/library/A/Book.m4b",
		"/library/A/Book.MP3",
	} {
		if !isBackfillableAudioFile(p) {
			t.Errorf("isBackfillableAudioFile(%q) = false; that book gets no book_file rows", p)
		}
	}
	for _, p := range []string{"", "/library/A/cover.jpg", "/library/A/Book", "/library/A/trailer.mp4"} {
		if isBackfillableAudioFile(p) {
			t.Errorf("isBackfillableAudioFile(%q) = true", p)
		}
	}
}

func TestIsBackfillableAudioFileNarrowsWithTheConfig(t *testing.T) {
	prev := config.Snapshot()
	config.Mutate(func(c *config.Config) { c.SupportedExtensions = []string{".m4b"} })
	t.Cleanup(func() { config.Mutate(func(c *config.Config) { *c = prev }) })

	if !isBackfillableAudioFile("a.m4b") {
		t.Error("configured .m4b rejected")
	}
	if isBackfillableAudioFile("a.aax") {
		t.Error("unconfigured .aax accepted")
	}
}
