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

What it trusts, because it runs as root and writes files root later executes:

    the destinations   a file already there is `unchanged` only if its bytes,
                       mode AND owner are right (a file the deploy user owns
                       is rewritten, not trusted); a directory is chowned 0:0
                       and chmod-ed; and it REFUSES, naming the path, when any
                       parent of the helpers' directory, /etc/audiobook-organizer
                       or /etc/sudoers.d is not root-owned or is group- or
                       world-writable (whoever can rename inside one could swap
                       a directory it just made for a path of their own)
    the sources        the checkout it reads from, every file it reads, and every
                       directory above them must be owned by root with no group
                       or other write bit, no ACL and no symlink (a sticky
                       root-owned ancestor such as /tmp excepted): it loads the
                       guard from there as root, and the owner of a file can
                       chmod it, so there is no "writable by the deploy user"
                       model, only "root's". Run it from a root-owned export,
                       see docs/system/deploy-and-gpu-ops.md. It runs python
                       with -I -B and runs the sources it loads from their
                       bytes, never through __pycache__.

--deploy-user is required (the sudoers drop-in grants that user, so it is never
guessed from $SUDO_USER), and the dry run prints the grantee and the rendered
sudoers text, or its diff against what is installed.

Needs root to apply (`make install-rollback-helpers APPLY=1` exports HEAD to a
root-owned directory and runs it from there with sudo; run directly as a user it
re-runs itself under `sudo`, which the source check above refuses from a
checkout that is not root's); --root
PREFIX puts every destination under PREFIX instead (the tests; also a staging
tree). Standard library only. Tests: scripts/tests/test_install_helpers.py.
"""

from __future__ import annotations

# ruff: noqa: E402  (the isolation re-exec has to come before any other import)
import os
import sys

if __name__ == "__main__" and not sys.flags.isolated:
    # This runs as root: re-run with -I, so no PYTHON* variable, no user site
    # and no script or current directory decides which module `import` finds.
    os.execv(sys.executable, [sys.executable, "-I", "-B", *sys.argv])  # noqa: S606 (argv)

import argparse
import contextlib
import difflib
import shlex
import stat
import subprocess
from dataclasses import dataclass
from pathlib import Path
from types import ModuleType
from typing import NoReturn

HERE = Path(__file__).resolve().parent
REPO = HERE.parent.parent
SUDOERS_NAME = "audiobook-organizer-rollback"
# A name starting with "." is skipped by sudo's includedir, so the file being
# validated is never live.
SUDOERS_TMP = f".{SUDOERS_NAME}.new"
SUDOERS_MODE = 0o440
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
    """Run path's SOURCE as a fresh module. Never `import`, never a loader that
    consults __pycache__: this runs as root, and a cached .pyc (an unchecked
    hash-based one is never compared with the source) in a directory somebody
    else can write would run as root whatever the .py next to it says."""
    try:
        code = compile(path.read_bytes(), str(path), "exec")
    except OSError as e:
        raise SystemExit(f"install_helpers: cannot load {path} ({e.strerror})") from e
    mod = ModuleType(name)
    mod.__file__ = str(path)
    sys.modules[name] = mod  # a dataclass looks its module up here
    exec(code, mod.__dict__)  # noqa: S102 (the file this installs from, checked above)
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
        "" if guard._SAFE_USER.fullmatch(a.deploy_user) else f"--deploy-user {a.deploy_user!r}"
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


# Owners trusted for what is already installed. Root, always; a --root staging
# tree (the tests) is written by the user running it, so that user too.
STAGING = False


def trusted_uids() -> set[int]:
    return {0, os.geteuid()} if STAGING else {0}


def owned_right(st: os.stat_result) -> bool:
    """Owned by a trusted user, no group or other write bit, one link."""
    return st.st_uid in trusted_uids() and not st.st_mode & 0o022 and st.st_nlink == 1


def refuse(msg: str) -> NoReturn:
    raise SystemExit(f"install_helpers: {msg}")


def check_parents(base: str, target: str, include_target: bool) -> None:
    """Refuse when a directory above target (and target itself when
    include_target), from base down, is not root-owned or is group- or
    world-writable: whoever can rename inside one can swap what is below it.
    base is "/" (the real run) or the --root prefix, the stand-in for "/";
    a directory that does not exist yet will be made by this run, as root."""
    real = os.path.realpath(target)
    start = os.path.realpath(base or "/")
    rel = os.path.relpath(real, start) if real.startswith(start) else real.lstrip("/")
    parts = [x for x in rel.split(os.sep) if x and x != "."]
    cur = start if real.startswith(start) else "/"
    chain = [cur]
    for part in parts:
        cur = os.path.join(cur, part)
        chain.append(cur)
    if not include_target and chain[-1] == real:
        chain = chain[:-1]
    for d in chain:
        try:
            st = os.lstat(d)
        except FileNotFoundError:
            break
        except OSError as e:
            refuse(f"cannot inspect {d} ({e.strerror}); nothing was changed")
        if not stat.S_ISDIR(st.st_mode):
            refuse(f"{d} is not a directory (a parent of {target}); nothing was changed")
        if st.st_uid not in trusted_uids() or st.st_mode & 0o022:
            refuse(
                f"{d} (a parent of {target}) is not root-owned or is group- or world-writable "
                f"(owner uid {st.st_uid}, mode {stat.S_IMODE(st.st_mode):04o}): whoever can rename "
                "inside it could swap what this installs for a path of their own. Nothing was "
                "changed; fix its owner and mode first"
            )


def untrusted_source(st: os.stat_result, ancestor: bool) -> bool:
    """Could anyone but root change this path (or what is below it)? No model
    of what one named user can write: the owner of a file can chmod it, so a
    path is trusted only when root (not the user running the tests, outside a
    --root staging run) owns it and no group or other write bit is set. One
    exception: a sticky directory ABOVE the checkout, owned by root (/tmp),
    where only the owner of an entry can rename or remove it; the entry below
    it is a watched path itself and has to be root's."""
    if st.st_uid not in trusted_uids():
        return True
    if not st.st_mode & 0o022:
        return False
    sticky_root_dir = (
        stat.S_ISDIR(st.st_mode) and bool(st.st_mode & stat.S_ISVTX) and st.st_uid == 0
    )
    return not (ancestor and sticky_root_dir)


def has_acl(path: Path) -> bool:
    """A POSIX ACL (Linux xattr) can grant write without a mode bit showing it."""
    listx = getattr(os, "listxattr", None)
    if listx is None:
        return False
    try:
        return any(n.startswith("system.posix_acl") for n in listx(path, follow_symlinks=False))
    except OSError:
        return False


def check_sources() -> list[str]:
    """The checkout this runs from, and every file read from it, must be
    root's and closed to group and others, with no ACL and no symlink: the
    installer loads the guard from there as root and copies the helper from
    there into a root-owned directory. A __pycache__ in a source directory is
    held to the same rule. Returns the offending paths (empty when fine)."""
    watch: list[Path] = [REPO, REPO / "deploy", REPO / "scripts", HERE]
    watch += [x for x in HERE.iterdir() if x.is_file()]
    watch += [REPO / "scripts" / "storage_format_guard.py"]
    watch += [REPO / "deploy" / "audiobook-organizer.service"]
    for d in (REPO / "deploy", REPO / "scripts", HERE):
        cache = d / "__pycache__"
        if cache.is_dir() and not cache.is_symlink():
            watch += [cache, *cache.iterdir()]
        elif os.path.lexists(cache):
            watch.append(cache)
    ancestors = set(REPO.parents)
    watch += list(REPO.parents)
    bad: list[str] = []
    for w in watch:
        try:
            st = w.lstat()
        except OSError:
            continue
        if stat.S_ISLNK(st.st_mode) or untrusted_source(st, w in ancestors) or has_acl(w):
            bad.append(str(w))
    return bad


def state(step: Step) -> str:
    """unchanged | create | replace | fix, as _state(); `unreadable` when the
    caller cannot read what is installed (a dry run as an ordinary user over a
    root-only file or directory: `APPLY=1` compares it as root)."""
    try:
        return _state(step)
    except PermissionError:
        return "unreadable"


def _state(step: Step) -> str:
    """unchanged | create | replace (bytes differ) | fix (right bytes, wrong owner,
    mode or link count; a directory: wrong owner or mode)."""
    p = Path(step.path)
    if step.kind == "dir":
        if p.is_symlink():
            refuse(f"{step.path} is a symlink; nothing was changed")
        if not os.path.lexists(step.path):
            return "create"
        if not p.is_dir():
            refuse(f"{step.path} exists and is not a directory; nothing was changed")
        st = p.stat()
        ok = st.st_uid in trusted_uids() and stat.S_IMODE(st.st_mode) == step.mode
        return "unchanged" if ok else "fix"
    if not p.exists() and not p.is_symlink():
        return "create"
    if p.is_symlink() or not p.is_file():
        return "replace"
    if p.read_bytes() != step.data:
        return "replace"
    st = p.stat()
    if stat.S_IMODE(st.st_mode) == step.mode and owned_right(st):
        return "unchanged"
    return "fix"


def sudoers_state(path: str, text: str) -> str:
    """unchanged | create | replace | fix, as state(): the text, the mode and
    the owner all have to be right; unreadable when the caller cannot read it
    (a dry run as an ordinary user: `APPLY=1` compares it as root)."""
    p = Path(path)
    if not os.path.lexists(path):
        return "create"
    if p.is_symlink() or not p.is_file():
        return "replace"
    try:
        if p.read_text() != text:
            return "replace"
    except PermissionError:
        # root-owned 0440: a dry run as an ordinary user cannot compare it.
        return "unreadable"
    st = p.stat()
    return "unchanged" if stat.S_IMODE(st.st_mode) == SUDOERS_MODE and owned_right(st) else "fix"


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


def _sync_dir(path: str) -> None:
    """Best-effort fsync of a directory, so a rename survives a power cut."""
    try:
        fd = os.open(path, os.O_RDONLY)
    except OSError:
        return
    try:
        os.fsync(fd)
    except OSError:
        pass
    finally:
        os.close(fd)


def fix_dir(path: str, mode: int, chown: bool) -> None:
    """An existing directory gets exactly mode and, as root, owner 0:0."""
    if chown:
        os.chown(path, 0, 0)
    os.chmod(path, mode)


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
    write_atomic(tmp, text.encode(), SUDOERS_MODE, chown=False)
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


def sudo_argv() -> list[str]:
    """The command that re-runs this installer as root: isolated (-I), so no
    PYTHON* variable survives sudo's environment handling into a root python."""
    return ["sudo", sys.executable, "-I", "-B", *sys.argv]


def check_whole_config(visudo: str) -> str:
    """`visudo -c` over the whole sudoers configuration: the drop-in checked on
    its own can still be wrong in context (an Include order, an alias another
    file defines). Returns "" when it passes, else what visudo said."""
    try:
        r = subprocess.run(  # noqa: S603 (argv, no shell)
            [*shlex.split(visudo), "-c"], capture_output=True, text=True, check=False
        )
    except OSError as e:
        return f"cannot run {visudo} ({e})"
    return "" if r.returncode == 0 else (r.stdout + r.stderr).strip() or f"exit {r.returncode}"


def main(argv: list[str] | None = None) -> int:
    p = argparse.ArgumentParser(description=(__doc__ or "").splitlines()[0])
    for flag in ("bin", "db", "unit", "state-dir", "service-user", "archive-dir", "stage-bin"):
        p.add_argument(f"--{flag}", default="")
    for flag in ("stage-local-conf", "checkpoint-dir", "service-group", "offline-env"):
        p.add_argument(f"--{flag}", default="")
    p.add_argument(
        "--deploy-user", default="", help="REQUIRED: the user the sudoers drop-in grants"
    )
    p.add_argument("--apply", action="store_true", help="make the changes (default: show them)")
    p.add_argument(
        "--replace-conf", action="store_true", help="overwrite a rollback.conf that differs"
    )
    p.add_argument("--root", default="", help="put every destination under this prefix")
    p.add_argument("--visudo", default="visudo", help="the visudo command (tests give a fake)")
    a = p.parse_args(argv)
    sys.dont_write_bytecode = True  # root writes no .pyc beside sources others may own
    a.unit = a.unit or "audiobook-organizer.service"
    global STAGING
    STAGING = bool(a.root)
    if not a.deploy_user:
        raise SystemExit(
            "install_helpers: --deploy-user is required: the sudoers drop-in lets that user run the "
            "helpers as root, so it is named, never guessed from $SUDO_USER or $USER "
            '(ROLLBACK_INSTALL_ARGS="--deploy-user <the user make deploy ssh\'s as> ...")'
        )
    bad = check_sources()
    if bad and (a.apply or os.geteuid() == 0):
        raise SystemExit(
            "install_helpers: REFUSING to run as root from a checkout that is not root's "
            "(owner, group/other write bit, ACL or symlink on a path it reads or sits under): "
            f"{', '.join(bad[:6])}"
            + (f" and {len(bad) - 6} more" if len(bad) > 6 else "")
            + ". It loads scripts/storage_format_guard.py as root and copies the helper into a "
            "root-owned directory, so whoever can edit them gets root. Run it from a root-owned "
            'export instead (docs/system/deploy-and-gpu-ops.md, "Rollback helpers"): '
            "d=$(sudo mktemp -d /opt/aorg-install.XXXXXX) && git -c tar.umask=0022 archive HEAD "
            "deploy/rollback-helpers deploy/audiobook-organizer.service "
            'scripts/storage_format_guard.py | sudo tar -x -f - --no-same-owner -C "$d" && '
            'sudo python3 -I -B "$d/deploy/rollback-helpers/install_helpers.py" '
            '<the same arguments> (then remove "$d" as root); '
            "or just `make install-rollback-helpers APPLY=1`, which does all of this"
        )
    rh = load("rollback_helper", HERE / "rollback_helper.py")
    guard = load("storage_format_guard", REPO / "scripts" / "storage_format_guard.py")
    steps, sudoers = plan(a, rh, guard)
    root = a.root.rstrip("/")
    sudoers_dir = f"{root}/etc/sudoers.d"
    sudoers_path = f"{sudoers_dir}/{SUDOERS_NAME}"
    # Everything is decided, and every parent checked, before anything is written.
    for s in steps:
        if s.kind == "dir":
            check_parents(root, s.path, include_target=False)
    check_parents(root, sudoers_dir, include_target=True)
    todo = [(s, state(s)) for s in steps]
    sudo_state = sudoers_state(sudoers_path, sudoers)
    conf_step = next(s for s in steps if s.note == "rollback.conf")
    conf_state = state(conf_step)
    refuse_conf = conf_state == "replace" and Path(conf_step.path).is_file() and not a.replace_conf
    print(f"Rollback helper bundle, from {REPO}:")
    if bad:
        print(
            f"  NOTICE: {len(bad)} path(s) of this checkout are not root's or are open to others "
            f"(first: {bad[0]}). Running this script with --apply from here would refuse; "
            "`make install-rollback-helpers APPLY=1` exports HEAD to a root-owned directory first."
        )
    for s, st in todo:
        print(f"  {st:<9} {s.path}" + (f"  ({s.note})" if s.note else ""))
    print(f"  {sudo_state:<9} {sudoers_path}  (sudoers drop-in, checked with visudo -c first)")
    print(f"\nSudoers grantee: {a.deploy_user} (may run the helpers as root, nothing else).")
    if sudo_state == "unchanged":
        print("The installed sudoers drop-in is already this one.")
    elif sudo_state == "unreadable":
        print(
            f"{sudoers_path} is root-only (mode 0440), so this run cannot compare it. APPLY=1 "
            "compares it as root and rewrites it only if it differs. The rendered text:"
            f"\n{sudoers}"
        )
    elif os.path.isfile(sudoers_path) and not os.path.islink(sudoers_path):
        print(f"Change to {sudoers_path}:")
        print(
            "\n".join(
                difflib.unified_diff(
                    Path(sudoers_path).read_text().splitlines(),
                    sudoers.splitlines(),
                    "installed",
                    "rendered",
                    lineterm="",
                )
            )
        )
    else:
        print(f"Rendered {sudoers_path}:\n{sudoers}")
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
        os.execvp("sudo", sudo_argv())  # noqa: S606 (argv)
    chown = os.geteuid() == 0
    # The sudoers text is checked first: a drop-in visudo refuses stops the
    # install before a single file is written.
    # (right text with a wrong owner or mode is `fix`: rewritten, not trusted)
    tmp = validate_sudoers(a.visudo, sudoers, sudoers_dir) if sudo_state != "unchanged" else ""
    for s, st in todo:
        if st == "unchanged" or (s is conf_step and refuse_conf):
            continue
        if s.kind == "dir":
            ensure_dir(s.path, s.mode)
            fix_dir(s.path, s.mode, chown)
            print(f"  {'made' if st == 'create' else 'fixed'} {s.path}")
            continue
        ensure_dir(os.path.dirname(s.path), 0o755)
        write_atomic(s.path, s.data, s.mode, chown)
        print(f"  wrote {s.path}")
    if tmp:
        try:
            old = Path(sudoers_path).read_bytes() if os.path.isfile(sudoers_path) else None
        except PermissionError:
            os.unlink(tmp)
            raise SystemExit(
                f"install_helpers: cannot read {sudoers_path}, so it cannot be put back if the "
                "whole-config check refuses the new one, so the drop-in was left as it is (run as root)"
            )
        if chown:
            os.chown(tmp, 0, 0)
        os.replace(tmp, sudoers_path)
        _sync_dir(sudoers_dir)
        why = check_whole_config(a.visudo)
        if why:
            # In context the drop-in breaks the sudoers configuration: put back
            # what was there (or nothing), and say so.
            if old is None:
                os.unlink(sudoers_path)
            else:
                write_atomic(sudoers_path, old, SUDOERS_MODE, chown)
            raise SystemExit(
                f"install_helpers: `visudo -c` over the whole configuration refused it after the "
                f"drop-in was moved into place, so {sudoers_path} was "
                f"{'removed' if old is None else 'put back as it was'} ({why})"
            )
        print(f"  wrote {sudoers_path}")
    print(
        "\nInstalled. Still yours: host.conf (the unit drop-in, written once; see "
        "docs/system/deploy-and-gpu-ops.md), then sync Makefile.local from Makefile.local.example. "
        "`make rollback-check` shows what the host keeps."
    )
    return 1 if refuse_conf else 0


if __name__ == "__main__":
    sys.exit(main())
