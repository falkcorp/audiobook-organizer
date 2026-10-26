// file: internal/database/retired_census_test.go
// version: 1.0.0
// guid: 91c4e7a2-5d38-4f16-8b0a-3e72d1c6f405
// last-edited: 2026-10-26

package database

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// retiredCoverageFixture: a trashed shell that keeps one live-keyed row (A), a
// row of its that is retired into a holder (B), and the holder's own row (C).
// Coverage must count A and C only: nothing backfills B.
func retiredCoverageFixture(t *testing.T) *retiredFixture {
	t.Helper()
	fx := newRetiredFixture(t)
	fx.create(t, fx.shell.ID, BookFile{FilePath: "/lib/Gotham/ret/cov/a.mp3"})
	b := fx.create(t, fx.shell.ID, BookFile{FilePath: "/lib/Gotham/ret/cov/b.mp3", AcoustIDFingerprintDurationSec: 30})
	fx.create(t, fx.holder.ID, BookFile{FilePath: "/lib/Gotham/ret/cov/c.mp3"})
	require.NoError(t, fx.retire(t, b))
	fx.ps.WaitForWarmup()
	require.True(t, fx.ps.IsMemReady())
	return fx
}

// The signal coverage report leaves retired-version rows out of its population
// on every path (memdb, deep Pebble scan), so a retire does not add gaps no
// backfill will fill.
func TestSignalCoverage_ExcludesRetiredVersionRows(t *testing.T) {
	fx := retiredCoverageFixture(t)
	for _, deep := range []bool{false, true} {
		cov, err := fx.ps.GetBookFileSignalCoverage(context.Background(), deep, 2)
		require.NoError(t, err, "deep=%v", deep)
		require.EqualValues(t, 2, cov.TotalBookFiles, "deep=%v source=%s", deep, cov.Source)
	}
}

// The fingerprint-window report counts the same population as the signal
// report on both paths.
func TestFingerprintWindowCoverage_ExcludesRetiredVersionRows(t *testing.T) {
	fx := retiredCoverageFixture(t)
	fast, err := fx.ps.GetFingerprintWindowCoverage(context.Background(), false, WindowCoverageCriteria{}, 2)
	require.NoError(t, err)
	require.EqualValues(t, 2, fast.PresentFiles, "memdb path")
	deep, err := fx.ps.GetFingerprintWindowCoverage(context.Background(), true,
		WindowCoverageCriteria{Pipeline: "p", WindowSet: "w"}, 2)
	require.NoError(t, err)
	require.EqualValues(t, 2, deep.PresentFiles, "deep path")
}

// The census reports a retired version's rows under their own figure, and a
// retired book's RetiredBookFiles no longer counts them (they left its key
// range), so the trash's files are not reported as reclaimed when only moved.
func TestDBCensus_CountsRetiredVersionFiles(t *testing.T) {
	fx := retiredCoverageFixture(t)
	shell, err := fx.ps.GetBookByID(fx.shell.ID)
	require.NoError(t, err)
	yes := true
	shell.MarkedForDeletion = &yes
	_, err = fx.ps.UpdateBook(shell.ID, shell)
	require.NoError(t, err)

	c, err := fx.ps.DBCensus(context.Background(), CensusOptions{Fresh: true})
	require.NoError(t, err)
	require.NotNil(t, c.Retired, "notes: %v", c.Notes)
	require.Equal(t, 1, c.Retired.RetiredVersionFiles)
	require.Equal(t, 1, c.Retired.RetiredBookFiles, "only A is still the trashed shell's own row")
	require.Equal(t, 1, c.Signals.FilesWithFingerprint, "the stored print on the retired row still exists")
}
