#!/usr/bin/env python3
# file: scripts/rebase-branches.py
# version: 1.0.3
# guid: 6155668c-d973-4826-aef8-773a851a4896
# last-edited: 2026-10-20

"""
Rebase all local branches onto origin/main.
Aborts on conflicts and marks branches for manual review.
"""

import contextlib
import subprocess
import sys


def run(cmd, capture=False):
    """Run a shell command."""
    try:
        if capture:
            result = subprocess.run(cmd, shell=True, capture_output=True, text=True, check=True)
            return result.stdout.strip()
        else:
            subprocess.run(cmd, shell=True, check=True)
            return None
    except subprocess.CalledProcessError:
        if capture:
            return None
        raise


def get_all_branches():
    """Get all branches not named 'main'."""
    output = run("git branch --format='%(refname:short)'", capture=True)
    branches = [b.strip() for b in output.split("\n") if b.strip() and b.strip() != "main"]
    return branches


def has_conflicts():
    """Check if current rebase has conflicts."""
    output = run("git status --short", capture=True)
    # Conflicted files show as 'UU', 'DD', 'AA', 'DU', 'UD', etc.
    return any(line[0:2] in ["UU", "DD", "AA", "DU", "UD"] for line in output.split("\n"))


def main():
    # Get current branch to restore later
    # run(capture=True) returns None (it does not raise) when the command fails.
    current_branch = run("git rev-parse --abbrev-ref HEAD", capture=True)
    if current_branch is None:
        print("Error: not in a git repository")
        sys.exit(1)

    # Fetch latest
    print("Fetching origin...")
    run("git fetch origin")

    # Stash any uncommitted changes in main checkout
    print("Stashing uncommitted changes...")
    run("git stash push -u -m 'rebase-branches-temp-stash' || true")

    branches = get_all_branches()
    print(f"\nRebasing {len(branches)} branches onto origin/main...\n")

    failed_branches = []
    succeeded = []

    for branch in branches:
        print(f"  {branch}...", end=" ", flush=True)

        # Switch to branch (with --force to override any working tree changes)
        try:
            run(f"git checkout {branch}")
        except subprocess.CalledProcessError:
            print("✗ (checkout failed)")
            failed_branches.append(branch)
            continue

        # Try rebase
        try:
            run("git rebase origin/main")
            print("✓")
            succeeded.append(branch)
        except subprocess.CalledProcessError:
            # Check for conflicts
            if has_conflicts():
                print("✗ (conflicts)")
                run("git rebase --abort")
                failed_branches.append(branch)
            else:
                # Other rebase error
                print("✗ (error)")
                run("git rebase --abort")
                failed_branches.append(branch)

    # Restore original branch; the stash pop must still run if this is interrupted
    try:
        with contextlib.suppress(subprocess.CalledProcessError):
            run(f"git checkout {current_branch}")
    finally:
        # Restore stashed changes
        print("\nRestoring stashed changes...")
        run("git stash pop || true")

    # Report
    print(f"\n{'=' * 60}")
    print(f"Succeeded: {len(succeeded)}")
    print(f"Failed (conflicts/errors): {len(failed_branches)}")

    if failed_branches:
        print("\nBranches to review manually:")
        for branch in failed_branches:
            print(f"  • {branch}")

    return 1 if failed_branches else 0


if __name__ == "__main__":
    sys.exit(main())
