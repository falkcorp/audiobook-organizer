// file: internal/maintenance/jobs/scan_composer_tags_store_test.go
// version: 1.0.0
// guid: e2e71f78-ac3c-49ec-92c1-d078851eff2d
// last-edited: 2026-11-12

package jobs_test

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/filehash"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

// A live scan-composer-tags run rewrites files, and a rewritten file whose
// book_file row keeps its old hash reads as replaced at the next scan. The job
// records through the store it is handed (05-tasks conversion rule 3), so a
// store that cannot record hashes refuses the run before any file is touched.
func TestScanComposerTags_LiveRunRefusesAStoreThatCannotRecordHashes(t *testing.T) {
	j, err := maintenance.Get("scan-composer-tags")
	require.NoError(t, err)
	// A JobStore that implements nothing: the refusal must come before the job
	// reads a single row (a call on it would panic).
	var store maintenance.JobStore
	err = j.Run(context.Background(), noRecorderStore{store}, &noopReporter{}, false)
	require.ErrorContains(t, err, "cannot record file hashes")
}

type noRecorderStore struct{ maintenance.JobStore }

// The tag write records the new hash on the store the run was handed, not on
// the store internal/metadata has installed at startup.
func TestScanComposerTags_RecordsHashesOnTheStoreItWasHanded(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not on PATH")
	}
	path := filepath.Join(t.TempDir(), "book.m4a")
	out, err := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "anullsrc=r=22050:cl=mono", "-t", "1",
		"-c:a", "aac", "-b:a", "32k", path).CombinedOutput()
	require.NoError(t, err, "ffmpeg: %s", out)
	before, err := filehash.BookFileHash(path)
	require.NoError(t, err)

	narrator := "Synthetic Narrator"
	handed := newSyncPebbleStore(t)
	b, err := handed.CreateBook(&database.Book{Title: "Gotham Nights", FilePath: path, Format: "m4a", Narrator: &narrator})
	require.NoError(t, err)
	require.NoError(t, handed.CreateBookFile(&database.BookFile{BookID: b.ID, FilePath: path, Format: "m4a", FileHash: before}))

	// Another store owns a row for the same path and is the one installed.
	installed := newSyncPebbleStore(t)
	ob, err := installed.CreateBook(&database.Book{Title: "Arkham", FilePath: path, Format: "m4a"})
	require.NoError(t, err)
	require.NoError(t, installed.CreateBookFile(&database.BookFile{BookID: ob.ID, FilePath: path, Format: "m4a", FileHash: before}))
	metadata.SetBookFileHashStore(installed)
	t.Cleanup(func() { metadata.SetBookFileHashStore(nil) })

	j, err := maintenance.Get("scan-composer-tags")
	require.NoError(t, err)
	require.NoError(t, j.Run(context.Background(), handed, &noopReporter{}, false))

	after, err := filehash.BookFileHash(path)
	require.NoError(t, err)
	require.NotEqual(t, before, after, "fixture error: the job wrote nothing")
	got, err := handed.GetBookFileByPath(path)
	require.NoError(t, err)
	require.Equal(t, after, got.FileHash, "the store the run was handed did not learn the new hash")
	other, err := installed.GetBookFileByPath(path)
	require.NoError(t, err)
	require.Equal(t, before, other.FileHash, "the installed package store was written")
}
