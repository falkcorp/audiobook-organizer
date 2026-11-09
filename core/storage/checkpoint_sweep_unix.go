// file: core/storage/checkpoint_sweep_unix.go
// version: 1.0.0
// guid: 0e4b8d21-7c3a-4f59-b6e2-91a7d5c0f348
// last-edited: 2026-11-08

//go:build unix

package storage

// unixModes: file modes carry the owner/group/other permission bits the
// sweep's mode check reads (§5S.3.12 "Modes", SEC-9).
const unixModes = true
