// file: internal/seqnum/race_enabled_race_test.go
// version: 1.0.0
// guid: 3c9e5a71-84d2-4b06-a1f7-e60d2b8c4935
// last-edited: 2026-11-18

//go:build race

package seqnum

// raceEnabled is true when the test binary is built with -race, which is how
// every CI run builds it. The generated tests in guard_test.go run many regexp
// matches, and a match costs far more under the race detector; see scaled.
const raceEnabled = true
