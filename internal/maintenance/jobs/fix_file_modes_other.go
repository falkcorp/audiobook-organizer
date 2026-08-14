// file: internal/maintenance/jobs/fix_file_modes_other.go
// version: 1.0.0
// guid: 8a2d5f17-9c43-4e6b-b0d8-6e1f3a7c2d95
// last-edited: 2026-08-14

//go:build !unix

package jobs

import "os"

// ownedByUID cannot be determined without unix stat; report false so the
// repair never chmods on platforms where the ownership check is unavailable.
func ownedByUID(_ os.FileInfo, _ int) bool { return false }
