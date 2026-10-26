// file: internal/maintenance/jobs/backfill_file_hashes_test.go
// version: 1.5.0
// guid: b2c3d4e5-f6a7-8901-bcde-f01234567890
// last-edited: 2026-10-26

package jobs_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// storeHashVia is a ModifyBookFile stub over files: the job stores a hash
// through ModifyBookFile (conditionally, on the row still matching), so it runs
// the callback on a row built from the matching file and reports the hash it
// would have stored to onStore.
func storeHashVia(files []database.BookFileCore, onStore func(id, hash string)) func(string, string, func(*database.BookFile) error) (*database.BookFile, error) {
	return func(bookID, fileID string, fn func(*database.BookFile) error) (*database.BookFile, error) {
		for _, c := range files {
			if c.ID != fileID {
				continue
			}
			row := database.BookFile{ID: c.ID, BookID: c.BookID, FilePath: c.FilePath, FileSize: c.FileSize, LastScanMtime: c.LastScanMtime, FileHash: c.FileHash}
			if err := fn(&row); err != nil {
				return &row, nil
			}
			onStore(fileID, row.FileHash)
			return &row, nil
		}
		return nil, nil
	}
}

func TestBackfillFileHashesJob_Registered(t *testing.T) {
	assertJobRegistered(t, "backfill-file-hashes")
}

func TestBackfillFileHashesJob_Metadata(t *testing.T) {
	j, err := maintenance.Get("backfill-file-hashes")
	require.NoError(t, err)
	assert.Equal(t, "backfill-file-hashes", j.ID())
	assert.NotEmpty(t, j.Name())
	assert.NotEmpty(t, j.Description())
	assert.Equal(t, "files", j.Category())
	assert.NotNil(t, j.DefaultParams())
	assert.True(t, j.CanResume(), "backfill-file-hashes must support resume (checkpoint-based)")
}

// TestBackfillFileHashesJob_BookIDsScope: a book_ids param limits the run to
// those books' rows (the Bible's 3,541 rows on prod, not the library's
// 700k), and the total reported is the scoped count.
func TestBackfillFileHashesJob_BookIDsScope(t *testing.T) {
	dir := t.TempDir()
	mk := func(name string) string {
		p := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(p, []byte(name+" audio bytes"), 0o644))
		return p
	}
	files := []database.BookFileCore{
		{ID: "a1", BookID: "A", FilePath: mk("a1.mp3")},
		{ID: "b1", BookID: "B", FilePath: mk("b1.mp3")},
		{ID: "c1", BookID: "C", FilePath: mk("c1.mp3")},
	}
	var mu sync.Mutex
	hashed := map[string]bool{}
	store := &database.MockStore{
		GetAllBookFilesCoreFunc: func() ([]database.BookFileCore, error) { return files, nil },
		ModifyBookFileFunc: storeHashVia(files, func(id, h string) {
			mu.Lock()
			defer mu.Unlock()
			hashed[id] = true
		}),
	}
	j, err := maintenance.Get("backfill-file-hashes")
	require.NoError(t, err)
	ctx := maintenance.WithRawParams(context.Background(), json.RawMessage(`{"dry_run":false,"book_ids":["A","C"]}`))
	rep := &countingReporter{}
	require.NoError(t, j.Run(ctx, store, rep, false))
	assert.Equal(t, map[string]bool{"a1": true, "c1": true}, hashed)
	assert.Equal(t, 2, rep.total, "the total is the scoped row count")
}

type countingReporter struct {
	noopReporter
	total int
}

func (r *countingReporter) SetTotal(n int) { r.total = n }

func TestBackfillFileHashesJob_SkipsAlreadyHashed(t *testing.T) {
	hash := "existinghash"
	files := []database.BookFileCore{{ID: "f1", FilePath: "/tmp/audio.m4b", FileHash: hash}}
	var setCalled bool
	store := &database.MockStore{
		GetAllBookFilesCoreFunc: func() ([]database.BookFileCore, error) { return files, nil },
		ModifyBookFileFunc:      storeHashVia(files, func(id, h string) { setCalled = true }),
	}

	j, err := maintenance.Get("backfill-file-hashes")
	require.NoError(t, err)
	require.NoError(t, j.Run(context.Background(), store, &noopReporter{}, false))
	assert.False(t, setCalled, "no hash may be stored for already-hashed files")
}

