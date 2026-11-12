// file: core/internal/libfs/stamp_darwin.go
// version: 1.0.0
// guid: 12eb70eb-2fe0-4877-9db7-3ec94c2c878c
// last-edited: 2026-11-12

package libfs

import "os"

func changeTime(sys *unixStat) int64 { return sys.Ctimespec.Nano() }

// birthAndGeneration reads st_birthtime. macOS reports st_gen only to root, so
// there is no generation.
func birthAndGeneration(_ *os.File, sys *unixStat) (birth int64, gen uint32, hasGen bool) {
	return sys.Birthtimespec.Nano(), 0, false
}
