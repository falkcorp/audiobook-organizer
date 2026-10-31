// file: internal/matcher/race_enabled_race_test.go
// version: 1.0.0
// guid: d2ead362-141e-4c78-bacc-f47fd7b932e1
// last-edited: 2026-10-30

//go:build race

package matcher

// raceEnabled is true when the test binary is built with -race. sync.Pool
// deliberately drops a random quarter of Puts under the race detector, so a
// zero-allocation assertion on a pooled path is only meaningful without it.
const raceEnabled = true