func TestBackfillFileHashesJob_HashesNewFile(t *testing.T) {
	// Write a temp file with known content so we can verify the hash.
	f, err := os.CreateTemp(t.TempDir(), "audio*.m4b")
	require.NoError(t, err)
	content := []byte("fake audio data for hashing")
	_, err = f.Write(content)
	require.NoError(t, err)
	f.Close()

	wantSum := sha256.Sum256(content)
	wantHash := fmt.Sprintf("%x", wantSum)

	files := []database.BookFileCore{{ID: "f2", FilePath: f.Name(), FileHash: ""}}
	var gotHash string
	store := &database.MockStore{
		GetAllBookFilesCoreFunc: func() ([]database.BookFileCore, error) { return files, nil },
		ModifyBookFileFunc:      storeHashVia(files, func(id, h string) { gotHash = h }),
	}

	j, err := maintenance.Get("backfill-file-hashes")
	require.NoError(t, err)
	require.NoError(t, j.Run(context.Background(), store, &noopReporter{}, false))
	assert.Equal(t, wantHash, gotHash)
}

func TestBackfillFileHashesJob_DryRun_SkipsWrite(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "audio*.m4b")
	require.NoError(t, err)
	_, _ = f.WriteString("audio")
	f.Close()

	files := []database.BookFileCore{{ID: "f3", FilePath: f.Name(), FileHash: ""}}
	var setCalled bool
	store := &database.MockStore{
		GetAllBookFilesCoreFunc: func() ([]database.BookFileCore, error) { return files, nil },
		ModifyBookFileFunc:      storeHashVia(files, func(id, h string) { setCalled = true }),
	}

	j, err := maintenance.Get("backfill-file-hashes")
	require.NoError(t, err)
	require.NoError(t, j.Run(context.Background(), store, &noopReporter{}, true /* dryRun */))
	assert.False(t, setCalled, "dry_run=true: no hash may be stored")
}

func TestBackfillFileHashesJob_MissingFile_Warns(t *testing.T) {
	files := []database.BookFileCore{{ID: "f4", FilePath: "/nonexistent/path/audio.m4b", FileHash: ""}}
	var setCalled bool
	rep := &noopReporter{}
	store := &database.MockStore{
		GetAllBookFilesCoreFunc: func() ([]database.BookFileCore, error) { return files, nil },
		ModifyBookFileFunc:      storeHashVia(files, func(id, h string) { setCalled = true }),
	}

	j, err := maintenance.Get("backfill-file-hashes")
	require.NoError(t, err)
	require.NoError(t, j.Run(context.Background(), store, rep, false))
	assert.False(t, setCalled, "no hash may be stored when file does not exist")
	assert.NotEmpty(t, rep.logs, "expected a warning log for the missing file")
}

// TestBackfillFileHashesJob_ParallelHashesAll hashes many real files at once and
// asserts every one is hashed exactly once with the correct digest. Run under
// -race it is the guard for the concurrent reporter/counter access the parallel
// pool introduced (a plain int++ in ProgressAdapter would trip the detector).
func TestBackfillFileHashesJob_ParallelHashesAll(t *testing.T) {
	const n = 60 // > hashBackfillChunkSize is not needed; > worker count is enough
	dir := t.TempDir()
	files := make([]database.BookFileCore, n)
	want := make(map[string]string, n)
	for i := range n {
		content := []byte(fmt.Sprintf("audio payload number %d — distinct bytes", i))
		p := filepath.Join(dir, fmt.Sprintf("audio%02d.m4b", i))
		require.NoError(t, os.WriteFile(p, content, 0o644))
		id := fmt.Sprintf("f%d", i)
		files[i] = database.BookFileCore{ID: id, FilePath: p, FileHash: ""}
		sum := sha256.Sum256(content)
		want[id] = fmt.Sprintf("%x", sum)
	}

	var mu sync.Mutex
	got := make(map[string]string, n)
	store := &database.MockStore{
		GetAllBookFilesCoreFunc: func() ([]database.BookFileCore, error) { return files, nil },
		ModifyBookFileFunc: storeHashVia(files, func(id, h string) {
			mu.Lock()
			defer mu.Unlock()
			if _, dup := got[id]; dup {
				t.Errorf("hash stored twice for %s", id)
			}
			got[id] = h
		}),
	}

	j, err := maintenance.Get("backfill-file-hashes")
	require.NoError(t, err)
	require.NoError(t, j.Run(context.Background(), store, &noopReporter{}, false))

	mu.Lock()
	defer mu.Unlock()
	assert.Len(t, got, n, "every file must be hashed exactly once")
	assert.Equal(t, want, got, "each file's stored hash must be its real sha256")
}

func TestBackfillFileHashesJob_Cancellation(t *testing.T) {
	files := make([]database.BookFileCore, 10)
	for i := range files {
		files[i] = database.BookFileCore{ID: fmt.Sprintf("f%d", i), FilePath: "/tmp/x.m4b", FileHash: ""}
	}
	store := &database.MockStore{
		GetAllBookFilesCoreFunc: func() ([]database.BookFileCore, error) { return files, nil },
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	j, err := maintenance.Get("backfill-file-hashes")
	require.NoError(t, err)
	err = j.Run(ctx, store, &noopReporter{}, false)
	if err != nil {
		assert.ErrorIs(t, err, context.Canceled)
	}
}
