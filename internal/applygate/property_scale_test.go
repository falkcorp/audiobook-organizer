// file: internal/applygate/property_scale_test.go
// version: 1.0.0
// guid: 7ff3bd8b-cf0b-4af8-8fe7-0506ee171ae5
// last-edited: 2026-11-17

package applygate

// raceScaleDown is how much smaller a generated property test is under -race.
const raceScaleDown = 5

// scaled returns full in a normal build and a fifth of it under -race.
//
// Why: the generated property tests (the oracle, symmetry, added-text, the
// neutral-title sweeps and the like) walk tens of thousands of inputs through
// the title check. Without the race detector the whole package takes about
// 100 CPU-seconds; with it, about 3,300, and the short race shard in CI hit its
// 25 minute go test timeout on this package alone. Every CI run is a -race run,
// so the draw counts and every floor that guards them (the "the generator
// reached this case N times" checks) are scaled together through this one
// function: a floor is only meaningful against the count it was set for. The
// full counts run in any plain `go test ./internal/applygate`.
func scaled(full int) int {
	if raceEnabled {
		return max(full/raceScaleDown, 1)
	}
	return full
}

// sampled keeps every raceScaleDown-th element under -race and all of them
// otherwise. The stride is coprime to the pool's loop extents (6, 9, 6), so the
// sample still crosses every title form instead of aliasing onto one.
func sampled[T any](pool []T) []T {
	if !raceEnabled {
		return pool
	}
	out := make([]T, 0, len(pool)/raceScaleDown+1)
	for i := 0; i < len(pool); i += raceScaleDown {
		out = append(out, pool[i])
	}
	return out
}
