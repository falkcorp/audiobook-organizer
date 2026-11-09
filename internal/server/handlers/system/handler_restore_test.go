// file: internal/server/handlers/system/handler_restore_test.go
// version: 1.0.0
// guid: 4b9d2e71-8c3a-4f05-a6e2-d17f53c08b94
// last-edited: 2026-11-09

package system_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/backup"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers/system"
	"github.com/falkcorp/mediacore/storage/restorejournal"
)

// stubStager records what POST /backup/restore asked of it, in order.
type stubStager struct {
	calls   []string // "stage <dir> <file> <verify> <by>" and "restart <reason>"
	err     error    // what StageRestore returns
	sidecar bool     // the archive has a checksum sidecar, so it is checked without verify too
}

func (s *stubStager) StageRestore(_ context.Context, dir, file string, verify bool, by string, diverged func()) (backup.StagedRestore, error) {
	s.calls = append(s.calls, fmt.Sprintf("stage %s %s %v %s", dir, file, verify, by))
	if s.err != nil {
		return backup.StagedRestore{}, s.err
	}
	// As books' stager does once every check passed, before the intent.
	diverged()
	return backup.StagedRestore{ID: "20261109T000000Z-oracle01", Archive: filepath.Join(dir, file), ArchiveID: "abc",
		Verified: verify || s.sidecar}, nil
}

func (s *stubStager) RequestRestart(reason string) { s.calls = append(s.calls, "restart "+reason) }

// postRestore serves one POST /backup/restore through h.
func postRestore(h *system.Handler, body string) *httpResponse {
	w := run(http.MethodPost, "/backup/restore", "/backup/restore", []byte(body), func(r *gin.Engine) {
		r.POST("/backup/restore", h.RestoreBackup)
	})
	out := &httpResponse{code: w.Code, body: w.Body.String()}
	_ = json.Unmarshal(w.Body.Bytes(), &out.json)
	return out
}

type httpResponse struct {
	code int
	body string
	json map[string]any
}

func (r *httpResponse) data(t *testing.T) map[string]any {
	t.Helper()
	d, ok := r.json["data"].(map[string]any)
	require.True(t, ok, "no data object in %s", r.body)
	return d
}

// withDatabasePath points the config at a database path under a fresh
// directory (and the default backup directory beside it) for the test.
func withDatabasePath(t *testing.T, dbPath string) {
	t.Helper()
	prevDB, prevBackup := config.AppConfig.DatabasePath, config.AppConfig.BackupDir
	config.AppConfig.DatabasePath, config.AppConfig.BackupDir = dbPath, ""
	t.Cleanup(func() { config.AppConfig.DatabasePath, config.AppConfig.BackupDir = prevDB, prevBackup })
}

