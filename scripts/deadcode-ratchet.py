#!/usr/bin/env python3
# file: scripts/deadcode-ratchet.py
# version: 1.4.3
# guid: 5d0c7f2e-8a41-4b6d-9e13-c7a2f4b86d90
# last-edited: 2026-11-09
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

Mains, modules and filter (09 §9.4 R1, MOD2-19). core/ is its own Go module
(github.com/falkcorp/mediacore, wired into the root module by a `replace`), and
a package loads in exactly one module's context: the root module's packages and
whatever the root imports load from the root's go.mod/go.sum; core's own
packages (httpapi and its jsonschema-go dependency among them) load from
core/go.mod/go.sum. A hello main loaded from the repo root therefore fails on
core's third-party dependencies (root's go.sum has no hash for them, and `go
mod tidy` at the root never adds one because no root package imports them), so
the analysis is TWO deadcode runs, each in its own module context:

  root run  cwd = repo root, `./...` (every ABO main). Filter: the root module
            plus core's `internal` and `kernel` trees (what root imports).
  core run  cwd = core/, hello's mains (`./examples/hello/...`). Filter: core's
            `internal` and `kernel` trees. Only when core/examples/hello holds
            Go files; without them there is one run, the root run, exactly as
            before hello existed.

`-filter` REPLACES deadcode's default (the loaded module's own packages), so
each run names its filter explicitly, anchored:
`github.com/falkcorp/mediacore/(internal|kernel)` for core. Core's exported
packages are SDK surface and out of scope, as `pkg/` is. A filter naming only
core's trees would drop every root finding, and `--prune` would then empty the
baseline, which is the silent re-baseline this gate exists to prevent.

SDK surface counts as live (core run only). Core's exported packages are not
analysed, but they ARE callable: app.App.Handler is spec'd "for coretest and
embedding", no main of ours calls it, and it is the only caller of
kernel.Kernel.Handler. Reporting that as dead would contradict treating the
surface as out of scope. So the core run has one more main besides hello's: a
synthetic package that references every exported func, and every exported
method of every exported type, of core's public packages (tools/deadcode/
surface generates it). Public means not `kernel`, nothing under `internal/`,
`examples/`, `testdata/` or `archtest`, no main package, and no *_test.go
file; the run never uses deadcode's `-test`, so test-only code stays dead.
References are method expressions and func values, each stored in its own
package-level variable (no instance is constructed, and never a []any: an
interface conversion makes the signature's types runtime types, and deadcode
then treats every exported method they reach as live through reflection,
which would hide Kernel.Lifecycle-style dead methods). Generic funcs and methods of generic types are instantiated with
struct{} when every constraint is any, interface{} or comparable; any other
constraint is SKIPPED, listed in the generated file's header comment, and what
only that member reaches stays unreachable. The package is written into a
scratch COPY of core/ (never the real tree): deadcode loads through
go/packages, which reads sources from disk and ignores `-overlay` in GOFLAGS,
so a virtual file does not load. Paths are still reported relative to the copy's
root, which has core's layout, so normalisation below is unchanged. The root run
has no such main, and without hello there is no core run and no scratch copy.

Combining the runs. A function is unreachable from every main only if no run
reaches it, but a run can only judge the packages it loaded: a core package
that the root run loads and hello does not is NOT shown to be live by hello's
silence. So the rule is, per function: it is dead iff every run that LOADED its
package reports it unreachable (the intersection over the runs that can see
it). A function in a package only one run loads is dead iff that run reports it.
A package loaded by neither is invisible, as it always was. Loaded packages are
read with `go list -deps` under the same cwd, pattern and GOOS/GOARCH/CGO pin
as the deadcode run. Two separate analyses never share runtime types the way a
single joint program would, so the intersection can only report MORE than a
joint analysis would (the mains never run in one process), never less.

Paths are normalised before anything else: deadcode prints file paths relative
to its cwd, so the core run's `kernel/kernel.go` becomes `core/kernel/kernel.go`
(repo-root-relative) before the exclusion rules and the identifier are built.
That is what keeps core's identifiers `core/`-prefixed in the one
scripts/deadcode-baseline.txt (there is no separate core baseline) and keeps
the baseline format unchanged.

