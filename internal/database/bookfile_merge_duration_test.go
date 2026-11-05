// file: internal/database/bookfile_merge_duration_test.go
// version: 1.0.1
// guid: 0b8e5d73-1c42-4f96-a7d8-93e4f6c2a105
// last-edited: 2026-11-05

package database

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/testutil/durtest"
)

// storedMeasuredRow is a stored row whose duration stage is Done and whose
// other stages are set, so a reset of the wrong stage shows.
func storedMeasuredRow() *BookFile {
	return &BookFile{
		ID: "f1", BookID: "b1", FilePath: "/lib/Year One/01.m4b",
		FileHash: "hash-old", Duration: Secs(100), Codec: "aac",
		Scan: ScanState{
			NeedsDeep:  true,
			Tagged:     StageStatus{Done: true},
			DurationOK: StageStatus{Done: true},
			FPWindowed: StageStatus{Done: true, Attempts: 1, LastError: "window"},
		},
	}
}

func mergeScannerRow(incoming *BookFile) bookFileContentVerdict {
	return mergeBookFileFromStored(incoming, storedMeasuredRow(), bookFileWriteUpsert, presenceSeen)
}

// Duration is preserveAudio: an incoming nil means "not loaded", so a scanner
// row that carries no duration keeps the stored one while the bytes are the
// same, and the duration stage with it.
func TestMergeNilDurationKeepsStoredWhenBytesUnchanged(t *testing.T) {
	in := &BookFile{ID: "f1", BookID: "b1", FileHash: "hash-old"}
	v := mergeScannerRow(in)
	require.False(t, v.bytesChanged)
	require.Equal(t, 100, durtest.Sec(in.Duration))
	require.True(t, in.Scan.DurationOK.Done)
}

// A changed hash with nothing measured on the replacement: Duration is dropped
// with the other audio-derived fields, and the stored duration stage (a
// measurement of the old file) is reset with it, whether the incoming row is
// the scanner's bare row or a read-modify-write copy of the stored one. The
// pipeline stages keep their stored state; the audio-derived ones (FPWindowed,
// FPFull) go with the audio.
func TestMergeAudioChangedResetsDurationOK(t *testing.T) {
	cases := map[string]*BookFile{
		"scanner bare row": {ID: "f1", BookID: "b1", FileHash: "hash-new"},
		"copy of the stored row, duration cleared": func() *BookFile {
			c := cloneBookFile(storedMeasuredRow())
			c.FileHash = "hash-new"
			c.Duration = nil
			return &c
		}(),
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			v := mergeScannerRow(in)
			require.True(t, v.audioChanged)
			require.Nil(t, in.Duration, "the old file's duration must not survive")
			require.Equal(t, StageStatus{}, in.Scan.DurationOK, "the old file's measurement must not describe the replacement")
			require.True(t, in.Scan.Tagged.Done, "other stages are kept")
			require.True(t, in.Scan.NeedsDeep)
			if name == "scanner bare row" {
				require.Equal(t, StageStatus{}, in.Scan.FPWindowed, "the windowed fingerprint is audio-derived and went with the audio")
			} else {
				// A copy of the stored row carries the old file's fingerprint
				// stage over; with the audio changed it is the old file's record,
				// not a measurement of the replacement, and is dropped like the
				// bare row's.
				require.Equal(t, StageStatus{}, in.Scan.FPWindowed, "a carried-over audio stage is dropped")
			}
		})
	}
}

// A failed attempt recorded against the old file is reset too, even when the
// replacement arrives with a duration: the replacement has not failed.
func TestMergeAudioChangedResetsFailedDurationAttempts(t *testing.T) {
	stored := storedMeasuredRow()
	stored.Duration = nil
	stored.Scan.DurationOK = StageStatus{Attempts: 2, LastError: "probe returned 0s"}
	in := &BookFile{ID: "f1", BookID: "b1", FileHash: "hash-new", Duration: Secs(500)}
	v := mergeBookFileFromStored(in, stored, bookFileWriteUpsert, presenceSeen)
	require.True(t, v.audioChanged)
	require.Equal(t, 500, durtest.Sec(in.Duration))
	require.Equal(t, StageStatus{Done: true}, in.Scan.DurationOK, "the old failures must not ride along with the new value")
}

// A replacement the incoming row measured keeps a stored Done: Done beside a
// known Duration is true of the new file too.
func TestMergeAudioChangedKeepsDoneBesideMeasuredDuration(t *testing.T) {
	in := &BookFile{ID: "f1", BookID: "b1", FileHash: "hash-new", Duration: Secs(900)}
	v := mergeScannerRow(in)
	require.True(t, v.audioChanged)
	require.Equal(t, 900, durtest.Sec(in.Duration))
	require.True(t, in.Scan.DurationOK.Done)
}

