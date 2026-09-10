// file: internal/maintenance/jobs/backfill_file_hashes_test.go
// version: 1.3.0
// guid: b2c3d4e5-f6a7-8901-bcde-f01234567890
// last-edited: 2026-09-10

package jobs_test

import (
	"context"
	"crypto/sha256"
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

func TestBackfillFileHashesJob_SkipsAlreadyHashed(t *testing.T) {
	hash := "existinghash"
	files := []database.BookFileCore{{ID: "f1", FilePath: "/tmp/audio.m4b", FileHash: hash}}
	var setCalled bool
	store := &database.MockStore{
		GetAllBookFilesCoreFunc: func() ([]database.BookFileCore, error) { return files, nil },
		SetBookFileHashFunc:     func(id, h string) error { setCalled = true; return nil },
	}

	j, err := maintenance.Get("backfill-file-hashes")
	require.NoError(t, err)
	require.NoError(t, j.Run(context.Background(), store, &noopReporter{}, false))
	assert.False(t, setCalled, "SetBookFileHash must not be called for already-hashed files")
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
		SetBookFileHashFunc:     func(id, h string) error { gotHash = h; return nil },
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
		SetBookFileHashFunc:     func(id, h string) error { setCalled = true; return nil },
	}

	j, err := maintenance.Get("backfill-file-hashes")
	require.NoError(t, err)
	require.NoError(t, j.Run(context.Background(), store, &noopReporter{}, true /* dryRun */))
	assert.False(t, setCalled, "dry_run=true: SetBookFileHash must not be called")
}

func TestBackfillFileHashesJob_MissingFile_Warns(t *testing.T) {
	files := []database.BookFileCore{{ID: "f4", FilePath: "/nonexistent/path/audio.m4b", FileHash: ""}}
	var setCalled bool
	rep := &noopReporter{}
	store := &database.MockStore{
		GetAllBookFilesCoreFunc: func() ([]database.BookFileCore, error) { return files, nil },
		SetBookFileHashFunc:     func(id, h string) error { setCalled = true; return nil },
	}

	j, err := maintenance.Get("backfill-file-hashes")
	require.NoError(t, err)
	require.NoError(t, j.Run(context.Background(), store, rep, false))
	assert.False(t, setCalled, "SetBookFileHash must not be called when file does not exist")
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
	for i := 0; i < n; i++ {
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
		SetBookFileHashFunc: func(id, h string) error {
			mu.Lock()
			defer mu.Unlock()
			if _, dup := got[id]; dup {
				t.Errorf("SetBookFileHash called twice for %s", id)
			}
			got[id] = h
			return nil
		},
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
