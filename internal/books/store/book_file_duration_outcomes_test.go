// file: internal/books/store/book_file_duration_outcomes_test.go
// version: 1.0.1
// guid: e8806232-72f3-4588-84ba-61dc9378c975
// last-edited: 2026-11-09

package store

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/testutil/durtest"
)

// TestDurationStateDerivesFromDurationOK walks the attempt count through the
// ceiling on both BookFile and BookFileCore, which must agree at every step.
//
// Pins current behaviour (characterisation test, not a guard for a reported bug).
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

// Pins current behaviour (characterisation test, not a guard for a reported bug).
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

// A rejected value must change NOTHING: neither a failure record nor a value
// already measured. The check that fails is the order of writes in
// SetMeasuredDuration (a stage or pointer written before validation).
func TestSetMeasuredDurationRejectionChangesNothing(t *testing.T) {
	rows := map[string]ScanState{
		"failed row": {NeedsDeep: true, DurationOK: StageStatus{Attempts: 1, LastError: "exit status 1"}},
		"known row":  {DurationOK: StageStatus{Done: true}},
		"bare row":   {},
	}
	for name, scan := range rows {
		for _, bad := range []int{0, -3} {
			t.Run(name, func(t *testing.T) {
				var dur *int
				if name == "known row" {
					dur = Secs(27000)
				}
				f := BookFile{Duration: dur, Scan: scan}
				c := BookFileCore{Duration: dur, Scan: scan}
				require.Error(t, f.SetMeasuredDuration(bad))
				require.Error(t, c.SetMeasuredDuration(bad))
				require.Equal(t, scan, f.Scan, "BookFile.Scan")
				require.Equal(t, scan, c.Scan, "BookFileCore.Scan")
				require.Same(t, dur, f.Duration, "BookFile.Duration pointer replaced")
				require.Same(t, dur, c.Duration, "BookFileCore.Duration pointer replaced")
			})
		}
	}
}

// Pins current behaviour (characterisation test, not a guard for a reported bug).
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

// Pins current behaviour (characterisation test, not a guard for a reported bug).
func TestRecordDurationProbeFailureEmptyMessageStillSaysWhy(t *testing.T) {
	var f BookFile
	f.RecordDurationProbeFailure("")
	require.Equal(t, 1, f.Scan.DurationOK.Attempts)
	require.NotEmpty(t, f.Scan.DurationOK.LastError)
}

// A failed re-probe does not contradict a measurement: a Known row is left alone.
//
// Pins current behaviour (characterisation test, not a guard for a reported bug).
func TestRecordDurationProbeFailureIgnoresKnownRow(t *testing.T) {
	f := BookFile{Duration: Secs(900), Scan: ScanState{DurationOK: StageStatus{Done: true}}}
	c := BookFileCore{Duration: Secs(900), Scan: ScanState{DurationOK: StageStatus{Done: true}}}
	f.RecordDurationProbeFailure("exit status 1")
	c.RecordDurationProbeFailure("exit status 1")
	require.Equal(t, StageStatus{Done: true}, f.Scan.DurationOK)
	require.Equal(t, StageStatus{Done: true}, c.Scan.DurationOK)
	require.Equal(t, 900, durtest.Sec(f.Duration))
}

// Pins current behaviour (characterisation test, not a guard for a reported bug).
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

// S1: whether a replacement's DurationOK is "the caller's own record" is decided
// by whether the caller SET it, not by comparing it with the stored value. A
// replaced file that fails with exactly the text the old file's record carries
// is still a fresh first failure of the new bytes.
func TestUpsertReplacedFileFailureEqualToStoredRecordIsStillFresh(t *testing.T) {
	s := openRescanStore(t)
	book, err := s.CreateBook(&Book{Title: "Batman: Year One"})
	require.NoError(t, err)
	require.NoError(t, s.CreateBookFile(&BookFile{
		BookID: book.ID, FilePath: "/lib/Year One/01.m4b", FileHash: "hash-old",
		Scan: ScanState{DurationOK: StageStatus{Attempts: 1, LastError: "exit status 1"}},
	}))

	row := &BookFile{BookID: book.ID, FilePath: "/lib/Year One/01.m4b", FileHash: "hash-new"}
	row.RecordDurationProbeFailure("exit status 1") // identical to the stored record
	require.Equal(t, StageStatus{Attempts: 1, LastError: "exit status 1"}, row.Scan.DurationOK)
	require.NoError(t, s.BatchUpsertScannedBookFiles([]ScannedBookFile{{File: row, Present: true}}))

	after := onlyBookFile(t, s, book.ID)
	require.Equal(t, 1, after.Scan.DurationOK.Attempts, "the replacement's failure was wiped as if it were the old record")
	require.Equal(t, "exit status 1", after.Scan.DurationOK.LastError)
	require.Equal(t, DurationProbeFailed, after.DurationState())
}

