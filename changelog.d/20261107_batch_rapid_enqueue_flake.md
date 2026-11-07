### Fixed

- `TestBatch_50RapidEnqueues` no longer flakes on loaded CI agents. The batch debounce window is trailing-edge by design, so an enqueuing goroutine stalled for longer than the window legitimately ended the burst early and split it across two rows (21 + 29 subjects); the test raced a real 100 ms window against goroutine scheduling. The test now injects the timer primitive (new test-only `registry.Options.BatchAfterFunc`) and fires the window by hand, and a real-timer companion asserts that every subject is dispatched exactly once across rows. No change to batching behaviour.
