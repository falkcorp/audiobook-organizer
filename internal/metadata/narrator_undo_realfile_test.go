// file: internal/metadata/narrator_undo_realfile_test.go
// version: 1.0.0
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
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not available; skipping real-file narrator undo test")
	}
	path := filepath.Join(t.TempDir(), "book.m4a")
	out, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "anullsrc=r=44100:cl=mono", "-t", "1",
		"-c:a", "aac", path).CombinedOutput()
	require.NoError(t, err, "ffmpeg: %s", out)
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
	require.Equal(t, "Old Narrator", before["narrator"])

	require.NoError(t, WriteMetadataToFile(path, map[string]any{"narrator": "New Narrator"}, fileops.OperationConfig{}))
	edited, err := ReadTagProperties(path)
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
	require.Equal(t, "", before["narrator"])

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
	require.Equal(t, "Old Narrator", old)

	require.NoError(t, WriteMetadataToFile(path, map[string]any{"narrator": "New Narrator"}, fileops.OperationConfig{}))
	require.NoError(t, WriteTagProperties(path, map[string]string{"narrator": old}))

	raw, err := readTagsWithTaglib(path)
	require.NoError(t, err)
	assert.Equal(t, []string{"Old Narrator"}, raw["PERFORMER"])
	m, err := ExtractMetadata(path, nil)
	require.NoError(t, err)
	assert.Equal(t, "Old Narrator", m.Narrator)
}

// Two properties holding different values cannot be put back from one string,
// so the key reads as unknown (left out) rather than as either value.
func TestReadTagProperties_DisagreeingNarratorPropertiesAreUnknown(t *testing.T) {
	path := makeNarratorTestAudio(t, map[string][]string{
		"NARRATOR":  {"Real Narrator"},
		"PERFORMER": {"Someone Else"},
	})
	got, err := ReadTagProperties(path)
	require.NoError(t, err)
	_, known := got["narrator"]
	assert.False(t, known)
}
