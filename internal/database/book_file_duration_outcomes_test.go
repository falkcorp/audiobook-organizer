// file: internal/database/book_file_duration_outcomes_test.go
// version: 1.0.0
// guid: e8806232-72f3-4588-84ba-61dc9378c975
// last-edited: 2026-11-04

package database

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/testutil/durtest"
)

// TestDurationStateDerivesFromDurationOK walks the attempt count through the
// ceiling on both BookFile and BookFileCore, which must agree at every step.
func TestDurationStateDerivesFromDurationOK(t *testing.T) {
	cases := []struct {
		name string
		dur  *int
		ok   StageStatus
		want BookFileDurationState
	}{
		{"nothing recorded", nil, StageStatus{}, DurationUnprobed},
		{"Done but no value is still unprobed", nil, StageStatus{Done: true}, DurationUnprobed},
		{"one failed attempt", nil, StageStatus{Attempts: 1, LastError: "exit status 1"}, DurationProbeFailed},
		{"two failed attempts", nil, StageStatus{Attempts: 2, LastError: "exit status 1"}, DurationProbeFailed},
		{"ceiling reached", nil, StageStatus{Attempts: DeepScanMaxAttempts, LastError: "exit status 1"}, DurationUnreadable},
		{"past the ceiling", nil, StageStatus{Attempts: DeepScanMaxAttempts + 4, LastError: "exit status 1"}, DurationUnreadable},
		{"a known value outranks old failures", Secs(27000), StageStatus{Attempts: DeepScanMaxAttempts, LastError: "stale"}, DurationKnown},
		{"known and Done", Secs(27000), StageStatus{Done: true}, DurationKnown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := BookFile{ID: "f1", Duration: tc.dur, Scan: ScanState{DurationOK: tc.ok}}
			c := BookFileCore{ID: "f1", Duration: tc.dur, Scan: ScanState{DurationOK: tc.ok}}
			require.Equal(t, tc.want, f.DurationState(), "BookFile")
			require.Equal(t, tc.want, c.DurationState(), "BookFileCore")
			require.Equal(t, tc.want, f.Core().DurationState(), "Core() projection")
		})
	}
}

func TestSetMeasuredDurationMarksStageDoneAndClearsFailures(t *testing.T) {
	failed := ScanState{NeedsDeep: true, DurationOK: StageStatus{Attempts: 2, LastError: "probe returned 0s"}}
	f := BookFile{ID: "f1", Scan: failed}
	c := BookFileCore{ID: "f1", Scan: failed}
	require.NoError(t, f.SetMeasuredDuration(27000))
	require.NoError(t, c.SetMeasuredDuration(27000))
	for name, got := range map[string]ScanState{"BookFile": f.Scan, "BookFileCore": c.Scan} {
		require.Equal(t, StageStatus{Done: true}, got.DurationOK, name)
		require.True(t, got.NeedsDeep, "%s: only the duration stage changes", name)
	}
	require.Equal(t, DurationKnown, f.DurationState())
}

func TestSetMeasuredDurationRejectionKeepsFailureRecord(t *testing.T) {
	rec := StageStatus{Attempts: 1, LastError: "exit status 1"}
	f := BookFile{Scan: ScanState{DurationOK: rec}}
	c := BookFileCore{Scan: ScanState{DurationOK: rec}}
	require.Error(t, f.SetMeasuredDuration(0))
	require.Error(t, c.SetMeasuredDuration(-3))
	require.Equal(t, rec, f.Scan.DurationOK)
	require.Equal(t, rec, c.Scan.DurationOK)
}

func TestRecordDurationProbeFailureAgreesAcrossBookFileAndCore(t *testing.T) {
	var f BookFile
	var c BookFileCore
	for i := 1; i <= DeepScanMaxAttempts; i++ {
		f.RecordDurationProbeFailure("exit status 1")
		c.RecordDurationProbeFailure("exit status 1")
		require.Equal(t, i, f.Scan.DurationOK.Attempts)
		require.Equal(t, f.Scan.DurationOK, c.Scan.DurationOK)
		require.Equal(t, f.DurationState(), c.DurationState())
		require.Nil(t, f.Duration)
		require.Nil(t, c.Duration)
		require.False(t, f.Scan.DurationOK.Done)
	}
	require.Equal(t, DurationUnreadable, f.DurationState())
	require.Equal(t, "exit status 1", f.Scan.DurationOK.LastError)

	f.RecordDurationProbeFailure(DurationProbeTimeoutPrefix + "media probe timed out")
	require.True(t, IsDurationTimeoutError(f.Scan.DurationOK.LastError))
	require.False(t, IsDurationTimeoutError("exit status 1"))
}

func TestRecordDurationProbeFailureEmptyMessageStillSaysWhy(t *testing.T) {
	var f BookFile
	f.RecordDurationProbeFailure("")
	require.Equal(t, 1, f.Scan.DurationOK.Attempts)
	require.NotEmpty(t, f.Scan.DurationOK.LastError)
}

// A failed re-probe does not contradict a measurement: a Known row is left alone.
func TestRecordDurationProbeFailureIgnoresKnownRow(t *testing.T) {
	f := BookFile{Duration: Secs(900), Scan: ScanState{DurationOK: StageStatus{Done: true}}}
	c := BookFileCore{Duration: Secs(900), Scan: ScanState{DurationOK: StageStatus{Done: true}}}
	f.RecordDurationProbeFailure("exit status 1")
	c.RecordDurationProbeFailure("exit status 1")
	require.Equal(t, StageStatus{Done: true}, f.Scan.DurationOK)
	require.Equal(t, StageStatus{Done: true}, c.Scan.DurationOK)
	require.Equal(t, 900, durtest.Sec(f.Duration))
}

