### Added

- Books declares a core schedule for every task HEAD's scheduler ran (F5-14b): `books.NewSchedules` gives 35 kinds a `ScheduleFromConfig` schedule over a key under `modules.books.scheduler.schedules`, with HEAD's gates (config switches, plugin presence, the identification and collections idle checks, library_scan's three switches) and its run mode. 19 new schedule keys carry the cadence HEAD hard-coded; the tasks HEAD's maintenance window ran keep their window position. `transcode` is deleted; `batch_poller` and `purge_old_logs` are core's.
- A gate HEAD read in a task's `IsEnabled` (catalog.enabled, the plugin-presence checks, the purge and AI-journal retentions, enable_ai_parsing, the db_optimize and acoustid toggles, nightly compaction, identification_advance_enabled) gates the interval with `IntervalWhen` and the window with `WindowWhen`: while it is closed the kind's clock stays where it was, and the first boot that finds it open catches up once on an overdue clock, as HEAD did. Only the idle checks HEAD ran inside the trigger stay `When` gates.
- `TestScheduleParity_AllDefinitions` runs HEAD's real scheduler on a fake clock beside core's over the same config (through M2's real conversion) and requires the same enqueues, window members and clocks, both ways, at every step, across 31 config rows (each window toggle alone among them).
- `TestCutover_NoCatchUpStorm` cuts a store HEAD ran over with M4 and checks core's first boot: only the on-start kinds fire, no kind fires before HEAD's next due instant, and every imported clock fires next at that instant, except an on-start kind, whose next scheduled run is one interval after the boot.

### Changed

- An on-start kind (purge_deleted, temp_file_cleanup, transcode_temp_cleanup, library_scan with scan_on_startup) restarts its interval at each boot's start run under core, where HEAD's start run left the old phase in place and could run the task again minutes later.

### Removed

- The 15 v2 `Schedule` cron strings have no core equivalent (Q-C, D15): 14 of their defs keep running at the cadence of the task that fires them, and `deluge.protected-paths-sync` is manual.
- The `scheduled.<entry>.on_startup` override is gone: on-start runs follow HEAD's fixed per-task rule (and `scan_on_startup` for the library scan).
