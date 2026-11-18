// file: internal/seqnum/race_enabled_norace_test.go
// version: 1.0.0
// guid: b17d4e83-0f5a-4c92-8d36-5a2e9c7f1084
// last-edited: 2026-11-18

//go:build !race

package seqnum

// raceEnabled is false in a normal test build; see race_enabled_race_test.go.
const raceEnabled = false
