#!/usr/bin/env python3
# file: deploy/rollback-helpers/install_helpers.py
# version: 0.1.0
# guid: 882cc62f-db07-4203-82a6-2ba8ea108329
# last-edited: 2026-11-20

"""`make install-rollback-helpers`: the owner's one-time install of the rollback
helper bundle, on the deploy host.

It puts in place, from the checkout it runs in, everything `make deploy`,
`make rollback` and `make rollback-restore` need on the host and nothing else:

    the helper bundle    rollback_helper.py and one stub per operation, and
                         the unit template, under the helpers' directory
    rollback.conf        the fixed paths the helpers use, rendered from the
                         values given (never from a guess)
    the sudoers drop-in  rendered by scripts/storage_format_guard.py (the same
                         table the recipes run), validated with `visudo -c`
                         on a temporary file and only then moved into place

It prints what it WOULD do and changes nothing unless run with --apply. It is
idempotent: a file already as it should be is reported `unchanged` and is not
written, so running it again after a `git pull` replaces exactly what changed
(the helper, after a release that changes it). A rollback.conf that exists and
differs from the rendered one is never overwritten without --replace-conf,
since the owner may have edited it; the difference is shown.

It does not write host.conf (the unit drop-in the owner writes once; see
docs/system/deploy-and-gpu-ops.md, "host.conf"), and it never touches
Makefile.local: install the bundle FIRST, then sync Makefile.local, because
the synced recipes call the helper at their first step.

Needs root to apply. Run as a user it re-runs itself under `sudo`; --root
PREFIX puts every destination under PREFIX instead (the tests; also a staging
tree). Standard library only. Tests: scripts/tests/test_install_helpers.py.
"""

from __future__ import annotations

import argparse
import contextlib
import difflib
import importlib.util
import os
import shlex
import stat
import subprocess
import sys
from dataclasses import dataclass
from pathlib import Path
from types import ModuleType

HERE = Path(__file__).resolve().parent
REPO = HERE.parent.parent
SUDOERS_NAME = "audiobook-organizer-rollback"
# A name starting with "." is skipped by sudo's includedir, so the file being
# validated is never live.
SUDOERS_TMP = f".{SUDOERS_NAME}.new"
CONF_KEYS = (
    "bin",
    "db",
    "state_dir",
    "unit",
    "service_user",
    "archive_dir",
    "checkpoint_dir",
    "offline_env",
    "service_group",
    "stage_bin",
    "stage_local_conf",
)
REQUIRED = ("bin", "db", "state_dir", "unit", "service_user", "archive_dir", "stage_bin")


def load(name: str, path: Path) -> ModuleType:
    spec = importlib.util.spec_from_file_location(name, path)
    if spec is None or spec.loader is None:
        raise SystemExit(f"install_helpers: cannot load {path}")
    mod = importlib.util.module_from_spec(spec)
    sys.modules[name] = mod
    spec.loader.exec_module(mod)
    return mod


@dataclass
class Step:
    """One destination: what it holds, who owns it, and what to do."""

    path: str
    data: bytes
    mode: int
    kind: str  # "file" or "dir"
    note: str = ""


def render_conf(values: dict[str, str]) -> str:
    """rollback.conf from the values given. The example's comments are not
    kept: the file the helpers read holds settings only."""
    lines = [
        "# /etc/audiobook-organizer/rollback.conf: written by `make install-rollback-helpers`.",
        "# Root-owned, mode 0644. The deploy user cannot change it.",
    ]
    for key in CONF_KEYS:
        lines.append(f"{key} = {values[key]}" if values.get(key) else f"{key} =")
    return "\n".join(lines) + "\n"


