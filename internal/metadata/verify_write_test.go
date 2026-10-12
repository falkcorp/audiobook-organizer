// file: internal/metadata/verify_write_test.go
// version: 1.1.0
// guid: 2f6a8d13-4c79-4b05-8e1a-96d0b3f7c524
// last-edited: 2026-10-11

package metadata

import (
	"context"
	"errors"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/fileops"
	"github.com/falkcorp/audiobook-organizer/internal/tagger"
	"github.com/stretchr/testify/require"
)

// These tests run against the writers of the build variant being tested: the
// WASM writers by default, the native (cgo) ones under -tags native_taglib,
// which is what production runs. They use the checked-in testdata/fixtures
// audio, not ffmpeg output, so they run in every gating job.

var fixtureNames = []string{"test_sample.m4b", "test_sample.mp3", "test_sample.flac"}

func setVerifyAfterWrite(t *testing.T, on bool) {
	t.Helper()
	prev := config.Snapshot()
	config.Mutate(func(c *config.Config) { c.VerifyAfterWrite = on })
	t.Cleanup(func() { config.Mutate(func(c *config.Config) { *c = prev }) })
}

func noSafeWriteDeps(t *testing.T) {
	t.Helper()
	prev := packageSafeWriteDeps
	t.Cleanup(func() { packageSafeWriteDeps = prev })
	packageSafeWriteDeps = tagger.SafeWriteDeps{}
}

// A track number an MP4 integer atom cannot hold reads back as 0: a real loss
// that verify_after_write must report. It is the verification failure these
// tests use, since it needs no hook into the writer.
var unrepresentableTrack = map[string]any{"title": "T", "track": "side A"}

// The metafetch/PATCH write-back writes ALBUM:[""] when a series has no album,
// and WriteSingleTag(..., "") clears a tag; taglib treats both as a delete, so
// verification must expect the key to be absent, not "".
func TestWriters_VerifyAfterWrite_EmptyValueDeletesVerify(t *testing.T) {
	setVerifyAfterWrite(t, true)
	noSafeWriteDeps(t)
	ctx := context.Background()
	for _, name := range fixtureNames {
		t.Run(name, func(t *testing.T) {
			path := copyFixture(t, name)
			require.NoError(t, WriteMetadataToFileContext(ctx, path, map[string]any{"title": "T", "series": "S"}, fileops.OperationConfig{}),
				"series without album writes ALBUM:[\"\"]")

			require.NoError(t, WriteSingleTagContext(ctx, path, "COMPOSER", "Someone"))
			require.NoError(t, WriteSingleTagContext(ctx, path, "COMPOSER", ""), "clearing a tag")
			raw, err := readTagsWithTaglib(path)
			require.NoError(t, err)
			require.Empty(t, raw["COMPOSER"])
			require.Equal(t, []string{"S"}, raw["SERIES"])
		})
	}
}

func TestWriters_VerifyAfterWrite_LossyWriteFails(t *testing.T) {
	setVerifyAfterWrite(t, true)
	noSafeWriteDeps(t)
	path := copyFixture(t, "test_sample.m4b")
	err := WriteMetadataToFileContext(context.Background(), path, unrepresentableTrack, fileops.OperationConfig{})
	require.ErrorIs(t, err, tagger.ErrVerifyAfterWrite)
	var vae *tagger.VerifyAfterWriteError
	require.True(t, errors.As(err, &vae))
	require.Equal(t, []string{"TRACKNUMBER"}, vae.Keys)

	// Flag off: the write is not checked.
	setVerifyAfterWrite(t, false)
	require.NoError(t, WriteMetadataToFileContext(context.Background(), copyFixture(t, "test_sample.m4b"), unrepresentableTrack, fileops.OperationConfig{}))
}

// verify_after_write fails a write the file already received. The CLI writers
// (AtomicParsley, ffmpeg) must not then run over a file taglib wrote: the error
// comes back as the verification error, not as whatever the fallback did.
func TestWriteMetadataToFile_VerifyFailureDoesNotFallBackToCLI(t *testing.T) {
	setVerifyAfterWrite(t, true)
	noSafeWriteDeps(t)
	path := copyFixture(t, "test_sample.m4b")
	err := WriteMetadataToFile(path, unrepresentableTrack, fileops.OperationConfig{})
	require.ErrorIs(t, err, tagger.ErrVerifyAfterWrite, "native verification failure must not fall through to the CLI writers")
	// taglib's write stands.
	raw, rerr := readTagsWithTaglib(path)
	require.NoError(t, rerr)
	require.Equal(t, []string{"T"}, raw["TITLE"])
}

