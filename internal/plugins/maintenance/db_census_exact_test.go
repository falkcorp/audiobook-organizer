// file: internal/plugins/maintenance/db_census_exact_test.go
// version: 1.2.0
// guid: 2e7cd14b-bfab-4670-8ca5-b918e74112e6
// last-edited: 2026-10-04

package maintenance

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// fakeCensusRunner records how it was called.
type fakeCensusRunner struct {
	last     *database.DBCensus
	runs     int
	gotBP    int64
	restarts int // DiscardExactCensusProgress calls
	gotRunID string
	progress *database.ExactCensusProgress
}

func (f *fakeCensusRunner) RunExactCensus(_ context.Context, opts database.ExactCensusOptions) (*database.DBCensus, error) {
	f.runs++
	f.gotBP = opts.ReadBytesPerSec
	f.gotRunID = opts.RunID
	if opts.Progress != nil {
		opts.Progress(database.ExactCensusProgress{FamiliesDone: 1, FamiliesAll: 2}, "book:")
	}
	c := &database.DBCensus{Kind: database.CensusKindExact, GeneratedAt: time.Now(),
		Families: []database.FamilyCensus{{Prefix: "book:", Keys: 3, Entries: 4}}}
	f.last = c
	return c, nil
}

func (f *fakeCensusRunner) LastExactCensus() (*database.DBCensus, error) { return f.last, nil }

func (f *fakeCensusRunner) ExactCensusInProgress() (*database.ExactCensusProgress, error) {
	return f.progress, nil
}

func (f *fakeCensusRunner) DiscardExactCensusProgress() error {
	f.restarts++
	f.progress = nil
	return nil
}

// checkpointReporter captures the op's checkpoint the way the registry
// stores it for a later resume.
type checkpointReporter struct {
	mockReporter
	state []byte
}

func (r *checkpointReporter) Checkpoint(state any) error {
	b, err := json.Marshal(state)
	r.state = b
	return err
}

// registryMerge mirrors registry.mergeJSONParams: checkpoint keys overwrite
// the original params on resume.
func registryMerge(t *testing.T, params, checkpoint []byte) dbCensusExactParams {
	t.Helper()
	m := map[string]any{}
	require.NoError(t, json.Unmarshal(params, &m))
	over := map[string]any{}
	require.NoError(t, json.Unmarshal(checkpoint, &over))
	for k, v := range over {
		m[k] = v
	}
	b, err := json.Marshal(m)
	require.NoError(t, err)
	var out dbCensusExactParams
	require.NoError(t, json.Unmarshal(b, &out))
	return out
}

func TestDBCensusExact_UsesTheConfiguredReadBudget(t *testing.T) {
	r := &fakeCensusRunner{}
	rep := &mockReporter{}
	require.NoError(t, runDBCensusExactWith(context.Background(), r, dbCensusExactParams{}, 0, 0, rep, "op1", time.Now()))
	require.Equal(t, 1, r.runs)
	require.Equal(t, int64(50)<<20, r.gotBP, "0 in config means the 50 MB/s default")

	r2 := &fakeCensusRunner{}
	require.NoError(t, runDBCensusExactWith(context.Background(), r2, dbCensusExactParams{}, 20, 0, rep, "op1", time.Now()))
	require.Equal(t, int64(20)<<20, r2.gotBP)

	r3 := &fakeCensusRunner{}
	require.NoError(t, runDBCensusExactWith(context.Background(), r3, dbCensusExactParams{ReadMBPerSec: 5}, 20, 0, rep, "op1", time.Now()))
	require.Equal(t, int64(5)<<20, r3.gotBP, "the op parameter overrides config")
}

