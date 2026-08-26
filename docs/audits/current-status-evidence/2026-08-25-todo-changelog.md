<!-- file: docs/audits/current-status-evidence/2026-08-25-todo-changelog.md -->
<!-- version: 1.0.0 -->
<!-- guid: 76b5bb99-4032-42fa-9ea0-3be3a8e643e0 -->
<!-- last-edited: 2026-08-25 -->

# TODO and CHANGELOG Reconciliation Evidence

## Census

At `main` commit `5e95fad6`, `TODO.md` has 498 direct checkbox lines: 97
checked and 401 unchecked. It is not a clean live-only backlog despite its
heading: it contains historical/completed material and two version headers.
`CHANGELOG.md` was last assembled on 2026-08-08, so it cannot be the sole
source for fixes merged through 2026-08-25.

## Scan, import, and metadata findings

- The new import and scanner paths now create `BookFile` rows: see
  `internal/importer/service.go:341-357` and
  `internal/scanner/scanner.go:1510-1517`. Existing damaged populations still
  require a separate repair decision.
- `library.ai-parse` and primary-row verification need a real production scan;
  this is verification work, not known missing code (`TODO.md:25-42`,
  `TODO.md:380-391`).
- The staged scanner/cache redesign and first-run counters remain valid open
  work (`TODO.md:403-428`).
- Explicit user-owned metadata fields are now protected against scan overwrite
  (`internal/scanner/override_guard.go:33-57`), but provider IDs are not yet
  locked by equivalent vocabulary.

## Corrections required

1. `TODO.md:3198-3209` leaves F7 unchecked although `ApplyMetadataFileIO`
   returns errors since `c946d0c8f`, and batch apply exposes failures.
2. `TODO.md:7688-7749` overstates the scan/metadata overwrite problem after
   `ea939404b`; retain a narrower provider-ID/UX follow-up instead.
3. `TODO.md:396-399` is a decided no-op still represented as unchecked work.
4. `TODO.md:12672` contradicts its own old claim at `:12686-12691` that no
   chapter backfill exists; retain the production-run task at `:2946-2955`.
5. `CHANGELOG.md:98-103` incorrectly says chapter persistence is not wired
   into the scanner, while current scanner paths call `PersistChaptersForBook`.

## Valid priority work

- Per-batch `BookFile` dedup and the historical-row repair decision
  (`TODO.md:90-132`).
- Staged scanner pipeline and first completed-scan metrics (`TODO.md:403-428`).
- One-row-per-track and missing-record population repairs.
- Metadata-organize residuals F5/F6 (`TODO.md:3211-3224`).
- Chapters-backfill dry-run/apply and durable no-result marker
  (`TODO.md:2946-2967`).

## Source precedence

Use current `main` and dated `changelog.d` fragments for shipped code,
`TODO.md` for current engineering work after correction, and
`docs/operations/pending-prod-actions.md` plus `docs/dedup/STATUS.md` for
production actions and measured state. Never infer production completion from
merged code.
