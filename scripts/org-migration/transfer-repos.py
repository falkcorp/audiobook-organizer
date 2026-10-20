#!/usr/bin/env python3
# file: scripts/org-migration/transfer-repos.py
# version: 1.0.1
# guid: 798c668a-6864-4700-b10b-1cd5f5c36fa0
# last-edited: 2026-10-20

"""
Phase 3: Transfer all in-scope repos to falkcorp org.
Uses post-rename names. Run after rename-repos.py completes.
Transfers in tier order to minimize broken-reference window.
"""

import subprocess
import sys
import time

# Ordered by dependency tier (Tier 0 first = fewest dependencies)
TRANSFERS = [
    # Tier 0 — standalone action repos
    "gha-release-go",
    "gha-release-python",
    "gha-release-rust",
    "gha-release-docker",
    "gha-release-frontend",
    "gha-release-protobuf",
    "gha-release-protobuf-base",
    "gha-auto-module-tagging",
    "gha-ci-workflow-helpers",
    "gha-generate-version",
    "gha-get-frontend-config",
    "gha-package-assets",
    "gha-docs-generator",
    "gha-release-strategy",
    "gha-update-action-docker-ref",
    "gha-load-config",
    "gha-detect-languages",
    "gha-ci-generate-matrices",
    "gha-security-summary",
    "gha-pr-auto-label",
    "gha-template-repo",
    # Tier 0 — standalone app/lib repos
    "safe-ai-util",
    "gcommon",
    "gcommon-proto",
    "transcoderr",
    "cockroach-rollout-agent",
    "ubuntu-autoinstall-agent",
    "mtls-bridge",
    # Tier 1 — depends on Tier 0
    "safe-ai-util-mcp",
    "burndown-runner-image",
    # Tier 2 — central hub
    "github-common",
    "burndown-tasks",
    # Tier 3 — depends on hub
    "overnight-burndown",
    "overnight-burndown-reconcile",
    "overnight-burndown-closing",
    "overnight-burndown-providers",
    # Tier 4 — primary applications
    "audiobook-organizer",
    "migrate-loop",
]

dry_run = "--dry-run" in sys.argv
new_owner = "falkcorp"


def gh(args):
    result = subprocess.run(["gh"] + args, capture_output=True, text=True)
    return result.stdout.strip(), result.stderr.strip(), result.returncode


def in_falkcorp(name):
    _, _, rc = gh(["repo", "view", f"{new_owner}/{name}", "--json", "name"])
    return rc == 0


def in_jdfalk(name):
    _, _, rc = gh(["repo", "view", f"jdfalk/{name}", "--json", "name"])
    return rc == 0


def transfer_repo(name):
    if in_falkcorp(name):
        print(f"  SKIP  {name} (already in falkcorp)")
        return True

    if not in_jdfalk(name):
        print(f"  MISS  {name} (not found in jdfalk)")
        return False

    if dry_run:
        print(f"  DRY   jdfalk/{name} → falkcorp/{name}")
        return True

    stdout, stderr, rc = gh(
        [
            "api",
            "-X",
            "POST",
            f"/repos/jdfalk/{name}/transfer",
            "-f",
            f"new_owner={new_owner}",
        ]
    )
    if rc == 0:
        print(f"  OK    jdfalk/{name} → falkcorp/{name}")
        return True
    else:
        print(f"  FAIL  {name}: {stderr or stdout}")
        return False


if dry_run:
    print("=== DRY RUN (pass no args to execute) ===\n")

ok, fail = 0, 0
for repo in TRANSFERS:
    result = transfer_repo(repo)
    if result:
        ok += 1
    else:
        fail += 1
    time.sleep(1)  # Avoid rate limits; transfer is heavy

print(f"\nDone: {ok} transferred, {fail} failed")
if fail > 0:
    sys.exit(1)
