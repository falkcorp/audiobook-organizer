// file: internal/dedup/race_enabled_norace_test.go
// version: 1.0.0
// guid: 599b1810-34eb-43b8-8ed4-e04af6deb1ed
// last-edited: 2026-10-30

//go:build !race

package dedup

// raceEnabled is false in a normal test build; see race_enabled_race_test.go.
const raceEnabled = false
