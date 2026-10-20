#!/usr/bin/env python3
# file: scripts/org-migration/bump-release-sha.py
# version: 1.0.3
# guid: 2465b808-74b7-45c8-9200-80854db55a3f
# last-edited: 2026-10-20

"""Bump reusable-release.yml SHA pin in all repos that still reference the old pre-migration SHA."""

import base64
import json
import subprocess
import time

OLD_SHA = "483f3afeb0064ccbe6ad565044042ba6faee1f96"
NEW_SHA = "bfb912c850effd1ae9aded26d14856a538d3e5fc"
NEW_TAG = "v1.10.7"  # includes actionlint/shellcheck CI fixes

REPOS = [
    "gha-get-frontend-config",
    "gha-load-config",
    "gha-ci-generate-matrices",
    "gha-package-assets",
    "gha-detect-languages",
    "gha-ci-workflow-helpers",
    "gha-release-protobuf",
    "gha-release-frontend",
    "gha-release-docker",
    "gha-release-rust",
    "gha-release-python",
    "gha-release-go",
    "gha-pr-auto-label",
    "gha-docs-generator",
]

WORKFLOW_PATH = ".github/workflows/release.yml"
BRANCH = "fix/bump-reusable-release-sha"


def gh(*args, check=True):
    cmd = ["gh"] + list(args)
    result = subprocess.run(cmd, capture_output=True, text=True)
    if check and result.returncode != 0:
        raise RuntimeError(f"gh {' '.join(args)} failed: {result.stderr}")
    return result.stdout.strip()


def gh_json(*args):
    return json.loads(gh(*args))


def update_repo(repo):
    full = f"falkcorp/{repo}"
    print(f"\n{'=' * 50}")
    print(f"  {full}")

    # Fetch current file
    file_info = gh_json("api", f"/repos/{full}/contents/{WORKFLOW_PATH}")
    content = base64.b64decode(file_info["content"]).decode()

    if OLD_SHA not in content:
        print("  SKIP — old SHA not found")
        return

    new_content = content.replace(f"@{OLD_SHA}", f"@{NEW_SHA} # {NEW_TAG}")

    # Check if branch already exists
    branches = gh("api", f"/repos/{full}/branches", check=False)
    branch_exists = BRANCH in branches

    # Get default branch HEAD sha for branch creation
    default_branch = gh("api", f"/repos/{full}", "--jq", ".default_branch").strip('"')
    default_sha = gh(
        "api", f"/repos/{full}/git/ref/heads/{default_branch}", "--jq", ".object.sha"
    ).strip('"')

    # Create branch if needed
    if not branch_exists:
        gh(
            "api",
            "-X",
            "POST",
            f"/repos/{full}/git/refs",
            "-f",
            f"ref=refs/heads/{BRANCH}",
            "-f",
            f"sha={default_sha}",
        )
        print(f"  Created branch {BRANCH}")
    else:
        print(f"  Branch {BRANCH} already exists")

    # Update the file on the branch
    encoded = base64.b64encode(new_content.encode()).decode()

    # Get file SHA on the branch
    file_on_branch = gh_json("api", f"/repos/{full}/contents/{WORKFLOW_PATH}?ref={BRANCH}")
    file_sha = file_on_branch["sha"]

    gh(
        "api",
        "-X",
        "PUT",
        f"/repos/{full}/contents/{WORKFLOW_PATH}",
        "-f",
        f"message=fix: bump reusable-release.yml SHA to {NEW_TAG} ({NEW_SHA[:8]})\n\nUpdates the falkcorp/github-common reusable-release.yml pin from the\npre-migration SHA to the current main which has falkcorp/ org refs.",
        "-f",
        f"content={encoded}",
        "-f",
        f"sha={file_sha}",
        "-f",
        f"branch={BRANCH}",
    )
    print(f"  Updated {WORKFLOW_PATH}")

    # Create or find PR
    existing_pr = gh(
        "pr",
        "list",
        "-R",
        full,
        "--head",
        BRANCH,
        "--json",
        "number",
        "-q",
        ".[0].number",
        check=False,
    ).strip()

    if existing_pr and existing_pr != "null":
        print(f"  PR already exists: #{existing_pr}")
    else:
        pr_url = gh(
            "pr",
            "create",
            "-R",
            full,
            "--head",
            BRANCH,
            "--base",
            "main",
            "--title",
            f"fix: bump reusable-release.yml pin to {NEW_TAG}",
            "--body",
            f"The release workflow was pinned to `{OLD_SHA[:8]}` (pre-migration), "
            f"which references `jdfalk/gha-release-docker` and other old-org actions. "
            f"Bumping to `{NEW_SHA[:8]}` ({NEW_TAG}) which has all `falkcorp/` refs.",
        )
        print(f"  PR: {pr_url}")
        return pr_url


def main():
    prs = []
    for repo in REPOS:
        try:
            pr = update_repo(repo)
            if pr:
                prs.append((repo, pr))
        except Exception as e:
            print(f"  ERROR: {e}")
        time.sleep(0.3)  # avoid rate limits

    print(f"\n\nDone. {len(prs)} PRs created.")
    print("\nMerging all with --admin --rebase...")
    for repo, _ in prs:
        full = f"falkcorp/{repo}"
        pr_num = gh(
            "pr", "list", "-R", full, "--head", BRANCH, "--json", "number", "-q", ".[0].number"
        )
        if pr_num and pr_num != "null":
            result = subprocess.run(
                ["gh", "pr", "merge", pr_num, "-R", full, "--rebase", "--delete-branch", "--admin"],
                capture_output=True,
                text=True,
            )
            if result.returncode == 0:
                print(f"  MERGED  {full} #{pr_num}")
            else:
                print(f"  FAILED  {full} #{pr_num}: {result.stderr[:100]}")
            time.sleep(0.5)


if __name__ == "__main__":
    main()
