// file: internal/plugins/maintenance/fragment_filesig_coarse_darwin_test.go
// version: 1.0.0
// guid: 4b7d2e91-6c3a-4f58-9e10-a2d5c8f36b07
// last-edited: 2026-10-21

//go:build darwin

package maintenance

import "syscall"

// setWholeSecondCtime makes st report a status-change time of exactly sec.
func setWholeSecondCtime(st *syscall.Stat_t, sec int64) {
	st.Ctimespec.Sec, st.Ctimespec.Nsec = sec, 0
}
