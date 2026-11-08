#!/usr/bin/env python3
# file: .github/workflows/scripts/core_checkpoint.py
# version: 1.0.0
# guid: 2ad9b4fd-b316-4ef1-8d10-accb0edd4d42
# last-edited: 2026-11-08
"""Record a core checkpoint for a production release (12 §12.4.1; release-prod.yml job core-checkpoint).

Every ABO production release that changed core/ records a checkpoint,
refs/core-checkpoints/v0.Y.Z, on the release commit. This script is that job's
whole decision:

  * core/ is unchanged since the newest checkpoint: nothing to do;
  * core/ changed and version.Version is above the newest checkpoint (or there
    is no checkpoint yet): ``git update-ref refs/core-checkpoints/v<Version>
    <commit>`` and push that one ref with an explicit refspec;
  * core/ changed and the constant was not bumped: record nothing, and open or
    update the one issue labelled core-version-bump-needed.

The ref name is derived from the constant, so a checkpoint can never name a
version the commit does not carry; a constant that is not 0.Y.Z is refused (a
post-split version is tagged by ``make release``, not recorded here).

The job is non-blocking (continue-on-error, nothing needs it): an exit 1 here
costs readiness time, never a release. The decision is also printed as the
``action`` step output (recorded, bump-needed or none).

Stdlib only; imports the shared helpers of core/scripts/ci/core_ci_lib.py.
"""

from __future__ import annotations

import argparse
import dataclasses
import pathlib
import subprocess
import sys
from typing import Callable

REPO_ROOT = pathlib.Path(__file__).resolve().parents[3]
sys.path.insert(0, str(REPO_ROOT / "core" / "scripts" / "ci"))
sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent))
import core_ci_lib as lib  # noqa: E402
import workflow_common  # noqa: E402

ISSUE_LABEL = "core-version-bump-needed"
ISSUE_TITLE = "core/ changed since the newest checkpoint but version.Version was not bumped"
ISSUE_LABEL_COLOR = "d93f0b"
ISSUE_LABEL_DESCRIPTION = "core/ changed since the newest checkpoint and version.Version is not above it (12 §12.4.1)"

Gh = Callable[[list[str]], str]


class Refusal(Exception):
    """The job must not record or decide (exit 1)."""


@dataclasses.dataclass
class Decision:
    action: str  # "recorded" | "bump-needed" | "none"
    version: str
    commit: str
    ref: str = ""
    newest: str = ""
    message: str = ""


def decide(repo: pathlib.Path, commit: str) -> Decision:
    """What the job does for commit; reads the repository, changes nothing."""
    sha = lib.git(repo, "rev-parse", "--verify", f"{commit}^{{commit}}", check=False)
    if sha.returncode != 0:
        raise Refusal(f"{commit} is not a commit of this repository")
    full = sha.stdout.strip()
    shown = lib.git(repo, "show", f"{full}:core/version/version.go", check=False)
    if shown.returncode != 0:
        raise Refusal(f"{full[:12]} has no core/version/version.go")
    try:
        text = lib.version_from_source(shown.stdout, "core/version/version.go")
        version = lib.parse_semver(text)
    except ValueError as e:
        raise Refusal(str(e)) from e
    try:
        checkpoints = lib.list_checkpoints(repo)
    except ValueError as e:
        raise Refusal(str(e)) from e

    if checkpoints:
        newest_ref, newest = checkpoints[0]
        changed = lib.git(repo, "diff", "--quiet", f"{newest_ref}..{full}", "--", "core/", check=False).returncode
        if changed not in (0, 1):
            raise Refusal(f"git diff {newest_ref}..{full} -- core/ failed")
        if changed == 0:
            return Decision("none", str(version), full, newest=str(newest), message=f"core/ is unchanged since checkpoint {newest}")
        if version <= newest:
            return Decision(
                "bump-needed",
                str(version),
                full,
                newest=str(newest),
                message=f"core/ changed since checkpoint {newest} but version.Version is {version}",
            )
    else:
        newest = None

    # The newest-first list has no entry at or above the constant here, so the ref is new.
    if version.major != 0 or version.pre:
        raise Refusal(
            f"version.Version {version} is not 0.Y.Z: a checkpoint is refs/core-checkpoints/v0.Y.Z "
            "(12 §12.4.1); versions from v1 are tagged by make release, not recorded"
        )
    return Decision(
        "recorded",
        str(version),
        full,
        ref=f"{lib.CHECKPOINT_PREFIX}v{version}",
        newest=str(newest) if newest else "",
        message=f"core/ changed (or has no checkpoint yet) and version.Version {version} is above "
        + (f"checkpoint {newest}" if newest else "every checkpoint (none recorded)"),
    )


