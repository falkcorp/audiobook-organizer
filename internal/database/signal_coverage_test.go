// file: internal/database/signal_coverage_test.go
// version: 1.0.0
// guid: 23717253-07aa-46c2-8759-470f05fa4c74
// last-edited: 2026-09-12

package database

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCountBookFileSignals_ShardedMatchesSequential runs the pool at several
// widths over rows whose expected tallies are computable by hand. A lost
// update, a shard boundary off by one, or a merge that drops a field shows up
// as a count that differs between widths.
func TestCountBookFileSignals_ShardedMatchesSequential(t *testing.T) {
	const n = 10_007 // prime, so shards never divide evenly
	rows := make([]BookFile, n)
	for i := range rows {
		rows[i] = BookFile{
			ID:       fmt.Sprintf("f%d", i),
			FileHash: map[bool]string{true: "h"}[i%2 == 0],
			Missing:  i%5 == 0,
			Duration: i % 3,
		}
	}
	at := func(i int) *BookFile { return &rows[i] }

	var want *BookFileSignalCoverage
	for _, workers := range []int{1, 2, 7, 32} {
		got, err := CountBookFileSignals(context.Background(), n, at, false, workers, "test")
		require.NoError(t, err)
		if want == nil {
			want = got
			assert.EqualValues(t, n, got.TotalBookFiles)
			assert.EqualValues(t, (n+1)/2, got.Signals[SignalFileHash].Have)
			assert.EqualValues(t, (n+4)/5, got.FileMissingRows)
			continue
		}
		assert.Equal(t, want.Signals, got.Signals, "workers=%d changed the tallies", workers)
		assert.Equal(t, want.FileMissingRows, got.FileMissingRows)
	}
	sc := want.Signals[SignalFileHash]
	assert.Equal(t, sc.Have+sc.Missing, want.TotalBookFiles)
	assert.Equal(t, sc.Missing, sc.MissingPresentOnDisk+sc.MissingFileMissing)
}

// TestGetBookFileSignalCoverage_DeepReadsStrippedFields proves the deep scan
// counts the fields memdb strips: a Seg0-only row, a raw-print row, and a
// failure reason. The memdb path must list the same fields as unavailable
// rather than reporting them as zero.
func TestGetBookFileSignalCoverage_DeepReadsStrippedFields(t *testing.T) {
	s := setupTestPebbleStore(t)
	book, err := s.CreateBook(&Book{Title: "Coverage", FilePath: "/test/coverage"})
	require.NoError(t, err)

	reason := "corrupt_audio"
	failedAt := time.Now()
	files := []*BookFile{
		{BookID: book.ID, FilePath: "/test/coverage/a.mp3", AcoustIDFingerprint: []byte{1, 2, 3, 4}, AcoustIDFingerprintDurationSec: 60},
		{BookID: book.ID, FilePath: "/test/coverage/c.mp3", FingerprintFailedAt: &failedAt, FingerprintFailureReason: &reason, Missing: true},
	}
	for _, f := range files {
		require.NoError(t, s.CreateBookFile(f))
	}
	// Seg0..6 are dropped by every store write since T020, so a Seg-only row
	// can only be LEGACY data. Plant two such rows straight into Pebble, the
	// way a pre-T020 binary left them.
	for i, seg := range []string{"AQAB", "AQAC"} {
		legacy := BookFile{ID: fmt.Sprintf("legacy-%d", i), BookID: book.ID, FilePath: "/test/coverage/legacy.mp3", AcoustIDSeg0: seg}
		if i == 1 {
			legacy.AcoustIDFingerprint = []byte{9, 9, 9, 9} // raw AND seg0: not seg-only
		}
		raw, err := json.Marshal(legacy)
		require.NoError(t, err)
		require.NoError(t, s.db.Set([]byte("book_file:"+book.ID+":"+legacy.ID), raw, nil))
	}

	cov, err := s.GetBookFileSignalCoverage(context.Background(), true, 4)
	require.NoError(t, err)
	assert.Equal(t, "pebble", cov.Source)
	assert.True(t, cov.ExactFingerprintFields)
	assert.EqualValues(t, 4, cov.TotalBookFiles)
	assert.EqualValues(t, 2, cov.Signals[SignalRawFingerprint].Have)
	assert.EqualValues(t, 1, cov.Signals[SignalSegOnlyFingerprint].Have, "the Seg0-only row is the one the old eligibility check skipped")
	assert.EqualValues(t, 2, cov.Signals["acoustid_seg0"].Have)
	assert.EqualValues(t, 1, cov.Signals[SignalFingerprintDuration].Have)
	assert.EqualValues(t, map[string]int64{"corrupt_audio": 1}, cov.FingerprintFailuresByReason)
	assert.EqualValues(t, 1, cov.Signals[SignalRawFingerprint].MissingFileMissing)

	s.WaitForWarmup()
	if !s.IsMemReady() {
		_, err := s.GetBookFileSignalCoverage(context.Background(), false, 4)
		assert.ErrorIs(t, err, ErrMemDBNotReady, "no memdb must refuse, not fall back to a Pebble scan")
		return
	}
	fast, err := s.GetBookFileSignalCoverage(context.Background(), false, 4)
	require.NoError(t, err)
	assert.Equal(t, "memdb", fast.Source)
	// The two legacy rows were planted under the store, so memdb never saw
	// them; the store-written pair is what the fast path counts.
	assert.EqualValues(t, 2, fast.TotalBookFiles)
	assert.EqualValues(t, 1, fast.Signals[SignalFingerprintDuration].Have)
	assert.Contains(t, fast.Unavailable, SignalRawFingerprint)
	_, has := fast.Signals["acoustid_seg0"]
	assert.False(t, has, "memdb rows have Seg0 stripped; counting it would report zero coverage")
}
