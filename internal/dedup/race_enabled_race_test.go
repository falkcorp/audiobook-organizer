// file: internal/dedup/race_enabled_race_test.go
// version: 1.0.0
// guid: 94a0b792-cfdd-45ee-bc80-587f722f1382
// last-edited: 2026-10-30

//go:build race

package dedup

// raceEnabled is true when the test binary is built with -race. sync.Pool
// deliberately drops a random quarter of Puts under the race detector, so a
// zero-allocation assertion on a pooled path is only meaningful without it.
const raceEnabled = true
