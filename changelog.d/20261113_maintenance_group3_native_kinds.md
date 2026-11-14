### Changed

- Fourteen maintenance operations (the purge, tombstone, temp-file, activity-log, backup and trash cleanups, clear-apply-rename-failures, the combine-journal check, the activity compaction, the folder-cover and cover-text jobs, the credit census and the credits backfill) now have a second, native task definition built from the same body as the existing one, so the two cannot drift. Nothing registers the new definitions yet: behaviour of every operation is unchanged until the task engine is switched on.
