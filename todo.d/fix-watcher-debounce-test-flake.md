<!-- file: todo.d/fix-watcher-debounce-test-flake.md -->
<!-- version: 1.0.0 -->
<!-- guid: 9eef5da1-8225-4987-9d10-b5e9033cf0c1 -->
<!-- last-edited: 2026-07-30 -->

- [ ] **Fix the wall-clock flake in `internal/watcher` `TestDebounceMultipleEvents`.**
  Root cause diagnosed, not guessed: the test writes 5 files with `time.Sleep(30ms)`
  between them — 120 ms of writing — against a **200 ms** debounce
  (`internal/watcher/watcher_test.go:68-93`). The margin is only ~80 ms, so any
  scheduling stall longer than that on a loaded runner lets the first event's debounce
  timer fire before the 5th write lands, and the test sees 2 callbacks instead of 1.
  Observed on CI in PR #2076 (`run 30587512388`, job 91022276468):
  `watcher_test.go:93: expected exactly 1 debounced callback, got 2`, with the log
  showing two `watcher triggering callback` lines a full second apart — a stall, not a
  logic error. Passes 10/10 locally under `-race`. `TestDebounceSingleEvent` has the
  same shape and the same latent problem.
  **Fix:** make the timing explicit instead of racy — inject the clock/timer, or widen
  the debounce well past the total write window (e.g. 2 s debounce for a 120 ms write
  burst) and assert on a channel signal rather than a `time.Sleep` + counter read.
  Do **not** just bump the sleeps; that trades one flake for a slower one.
  The package was last touched 2026-07-03, so this predates the ABS work.
