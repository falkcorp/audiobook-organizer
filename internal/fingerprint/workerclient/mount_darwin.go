// file: internal/fingerprint/workerclient/mount_darwin.go
// version: 1.0.0
// guid: ab8c1f7b-4693-49eb-beb9-89c94d85f7d9
// last-edited: 2026-09-19

//go:build darwin

package workerclient

import (
	"bytes"
	"fmt"

	"golang.org/x/sys/unix"
)

// statMount reports the filesystem holding path: statfs(2)'s f_fstypename,
// f_mntonname and the MNT_RDONLY bit of f_flags.
func statMount(path string) (MountInfo, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return MountInfo{}, fmt.Errorf("statfs %s: %w", path, err)
	}
	return MountInfo{
		FSType:     cString(st.Fstypename[:]),
		MountPoint: cString(st.Mntonname[:]),
		ReadOnly:   st.Flags&unix.MNT_RDONLY != 0,
	}, nil
}

func cString(b []byte) string {
	if i := bytes.IndexByte(b, 0); i >= 0 {
		b = b[:i]
	}
	return string(b)
}
