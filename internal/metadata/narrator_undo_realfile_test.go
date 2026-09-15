// file: internal/metadata/narrator_undo_realfile_test.go
// version: 1.1.0
// guid: 8c4e1a7d-2b9f-4d63-a0e5-6f1c3b8d9e24
// last-edited: 2026-09-14

package metadata

import (
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/fileops"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	taglib "go.senan.xyz/taglib"
)

// makeNarratorTestAudio synthesizes a real audio file with ffmpeg (the LFS
// fixtures under testdata/ are not fetched by CI). Skips without ffmpeg.
func makeNarratorTestAudio(t *testing.T, props map[string][]string) string {
	t.Helper()
	return makeTestAudioExt(t, "m4a", props)
}

// testAudioCodecs maps each container the real-file tests cover to the ffmpeg
// encoder that produces it.
var testAudioCodecs = map[string]string{"m4a": "aac", "mp3": "libmp3lame", "flac": "flac"}

// makeTestAudioExt synthesizes a real ext file with ffmpeg and writes props to
// it. Skips when ffmpeg or its encoder for ext is unavailable.
func makeTestAudioExt(t *testing.T, ext string, props map[string][]string) string {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not available; skipping real-file tag undo test")
	}
	path := filepath.Join(t.TempDir(), "book."+ext)
	out, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "anullsrc=r=44100:cl=mono", "-t", "1",
		"-c:a", testAudioCodecs[ext], path).CombinedOutput()
	if err != nil {
		t.Skipf("ffmpeg cannot encode %s here: %v: %s", ext, err, out)
	}
	if len(props) > 0 {
		require.NoError(t, taglib.WriteTags(path, props, 0))
	}
	return path
}

// A narrator edit goes through the write-back map, which writes the narrator
// key to both PERFORMER and NARRATOR. The undo reads and writes through the
// tag-property table, so it must restore both properties; restoring NARRATOR
// alone left PERFORMER holding the edited value. The assertions read the raw
// properties, not a reader, so they hold whichever narrator tag a reader
// prefers.
func TestNarratorEditUndo_RestoresEveryNarratorProperty(t *testing.T) {
	path := makeNarratorTestAudio(t, map[string][]string{
		"NARRATOR":  {"Old Narrator"},
		"PERFORMER": {"Old Narrator"},
	})
	before, err := ReadTagProperties(path)
	require.NoError(t, err)

	require.NoError(t, WriteMetadataToFile(path, map[string]any{"narrator": "New Narrator"}, fileops.OperationConfig{}))
	edited, err := ReadTagValues(path)
	require.NoError(t, err)
	require.Equal(t, "New Narrator", edited["narrator"])

	require.NoError(t, WriteTagProperties(path, map[string]string{"narrator": before["narrator"]}))

	raw, err := readTagsWithTaglib(path)
	require.NoError(t, err)
	assert.Equal(t, []string{"Old Narrator"}, raw["NARRATOR"])
	assert.Equal(t, []string{"Old Narrator"}, raw["PERFORMER"])
	m, err := ExtractMetadata(path, nil)
	require.NoError(t, err)
	assert.Equal(t, "Old Narrator", m.Narrator)
}

// A file that carried no narrator before the edit: the undo removes every
// property the edit wrote.
func TestNarratorEditUndo_RemovesPropertiesAbsentBefore(t *testing.T) {
	path := makeNarratorTestAudio(t, map[string][]string{"TITLE": {"Book"}})
	before, err := ReadTagProperties(path)
	require.NoError(t, err)
	require.Contains(t, before, "narrator")

	require.NoError(t, WriteMetadataToFile(path, map[string]any{"narrator": "New Narrator"}, fileops.OperationConfig{}))
	require.NoError(t, WriteTagProperties(path, map[string]string{"narrator": ""}))

	raw, err := readTagsWithTaglib(path)
	require.NoError(t, err)
	assert.Empty(t, raw["NARRATOR"])
	assert.Empty(t, raw["PERFORMER"])
}

// A file carrying only PERFORMER: the absent NARRATOR is no value, so the
// pre-write narrator is known and the undo puts it back where every reader
// finds it.
func TestNarratorEditUndo_PerformerOnlyFileIsRestorable(t *testing.T) {
	path := makeNarratorTestAudio(t, map[string][]string{"PERFORMER": {"Old Narrator"}})
	before, err := ReadTagProperties(path)
	require.NoError(t, err)
	old, known := before["narrator"]
	require.True(t, known, "a PERFORMER-only file must have a restorable pre-write narrator")

	require.NoError(t, WriteMetadataToFile(path, map[string]any{"narrator": "New Narrator"}, fileops.OperationConfig{}))
	require.NoError(t, WriteTagProperties(path, map[string]string{"narrator": old}))

	raw, err := readTagsWithTaglib(path)
	require.NoError(t, err)
	assert.Equal(t, []string{"Old Narrator"}, raw["PERFORMER"])
	m, err := ExtractMetadata(path, nil)
	require.NoError(t, err)
	assert.Equal(t, "Old Narrator", m.Narrator)
}
