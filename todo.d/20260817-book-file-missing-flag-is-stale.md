### 🐛 `book_file.missing` / `file_exists` are stale — nothing maintains them

Measured 2026-08-17 against prod: all **2,077** `book_file` rows belonging to the
60 fully-broken books reported `missing: false` and `file_exists: true` via
`GET /audiobooks/:id/files`, while `maintenance.missing-file-audit` proves every
one of those paths fails `os.Stat`.

They are stored columns, not live checks, and no writer keeps them current.

- **Do not filter on them.** Any query that treats `missing = false` as "the file
  is there" is silently wrong, and would report a fully-broken library as healthy.
- Audit who reads them before deciding the fix — either maintain them or remove
  them so nothing can be misled.
- **The cheap fix is already half-built:** `maintenance.missing-file-audit` stats
  every `book_file` path and therefore computes exactly this truth on every run
  (532,296 rows in ~168s). It just discards it. Persisting the per-row verdict
  would make the columns correct as a side effect of a job that already runs.

⚠️ **Unowned.** Neither of the two sessions working this repo on 2026-08-17 owns it:
the maintenance-v2 lane owns the `MaintenanceJob` → `OperationDef` migration, not
these columns; the prod-ops lane found it but does not own
`internal/plugins/maintenance`. Surfaced deliberately rather than absorbed by
either, so it needs an owner assigned.

Found while measuring signal coverage for the missing-file repoint work; see
`docs/audits/2026-08-17-missing-file-audit-full-population.md` §9.
