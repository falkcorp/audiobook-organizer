#!/usr/bin/env python3
# file: .github/workflows/scripts/release_workflow.py
# version: 2.1.1
# guid: 9f7e6d5c-4b3a-2f1e-0d9c-8b7a6f5e4d3c

"""
Changelog generator for prerelease workflow with proper git history integration.

The upstream reusable workflow expects a release_workflow.py script exposed via
GHCOMMON_SCRIPTS_DIR. This implementation generates real changelogs from git
history instead of synthetic placeholders.
"""

from __future__ import annotations

import os
import subprocess
import sys


def write_output(key: str, value: str) -> None:
    """Append a multiline output value for GitHub Actions."""
    output_path = os.environ.get("GITHUB_OUTPUT")
    if not output_path:
        return
    with open(output_path, "a", encoding="utf-8") as handle:
        handle.write(f"{key}<<'EOF'\n{value}\nEOF\n")


def get_git_commits_since_last_tag() -> list[str]:
    """Get commits since the last release tag."""
    try:
        # Get the latest tag
        result = subprocess.run(
            ["git", "describe", "--tags", "--abbrev=0"],
            capture_output=True,
            text=True,
            check=False,
        )

        if result.returncode == 0:
            last_tag = result.stdout.strip()
            # Get commits since last tag
            result = subprocess.run(
                ["git", "log", f"{last_tag}..HEAD", "--oneline", "--no-decorate"],
                capture_output=True,
                text=True,
                check=True,
            )
            commits = result.stdout.strip().split("\n")
            return [f"- {commit}" for commit in commits if commit]
        else:
            # No tags found, get all commits
            result = subprocess.run(
                ["git", "log", "--oneline", "--no-decorate", "-20"],
                capture_output=True,
                text=True,
                check=True,
            )
            commits = result.stdout.strip().split("\n")
            return [f"- {commit}" for commit in commits if commit]
    except subprocess.CalledProcessError as e:
        print(f"Error getting git commits: {e}", file=sys.stderr)
        return ["- Automated prerelease build"]


def get_current_commit() -> str:
    """Get the current commit hash."""
    try:
        result = subprocess.run(
            ["git", "rev-parse", "HEAD"],
            capture_output=True,
            text=True,
            check=True,
        )
        return result.stdout.strip()
    except subprocess.CalledProcessError:
        return "unknown"


def generate_changelog() -> None:
    """Generate changelog from git history for prerelease builds."""
    commits = get_git_commits_since_last_tag()
    current_commit = get_current_commit()

    # Build changelog content
    content_parts = [
        "## Changelog",
        "",
        "### Changes in this release",
        "",
    ]

    if commits:
        content_parts.extend(commits)
    else:
        content_parts.append("- Initial release")

    content_parts.extend(
        [
            "",
            f"**Commit**: {current_commit[:8]}",
            "",
            "This is an automated prerelease build from the main branch.",
        ]
    )

    content = "\n".join(content_parts)
    write_output("changelog_content", content)


def main(argv: list[str]) -> int:
    if len(argv) < 2:
        print("usage: release_workflow.py <command>", file=sys.stderr)
        return 1

    command = argv[1]
    if command == "generate-changelog":
        try:
            generate_changelog()
        except Exception as e:  # noqa: BLE001
            # Write a minimal changelog so the workflow step does not fail
            # silently — the release can still proceed with a stub.
            print(f"Warning: changelog generation failed: {e}", file=sys.stderr)
            write_output(
                "changelog_content",
                f"## Changelog\n\nError generating changelog: {e}\n",
            )
        return 0

    print(f"unknown command: {command}", file=sys.stderr)
    return 1


if __name__ == "__main__":
    sys.exit(main(sys.argv))
