### Fixed

- Internal: fixed a flaky `TestResume_ShutdownTimeoutLeavesQuiescedAndResumable`. The test slept a fixed 400ms and then read the store, racing the drain-deadline stamp that `Shutdown` writes on its own goroutine, so a CPU-starved CI agent could read the row as `running`. It now waits for `Shutdown` to return, which always happens after the stamp. Test-only change; `Shutdown` itself was correct.
