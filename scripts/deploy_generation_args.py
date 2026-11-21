#!/usr/bin/env python3
# file: scripts/deploy_generation_args.py
# version: 0.1.0
# guid: 1b45a9c9-bfe8-483e-bc20-32543afb8815
# last-edited: 2026-11-20

"""What a deploy tells `install-staged binary` about the build it is installing.

`make deploy` runs this after uploading the binary and before it changes
anything on the host, and passes the four words it prints to the helper:

    SHA256 VERSION|- LEVELS|-|. ENFORCED(0|1|-)

The helper keeps the environment it replaces as a generation named by the
sha256 of the binary that ran under it, and uses these words to describe that
generation (`make rollback-check`) and to decide which generations a hotfix
train must not evict: the one for the last build before a store level step
or before the retired-name list was enforced. The helper never runs a binary
as root, so the facts are measured here, as the deploy user, on the staged file:

  sha256    of the local file that was uploaded (the helper records the facts
            only when this equals the bytes it installs)
  version   `STAGE --version-json` over ssh
  levels    `STAGE --print-storage-levels` over ssh (a build that predates it
            and reads storage format 1 is every owner at 0)
  enforced  `# enforced:` in deploy/retired-env-names.txt of this checkout,
            which the binary was built from

A fact that cannot be measured is `-` (unknown). The helper pins a generation
whose facts are unknown, so a failure here keeps more, never less. Only an
unreadable local file fails: no hash, no deploy.

The words are validated with the helper's own parser before they are printed.
Standard library only. Tests: scripts/tests/test_deploy_generation_args.py.
"""

from __future__ import annotations

import argparse
import hashlib
import importlib.util
import json
import shlex
import subprocess
import sys
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent
LIST_PATH = REPO / "deploy" / "retired-env-names.txt"
HELPER = REPO / "deploy" / "rollback-helpers" / "rollback_helper.py"


def load_helper():
    spec = importlib.util.spec_from_file_location("rollback_helper", HELPER)
    if spec is None or spec.loader is None:
        raise SystemExit(f"deploy_generation_args: cannot load {HELPER}")
    mod = importlib.util.module_from_spec(spec)
    sys.modules["rollback_helper"] = mod
    spec.loader.exec_module(mod)
    return mod


def sha256_of(path: Path) -> str:
    h = hashlib.sha256()
    with path.open("rb") as f:
        for chunk in iter(lambda: f.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()


def ask(host: str, command: str, timeout: float) -> tuple[int, str]:
    """(exit status, stdout) of `command` on host, as the ssh user; (255, "")
    when ssh cannot be run or times out."""
    try:
        r = subprocess.run(  # noqa: S603 (argv, no shell on this side)
            ["ssh", "-o", "BatchMode=yes", host, command],
            capture_output=True,
            text=True,
            timeout=timeout,
            check=False,
        )
    except (OSError, subprocess.TimeoutExpired):
        return 255, ""
    return r.returncode, r.stdout


def measure_version(host: str, stage: str, timeout: float) -> str:
    rc, text = ask(host, f"{shlex.quote(stage)} --version-json", timeout)
    if rc != 0:
        return "-"
    try:
        v = json.loads(text).get("version")
    except (ValueError, AttributeError):
        return "-"
    return v if isinstance(v, str) and v else "-"


def format_levels(doc: object) -> str:
    """`owner=n,owner=n`, `.` for none, `-` for anything that is not an object
    of small non-negative integers."""
    if not isinstance(doc, dict):
        return "-"
    parts = []
    for owner, level in sorted(doc.items()):
        if not isinstance(owner, str) or isinstance(level, bool) or not isinstance(level, int):
            return "-"
        parts.append(f"{owner}={level}")
    return ",".join(parts) or "."


def measure_levels(host: str, stage: str, timeout: float) -> str:
    q = shlex.quote(stage)
    rc, text = ask(host, f"{q} --print-storage-levels", timeout)
    if rc == 0:
        try:
            return format_levels(json.loads(text))
        except ValueError:
            return "-"
    # An R0 build: no --print-storage-levels, and storage format 1 means every
    # owner at level 0 (the same fallback the rollback recipes use).
    rc, text = ask(host, f"{q} --print-storage-format", timeout)
    return "." if rc == 0 and text.strip() == "1" else "-"


def measure_enforced(list_path: Path) -> str:
    try:
        text = list_path.read_text(encoding="utf-8")
    except (OSError, UnicodeDecodeError):
        return "-"
    for line in text.splitlines():
        if line.startswith("# enforced:"):
            value = line.split(":", 1)[1].strip()
            return {"true": "1", "false": "0"}.get(value, "-")
    return "-"


def assemble(host: str, stage: str, sha: str, enforced: str, timeout: float) -> list[str]:
    """The four words for the binary staged at `stage` on host, whose sha256
    the caller already has (a local upload hashes its file; a release fetched
    on the host hashes it there) and whose `enforced` fact the caller knows."""
    out = [
        sha,
        measure_version(host, stage, timeout),
        measure_levels(host, stage, timeout),
        enforced,
    ]
    # The helper's parser is the grammar. A word it would refuse (a version
    # tag with a character it does not allow) is replaced by unknown rather
    # than failing the deploy for a display fact.
    helper = load_helper()
    for i in (1, 2):
        probe = [out[0], "-", "-", "-"]
        probe[i] = out[i]
        try:
            helper._parse_meta(probe)
        except helper.RefusedError:
            out[i] = "-"
    helper._parse_meta(out)  # raises on a shape bug here; never ship a bad line
    return out


def words(args: argparse.Namespace) -> list[str]:
    return assemble(
        args.host,
        args.stage,
        sha256_of(Path(args.file)),
        measure_enforced(Path(args.list)),
        args.timeout,
    )


def main(argv: list[str] | None = None) -> int:
    p = argparse.ArgumentParser(description=(__doc__ or "").splitlines()[0])
    p.add_argument("--host", required=True)
    p.add_argument("--stage", required=True, help="the staged binary's path on the host")
    p.add_argument("--file", required=True, help="the local file that was uploaded as it")
    p.add_argument("--list", default=str(LIST_PATH))
    p.add_argument("--timeout", type=float, default=30.0)
    a = p.parse_args(argv)
    try:
        out = words(a)
    except OSError as e:
        print(f"deploy_generation_args: cannot read {a.file} ({e.strerror})", file=sys.stderr)
        return 1
    print(" ".join(out))
    return 0


if __name__ == "__main__":
    sys.exit(main())