Excluded from the set: pkg/ (public SDK, exported on purpose),
internal/writeback/ (the iTunes writer), test files, test helper packages
(testutil, conformance, rapidgen and the explicit TEST_HELPER_DIRS list), mock_store.go, and any name
containing `ForTest` (test seams).
"""

from __future__ import annotations

import argparse
import dataclasses
import os
import posixpath
import re
import shutil
import subprocess
import sys
import tempfile
from dataclasses import dataclass
from pathlib import Path

DEADCODE_TOOLS_VERSION = "v0.51.0"  # keep equal to tools/deadcode/go.mod (a test checks)
INSTALL_TIMEOUT_S = 300
RUN_TIMEOUT_S = 1800
LINE_RE = re.compile(r"^(?P<file>[^:\s]+):\d+:\d+: unreachable func: (?P<name>\S+)\s*$")
EXCLUDED_DIR_PREFIXES = ("pkg/", "internal/writeback/")
# Test-helper package directories, listed explicitly. A `*test` suffix match
# would also drop a production package such as a future internal/latest/.
# Update this list when a new test-helper package is added.
TEST_HELPER_DIRS = {
    "aggtest",
    "storetest",
    "vptest",
    "logtest",
    "opstest",
    "mailtest",
    "durtest",
    "metricstest",
}
EXCLUDED_PKG_NAMES = {"testutil", "conformance", "rapidgen"} | TEST_HELPER_DIRS


class ToolError(Exception):
    """The tool could not be run or its output not understood (exit 2)."""


CORE_MODULE = "github.com/falkcorp/mediacore"
CORE_DIR = "core"
CORE_FILTER = re.escape(CORE_MODULE) + r"/(internal|kernel)"
HELLO_DIR = (CORE_DIR, "examples", "hello")
HELLO_PATTERN = "./examples/hello/..."  # relative to core/: every hello main (cmd/hello)
SURFACE_DIR = "zz_sdksurface"  # the synthetic main, in the scratch copy of core/ only
MODULE_RE = re.compile(r"^module\s+(\S+)\s*$", re.M)


def root_module(root: Path) -> str:
    m = MODULE_RE.search((root / "go.mod").read_text())
    if not m:
        raise ToolError(f"no module line in {root / 'go.mod'}")
    return m.group(1)


@dataclass(frozen=True)
class RunSpec:
    """One deadcode run: where it runs (its module context) and what it loads."""

    name: str
    cwd: Path  # where the run executes; the real module dir, or a scratch copy of it
    filter: str
    patterns: tuple[str, ...]
    base: str = ""  # the module dir relative to the repo root ("" root, "core")
    surface: bool = False  # add the synthetic SDK-surface main (see the docstring)

    @property
    def args(self) -> list[str]:
        return ["-filter", self.filter, *self.patterns]


def hello_present(root: Path) -> bool:
    """True once core/examples/hello holds Go files (testdata/ does not count)."""
    hello = root.joinpath(*HELLO_DIR)
    return hello.is_dir() and any(
        "testdata" not in p.relative_to(hello).parts for p in hello.rglob("*.go")
    )


def plan(root: Path) -> list[RunSpec]:
    """The deadcode runs, one per module context (see the module docstring).

    The root run is always there. The core run exists only when hello does."""
    root_filter = f"^({re.escape(root_module(root))}|{CORE_FILTER})(/|$)"
    specs = [RunSpec("root", root, root_filter, ("./...",))]
    if hello_present(root):
        specs.append(
            RunSpec(
                "core",
                root / CORE_DIR,
                f"^{CORE_FILTER}(/|$)",
                (HELLO_PATTERN,),
                base=CORE_DIR,
                surface=True,
            )
        )
    return specs


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


def parse(output: str, base: str = "") -> dict[str, str]:
    """Turn deadcode's flat `file:line:col: unreachable func: Name` lines into
    `{<package dir>.<Name>: <package dir>}`, normalisation and filters applied.

    `base` is the run's cwd relative to the repo root ("" for the root run,
    "core" for the core run): deadcode prints paths relative to its cwd, and
    they are made repo-root-relative here, before the exclusion rules and the
    identifier see them.

    A non-empty line that does not match the format raises ToolError: a format
    change must not be read as "nothing is unreachable"."""
    found: dict[str, str] = {}
    for line in output.splitlines():
        if not line.strip():
            continue
        m = LINE_RE.match(line)
        if not m:
            raise ToolError(f"unrecognised deadcode output line: {line!r}")
        file, name = posixpath.normpath(posixpath.join(base, m.group("file"))), m.group("name")
        if excluded(file, name):
            continue
        pkg_dir = file.rsplit("/", 1)[0] if "/" in file else "."
        found[f"{pkg_dir}.{name}"] = pkg_dir
    return found


@dataclass(frozen=True)
class RunResult:
    """What one run found. `dead` maps identifier to package dir. `loaded` is
    the repo-relative package dirs the run loaded (None when it is the only run
    and so never needs to be compared)."""

    name: str
    dead: dict[str, str]
    loaded: frozenset[str] | None = None


def combine(results: list[RunResult]) -> set[str]:
    """Identifiers unreachable from every main across all runs.

    An identifier is dead iff every run that loaded its package reports it. A
    run that did not load the package cannot vouch for it either way (hello's
    silence about a package it never imports proves nothing), so it abstains.
    With one run there is nothing to combine."""
    if len(results) == 1:
        return set(results[0].dead)
    runs: list[tuple[dict[str, str], frozenset[str]]] = []
    for r in results:
        if r.loaded is None:
            raise ToolError(f"run {r.name!r}: loaded-package set missing in a multi-run analysis")
        for ident, pkg in r.dead.items():
            if pkg not in r.loaded:
                # deadcode reported a function in a package `go list -deps` says
                # the run did not load: the loaded set cannot be trusted.
                raise ToolError(f"run {r.name!r} reported {ident} from a package it did not load")
        runs.append((r.dead, r.loaded))
    dead: set[str] = set()
    for run_dead, _ in runs:
        for ident, pkg in run_dead.items():
            if not any(pkg in loaded and ident not in d for d, loaded in runs):
                dead.add(ident)
    return dead


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


def _run(cmd: list[str], spec: RunSpec, env: dict[str, str], what: str) -> str:
    try:
        res = subprocess.run(
            cmd, cwd=spec.cwd, env=env, capture_output=True, text=True, timeout=RUN_TIMEOUT_S
        )
    except subprocess.TimeoutExpired as e:
        raise ToolError(f"{what} ({spec.name} run) timed out after {RUN_TIMEOUT_S}s") from e
    except OSError as e:
        raise ToolError(f"could not run {cmd[0]} ({spec.name} run): {e}") from e
    if res.returncode != 0:
        raise ToolError(f"{what} ({spec.name} run) exited {res.returncode}:\n{res.stderr}")
    return res.stdout


def loaded_dirs(spec: RunSpec, env: dict[str, str]) -> frozenset[str]:
    """Repo-relative dirs of the non-std packages the run loads: what deadcode
    sees, via `go list -deps` with the run's cwd, patterns and env. A scratch
    copy has the module's layout, so spec.base re-roots it."""
    out = _run(
        ["go", "list", "-deps", "-f", "{{if not .Standard}}{{.Dir}}{{end}}", *spec.patterns],
        spec,
        env,
        "go list",
    )
    real_cwd = os.path.realpath(spec.cwd)
    dirs = set()
    for d in out.split():
        rel = os.path.relpath(os.path.realpath(d), real_cwd)
        if rel != ".." and not rel.startswith(".." + os.sep):  # module cache etc. is not ours
            dirs.add(posixpath.normpath(posixpath.join(spec.base, Path(rel).as_posix())))
    return frozenset(dirs)


