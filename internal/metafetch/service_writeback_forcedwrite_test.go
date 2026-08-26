// file: internal/metafetch/service_writeback_forcedwrite_test.go
// version: 1.0.0
// guid: 5b91d2c7-3a48-4e60-9f15-8c27a04be3d1
// last-edited: 2026-08-16

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

// makeTestAudioFormat synthesizes a real audio file in the requested container.
// The write-back round trip is container-sensitive — mp3 carries ID3 frames and
// m4a carries MP4 atoms — so a tag that round-trips in one can be dropped in the
// other, and testing only one format hides exactly that.
func makeTestAudioFormat(t *testing.T, name, codec string) string {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not available; skipping real-file write-back test")
	}
	path := filepath.Join(t.TempDir(), name)
	cmd := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "anullsrc=r=44100:cl=mono", "-t", "1",
		"-c:a", codec, path)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "ffmpeg failed to synthesize test audio: %s", out)
	require.FileExists(t, path)
	return path
}

// TestWriteBack_SecondPassIsANoOp is the round-trip contract that keeps the
// library from rewriting itself forever.
//
// FilterUnchangedTags only skips a write when the value it wants to write equals
// the value read back off disk. That makes it a contract between two independent
// components: the writer must emit a tag under a name the extractor looks for.
// If it doesn't, the comparison sees "missing", force-writes, and the NEXT run
// sees "missing" again — the write succeeds every time and the loop never ends.
// A one-sided test of either half passes while this happens.
//
// Measured in production on 2026-08-16, over a ten-minute window of one batch:
//
//	publisher     1289 forced writes
//	isbn13         942
//	series_index   608
//	track          413
//
// Each forced write is a full copy-and-swap of the audio file on network
// storage, which is where "applying cached metadata is slow" actually comes
// from. A file confirmed by ffprobe to carry TAG:PUBLISHER was still reported as
// having no publisher by the comparison on the very next pass.
func TestWriteBack_SecondPassIsANoOp(t *testing.T) {
	for _, tc := range []struct{ name, file, codec string }{
		{"m4a", "roundtrip.m4a", "aac"},
		{"mp3", "roundtrip.mp3", "libmp3lame"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := makeTestAudioFormat(t, tc.file, tc.codec)

			tagMap := map[string]any{
				"title":        "01 - The Long Way Home",
				"album":        "The Long Way Home",
				"artist":       "Test Author",
				"genre":        "Audiobook",
				"track":        "1/12",
				"publisher":    "Podium Audio",
				"isbn13":       "9781774244029",
				"series_index": "4",
			}

			_, _, err := fileops.WriteTagsSafe(path, func(tmpPath string) error {
				return metadata.WriteMetadataToFileInPlace(tmpPath, tagMap, fileops.OperationConfig{})
			}, fileops.WriteTagsSafeOptions{})
			require.NoError(t, err, "first write must succeed")
			require.FileExists(t, path)

			// The whole point: having just written these values, a second pass
			// must find nothing left to do. Any key still present here is one
			// that will be rewritten on every run for the life of the file.
			remaining := FilterUnchangedTags(path, tagMap)

			if len(remaining) > 0 {
				keys := make([]string, 0, len(remaining))
				for k := range remaining {
					keys = append(keys, k)
				}
				cur, exErr := metadata.ExtractMetadata(path, nil)
				t.Logf("read-back after write: publisher=%q isbn13=%q seriesIndex=%d track=%d/%d err=%v",
					cur.Publisher, cur.ISBN13, cur.SeriesIndex, cur.TrackNumber, cur.TrackTotal, exErr)
				assert.Empty(t, keys,
					"these keys did not survive the write→read round trip, so every run rewrites the file")
			}
		})
	}
}

// TestWriteBack_ChangedValueStillWrites is the counterweight. A filter that
// simply returned "nothing to do" would pass the test above while silently
// dropping real edits, so pin that a genuine change is still written.
func TestWriteBack_ChangedValueStillWrites(t *testing.T) {
	path := makeTestAudioFormat(t, "changed.m4a", "aac")

	initial := map[string]any{
		"title":     "01 - The Long Way Home",
		"album":     "The Long Way Home",
		"publisher": "Podium Audio",
	}
	_, _, err := fileops.WriteTagsSafe(path, func(tmpPath string) error {
		return metadata.WriteMetadataToFileInPlace(tmpPath, initial, fileops.OperationConfig{})
	}, fileops.WriteTagsSafeOptions{})
	require.NoError(t, err)

	changed := map[string]any{
		"title":     "01 - The Long Way Home",
		"album":     "The Long Way Home",
		"publisher": "Tantor Audio", // genuinely different
	}
	remaining := FilterUnchangedTags(path, changed)
	assert.Contains(t, remaining, "publisher",
		"a real change must survive the filter, or edits are silently dropped")

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("file vanished: %v", err)
	}
}
