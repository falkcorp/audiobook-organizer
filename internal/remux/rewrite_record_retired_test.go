// file: internal/remux/rewrite_record_retired_test.go
// version: 1.0.0
// guid: 2f6b9a04-8c31-4d75-b1e8-93a7d0c5e264
// last-edited: 2026-10-24

package remux

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/diagnosis"
	"github.com/falkcorp/audiobook-organizer/internal/filehash"
)

// retiredRowFixture is rowFixture with the file's row retired into a holder
// book, so the live-only path getter no longer finds it.
func retiredRowFixture(t *testing.T) (*database.PebbleStore, string, string, *database.BookFile) {
	t.Helper()
	store, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	store.WaitForWarmup()
	path := filepath.Join(t.TempDir(), "book.m4b")
	require.NoError(t, os.WriteFile(path, bytes.Repeat([]byte("original audio "), 4096), 0o644))
	before, err := filehash.BookFileHash(path)
	require.NoError(t, err)

	holder, err := store.CreateBook(&database.Book{Title: "Holder", FilePath: filepath.Join(t.TempDir(), "holder")})
	require.NoError(t, err)
	shell, err := store.CreateBook(&database.Book{Title: "Shell", FilePath: filepath.Dir(path)})
	require.NoError(t, err)
	v, err := store.CreateBookVersion(&database.BookVersion{
		BookID: holder.ID, Status: database.BookVersionStatusRetired, Format: "m4b", Source: "retired_book",
		RetiredFromBookID: shell.ID, RetireOpID: "op-1", RetiredAt: new(time.Now()),
	})
	require.NoError(t, err)
	row := &database.BookFile{BookID: shell.ID, FilePath: path, FileHash: before, Codec: "mp3", BitrateKbps: 128, SampleRateHz: 44100}
	require.NoError(t, store.CreateBookFile(row))
	require.NoError(t, store.RetireBookFiles(
		[]database.BookFileMove{{FileIDs: []string{row.ID}, SourceBookID: shell.ID}}, holder.ID, v.ID))

	live, err := store.GetBookFileByPath(path)
	require.NoError(t, err)
	require.Nil(t, live, "precondition: the live-only getter does not see a retired row")
	return store, path, before, row
}

// A rewrite of a retired version's file keeps its row describing the bytes on
// disk: the hash is what recognises the content later, and the codec fields are
// what the row reports. The live-only getter answered nil, so neither was
// recorded.
func TestRewriteRecord_RetiredRowKeepsDescribingTheBytes(t *testing.T) {
	store, path, before, row := retiredRowFixture(t)
	tr := NewTranscoder(&MockStore{})
	tr.SetBookFileStore(store)
	probe := func(context.Context, string) diagnosis.FileDiagnostic {
		return diagnosis.FileDiagnostic{Codec: "aac", BitrateKbps: 64, SampleRateHz: 22050}
	}

	require.NoError(t, tr.transcodeAndRecord(context.Background(), path, rewriteTo("transcoded audio "), probe))

	after, err := filehash.BookFileHash(path)
	require.NoError(t, err)
	require.NotEqual(t, before, after, "fixture: the rewrite changed the bytes")
	got, err := store.GetBookFileByPathIncludingRetired(path)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, row.ID, got.ID)
	require.True(t, got.Retired, "the row stays retired")
	require.Equal(t, after, got.FileHash)
	require.NotEmpty(t, got.PostMetadataHash)
	require.Equal(t, "aac", got.Codec)
	require.Equal(t, 64, got.BitrateKbps)
	require.Equal(t, 22050, got.SampleRateHz)
}
