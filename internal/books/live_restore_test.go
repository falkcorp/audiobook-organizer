// file: internal/books/live_restore_test.go
// version: 1.0.0
// guid: 2f8c5a17-6e3b-4d94-b1a0-7c9e4d28f53b
// last-edited: 2026-11-09

package books

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/backup"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/telemetry"
	"github.com/falkcorp/mediacore/app"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/restorejournal"
)

// liveRestoreEnv carries the serving child's inputs (TestLiveRestoreStagesAndExits75).
const liveRestoreEnv = "BOOKS_TEST_LIVE_RESTORE"

// liveRestoreArgs is what the parent hands the serving child.
type liveRestoreArgs struct {
	Test, State, Path, Root, File, Served string
}

// TestLiveRestoreStagesAndExits75 (05-backup §5B.3.4; 06 §6.5.2 "Restart
// request"; R1-25 Goal item 3), end to end over ABO on disk: a serving ABO
// answers POST /api/v1/backup/restore with 202 and restart_required, and the
// process then exits 75 on its own, with the store it served left in place
// and the restore staged in the one slot under this binary's ID, the live
// install ID and the archive's checksum; the HNSW clean-stop marker of the
// store being replaced is gone. The next boot applies the swap before the
// store opens: the restored store holds the archive's settings, the slot is
// empty and the journal records the swap, never a rejection.
//
// The exit is os.Exit from the kernel's restart path, so the serving half
// runs in a child test process.
func TestLiveRestoreStagesAndExits75(t *testing.T) {
	if raw := os.Getenv(liveRestoreEnv); raw != "" {
		var a liveRestoreArgs
		require.NoError(t, json.Unmarshal([]byte(raw), &a))
		if a.Test == t.Name() {
			liveRestoreChild(t, a)
			return
		}
	}
	h := newOfflineABO(t)
	info, err := backup.CreateBackup(h.path, "pebble", backup.BackupConfig{BackupDir: backup.ResolveDir("", h.path), MaxBackups: 5})
	require.NoError(t, err)
	elsewhere := filepath.Join(t.TempDir(), "arkham")
	h.setStoredRoot(elsewhere)
	require.Equal(t, elsewhere, h.storedRoot())
	require.NoError(t, os.MkdirAll(filepath.Dir(h.marker), 0o700))
	require.NoError(t, os.WriteFile(h.marker, nil, 0o600))

	a := liveRestoreArgs{Test: t.Name(), State: h.state, Path: h.path, Root: elsewhere, File: info.Filename,
		Served: filepath.Join(t.TempDir(), "served")}
	raw, err := json.Marshal(a)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^"+regexp.QuoteMeta(t.Name())+"$", "-test.count=1", "-test.v")
	cmd.Env = append(os.Environ(), liveRestoreEnv+"="+string(raw))
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	require.True(t, errors.As(err, &exit), "the serving child did not exit with a status (err %v):\n%s", err, out)
	require.Equal(t, 75, exit.ExitCode(), "the serving child's exit status:\n%s", out)
	served, err := os.ReadFile(a.Served)
	require.NoError(t, err, "the child never answered the restore:\n%s", out)
	id, ok := strings.CutPrefix(string(served), "202 ")
	require.True(t, ok, "the restore's answer: %s\n%s", served, out)

	st := h.slot()
	require.True(t, st.Busy(), "nothing staged after the restore's restart")
	require.ErrorIs(t, st.Err(), restorejournal.ErrSwapPending)
	assert.Contains(t, st.Err().Error(), restorejournal.TokenRestorePending)
	var in restorejournal.Intent
	b, err := os.ReadFile(restorejournal.PendingPath(h.state))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(b, &in))
	build, err := app.BinaryID(app.Options{Version: telemetry.Version()})
	require.NoError(t, err)
	assert.Equal(t, id, in.ID)
	assert.Equal(t, restorejournal.KindArchive, in.Kind)
	assert.Equal(t, info.Checksum, in.ArchiveID)
	assert.Equal(t, build, in.StagedByBuild)
	assert.NotEmpty(t, in.InstallID)
	assert.Equal(t, []restorejournal.Target{{From: filepath.Join(restorejournal.StagingDir(h.path, id), "db"), To: h.path}}, in.Targets)
	assert.False(t, exists(h.marker), "the HNSW clean-stop marker of the replaced store survived the stage")
	// The offline open refuses while the restore is staged.
	_, err = app.OpenOffline(t.Context(), h.opts, true, h.mod)
	require.ErrorIs(t, err, restorejournal.ErrSwapPending)

	h.boot()
	require.Equal(t, h.root, h.storedRoot(), "the boot after the restart did not apply the archive")
	require.False(t, h.slot().Busy(), "slot after the applying boot = %+v", h.slot())
	var steps []string
	for _, r := range journalOf(t, h.state, id) {
		steps = append(steps, r.Step)
	}
	assert.Contains(t, steps, "swapped", "journal %v", steps)
	assert.NotContains(t, steps, "rejected", "journal %v", steps)
}

// liveRestoreChild serves ABO over the parent's store, posts the restore,
// records the answer and waits for the kernel's restart to exit the
// process. Returning, or a failure, means no restart came.
func liveRestoreChild(t *testing.T, a liveRestoreArgs) {
	edit := func(c *config.Config) { c.DatabasePath, c.RootDir = a.Path, a.Root }
	disk := func(o *app.Options) { o.StorageFS = storage.FS{} }
	r := runComposedIn(t, a.State, edit, nil, disk, nil)
	w := r.serve(http.MethodPost, "/api/v1/backup/restore", []byte(fmt.Sprintf(`{"backup_filename":%q,"verify":true}`, a.File)))
	var resp struct {
		Data struct {
			RestartRequired bool   `json:"restart_required"`
			RestoreID       string `json:"restore_id"`
		} `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	answer := fmt.Sprintf("%d %s", w.Code, resp.Data.RestoreID)
	if w.Code != http.StatusAccepted || !resp.Data.RestartRequired {
		answer = fmt.Sprintf("%d %s", w.Code, w.Body.String())
	}
	require.NoError(t, os.WriteFile(a.Served, []byte(answer), 0o600))
	require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
	select {
	case <-time.After(90 * time.Second):
		t.Fatal("the service did not restart after staging the restore")
	case err := <-r.ran:
		t.Fatalf("Run returned without exiting the process: %v", err)
	}
}
