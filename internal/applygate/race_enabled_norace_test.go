// file: internal/applygate/race_enabled_norace_test.go
// version: 1.0.0
// guid: 9ec58909-ccbd-4a86-8619-4f5635349e4b
// last-edited: 2026-11-17

//go:build !race

package applygate

// raceEnabled is false in a normal test build; see race_enabled_race_test.go.
const raceEnabled = false
