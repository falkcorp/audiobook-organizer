// file: internal/server/backup_optout_test.go
// version: 1.0.0
// guid: e1296d8d-87ce-482e-a80c-45c24c802435
// last-edited: 2026-10-10

package server

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	taglib "go.senan.xyz/taglib"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/internal/tagger"
)

// Owner decision D69: every bulk tag-writing entry point wraps its context
// with tagger.WithoutBackup, so create_backups (default on) does not leave a
// full .bak-* copy beside every file a bulk op touches. Each test below turns
// create_backups on, drives one entry point, and asserts the context that
// reaches the file writer opts out. Removing any one wrap fails its test.

// backupsOn turns create_backups on for one test and checks the precondition:
// a plain context WANTS a backup, so a false below is the opt-out's doing.
func backupsOn(t *testing.T) {
	t.Helper()
	prev := config.Snapshot()
	config.Mutate(func(c *config.Config) { c.CreateBackups = true })
	t.Cleanup(func() { config.Mutate(func(c *config.Config) { *c = prev }) })
	require.True(t, tagger.BackupWanted(context.Background()), "precondition: create_backups on")
}

// ctxRecorder collects the contexts a seam receives.
type ctxRecorder struct {
	mu   sync.Mutex
	ctxs []context.Context
}

func (r *ctxRecorder) add(ctx context.Context) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ctxs = append(r.ctxs, ctx)
}

// requireAllOptOut fails unless want contexts were recorded and none of them
// wants a backup.
func (r *ctxRecorder) requireAllOptOut(t *testing.T, want int) {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	require.Len(t, r.ctxs, want, "file-work calls")
	for i, ctx := range r.ctxs {
		require.False(t, tagger.BackupWanted(ctx), "call %d: the ctx reaching the writer wants a backup; the bulk opt-out was dropped", i)
	}
}

// captureWriteBack replaces the server's write-back seam with a recorder.
func captureWriteBack(t *testing.T) *ctxRecorder {
	t.Helper()
	rec := &ctxRecorder{}
	prev := writeBackBookFiles
	writeBackBookFiles = func(_ *metafetch.Service, ctx context.Context, _ string) (int, error) {
		rec.add(ctx)
		return 0, nil
	}
	t.Cleanup(func() { writeBackBookFiles = prev })
	return rec
}

// writeBackBook creates one unprotected synthetic book for the write-back ops.
func writeBackBook(t *testing.T, s *Server) string {
	t.Helper()
	b, err := s.store.CreateBook(&database.Book{
		Title:    "Synthetic Title",
		Format:   "m4b",
		FilePath: filepath.Join(t.TempDir(), "synthetic.m4b"),
	})
	require.NoError(t, err)
	return b.ID
}

func TestBackupOptOut_LibraryBulkWriteBackOp(t *testing.T) {
	s, cleanup := setupTestServer(t)
	defer cleanup()
	backupsOn(t)
	rec := captureWriteBack(t)
	id := writeBackBook(t, s)

	params, err := json.Marshal(bulkWriteBackOpParams{BookIDs: []string{id}})
	require.NoError(t, err)
	require.NoError(t, s.runBulkWriteBackOp(context.Background(), params, &resumeRecorder{opID: "op-optout-library"}))
	rec.requireAllOptOut(t, 1)
}

// RunBulkWriteBack is the server side of maintenance.bulk-write-back.
func TestBackupOptOut_MaintenanceRunBulkWriteBack(t *testing.T) {
	s, cleanup := setupTestServer(t)
	defer cleanup()
	backupsOn(t)
	rec := captureWriteBack(t)
	id := writeBackBook(t, s)

	progress := registryProgressAdapter{r: &resumeRecorder{opID: "op-optout-maint"}}
	require.NoError(t, s.RunBulkWriteBack(context.Background(), "op-optout-maint", []string{id}, false, 0, progress))
	rec.requireAllOptOut(t, 1)
}

