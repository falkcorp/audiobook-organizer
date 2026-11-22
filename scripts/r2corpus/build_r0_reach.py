#!/usr/bin/env python3
# file: scripts/r2corpus/build_r0_reach.py
# version: 0.1.0
# guid: 7c2e9a14-5b38-4f06-a9d1-3e8b6f0c2d75
# last-edited: 2026-11-22
"""Build the binary R0's reach is recorded from: main at the pinned commit (F4-08).

R0 used to mean the v0.223.0 release asset. Since 2026-11-22 the reach baseline
(``internal/books/rbacreach``) is main at the commit named in
``r0config/r0_reach_commit.txt``, built with the release asset's recipe, so the
baseline holds whatever main had merged by then and a flip difference is measured
against main, not against a 16-day-old release. The corpus writer, its manifest
and ``TestR0OpensR1Store`` keep the release (``ci/r0_release.txt``): only the
reach recording moves.

The recipe is the release asset's, read off v0.223.0's build info (``go version
-m``): ``CGO_ENABLED=0``, ``GOOS=linux GOARCH=amd64``, ``-tags embed_frontend``
over a ``web/dist`` built by ``scripts/ci/build_web.sh deps`` and ``npm run
build``, and ``-ldflags "-s -w -X main.version=... -X main.commit=... -X
main.date=..."``. Two changes, both so a rebuild can be compared: ``-trimpath``
(the checkout path is not in the binary) and ``main.date`` is the commit's time,
not the clock's. Neither reaches a handler.

The commit is the identity, not the digest. The binary's own build info must name
the pinned commit (``vcs.revision``, which the go command stamps from the checkout
itself), a clean tree (``vcs.modified=false``) and the recipe's settings
(``check_build_info``), so a binary built from anything else, or by another
recipe, is refused before it is booted. The ``-X`` values are not in the build
info: the go command leaves ``-ldflags`` out of it under ``-trimpath``. The digest is
recorded as provenance (``recorded.r0_binary_sha256``): a vite bundle and a Go
link are not promised to be bit-identical across runner images, so a digest
compared by ``--check`` would fail a correct rebuild.

    python3 scripts/r2corpus/build_r0_reach.py --out DIR   # needs go, node, npm, git
"""

from __future__ import annotations

import argparse
import datetime
import hashlib
import os
import pathlib
import re
import shutil
import subprocess
import sys
import tempfile

_HERE = pathlib.Path(__file__).resolve().parent
REPO_ROOT = _HERE.parents[1]
COMMIT_FILE = _HERE / "r0config" / "r0_reach_commit.txt"
BINARY_NAME = "audiobook-organizer_Linux_x86_64"
GOOS, GOARCH = "linux", "amd64"
TAGS = "embed_frontend"
# The build settings check_build_info requires, as `go version -m` prints them.
REQUIRED_SETTINGS = {
    "CGO_ENABLED": "0",
    "GOOS": GOOS,
    "GOARCH": GOARCH,
    "-tags": TAGS,
    "-trimpath": "true",
    "vcs": "git",
    "vcs.modified": "false",
}
# The recipe as one line, recorded in verdicts.json and compared by --check.
RECIPE = (
    f"CGO_ENABLED=0 GOOS={GOOS} GOARCH={GOARCH} go build -trimpath -buildvcs=true "
    f"-tags {TAGS} -ldflags '-s -w -X main.version=0.0.0-r0reach.<commit12> "
    "-X main.commit=<commit> -X main.date=<commit time>' . "
    "(web/dist: sh scripts/ci/build_web.sh deps && npm run build, in web/)"
)


class BuildError(Exception):
    """The pinned commit cannot be built, or a binary is not its build."""


def read_commit(path: pathlib.Path | None = None) -> str:
    """The one line of ``r0_reach_commit.txt`` that is neither blank nor a comment."""
    path = path or COMMIT_FILE
    lines = [
        ln.strip()
        for ln in path.read_text(encoding="utf-8").splitlines()
        if ln.strip() and not ln.lstrip().startswith("#")
    ]
    if len(lines) != 1:
        raise BuildError(f"{path}: want exactly one commit line, found {len(lines)}")
    if not re.fullmatch(r"[0-9a-f]{40}", lines[0]):
        raise BuildError(f"{path}: {lines[0]!r} is not a full commit SHA")
    return lines[0]


