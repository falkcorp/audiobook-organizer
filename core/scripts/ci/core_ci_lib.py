#!/usr/bin/env python3
# file: core/scripts/ci/core_ci_lib.py
# version: 1.4.0
# guid: d60ca7b1-bb62-4184-8eec-82dba792b0ec
# last-edited: 2026-11-08
"""Shared helpers of core's version and readiness scripts (12 §12.3, §12.4).

Not a script: check_core_version.py, core_api_gate.py, readiness_report.py and
the root-side core-checkpoint job (.github/workflows/scripts/core_checkpoint.py)
import it, so there is one definition of "the version constant", "a checkpoint
ref" and "semver precedence", and one shape for a readiness result.

Stdlib only.
"""

from __future__ import annotations

import dataclasses
import json
import os
import pathlib
import re
import subprocess
from typing import Sequence

CHECKPOINT_PREFIX = "refs/core-checkpoints/"
# Checkpoints are refs, not tags (12 §12.4.1), so a default fetch never brings
# them; every reader fetches them with this refspec.
CHECKPOINT_REFSPEC = "+refs/core-checkpoints/*:refs/core-checkpoints/*"

_VERSION_CONST_RE = re.compile(r'^\s*const\s+Version\s*=\s*"([^"]*)"\s*$', re.M)
_CHECKPOINT_RE = re.compile(r"^refs/core-checkpoints/v(\d+\.\d+\.\d+)$")
_SEMVER_RE = re.compile(
    r"^v?(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)"
    r"(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$"
)


@dataclasses.dataclass(frozen=True)
class Semver:
    major: int
    minor: int
    patch: int
    pre: tuple[str, ...] = ()

    def __str__(self) -> str:
        core = f"{self.major}.{self.minor}.{self.patch}"
        return f"{core}-{'.'.join(self.pre)}" if self.pre else core

    def _key(self) -> tuple:
        # A pre-release sorts below its release; numeric identifiers sort below
        # alphanumeric ones, numerically among themselves (semver.org §11).
        pre = (
            (1,)
            if not self.pre
            else (0, *((0, int(p), "") if p.isdigit() else (1, 0, p) for p in self.pre))
        )
        return (self.major, self.minor, self.patch, pre)

    def __lt__(self, other: Semver) -> bool:
        return self._key() < other._key()

    def __le__(self, other: Semver) -> bool:
        return self._key() <= other._key()

    def __gt__(self, other: Semver) -> bool:
        return self._key() > other._key()

    def __ge__(self, other: Semver) -> bool:
        return self._key() >= other._key()


def parse_semver(text: str) -> Semver:
    m = _SEMVER_RE.match(text.strip())
    if not m:
        raise ValueError(f"not a semantic version: {text!r}")
    pre = tuple(m.group(4).split(".")) if m.group(4) else ()
    return Semver(int(m.group(1)), int(m.group(2)), int(m.group(3)), pre)


def version_from_source(source: str, where: str = "version.go") -> str:
    """The text of ``const Version`` in the source of core/version/version.go."""
    found = _VERSION_CONST_RE.findall(source)
    if len(found) != 1:
        raise ValueError(
            f"{where}: expected exactly one 'const Version = \"...\"', found {len(found)}"
        )
    return found[0]


def read_version(core: pathlib.Path) -> str:
    """The text of ``const Version`` in core/version/version.go."""
    path = core / "version" / "version.go"
    if not path.is_file():
        raise ValueError(f"{path} is missing")
    return version_from_source(path.read_text(encoding="utf-8"), str(path))


def module_path(core: pathlib.Path) -> str:
    """core's module path, from the module line of core/go.mod."""
    m = re.search(r"^module\s+(\S+)\s*$", (core / "go.mod").read_text(encoding="utf-8"), re.M)
    if not m:
        raise ValueError(f"{core / 'go.mod'}: no module line")
    return m.group(1)


def git(repo: pathlib.Path, *args: str, check: bool = True) -> subprocess.CompletedProcess:
    return subprocess.run(
        ["git", "-C", str(repo), *args],
        capture_output=True,
        text=True,
        check=check,
    )


def repo_root(core: pathlib.Path) -> tuple[pathlib.Path, str]:
    """(the git work tree holding core, core's path inside it).

    ("<repo>", "core/") in this monorepo; ("<core>", "") in an export of core/
    alone (12 SC4(a)), which is a fresh repository holding only core/.
    """
    top = git(core, "rev-parse", "--show-toplevel").stdout.strip()
    prefix = git(core, "rev-parse", "--show-prefix").stdout.strip()
    return pathlib.Path(top), prefix


def is_git_repo(repo: pathlib.Path) -> bool:
    res = git(repo, "rev-parse", "--git-dir", check=False)
    return res.returncode == 0