// The same replaced file with NO record of its own: the old file's record
// (failed attempts or a measurement) describes bytes that are gone, and must not
// be restored onto the replacement.
func TestUpsertReplacedFileWithoutRecordDropsOldStage(t *testing.T) {
	for name, old := range map[string]StageStatus{
		"old failures": {Attempts: 2, LastError: "exit status 1"},
		"old measured": {Done: true},
	} {
		t.Run(name, func(t *testing.T) {
			s := openRescanStore(t)
			book, err := s.CreateBook(&Book{Title: "Batman: Hush"})
			require.NoError(t, err)
			f := &BookFile{BookID: book.ID, FilePath: "/lib/Hush/01.m4b", FileHash: "hash-old", Scan: ScanState{NeedsDeep: true, DurationOK: old}}
			if old.Done {
				f.Duration = Secs(5400)
			}
			require.NoError(t, s.CreateBookFile(f))

			row := &BookFile{BookID: book.ID, FilePath: "/lib/Hush/01.m4b", FileHash: "hash-new"}
			require.NoError(t, s.BatchUpsertScannedBookFiles([]ScannedBookFile{{File: row, Present: true}}))

			after := onlyBookFile(t, s, book.ID)
			require.Nil(t, after.Duration)
			require.Equal(t, StageStatus{}, after.Scan.DurationOK)
			require.Equal(t, DurationUnprobed, after.DurationState())
			require.True(t, after.Scan.NeedsDeep, "pipeline state is kept across a replacement")
		})
	}
}

// S2: a stored Duration and its stage record never disagree after a write.
// Path 1: an upsert that carries a Duration but no DurationOK, over a stored row
// with a failure record. Path 2: the scanner seeded the stored failure onto a
// bare row, the probe failed again, and the merge kept the STORED Duration
// because the hash did not change.
func TestUpsertKnownDurationNeverSitsBesideAFailureRecord(t *testing.T) {
	stale := StageStatus{Attempts: 2, LastError: "exit status 1"}
	t.Run("upsert carries a Duration but no stage", func(t *testing.T) {
		s := openRescanStore(t)
		book, err := s.CreateBook(&Book{Title: "Batman: The Long Halloween"})
		require.NoError(t, err)
		require.NoError(t, s.CreateBookFile(&BookFile{
			BookID: book.ID, FilePath: "/lib/TLH/01.m4b", FileHash: "hash-a", Scan: ScanState{DurationOK: stale},
		}))
		row := &BookFile{BookID: book.ID, FilePath: "/lib/TLH/01.m4b", FileHash: "hash-a", Duration: Secs(5400)}
		require.NoError(t, s.BatchUpsertScannedBookFiles([]ScannedBookFile{{File: row, Present: true}}))

		after := onlyBookFile(t, s, book.ID)
		require.Equal(t, DurationKnown, after.DurationState())
		require.Equal(t, StageStatus{Done: true}, after.Scan.DurationOK, "a value beside a stale failure record")
	})
	t.Run("seeded failure, stored Duration kept by the merge", func(t *testing.T) {
		s := openRescanStore(t)
		book, err := s.CreateBook(&Book{Title: "Batman: The Dark Knight Returns"})
		require.NoError(t, err)
		require.NoError(t, s.CreateBookFile(&BookFile{
			BookID: book.ID, FilePath: "/lib/DKR/01.m4b", FileHash: "hash-a",
			Duration: Secs(5400), Scan: ScanState{DurationOK: StageStatus{Done: true}},
		}))
		row := &BookFile{BookID: book.ID, FilePath: "/lib/DKR/01.m4b", FileHash: "hash-a"}
		row.Scan.DurationOK = StageStatus{Done: true}   // seeded from the stored row
		row.RecordDurationProbeFailure("exit status 1") // the re-probe failed
		require.Equal(t, 1, row.Scan.DurationOK.Attempts)
		require.NoError(t, s.BatchUpsertScannedBookFiles([]ScannedBookFile{{File: row, Present: true}}))

		after := onlyBookFile(t, s, book.ID)
		require.Equal(t, 5400, durtest.Sec(after.Duration), "the stored measurement is kept")
		require.Equal(t, StageStatus{Done: true}, after.Scan.DurationOK, "the failure record beside the kept value")
		require.Equal(t, DurationKnown, after.DurationState())
	})
	t.Run("full-row replace with a Duration and no stage", func(t *testing.T) {
		s := openRescanStore(t)
		book, err := s.CreateBook(&Book{Title: "Batman: Earth One"})
		require.NoError(t, err)
		f := &BookFile{BookID: book.ID, FilePath: "/lib/E1/01.m4b", FileHash: "hash-a", Scan: ScanState{DurationOK: stale}}
		require.NoError(t, s.CreateBookFile(f))
		cur := onlyBookFile(t, s, book.ID)
		cur.Duration = Secs(60)
		cur.Scan.DurationOK = StageStatus{}
		require.NoError(t, s.UpdateBookFile(cur.ID, &cur))
		require.Equal(t, StageStatus{Done: true}, onlyBookFile(t, s, book.ID).Scan.DurationOK)
	})
}

