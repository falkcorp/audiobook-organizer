// file: internal/database/race_enabled_norace_test.go
// version: 1.0.0
// guid: 5c2def95-b174-4944-9a77-33f4e0b432e8
// last-edited: 2026-10-11

//go:build !race

package database

// raceEnabled: see race_enabled_race_test.go.
const raceEnabled = false
