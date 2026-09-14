// file: internal/audiobooks/revert_tag_realfile_test.go
// version: 1.0.0
// guid: 5d2b8e46-1f7a-4c93-b0e5-8a6c3d9f2e17
// last-edited: 2026-09-13

package audiobooks

import (
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	taglib "go.senan.xyz/taglib"
)

// These tests run the revert with its real tag reader and writer on a real
// audio file and read the tags back from disk. The fake tag writer the other
// revert tests use took a single-key map at face value, which hid that the real
// writer dropped an empty value (so an absent tag was never removed) and turned
// one key into several properties (so reverting artist erased COMPOSER).

// makeRevertTestAudio synthesizes a real audio file with ffmpeg. The fixtures
// under testdata/ are Git LFS objects, which CI does not fetch, so they cannot
// be used here. Skips when ffmpeg is unavailable.
func makeRevertTestAudio(t *testing.T, props map[string][]string) string {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not available; skipping real-file tag revert test")
	}
	path := filepath.Join(t.TempDir(), "book.m4a")
	out, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "anullsrc=r=44100:cl=mono", "-t", "1",
		"-c:a", "aac", path).CombinedOutput()
	require.NoError(t, err, "ffmpeg: %s", out)
	require.NoError(t, taglib.WriteTags(path, props, 0))
	return path
}

// realTagBook creates a single-file book at path with one tag_write row per
// entry of rows ({tag, old, new}).
func realTagBook(t *testing.T, store *database.PebbleStore, op, path string, rows ...[3]string) {
	t.Helper()
	book, err := store.CreateBook(&database.Book{Title: "A", FilePath: path, Format: "m4a"})
	require.NoError(t, err)
	require.NoError(t, store.CreateBookFile(&database.BookFile{BookID: book.ID, FilePath: path, Format: "m4a"}))
	files, err := store.GetBookFiles(book.ID)
	require.NoError(t, err)
	require.Len(t, files, 1)
	for _, r := range rows {
		require.NoError(t, store.CreateOperationChange(&database.OperationChange{
			OperationID: op, BookID: book.ID, ChangeType: undo.ChangeTypeTagWrite,
			FieldName: undo.TagWriteField(r[0], files[0].ID), OldValue: r[1], NewValue: r[2],
		}))
	}
}

func diskTags(t *testing.T, path string) map[string][]string {
	t.Helper()
	tags, err := taglib.ReadTags(path)
	require.NoError(t, err)
	return tags
}

// A tag the file did not carry before organize is removed from the file.
func TestRevertTagWrite_RealFile_AbsentTagIsRemoved(t *testing.T) {
	store := newRevertPebble(t)
	path := makeRevertTestAudio(t, map[string][]string{"TITLE": {"T"}, "ALBUM": {"Organized Album"}})
	realTagBook(t, store, "op-absent-real", path, [3]string{"album", undo.TagAbsentValue, "Organized Album"})

	res, err := NewRevertService(store).RevertOperation("op-absent-real")
	require.NoError(t, err, "result %+v", res)

	tags := diskTags(t, path)
	assert.NotContains(t, tags, "ALBUM", "the tag organize added must be gone from the file")
	assert.Equal(t, []string{"T"}, tags["TITLE"], "other tags stay")
	assert.Equal(t, 1, res.Restored, "result %+v", res)
}

// Reverting artist writes ARTIST only. The write map for a lone artist key also
// set ALBUMARTIST and wrote COMPOSER="", which erased the narrator.
func TestRevertTagWrite_RealFile_ArtistKeepsComposerAndAlbum(t *testing.T) {
	store := newRevertPebble(t)
	path := makeRevertTestAudio(t, map[string][]string{
		"ARTIST":      {"Organized Artist"},
		"ALBUMARTIST": {"Organized Artist"},
		"COMPOSER":    {"The Narrator"},
		"ALBUM":       {"The Album"},
	})
	realTagBook(t, store, "op-artist-real", path, [3]string{"artist", "Orig Artist", "Organized Artist"})

	res, err := NewRevertService(store).RevertOperation("op-artist-real")
	require.NoError(t, err, "result %+v", res)

	tags := diskTags(t, path)
	assert.Equal(t, []string{"Orig Artist"}, tags["ARTIST"])
	assert.Equal(t, []string{"The Narrator"}, tags["COMPOSER"], "the narrator in COMPOSER must survive")
	assert.Equal(t, []string{"The Album"}, tags["ALBUM"])
	assert.Equal(t, []string{"Organized Artist"}, tags["ALBUMARTIST"], "ALBUMARTIST has its own row; this one does not touch it")
}

// An album_artist row is written to ALBUMARTIST. The writer ignored the key, so
// the row reported restored while the file kept the organized value.
func TestRevertTagWrite_RealFile_AlbumArtistIsWritten(t *testing.T) {
	store := newRevertPebble(t)
	path := makeRevertTestAudio(t, map[string][]string{"ALBUMARTIST": {"Organized AA"}, "COMPOSER": {"The Narrator"}})
	realTagBook(t, store, "op-aa-real", path, [3]string{"album_artist", "Orig AA", "Organized AA"})

	res, err := NewRevertService(store).RevertOperation("op-aa-real")
	require.NoError(t, err, "result %+v", res)

	tags := diskTags(t, path)
	assert.Equal(t, []string{"Orig AA"}, tags["ALBUMARTIST"])
	assert.Equal(t, []string{"The Narrator"}, tags["COMPOSER"])
	assert.Equal(t, 1, res.Restored, "result %+v", res)
}

// A tag key the revert cannot write to one file property fails the row and
// leaves the file alone; it is never reported restored.
func TestRevertTagWrite_RealFile_UnwritableKeyFails(t *testing.T) {
	store := newRevertPebble(t)
	path := makeRevertTestAudio(t, map[string][]string{"TITLE": {"T"}})
	realTagBook(t, store, "op-unwritable", path, [3]string{"book_id", "old-id", "new-id"})

	// RevertOperation reports a failed row as an error as well as in Failed.
	res, _ := NewRevertService(store).RevertOperation("op-unwritable")
	require.NotNil(t, res)
	assert.Equal(t, 0, res.Restored, "result %+v", res)
	assert.Equal(t, 1, res.Failed, "result %+v", res)
	assert.Equal(t, []string{"T"}, diskTags(t, path)["TITLE"])
}