// N-d: the two create paths have nothing to merge with, so they make the
// Duration/DurationOK pair consistent themselves: a value comes with exactly
// Done:true, no value comes with Done false (a failure record is kept).
func TestCreateBookFilePathsNormaliseTheDurationStage(t *testing.T) {
	stale := StageStatus{Attempts: 2, LastError: "exit status 1"}
	failure := StageStatus{Attempts: 1, LastError: "probe returned 0s"}
	type tc struct {
		name string
		row  BookFile
		want StageStatus
	}
	cases := []tc{
		{"value beside a stale failure record", BookFile{FilePath: "/lib/A/01.m4b", Duration: Secs(60), Scan: ScanState{DurationOK: stale}}, StageStatus{Done: true}},
		{"value with no stage", BookFile{FilePath: "/lib/A/02.m4b", Duration: Secs(60)}, StageStatus{Done: true}},
		{"Done with no value", BookFile{FilePath: "/lib/A/03.m4b", Scan: ScanState{DurationOK: StageStatus{Done: true}}}, StageStatus{}},
		{"non-positive value is no value", BookFile{FilePath: "/lib/A/04.m4b", Duration: Secs(0), Scan: ScanState{DurationOK: StageStatus{Done: true}}}, StageStatus{}},
		{"failure record with no value is kept", BookFile{FilePath: "/lib/A/05.m4b", Scan: ScanState{DurationOK: failure}}, failure},
	}
	for _, c := range cases {
		t.Run("CreateBookFile/"+c.name, func(t *testing.T) {
			s := openRescanStore(t)
			book, err := s.CreateBook(&Book{Title: "Batman: Hush"})
			require.NoError(t, err)
			row := c.row
			row.BookID = book.ID
			require.NoError(t, s.CreateBookFile(&row))
			require.Equal(t, c.want, onlyBookFile(t, s, book.ID).Scan.DurationOK)
		})
		t.Run("BatchCreateBookFiles/"+c.name, func(t *testing.T) {
			s := openRescanStore(t)
			book, err := s.CreateBook(&Book{Title: "Batman: Hush"})
			require.NoError(t, err)
			row := c.row
			row.BookID = book.ID
			require.NoError(t, s.BatchCreateBookFiles([]*BookFile{&row}))
			require.Equal(t, c.want, onlyBookFile(t, s, book.ID).Scan.DurationOK)
		})
	}
}

// N1: every ScanState field is classified in scanStateFieldClasses, the way
// every BookFile field is in bookFileFieldClasses, so a stage added later must
// say what invalidates it.
func TestScanStateFieldClasses_EveryFieldClassified(t *testing.T) {
	typ := reflect.TypeFor[ScanState]()
	fields := map[string]bool{}
	for field := range typ.Fields() {
		fields[field.Name] = true
		if _, ok := scanStateFieldClasses[field.Name]; !ok {
			t.Errorf("ScanState.%s is not classified in scanStateFieldClasses (internal/books/store/bookfile_merge.go): "+
				"decide whether a changed file's upsert keeps it (derivedNone), drops it with the bytes (derivedBytes) or with the audio (derivedAudio)", field.Name)
		}
	}
	for name := range scanStateFieldClasses {
		if !fields[name] {
			t.Errorf("scanStateFieldClasses names %q, which is not a ScanState field", name)
		}
	}
}

// restoreScanFields drives off the table: a zero incoming field is restored
// from the stored row unless its derivation fired, and a field the caller set
// is never replaced.
func TestRestoreScanFields_FollowsTheClassTable(t *testing.T) {
	var stored ScanState
	fillValueNonZero(t, reflect.ValueOf(&stored).Elem(), "ScanState")
	verdicts := map[string]bookFileContentVerdict{
		"unchanged":     {},
		"bytes changed": {bytesChanged: true},
		"audio changed": {bytesChanged: true, audioChanged: true},
	}
	for vname, v := range verdicts {
		t.Run(vname, func(t *testing.T) {
			var in ScanState
			restoreScanFields(&in, &stored, v)
			got := reflect.ValueOf(in)
			want := reflect.ValueOf(stored)
			for i, derived := range scanStateDerivationByIndex {
				name := got.Type().Field(i).Name
				dropped := (derived == derivedBytes && v.bytesChanged) || (derived == derivedAudio && v.audioChanged)
				if dropped {
					require.True(t, got.Field(i).IsZero(), "%s must not be restored (%s)", name, vname)
				} else {
					require.True(t, reflect.DeepEqual(got.Field(i).Interface(), want.Field(i).Interface()), "%s must be restored (%s)", name, vname)
				}
			}
		})
	}
	t.Run("a field the caller set is never replaced", func(t *testing.T) {
		in := ScanState{FPWindowed: StageStatus{Attempts: 1, LastError: "mine"}}
		restoreScanFields(&in, &stored, bookFileContentVerdict{bytesChanged: true, audioChanged: true})
		require.Equal(t, StageStatus{Attempts: 1, LastError: "mine"}, in.FPWindowed)
		in = ScanState{FPWindowed: StageStatus{Attempts: 1, LastError: "mine"}}
		restoreScanFields(&in, &stored, bookFileContentVerdict{})
		require.Equal(t, StageStatus{Attempts: 1, LastError: "mine"}, in.FPWindowed)
	})
}
