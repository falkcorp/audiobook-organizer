// file: core/internal/libfs/stamp.go
// version: 1.0.0
// guid: a1cac1ea-e9e7-4267-bef4-207d89267347
// last-edited: 2026-11-12

package libfs

// FileFacts is what tells one regular file from another that held the same device
// and inode number at a different time. A filesystem such as ext4 hands a freed
// inode number straight back to the next file created, so (device, inode, size,
// modification time) cannot separate "the file I looked at" from "an identical
// file written in its place with the modification time put back". The facts
// added here do:
//
//   - CTime, the inode change time. The kernel sets it on every create, rename,
//     link, chmod and write and no system call can set it, so a file written after
//     the first look has a later one. It has the clock's tick granularity, so a
//     file replaced within one tick of the last change to the original can share it.
//   - Birth, the creation time, where the filesystem records one (statx btime on
//     Linux, st_birthtime on macOS). It never changes while the file is the same
//     inode, and a replacement has its own.
//   - Gen, the inode generation (ext4, XFS and btrfs: FS_IOC_GETVERSION). The
//     filesystem draws a new one each time an inode number is allocated, so a
//     recycled number differs even inside one tick. Filesystems that do not report
//     one (overlayfs, and macOS, which is not asked) leave HasGen false.
//
// A zero CTime or Birth, or a false HasGen, means the platform or filesystem does
// not report that fact.
type FileFacts struct {
	Dev, Ino uint64
	Size     int64
	MTime    int64 // ns since the epoch
	CTime    int64 // ns since the epoch
	Birth    int64 // ns since the epoch
	Gen      uint32
	HasGen   bool
	Regular  bool // the descriptor is a regular file; the other facts are meaningful only then
}
