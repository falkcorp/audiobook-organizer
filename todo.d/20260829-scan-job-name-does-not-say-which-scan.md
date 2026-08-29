### A running scan does not say which kind of scan it is

Reported by the user: the job name gives no way to tell an incremental scan from
a full sweep. Confirmed — it cannot, because two different scheduled tasks report
through one operation definition:

- `internal/server/library_core_ops.go:51` — `DisplayName: "Library Scan"`, the
  single display name for op id `library.scan`.
- `internal/scheduler/tasks.go:104` — scheduled task `library_scan`, the periodic
  incremental scan.
- `internal/scheduler/tasks.go:185` — scheduled task `library_scan_full`, the
  weekly full sweep that re-reads and re-hashes every file.

So "Library Scan" in the operations list and the bell covers both a cheap
incremental pass and a full re-hash of the library, with nothing to distinguish
them. A full sweep is the expensive one and the one worth knowing about.

⚠️ Do NOT fix this by splitting `library.scan`'s ConcurrencyKey — that key is
load-bearing and splitting it reintroduces the 2026-08-07 silent field-loss.
The fix is in how the operation is LABELLED, not how it is keyed.

- [ ] Give the running operation a display name that names the mode
      (incremental vs full), sourced from the task that started it
- [ ] Check the progress/log lines too — "Reading tags: N files" never says
      which sweep it belongs to
