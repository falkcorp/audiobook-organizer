#!/usr/bin/env python3
# file: scripts/deadcode-ratchet.py
# version: 1.1.3
# guid: 5d0c7f2e-8a41-4b6d-9e13-c7a2f4b86d90
# last-edited: 2026-10-19
"""Ratchet on production functions unreachable from every `main`.

Runs golang.org/x/tools/cmd/deadcode (pinned) and compares the set of
unreachable function identifiers against scripts/deadcode-baseline.txt.

One-way, like scripts/check-errcheck-ratchet.sh (owner decision D47): a NEW
identifier fails; an identifier that disappeared prints a ::notice:: and exits
0. Humans never lower the baseline by hand; the scheduled bot (07-G4) runs
`--prune` and opens the PR that does. `--prune` (and its older spelling
`--write-baseline`) is LOWER-ONLY: it writes baseline ∩ current and can never
add an identifier, so a bot run cannot launder a new dead function into the
baseline.

Exit codes: 0 ok, 1 ratchet rose, 2 the tool could not run or its output could
not be understood (never the same code as a real rise).

The tool is built from tools/deadcode/go.mod (go.sum-verified, so it builds
offline from a warm module cache), like staticcheck and mockery. Binary lookup
order: $DEADCODE_BIN, then GOBIN / GOPATH/bin, then PATH, and only then a
fallback build from the tools module into a temp dir. Whichever binary is used
must have been built from golang.org/x/tools at the pinned version (checked
with `go version -m`; deadcode has no -version flag).

Identifiers, not line numbers, are stored (`<package dir>.<Name>`, methods as
`<package dir>.<Type>.<Name>`) so unrelated edits that shift a function do not
change the baseline.

Why GOOS/GOARCH are pinned to linux/amd64: build-tagged files
(`*_linux.go`, `*_darwin.go`) are only analysed under the GOOS that selects
them, so a run on a developer Mac and a run on the ubuntu CI runner would see
different sets. Pinning makes every runner report what CI reports. The tool
binary itself is built for the host (a cross-GOOS `go run` cannot execute).
CGO_ENABLED=0 is pinned for the same reason: with cgo on, a Mac cannot load
the linux cgo packages and the run fails. The pin is x/tools v0.51.0 (the
version the baseline was measured with): a version bump can change the set on
its own and trip the ratchet as if code had changed.

Excluded from the set: pkg/ (public SDK, exported on purpose),
internal/writeback/ (the iTunes writer), test files, test helper packages
(testutil, conformance, rapidgen and the explicit TEST_HELPER_DIRS list), mock_store.go, and any name
containing `ForTest` (test seams).
"""

from __future__ import annotations

import argparse
import os
import re
import shutil
import subprocess
import sys
import tempfile
from pathlib import Path

DEADCODE_TOOLS_VERSION = "v0.51.0"  # keep equal to tools/deadcode/go.mod (a test checks)
INSTALL_TIMEOUT_S = 300
RUN_TIMEOUT_S = 1800
LINE_RE = re.compile(r"^(?P<file>[^:\s]+):\d+:\d+: unreachable func: (?P<name>\S+)\s*$")
EXCLUDED_DIR_PREFIXES = ("pkg/", "internal/writeback/")
# Test-helper package directories, listed explicitly. A `*test` suffix match
# would also drop a production package such as a future internal/latest/.
# Update this list when a new test-helper package is added.
TEST_HELPER_DIRS = {"aggtest", "dbtest", "vptest", "logtest", "opstest", "mailtest"}
EXCLUDED_PKG_NAMES = {"testutil", "conformance", "rapidgen"} | TEST_HELPER_DIRS


class ToolError(Exception):
    """The tool could not be run or its output not understood (exit 2)."""


def repo_root() -> Path:
    out = subprocess.run(
        ["git", "rev-parse", "--show-toplevel"], check=True, capture_output=True, text=True
    )
    return Path(out.stdout.strip())


def excluded(file: str, name: str) -> bool:
    if file.endswith("_test.go") or file.endswith("mock_store.go"):
        return True
    if "ForTest" in name:
        return True
    if file.startswith(EXCLUDED_DIR_PREFIXES):
        return True
    return any(part in EXCLUDED_PKG_NAMES for part in file.split("/")[:-1])


def parse(output: str) -> set[str]:
    """Turn deadcode's flat `file:line:col: unreachable func: Name` lines
    into a set of `<package dir>.<Name>` identifiers, filters applied.

    A non-empty line that does not match the format raises ToolError: a format
    change must not be read as "nothing is unreachable"."""
    found: set[str] = set()
    for line in output.splitlines():
        if not line.strip():
            continue
        m = LINE_RE.match(line)
        if not m:
            raise ToolError(f"unrecognised deadcode output line: {line!r}")
        file, name = m.group("file"), m.group("name")
        if excluded(file, name):
            continue
        pkg_dir = file.rsplit("/", 1)[0] if "/" in file else "."
        found.add(f"{pkg_dir}.{name}")
    return found


def read_baseline(path: Path) -> set[str]:
    return {
        ln.strip()
        for ln in path.read_text().splitlines()
        if ln.strip() and not ln.lstrip().startswith("#")
    }


def compare(current: set[str], baseline: set[str]) -> tuple[list[str], list[str]]:
    return sorted(current - baseline), sorted(baseline - current)


