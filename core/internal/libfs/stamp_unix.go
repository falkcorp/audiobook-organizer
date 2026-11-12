// file: core/internal/libfs/stamp_unix.go
// version: 1.0.0
// guid: 7665d566-2a0b-4552-8434-16d1ea08d9d3
// last-edited: 2026-11-12

//go:build unix

package libfs

import "os"

// FactsOf reads the identity facts of the open file f from its descriptor, so
// they describe the file f really is and not whatever the name has become since.
// A file that is not a regular one (a FIFO opened non-blocking, a device) is
// reported with Regular false and nothing else asked of it: an ioctl on a device
// can have effects.
func FactsOf(f *os.File) (FileFacts, error) {
	st, err := f.Stat()
	if err != nil {
		return FileFacts{}, err
	}
	facts := FileFacts{Size: st.Size(), MTime: st.ModTime().UnixNano(), Regular: st.Mode().IsRegular()}
	sys, ok := st.Sys().(*unixStat)
	if !ok || sys == nil {
		return facts, nil
	}
	facts.Dev, facts.Ino = uint64(sys.Dev), uint64(sys.Ino)
	facts.CTime = changeTime(sys)
	if facts.Regular {
		facts.Birth, facts.Gen, facts.HasGen = birthAndGeneration(f, sys)
	}
	return facts, nil
}
