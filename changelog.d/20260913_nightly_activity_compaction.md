### Added

- Nightly activity compaction (`maintenance.nightly-compact-activity-log`, scheduler task `nightly_activity_compaction`). It collapses every activity entry from before the start of the current local day into daily digests, on both activity backends, and keeps today in full detail. Two new settings control it: `activity_log_nightly_compaction_enabled` (default `true`, and toggleable on the Maintenance tab) and `activity_log_full_detail_days` (default `0`, meaning "today only", accepting whole days from 0 to 36500).

### Changed

- While nightly compaction is enabled, `maintenance.cleanup-activity-log` no longer runs its own compaction pass. Summarize, prune and index repair still run. `activity_log_compaction_days` keeps its old meaning (0 means 14) and applies only when nightly compaction is disabled. Production's stored `0` is unchanged.
