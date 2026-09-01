// file: internal/fileops/reflink_control_other_test.go
// version: 1.0.0
// guid: 534f50af-b6eb-4c9c-8b31-7408d05d269a
// last-edited: 2026-09-01

//go:build !linux && !darwin

package fileops

import "errors"

// rawClone reports that this platform has no clone primitive at all, so the
// clone test's skip is genuinely unavoidable here rather than masking a
// regression.
func rawClone(src, dst string) error {
	return errors.New("no clone primitive on this platform")
}
