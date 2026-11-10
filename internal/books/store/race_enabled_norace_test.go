// file: internal/books/store/race_enabled_norace_test.go
// version: 1.0.1
// guid: 5c2def95-b174-4944-9a77-33f4e0b432e8
// last-edited: 2026-11-09

//go:build !race

package store

// raceEnabled: see race_enabled_race_test.go.
const raceEnabled = false
