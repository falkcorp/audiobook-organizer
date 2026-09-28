// file: internal/metabatch/upgrade_outcome_log_test.go
// version: 1.0.0
// guid: 2c61318b-44fd-4559-8aa0-1575b32d9217
// last-edited: 2026-09-28

package metabatch

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// upgradeLogRecorder is an operations.ProgressReporter that keeps log lines.
type upgradeLogRecorder struct {
	mu    sync.Mutex
	lines []string
	lvls  []string
}

func (r *upgradeLogRecorder) UpdateProgress(int, int, string) error { return nil }
func (r *upgradeLogRecorder) Log(level, message string, _ *string) error {
	r.mu.Lock()
	r.lines = append(r.lines, message)
	r.lvls = append(r.lvls, level)
	r.mu.Unlock()
	return nil
}
func (r *upgradeLogRecorder) IsCanceled() bool { return false }

// Every book the upgrade looks at gets an op-log line: the upgrade with its
// from/to sources and candidate, or the reason it was skipped.
func TestRunUpgrade_LogsPerBookOutcome(t *testing.T) {
	t.Run("upgraded", func(t *testing.T) {
		f := newRankFixture()
		svc, _ := f.service(candidate("Audible", 0.95))
		rec := &upgradeLogRecorder{}
		_, err := svc.RunUpgrade(context.Background(), 200, rec)
		require.NoError(t, err)
		require.Equal(t, []string{`upgraded: "Project Hail Mary" — open_library → audible, now "Project Hail Mary" (score 0.95)`}, rec.lines)
		require.Equal(t, []string{"info"}, rec.lvls)
	})
	t.Run("gate refused", func(t *testing.T) {
		f := newRankFixture()
		svc, _ := f.service(candidate("Audible", 0.80))
		rec := &upgradeLogRecorder{}
		_, err := svc.RunUpgrade(context.Background(), 200, rec)
		require.NoError(t, err)
		require.Len(t, rec.lines, 1)
		require.True(t, strings.HasPrefix(rec.lines[0],
			`skipped: "Project Hail Mary" (source open_library) — `+upgradeSkipGateRefused+" (audible 0.80: "), rec.lines[0])
	})
	t.Run("none outrank", func(t *testing.T) {
		f := newRankFixture()
		svc, _ := f.service(candidate("Open Library", 0.99))
		rec := &upgradeLogRecorder{}
		_, err := svc.RunUpgrade(context.Background(), 200, rec)
		require.NoError(t, err)
		require.Equal(t, []string{`skipped: "Project Hail Mary" (source open_library) — ` + upgradeSkipNoneOutrank}, rec.lines)
	})
	t.Run("no results", func(t *testing.T) {
		f := newRankFixture()
		svc, _ := f.service()
		rec := &upgradeLogRecorder{}
		_, err := svc.RunUpgrade(context.Background(), 200, rec)
		require.NoError(t, err)
		require.Equal(t, []string{`skipped: "Project Hail Mary" (source open_library) — ` + upgradeSkipNoResults}, rec.lines)
	})
}

// The skip reasons tryUpgradeBook reports directly.
func TestTryUpgradeBook_SkipReasons(t *testing.T) {
	f := newRankFixture()
	svc, _ := f.service(candidate("Audible", 0.95))
	out, err := svc.tryUpgradeBook(context.Background(), "rank-1", "some_new_provider")
	require.NoError(t, err)
	require.Equal(t, upgradeSkipUnranked, out.Reason)

	f = newRankFixture()
	f.book.MetadataReviewStatus = new("no_match")
	svc, _ = f.service(candidate("Audible", 0.95))
	out, err = svc.tryUpgradeBook(context.Background(), "rank-1", "open_library")
	require.NoError(t, err)
	require.False(t, out.Upgraded)
	require.Equal(t, upgradeSkipMarkedNoMatch, out.Reason)
}
