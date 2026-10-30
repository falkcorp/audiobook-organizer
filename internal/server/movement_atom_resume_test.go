// file: internal/server/movement_atom_resume_test.go
// version: 1.0.0
// guid: 117ace2e-1939-411e-8876-5015c7e30f68
// last-edited: 2026-10-30

package server

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/walkresume"
)

func movementResumeFixture(t *testing.T, n int) *Server {
	t.Helper()
	store, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	root := t.TempDir()
	prevRoot := config.AppConfig.RootDir
	config.AppConfig.RootDir = root
	t.Cleanup(func() { config.AppConfig.RootDir = prevRoot })
	withAppDirConfig(t, root)
	// "Batman" and "Batman-Begins" straddle '/' in string order: the case a
	// cursor over a raw WalkDir order gets wrong.
	dirs := []string{"Batman", "Batman-Begins", "Robin", "Batman/Year One"}
	for i := 0; i < n; i++ {
		f := filepath.Join(root, dirs[i%len(dirs)], fmt.Sprintf("book-%03d.m4b", i))
		require.NoError(t, os.MkdirAll(filepath.Dir(f), 0o755))
		require.NoError(t, os.WriteFile(f, []byte("not a real m4b"), 0o644))
	}
	return &Server{store: store}
}

// A cleanup cancelled mid-walk checkpoints a cursor; the resumed run starts
// after it. Each unreadable fixture counts once as Failed, so the carried plus
// the new Visited equals the library size only if no earlier file was visited
// again.
func TestStripMovementAtoms_CancelledRunCheckpointsAndResumeSkipsDoneFiles(t *testing.T) {
	const n = 60
	srv := movementResumeFixture(t, n)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var states []walkresume.State
	first := walkresume.Run{
		Checkpoint: func(st walkresume.State) { states = append(states, st) },
		// Progress fires at every 25th file, inside the file's own work.
		Progress: func(processed, _ int, _ string) {
			if processed >= 25 {
				cancel()
			}
		},
	}
	res := srv.stripMovementAtoms(ctx, walkresume.WithRun(first))
	require.False(t, res.Completed, "a cancelled walk must not report completion")
	done, _ := srv.Ops().GetSetting(movementAtomCleanupKey)
	require.Nil(t, done, "a cancelled walk must not set the done flag")
	require.NotEmpty(t, states, "no checkpoint was written before the cancel")
	last := states[len(states)-1]
	require.NotEmpty(t, last.Cursor)
	require.Equal(t, 2*walkresume.CheckpointEvery, last.Failed, "the checkpoint carries the files done so far")

	res = srv.stripMovementAtoms(context.Background(), walkresume.WithRun(walkresume.Run{Resume: last}))
	require.True(t, res.Completed, "%+v", res)
	require.Equal(t, n, res.Visited, "resumed Visited = carried + new: more means files before the cursor were visited again")
	require.Equal(t, n, res.Failed)
	done, err := srv.Ops().GetSetting(movementAtomCleanupKey)
	require.NoError(t, err)
	require.Equal(t, "true", done.Value)
}

// A run told to yield stops after a checkpoint with Yielded set and the done
// flag unset, and says so through StripMovementAtoms as walkresume.ErrYield.
func TestStripMovementAtoms_YieldStopsAfterCheckpoint(t *testing.T) {
	srv := movementResumeFixture(t, 30)
	var states []walkresume.State
	run := walkresume.Run{
		Checkpoint: func(st walkresume.State) { states = append(states, st) },
		YieldDue:   func() bool { return true },
	}
	_, err := srv.StripMovementAtoms(context.Background(), run)
	require.ErrorIs(t, err, walkresume.ErrYield)
	require.Len(t, states, 1)
	done, _ := srv.Ops().GetSetting(movementAtomCleanupKey)
	require.Nil(t, done)

	alreadyDone, err := srv.StripMovementAtoms(context.Background(), walkresume.Run{Resume: states[0]})
	require.NoError(t, err)
	require.False(t, alreadyDone)
	done, err = srv.Ops().GetSetting(movementAtomCleanupKey)
	require.NoError(t, err)
	require.Equal(t, "true", done.Value)
}