// TestRestoreBackupStagesAndRestarts (05-backup §5B.3.4; R1-25 Goal item 3):
// POST /backup/restore never restores in place. It hands the request to the
// stager, answers 202 with restart_required and the intent's id, and only
// then asks for the restart; a refusal is mapped to its status and never
// restarts; target_path is refused before the stager is asked; a process
// with no stager answers 503.
func TestRestoreBackupStagesAndRestarts(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "audiobooks.pebble")
	withDatabasePath(t, dbPath)
	backupDir := backup.ResolveDir("", dbPath)

	t.Run("staged", func(t *testing.T) {
		h, _ := newTestHandler(t)
		st := &stubStager{}
		h.WithRestoreStager(func() system.RestoreStager { return st })
		r := postRestore(h, `{"backup_filename":"gotham.tar.gz","verify":true}`)
		require.Equal(t, http.StatusAccepted, r.code, r.body)
		d := r.data(t)
		assert.Equal(t, true, d["restart_required"])
		assert.Equal(t, "20261109T000000Z-oracle01", d["restore_id"])
		assert.Equal(t, true, d["verified"])
		assert.Equal(t, true, d["verify_requested"])
		assert.Equal(t, []string{
			"stage " + backupDir + " gotham.tar.gz true api",
			"restart restore 20261109T000000Z-oracle01 staged",
		}, st.calls, "stage first, restart once after it")
	})

	t.Run("unverified", func(t *testing.T) {
		h, _ := newTestHandler(t)
		st := &stubStager{}
		h.WithRestoreStager(func() system.RestoreStager { return st })
		r := postRestore(h, `{"backup_filename":"gotham.tar.gz","verify":false}`)
		require.Equal(t, http.StatusAccepted, r.code, r.body)
		assert.Equal(t, false, r.data(t)["verified"])
		assert.Equal(t, "stage "+backupDir+" gotham.tar.gz false api", st.calls[0])
	})

	t.Run("checked without verify", func(t *testing.T) {
		h, _ := newTestHandler(t)
		st := &stubStager{sidecar: true}
		h.WithRestoreStager(func() system.RestoreStager { return st })
		r := postRestore(h, `{"backup_filename":"gotham.tar.gz","verify":false}`)
		require.Equal(t, http.StatusAccepted, r.code, r.body)
		assert.Equal(t, true, r.data(t)["verified"], "the archive's sidecar was checked although verify was not asked")
		assert.Equal(t, false, r.data(t)["verify_requested"])
	})

	t.Run("target_path is refused", func(t *testing.T) {
		h, _ := newTestHandler(t)
		st := &stubStager{}
		h.WithRestoreStager(func() system.RestoreStager { return st })
		r := postRestore(h, `{"backup_filename":"gotham.tar.gz","target_path":"/tmp/arkham"}`)
		assert.Equal(t, http.StatusBadRequest, r.code)
		assert.Contains(t, r.body, "storage restore-offline")
		assert.Empty(t, st.calls)
	})

	t.Run("filename required", func(t *testing.T) {
		h, _ := newTestHandler(t)
		st := &stubStager{}
		h.WithRestoreStager(func() system.RestoreStager { return st })
		assert.Equal(t, http.StatusBadRequest, postRestore(h, `{}`).code)
		assert.Empty(t, st.calls)
	})

	t.Run("no stager", func(t *testing.T) {
		h, _ := newTestHandler(t)
		assert.Equal(t, http.StatusServiceUnavailable, postRestore(h, `{"backup_filename":"gotham.tar.gz"}`).code)
		h.WithRestoreStager(func() system.RestoreStager { return nil })
		assert.Equal(t, http.StatusServiceUnavailable, postRestore(h, `{"backup_filename":"gotham.tar.gz"}`).code)
	})

	for _, c := range []struct {
		err  error
		code int
	}{
		{fmt.Errorf("%w: gotham.tar.gz", backup.ErrVerificationUnsupported), http.StatusBadRequest},
		{fmt.Errorf("extract: %w: %q", backup.ErrArchiveEntryEscapes, "../arkham"), http.StatusBadRequest},
		{fmt.Errorf("extract: %w: %q", backup.ErrArchiveCarriesHNSWSnapshot, "gotham.pebble/hnsw/hnsw.clean-stop"), http.StatusBadRequest},
		{fmt.Errorf("extract: %w", backup.ErrArchiveEmpty), http.StatusInternalServerError},
		{fmt.Errorf("backup %q not found: %w", "gotham.tar.gz", os.ErrNotExist), http.StatusNotFound},
		{fmt.Errorf("%w: gotham.tar.gz", backup.ErrChecksumMismatch), http.StatusConflict},
		{fmt.Errorf("slot: %w", restorejournal.ErrSwapPending), http.StatusConflict},
		{fmt.Errorf("placement: %w", restorejournal.ErrRenameRefused), http.StatusUnprocessableEntity},
		{fmt.Errorf("targets: %w", restorejournal.ErrTargetsRefused), http.StatusUnprocessableEntity},
		{errors.New("the batcomputer is down"), http.StatusInternalServerError},
	} {
		t.Run(fmt.Sprintf("refused %d %v", c.code, c.err), func(t *testing.T) {
			h, _ := newTestHandler(t)
			st := &stubStager{err: c.err}
			h.WithRestoreStager(func() system.RestoreStager { return st })
			r := postRestore(h, `{"backup_filename":"gotham.tar.gz","verify":true}`)
			assert.Equal(t, c.code, r.code, r.body)
			require.Len(t, st.calls, 1)
			assert.True(t, strings.HasPrefix(st.calls[0], "stage "), "a refused stage must not restart: %v", st.calls)
		})
	}
}

// TestRestoreBackupMarksTheHNSWStoreDiverged (R1-15 round-2 review B1, on
// the staged restore): the handler hands the stager its divergence mark,
// which the stager runs once every check passed; the mark reaches the
// installed WithANNSnapshotDivergence with a reason naming the restore, so
// the restart that applies it writes no clean-stop marker. A refused stage
// never runs it.
func TestRestoreBackupMarksTheHNSWStoreDiverged(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "audiobooks.pebble")
	withDatabasePath(t, dbPath)
	for name, tc := range map[string]struct {
		err  error
		code int
		want int
	}{
		"staged":  {code: http.StatusAccepted, want: 1},
		"refused": {err: restorejournal.ErrSwapPending, code: http.StatusConflict, want: 0},
	} {
		t.Run(name, func(t *testing.T) {
			h, _ := newTestHandler(t)
			var reasons []string
			h.WithANNSnapshotDivergence(func(reason string) { reasons = append(reasons, reason) })
			st := &stubStager{err: tc.err}
			h.WithRestoreStager(func() system.RestoreStager { return st })
			r := postRestore(h, `{"backup_filename":"gotham.tar.gz","verify":true}`)
			require.Equal(t, tc.code, r.code, r.body)
			require.Len(t, reasons, tc.want, "divergence marks: %v", reasons)
			for _, reason := range reasons {
				assert.Contains(t, reason, "restore")
			}
		})
	}
}

// fileStager is the stager over the real backup.StageRestore, as books
// installs it, with an always-opening store check (the archives here are of
// a stand-in store directory, not a Pebble store).
type fileStager struct {
	state, db string
	restarts  []string
}

