// file: internal/database/book_file_duration.go
// version: 2.1.0
// guid: 5521b477-4556-48b9-92d7-a6cfd09e8f71
// last-edited: 2026-11-04

package database

import (
	"encoding/json"
	"fmt"
	"strings"
)

// BookFileDurationState classifies what is known about a file's duration.
//
// Duration is a *int: nil is "not measured" and a stored value is always > 0.
// The state derives from that field and ScanState.DurationOK:
//
//	Known       Duration is non-nil (and > 0); DurationOK is not consulted.
//	Unprobed    Duration is nil and DurationOK.Attempts == 0: nothing has
//	            measured it, or the only attempts could not run (tool missing,
//	            file absent), which are not counted.
//	ProbeFailed Duration is nil and 0 < DurationOK.Attempts < DeepScanMaxAttempts.
//	Unreadable  Duration is nil and DurationOK.Attempts >= DeepScanMaxAttempts.
//
// DurationOK.LastError says why the most recent counted attempt failed. A
// timeout carries DurationProbeTimeoutPrefix, so a reader that wants to surface
// timeouts only at the ceiling (a stalled NFS mount must not flag a good file)
// can tell them apart; the state itself does not distinguish them.
type BookFileDurationState int

const (
	// DurationUnprobed means no duration has been measured.
	DurationUnprobed BookFileDurationState = iota
	// DurationKnown means a positive duration is stored.
	DurationKnown
	// DurationProbeFailed means a probe ran on a present file and failed, fewer
	// times than DeepScanMaxAttempts.
	DurationProbeFailed
	// DurationUnreadable means probes failed DeepScanMaxAttempts times or more.
	DurationUnreadable
)

// String returns a stable lowercase name for logs and tests.
func (s BookFileDurationState) String() string {
	switch s {
	case DurationKnown:
		return "known"
	case DurationUnprobed:
		return "unprobed"
	case DurationProbeFailed:
		return "probe_failed"
	case DurationUnreadable:
		return "unreadable"
	}
	return fmt.Sprintf("BookFileDurationState(%d)", int(s))
}

// DurationProbeTimeoutPrefix starts the DurationOK.LastError of an attempt that
// failed because the probe timed out, as opposed to the file being bad. See
// IsDurationTimeoutError.
const DurationProbeTimeoutPrefix = "timeout: "

// IsDurationTimeoutError reports whether a DurationOK.LastError records a probe
// timeout (RecordDurationProbeFailure was given a DurationProbeTimeoutPrefix
// message).
func IsDurationTimeoutError(lastError string) bool {
	return strings.HasPrefix(lastError, DurationProbeTimeoutPrefix)
}

// unknownDurationLastError stands in for an empty failure message, so that
// "Attempts > 0" always comes with a reason.
const unknownDurationLastError = "probe failed (no detail)"

func durationStateOf(p *int, st StageStatus) BookFileDurationState {
	switch {
	case p != nil && *p > 0:
		return DurationKnown
	case st.Attempts >= DeepScanMaxAttempts:
		return DurationUnreadable
	case st.Attempts > 0:
		return DurationProbeFailed
	}
	return DurationUnprobed
}

// measuredDurationStage is DurationOK for a file whose duration was just
// measured: done, with no attempts and no error. A failure recorded earlier is
// moot once a value exists, and "Done with a LastError" would read as both.
func measuredDurationStage() StageStatus { return StageStatus{Done: true} }

// failedDurationStage is st after one more failed probe attempt. The caller
// has already checked that no duration is known.
func failedDurationStage(st StageStatus, err string) StageStatus {
	if err == "" {
		err = unknownDurationLastError
	}
	return StageStatus{Attempts: st.Attempts + 1, LastError: err}
}

func durationSecOf(p *int) (sec int, known bool) {
	if p != nil && *p > 0 {
		return *p, true
	}
	return 0, false
}

func validateMeasuredDuration(sec int) error {
	if sec <= 0 {
		return fmt.Errorf("measured duration must be > 0 seconds, got %d", sec)
	}
	return nil
}

// Secs returns a fresh *int holding sec for use as a BookFile or BookFileCore
// Duration, or nil when sec <= 0 (not measured: a stored duration is always
// > 0). It is the literal and conversion form of SetMeasuredDuration for code
// that builds a row in one expression, such as a struct literal, an iTunes
// TotalTime import, or a test fixture.
func Secs(sec int) *int {
	if sec <= 0 {
		return nil
	}
	return &sec
}

