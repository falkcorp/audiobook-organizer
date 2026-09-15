// file: internal/audiobooks/revert_tag_realfile_test.go
// version: 1.3.0
// guid: 5d2b8e46-1f7a-4c93-b0e5-8a6c3d9f2e17
// last-edited: 2026-09-14

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
	return makeRevertTestAudioExt(t, "m4a", props)
}

// revertTestCodecs maps each container the real-file tests cover to the
// ffmpeg encoder that produces it. Album Artist is aART in m4a, TPE2 in mp3
// and ALBUMARTIST in flac; TagLib exposes all three as ALBUMARTIST.
var revertTestCodecs = map[string]string{"m4a": "aac", "mp3": "libmp3lame", "flac": "flac"}

// makeRevertTestAudioExt synthesizes a real ext file with ffmpeg and writes
// props to it. Skips when ffmpeg or its encoder for ext is unavailable.
func makeRevertTestAudioExt(t *testing.T, ext string, props map[string][]string) string {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not available; skipping real-file tag revert test")
	}
	path := filepath.Join(t.TempDir(), "book."+ext)
	out, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "anullsrc=r=44100:cl=mono", "-t", "1",
		"-c:a", revertTestCodecs[ext], path).CombinedOutput()
	if err != nil {
		t.Skipf("ffmpeg cannot encode %s here: %v: %s", ext, err, out)
	}
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
	cases := map[string]map[string][]string{
		"ALBUMARTIST set before": {
			"TITLE":       {"Orig Title"},
			"ARTIST":      {"Orig Artist"},
			"ALBUMARTIST": {"Orig AA"},
			"COMPOSER":    {"Orig Composer"},
			"GENRE":       {"Audiobook"},
		},
		"ALBUMARTIST absent before": {
			"TITLE":    {"Orig Title"},
			"ARTIST":   {"Orig Artist"},
			"COMPOSER": {"Orig Composer"},
			"GENRE":    {"Audiobook"},
		},
	}
	for name, orig := range cases {
		for _, ext := range []string{"m4a", "mp3", "flac"} {
			t.Run(name+"/"+ext, func(t *testing.T) {
				organizeRoundTrip(t, ext, orig)
			})
		}
	}
}

// organizeRoundTrip runs the organize tag write on a real ext file carrying
// orig, records its rows the way the organizer does, reverts them and checks
// the file is back as it was.
func organizeRoundTrip(t *testing.T, ext string, orig map[string][]string) {
	t.Helper()
	store := newRevertPebble(t)
	path := makeRevertTestAudioExt(t, ext, orig)
	rows := organizeAndRecord(t, store, path, "op-organize-roundtrip")

	res, err := NewRevertService(store).RevertOperation("op-organize-roundtrip")
	require.NoError(t, err, "result %+v", res)
	assert.Equal(t, rows, res.Restored, "result %+v", res)
	tags := diskTags(t, path)
	for k, v := range orig {
		assert.Equal(t, v, tags[k], "%s after undo", k)
	}
	if _, had := orig["ALBUMARTIST"]; !had {
		assert.NotContains(t, tags, "ALBUMARTIST", "undo removes the ALBUMARTIST organize added")
	}
	assert.NotContains(t, tags, "ALBUM", "organize added ALBUM; undo removes it")
}

// organizeAndRecord runs the organize tag write NewRenameService wires on path
// ("New Title" by "New Author", read by "The Narrator"), records one tag_write
// row per written key under op the way the organizer does, and returns the
// number of rows.
func organizeAndRecord(t *testing.T, store *database.PebbleStore, path, op string) int {
	t.Helper()
	svc := NewRenameService(store)
	meta := svc.BuildTagMetadata(&database.Book{Title: "New Title"}, "New Author", "The Narrator")

	filtered := svc.FilterUnchangedTags(path, meta)
	current, err := svc.ReadCurrentTags(path)
	require.NoError(t, err)
	if current["genre"] == "Audiobook" {
		assert.NotContains(t, filtered, "genre", "an unchanged tag is not written")
	}
	write := svc.WriteTags
	if write == nil { // the organizer's own default
		write = func(p string, m map[string]any) error {
			return metadata.WriteMetadataToFile(p, m, fileops.OperationConfig{VerifyChecksums: true})
		}
	}
	require.NoError(t, write(path, filtered))

	written := diskTags(t, path)
	assert.Equal(t, []string{"New Author"}, written["ALBUMARTIST"], "organize writes ALBUMARTIST = author")
	assert.Equal(t, []string{"New Author"}, written["ARTIST"])
	after, err := metadata.ReadTagValues(path)
	require.NoError(t, err)
	var rows [][3]string
	for k, v := range filtered {
		assert.Equal(t, fmt.Sprint(v), after[k], "the %s row records %q; the file must hold it", k, v)
		old, known := current[k]
		require.True(t, known, "the pre-write value of %s must be recorded", k)
		if old == "" {
			old = undo.TagAbsentValue
		}
		rows = append(rows, [3]string{k, old, fmt.Sprint(v)})
	}
	realTagBook(t, store, op, path, rows...)
	return len(rows)
}

