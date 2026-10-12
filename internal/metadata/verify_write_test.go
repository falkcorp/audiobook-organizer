// file: internal/metadata/verify_write_test.go
// version: 1.3.0
// guid: 2f6a8d13-4c79-4b05-8e1a-96d0b3f7c524
// last-edited: 2026-10-11

package metadata

import (
	"context"
	"errors"
	"os/exec"
	"sort"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/filehash"
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
// (AtomicParsley, eyeD3, metaflac) must not then run over a file taglib wrote: the error
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

// A property value holding a NUL is cut at the NUL by TagLib's C string
// handling, on every container and in both build variants: a write that is
// lossy for a reason no container-specific rule explains.
const nulTitle = "a\x00b"

// WriteTagProperties checks the file even with verify_after_write off, in
// both build variants: reverts rely on a failed set or removal being an error.
func TestWriteTagProperties_LossyPropertyFailsWithTheSettingOff(t *testing.T) {
	setVerifyAfterWrite(t, false)
	noSafeWriteDeps(t)
	for _, name := range fixtureNames {
		t.Run(name, func(t *testing.T) {
			path := copyFixture(t, name)
			err := WriteTagProperties(path, map[string]string{"title": nulTitle})
			require.ErrorIs(t, err, tagger.ErrVerifyAfterWrite)
			var vae *tagger.VerifyAfterWriteError
			require.True(t, errors.As(err, &vae))
			require.Equal(t, []string{"TITLE"}, vae.Keys)
		})
	}
}

// WriteSingleTag is verified like the other writers (the native build's
// writeSingleTagWithTaglib has its own call to the read-back): TRACKNUMBER
// "side A" is not an integer an MP4 atom can hold.
func TestWriteSingleTag_LossyValueFailsVerification(t *testing.T) {
	setVerifyAfterWrite(t, true)
	noSafeWriteDeps(t)
	path := copyFixture(t, "test_sample.m4b")
	err := WriteSingleTagContext(context.Background(), path, "TRACKNUMBER", "side A")
	require.ErrorIs(t, err, tagger.ErrVerifyAfterWrite)
	var vae *tagger.VerifyAfterWriteError
	require.True(t, errors.As(err, &vae))
	require.Equal(t, []string{"TRACKNUMBER"}, vae.Keys)

	// Flag off: the write is not checked.
	setVerifyAfterWrite(t, false)
	require.NoError(t, WriteSingleTagContext(context.Background(), copyFixture(t, "test_sample.m4b"), "TRACKNUMBER", "side A"))
}

// hashedFixture copies a fixture, gives it a book_file row in a real store,
// and installs the store as the package's book_file hash store, as the server
// does at startup. packageSafeWriteDeps stays empty: WriteTagProperties must
// reach the store through the installed hash store alone.
func hashedFixture(t *testing.T, name string) (path string, store *database.PebbleStore, row *database.BookFile) {
	t.Helper()
	noSafeWriteDeps(t)
	path = copyFixture(t, name)
	store, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	book, err := store.CreateBook(&database.Book{Title: "Old", FilePath: path})
	require.NoError(t, err)
	before, err := filehash.BookFileHash(path)
	require.NoError(t, err)
	require.NoError(t, store.CreateBookFile(&database.BookFile{BookID: book.ID, FilePath: path, FileHash: before}))
	SetBookFileHashStore(store)
	t.Cleanup(func() { SetBookFileHashStore(nil) })
	row, err = store.GetBookFileByPath(path)
	require.NoError(t, err)
	require.NotNil(t, row)
	return path, store, row
}

// A revert (WriteTagProperties) rewrites the file's bytes, so the file's
// book_file row must learn the new hash, or the next rescan reads the file as
// replaced. Until the revert writers were wired through the installed hash
// store they recorded nothing.
func TestWriteTagProperties_RecordsTheRowsHashes(t *testing.T) {
	setVerifyAfterWrite(t, true)
	for _, name := range fixtureNames {
		t.Run(name, func(t *testing.T) {
			path, store, row := hashedFixture(t, name)
			require.NoError(t, WriteTagProperties(path, map[string]string{"title": "Reverted"}))

			after, err := filehash.BookFileHash(path)
			require.NoError(t, err)
			require.NotEqual(t, row.FileHash, after, "fixture error: the write did not change the file's bytes")
			got, err := store.GetBookFileByPath(path)
			require.NoError(t, err)
			require.Equal(t, after, got.FileHash)
		})
	}
}

// A revert whose read-back fails records the diff as a FileEventObserved event
// on the file's row, even with verify_after_write off: before the writers were
// wired through the hash store there was no recorder, and the failure left no
// trace beyond the returned error.
func TestWriteTagProperties_FailedVerificationRecordsProvenance(t *testing.T) {
	setVerifyAfterWrite(t, false)
	for _, name := range fixtureNames {
		t.Run(name, func(t *testing.T) {
			path, store, row := hashedFixture(t, name)
			err := WriteTagProperties(path, map[string]string{"title": nulTitle})
			require.ErrorIs(t, err, tagger.ErrVerifyAfterWrite)

			history, herr := store.GetFileHistory(row.ID)
			require.NoError(t, herr)
			var observed []database.FileEvent
			for _, e := range history {
				if e.Kind == database.FileEventObserved {
					observed = append(observed, e)
				}
			}
			require.Len(t, observed, 1, "history: %+v", history)
			require.Contains(t, observed[0].Detail, "TITLE")
			require.Equal(t, row.ID, observed[0].BookFileID)
			// The event carries the digest of the file as written, so the
			// whole-file hash finds it.
			byHash, ferr := store.FindFileEventsByHash(observed[0].Digest.SHA256Full)
			require.NoError(t, ferr)
			require.NotEmpty(t, byHash)
		})
	}
}

// CLI fallback. When native TagLib fails, AtomicParsley, eyeD3 or metaflac
// writes the file, and WriteMetadataToFileAtomic checks it against the tags
// that writer sets (cliWriterTags) rather than skipping verification. The gate
// tests force the native writer off (taglibAvailable = false: the in-place
// write then goes straight to the CLI writer) and stand in for the CLI tool, so
// they need no external program.

var cliGateMetadata = map[string]any{
	"title": "Gate Title", "artist": "Gate Author", "album": "Gate Album", "year": 2020,
	"track": "3", "series": "Gate Series", "narrator": "Gate Narrator",
	"publisher": "Gate Publisher", "description": "Gate Description", "book_id": "gate-1",
}

func forceCLIFallback(t *testing.T, writer func(path string, md map[string]any) error) {
	t.Helper()
	prevNative, prevCLI := taglibAvailable, cliTagWriter
	t.Cleanup(func() { taglibAvailable, cliTagWriter = prevNative, prevCLI })
	taglibAvailable = false
	cliTagWriter = func(path string, md map[string]any, _ fileops.OperationConfig) error { return writer(path, md) }
}

// writeCLITags writes the tags the CLI writer for path would set, except drop.
func writeCLITags(path string, md map[string]any, drop ...string) error {
	want := cliWriterTags(path, md)
	for _, k := range drop {
		delete(want, k)
	}
	return tagger.WriteTagsInPlace(path, want, 0)
}

func atomicWrite(path string) error {
	return WriteMetadataToFileAtomic(path, cliGateMetadata, fileops.OperationConfig{}, fileops.WriteTagsSafeOptions{}, tagger.SafeWriteDeps{})
}

func TestWriteMetadataToFileAtomic_VerifiesAfterCLIFallback(t *testing.T) {
	setVerifyAfterWrite(t, true)
	noSafeWriteDeps(t)
	for _, name := range fixtureNames {
		t.Run(name+"/tags landed", func(t *testing.T) {
			forceCLIFallback(t, func(path string, md map[string]any) error { return writeCLITags(path, md) })
			require.NoError(t, atomicWrite(copyFixture(t, name)))
		})
		t.Run(name+"/a tag the writer set is missing", func(t *testing.T) {
			forceCLIFallback(t, func(path string, md map[string]any) error { return writeCLITags(path, md, "TITLE") })
			err := atomicWrite(copyFixture(t, name))
			require.ErrorIs(t, err, tagger.ErrVerifyAfterWrite)
			var vae *tagger.VerifyAfterWriteError
			require.True(t, errors.As(err, &vae))
			require.Equal(t, []string{"TITLE"}, vae.Keys)
		})
		t.Run(name+"/the writer reported success and wrote nothing", func(t *testing.T) {
			forceCLIFallback(t, func(string, map[string]any) error { return nil })
			path := copyFixture(t, name)
			err := atomicWrite(path)
			require.ErrorIs(t, err, tagger.ErrVerifyAfterWrite)
			var vae *tagger.VerifyAfterWriteError
			require.True(t, errors.As(err, &vae))
			wantKeys := make([]string, 0)
			for k := range cliWriterTags(path, cliGateMetadata) {
				wantKeys = append(wantKeys, k)
			}
			sort.Strings(wantKeys)
			require.Equal(t, wantKeys, vae.Keys)
		})
		t.Run(name+"/setting off", func(t *testing.T) {
			setVerifyAfterWrite(t, false)
			forceCLIFallback(t, func(string, map[string]any) error { return nil })
			require.NoError(t, atomicWrite(copyFixture(t, name)))
		})
		t.Run(name+"/a CLI failure is returned as is", func(t *testing.T) {
			boom := errors.New("tool failed")
			forceCLIFallback(t, func(string, map[string]any) error { return boom })
			require.ErrorIs(t, atomicWrite(copyFixture(t, name)), boom)
		})
	}
}

// The CLI writers set a subset of the native map; a tag they do not set is not
// held against them.
func TestCLIWriterTags_AreASubsetOfWhatEachWriterSets(t *testing.T) {
	md := cliGateMetadata
	m4b := cliWriterTags("x.m4b", md)
	require.Equal(t, []string{"Gate Title"}, m4b["TITLE"])
	require.Equal(t, []string{"Gate Author"}, m4b["ALBUMARTIST"])
	require.Equal(t, []string{"Gate Series"}, m4b["GROUPING"])
	for _, k := range []string{"PUBLISHER", "LABEL", "DESCRIPTION", "COMMENT", "NARRATOR", "AUDIOBOOK_ORGANIZER_ID", "GENRE"} {
		require.NotContains(t, m4b, k, "AtomicParsley: %s is not a stable read-back", k)
	}
	mp3 := cliWriterTags("x.mp3", md)
	require.Equal(t, []string{"Gate Narrator"}, mp3["NARRATOR"])
	require.Equal(t, []string{"2020"}, mp3["RELEASEDATE"], "eyeD3 -Y is the release year")
	require.NotContains(t, mp3, "DATE")
	require.NotContains(t, mp3, "PUBLISHER")
	require.Equal(t, []string{"gate-1"}, mp3["AUDIOBOOK_ORGANIZER_ID"])
	flac := cliWriterTags("x.flac", md)
	require.Equal(t, []string{"2020"}, flac["DATE"])
	require.Equal(t, []string{"gate-1"}, flac["AUDIOBOOK_ORGANIZER_ID"])
	require.NotContains(t, flac, "PUBLISHER")
	require.Nil(t, cliWriterTags("x.ogg", md), "no CLI writer for ogg")
}

// The real tools: what AtomicParsley, eyeD3 and metaflac write is what
// cliWriterTags says TagLib reads back. Skipped where a tool is not installed.
func TestWriteMetadataToFileAtomic_RealCLIWriters(t *testing.T) {
	setVerifyAfterWrite(t, true)
	noSafeWriteDeps(t)
	for name, tool := range map[string]string{"test_sample.m4b": "AtomicParsley", "test_sample.mp3": "eyeD3", "test_sample.flac": "metaflac"} {
		t.Run(name, func(t *testing.T) {
			if _, err := exec.LookPath(tool); err != nil {
				t.Skipf("%s not on PATH", tool)
			}
			prev := taglibAvailable
			t.Cleanup(func() { taglibAvailable = prev })
			taglibAvailable = false
			path := copyFixture(t, name)
			require.NoError(t, atomicWrite(path), "the %s write must read back as cliWriterTags says", tool)
			raw, err := readTagsWithTaglib(path)
			require.NoError(t, err)
			require.Equal(t, []string{"Gate Title"}, raw["TITLE"])
		})
	}
}
