// file: core/storage/checkpoint_sweep_other.go
// version: 1.0.0
// guid: 6f2c9a40-3d18-4b7e-8a05-c4e1b27d9f63
// last-edited: 2026-11-08

//go:build !unix

package storage

// unixModes: off unix the permission bits Go reports are synthesised, so
// the sweep's mode check (§5S.3.12 "Modes") reads none.
const unixModes = false
