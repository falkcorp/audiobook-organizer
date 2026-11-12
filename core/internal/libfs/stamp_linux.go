// file: core/internal/libfs/stamp_linux.go
// version: 1.0.0
// guid: 2137d188-0aac-4ad9-a0f3-8b5c3f963b33
// last-edited: 2026-11-12

package libfs

import (
	"os"
	"unsafe"

	"golang.org/x/sys/unix"
)

// fsIocGetVersion is FS_IOC_GETVERSION, _IOR('v', 1, long): the inode generation.
// The size field of the request is the size of a C long, which the platform's
// pointer width stands in for. x/sys/unix does not define it.
const fsIocGetVersion = 0x80007601 | uint(unsafe.Sizeof(uintptr(0)))<<16

func changeTime(sys *unixStat) int64 { return sys.Ctim.Nano() }

// birthAndGeneration asks the descriptor for its creation time (statx) and its
// inode generation (FS_IOC_GETVERSION). Either may be unavailable on a given
// filesystem, which is reported as zero and false and is not an error: the facts
// that are there still count.
func birthAndGeneration(f *os.File, _ *unixStat) (birth int64, gen uint32, hasGen bool) {
	rc, err := f.SyscallConn()
	if err != nil {
		return 0, 0, false
	}
	var (
		stx  unix.Statx_t
		serr error
		gerr error
	)
	if cerr := rc.Control(func(fd uintptr) {
		serr = unix.Statx(int(fd), "", unix.AT_EMPTY_PATH|unix.AT_STATX_SYNC_AS_STAT, unix.STATX_BTIME, &stx)
		gen, gerr = unix.IoctlGetUint32(int(fd), fsIocGetVersion)
	}); cerr != nil {
		return 0, 0, false
	}
	if serr == nil && stx.Mask&unix.STATX_BTIME != 0 {
		birth = int64(stx.Btime.Sec)*1e9 + int64(stx.Btime.Nsec)
	}
	return birth, gen, gerr == nil
}
