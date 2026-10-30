<!-- file: docs/decisions/2026-10-30-reports-dir-prune.md -->
<!-- version: 1.0.0 -->
<!-- guid: 4b8e2d17-6c3a-4f90-a5d2-9e1c7b03f6a4 -->
<!-- last-edited: 2026-10-30 -->

# Decision log: pruning the library `.reports` directory

Task: TODO-REPORTS-PRUNE. Taken autonomously under the 2026-10-30 instruction; review when convenient.

## Chosen

- A new op `maintenance.reports-prune` (`internal/plugins/maintenance/reports_prune.go`), driven nightly by a scheduler task `reports_prune` in the maintenance window, the way `ai_journal_prune` is. The def declares no cron `Schedule` of its own: a def with a Schedule needs a task driver anyway, and the task is where `dry_run=false` is passed (the op previews when the mode is omitted, per the 2026-09-25 rule).
- Retention: keep the newest 3 reports per op kind AND anything younger than 14 days; a file is removed only when it fails both. Params `keep_per_kind` and `max_age_days`, each with a floor of 1.
- Which files: an explicit allow-list of op kinds (`reportsPruneKinds`) and the name shape `<kind>-<opID>.tsv`. The allow-list was built by grepping every writer into `.reports` (the `opReportFileName` callers plus `backfill-file-hashes`).
- Live runs: ids of queued or running operations (`ListActiveOperationsV2`) are never removed; an unreadable queue refuses the whole run. Names containing `.pending` are never touched.
- Fail closed: root must be absolute; `.reports` must be a real directory (Lstat); every file is Lstat-ed and only regular files are removed.

## Findings that contradict the task text

- No writer in the codebase uses a `.pending` suffix today. The rule is implemented as a precaution (and a `.pending` name never matches the `.tsv` allow-list anyway).
- `dedupe-book-file-rows` writes fixed names (`dedupe-book-file-rows-<mode>.tsv` and a `-decisions` twin) that are overwritten in place, so it does not grow and is not in the allow-list.

## Rejected

- Age only, or count only: count only deletes a kind's whole history after a burst of reruns; age only keeps a 100 MB file per night forever if the op runs nightly. The conjunction bounds both.
- Floor of 0 for the params: a paused backfill continues under the same op id but is not in the active queue, so 0 could delete a report that is about to be resumed. The 1-day floor on age plus the newest-N rule protects it.
- Deriving "live" from file mtime alone: a long-running quiet run has an old mtime; the queue is the authority.
- Prefix-matching any `*.tsv`: the directory is inside the shared library volume, and an operator's own files must survive.
