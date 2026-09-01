// file: internal/metadata/unknown_author_directory_consumer_test.go
// version: 1.0.0
// guid: c47a1d95-3e82-4b60-a7f1-90d5e6c83b24
// last-edited: 2026-09-01

package metadata

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/authorname"
)

// TestExtractMetadataNeverReturnsThePlaceholderAsArtist pins the CONSUMER, not
// the helper.
//
// Why this test exists, precisely: collapsing the two path->author parsers into
// internal/authorname changed this package's helper on exactly one input. A
// 28-path differential corpus showed metadata's old copy returned
// "Unknown Author" for a file directly inside a directory of that name, where
// scanner's copy returned "" (its skipDirs carried the placeholder). Every other
// path in the corpus agreed, before and after.
//
// The claim that the change is invisible to callers rests on ExtractMetadata
// clearing the placeholder six lines below the assignment. That is straight-line
// code and reads conclusively -- but reading a gate and asserting about the
// consumer is exactly how #3029 shipped 886 bad author strings. So this asserts
// on ExtractMetadata's OUTPUT.
//
// It is deliberately an INVARIANT, not a change-detector: it passes on both
// sides of the refactor. That is the point. If a future change moves the
// placeholder clear, reorders the defer, or drops the skipDirs entry, this fails
// regardless of which of those it was.
func TestExtractMetadataNeverReturnsThePlaceholderAsArtist(t *testing.T) {
	cases := []struct {
		name string
		dir  string
	}{
		{"bare placeholder directory", authorname.Placeholder},
		{"lowercased by a filesystem round trip", "unknown author"},
		{"decorated with an edition suffix", authorname.Placeholder + " (Unabridged)"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// A real file is required: ExtractMetadata stats the path and
			// returns early for anything it cannot open. The CONTENT is
			// irrelevant -- tag extraction finds nothing in it, which is what
			// drives execution into the directory-name fallback under test.
			root := t.TempDir()
			dir := filepath.Join(root, tc.dir)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			file := filepath.Join(dir, "Mort.mp3")
			if err := os.WriteFile(file, []byte("not audio"), 0o644); err != nil {
				t.Fatalf("write: %v", err)
			}

			got, err := ExtractMetadata(file, nil)
			if err != nil {
				t.Fatalf("ExtractMetadata(%q) error = %v", file, err)
			}

			if got.Artist != "" {
				t.Errorf("ExtractMetadata under %q: Artist = %q, want \"\"\n"+
					"the organizer's own placeholder directory was read back as a real author; "+
					"that closes the AI nomination gate and nothing downstream can recognise it as junk",
					tc.dir, got.Artist)
			}
		})
	}
}

// TestExtractMetadataStillReadsARealAuthorDirectory is the known-good twin.
//
// The test above only ever asserts on an EMPTY result, so on its own it passes
// just as happily against a directory fallback that has been broken to return ""
// for everything. This one fails in that case. A bogus-value check needs a
// known-good control or it is not an instrument.
func TestExtractMetadataStillReadsARealAuthorDirectory(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "Terry Pratchett")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	file := filepath.Join(dir, "Mort.mp3")
	if err := os.WriteFile(file, []byte("not audio"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	got, err := ExtractMetadata(file, nil)
	if err != nil {
		t.Fatalf("ExtractMetadata error = %v", err)
	}
	if got.Artist != "Terry Pratchett" {
		t.Errorf("Artist = %q, want %q -- the directory fallback is not reaching a real author, "+
			"which would make the placeholder test above vacuous", got.Artist, "Terry Pratchett")
	}
}