def plan(a: argparse.Namespace, rh: ModuleType, guard: ModuleType) -> tuple[list[Step], str]:
    """The steps, and the rendered sudoers text."""
    libdir = rh.LIBDIR
    if libdir != guard.HELPER_DIR:
        raise SystemExit(
            f"install_helpers: the helper ({libdir}) and the guard ({guard.HELPER_DIR}) disagree "
            "about the helpers' directory"
        )
    root = a.root.rstrip("/")
    values = {
        "bin": a.bin,
        "db": a.db,
        "state_dir": a.state_dir,
        "unit": a.unit,
        "service_user": a.service_user,
        "archive_dir": a.archive_dir,
        "checkpoint_dir": a.checkpoint_dir,
        "offline_env": a.offline_env or f"HOME={a.state_dir} ABK_STATE_DIR={a.state_dir}",
        "service_group": a.service_group,
        "stage_bin": a.stage_bin,
        "stage_local_conf": a.stage_local_conf,
    }
    missing = [k for k in REQUIRED if not values.get(k)]
    if missing:
        raise SystemExit(
            "install_helpers: not set: "
            + ", ".join(missing)
            + " (the Makefile target passes DEPLOY_BIN, DEPLOY_DB and DEPLOY_UNIT; give the rest as "
            "ROLLBACK_INSTALL_ARGS, see docs/system/deploy-and-gpu-ops.md)"
        )
    o = guard.Opts(
        host="localhost",
        db=a.db,
        bin=a.bin,
        unit=a.unit,
        user=a.service_user,
        stage_bin=a.stage_bin,
    )
    bad = guard._validate(o) or (
        "" if guard._SAFE_USER.match(a.deploy_user) else f"--deploy-user {a.deploy_user!r}"
    )
    if bad:
        raise SystemExit(f"install_helpers: {bad}")
    steps = [
        Step(f"{root}{libdir}", b"", 0o755, "dir"),
        Step(f"{root}{os.path.dirname(rh.CONFIG)}", b"", 0o755, "dir"),
    ]
    steps.append(
        Step(
            f"{root}{libdir}/rollback_helper.py",
            (HERE / "rollback_helper.py").read_bytes(),
            0o755,
            "file",
        )
    )
    for stub in sorted(rh.STUBS):
        steps.append(Step(f"{root}{libdir}/{stub}", (HERE / stub).read_bytes(), 0o755, "file"))
    steps.append(
        Step(
            f"{root}{libdir}/{rh.UNIT_TEMPLATE}",
            (REPO / "deploy" / rh.UNIT_TEMPLATE).read_bytes(),
            0o644,
            "file",
            "the unit template the helper installs as the unit",
        )
    )
    steps.append(
        Step(f"{root}{rh.CONFIG}", render_conf(values).encode(), 0o644, "file", "rollback.conf")
    )
    sudoers = guard.sudoers(o, a.deploy_user)
    return steps, sudoers


def state(step: Step) -> str:
    """unchanged | create | replace (a directory: unchanged | create)."""
    p = Path(step.path)
    if step.kind == "dir":
        return "unchanged" if p.is_dir() and not p.is_symlink() else "create"
    if not p.exists() and not p.is_symlink():
        return "create"
    if p.is_symlink() or not p.is_file():
        return "replace"
    if p.read_bytes() == step.data and stat.S_IMODE(p.stat().st_mode) == step.mode:
        return "unchanged"
    return "replace"


def ensure_dir(path: str, mode: int) -> None:
    """Create path and any missing parent, each with exactly mode: makedirs
    applies the umask, and a root shell with a tight one would leave the
    helpers' directory unreadable to the service user."""
    missing = []
    p = Path(path)
    while not p.exists() and p != p.parent:
        missing.append(p)
        p = p.parent
    for d in reversed(missing):
        d.mkdir()
        os.chmod(d, mode)


def write_atomic(path: str, data: bytes, mode: int, chown: bool) -> None:
    tmp = f"{path}.tmp"
    with contextlib.suppress(FileNotFoundError):
        os.unlink(tmp)
    fd = os.open(tmp, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, mode)
    try:
        with os.fdopen(fd, "wb") as f:
            f.write(data)
            f.flush()
            os.fchmod(f.fileno(), mode)
            os.fsync(f.fileno())
        if chown:
            os.chown(tmp, 0, 0)
        os.replace(tmp, path)
    except BaseException:
        with contextlib.suppress(OSError):
            os.unlink(tmp)
        raise


def validate_sudoers(visudo: str, text: str, directory: str) -> str:
    """Write text beside the live drop-in under a name sudo ignores and check
    it with `visudo -c -f`. Returns the temporary path; raises SystemExit
    (after deleting it) when visudo refuses."""
    ensure_dir(directory, 0o755)
    tmp = f"{directory}/{SUDOERS_TMP}"
    write_atomic(tmp, text.encode(), 0o440, chown=False)
    try:
        r = subprocess.run(  # noqa: S603 (argv, no shell)
            [*shlex.split(visudo), "-c", "-f", tmp], capture_output=True, text=True, check=False
        )
    except OSError as e:
        os.unlink(tmp)
        raise SystemExit(
            f"install_helpers: cannot run {visudo} ({e}); nothing was moved into place"
        )
    if r.returncode != 0:
        os.unlink(tmp)
        raise SystemExit(
            f"install_helpers: visudo refused the sudoers drop-in, so it was NOT installed "
            f"({(r.stdout + r.stderr).strip()})"
        )
    return tmp