// metafetch's write-back runs the tag write inside its own
// fileops.WriteTagsSafe; WriteMetadataToFileAtomic verifies after that rename.
func TestWriteMetadataToFileAtomic_VerifiesAfterRename(t *testing.T) {
	setVerifyAfterWrite(t, true)
	noSafeWriteDeps(t)
	for _, name := range fixtureNames {
		t.Run(name, func(t *testing.T) {
			path := copyFixture(t, name)
			err := WriteMetadataToFileAtomic(path, map[string]any{"title": "Atomic", "series": "S"}, fileops.OperationConfig{}, fileops.WriteTagsSafeOptions{}, tagger.SafeWriteDeps{})
			require.NoError(t, err)
			raw, rerr := readTagsWithTaglib(path)
			require.NoError(t, rerr)
			require.Equal(t, []string{"Atomic"}, raw["TITLE"])
		})
	}

	path := copyFixture(t, "test_sample.m4b")
	err := WriteMetadataToFileAtomic(path, unrepresentableTrack, fileops.OperationConfig{}, fileops.WriteTagsSafeOptions{}, tagger.SafeWriteDeps{})
	require.ErrorIs(t, err, tagger.ErrVerifyAfterWrite)

	setVerifyAfterWrite(t, false)
	require.NoError(t, WriteMetadataToFileAtomic(copyFixture(t, "test_sample.m4b"), unrepresentableTrack, fileops.OperationConfig{}, fileops.WriteTagsSafeOptions{}, tagger.SafeWriteDeps{}))
}

// WriteTagProperties verifies the write itself (one read-back): a property set
// and a property removed both round-trip on every container.
func TestWriteTagProperties_SetsAndRemovesWithOneVerification(t *testing.T) {
	setVerifyAfterWrite(t, true)
	noSafeWriteDeps(t)
	for _, name := range fixtureNames {
		t.Run(name, func(t *testing.T) {
			path := copyFixture(t, name)
			require.NoError(t, WriteTagProperties(path, map[string]string{"narrator": "Reader One", "series": "Saga"}))
			raw, err := readTagsWithTaglib(path)
			require.NoError(t, err)
			require.Equal(t, []string{"Reader One"}, raw["NARRATOR"])
			require.Equal(t, []string{"Reader One"}, raw["PERFORMER"])

			require.NoError(t, WriteTagProperties(path, map[string]string{"narrator": ""}))
			raw, err = readTagsWithTaglib(path)
			require.NoError(t, err)
			require.Empty(t, raw["NARRATOR"])
			require.Equal(t, []string{"Saga"}, raw["SERIES"])
		})
	}
}

// It checks the file even with verify_after_write off: reverts rely on a
// failed removal being an error.
func TestWriteTagProperties_VerifiesWithTheSettingOff(t *testing.T) {
	setVerifyAfterWrite(t, false)
	noSafeWriteDeps(t)
	path := copyFixture(t, "test_sample.m4b")
	require.NoError(t, WriteTagProperties(path, map[string]string{"title": "Still Checked"}))
}

// The full tag map the PATCH and metafetch write-backs send (both spellings of
// author, narrator, publisher and description, series and its grouping, the
// custom AUDIOBOOK_ORGANIZER_* tags) must not read back as a mismatch on any
// container: verify_after_write is on by default, so one false mismatch fails
// every write-back for that container.
func TestWriters_VerifyAfterWrite_FullProductionTagMap(t *testing.T) {
	setVerifyAfterWrite(t, true)
	noSafeWriteDeps(t)
	ctx := context.Background()
	full := map[string]any{
		"title": "T", "artist": "A One", "narrator": "N", "publisher": "P", "year": 2020,
		"album": "Al", "genre": "Audiobook", "track": "3/12", "description": "D", "series": "S",
		"series_index": 2, "language": "en", "isbn13": "9780000000000", "asin": "B000000000",
		"book_id": "book-1", "edition": "First", "print_year": "1999",
	}
	for _, name := range fixtureNames {
		t.Run(name, func(t *testing.T) {
			path := copyFixture(t, name)
			cfg := fileops.OperationConfig{}
			require.NoError(t, WriteMetadataToFileContext(ctx, path, full, cfg), "full map")
			require.NoError(t, WriteMetadataToFileContext(ctx, path, map[string]any{"title": "T", "series": "S"}, cfg), "series without album")
			require.NoError(t, WriteSingleTagContext(ctx, path, "COMPOSER", ""), "clear composer")
			require.NoError(t, WriteMetadataToFileContext(ctx, path, map[string]any{"track": "03"}, cfg), "zero-padded track")
			require.NoError(t, WriteSingleTagContext(ctx, path, "TITLE", "T "), "trailing space")
		})
	}
}
