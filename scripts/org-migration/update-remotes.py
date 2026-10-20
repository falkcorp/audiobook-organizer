#!/usr/bin/env python3
# file: scripts/org-migration/update-remotes.py
# version: 1.0.2
# guid: f361303b-7845-48b0-96d4-1348d770c811
# last-edited: 2026-10-20

"""Phase 8: Update local git remotes to point to falkcorp."""

import os
import subprocess

BASE = os.path.expanduser("~/repos/github.com/jdfalk")
FALKCORP_DIR = os.path.expanduser("~/repos/github.com/falkcorp")

RENAMES = {
    "ghcommon": "github-common",
    "release-go-action": "gha-release-go",
    "release-python-action": "gha-release-python",
    "release-rust-action": "gha-release-rust",
    "release-docker-action": "gha-release-docker",
    "release-frontend-action": "gha-release-frontend",
    "release-protobuf-action": "gha-release-protobuf-base",
    "auto-module-tagging-action": "gha-auto-module-tagging",
    "ci-workflow-helpers-action": "gha-ci-workflow-helpers",
    "generate-version-action": "gha-generate-version",
    "get-frontend-config-action": "gha-get-frontend-config",
    "package-assets-action": "gha-package-assets",
    "docs-generator-action": "gha-docs-generator",
    "release-strategy-action": "gha-release-strategy",
    "update-action-docker-ref-action": "gha-update-action-docker-ref",
    "load-config-action": "gha-load-config",
    "detect-languages-action": "gha-detect-languages",
    "ci-generate-matrices-action": "gha-ci-generate-matrices",
    "security-summary-action": "gha-security-summary",
    "pr-auto-label-action": "gha-pr-auto-label",
    "jft-github-actions": "gha-template-repo",
}

os.makedirs(FALKCORP_DIR, exist_ok=True)

for entry in sorted(os.listdir(BASE)):
    repo_dir = os.path.join(BASE, entry)
    if not os.path.isdir(os.path.join(repo_dir, ".git")):
        continue

    new_name = RENAMES.get(entry, entry)
    new_remote = f"https://github.com/falkcorp/{new_name}.git"

    result = subprocess.run(
        ["git", "remote", "get-url", "origin"], capture_output=True, text=True, cwd=repo_dir
    )
    current = result.stdout.strip()

    if not current:
        print(f"SKIP  {entry} (no remote)")
        continue

    if "github.com/jdfalk/" in current or (
        "github.com/falkcorp/" in current and current != new_remote
    ):
        subprocess.run(["git", "remote", "set-url", "origin", new_remote], cwd=repo_dir, check=True)
        print(f"UPDATE {entry} → {new_remote}")
    else:
        print(f"OK    {entry} ({current})")

    # Symlink falkcorp/new_name → jdfalk/old_name
    symlink = os.path.join(FALKCORP_DIR, new_name)
    if not os.path.exists(symlink):
        os.symlink(repo_dir, symlink)
        print(f"LINK  {symlink} → {repo_dir}")

print("\nDone. Symlinks created at ~/repos/github.com/falkcorp/")