def build_surface_helper(root: Path, dest: str) -> str:
    """Build tools/deadcode/surface (stdlib only, so it builds offline) into dest."""
    env = {k: v for k, v in os.environ.items() if k not in ("GOOS", "GOARCH")}
    binary = os.path.join(dest, "surface")
    try:
        subprocess.run(
            ["go", "-C", str(root / "tools" / "deadcode"), "build", "-o", binary, "./surface"],
            check=True,
            env=env,
            timeout=INSTALL_TIMEOUT_S,
            capture_output=True,
            text=True,
        )
    except subprocess.CalledProcessError as e:
        raise ToolError(
            f"could not build tools/deadcode/surface (exit {e.returncode}): "
            f"{(e.stderr or '').strip()[-600:]}"
        ) from e
    except (subprocess.TimeoutExpired, OSError) as e:
        raise ToolError(f"could not build tools/deadcode/surface: {e}") from e
    return binary


def with_surface(spec: RunSpec, helper: str, scratch: str, env: dict[str, str]) -> RunSpec:
    """The spec re-pointed at a scratch copy of its module that also holds the
    synthetic SDK-surface main, which joins the run's patterns. The real tree
    is only read."""
    copy = os.path.join(scratch, spec.base or "module")
    shutil.copytree(spec.cwd, copy, ignore=shutil.ignore_patterns(".git"))
    copied = dataclasses.replace(
        spec, cwd=Path(copy), patterns=(*spec.patterns, "./" + SURFACE_DIR)
    )
    _run([helper, "-dir", copy, "-out", os.path.join(copy, SURFACE_DIR)], copied, env, "surface")
    return copied


def run_deadcode(root: Path) -> list[RunResult]:
    with tempfile.TemporaryDirectory() as tmp:
        binary = find_binary() or build_from_tools_module(root, tmp)
        check_binary_version(binary)
        specs = plan(root)
        env = dict(os.environ, GOOS="linux", GOARCH="amd64", CGO_ENABLED="0")
        helper = build_surface_helper(root, tmp) if any(s.surface for s in specs) else ""
        results = []
        for spec in specs:
            if spec.surface:
                spec = with_surface(spec, helper, tmp, env)
            out = _run([binary, *spec.args], spec, env, "deadcode")
            dead = parse(out, spec.base)
            loaded = loaded_dirs(spec, env) if len(specs) > 1 else None
            results.append(RunResult(spec.name, dead, loaded))
        return results


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
    ap.add_argument(
        "--input",
        help="read ONE run's deadcode output (root-relative paths) from this file "
        "instead of running deadcode",
    )
    args = ap.parse_args(argv)

    root = repo_root()
    baseline_path = root / args.baseline
    try:
        if args.input:
            results = [RunResult("input", parse(Path(args.input).read_text()))]
        else:
            results = run_deadcode(root)
        current = combine(results)
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
