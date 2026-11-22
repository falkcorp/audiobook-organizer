// file: internal/server/settle_disk_windows_test.go
// version: 1.0.0
// guid: e41b8d07-5a63-4c92-b7f0-3d9a26c1f85e
// last-edited: 2026-11-22

//go:build windows

package server

import "testing"

// settleDisk is a no-op on Windows: filePhysicalSize returns the logical size
// there, so there is no lazily allocated block count to wait for.
func settleDisk(t *testing.T) { t.Helper() }
