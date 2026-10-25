// file: internal/audiobooks/purge_file_identity.go
// version: 1.0.0
// guid: 8d3f6a21-4b70-4e19-a5c2-0f9e7b1d6c34
// last-edited: 2026-10-25

package audiobooks

import "os"

// fileIdentity is what a purge pass saw of a file when it decided to remove it:
// enough to tell, later, whether the thing at that path is still the same file.
//
// Why it exists. A removal the pass decides on is carried out later: at the end
// of the pass, or, for a pass that died, by the next one (the journal in
// purge_pending_journal.go), which can be days on. Checking that no book row
// names the path does not make the path the same FILE: a re-download, a copy
// the owner made by hand or a restore can put different audio at the same
// organized path before any row names it, and the scanner finds files that are
// already on disk. The removal therefore goes ahead only when the file now at
// the path has the device, inode, size and modification time the decision saw.
// Anything else is a different file, which this purge never decided on.
//
// The zero value is "unknown" and never matches anything: a record with no
// identity cannot be vouched for, so its file is kept.
type fileIdentity struct {
	// Known is set by identityOf; a zero fileIdentity is not an identity.
	Known bool `json:"known,omitempty"`
	// HasInode says Dev and Ino are real (the platform reports them).
	HasInode bool   `json:"has_inode,omitempty"`
	Dev      uint64 `json:"dev,omitempty"`
	Ino      uint64 `json:"ino,omitempty"`
	Size     int64  `json:"size"`
	MtimeNS  int64  `json:"mtime_ns"`
}

// identityOf is the identity of the file info describes (a stat of the path).
func identityOf(info os.FileInfo) fileIdentity {
	id := fileIdentity{Known: true, Size: info.Size(), MtimeNS: info.ModTime().UnixNano()}
	id.Dev, id.Ino, id.HasInode = deviceAndInode(info)
	return id
}

// matches reports whether a and b name the same file. For a directory (an empty
// one, the only kind a purge removes) the device and inode decide: its size and
// modification time change with its entries, and it is empty either way. A
// platform with no inode falls back to the modification time for it.
func (a fileIdentity) matches(b fileIdentity, isDir bool) bool {
	if !a.Known || !b.Known || a.HasInode != b.HasInode {
		return false
	}
	if a.HasInode && (a.Dev != b.Dev || a.Ino != b.Ino) {
		return false
	}
	if isDir && a.HasInode {
		return true
	}
	if isDir {
		return a.MtimeNS == b.MtimeNS
	}
	return a.Size == b.Size && a.MtimeNS == b.MtimeNS
}
