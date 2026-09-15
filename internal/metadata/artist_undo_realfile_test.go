// file: internal/metadata/artist_undo_realfile_test.go
// version: 1.1.0
// guid: 7e2c5a19-3d8b-4f64-b1a0-9c6e2d4f8a37
// last-edited: 2026-09-14

package metadata

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/fileops"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// editThenUndo records the undo value of key the way the organize does
// (ReadTagProperties), edits key through the write-back map, then reverts it
// through WriteTagProperties, and returns the file's raw tags afterwards.
func editThenUndo(t *testing.T, path, key, newValue string) map[string][]string {
	t.Helper()
	before, err := ReadTagProperties(path)
	require.NoError(t, err)
	old, known := before[key]
	require.True(t, known, "the pre-write value of %s must be restorable", key)

	require.NoError(t, WriteMetadataToFile(path, map[string]any{key: newValue}, fileops.OperationConfig{}))
	require.NoError(t, WriteTagProperties(path, map[string]string{key: old}))

	raw, err := readTagsWithTaglib(path)
	require.NoError(t, err)
	return raw
}

// The write-back map writes an author edit to ARTIST and ALBUMARTIST.
// ALBUMARTIST is the author, so an undo that restored ARTIST alone left the new
// author in place. Both come back, and COMPOSER is never touched.
func TestAuthorEditUndo_RestoresArtistAndAlbumArtistLeavesComposer(t *testing.T) {
	path := makeNarratorTestAudio(t, map[string][]string{
		"ARTIST":      {"Old Author"},
		"ALBUMARTIST": {"Old Author"},
		"COMPOSER":    {"Owner Composer"},
	})
	raw := editThenUndo(t, path, "artist", "New Author")
	assert.Equal(t, []string{"Old Author"}, raw["ARTIST"])
	assert.Equal(t, []string{"Old Author"}, raw["ALBUMARTIST"])
	assert.Equal(t, []string{"Owner Composer"}, raw["COMPOSER"])
}

// A file with no ALBUMARTIST before the edit has none after the undo.
func TestAuthorEditUndo_RemovesAlbumArtistAbsentBefore(t *testing.T) {
	path := makeNarratorTestAudio(t, map[string][]string{"ARTIST": {"Old Author"}})
	raw := editThenUndo(t, path, "artist", "New Author")
	assert.Equal(t, []string{"Old Author"}, raw["ARTIST"])
	assert.Empty(t, raw["ALBUMARTIST"])
	assert.Empty(t, raw["COMPOSER"])
}

// ARTIST and ALBUMARTIST legitimately differ (a co-author in ARTIST). Each is
// restored to its own value; the undo does not refuse and does not pick one.
func TestAuthorEditUndo_RestoresDifferingArtistAndAlbumArtistExactly(t *testing.T) {
	path := makeNarratorTestAudio(t, map[string][]string{
		"ARTIST":      {"Co Author"},
		"ALBUMARTIST": {"Main Author"},
	})
	raw := editThenUndo(t, path, "artist", "New Author")
	assert.Equal(t, []string{"Co Author"}, raw["ARTIST"])
	assert.Equal(t, []string{"Main Author"}, raw["ALBUMARTIST"])
}

// The narrator tags can differ too; each is restored to its own value.
func TestNarratorEditUndo_RestoresDifferingNarratorTagsExactly(t *testing.T) {
	path := makeNarratorTestAudio(t, map[string][]string{
		"NARRATOR":  {"Real Narrator"},
		"PERFORMER": {"Someone Else"},
	})
	raw := editThenUndo(t, path, "narrator", "New Narrator")
	assert.Equal(t, []string{"Real Narrator"}, raw["NARRATOR"])
	assert.Equal(t, []string{"Someone Else"}, raw["PERFORMER"])
}

// The write-back map (manual author edit, metafetch write-back) writes the
// author to ARTIST and ALBUMARTIST and never touches COMPOSER. It wrote
// COMPOSER="", erasing whatever the owner kept there.
func TestWriteBackAuthorEdit_LeavesComposer(t *testing.T) {
	for _, ext := range []string{"m4a", "mp3", "flac"} {
		t.Run(ext, func(t *testing.T) {
			path := makeTestAudioExt(t, ext, map[string][]string{
				"ARTIST": {"Old Author"}, "COMPOSER": {"Owner Composer"},
			})
			require.NoError(t, WriteMetadataToFile(path, map[string]any{"artist": "New Author"}, fileops.OperationConfig{}))
			raw, err := readTagsWithTaglib(path)
			require.NoError(t, err)
			assert.Equal(t, []string{"New Author"}, raw["ARTIST"])
			assert.Equal(t, []string{"New Author"}, raw["ALBUMARTIST"], "Album Artist is the author")
			assert.Equal(t, []string{"Owner Composer"}, raw["COMPOSER"])
		})
	}
}

// The author and narrator edit-then-undo round trip on mp3 (TPE2 is Album
// Artist) and flac (ALBUMARTIST), beside the m4a tests above.
func TestEditUndo_RoundTripMp3Flac(t *testing.T) {
	for _, ext := range []string{"mp3", "flac"} {
		t.Run(ext+"/artist", func(t *testing.T) {
			path := makeTestAudioExt(t, ext, map[string][]string{
				"ARTIST": {"Co Author"}, "ALBUMARTIST": {"Main Author"}, "COMPOSER": {"Owner Composer"},
			})
			raw := editThenUndo(t, path, "artist", "New Author")
			assert.Equal(t, []string{"Co Author"}, raw["ARTIST"])
			assert.Equal(t, []string{"Main Author"}, raw["ALBUMARTIST"])
			assert.Equal(t, []string{"Owner Composer"}, raw["COMPOSER"])
		})
		t.Run(ext+"/artist absent album artist", func(t *testing.T) {
			path := makeTestAudioExt(t, ext, map[string][]string{"ARTIST": {"Old Author"}})
			raw := editThenUndo(t, path, "artist", "New Author")
			assert.Equal(t, []string{"Old Author"}, raw["ARTIST"])
			assert.Empty(t, raw["ALBUMARTIST"])
		})
		t.Run(ext+"/narrator", func(t *testing.T) {
			path := makeTestAudioExt(t, ext, map[string][]string{
				"NARRATOR": {"Real Narrator"}, "PERFORMER": {"Someone Else"},
			})
			raw := editThenUndo(t, path, "narrator", "New Narrator")
			assert.Equal(t, []string{"Real Narrator"}, raw["NARRATOR"])
			assert.Equal(t, []string{"Someone Else"}, raw["PERFORMER"])
		})
	}
}

// A snapshot of the artist key records the properties organize writes (ARTIST,
// ALBUMARTIST) and not COMPOSER, so an undo never writes COMPOSER back.
func TestReadTagProperties_ArtistSnapshotExcludesComposer(t *testing.T) {
	path := makeNarratorTestAudio(t, map[string][]string{"ARTIST": {"A"}, "COMPOSER": {"C"}})
	got, err := ReadTagProperties(path)
	require.NoError(t, err)
	snap, err := decodeTagSnapshot(got["artist"])
	require.NoError(t, err)
	assert.NotContains(t, snap, "COMPOSER")
	assert.Contains(t, snap, "ARTIST")
	assert.Contains(t, snap, "ALBUMARTIST")
}
