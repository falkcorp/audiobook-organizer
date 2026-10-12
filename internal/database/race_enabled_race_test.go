// file: internal/database/race_enabled_race_test.go
// version: 1.0.0
// guid: 7bbd594b-a40f-42cf-a07f-4835fd1c8058
// last-edited: 2026-10-11

//go:build race

package database

// raceEnabled is true when the test binary is built with -race. Tests whose
// size is set for a coverage claim (the identification index's randomized
// recount, for one) shrink with it: the race detector slows the store's hot
// paths several times over, and CI runs the package under -race with a
// 10-minute budget. The full-size run is the plain go test.
const raceEnabled = true
