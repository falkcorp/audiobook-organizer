<!-- file: docs/audits/2026-08-27-library-reliability-current-status.md -->
<!-- version: 1.1.0 -->
<!-- guid: 6c884144-c4fe-4f5a-bbdc-1a103cd5aa13 -->
<!-- last-edited: 2026-08-27 -->

# Library reliability current status

## Production canary: verified

The scan was terminal before the canary. Production has `auto_organize=true`,
`organization_strategy=auto`, and the intended library root configured.

The import/organize canary for *Starbreaker: Volume 5 - Starbreaker* completed
successfully. It created an organized primary version under the library root and
retained the source version under `newbooks`. Both files exist, have the same
604,765,452-byte size, and have identical SHA-256 values. Their inodes differ
and both have a link count of one, so the result is not a hardlink. With the
configured auto strategy (reflink, then hardlink, then copy), the 605 MB transfer
completed in roughly 0.3 seconds; that is consistent with a reflink and not a
full copy. The filesystem did not expose extent mappings, so the method is
recorded as an evidence-backed inference rather than a directly emitted metric.

An earlier canary was correctly deferred because dedup had already assigned its
source record to a version group with a primary. That showed version-group
deduplication working; it was not used as transfer evidence.

## Merged

- PR #2937: runtime-mismatch review filter and shift-selection regression
  coverage. Merged; deploy with the next approved production release.
- PR #2938: `backfill-book-files` now writes each eligible book's file rows in
  one atomic batch. Merged as `85001ca7d` and deployed successfully.

## Book-file backfill: complete and idempotent

The deployed `backfill-book-files` job was run first with `dry_run=true`, then
with `dry_run=false`. Both runs scanned 57,822 books and reported zero candidate
files, zero created rows, and zero errors. The apply run therefore made no
changes; the prior repair remains complete and the new batch writer is safe to
run again.

## In flight

- PR #2936 enables LFS checkout on all workflow checkout steps and recursive
  submodules where absent. Its first CI run exposed unrelated existing shell
  lint; the targeted lint fixes are pushed and the rerun is pending.
- Shared file-write concurrency (default four, globally bounded, cap eight) is
  committed on `codex/library-reliability` as `6a945c37f`.
- Explicit persisted `chapter_consolidation_threshold_min=0` is now auditable
  without overriding an intentional disable, committed as `f5cce0c32`.

## Remaining before broad repair work

1. Finish durable semantic coalescing for one-book metadata apply/save requests;
   do not coalesce scans, imports, organizes, or destructive merges.
2. Produce a unique-file scan plan that preserves the longest matching import
   root source identity.
3. Run `dedup.split-book-scan` preview, retain its census, and only then design
   a durable fragment-merge executor that preserves partial failures.
