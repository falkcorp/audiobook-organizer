// file: internal/libroot/export_test.go
// version: 1.0.0
// guid: 3b9d7e52-6c14-4f80-a2d5-8e1f4c0b7a93
// last-edited: 2026-10-26

//go:build unix

package libroot

// SetEUIDForTest makes OwnedByCurrentUser compare against uid and returns a
// function that restores the real one.
func SetEUIDForTest(uid int) func() {
	prev := euid
	euid = func() int { return uid }
	return func() { euid = prev }
}
