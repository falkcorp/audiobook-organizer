### `PUT /tasks/:name` refuses 16 of the 27 registered scheduler tasks

`bindingForTask` in `internal/server/handlers/operations/handler.go` covers 12
task names. The scheduler registers 27. The other 16 fall to the "task %q config
is not configurable" 400:

`acoustid_online_lookup`, `ai_dedup_batch`, `archive_sweep`, `batch_poller`,
`cleanup_activity_log`, `cleanup_old_backups`, `dedup_llm_review`,
`isbn_enrichment`, `label_refinement`, `library_size_refresh`,
`metadata_upgrade`, `resolve_production_authors`, `series_normalize`,
`temp_file_cleanup`, `transcode`, `trash_cleanup`

**This is pre-existing, not a regression.** The switch that `bindingForTask`
replaced had the same 12 names and the same `default:` 400 — verified against
`a422b4d7^`. It is filed here because the binding table now makes the gap
countable, and because it fails LOUDLY (400), which is a different and much less
dangerous defect than the silent 200 that PR #2502 fixes.

At least one of the 16 has real config behind it: `ai_dedup_batch`'s IsEnabled is
`Scheduled.AIDedupBatch.Enabled && config.AppConfig.EnableAIParsing`
(`internal/scheduler/tasks.go:745`), so there is a per-task `enabled` field the
endpoint declines to write. Note the `&&` — the same getter-reads-more-than-the-
bound-field shape that `library_scan` had, so a binding added for it needs the
same treatment (see `foldLegacy`, and prefer rejecting with a hint naming
`enable_ai_parsing` over pretending the per-task flag is sufficient).

Before adding bindings, check what the Maintenance settings page actually renders
for these 16 — if it shows editable controls for them, users are getting a 400
from a control that looks live.

Do NOT add a binding without also extending
`TestUpdateTaskConfig_SchedulerReportsTheAppliedValue`, which reads the value
back through the real `TaskDefinition` getters via `scheduler.ListTasks()`. The
field-level assertion alone cannot see an OR/AND mask — that is exactly how the
`library_scan` masking survived the first round of tests.