// The cooldown stops back-to-back runs; force overrides it.
func TestDBCensusExact_CooldownRefusesBackToBackRuns(t *testing.T) {
	r := &fakeCensusRunner{}
	rep := &mockReporter{}
	now := time.Now()
	require.NoError(t, runDBCensusExactWith(context.Background(), r, dbCensusExactParams{}, 0, 6, rep, "op1", now))
	require.Equal(t, 1, r.runs)

	require.NoError(t, runDBCensusExactWith(context.Background(), r, dbCensusExactParams{}, 0, 6, rep, "op1", now.Add(time.Hour)))
	require.Equal(t, 1, r.runs, "a second run inside the cooldown must not start")
	found := false
	for _, l := range rep.logs {
		if strings.Contains(l, "cooldown") {
			found = true
		}
	}
	require.True(t, found, "the skip is logged: %v", rep.logs)

	require.NoError(t, runDBCensusExactWith(context.Background(), r, dbCensusExactParams{Force: true}, 0, 6, rep, "op1", now.Add(time.Hour)))
	require.Equal(t, 2, r.runs, "force runs inside the cooldown")
	require.Equal(t, 1, r.restarts, "force discards saved progress")

	require.NoError(t, runDBCensusExactWith(context.Background(), r, dbCensusExactParams{}, 0, 6, rep, "op1", r.last.GeneratedAt.Add(7*time.Hour)))
	require.Equal(t, 3, r.runs, "past the cooldown it runs")
}

func TestDBCensusExact_IsRegistered(t *testing.T) {
	def := (&Plugin{}).dbCensusExactDef()
	require.Equal(t, dbCensusExactOpID, def.ID)
	require.Nil(t, def.Schedule, "manual op")
	require.NotNil(t, def.Run)
	require.Contains(t, def.Capabilities, sdk.CapLibraryWrite)
}

func TestDBCensusExact_RestartParamDiscardsProgress(t *testing.T) {
	r := &fakeCensusRunner{}
	require.NoError(t, runDBCensusExactWith(context.Background(), r, dbCensusExactParams{Restart: true}, 0, 6, &mockReporter{}, "op1", time.Now()))
	require.Equal(t, 1, r.restarts)
}

// SF2: the registry re-dispatches a ResumeRestart op with its original params
// merged with its checkpoint. A forced run must not discard its own progress
// (or hit the cooldown) when it resumes.
func TestDBCensusExact_ForcedRunResumesItsOwnProgress(t *testing.T) {
	r := &fakeCensusRunner{last: &database.DBCensus{GeneratedAt: time.Now()}} // inside the cooldown
	rep := &checkpointReporter{}
	original := []byte(`{"force":true,"read_mb_per_sec":7}`)
	var args dbCensusExactParams
	require.NoError(t, json.Unmarshal(original, &args))
	require.NoError(t, runDBCensusExactWith(context.Background(), r, args, 0, 6, rep, "op-A", time.Now()))
	require.Equal(t, 1, r.restarts, "the first, forced dispatch starts fresh")
	require.Equal(t, 1, r.runs)
	require.Equal(t, "op-A", r.gotRunID)
	require.NotEmpty(t, rep.state, "the op checkpoints its resume flag")

	// Simulate the deploy: the run was interrupted with progress saved, and
	// the registry re-dispatches with params ⊕ checkpoint.
	r.progress = &database.ExactCensusProgress{RunID: "op-A", FamiliesDone: 50, FamiliesAll: 194}
	resumed := registryMerge(t, original, rep.state)
	require.False(t, resumed.Force)
	require.True(t, resumed.Resumed)
	require.Equal(t, 7, resumed.ReadMBPerSec)
	require.NoError(t, runDBCensusExactWith(context.Background(), r, resumed, 0, 6, rep, "op-A", time.Now()))
	require.Equal(t, 1, r.restarts, "the resumed dispatch keeps its progress")
	require.Equal(t, 2, r.runs, "and runs despite the cooldown")
	require.Equal(t, int64(7)<<20, r.gotBP)
}

// An unfinished, current run is resumed by a plain dispatch even inside the
// cooldown of the last completed one; a stale one is not.
func TestDBCensusExact_UnfinishedRunSkipsTheCooldown(t *testing.T) {
	r := &fakeCensusRunner{last: &database.DBCensus{GeneratedAt: time.Now()},
		progress: &database.ExactCensusProgress{RunID: "op-old"}}
	require.NoError(t, runDBCensusExactWith(context.Background(), r, dbCensusExactParams{}, 0, 6, &mockReporter{}, "op-new", time.Now()))
	require.Equal(t, 1, r.runs)

	r2 := &fakeCensusRunner{last: &database.DBCensus{GeneratedAt: time.Now()},
		progress: &database.ExactCensusProgress{RunID: "op-old", Stale: true}}
	require.NoError(t, runDBCensusExactWith(context.Background(), r2, dbCensusExactParams{}, 0, 6, &mockReporter{}, "op-new", time.Now()))
	require.Zero(t, r2.runs, "stale progress does not lift the cooldown")
}
