<!-- file: .claude/notes/fix-ops-preview-by-default-progress.md -->
<!-- version: 1.0.0 -->
<!-- guid: ba9ea3aa-1777-4ba4-85c6-6afc338eb305 -->
<!-- last-edited: 2026-09-25 -->

# fix/ops-preview-by-default — progress

## Done
- Shared helper `internal/operations/opmode` (ResolveDryRun / ParseDryRun, default true).
- author.go + scheduler/extra_ops.go parse helpers replaced by opmode.ParseDryRun.
- author-id-repair, author-path-link, repoint-missing-to-folder-audio resolution moved onto opmode.
- 7 plugin ops (itunes-regroup, itunes-playlist-import, tag-backfill, booksig-sidecar-migrate,
  fs-regroup-xml, booksig-recovery-audit, title-backfill) -> *bool + snake alias.
- itunes.path-repair (was LIVE on {}), operations.backfill-legacy-status, dedup.split-book-bulk-merge
  (was LIVE with items and no dry_run) -> *bool.

## Next
- Flip DefaultParams dry_run true for 8 maintenance jobs that honor dryRun.
- Guard tests: maintenance.All() iteration, per-op resolver table, AST scan; mutation check.
- Inventory doc docs/audits/2026-09-25-op-preview-default-inventory.md; changelog fragment.
- go vet/test touched packages, make ci, rebase + push proposed-main.
