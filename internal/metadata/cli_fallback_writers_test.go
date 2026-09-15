// file: internal/metadata/cli_fallback_writers_test.go
// version: 1.0.0
// guid: 3f6a2d81-7c4e-4b19-a0d5-9e8b1c2f4a67
// last-edited: 2026-09-14

package metadata

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/fileops"
)

func requireTools(t *testing.T, tools ...string) {
	t.Helper()
	for _, tool := range tools {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not installed", tool)
		}
	}
}

func makeAudio(t *testing.T, name string, meta ...string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	args := []string{"-v", "error", "-f", "lavfi", "-i", "anullsrc=r=44100:cl=mono", "-t", "1"}
	for _, m := range meta {
		args = append(args, "-metadata", m)
	}
	args = append(args, "-y", p)
	if out, err := exec.Command("ffmpeg", args...).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v %s", err, out)
	}
	return p
}

// The FLAC fallback used to remove TITLE/ARTIST/ALBUM/GENRE/DATE/NARRATOR
// unconditionally, so an author-only write erased the title. It must now
// touch only the tags it sets, write ALBUMARTIST = author, and leave
// COMPOSER alone.
func TestWriteFLACMetadata_AuthorOnlyKeepsOtherTags(t *testing.T) {
	requireTools(t, "ffmpeg", "metaflac")
	p := makeAudio(t, "a.flac", "title=Keep Title", "artist=Old", "album_artist=Old AA", "composer=Owner Composer")

	if err := writeFLACMetadata(p, map[string]any{"artist": "New Author"}, fileops.OperationConfig{}); err != nil {
		t.Fatal(err)
	}
	show := func(tag string) string {
		out, err := exec.Command("metaflac", "--show-tag="+tag, p).Output()
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(out))
	}
	for tag, want := range map[string]string{
		"TITLE":       "TITLE=Keep Title",
		"ARTIST":      "ARTIST=New Author",
		"ALBUMARTIST": "ALBUMARTIST=New Author",
		"COMPOSER":    "COMPOSER=Owner Composer",
	} {
		if got := show(tag); !strings.EqualFold(got, want) {
			t.Errorf("%s: got %q, want %q", tag, got, want)
		}
	}
}

// The M4B fallback used to pass --composer "" on every write.
func TestWriteM4BMetadata_KeepsComposerSetsAlbumArtist(t *testing.T) {
	requireTools(t, "ffmpeg", "AtomicParsley")
	p := makeAudio(t, "a.m4a", "title=Keep Title", "artist=Old", "album_artist=Old AA", "composer=Owner Composer")

	if err := writeM4BMetadata(p, map[string]any{"artist": "New Author"}, fileops.OperationConfig{}); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("AtomicParsley", p, "-t").Output()
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, want := range []string{`"©wrt" contains: Owner Composer`, `"aART" contains: New Author`, `"©ART" contains: New Author`, `"©nam" contains: Keep Title`} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in:\n%s", want, s)
		}
	}
}
