### Changed

#### A core schedule can carry an interval and a maintenance-window slot

`tasks.ScheduleConfig` now accepts `every` together with `window` (owner ruling 2026-11-20). Such a kind is polled on its interval and is also started by its window, and it fires on whichever comes due; a start that finds the kind's run active folds into it, so one due moment never starts two runs. `window` with `daily_at` or `cron` is still refused. A window member's on-start schedule now fires at boot. This keeps HEAD's cadence for the five books tasks that tick on an interval and also sit in the nightly window (`tombstone_cleanup`, `reports_prune`, `purge_deleted`, `cleanup_activity_log`, `ai_journal_prune`) and for `temp_file_cleanup`'s start-up run. Nothing registers a core-scheduled books kind yet, so production behaviour is unchanged.

### Added

#### A clock seam on the books scheduler

`internal/scheduler.SchedulerDeps` gains optional `Now`, `Location` and `Tick` fields so the schedule-parity oracle (F5-14) can drive the books scheduler's own due rules with a fake clock. Left nil, as production leaves them, they are the wall clock, the local zone and a real ticker, exactly as before.