// durationPtrEqual compares two Duration fields by value: both nil, or both
// non-nil and equal. Comparing the pointers themselves would report every
// separately decoded row as changed.
func durationPtrEqual(a, b *int) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// normalizeDecodedDuration maps a decoded Duration <= 0 to nil. A row never
// stores "duration":0 (omitzero on a nil pointer), so this only matters for a
// hand-edited or foreign row; it keeps the invariant "non-nil means > 0" true
// for every reader of a BookFile or BookFileCore. Every decode of a stored
// book_file row INTO one of those two types goes through unmarshalBookFile or
// decodeBookFileCore. The slim decode structs some scans use read "duration"
// into a plain int and do not need it: a missing or zero key is 0 there, which
// is what unknown means to them.
func normalizeDecodedDuration(p **int) {
	if *p != nil && **p <= 0 {
		*p = nil
	}
}

// unmarshalBookFile decodes one stored book_file row into f and normalizes the
// decoded Duration. BookFile deliberately has no UnmarshalJSON (see
// TestBookFileCoreIsBookFileMinusHeavyFields), so each decode site calls this.
func unmarshalBookFile(data []byte, f *BookFile) error {
	if err := json.Unmarshal(data, f); err != nil {
		return err
	}
	normalizeDecodedDuration(&f.Duration)
	return nil
}

// DurationSec returns the stored duration in seconds and whether it is known.
func (f BookFile) DurationSec() (sec int, known bool) {
	return durationSecOf(f.Duration)
}

// DurationState reports the duration's state. See BookFileDurationState.
func (f BookFile) DurationState() BookFileDurationState {
	return durationStateOf(f.Duration, f.Scan.DurationOK)
}

// SetMeasuredDuration stores a measured duration; sec must be > 0. It assigns
// a fresh pointer so copies of the row made earlier are not changed. It also
// writes Scan.DurationOK as {Done: true}: the stage is complete, and any earlier
// failed attempts and LastError are cleared because a value now exists. A
// rejected sec (<= 0) changes nothing, so a failure record survives it.
func (f *BookFile) SetMeasuredDuration(sec int) error {
	if err := validateMeasuredDuration(sec); err != nil {
		return err
	}
	f.Duration = &sec
	f.Scan.DurationOK = measuredDurationStage()
	return nil
}

// RecordDurationProbeFailure records one failed duration probe of a present
// file: Scan.DurationOK.Attempts is incremented, LastError set to err and Done
// cleared, and Duration stays nil. At DeepScanMaxAttempts the state becomes
// DurationUnreadable. A probe that could not run at all (ffprobe missing, file
// absent) is not a failure of the file and must not be recorded; classifying
// that is the caller's job (bookfileaudio.RecordProbeFailure). Start err with
// DurationProbeTimeoutPrefix for a timeout.
//
// A row whose duration is already known is left alone: a failed re-probe does
// not contradict an earlier measurement, and Known ignores DurationOK anyway.
func (f *BookFile) RecordDurationProbeFailure(err string) {
	if f.Duration != nil {
		return
	}
	f.Scan.DurationOK = failedDurationStage(f.Scan.DurationOK, err)
}

// ResetDuration returns the file to the unprobed state: Duration is cleared
// and Scan.DurationOK is zeroed, so neither a stale Done nor an old failure
// outlives the value it described.
func (f *BookFile) ResetDuration() {
	f.Duration = nil
	f.Scan.DurationOK = StageStatus{}
}

// DurationSec is the BookFileCore form of BookFile.DurationSec.
func (f BookFileCore) DurationSec() (sec int, known bool) {
	return durationSecOf(f.Duration)
}

// DurationState is the BookFileCore form of BookFile.DurationState.
func (f BookFileCore) DurationState() BookFileDurationState {
	return durationStateOf(f.Duration, f.Scan.DurationOK)
}

// SetMeasuredDuration is the BookFileCore form of BookFile.SetMeasuredDuration.
func (f *BookFileCore) SetMeasuredDuration(sec int) error {
	if err := validateMeasuredDuration(sec); err != nil {
		return err
	}
	f.Duration = &sec
	f.Scan.DurationOK = measuredDurationStage()
	return nil
}

// RecordDurationProbeFailure is the BookFileCore form of
// BookFile.RecordDurationProbeFailure.
func (f *BookFileCore) RecordDurationProbeFailure(err string) {
	if f.Duration != nil {
		return
	}
	f.Scan.DurationOK = failedDurationStage(f.Scan.DurationOK, err)
}

// ResetDuration is the BookFileCore form of BookFile.ResetDuration.
func (f *BookFileCore) ResetDuration() {
	f.Duration = nil
	f.Scan.DurationOK = StageStatus{}
}
