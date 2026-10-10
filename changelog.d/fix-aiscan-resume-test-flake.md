### Fixed

#### AI scan shutdown-resume test no longer doubles its results

- `TestShutdownLeavesBatchRunningForResume` failed intermittently with 8 results instead of 4. The test restarts by building a second `PipelineManager` on the same store while the first one is still alive in the same process. Phase claims are held per manager, so a `groups_enrich` still running in the first manager ran a second time in the second, both managers cross-validated, and the two concurrent result writes kept every result twice. The test now waits for the first manager to settle its groups side (`awaitGroupsSettled`) before the restart, the same way the other two-manager resume tests do. This was a test-only fault: production builds one `PipelineManager` per process, and a real restart ends the old process.