def main(argv: list[str] | None = None) -> int:
    p = argparse.ArgumentParser(description=(__doc__ or "").splitlines()[0])
    for flag in ("bin", "db", "unit", "state-dir", "service-user", "archive-dir", "stage-bin"):
        p.add_argument(f"--{flag}", default="")
    for flag in ("stage-local-conf", "checkpoint-dir", "service-group", "offline-env"):
        p.add_argument(f"--{flag}", default="")
    p.add_argument("--deploy-user", default="")
    p.add_argument("--apply", action="store_true", help="make the changes (default: show them)")
    p.add_argument(
        "--replace-conf", action="store_true", help="overwrite a rollback.conf that differs"
    )
    p.add_argument("--root", default="", help="put every destination under this prefix")
    p.add_argument("--visudo", default="visudo", help="the visudo command (tests give a fake)")
    a = p.parse_args(argv)
    a.unit = a.unit or "audiobook-organizer.service"
    a.deploy_user = a.deploy_user or os.environ.get("SUDO_USER") or os.environ.get("USER", "")
    rh = load("rollback_helper", HERE / "rollback_helper.py")
    guard = load("storage_format_guard", REPO / "scripts" / "storage_format_guard.py")
    steps, sudoers = plan(a, rh, guard)
    root = a.root.rstrip("/")
    sudoers_dir = f"{root}/etc/sudoers.d"
    sudoers_path = f"{sudoers_dir}/{SUDOERS_NAME}"
    # Everything is decided before anything is written.
    todo = [(s, state(s)) for s in steps]
    sudo_state = (
        "unchanged"
        if Path(sudoers_path).is_file() and Path(sudoers_path).read_text() == sudoers
        else ("replace" if os.path.lexists(sudoers_path) else "create")
    )
    conf_step = next(s for s in steps if s.note == "rollback.conf")
    conf_state = state(conf_step)
    refuse_conf = conf_state == "replace" and Path(conf_step.path).is_file() and not a.replace_conf
    print(f"Rollback helper bundle, from {REPO}:")
    for s, st in todo:
        print(f"  {st:<9} {s.path}" + (f"  ({s.note})" if s.note else ""))
    print(f"  {sudo_state:<9} {sudoers_path}  (sudoers drop-in, checked with visudo -c first)")
    if refuse_conf:
        old = Path(conf_step.path).read_text().splitlines()
        new = conf_step.data.decode().splitlines()
        print(f"\n{conf_step.path} exists and differs from the values given:")
        print("\n".join(difflib.unified_diff(old, new, "installed", "given", lineterm="")))
        print("It is left alone; pass --replace-conf to overwrite it.")
    changes = [x for x in todo if x[1] != "unchanged"] + (
        [(None, sudo_state)] if sudo_state != "unchanged" else []
    )
    if not changes:
        print("\nNothing to do: the bundle is installed and current.")
        return 1 if refuse_conf else 0
    if not a.apply:
        print(
            "\nThis was a dry run: nothing was changed. Run it again with APPLY=1 "
            "(make install-rollback-helpers APPLY=1) to install."
        )
        return 1 if refuse_conf else 0
    if os.geteuid() != 0 and not root:
        print("\nApplying needs root: re-running under sudo.", flush=True)
        os.execvp("sudo", ["sudo", sys.executable, *sys.argv])  # noqa: S606 (argv)
    chown = os.geteuid() == 0
    # The sudoers text is checked first: a drop-in visudo refuses stops the
    # install before a single file is written.
    tmp = validate_sudoers(a.visudo, sudoers, sudoers_dir) if sudo_state != "unchanged" else ""
    for s, st in todo:
        if st == "unchanged" or (s is conf_step and refuse_conf):
            continue
        if s.kind == "dir":
            ensure_dir(s.path, s.mode)
            continue
        ensure_dir(os.path.dirname(s.path), 0o755)
        write_atomic(s.path, s.data, s.mode, chown)
        print(f"  wrote {s.path}")
    if tmp:
        if chown:
            os.chown(tmp, 0, 0)
        os.replace(tmp, sudoers_path)
        print(f"  wrote {sudoers_path}")
    print(
        "\nInstalled. Still yours: host.conf (the unit drop-in, written once; see "
        "docs/system/deploy-and-gpu-ops.md), then sync Makefile.local from Makefile.local.example. "
        "`make rollback-check` shows what the host keeps."
    )
    return 1 if refuse_conf else 0


if __name__ == "__main__":
    sys.exit(main())
