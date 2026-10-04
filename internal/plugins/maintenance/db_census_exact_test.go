// file: internal/plugins/maintenance/db_census_exact_test.go
// version: 1.0.0
// guid: 2e7cd14b-bfab-4670-8ca5-b918e74112e6
// last-edited: 2026-10-04

package maintenance

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// fakeCensusRunner records how it was called.
type fakeCensusRunner struct {
	last  *database.DBCensus
	runs  int
	gotBP int64
}

func (f *fakeCensusRunner) RunExactCensus(_ context.Context, opts database.ExactCensusOptions) (*database.DBCensus, error) {
	f.runs++
	f.gotBP = opts.ReadBytesPerSec
	if opts.Progress != nil {
		opts.Progress(database.ExactCensusProgress{FamiliesDone: 1, FamiliesAll: 2}, "book:")
	}
	c := &database.DBCensus{Kind: database.CensusKindExact, GeneratedAt: time.Now(),
		Families: []database.FamilyCensus{{Prefix: "book:", Keys: 3, Entries: 4}}}
	f.last = c
	return c, nil
}

func (f *fakeCensusRunner) LastExactCensus() (*database.DBCensus, error) { return f.last, nil }

func TestDBCensusExact_UsesTheConfiguredReadBudget(t *testing.T) {
	r := &fakeCensusRunner{}
	rep := &mockReporter{}
	require.NoError(t, runDBCensusExactWith(context.Background(), r, dbCensusExactParams{}, 0, 0, rep, time.Now()))
	require.Equal(t, 1, r.runs)
	require.Equal(t, int64(50)<<20, r.gotBP, "0 in config means the 50 MB/s default")

	r2 := &fakeCensusRunner{}
	require.NoError(t, runDBCensusExactWith(context.Background(), r2, dbCensusExactParams{}, 20, 0, rep, time.Now()))
	require.Equal(t, int64(20)<<20, r2.gotBP)

	r3 := &fakeCensusRunner{}
	require.NoError(t, runDBCensusExactWith(context.Background(), r3, dbCensusExactParams{ReadMBPerSec: 5}, 20, 0, rep, time.Now()))
	require.Equal(t, int64(5)<<20, r3.gotBP, "the op parameter overrides config")
}

// The cooldown stops back-to-back runs; force overrides it.
func TestDBCensusExact_CooldownRefusesBackToBackRuns(t *testing.T) {
	r := &fakeCensusRunner{}
	rep := &mockReporter{}
	now := time.Now()
	require.NoError(t, runDBCensusExactWith(context.Background(), r, dbCensusExactParams{}, 0, 6, rep, now))
	require.Equal(t, 1, r.runs)

	require.NoError(t, runDBCensusExactWith(context.Background(), r, dbCensusExactParams{}, 0, 6, rep, now.Add(time.Hour)))
	require.Equal(t, 1, r.runs, "a second run inside the cooldown must not start")
	found := false
	for _, l := range rep.logs {
		if strings.Contains(l, "cooldown") {
			found = true
		}
	}
	require.True(t, found, "the skip is logged: %v", rep.logs)

	require.NoError(t, runDBCensusExactWith(context.Background(), r, dbCensusExactParams{Force: true}, 0, 6, rep, now.Add(time.Hour)))
	require.Equal(t, 2, r.runs, "force runs inside the cooldown")

	require.NoError(t, runDBCensusExactWith(context.Background(), r, dbCensusExactParams{}, 0, 6, rep, r.last.GeneratedAt.Add(7*time.Hour)))
	require.Equal(t, 3, r.runs, "past the cooldown it runs")
}

func TestDBCensusExact_IsRegistered(t *testing.T) {
	def := (&Plugin{}).dbCensusExactDef()
	require.Equal(t, dbCensusExactOpID, def.ID)
	require.Nil(t, def.Schedule, "manual op")
	require.NotNil(t, def.Run)
}
