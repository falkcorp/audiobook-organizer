// file: core/internal/libfs/stamp_unixother.go
// version: 1.0.0
// guid: 0e071a4a-5ba5-4c5b-a47e-d3b2c9946025
// last-edited: 2026-11-12

//go:build unix && !linux && !darwin

package libfs

import "os"

// Platforms other than Linux and macOS report neither a change time through this
// package nor a creation time or generation: the facts are absent, which a caller
// treats as "not recorded".
func changeTime(*unixStat) int64 { return 0 }

func birthAndGeneration(*os.File, *unixStat) (birth int64, gen uint32, hasGen bool) {
	return 0, 0, false
}