func TestBackupOptOut_BatchSaveOp(t *testing.T) {
	s, cleanup := setupTestServer(t)
	defer cleanup()
	backupsOn(t)
	rec := captureWriteBack(t)
	id := writeBackBook(t, s)

	reg := capOpReg(t)
	require.NoError(t, s.RegisterBatchSaveToFilesOp(reg))
	def, ok := reg.Def("metadata.batch-save")
	require.True(t, ok)
	params, err := json.Marshal(batchSaveOpParams{BookIDs: []string{id}, Force: true})
	require.NoError(t, err)
	require.NoError(t, def.Run(context.Background(), params, &resumeRecorder{opID: "op-optout-save"}))
	rec.requireAllOptOut(t, 1)
}

// applyCachedCandidateForBookTimed is the batch apply op's per-book body.
func TestBackupOptOut_BatchApply(t *testing.T) {
	backupsOn(t)
	svc := &fakeApplySvc{candidates: oneCandidate(t)}
	out := applyCachedCandidateForBookTimed(context.Background(), svc, fakeBooks{}, "b1", true, nil,
		metafetch.NewApplyPhaseTimings(), nil, nil, "")
	require.True(t, out.Applied, "outcome: %+v", out)
	rec := &ctxRecorder{ctxs: svc.ctxs}
	rec.requireAllOptOut(t, 1)
}

// submitOpResultFileWork is the file job of batch-apply-candidates and of
// metadata.apply-when-scanned.
func TestBackupOptOut_ApplyWhenScannedFileWork(t *testing.T) {
	backupsOn(t)
	rec := &ctxRecorder{}
	done := make(chan struct{})
	prev := finishApplyFileWork
	finishApplyFileWork = func(_ *metafetch.Service, ctx context.Context, _, _ string, _, _ bool, _ func() error) error {
		rec.add(ctx)
		close(done)
		return nil
	}
	t.Cleanup(func() { finishApplyFileWork = prev })

	pool := NewFileIOPool(1)
	defer pool.Stop()
	s := &Server{fileIOPool: pool}
	s.submitOpResultFileWork(context.Background(), "b1", "")
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the file job never ran")
	}
	rec.requireAllOptOut(t, 1)
}

// fakeAutoFetchRecoverer records the ctx of the auto-fetch replay.
type fakeAutoFetchRecoverer struct{ rec *ctxRecorder }

func (f fakeAutoFetchRecoverer) FinishAutoFetchFileWork(ctx context.Context, _, _ string, _ bool) error {
	f.rec.add(ctx)
	return nil
}

func TestBackupOptOut_FileIOPoolReplays(t *testing.T) {
	backupsOn(t)
	apply := &fakeApplyRecoverer{}
	recoverApplyMetadataFileOp(apply, "b1")
	(&ctxRecorder{ctxs: apply.ctxs}).requireAllOptOut(t, 1)

	auto := &ctxRecorder{}
	recoverAutoFetchFileOp(fakeAutoFetchRecoverer{rec: auto}, "b1")
	auto.requireAllOptOut(t, 1)
}

// The startup movement-atom cleanup rewrites every m4b/m4a under RootDir. With
// create_backups on it must still leave no .bak-* sibling.
func TestBackupOptOut_MovementAtomCleanupLeavesNoBackup(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not on PATH")
	}
	backupsOn(t)
	store, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	root := t.TempDir()
	prevRoot := config.AppConfig.RootDir
	config.AppConfig.RootDir = root
	t.Cleanup(func() { config.AppConfig.RootDir = prevRoot })
	withAppDirConfig(t, root)

	path := filepath.Join(root, "Book", "book.m4a")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	out, err := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "anullsrc=r=22050:cl=mono", "-t", "1",
		"-c:a", "aac", "-b:a", "32k", path).CombinedOutput()
	require.NoError(t, err, "ffmpeg: %s", out)
	require.NoError(t, taglib.WriteTags(path, map[string][]string{"MOVEMENTNAME": {"Synthetic Movement"}}, 0))

	// The package-level book_file hash store is whatever an earlier test's
	// server installed (and has since closed); point it at this test's store.
	metadata.SetBookFileHashStore(store)
	t.Cleanup(func() { metadata.SetBookFileHashStore(nil) })

	res := (&Server{store: store}).stripMovementAtoms(context.Background())
	require.Equal(t, 1, res.Stripped, "result: %+v", res)
	baks, err := filepath.Glob(path + ".bak-*")
	require.NoError(t, err)
	require.Empty(t, baks, "the movement-atom cleanup left backups")
}
