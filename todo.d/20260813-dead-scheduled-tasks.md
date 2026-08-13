## ✅ Six scheduled tasks were enabled but could never run

Found by the three-state startup diagnostic added in #2346, on the first production boot
that carried it (2026-08-13 02:45 UTC). Of 18 registered tasks: 5 had a working ticker, 7
were reachable through the nightly maintenance window, and **6 were enabled and dead**.

Four of the six declared `RunInMaintenanceWindow` but were **absent from
`maintenanceOrder`** in `NewTaskScheduler`. `internal/server/scheduler_maintenance_window_op.go:97`
iterates `MaintenanceOrder()` and only then checks `IsEnabled() && RunInMaintenanceWindow()`,
so a task missing from the list is unreachable no matter what the toggle says. This is the
*same* dead-config shape already documented in the `maintenanceOrder` comment for
`library_scan` — it recurred four more times because nothing checked.

| Task | Was | Now |
|---|---|---|
| `temp_file_cleanup` | declared window, not listed | listed with the cleanup cluster |
| `trash_cleanup` | declared window, not listed | listed with the cleanup cluster |
| `archive_sweep` | declared window, not listed | listed with the cleanup cluster |
| `library_organize` | declared window, not listed | listed late (mutates files, expensive) |
| `transcode` | `IsEnabled: true`, trigger fails by design | `IsEnabled: false` |
| `series_normalize` | `IsEnabled: true`, no timer, opts out of window | `IsEnabled: false` |

Three of these are unbounded on-disk leaks that had never run: orphaned `*.tmp.m4b` /
`*.tmp.m4a` from crashed ffmpeg, trashed versions past their 14-day TTL, and soft-deleted
books past the 30-day retention window.

`transcode` and `series_normalize` are marked disabled rather than given tickers because
neither can usefully run unattended — `transcode`'s scheduled `TriggerFn` fails on purpose
without a `book_id`, and `series_normalize` moves files. `runTask` does not consult
`IsEnabled`, so manual/API invocation is unchanged; only the automatic paths and the
displayed state are affected.

### Recurrence guard

`internal/scheduler/task_reachability_test.go` asserts every registered task is
timer-driven, wired into `maintenanceOrder`, or explicitly disabled, plus two narrower
checks (no `maintenanceOrder` entry naming a non-existent task; no task claiming the window
while absent from the list). The invariant checks **wiring**, not configuration — it uses
`inMaintenanceOrder` rather than `reachableViaMaintenanceWindow`, because the latter reads
`config.AppConfig.Maintenance.*`, which is zero under test, and a structural invariant that
fails on an operator's config choice gets muted.

### Not claimed / still open

- **How much disk the three leaks are holding is NOT measured.** Nothing counted the
  orphaned temp files, expired trash, or over-retention archives on prod before this
  landed. Worth measuring on the first window run after deploy.
- Whether the window has time to reach the newly-appended entries is untested: the op
  breaks out of the loop when the window closes (01:00–04:00 on prod), and `library_organize`
  plus `library_scan` sit at the end behind everything else.
- `metadata_upgrade`, `library_size_refresh` and `library_organize` are wired but gated on
  `config.Maintenance.*` toggles whose production values were not checked.
