#!/usr/bin/env python3
"""Delete all branches with rebase conflicts."""

import subprocess

conflicts = [
    "audit/branch-conflicts",
    "cleanup/fix-branch-conflicts",
    "feat/activity-log-ux-overhaul",
    "feat/cache-metrics",
    "feat/dedup-metadata-hash",
    "feat/dedup-scan-status-api",
    "feat/dedup-toolbar-clarity",
    "feat/match4-import-hash-dedup",
    "feat/property-based-tests",
    "feat/rate-2-star-widget",
    "feat/settings-protected-paths-ui",
    "feat/slog-w9b",
    "feat/spec2-optimize-button",
    "feat/uos-12-maintenance-plugin",
    "feat/uos-pending-review",
    "feat/uos-selection-spec",
    "feat/wire-scan-service",
    "feature/activity-frontend-render",
    "fix-uos-09-itunes",
    "fix-uos-09-lifecycle",
    "fix-uos-11-imports",
    "fix-uos-11-plugins",
    "fix-uos-12-config-leak",
    "fix-uos-12-nilguard",
    "fix/restore-backup-tests",
    "fleet/020-backfill-async-3-metadata-hash-ui",
    "rebase-uos-10",
    "rebase-uos-11",
    "rebase-uos-12",
    "refactor/library-filter-panel",
    "refactor/struct-3-maintenance-fixups-split",
    "ui/library-header-kebab-menu",
    "worktree-agent-a1c6398f41201007c",
    "worktree-agent-a3365816",
    "worktree-agent-a6026b780a029f299",
    "worktree-agent-a652741ec35647520",
    "worktree-agent-a6f97278541497827",
    "worktree-agent-a73b332298c8609b3",
    "worktree-agent-a83fd994",
    "worktree-agent-ac49815a411d89251",
]


def run(cmd):
    subprocess.run(cmd, shell=True, capture_output=True)


print(f"Deleting {len(conflicts)} conflict branches...\n")

deleted = 0
failed = 0
for branch in conflicts:
    result = subprocess.run(f"git branch -D {branch}", shell=True, capture_output=True, text=True)
    if result.returncode == 0:
        print(f"✓ {branch}")
        deleted += 1
    else:
        print(f"✗ {branch}")
        failed += 1

print(f"\n{'=' * 60}")
print(f"Deleted: {deleted}")
print(f"Failed: {failed}")