def has_remote(repo: pathlib.Path, remote: str) -> bool:
    return remote in git(repo, "remote").stdout.split()


def fetch_checkpoints(repo: pathlib.Path, remote: str = "origin") -> None:
    """Fetch every refs/core-checkpoints/* from the remote (12 §12.4.1).

    A refspec glob that matches nothing is not an error, so "no checkpoint yet"
    fetches cleanly. Any other failure raises RuntimeError: the caller must not
    treat an unreachable remote as "no checkpoints".
    """
    res = git(repo, "fetch", "--quiet", "--no-tags", remote, CHECKPOINT_REFSPEC, check=False)
    if res.returncode != 0:
        raise RuntimeError(f"git fetch {remote} {CHECKPOINT_REFSPEC}: {res.stderr.strip()}")


def checkpoint_version(refname: str) -> Semver:
    m = _CHECKPOINT_RE.match(refname)
    if not m:
        raise ValueError(
            f"{refname}: a checkpoint ref must be {CHECKPOINT_PREFIX}vX.Y.Z (12 §12.4.1)"
        )
    return parse_semver(m.group(1))


def list_checkpoints(repo: pathlib.Path) -> list[tuple[str, Semver]]:
    """Every checkpoint ref in the repository, newest version first.

    Raises ValueError for a ref under refs/core-checkpoints/ whose name is not
    vX.Y.Z: sorting around a malformed name would hide a recording defect.
    """
    out = git(repo, "for-each-ref", "--format=%(refname)", CHECKPOINT_PREFIX).stdout
    refs = [(name, checkpoint_version(name)) for name in out.split()]
    refs.sort(key=lambda r: r[1], reverse=True)
    return refs


def commit_date(repo: pathlib.Path, ref: str) -> str:
    """The committer date of ref as YYYY-MM-DD (git log -1 --format=%cs)."""
    return git(repo, "log", "-1", "--format=%cs", ref).stdout.strip()


PASS = "PASS"
FAIL = "FAIL"
NOT_APPLICABLE = "NOT_APPLICABLE"
ERROR = "ERROR"


@dataclasses.dataclass
class Result:
    """One readiness criterion's outcome (the unit readiness_report.py folds).

    status is PASS, FAIL, NOT_APPLICABLE (the subject does not exist yet) or
    ERROR (the measurement itself could not run, which is not a FAIL of the
    criterion). measured is the one-line value; details are extra lines.
    """

    id: str
    title: str
    status: str
    measured: str
    details: list[str] = dataclasses.field(default_factory=list)

    def line(self) -> str:
        return f"{self.status} {self.id}: {self.measured}"


def write_result(out_dir: pathlib.Path, result: Result) -> pathlib.Path:
    out_dir.mkdir(parents=True, exist_ok=True)
    path = out_dir / f"{result.id.lower()}.json"
    path.write_text(json.dumps(dataclasses.asdict(result), indent=2) + "\n", encoding="utf-8")
    return path


def finish(result: Result, out_dir: pathlib.Path | None, report_only: bool) -> int:
    """Print the result, store it when asked, and return the exit code.

    PASS and NOT_APPLICABLE exit 0. FAIL exits 1 and ERROR (the measurement could
    not run) exits 2, unless report_only: `make -C core readiness` is a report,
    so a criterion that is not met, or a tool that is missing, is recorded in the
    result and the script still exits 0. A crash is an exception and never exits 0.
    """
    print(result.line())
    for line in result.details:
        print(f"  {line}")
    if out_dir is not None:
        write_result(out_dir, result)
    if result.status in (PASS, NOT_APPLICABLE) or report_only:
        return 0
    return 2 if result.status == ERROR else 1


def go_env() -> dict[str, str]:
    """The environment for go commands: no workspace, no inherited flags, core's toolchain pin."""
    env = dict(os.environ)
    env.pop("GOFLAGS", None)
    env["GOWORK"] = "off"
    if not env.get("GOTOOLCHAIN"):
        core = pathlib.Path(__file__).resolve().parents[2]
        pin = subprocess.run(
            ["make", "-s", "-C", str(core), "print-toolchain"],
            capture_output=True,
            text=True,
            check=False,
        )
        if pin.returncode == 0 and pin.stdout.strip():
            env["GOTOOLCHAIN"] = pin.stdout.strip()
    return env


def load_results(out_dir: pathlib.Path, ids: Sequence[str]) -> list[Result | None]:
    """The stored result for each id, None for one that was never written."""
    loaded: list[Result | None] = []
    for ident in ids:
        path = out_dir / f"{ident.lower()}.json"
        if not path.is_file():
            loaded.append(None)
            continue
        loaded.append(Result(**json.loads(path.read_text(encoding="utf-8"))))
    return loaded