def issue_body(d: Decision) -> str:
    return (
        f"{d.message}.\n\n"
        f"- release commit: `{d.commit}`\n"
        f"- newest checkpoint: `v{d.newest}`\n"
        f"- `version.Version`: `{d.version}`\n\n"
        "No checkpoint was recorded for this release, so the readiness clock of SC1 "
        "(three API-stable checkpoints over six weeks) did not advance. Raise "
        "`core/version/version.go` in one \"core version\" PR before the next production "
        "release: a minor bump for an incompatible or additive change, a patch otherwise "
        "(pick it from the apidiff result against the newest checkpoint, 12 §12.4.1). "
        "The next production release records the checkpoint.\n\n"
        "This issue is updated by the `core-checkpoint` job of `release-prod.yml`; "
        "it never fails the release."
    )


def record(repo: pathlib.Path, d: Decision, remote: str) -> None:
    if lib.git(repo, "show-ref", "--verify", "--quiet", d.ref, check=False).returncode == 0:
        raise Refusal(f"{d.ref} already exists")
    lib.git(repo, "update-ref", d.ref, d.commit)
    pushed = lib.git(repo, "push", remote, f"{d.ref}:{d.ref}", check=False)
    if pushed.returncode != 0:
        lib.git(repo, "update-ref", "-d", d.ref, check=False)
        raise Refusal(f"git push {remote} {d.ref}: {pushed.stderr.strip()}")


def sync_issue(gh: Gh, d: Decision) -> str:
    """Open the one core-version-bump-needed issue, or update it; returns its number."""
    gh(["label", "create", ISSUE_LABEL, "--force", "--color", ISSUE_LABEL_COLOR, "--description", ISSUE_LABEL_DESCRIPTION])
    existing = gh(
        ["issue", "list", "--label", ISSUE_LABEL, "--state", "open", "--json", "number", "--jq", ".[0].number // empty"]
    ).strip()
    body = issue_body(d)
    if existing:
        gh(["issue", "edit", existing, "--title", ISSUE_TITLE, "--body", body])
        return existing
    url = gh(["issue", "create", "--title", ISSUE_TITLE, "--label", ISSUE_LABEL, "--body", body]).strip()
    return url.rsplit("/", 1)[-1]


def run_gh(args: list[str]) -> str:
    res = subprocess.run(["gh", *args], capture_output=True, text=True, check=False)
    if res.returncode != 0:
        raise Refusal(f"gh {' '.join(args[:2])}: {res.stderr.strip()}")
    return res.stdout


def main(argv: list[str] | None = None, gh: Gh = run_gh) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--repo", type=pathlib.Path, default=REPO_ROOT, help="git work tree (default: this repo)")
    parser.add_argument("--commit", default="HEAD", help="the release commit (default HEAD)")
    parser.add_argument("--remote", default="origin")
    parser.add_argument("--no-fetch", action="store_true", help="do not fetch refs/core-checkpoints/* first")
    parser.add_argument("--dry-run", action="store_true", help="decide and print; record nothing, open no issue")
    parser.add_argument("--open-issue", action="store_true", help="on bump-needed, open or update the issue with gh")
    args = parser.parse_args(argv)
    repo = args.repo.resolve()
    try:
        if not args.no_fetch:
            try:
                lib.fetch_checkpoints(repo, args.remote)
            except RuntimeError as e:
                raise Refusal(f"cannot decide without the recorded checkpoints: {e}") from e
        d = decide(repo, args.commit)
        print(f"core-checkpoint: {d.action}: {d.message}")
        if d.action == "recorded" and not args.dry_run:
            record(repo, d, args.remote)
            print(f"core-checkpoint: recorded {d.ref} at {d.commit}")
        elif d.action == "recorded":
            print(f"core-checkpoint: dry run, would record {d.ref} at {d.commit}")
        elif d.action == "bump-needed":
            print(f"core-checkpoint: records nothing. Issue [{ISSUE_LABEL}] text:\n{ISSUE_TITLE}\n\n{issue_body(d)}")
            if args.open_issue and not args.dry_run:
                print(f"core-checkpoint: issue #{sync_issue(gh, d)} is open")
        workflow_common.write_output("action", d.action)
        workflow_common.write_output("version", d.version)
        return 0
    except Refusal as e:
        print(f"::error::core-checkpoint: {e}")
        return 1


if __name__ == "__main__":
    sys.exit(main())
