<!-- file: docs/audits/current-status-evidence/2026-08-25-todo-fragments.md -->
<!-- version: 1.0.0 -->
<!-- guid: 3c2ef6f0-eccb-4e5e-af9a-c6ff1e94fc04 -->
<!-- last-edited: 2026-08-25 -->

# todo.d Reconciliation Evidence

## Verdict and census

`todo.d` and `TODO.md` are not in sync. `python3 scripts/assemble_todo.py
--check` reports 16 pending nonempty fragments after the collector's
2026-08-25 07:59 UTC snapshot. The collector's daily latency explains much of
the gap, but aggregate quality defects also exist.

- Pending fragments: 16 blocks, 37 open and 2 completed raw checkbox lines.
- Aggregate raw checkbox lines: 409 open and 106 completed; it is not a pure
  backlog because it contains quoted/historical material.
- The aggregate has duplicate version headers at `TODO.md:2-3`. The assembler
  only updates the first (`scripts/assemble_todo.py:301-303`).

## Do not collect unchanged

- `20260825-book-file-creation-regression.md`: diagnostic prose without an
  actionable checklist; superseded by the more precise root-cause fragment.
- `20260825-import-organize-flag-and-fast-path-filters.md`: contains two
  completed entries; split it and retain only its live fast-path task.
- `20260825-path-author-parser-exists-twice.md`: partly duplicates the
  existing directory-fallback task at `TODO.md:503-505`.
- `20260825-repair-duplicate-author-rows.md`: exact duplicate of the existing
  repair decision at `TODO.md:440-473`.

## Valid fragments relevant to readiness

The chapter-consolidation production fix, per-file scan-cache backfill,
single-file/backfill residue, LLM fallback stages, and placeholder repair are
valid inputs but need curation before collection. Preserve explicit owner and
production gates in those tasks.

## Root causes of drift

The collector is intentionally add-only and deletes consumed fragments
(`todo.d/README.md:68-75`), while the scheduled workflow is daily
(`.github/workflows/todo-collect.yml:14-19`). It does not validate task shape,
duplicates, or completed content. Its lint catches leaked fragment headers but
not duplicate aggregate headers. Filename convention drift (`YYYYMMDD` versus
documented `YYYY-MM-DD`) is currently unenforced.

## Required remediation

1. Repair `TODO.md` to one version header.
2. Retire/rewrite the zero-task diagnostic fragment.
3. Split completed entries from the import fragment.
4. Fold duplicate/companion fragment work into the existing aggregate tasks.
5. Collect the remaining valid fragments, then keep their status current after
   related changes ship.
