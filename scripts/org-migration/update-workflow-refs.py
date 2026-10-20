#!/usr/bin/env python3
# file: scripts/org-migration/update-workflow-refs.py
# version: 1.0.2
# guid: 508d12b1-30df-41a5-ae8b-b03dca681dbe
# last-edited: 2026-10-20

"""
Phase 4: Update all cross-repo jdfalk/ → falkcorp/ references.
Opens a PR in each repo that has changes.

Run from any directory. Requires gh CLI and git.
"""

import glob
import os
import subprocess
import sys
import time

BRANCH = "chore/falkcorp-migration"
COMMIT_MSG = "chore: migrate jdfalk/ refs to falkcorp/ org"

# Order matters — most-specific first to avoid double-substitution
SUBSTITUTIONS = [
    # ghcommon → github-common
    ("jdfalk/ghcommon/", "falkcorp/github-common/"),
    ("jdfalk/ghcommon@", "falkcorp/github-common@"),
    # buf.build migration
    ("buf.build/jdfalk/", "buf.build/falkcorp/"),
    # *-action → gha-* renames (uses: references)
    ("jdfalk/load-config-action@", "falkcorp/gha-load-config@"),
    ("jdfalk/detect-languages-action@", "falkcorp/gha-detect-languages@"),
    ("jdfalk/ci-generate-matrices-action@", "falkcorp/gha-ci-generate-matrices@"),
    ("jdfalk/ci-workflow-helpers-action@", "falkcorp/gha-ci-workflow-helpers@"),
    ("jdfalk/get-frontend-config-action@", "falkcorp/gha-get-frontend-config@"),
    ("jdfalk/release-go-action@", "falkcorp/gha-release-go@"),
    ("jdfalk/release-python-action@", "falkcorp/gha-release-python@"),
    ("jdfalk/release-rust-action@", "falkcorp/gha-release-rust@"),
    ("jdfalk/release-docker-action@", "falkcorp/gha-release-docker@"),
    ("jdfalk/release-frontend-action@", "falkcorp/gha-release-frontend@"),
    ("jdfalk/release-protobuf-action@", "falkcorp/gha-release-protobuf-base@"),
    ("jdfalk/auto-module-tagging-action@", "falkcorp/gha-auto-module-tagging@"),
    ("jdfalk/package-assets-action@", "falkcorp/gha-package-assets@"),
    ("jdfalk/docs-generator-action@", "falkcorp/gha-docs-generator@"),
    ("jdfalk/release-strategy-action@", "falkcorp/gha-release-strategy@"),
    ("jdfalk/update-action-docker-ref-action@", "falkcorp/gha-update-action-docker-ref@"),
    ("jdfalk/generate-version-action@", "falkcorp/gha-generate-version@"),
    ("jdfalk/security-summary-action@", "falkcorp/gha-security-summary@"),
    ("jdfalk/pr-auto-label-action@", "falkcorp/gha-pr-auto-label@"),
    # gha-* repos — org change only
    ("jdfalk/gha-release-go@", "falkcorp/gha-release-go@"),
    ("jdfalk/gha-release-python@", "falkcorp/gha-release-python@"),
    ("jdfalk/gha-release-rust@", "falkcorp/gha-release-rust@"),
    ("jdfalk/gha-release-docker@", "falkcorp/gha-release-docker@"),
    ("jdfalk/gha-release-frontend@", "falkcorp/gha-release-frontend@"),
    ("jdfalk/gha-release-protobuf@", "falkcorp/gha-release-protobuf@"),
    ("jdfalk/gha-template-repo@", "falkcorp/gha-template-repo@"),
    # ghcr.io image references
    ("ghcr.io/jdfalk/burndown-runner-image:", "ghcr.io/falkcorp/burndown-runner-image:"),
    ("ghcr.io/jdfalk/load-config-action:", "ghcr.io/falkcorp/gha-load-config:"),
    ("ghcr.io/jdfalk/detect-languages-action:", "ghcr.io/falkcorp/gha-detect-languages:"),
    ("ghcr.io/jdfalk/ci-generate-matrices-action:", "ghcr.io/falkcorp/gha-ci-generate-matrices:"),
    ("ghcr.io/jdfalk/ci-workflow-helpers-action:", "ghcr.io/falkcorp/gha-ci-workflow-helpers:"),
    ("ghcr.io/jdfalk/get-frontend-config-action:", "ghcr.io/falkcorp/gha-get-frontend-config:"),
    ("ghcr.io/jdfalk/release-frontend-action:", "ghcr.io/falkcorp/gha-release-frontend:"),
    ("ghcr.io/jdfalk/generate-version-action:", "ghcr.io/falkcorp/gha-generate-version:"),
    ("ghcr.io/jdfalk/auto-module-tagging-action:", "ghcr.io/falkcorp/gha-auto-module-tagging:"),
    ("ghcr.io/jdfalk/package-assets-action:", "ghcr.io/falkcorp/gha-package-assets:"),
    ("ghcr.io/jdfalk/release-go-action:", "ghcr.io/falkcorp/gha-release-go:"),
    ("ghcr.io/jdfalk/release-protobuf-action:", "ghcr.io/falkcorp/gha-release-protobuf-base:"),
    ("ghcr.io/jdfalk/release-docker-action:", "ghcr.io/falkcorp/gha-release-docker:"),
    ("ghcr.io/jdfalk/release-rust-action:", "ghcr.io/falkcorp/gha-release-rust:"),
    # Application repo references
    ("jdfalk/audiobook-organizer", "falkcorp/audiobook-organizer"),
    ("jdfalk/burndown-tasks", "falkcorp/burndown-tasks"),
    ("jdfalk/burndown-runner-image", "falkcorp/burndown-runner-image"),
    ("jdfalk/safe-ai-util-mcp", "falkcorp/safe-ai-util-mcp"),
    ("jdfalk/safe-ai-util", "falkcorp/safe-ai-util"),
    ("jdfalk/gcommon-proto", "falkcorp/gcommon-proto"),
    ("jdfalk/gcommon", "falkcorp/gcommon"),
    ("jdfalk/overnight-burndown", "falkcorp/overnight-burndown"),
    ("jdfalk/migrate-loop", "falkcorp/migrate-loop"),
    # go module paths
    ("github.com/jdfalk/gcommon", "github.com/falkcorp/gcommon"),
    ("github.com/jdfalk/gcommon-proto", "github.com/falkcorp/gcommon-proto"),
]

