// file: internal/applygate/race_enabled_race_test.go
// version: 1.0.0
// guid: 62464ea6-d41e-45c8-9177-70dad37841ca
// last-edited: 2026-11-17

//go:build race

package applygate

// raceEnabled is true when the test binary is built with -race. The generated
// property tests in this package are CPU-bound through the title check and run
// about twelve times slower under the race detector, which is how every CI run
// builds them; see scaled in property_scale_test.go.
const raceEnabled = true