func TestResetDurationZeroesDurationOK(t *testing.T) {
	scan := ScanState{NeedsDeep: true, Tagged: StageStatus{Done: true}, DurationOK: StageStatus{Attempts: 3, LastError: "x"}}
	f := BookFile{Duration: nil, Scan: scan}
	c := BookFileCore{Scan: scan}
	f.ResetDuration()
	c.ResetDuration()
	for name, got := range map[string]ScanState{"BookFile": f.Scan, "BookFileCore": c.Scan} {
		require.Equal(t, StageStatus{}, got.DurationOK, name)
		require.True(t, got.NeedsDeep, name)
		require.Equal(t, StageStatus{Done: true}, got.Tagged, name)
	}
	k := BookFile{Duration: Secs(60), Scan: ScanState{DurationOK: StageStatus{Done: true}}}
	k.ResetDuration()
	require.Equal(t, DurationUnprobed, k.DurationState())
	require.Equal(t, StageStatus{}, k.Scan.DurationOK)
}

// The bug this pins: Scan was restored from the stored row only when the
// incoming Scan was wholly zero, so a scanner row that carried only a fresh
// DurationOK failure wiped every other stage and every earlier attempt. Run
// against the real store because the merge is what is under test.
func TestUpsertWithDurationFailureKeepsOtherScanStagesAndCountsToUnreadable(t *testing.T) {
	s := openRescanStore(t)
	book, err := s.CreateBook(&Book{Title: "Year One"})
	require.NoError(t, err)
	stored := &BookFile{
		BookID: book.ID, FilePath: "/lib/Year One/01.m4b", FileHash: "hash-a",
		Scan: ScanState{
			NeedsDeep: true, HashStale: true, Attempts: 1, LastError: "deep",
			Tagged:     StageStatus{Done: true},
			DurationOK: StageStatus{Attempts: DeepScanMaxAttempts - 1, LastError: "exit status 1"},
		},
	}
	require.NoError(t, s.CreateBookFile(stored))

	// The caller seeded the stored stage and added one more failed attempt, as
	// the scanner does for a file it re-probes with the same bytes.
	row := &BookFile{BookID: book.ID, FilePath: "/lib/Year One/01.m4b", FileHash: "hash-a"}
	row.Scan.DurationOK = StageStatus{Attempts: DeepScanMaxAttempts - 1, LastError: "exit status 1"}
	row.RecordDurationProbeFailure("probe returned 0s")
	require.NoError(t, s.BatchUpsertScannedBookFiles([]ScannedBookFile{{File: row, Present: true}}))

	after := onlyBookFile(t, s, book.ID)
	require.Equal(t, DurationUnreadable, after.DurationState())
	require.Equal(t, StageStatus{Attempts: DeepScanMaxAttempts, LastError: "probe returned 0s"}, after.Scan.DurationOK)
	require.True(t, after.Scan.NeedsDeep, "NeedsDeep wiped")
	require.True(t, after.Scan.HashStale, "HashStale wiped")
	require.Equal(t, 1, after.Scan.Attempts, "deep-pass attempts wiped")
	require.Equal(t, StageStatus{Done: true}, after.Scan.Tagged, "Tagged stage wiped")

	// A bare scanner row (no probe) keeps all of it.
	bare := &BookFile{BookID: book.ID, FilePath: "/lib/Year One/01.m4b", FileHash: "hash-a"}
	require.NoError(t, s.BatchUpsertScannedBookFiles([]ScannedBookFile{{File: bare, Present: true}}))
	require.Equal(t, after.Scan, onlyBookFile(t, s, book.ID).Scan)

	// A later successful measurement replaces the failure record.
	ok := &BookFile{BookID: book.ID, FilePath: "/lib/Year One/01.m4b", FileHash: "hash-a"}
	require.NoError(t, ok.SetMeasuredDuration(27000))
	require.NoError(t, s.BatchUpsertScannedBookFiles([]ScannedBookFile{{File: ok, Present: true}}))
	final := onlyBookFile(t, s, book.ID)
	require.Equal(t, DurationKnown, final.DurationState())
	require.Equal(t, StageStatus{Done: true}, final.Scan.DurationOK)
	require.True(t, final.Scan.NeedsDeep)
}

// A replaced file: the old file's measurement is reset, and a failure the
// replacement's probe recorded (a different DurationOK) is the caller's own
// record about the new bytes and is kept, with the other stages.
func TestUpsertReplacedFileFailureReplacesOldDurationStage(t *testing.T) {
	s := openRescanStore(t)
	book, err := s.CreateBook(&Book{Title: "The Killing Joke"})
	require.NoError(t, err)
	require.NoError(t, s.CreateBookFile(&BookFile{
		BookID: book.ID, FilePath: "/lib/The Killing Joke/01.m4b", FileHash: "hash-old",
		Duration: Secs(5400), Codec: "aac",
		Scan: ScanState{NeedsDeep: true, DurationOK: StageStatus{Done: true}},
	}))

	row := &BookFile{BookID: book.ID, FilePath: "/lib/The Killing Joke/01.m4b", FileHash: "hash-new"}
	row.RecordDurationProbeFailure("exit status 1")
	require.NoError(t, s.BatchUpsertScannedBookFiles([]ScannedBookFile{{File: row, Present: true}}))

	after := onlyBookFile(t, s, book.ID)
	require.Nil(t, after.Duration, "the old file's duration must not describe the replacement")
	require.Equal(t, StageStatus{Attempts: 1, LastError: "exit status 1"}, after.Scan.DurationOK)
	require.True(t, after.Scan.NeedsDeep)
	require.Equal(t, DurationProbeFailed, after.DurationState())
}
