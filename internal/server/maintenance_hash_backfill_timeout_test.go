// file: internal/server/maintenance_hash_backfill_timeout_test.go
// version: 1.0.0
// guid: 9b3f7d21-5e8a-4c06-a4d9-0f1b6e2c8a73
// last-edited: 2026-10-26

package server

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/operations/state"
)

// The prod failure of 2026-10-26: backfill-file-hashes ran a dry run over a
// library too big for the 4h Timeout and ended timed_out part way, with no way to
// continue (a retry is a new operation id, and the report that records progress
// is named after the id). The REAL job and the REAL registry, with a Timeout
// shorter than the job's pause margin, so every attempt that has made progress
// pauses at its first chunk boundary: the operation restarts under its own id,
// never ends timed_out, and finishes with every row in its report exactly once.
func TestHashBackfill_TimeoutSizedRunFinishesUnderOneOperationID(t *testing.T) {
	const total = 1300 // chunks of 512: three attempts

	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "Gotham"), 0o755))
	prev := config.AppConfig.RootDir
	config.AppConfig.RootDir = root
	t.Cleanup(func() { config.AppConfig.RootDir = prev })

	store, err := database.NewPebbleStoreInMemory(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	for b := 0; b < 9; b++ {
		_, err := store.CreateBook(&database.Book{ID: fmt.Sprintf("b%d", b), Title: fmt.Sprintf("Knightfall Part %d", b)})
		require.NoError(t, err)
	}
	for i := 0; i < total; i++ {
		path := filepath.Join(root, "Gotham", fmt.Sprintf("Arkham %04d.m4b", i)) // most are not on disk
		if i%10 == 0 {
			path = filepath.Join(root, "Gotham", fmt.Sprintf("Knightfall %04d.m4b", i))
			require.NoError(t, os.WriteFile(path, fmt.Appendf(nil, "knightfall bytes %d", i), 0o644))
		}
		require.NoError(t, store.CreateBookFile(&database.BookFile{
			ID: fmt.Sprintf("f%04d", i), BookID: fmt.Sprintf("b%d", i%9), FilePath: path,
		}))
	}

	// The job's own def, with a Timeout (4 minutes) under the job's 5 minute
	// pause margin: the time left is always inside the margin.
	src := maintReg(t)
	job, err := maintenance.Get("backfill-file-hashes")
	require.NoError(t, err)
	srv := &Server{store: store}
	require.NoError(t, srv.registerMaintenanceJobOp(src, job))
	def, ok := src.Def(maintenanceOpID(job.ID()))
	require.True(t, ok)
	require.Equal(t, opsregistry.ResumeRestart, def.ResumePolicy)
	def.Timeout = 4 * time.Minute

	reg := opsregistry.NewWithOptions(store, slog.New(slog.DiscardHandler), 2, opsregistry.Options{
		AbandonGrace:     50 * time.Millisecond,
		WatchdogInterval: time.Hour,
		SweepInterval:    time.Hour,
		AbandonedCap:     100,
	})
	t.Cleanup(func() {
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = reg.Shutdown(sctx)
	})
	require.NoError(t, reg.RegisterOp(def))
	reg.Start(t.Context())

	opID, err := reg.EnqueueOp(t.Context(), def.ID, json.RawMessage(`{"job_id":"backfill-file-hashes","dry_run":true}`))
	require.NoError(t, err)

	deadline := time.Now().Add(60 * time.Second)
	var row *database.OperationV2Row
	for time.Now().Before(deadline) {
		row, err = store.GetOperationV2(opID)
		require.NoError(t, err)
		if row != nil && row.Status == "completed" {
			break
		}
		require.NotContains(t, []string{"canceled", "failed", "interrupted_dropped"}, row.Status,
			"the operation must not end early (message %v)", row.ErrorMessage)
		time.Sleep(20 * time.Millisecond)
	}
	require.NotNil(t, row)
	require.Equal(t, "completed", row.Status, "the operation never finished")
	require.True(t, row.ErrorMessage == nil || !strings.HasPrefix(*row.ErrorMessage, state.TimeoutPrefix), "the operation must not carry a timeout")

	// The same row ran three attempts: two pauses, each a restart in place.
	assert.Equal(t, 2, row.ResumeCount, "two pauses, each resumed in place under the same id")
	rec, err := store.GetRunV3(opID)
	require.NoError(t, err)
	require.NotNil(t, rec)
	assert.Equal(t, state.V3Succeeded, rec.State)
	evts, err := store.ListRunEventsV3(opID, 0)
	require.NoError(t, err)
	var names []string
	for _, e := range evts {
		names = append(names, e.Event)
	}
	assert.NotContains(t, names, "v2_write", "every pause and resume was a legal v3 transition")
	assert.NotContains(t, names, "return_err")

	// One report, named for the one operation id, every row once.
	f, err := os.Open(filepath.Join(root, ".reports", "backfill-file-hashes-"+opID+".tsv"))
	require.NoError(t, err)
	defer f.Close()
	seen := map[string]bool{}
	sc := bufio.NewScanner(f)
	require.True(t, sc.Scan())
	require.Equal(t, "bucket\tfile_id\tbook_id\tpath\treason", sc.Text())
	for sc.Scan() {
		fields := strings.Split(sc.Text(), "\t")
		require.Len(t, fields, 5)
		require.Falsef(t, seen[fields[1]], "file %s is in the report twice", fields[1])
		seen[fields[1]] = true
	}
	assert.Len(t, seen, total, "every row is in the report")
}
