// file: internal/plugins/maintenance/fragment_filesig_coarse_linux_test.go
// version: 1.0.0
// guid: 8d1f5a36-9e24-4b70-a3c8-5f0e7b2d9c41
// last-edited: 2026-10-21

//go:build linux

package maintenance

import "syscall"

// setWholeSecondCtime makes st report a status-change time of exactly sec.
func setWholeSecondCtime(st *syscall.Stat_t, sec int64) {
	st.Ctim.Sec, st.Ctim.Nsec = sec, 0
}
