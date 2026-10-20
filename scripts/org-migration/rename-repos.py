#!/usr/bin/env python3
# file: scripts/org-migration/rename-repos.py
# version: 1.0.1
# guid: 386495ae-52d1-4a7a-935a-f6673b75c538
# last-edited: 2026-10-20

"""
Phase 2: Rename all in-scope repos within jdfalk personal account.
Renames happen BEFORE org transfer so redirects are in place.
GitHub creates automatic redirects for each rename.
"""

import subprocess
import sys
import time

RENAMES = [
    # (old_name, new_name)
    ("ghcommon", "github-common"),
    ("release-go-action", "gha-release-go"),
    ("release-python-action", "gha-release-python"),
    ("release-rust-action", "gha-release-rust"),
    ("release-docker-action", "gha-release-docker"),
    ("release-frontend-action", "gha-release-frontend"),
    ("release-protobuf-action", "gha-release-protobuf-base"),
    ("auto-module-tagging-action", "gha-auto-module-tagging"),
    ("ci-workflow-helpers-action", "gha-ci-workflow-helpers"),
    ("generate-version-action", "gha-generate-version"),
    ("get-frontend-config-action", "gha-get-frontend-config"),
    ("package-assets-action", "gha-package-assets"),
    ("docs-generator-action", "gha-docs-generator"),
    ("release-strategy-action", "gha-release-strategy"),
    ("update-action-docker-ref-action", "gha-update-action-docker-ref"),
    ("load-config-action", "gha-load-config"),
    ("detect-languages-action", "gha-detect-languages"),
    ("ci-generate-matrices-action", "gha-ci-generate-matrices"),
    ("security-summary-action", "gha-security-summary"),
    ("pr-auto-label-action", "gha-pr-auto-label"),
    ("jft-github-actions", "gha-template-repo"),
]

dry_run = "--dry-run" in sys.argv


def gh(args):
    result = subprocess.run(["gh"] + args, capture_output=True, text=True)
    return result.stdout.strip(), result.stderr.strip(), result.returncode


def repo_exists(name):
    _, _, rc = gh(["repo", "view", f"jdfalk/{name}", "--json", "name"])
    return rc == 0


def rename_repo(old_name, new_name):
    if not repo_exists(old_name):
        # Maybe already renamed
        if repo_exists(new_name):
            print(f"  SKIP  {old_name} → {new_name} (target already exists, assuming done)")
            return True
        print(f"  MISS  {old_name} → {new_name} (source not found on GitHub)")
        return False

    if dry_run:
        print(f"  DRY   {old_name} → {new_name}")
        return True

    stdout, stderr, rc = gh(
        [
            "api",
            "-X",
            "PATCH",
            f"/repos/jdfalk/{old_name}",
            "-f",
            f"name={new_name}",
        ]
    )
    if rc == 0:
        print(f"  OK    {old_name} → {new_name}")
        return True
    else:
        print(f"  FAIL  {old_name} → {new_name}: {stderr or stdout}")
        return False


if dry_run:
    print("=== DRY RUN (pass no args to execute) ===\n")

ok, fail, skip = 0, 0, 0
for old, new in RENAMES:
    result = rename_repo(old, new)
    if result:
        ok += 1
    else:
        fail += 1
    time.sleep(0.5)

print(f"\nDone: {ok} renamed, {fail} failed")
if fail > 0:
    sys.exit(1)