def _go_env_bin_dirs() -> list[str]:
    dirs: list[str] = []
    try:
        out = subprocess.run(
            ["go", "env", "GOBIN", "GOPATH"], capture_output=True, text=True, timeout=30, check=True
        ).stdout.split("\n")
    except (OSError, subprocess.SubprocessError):
        return dirs
    gobin = out[0].strip() if out else ""
    gopath = out[1].strip() if len(out) > 1 else ""
    if gobin:
        dirs.append(gobin)
    for p in gopath.split(os.pathsep):
        if p:
            dirs.append(os.path.join(p, "bin"))
    return dirs


def find_binary() -> str | None:
    """$DEADCODE_BIN, then GOBIN / GOPATH/bin, then PATH."""
    env = os.environ.get("DEADCODE_BIN")
    if env:
        return env
    for d in _go_env_bin_dirs():
        cand = os.path.join(d, "deadcode")
        if os.path.isfile(cand) and os.access(cand, os.X_OK):
            return cand
    return shutil.which("deadcode")


def build_from_tools_module(root: Path, dest: str) -> str:
    """Fallback: build the pinned tool from tools/deadcode/go.mod into dest."""
    env = {k: v for k, v in os.environ.items() if k not in ("GOOS", "GOARCH")}
    env["GOBIN"] = dest
    try:
        subprocess.run(
            ["go", "-C", str(root / "tools" / "deadcode"), "install", "tool"],
            check=True,
            env=env,
            timeout=INSTALL_TIMEOUT_S,
            capture_output=True,
            text=True,
        )
    except subprocess.CalledProcessError as e:
        raise ToolError(
            f"could not build deadcode from tools/deadcode (exit {e.returncode}): "
            f"{(e.stderr or '').strip()[-600:]}\n"
            "Run `make deadcode-install` once with network access, or set DEADCODE_BIN."
        ) from e
    except subprocess.TimeoutExpired as e:
        raise ToolError(
            f"building deadcode timed out after {INSTALL_TIMEOUT_S}s (unreachable module proxy?). "
            "Run `make deadcode-install` once with network access, or set DEADCODE_BIN."
        ) from e
    except OSError as e:
        raise ToolError(f"could not run `go` to build deadcode: {e}") from e
    return os.path.join(dest, "deadcode")


def check_binary_version(binary: str) -> None:
    """deadcode has no -version flag; read the module version it was built with."""
    try:
        res = subprocess.run(
            ["go", "version", "-m", binary], capture_output=True, text=True, timeout=60
        )
    except (OSError, subprocess.SubprocessError) as e:
        raise ToolError(f"cannot inspect {binary} with `go version -m`: {e}") from e
    if res.returncode != 0:
        raise ToolError(f"{binary} is not a Go binary or does not exist: {res.stderr.strip()}")
    want = re.compile(
        r"^\s*mod\s+golang\.org/x/tools\s+" + re.escape(DEADCODE_TOOLS_VERSION) + r"\b", re.M
    )
    if not want.search(res.stdout):
        raise ToolError(
            f"{binary} was not built from golang.org/x/tools {DEADCODE_TOOLS_VERSION} "
            "(a different version can change the set on its own). "
            "Rebuild with `make deadcode-install`."
        )


def run_deadcode(root: Path) -> str:
    with tempfile.TemporaryDirectory() as tmp:
        binary = find_binary() or build_from_tools_module(root, tmp)
        check_binary_version(binary)
        env = dict(os.environ, GOOS="linux", GOARCH="amd64", CGO_ENABLED="0")
        try:
            res = subprocess.run(
                [binary, "./..."],
                cwd=root,
                env=env,
                capture_output=True,
                text=True,
                timeout=RUN_TIMEOUT_S,
            )
        except subprocess.TimeoutExpired as e:
            raise ToolError(f"deadcode timed out after {RUN_TIMEOUT_S}s") from e
        except OSError as e:
            raise ToolError(f"could not run {binary}: {e}") from e
        if res.returncode != 0:
            raise ToolError(f"deadcode exited {res.returncode}:\n{res.stderr}")
        return res.stdout


def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    ap.add_argument(
        "--prune",
        "--write-baseline",
        dest="prune",
        action="store_true",
        help="lower-only: rewrite the baseline as baseline ∩ current (never adds; bot only)",
    )
    ap.add_argument("--baseline", default="scripts/deadcode-baseline.txt")
    ap.add_argument("--input", help="read deadcode output from this file instead of running it")
    args = ap.parse_args(argv)

    root = repo_root()
    baseline_path = root / args.baseline
    try:
        output = Path(args.input).read_text() if args.input else run_deadcode(root)
        current = parse(output)
    except ToolError as e:
        print(f"deadcode-ratchet: ERROR (not a ratchet rise): {e}", file=sys.stderr)
        return 2

    baseline = read_baseline(baseline_path)

    if args.prune:
        kept = baseline & current  # lower-only: never add what is not already baselined
        baseline_path.write_text("".join(f"{i}\n" for i in sorted(kept)))
        print(f"pruned {args.baseline}: {len(baseline)} -> {len(kept)} identifiers")
        return 0

    new, gone = compare(current, baseline)
    print(f"deadcode: {len(current)} unreachable (baseline {len(baseline)})")
    if gone:
        print(
            f"::notice::deadcode: {len(gone)} identifier(s) no longer unreachable; "
            f"the bot (or `python3 scripts/deadcode-ratchet.py --prune`, lower-only) prunes the baseline"
        )
    if new:
        print(
            f"FAIL: {len(new)} new unreachable function(s) not in {args.baseline}:", file=sys.stderr
        )
        for ident in new:
            print(f"  {ident}", file=sys.stderr)
        print(
            "Delete the dead code, or make it reachable from a main. "
            "Do not add to the baseline by hand.",
            file=sys.stderr,
        )
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