func (s *fileStager) StageRestore(ctx context.Context, dir, file string, verify bool, by string, diverged func()) (backup.StagedRestore, error) {
	return backup.StageRestore(ctx, backup.StageRestoreOptions{
		BackupDir: dir, Filename: file, Verify: verify,
		StateDir: s.state, DBPath: s.db, BuildID: "batcomputer-r1", RequestedBy: by,
		VerifyStore: func(context.Context, string) error { return nil },
		BeforeStage: func(string) error { diverged(); return nil },
	})
}

func (s *fileStager) RequestRestart(reason string) { s.restarts = append(s.restarts, reason) }

// TestRestoreBackupThroughStageRestore (TASK-306's regressions, on the
// staged restore): with the real stager, a fresh backup restores verified
// (the Settings page asks for verification by default), a tampered archive
// is refused 409, a backup without a checksum sidecar is refused 400 when
// verification is asked, and verify=false stages it. A refusal stages
// nothing and never restarts; the live store is never touched.
func TestRestoreBackupThroughStageRestore(t *testing.T) {
	setup := func(t *testing.T) (*system.Handler, *fileStager, *backup.BackupInfo) {
		t.Helper()
		root := t.TempDir()
		dbPath := filepath.Join(root, "audiobooks.pebble")
		require.NoError(t, os.MkdirAll(dbPath, 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(dbPath, "CURRENT"), []byte("the live store\n"), 0o600))
		withDatabasePath(t, dbPath)
		src := filepath.Join(t.TempDir(), "gotham.pebble")
		require.NoError(t, os.MkdirAll(src, 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(src, "CURRENT"), []byte("MANIFEST-000001\n"), 0o600))
		info, err := backup.CreateBackup(src, "pebble", backup.BackupConfig{BackupDir: backup.ResolveDir("", dbPath), MaxBackups: 5})
		require.NoError(t, err)
		h, _ := newTestHandler(t)
		st := &fileStager{state: t.TempDir(), db: dbPath}
		h.WithRestoreStager(func() system.RestoreStager { return st })
		return h, st, info
	}
	staged := func(t *testing.T, st *fileStager) bool {
		t.Helper()
		_, err := os.Stat(restorejournal.PendingPath(st.state))
		return err == nil
	}
	liveUntouched := func(t *testing.T, st *fileStager) {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(st.db, "CURRENT"))
		require.NoError(t, err)
		assert.Equal(t, "the live store\n", string(b))
	}

	t.Run("verified", func(t *testing.T) {
		h, st, info := setup(t)
		r := postRestore(h, fmt.Sprintf(`{"backup_filename":%q,"verify":true}`, info.Filename))
		require.Equal(t, http.StatusAccepted, r.code, r.body)
		assert.Equal(t, true, r.data(t)["verified"])
		assert.True(t, staged(t, st), "no intent in the slot")
		assert.Len(t, st.restarts, 1)
		liveUntouched(t, st)
		// A second restore while the first is staged is refused.
		r = postRestore(h, fmt.Sprintf(`{"backup_filename":%q,"verify":true}`, info.Filename))
		assert.Equal(t, http.StatusConflict, r.code, r.body)
		assert.Len(t, st.restarts, 1)
	})

	t.Run("tampered archive", func(t *testing.T) {
		h, st, info := setup(t)
		raw, err := os.ReadFile(info.Path)
		require.NoError(t, err)
		raw[0] ^= 0xFF
		require.NoError(t, os.WriteFile(info.Path, raw, 0o644))
		r := postRestore(h, fmt.Sprintf(`{"backup_filename":%q,"verify":true}`, info.Filename))
		assert.Equal(t, http.StatusConflict, r.code)
		assert.Contains(t, r.body, "checksum")
		assert.False(t, staged(t, st))
		assert.Empty(t, st.restarts)
		liveUntouched(t, st)
	})

	t.Run("no sidecar, verification asked", func(t *testing.T) {
		h, st, info := setup(t)
		require.NoError(t, os.Remove(info.Path+".sha256"))
		r := postRestore(h, fmt.Sprintf(`{"backup_filename":%q,"verify":true}`, info.Filename))
		assert.Equal(t, http.StatusBadRequest, r.code)
		assert.Contains(t, r.body, "checksum")
		assert.False(t, staged(t, st))
		assert.Empty(t, st.restarts)
	})

	t.Run("no sidecar, unverified", func(t *testing.T) {
		h, st, info := setup(t)
		require.NoError(t, os.Remove(info.Path+".sha256"))
		r := postRestore(h, fmt.Sprintf(`{"backup_filename":%q,"verify":false}`, info.Filename))
		require.Equal(t, http.StatusAccepted, r.code, r.body)
		assert.Equal(t, false, r.data(t)["verified"])
		assert.True(t, staged(t, st))
		liveUntouched(t, st)
	})

	t.Run("not in the backup directory", func(t *testing.T) {
		h, st, _ := setup(t)
		r := postRestore(h, `{"backup_filename":"../audiobooks.pebble"}`)
		assert.Equal(t, http.StatusNotFound, r.code, r.body)
		assert.False(t, staged(t, st))
	})
}
