<!-- file: .claude/notes/fix-ops-preview-by-default-progress.md -->
<!-- version: 1.2.0 -->
<!-- guid: ba9ea3aa-1777-4ba4-85c6-6afc338eb305 -->
<!-- last-edited: 2026-09-25 -->

# fix/ops-preview-by-default — progress

## Done
- Shared helper `internal/operations/opmode` (ResolveDryRun / ParseDryRun, default true).
- author.go + scheduler/extra_ops.go parse helpers replaced by opmode.ParseDryRun.
- author-id-repair, author-path-link, repoint-missing-to-folder-audio resolution moved onto opmode.
- 7 plugin ops + operations.backfill-legacy-status -> *bool both spellings.
- itunes.path-repair and dedup.split-book-bulk-merge (both LIVE on omitted mode) -> *bool.
- 8 maintenance jobs flipped to advertise dry_run:true.
- Guards: AST scan (opmode/guard_test.go), jobs registry iteration, wiring tests. Mutation-checked both guards.
- Inventory doc, changelog fragment, todo.d follow-ups.

- Rebased onto origin/proposed-main; build, vet, touched-package tests pass.
- make ci reds not from this branch: logger slog ratchet (dedup/engine.go, from 082ff2e82),
  sdkguard (internal/chaptershape), fmt-check (author_strip_merge.go), database 25m timeout.

## Next
- Push to proposed-main by SHA; hand back.