// A DurationOK the caller set itself and that differs from the stored one is
// its own record about the new file, even with nothing measured (a failed probe
// of the replacement). It is never reset.
func TestMergeCallerSetDurationOKIsNotReset(t *testing.T) {
	want := StageStatus{Attempts: 1, LastError: "probe returned 0s"}
	in := &BookFile{ID: "f1", BookID: "b1", FileHash: "hash-new",
		Scan: ScanState{DurationOK: want}}
	v := mergeScannerRow(in)
	require.True(t, v.audioChanged)
	require.Nil(t, in.Duration)
	require.Equal(t, want, in.Scan.DurationOK)
}

// Same audio re-tagged (hash changed, duration within a second): nothing about
// the audio is dropped, so the duration stage stays.
func TestMergeSameAudioKeepsDurationOK(t *testing.T) {
	in := &BookFile{ID: "f1", BookID: "b1", FileHash: "hash-new", Duration: Secs(101), Codec: "aac"}
	v := mergeScannerRow(in)
	require.True(t, v.bytesChanged)
	require.False(t, v.audioChanged)
	require.True(t, in.Scan.DurationOK.Done)
}

// A scanned row whose stat failed is never judged changed, so nothing resets.
func TestMergeNotSeenNeverResetsDurationOK(t *testing.T) {
	in := &BookFile{ID: "f1", BookID: "b1", FileHash: "hash-new"}
	v := mergeBookFileFromStored(in, storedMeasuredRow(), bookFileWriteUpsert, presenceNotSeen)
	require.False(t, v.bytesChanged)
	require.True(t, in.Scan.DurationOK.Done)
	require.Equal(t, 100, durtest.Sec(in.Duration))
}

// UpdateBookFile (replace mode) has no content judgement: the caller's row is
// the truth, and a bare Scan is not auto-reset.
func TestMergeReplaceModeDoesNotJudgeContent(t *testing.T) {
	in := &BookFile{ID: "f1", BookID: "b1", FileHash: "hash-new"}
	v := mergeBookFileFromStored(in, storedMeasuredRow(), bookFileWriteReplace, presenceNotScanned)
	require.False(t, v.audioChanged)
}

// A fingerprint stage the caller supplies that DIFFERS from the stored one is the
// caller's own record about the new audio and stays, even with the audio changed;
// only a stage identical to the stored one is a carried-over copy.
func TestMergeAudioChangedKeepsCallerOwnFingerprintStage(t *testing.T) {
	own := StageStatus{Done: true}
	in := &BookFile{ID: "f1", BookID: "b1", FileHash: "hash-new", Scan: ScanState{FPWindowed: own, FPFull: own}}
	v := mergeScannerRow(in)
	require.True(t, v.audioChanged)
	require.Equal(t, own, in.Scan.FPWindowed)
	require.Equal(t, own, in.Scan.FPFull, "the stored FPFull is zero, so a supplied one is the caller's own")
}

// N-e: a Done DurationOK beside no Duration is not a record of anything. It is
// cleared whatever the write mode, symmetrically with a Duration forcing Done.
func TestMergeNoDurationClearsDoneDurationStage(t *testing.T) {
	for name, mode := range map[string]bookFileWriteMode{"upsert": bookFileWriteUpsert, "replace": bookFileWriteReplace} {
		t.Run(name, func(t *testing.T) {
			stored := storedMeasuredRow()
			stored.Duration = nil // stored: Done with nothing measured
			in := &BookFile{ID: "f1", BookID: "b1", FileHash: "hash-old", Scan: ScanState{DurationOK: StageStatus{Done: true}}}
			mergeBookFileFromStored(in, stored, mode, presenceSeen)
			require.Nil(t, in.Duration)
			require.False(t, in.Scan.DurationOK.Done, "Done beside no Duration reads as measured")
		})
	}
	t.Run("failure record is not touched", func(t *testing.T) {
		stored := storedMeasuredRow()
		stored.Duration = nil
		want := StageStatus{Attempts: 2, LastError: "probe returned 0s"}
		in := &BookFile{ID: "f1", BookID: "b1", FileHash: "hash-old", Scan: ScanState{DurationOK: want}}
		mergeBookFileFromStored(in, stored, bookFileWriteUpsert, presenceSeen)
		require.Equal(t, want, in.Scan.DurationOK)
	})
}
