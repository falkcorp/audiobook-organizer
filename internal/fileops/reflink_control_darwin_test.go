// file: internal/fileops/reflink_control_darwin_test.go
// version: 1.0.0
// guid: 4877dd36-17ba-4a9d-9bc1-79dea4ca35ac
// last-edited: 2026-09-01

//go:build darwin

package fileops

import "golang.org/x/sys/unix"

// rawClone issues the platform clone primitive directly, bypassing Reflink.
// It is the KNOWN-GOOD TWIN for the clone test: without it, a regression that
// disables cloning is indistinguishable from a filesystem that cannot clone,
// and the test skips instead of failing.
func rawClone(src, dst string) error { return unix.Clonefile(src, dst, 0) }
