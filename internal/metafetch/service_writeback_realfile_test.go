// file: internal/metafetch/service_writeback_realfile_test.go
// version: 1.0.0
// guid: 3e5f7a91-8c24-4b6d-a017-5d92c4e8b3f0
// last-edited: 2026-08-15

package metafetch

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/fileops"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// makeTestAudio synthesizes a real, valid audio file with ffmpeg. Skips the test
// when ffmpeg is unavailable rather than failing, so the suite still runs in
// environments without it.
func makeTestAudio(t *testing.T, name string) string {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not available; skipping real-file write-back test")
	}
	path := filepath.Join(t.TempDir(), name)
	cmd := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "anullsrc=r=44100:cl=mono", "-t", "1",
		"-c:a", "aac", path)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "ffmpeg failed to synthesize test audio: %s", out)
	require.FileExists(t, path)
	return path
}

// TestWriteBackRoundTrip_RealFile exercises the ACTUAL write path end to end on
// a real audio file: fileops.WriteTagsSafe wrapping the in-place writer, then a
// read-back, then the unchanged-tag filter.
//
// Everything else in this package's tests runs against constructed Metadata
// structs, which proves the comparison logic but cannot prove that the de-nested
// writer still produces a valid file with the right tags. This closes that gap.
func TestWriteBackRoundTrip_RealFile(t *testing.T) {
	path := makeTestAudio(t, "roundtrip.m4a")

	before, err := os.Stat(path)
	require.NoError(t, err)

	tagMap := map[string]interface{}{
		"title":  "01 - The Long Way Home",
		"album":  "The Long Way Home",
		"artist": "Test Author",
		"genre":  "Audiobook",
		"track":  "1/12",
	}

	// This is exactly the shape the multi-file write path uses: an outer
	// WriteTagsSafe whose writeFn is the IN-PLACE writer. Before the de-nesting
	// fix the writeFn re-entered WriteTagsSafe, doubling the copies and hashes.
	_, _, err = fileops.WriteTagsSafe(path, func(tmpPath string) error {
		return metadata.WriteMetadataToFileInPlace(tmpPath, tagMap, fileops.OperationConfig{})
	}, fileops.WriteTagsSafeOptions{})
	require.NoError(t, err, "the de-nested write path must succeed on a real file")

	// The file must still be a valid, readable audio file — a de-nesting bug
	// could plausibly leave a truncated or unrenamed temp file behind.
	after, err := os.Stat(path)
	require.NoError(t, err, "original path must still exist after the atomic rename")
	assert.Greater(t, after.Size(), int64(0), "file must not be truncated")
	assert.Equal(t, before.Mode().Perm(), after.Mode().Perm(),
		"permissions must survive the copy+rename (an 0600 regression once locked out every non-owner reader)")

	// Tags must actually have landed.
	got, err := metadata.ExtractMetadata(path, nil)
	require.NoError(t, err, "written file must be readable")
	assert.Equal(t, "01 - The Long Way Home", got.Title)
	assert.Equal(t, "The Long Way Home", got.Album)
	assert.Equal(t, "Test Author", got.Artist)
	assert.Equal(t, 1, got.TrackNumber, "track number must round-trip")

	// THE payoff: re-filtering the same tag map against the file we just wrote
	// must come back empty, i.e. a second write-back run skips this file
	// entirely. This is the behaviour that was impossible before the track fix —
	// len(tagMap) == 0 was unreachable for any multi-file book, so every run
	// rewrote every file forever.
	filtered := FilterUnchangedTags(path, tagMap)
	assert.Empty(t, filtered,
		"a second write-back over unchanged tags must filter to zero and skip the file; "+
			"got %v", filtered)
}

// TestWriteBackRoundTrip_RealFile_ChangedTagStillWrites is the negative control.
// Without it, a filter that simply dropped everything would pass the test above.
func TestWriteBackRoundTrip_RealFile_ChangedTagStillWrites(t *testing.T) {
	path := makeTestAudio(t, "changed.m4a")

	initial := map[string]interface{}{
		"title":  "01 - Original",
		"album":  "Original Album",
		"artist": "Test Author",
		"track":  "1/12",
	}
	_, _, err := fileops.WriteTagsSafe(path, func(tmpPath string) error {
		return metadata.WriteMetadataToFileInPlace(tmpPath, initial, fileops.OperationConfig{})
	}, fileops.WriteTagsSafeOptions{})
	require.NoError(t, err)

	// Same file, but the album genuinely changed.
	changed := map[string]interface{}{
		"title":  "01 - Original",   // unchanged
		"album":  "Corrected Album", // CHANGED
		"artist": "Test Author",     // unchanged
		"track":  "1/12",            // unchanged
	}

	filtered := FilterUnchangedTags(path, changed)

	assert.Contains(t, filtered, "album", "a genuinely changed tag must still be written")
	assert.NotContains(t, filtered, "track", "an unchanged track must not force a write")
	assert.NotContains(t, filtered, "artist", "an unchanged artist must not force a write")
}
