### Fixed

#### AI scan shutdown-resume test no longer doubles its results

- `TestShutdownLeavesBatchRunningForResume` failed intermittently with 8 results instead of 4. The test restarts by building a second `PipelineManager` on the same store while the first one is still alive in the same process. Phase claims are held per manager, so a `groups_enrich` still running in the first manager ran a second time in the second, both managers cross-validated, and the two concurrent result writes kept every result twice. The test now waits for the first manager to settle its groups side (`awaitGroupsSettled`) before the restart, the same way the other two-manager resume tests do. This was a test-only fault: production builds one `PipelineManager` per process, and a real restart ends the old process.

#### AI scan results are replaced once when two replaces overlap, and IDs are unique under concurrency

- `AIScanStore.ReplaceScanResults` said it "atomically replaces" a scan's results, but two overlapping calls each listed the rows to delete before the other committed, so the scan kept both sets. It now holds `applyMu` for the whole replace. `ReplaceScanResultsIfUnapplied`, which already holds that lock, calls a new unlocked `replaceScanResultsLocked`. Replaces also no longer interleave with `MarkResultApplied`. Lock order stays applyMu, then stateMu.
- `AIScanStore.nextID` read the counter and wrote it back as two separate steps, so two concurrent callers could get the same scan or result ID. A new leaf lock, `idMu`, makes the two steps one. In production the pipeline's per-process phase claims already kept cross-validation from overlapping itself, so this closes a guarantee the store documented but did not keep. It is not a fix for duplicates users have seen.
- New tests in `internal/database/ai_scan_store_concurrency_test.go` fail against the old code and pass with `-race -count=50`.