def sha256_file(path: pathlib.Path) -> str:
    h = hashlib.sha256()
    with path.open("rb") as fh:
        for chunk in iter(lambda: fh.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()


def build_info(binary: pathlib.Path) -> dict[str, str]:
    """``go version -m``'s build settings and the main module's ``-X`` values."""
    out = subprocess.run(
        ["go", "version", "-m", str(binary)], check=True, capture_output=True, text=True
    ).stdout
    info: dict[str, str] = {}
    for line in out.splitlines():
        parts = line.strip().split("\t")
        if len(parts) >= 2 and parts[0] == "build":
            key, _, value = parts[1].partition("=")
            info[key] = value.strip('"')
    for name, value in re.findall(r"-X main\.(\w+)=(\S+)", info.get("-ldflags", "")):
        info[f"main.{name}"] = value
    return info


def check_build_info(binary: pathlib.Path, commit: str) -> dict[str, str]:
    """Refuse a binary that is not the pinned commit built by the recipe."""
    info = build_info(binary)
    problems = [
        f"{k}={info.get(k)!r}, want {v!r}"
        for k, v in sorted(REQUIRED_SETTINGS.items())
        if info.get(k) != v
    ]
    if info.get("vcs.revision") != commit:
        problems.append(f"vcs.revision={info.get('vcs.revision')!r}, want {commit}")
    if problems:
        raise BuildError(f"{binary.name} is not the pinned R0 build: " + "; ".join(problems))
    return info


def _run(argv: list[str], cwd: pathlib.Path, env: dict[str, str] | None = None) -> str:
    r = subprocess.run(argv, cwd=cwd, env=env, check=False, capture_output=True, text=True)
    if r.returncode != 0:
        raise BuildError(
            f"{' '.join(argv[:3])} failed ({r.returncode}): {r.stderr.strip()[-1200:]}"
        )
    return r.stdout


def build(commit: str, out_dir: pathlib.Path) -> pathlib.Path:
    """Build ``commit`` in a detached worktree by the recipe; the binary lands in
    ``out_dir`` and is checked against its own build info before it is returned."""
    out_dir.mkdir(parents=True, exist_ok=True)
    dest = out_dir / BINARY_NAME
    tmp = pathlib.Path(tempfile.mkdtemp(prefix="r0reach-build-"))
    src = tmp / "src"
    try:
        _run(["git", "worktree", "add", "--detach", str(src), commit], REPO_ROOT)
        when = _run(["git", "log", "-1", "--format=%cI", commit], src).strip()
        date = (
            datetime.datetime.fromisoformat(when)
            .astimezone(datetime.timezone.utc)
            .strftime("%Y-%m-%dT%H:%M:%SZ")
        )
        _run(["sh", "scripts/ci/build_web.sh", "deps"], src)
        web_env = {k: v for k, v in os.environ.items() if k != "BUILD_ID"}
        _run(["npm", "run", "build"], src / "web", web_env)
        ldflags = (
            f"-s -w -X main.version=0.0.0-r0reach.{commit[:12]} "
            f"-X main.commit={commit} -X main.date={date}"
        )
        env = {**os.environ, "CGO_ENABLED": "0", "GOOS": GOOS, "GOARCH": GOARCH}
        env.pop("GOFLAGS", None)
        _run(
            ["go", "build", "-trimpath", "-buildvcs=true", "-tags", TAGS,
             "-ldflags", ldflags, "-o", str(dest), "."],
            src, env,
        )  # fmt: skip
    finally:
        subprocess.run(
            ["git", "worktree", "remove", "--force", str(src)], cwd=REPO_ROOT, check=False
        )
        shutil.rmtree(tmp, ignore_errors=True)
    dest.chmod(0o755)
    check_build_info(dest, commit)
    return dest


def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    ap.add_argument("--out", required=True, help="directory the binary is written to")
    ap.add_argument("--commit", help="build this commit (default: r0_reach_commit.txt)")
    args = ap.parse_args(argv)
    try:
        commit = args.commit or read_commit()
        binary = build(commit, pathlib.Path(args.out))
    except BuildError as e:
        print(f"ERROR: {e}", file=sys.stderr)
        return 1
    print(f"{sha256_file(binary)}  {binary}  (commit {commit})")
    return 0


if __name__ == "__main__":
    sys.exit(main())
