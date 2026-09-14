// file: internal/audiobooks/revert_tag_realfile_test.go
// version: 1.2.0
// guid: 5d2b8e46-1f7a-4c93-b0e5-8a6c3d9f2e17
// last-edited: 2026-09-13

package audiobooks

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/fileops"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
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

// The organize tag write through the service NewRenameService wires, then its
// undo: every tag_write row must name a value the file really holds after the
// write, and reverting them all must put the file back as it was. The organize
// write went through the write-back map, which ignored album_artist and
// composer (their rows claimed writes that never happened) and fanned artist
// out to ALBUMARTIST and a blank COMPOSER that no row recorded.
func TestOrganizeTagWrite_RealFile_RowsMatchTheFileAndUndoRestoresIt(t *testing.T) {
	store := newRevertPebble(t)
	orig := map[string][]string{
		"TITLE":       {"Orig Title"},
		"ARTIST":      {"Orig Artist"},
		"ALBUMARTIST": {"Orig AA"},
		"COMPOSER":    {"Orig Composer"},
		"GENRE":       {"Audiobook"},
	}
	path := makeRevertTestAudio(t, orig)
	svc := NewRenameService(store)
	meta := svc.BuildTagMetadata(&database.Book{Title: "New Title"}, "New Author", "The Narrator")

	filtered := svc.FilterUnchangedTags(path, meta)
	assert.NotContains(t, filtered, "genre", "an unchanged tag is not written")
	current, err := svc.ReadCurrentTags(path)
	require.NoError(t, err)
	write := svc.WriteTags
	if write == nil { // the organizer's own default
		write = func(p string, m map[string]any) error {
			return metadata.WriteMetadataToFile(p, m, fileops.OperationConfig{VerifyChecksums: true})
		}
	}
	require.NoError(t, write(path, filtered))

	after, err := metadata.ReadTagProperties(path)
	require.NoError(t, err)
	assert.Equal(t, "Orig AA", after["album_artist"], "organize never writes ALBUMARTIST")
	assert.Equal(t, "Orig Composer", after["composer"], "organize never writes COMPOSER")
	var rows [][3]string
	for k, v := range filtered {
		assert.Equal(t, fmt.Sprint(v), after[k], "the %s row records %q; the file must hold it", k, v)
		old, known := current[k]
		if known && old == "" {
			old = undo.TagAbsentValue
		}
		rows = append(rows, [3]string{k, old, fmt.Sprint(v)})
	}
	realTagBook(t, store, "op-organize-roundtrip", path, rows...)

	res, err := NewRevertService(store).RevertOperation("op-organize-roundtrip")
	require.NoError(t, err, "result %+v", res)
	assert.Equal(t, len(rows), res.Restored, "result %+v", res)
	tags := diskTags(t, path)
	for k, v := range orig {
		assert.Equal(t, v, tags[k], "%s after undo", k)
	}
	assert.NotContains(t, tags, "ALBUM", "organize added ALBUM; undo removes it")
}

// After organize, the file reader must read the book's author back as the
// author. Its author priority is ALBUMARTIST > ARTIST > COMPOSER, so an
// organize that put the narrator into ALBUMARTIST made the next scan take the
// narrator as the author. A COMPOSER the owner set is left alone.
func TestOrganizeTagWrite_RealFile_AuthorReadsBackAsAuthor(t *testing.T) {
	store := newRevertPebble(t)
	path := makeRevertTestAudio(t, map[string][]string{
		"TITLE":    {"Orig Title"},
		"ARTIST":   {"Orig Artist"},
		"COMPOSER": {"Owner Composer"},
	})
	svc := NewRenameService(store)
	meta := svc.BuildTagMetadata(&database.Book{Title: "New Title"}, "New Author", "The Narrator")
	assert.NotContains(t, meta, "album_artist")
	assert.NotContains(t, meta, "composer")

	filtered := svc.FilterUnchangedTags(path, meta)
	write := svc.WriteTags
	if write == nil {
		write = func(p string, m map[string]any) error {
			return metadata.WriteMetadataToFile(p, m, fileops.OperationConfig{VerifyChecksums: true})
		}
	}
	require.NoError(t, write(path, filtered))

	md, err := metadata.ExtractMetadata(path, nil)
	require.NoError(t, err)
	assert.Equal(t, "New Author", md.Artist, "the author read back after organize")
	tags := diskTags(t, path)
	assert.Equal(t, []string{"Owner Composer"}, tags["COMPOSER"], "organize leaves COMPOSER alone")
	assert.NotContains(t, tags, "ALBUMARTIST", "organize does not add ALBUMARTIST")
	assert.Equal(t, []string{"The Narrator"}, tags["NARRATOR"])
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