FILE_PATTERNS = [
    ".github/workflows/*.yml",
    ".github/workflows/*.yaml",
    "action.yml",
    "action.yaml",
    "*.md",
    "go.mod",
    "buf.yaml",
    "buf.gen.yaml",
]

BASE = os.path.expanduser("~/repos/github.com/jdfalk")
dry_run = "--dry-run" in sys.argv
skip_pr = "--no-pr" in sys.argv


def run(cmd, cwd=None, check=True):
    result = subprocess.run(cmd, capture_output=True, text=True, cwd=cwd)
    if check and result.returncode != 0:
        raise RuntimeError(f"Command {cmd} failed: {result.stderr}")
    return result.stdout.strip()


def apply_substitutions(content):
    for old, new in SUBSTITUTIONS:
        content = content.replace(old, new)
    return content


def find_files(repo_dir):
    files = []
    for pattern in FILE_PATTERNS:
        matched = glob.glob(os.path.join(repo_dir, pattern), recursive=False)
        # Also check in .github/workflows/
        matched += glob.glob(
            os.path.join(repo_dir, ".github/workflows", os.path.basename(pattern)), recursive=False
        )
        files.extend(matched)
    # Deduplicate
    return list(set(files))


def process_repo(repo_name, repo_dir):
    if not os.path.isdir(repo_dir):
        print(f"  MISS  {repo_name} (not cloned at {repo_dir})")
        return False

    files = find_files(repo_dir)
    changed_files = []

    for fpath in files:
        if not os.path.isfile(fpath):
            continue
        try:
            with open(fpath, encoding="utf-8") as f:
                original = f.read()
        except (UnicodeDecodeError, PermissionError):
            continue

        updated = apply_substitutions(original)
        if updated != original:
            changed_files.append((fpath, updated))

    if not changed_files:
        print(f"  NOOP  {repo_name}")
        return False

    print(f"  CHANGES {repo_name}: {len(changed_files)} files")
    for fpath, _ in changed_files:
        print(f"    {os.path.relpath(fpath, repo_dir)}")

    if dry_run:
        return False

    # Apply changes
    for fpath, content in changed_files:
        with open(fpath, "w", encoding="utf-8") as f:
            f.write(content)

    # Git operations
    try:
        # Check if branch already exists
        branches = run(["git", "branch", "--list", BRANCH], cwd=repo_dir)
        if BRANCH in branches:
            run(["git", "checkout", BRANCH], cwd=repo_dir)
        else:
            # Get default branch
            default = run(["git", "remote", "show", "origin"], cwd=repo_dir)
            default_branch = "main"
            for line in default.split("\n"):
                if "HEAD branch:" in line:
                    default_branch = line.split(":")[-1].strip()
                    break
            run(["git", "fetch", "origin", default_branch], cwd=repo_dir)
            run(["git", "checkout", "-b", BRANCH, f"origin/{default_branch}"], cwd=repo_dir)

        # Stage and commit
        for fpath, _ in changed_files:
            run(["git", "add", fpath], cwd=repo_dir)
        run(["git", "commit", "-m", COMMIT_MSG], cwd=repo_dir)
        run(["git", "push", "-u", "origin", BRANCH, "--force-with-lease"], cwd=repo_dir)

        if not skip_pr:
            # Determine if repo is in jdfalk or falkcorp now
            remote_url = run(["git", "remote", "get-url", "origin"], cwd=repo_dir)
            if "falkcorp" in remote_url:
                repo_ref = f"falkcorp/{repo_name}"
            else:
                repo_ref = f"jdfalk/{repo_name}"

            pr_body = f"""## Migrate org references: jdfalk → falkcorp

Automated PR to update all workflow `uses:` references, Go module paths, and GHCR image tags
from `jdfalk/` to `falkcorp/` as part of the organization migration.

### Changed files:
{chr(10).join(f"- `{os.path.relpath(f, repo_dir)}`" for f, _ in changed_files)}

🤖 Generated by `scripts/org-migration/update-workflow-refs.py`"""

            run(
                [
                    "gh",
                    "pr",
                    "create",
                    "-R",
                    repo_ref,
                    "--title",
                    "chore: migrate jdfalk/ refs to falkcorp/ org",
                    "--body",
                    pr_body,
                    "--head",
                    BRANCH,
                    "--base",
                    "main",
                ],
                cwd=repo_dir,
            )
            print(f"  PR    {repo_name}")

    except RuntimeError as e:
        print(f"  ERROR {repo_name}: {e}")
        return False

    return True


if dry_run:
    print("=== DRY RUN (pass no args to execute) ===\n")

# Get all jdfalk repos
repos = sorted(
    [d for d in os.listdir(BASE) if os.path.isdir(os.path.join(BASE, d)) and not d.startswith(".")]
)

changed, noop, missed = 0, 0, 0
for repo in repos:
    result = process_repo(repo, os.path.join(BASE, repo))
    if result:
        changed += 1
    elif os.path.isdir(os.path.join(BASE, repo)):
        noop += 1
    else:
        missed += 1
    time.sleep(0.2)

print(f"\nDone: {changed} repos with PRs, {noop} no changes, {missed} not found")
