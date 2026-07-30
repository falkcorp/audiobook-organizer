<!-- file: changelog.d/watcher-debounce-test-flake.md -->
<!-- version: 1.0.0 -->
<!-- guid: 5c8e2a71-9b34-4d6f-a815-3e0c7f2b9d61 -->
<!-- last-edited: 2026-07-30 -->

### Fixed

#### `internal/watcher`: eliminated the wall-clock flake in the debounce tests

`TestDebounceMultipleEvents` and `TestDebounceSingleEvent` paced file writes and
asserted the debounce callback count with `time.Sleep` against a short debounce
window (as little as ~80ms of margin), so a scheduling stall on a loaded CI
runner could let the debounce timer fire before the write burst finished,
producing an extra callback and failing the test. This was root-caused from a
CI log showing two "watcher triggering callback" lines a full second apart —
a stall signature, not a logic error.

`internal/watcher/watcher.go` now takes its debounce timer through a small
unexported `scanClock` interface (`AfterFunc(d, f) stoppableTimer`), with the
existing `time.AfterFunc`-backed behavior preserved exactly for all real
callers via a `realClock` implementation set by `New()`. The tests substitute
a `fakeClock` that never fires on its own — the test waits for the real
fsnotify events to land (polling the watcher's internal scan generation
counter until it stops changing, with a generous timeout, instead of guessing
a sleep duration) and then explicitly advances virtual time to fire the
debounce deterministically. The "exactly one callback" assertion is
unchanged; only the timing mechanism is fixed.

Verified with `go test ./internal/watcher/ -race -count=50` (green) and again
under artificial 10-way CPU saturation with `-count=20` (green, 160/160 sub-test
passes). The now-resolved `todo.d/fix-watcher-debounce-test-flake.md` fragment
is removed in the same change.
