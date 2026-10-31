// file: internal/matcher/race_enabled_norace_test.go
// version: 1.0.0
// guid: 5f533364-50ab-4abb-ae2d-827c2beddce3
// last-edited: 2026-10-30

//go:build !race

package matcher

// raceEnabled is false in a normal test build; see race_enabled_race_test.go.
const raceEnabled = false