// Another tool changed ALBUMARTIST after the organize. The undo restores
// ARTIST (still what organize wrote) and keeps the later ALBUMARTIST, instead
// of refusing the whole artist row.
func TestRevertTagWrite_RealFile_AlbumArtistChangedSinceIsKeptArtistRestored(t *testing.T) {
	store := newRevertPebble(t)
	path := makeRevertTestAudio(t, map[string][]string{
		"TITLE": {"Orig Title"}, "ARTIST": {"Orig Artist"}, "ALBUMARTIST": {"Orig AA"},
	})
	rows := organizeAndRecord(t, store, path, "op-aa-drift")
	require.NoError(t, taglib.WriteTags(path, map[string][]string{"ALBUMARTIST": {"Later AA"}}, 0))

	res, err := NewRevertService(store).RevertOperation("op-aa-drift")
	require.NoError(t, err, "result %+v", res)
	assert.Equal(t, rows, res.Restored, "the artist row is restored in part, not refused: %+v", res)
	assert.Equal(t, 1, res.PartiallyRestored, "result %+v", res)
	require.Len(t, res.Kept, 1)
	assert.Contains(t, res.Kept[0], "kept ALBUMARTIST")
	tags := diskTags(t, path)
	assert.Equal(t, []string{"Orig Artist"}, tags["ARTIST"])
	assert.Equal(t, []string{"Later AA"}, tags["ALBUMARTIST"], "the later ALBUMARTIST is kept")
	assert.Equal(t, []string{"Orig Title"}, tags["TITLE"])
}

// A snapshot row whose file already holds the pre-organize values counts as
// restored: here a second operation carries the same rows as one already
// reverted, including an ALBUMARTIST that was absent before.
func TestRevertTagWrite_RealFile_AlreadyRestoredSnapshotCountsRestored(t *testing.T) {
	for name, orig := range map[string]map[string][]string{
		"ALBUMARTIST absent before": {"TITLE": {"Orig Title"}, "ARTIST": {"Orig Artist"}},
		"ALBUMARTIST set before":    {"TITLE": {"Orig Title"}, "ARTIST": {"Orig Artist"}, "ALBUMARTIST": {"Orig AA"}},
	} {
		t.Run(name, func(t *testing.T) {
			store := newRevertPebble(t)
			path := makeRevertTestAudio(t, orig)
			rows := organizeAndRecord(t, store, path, "op-first")
			changes, err := store.GetOperationChanges("op-first")
			require.NoError(t, err)
			var again [][3]string
			for _, c := range changes {
				tag, _, _ := undo.TagWriteFromField(c.FieldName)
				again = append(again, [3]string{tag, c.OldValue, c.NewValue})
			}
			realTagBook(t, store, "op-second", path, again...)

			res, err := NewRevertService(store).RevertOperation("op-first")
			require.NoError(t, err, "result %+v", res)
			require.Equal(t, rows, res.Restored)
			res, err = NewRevertService(store).RevertOperation("op-second")
			require.NoError(t, err, "result %+v", res)
			assert.Equal(t, rows, res.Restored, "already restored, not drift: %+v", res)
			assert.Zero(t, res.ChangedSince)
		})
	}
}

// Organize never writes COMPOSER, so its undo never writes COMPOSER either: a
// COMPOSER edited after the organize survives the undo.
func TestRevertTagWrite_RealFile_UndoLeavesLaterComposer(t *testing.T) {
	store := newRevertPebble(t)
	path := makeRevertTestAudio(t, map[string][]string{
		"TITLE": {"Orig Title"}, "ARTIST": {"Orig Artist"}, "COMPOSER": {"Orig Composer"},
	})
	organizeAndRecord(t, store, path, "op-composer")
	require.NoError(t, taglib.WriteTags(path, map[string][]string{"COMPOSER": {"Later Composer"}}, 0))

	res, err := NewRevertService(store).RevertOperation("op-composer")
	require.NoError(t, err, "result %+v", res)
	tags := diskTags(t, path)
	assert.Equal(t, []string{"Later Composer"}, tags["COMPOSER"], "undo must not write COMPOSER back")
	assert.Equal(t, []string{"Orig Artist"}, tags["ARTIST"])
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
	assert.Equal(t, []string{"New Author"}, tags["ALBUMARTIST"], "organize writes ALBUMARTIST = author")
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

// A plain (pre-snapshot) artist row, from an organize that wrote ARTIST only,
// reverts ARTIST only. The write map for a lone artist key also set ALBUMARTIST
// and wrote COMPOSER="", which erased the narrator.
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
