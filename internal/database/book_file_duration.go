// file: internal/database/book_file_duration.go
// version: 1.0.0
// guid: 5521b477-4556-48b9-92d7-a6cfd09e8f71
// last-edited: 2026-11-04

package database

import "fmt"

// BookFileDurationState classifies what is known about a file's duration.
//
// Today only Known and Unprobed are derived (from Duration > 0). ProbeFailed
// and Unreadable are declared for the probe-outcome work that will derive them
// from ScanState.DurationOK; nothing returns them yet.
type BookFileDurationState int

const (
	// DurationUnprobed means no duration has been measured.
	DurationUnprobed BookFileDurationState = iota
	// DurationKnown means a positive duration is stored.
	DurationKnown
	// DurationProbeFailed means a probe was attempted and failed (reserved).
	DurationProbeFailed
	// DurationUnreadable means probes failed up to the attempt ceiling (reserved).
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

func durationStateOf(sec int) BookFileDurationState {
	if sec > 0 {
		return DurationKnown
	}
	return DurationUnprobed
}

func validateMeasuredDuration(sec int) error {
	if sec <= 0 {
		return fmt.Errorf("measured duration must be > 0 seconds, got %d", sec)
	}
	return nil
}

// DurationSec returns the stored duration in seconds and whether it is known.
// Unknown is currently Duration <= 0.
func (f BookFile) DurationSec() (sec int, known bool) {
	return f.Duration, f.Duration > 0
}

// DurationState reports the duration's state. See BookFileDurationState.
func (f BookFile) DurationState() BookFileDurationState {
	return durationStateOf(f.Duration)
}

// SetMeasuredDuration stores a measured duration; sec must be > 0.
func (f *BookFile) SetMeasuredDuration(sec int) error {
	if err := validateMeasuredDuration(sec); err != nil {
		return err
	}
	f.Duration = sec
	return nil
}

// ResetDuration returns the file to the unprobed state.
func (f *BookFile) ResetDuration() {
	f.Duration = 0
}

// DurationSec is the BookFileCore form of BookFile.DurationSec.
func (f BookFileCore) DurationSec() (sec int, known bool) {
	return f.Duration, f.Duration > 0
}

// DurationState is the BookFileCore form of BookFile.DurationState.
func (f BookFileCore) DurationState() BookFileDurationState {
	return durationStateOf(f.Duration)
}

// SetMeasuredDuration is the BookFileCore form of BookFile.SetMeasuredDuration.
func (f *BookFileCore) SetMeasuredDuration(sec int) error {
	if err := validateMeasuredDuration(sec); err != nil {
		return err
	}
	f.Duration = sec
	return nil
}

// ResetDuration is the BookFileCore form of BookFile.ResetDuration.
func (f *BookFileCore) ResetDuration() {
	f.Duration = 0
}
