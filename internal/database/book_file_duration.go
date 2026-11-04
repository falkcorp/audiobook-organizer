// file: internal/database/book_file_duration.go
// version: 2.0.0
// guid: 5521b477-4556-48b9-92d7-a6cfd09e8f71
// last-edited: 2026-11-04

package database

import (
	"encoding/json"
	"fmt"
)

// BookFileDurationState classifies what is known about a file's duration.
//
// Duration is a *int: nil is "not measured" and a stored value is always > 0.
// Today only Known and Unprobed are derived (Known = non-nil and > 0).
// ProbeFailed and Unreadable are declared for the probe-outcome work that will
// derive them from ScanState.DurationOK; nothing returns them yet.
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

func durationStateOf(p *int) BookFileDurationState {
	if p != nil && *p > 0 {
		return DurationKnown
	}
	return DurationUnprobed
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
	return durationStateOf(f.Duration)
}

// SetMeasuredDuration stores a measured duration; sec must be > 0. It assigns
// a fresh pointer so copies of the row made earlier are not changed.
func (f *BookFile) SetMeasuredDuration(sec int) error {
	if err := validateMeasuredDuration(sec); err != nil {
		return err
	}
	f.Duration = &sec
	return nil
}

// ResetDuration returns the file to the unprobed state.
func (f *BookFile) ResetDuration() {
	f.Duration = nil
}

// DurationSec is the BookFileCore form of BookFile.DurationSec.
func (f BookFileCore) DurationSec() (sec int, known bool) {
	return durationSecOf(f.Duration)
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
	f.Duration = &sec
	return nil
}

// ResetDuration is the BookFileCore form of BookFile.ResetDuration.
func (f *BookFileCore) ResetDuration() {
	f.Duration = nil
}
