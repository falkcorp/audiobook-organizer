#!/usr/bin/python3 -I
# file: deploy/rollback-helpers/rollback_helper.py
# version: 1.13.0
# guid: 1d8f47fb-8f03-4fc9-af45-4b16e6ef75aa
# last-edited: 2026-11-18

"""Root-owned helpers for `make deploy`, `make rollback` and `make rollback-restore` (R1-27).

The deploy user runs each operation through `sudo -n` and one stub per
operation in LIBDIR (see STUBS). sudoers grants each stub with no argument
list, which in sudoers means ANY arguments, so the deploy user chooses every
argument. This file therefore trusts no argument: each operation takes a
fixed shape, refuses anything else before it runs a thing (exit 2), builds
the argv of what it runs itself, and never goes through a shell. Every path
it touches comes from CONFIG, a root-owned file the owner writes once:

    bin = /usr/local/bin/audiobook-organizer
    db = /path/to/audiobooks.pebble
    state_dir = /var/lib/audiobook-organizer
    unit = audiobook-organizer.service
    service_user = audiobook
    archive_dir = /path/to/archives
    checkpoint_dir =                       (optional; core.storage.checkpoint_dir)
    offline_env = HOME=/var/lib/audiobook-organizer ABK_STATE_DIR=/var/lib/audiobook-organizer
    service_group = audiobook             (optional; defaults to service_user)
    stage_bin = /home/USER/audiobook-organizer                    (optional)
    stage_local_conf = /home/USER/audiobook-organizer-local.conf  (optional)

CONFIG and its directory must be owned by root (or by the user running the
helper) and not writable by group or others, or every operation refuses.

Operations (the stub name, then the subcommand):

  inflight-probe     `probe`, no arguments, as root. Runs the in-flight probe
                     of 05-storage §5S.3.7, literally `find <state_dir>
                     -maxdepth 3 -path '<state_dir>/restore/*/inflight.json'
                     -print`, and passes its output and status through.
  checkpoint-record  `record NAME`, as the SERVICE USER (sudoers `(audiobook)`).
                     NAME must match the checkpoint grammar
                     (05-storage §5S.3.12 "Where"); prints the record
                     `<checkpoint dir>/.mediacore-checkpoints/NAME.json`, opened
                     without following a symlink, if it is JSON under 1 MiB.
  restore-offline    `restore checkpoint NAME`, `restore archive BASENAME` or
                     `restore into-check BASENAME`, as root. Runs the current
                     binary's `storage restore-offline` as service_user in a
                     transient unit (systemd-run --uid, unit TRANSIENT_UNIT),
                     with offline_env, its output appended to RUN_DIR/restore.log
                     and relayed. In place: `--checkpoint NAME --accept-loss` or
                     `--archive <archive_dir>/BASENAME --accept-loss`.
                     into-check runs `_into-worker BASENAME` in that unit
                     instead: it checks free space, extracts the archive into
                     the fixed scratch copy (`--archive ... --into
                     <cp area>/scratch/rollback-restore-levels`), prints the
                     copy's `.storage-levels` between LEVELS-BEGIN and
                     LEVELS-END, and deletes the copy whatever happens. All of
                     that runs as the service user, never as root, so no root
                     process reads or deletes under the service's directories.
                     The unit outlives a dropped ssh connection (its output
                     goes to a file, not to the connection), and its fixed
                     name makes systemd refuse a second one while it runs.
  unit-type          `unit-type simple` or `unit-type notify`, as root. simple
                     writes the fixed drop-in `[Service]\\nType=simple\\n` to
                     /etc/systemd/system/<unit>.d/zz-rollback-type-simple.conf;
                     notify removes it. Then `systemctl daemon-reload`.
  install-staged     `install binary`, `install prev`, `install unit`,
                     `install local-conf` or `install rollback ...`, as root:
                     the deploy and the environment-restoring rollback. binary
                     installs stage_bin as <bin> atomically (copy to
                     <bin>.new, fsync, rename), keeping the replaced one as
                     <bin>.prev. prev installs stage_bin as <bin>.prev the
                     same way and touches nothing else (make rollback-restore
                     RELEASE=<tag>, once it has fetched that release into
                     stage_bin and checked its store levels). systemd reads the unit
                     and its drop-ins as root, so neither takes free-form
                     content from the deploy user: unit installs
                     LIBDIR/UNIT_TEMPLATE, the copy of
                     deploy/audiobook-organizer.service the owner installed
                     root-owned with the helpers, as /etc/systemd/system/<unit>
                     (after checking it names bin, service_user and
                     service_group); local-conf renders
                     /etc/systemd/system/<unit>.d/local.conf itself from the
                     Environment= lines of stage_local_conf, each one
                     assignment in a strict grammar (render_local_conf), so
                     systemd reads only lines this helper wrote. Both then
                     `systemctl daemon-reload`. A staged file is reached with
                     no symlink in any path component, must be a regular file
                     owned by the deploy user (SUDO_UID) with one link, and
                     is read once.
                     The service ENVIRONMENT is part of a rollback, kept per
                     BINARY: a deploy that replaces a binary keeps the unit and
                     local.conf drop-in it ran under as one generation file
                     <bin>.envgen/<sha256 of that binary>.json (directory 0700,
                     file 0600, written atomically, never printed). `install
                     unit` and `install local-conf` capture the file they are
                     about to replace into <bin>.envgen/pending.json (the first
                     capture of a deploy wins, so a rerun after a half-done
                     deploy keeps the real previous environment); `install
                     binary` writes <bin>.new first, and only if it differs
                     from the installed binary keeps that one as <bin>.prev
                     and commits the capture as its generation (a deploy that
                     never captured one takes the environment as it stands:
                     it did not change it), then renames <bin>.new last; the
                     capture is deleted only after that rename. The same bytes
                     again rotate nothing. At least GEN_KEEP generations are
                     kept, plus every one whose binary a deploy replaced
                     across a store level raise or an `enforced` change
                     (never evicted), plus the newest GEN_KEEP_PINNED of those
                     replaced without the facts to tell. `install binary` takes either nothing or
                     `SHA256 VERSION LEVELS ENFORCED`, what the deploy recipe
                     measured about the staged binary as the deploy user (this
                     helper never runs a binary as root); they are validated,
                     displayed, and used only for that pin, never to choose a
                     generation. `install rollback list` prints every
                     generation (no content); `install rollback check prev|SHA256
                     [GENERATION]` validates the generation of that binary with
                     the same rules the installs use and prints it, changing
                     nothing; `install rollback apply SHA256 [GENERATION]` is
                     the rollback: it refuses unless <bin>.prev still has the
                     sha256 the recipe checked (a deploy that landed in
                     between), and restores the generation whose binary is
                     that sha256, or the one the operator named by its sha256
                     (a release built elsewhere never has the deployed build's
                     hash). It stages the binary (copied and hashed in one
                     pass, so a .prev swapped after the check is refused), the
                     unit and local.conf, and the kept <bin>.rolled-back copy
                     (skipped when <bin> already is the restored binary),
                     then renames all three in a row, the binary last, and
                     `daemon-reload`s. It refuses, changing nothing, when no
                     generation matches or it is malformed, rather than roll
                     back half. Every verb that changes something (install
                     binary|prev|unit|local-conf, rollback apply, swap-prev)
                     holds one lock for its life and refuses, changing
                     nothing, if another holds it.
  pprof              `pprof on` or `pprof off`, as root. on writes the fixed
                     drop-in `[Service]\\nEnvironment=ABK_PPROF_ADDR=localhost:6060\\n`
                     to /etc/systemd/system/<unit>.d/pprof.conf; off removes
                     it. Then `systemctl daemon-reload`.
  swap-prev          `swap-prev`, no arguments, as root: the BINARY only, kept
                     as the manual emergency path (the recipes use `install
                     rollback apply`, which restores the environment too).
                     Keeps <bin> as
                     <bin>.rolled-back and installs <bin>.prev as <bin>
                     atomically (copy to <bin>.new, then rename), which also
                     avoids ETXTBSY on the running executable.

Exit 0 done; 1 refused or failed (the message says which step); 2 bad
arguments. Standard library only. Tests: scripts/tests/test_rollback_helper.py.
Install: docs/system/deploy-and-gpu-ops.md ("Rollback helpers").
"""

from __future__ import annotations

import contextlib
import fcntl
import hashlib
import json
import os
import re
import shutil
import stat
import subprocess
import sys
import tarfile
import time

CONFIG = "/etc/audiobook-organizer/rollback.conf"
LIBDIR = "/usr/local/libexec/audiobook-organizer/rollback"
SYSTEMD_DIR = "/etc/systemd/system"
RUN_DIR = "/run/audiobook-organizer-rollback"
TOOLS = {
    "find": "/usr/bin/find",
    "systemctl": "/usr/bin/systemctl",
    "systemd-run": "/usr/bin/systemd-run",
    "python3": "/usr/bin/python3",
}
TRANSIENT_UNIT = "audiobook-organizer-rollback-restore"
# The last line _report writes into the restore log; scripts/
# storage_format_guard.py carries the same string for the recovery text.
RESTORE_DONE = "rollback helper: restore-offline finished: "
INTO_NAME = "rollback-restore-levels"
DROPIN = "zz-rollback-type-simple.conf"
DROPIN_TEXT = "[Service]\nType=simple\n"
PPROF_DROPIN = "pprof.conf"
PPROF_TEXT = "[Service]\nEnvironment=ABK_PPROF_ADDR=localhost:6060\n"
UNIT_LIMIT = 256 << 10
UNIT_TEMPLATE = "audiobook-organizer.service"
RECORD_LIMIT = 1 << 20
# stub file name -> subcommand. Each stub is `exec /usr/bin/python3 -I
# LIBDIR/rollback_helper.py <subcommand> "$@"`.
STUBS = {
    "inflight-probe": "probe",
    "checkpoint-record": "record",
    "restore-offline": "restore",
    "unit-type": "unit-type",
    "swap-prev": "swap-prev",
    "install-staged": "install",
    "pprof": "pprof",
}

_NAME = re.compile(r"[0-9]{8}T[0-9]{6}Z-[0-9a-f]{8}")
_BASENAME = re.compile(r"[A-Za-z0-9_][A-Za-z0-9._-]{0,199}")
_PATH = re.compile(r"/[A-Za-z0-9._/@+-]+")
_USER = re.compile(r"[a-z_][a-z0-9_-]{0,31}")
_UNIT = re.compile(r"[A-Za-z0-9@._-]+\.service")
_ENV = re.compile(r"(HOME|ABK_STATE_DIR|ABK_[A-Z0-9_]+)=[A-Za-z0-9._/@+:-]*")
_KEYS = {
    "bin": True,
    "db": True,
    "state_dir": True,
    "unit": True,
    "service_user": True,
    "archive_dir": True,
    "checkpoint_dir": False,
    "offline_env": False,
    "service_group": False,
    "stage_bin": False,
    "stage_local_conf": False,
}


class RefusedError(Exception):
    """A refusal: message, exit status."""

    def __init__(self, message: str, code: int = 1):
        super().__init__(message)
        self.code = code


def _owner_ok(st: os.stat_result) -> bool:
    return st.st_uid in (0, os.geteuid()) and not st.st_mode & 0o022


def load_config() -> dict[str, str]:
    """Read and validate CONFIG; raise RefusedError on any problem."""
    d = os.path.dirname(CONFIG)
    try:
        dst = os.stat(d)
        fd = os.open(CONFIG, os.O_RDONLY | os.O_NOFOLLOW)
    except OSError as e:
        raise RefusedError(
            f"cannot read {CONFIG}: {e.strerror}. Install the rollback helpers' config first."
        ) from e
    try:
        st = os.fstat(fd)
        if not stat.S_ISREG(st.st_mode) or st.st_size > 65536:
            raise RefusedError(f"{CONFIG} is not a small regular file")
        if not _owner_ok(st) or not _owner_ok(dst):
            raise RefusedError(
                f"{CONFIG} or its directory is not root-owned, or is group or world writable"
            )
        text = _read_all(fd, 65537).decode("utf-8", "strict")
    finally:
        os.close(fd)
    cfg: dict[str, str] = {}
    for n, raw in enumerate(text.splitlines(), 1):
        line = raw.strip()
        if not line or line.startswith("#"):
            continue
        key, sep, value = line.partition("=")
        key, value = key.strip(), value.strip()
        if not sep or key not in _KEYS:
            raise RefusedError(f"{CONFIG} line {n}: unknown setting {key!r}")
        if key in cfg:
            raise RefusedError(f"{CONFIG} line {n}: {key} is set twice")
        cfg[key] = value
    for key, required in _KEYS.items():
        if required and not cfg.get(key):
            raise RefusedError(f"{CONFIG}: {key} is not set")
    for key in (
        "bin",
        "db",
        "state_dir",
        "archive_dir",
        "checkpoint_dir",
        "stage_bin",
        "stage_local_conf",
    ):
        v = cfg.get(key, "")
        if not v:
            continue
        if not _PATH.fullmatch(v) or ".." in v.split("/") or v != v.rstrip("/") and v != "/":
            raise RefusedError(
                f"{CONFIG}: {key} {v!r} must be an absolute path of letters, digits and ._/@+-"
            )
    if not _USER.fullmatch(cfg["service_user"]) or cfg["service_user"] == "root":
        raise RefusedError(
            f"{CONFIG}: service_user {cfg['service_user']!r} must be the service's own user, not root"
        )
    group = cfg.setdefault("service_group", "") or cfg["service_user"]
    if not _USER.fullmatch(group) or group == "root":
        raise RefusedError(
            f"{CONFIG}: service_group {group!r} must be the service's own group, not root"
        )
    cfg["service_group"] = group
    if not _UNIT.fullmatch(cfg["unit"]):
        raise RefusedError(f"{CONFIG}: unit {cfg['unit']!r} is not a .service unit name")
    for tok in cfg.get("offline_env", "").split():
        if not _ENV.fullmatch(tok):
            raise RefusedError(
                f"{CONFIG}: offline_env token {tok!r} is not HOME=, ABK_STATE_DIR= or ABK_*="
            )
    return cfg


def cp_area(cfg: dict[str, str]) -> str:
    return (cfg.get("checkpoint_dir") or os.path.dirname(cfg["db"])) + "/.mediacore-checkpoints"


def into_path(cfg: dict[str, str]) -> str:
    return f"{cp_area(cfg)}/scratch/{INTO_NAME}"


def _arity(args: list[str], n: int, usage: str) -> None:
    if len(args) != n:
        raise RefusedError(f"usage: {usage}", 2)


def _match(rx: re.Pattern, value: str, what: str) -> str:
    if not rx.fullmatch(value) or ".." in value:
        raise RefusedError(f"{what} {value!r} is refused", 2)
    return value


def op_probe(args: list[str]) -> int:
    _arity(args, 0, "inflight-probe (no arguments)")
    sd = load_config()["state_dir"]
    argv = [TOOLS["find"], sd, "-maxdepth", "3", "-path", f"{sd}/restore/*/inflight.json", "-print"]
    return subprocess.run(argv, check=False).returncode


def op_record(args: list[str]) -> int:
    _arity(args, 1, "checkpoint-record NAME")
    name = _match(_NAME, args[0], "checkpoint name")
    cfg = load_config()
    path = f"{cp_area(cfg)}/{name}.json"
    try:
        fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    except OSError as e:
        raise RefusedError(f"cannot open {path}: {e.strerror}") from e
    try:
        st = os.fstat(fd)
        if not stat.S_ISREG(st.st_mode) or st.st_size > RECORD_LIMIT:
            raise RefusedError(f"{path} is not a regular file under {RECORD_LIMIT} bytes")
        data = _read_all(fd, RECORD_LIMIT + 1)
    finally:
        os.close(fd)
    try:
        doc = json.loads(data.decode("utf-8"))
    except (UnicodeDecodeError, ValueError) as e:
        raise RefusedError(f"{path} is not JSON") from e
    if not isinstance(doc, dict):
        raise RefusedError(f"{path} is not a JSON object")
    print(json.dumps(doc))
    return 0


def _transient_state() -> str:
    p = subprocess.run(  # noqa: S603 (fixed argv)
        [TOOLS["systemctl"], "show", "-p", "ActiveState", "--value", f"{TRANSIENT_UNIT}.service"],
        capture_output=True,
        text=True,
        check=False,
    )
    return p.stdout.strip() if p.returncode == 0 else ""


def _deploy_gid() -> int:
    """The primary group of the user who ran sudo (SUDO_GID), or this
    process's group when run without sudo."""
    v = os.environ.get("SUDO_GID", "")
    return int(v) if v.isdigit() else os.getgid()


def _open_log(gid: int) -> int:
    """The restore log, opened for append (never truncated: a second run
    must not wipe the output of one still running or just lost to ssh).
    RUN_DIR and the log stay root-owned, because systemd opens `append:` as
    root and a file the deploy user owned could be swapped for a symlink;
    they are group-readable by the deploy user's group so the recovery steps
    need no sudo."""
    os.makedirs(RUN_DIR, mode=0o750, exist_ok=True)
    d = os.open(RUN_DIR, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    try:
        os.fchown(d, -1, gid)
        os.fchmod(d, 0o750)
    finally:
        os.close(d)
    log = f"{RUN_DIR}/restore.log"
    try:
        fd = os.open(log, os.O_WRONLY | os.O_CREAT | os.O_APPEND | os.O_NOFOLLOW, 0o640)
    except OSError as e:
        raise RefusedError(f"cannot open {log} ({e.strerror}); nothing was run") from e
    st = os.fstat(fd)
    if not stat.S_ISREG(st.st_mode) or st.st_nlink != 1:
        os.close(fd)
        raise RefusedError(f"{log} is not a plain file; nothing was run")
    os.fchown(fd, -1, gid)
    os.fchmod(fd, 0o640)
    return fd


def _transient(cfg: dict[str, str], cmd: list[str]) -> int:
    """Run cmd as the service user in the transient unit, relaying its log.

    One restore at a time: a unit still running (or in a state that cannot be
    read) refuses before the log is opened. The unit runs cmd under _report,
    which appends `RESTORE_DONE exit N` to the log when cmd ends, so the
    result is on the host even if this connection (and this process with
    it) is gone. The unit is not --collect'ed: a failed one keeps its Result
    for `systemctl status` until the next run resets it."""
    state = _transient_state()
    if state not in ("inactive", "failed"):
        raise RefusedError(
            f"{TRANSIENT_UNIT}.service is {state or 'in a state that cannot be read'}: a restore may still be "
            f"running (systemctl status {TRANSIENT_UNIT}). Nothing was started and its log is untouched."
        )
    if state == "failed":
        subprocess.run([TOOLS["systemctl"], "reset-failed", TRANSIENT_UNIT], check=False)  # noqa: S603
    fd = _open_log(_deploy_gid())
    log = f"{RUN_DIR}/restore.log"
    try:
        stamp = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())
        os.write(fd, f"=== {TRANSIENT_UNIT} started {stamp} ===\n".encode())
        pos = os.fstat(fd).st_size
    finally:
        os.close(fd)
    argv = [
        TOOLS["systemd-run"],
        "--wait",
        "--quiet",
        f"--unit={TRANSIENT_UNIT}",
        "--service-type=exec",
        f"--uid={cfg['service_user']}",
        f"--property=StandardOutput=append:{log}",
        f"--property=StandardError=append:{log}",
    ]
    argv += [f"--setenv={tok}" for tok in cfg.get("offline_env", "").split()]
    argv += ["--", TOOLS["python3"], "-I", os.path.abspath(__file__), "_report", "--", *cmd]
    print(
        f"rollback helper: running as {cfg['service_user']} in {TRANSIENT_UNIT}.service; if this connection drops "
        f"it keeps running: `systemctl status {TRANSIENT_UNIT}`, output in {log}, whose last line reads "
        f"`{RESTORE_DONE}exit N` once it has ended.",
        file=sys.stderr,
    )
    proc = subprocess.Popen(argv)  # noqa: S603 (fixed argv built above)
    while True:
        done = proc.poll() is not None
        with open(log, "rb") as f:
            f.seek(pos)
            chunk = f.read()
        if chunk:
            pos += len(chunk)
            sys.stdout.write(chunk.decode("utf-8", "replace"))
            sys.stdout.flush()
        if done:
            return proc.returncode
        time.sleep(0.2)


def op_report(args: list[str]) -> int:
    """Run in the transient unit as the service user (never through sudo):
    run the command after `--`, then print RESTORE_DONE and its exit status
    or signal as the log's last line, and exit with that status."""
    if len(args) < 2 or args[0] != "--":
        raise RefusedError("usage: _report -- COMMAND...", 2)
    try:
        rc = subprocess.run(args[1:], check=False).returncode  # noqa: S603 (the unit's own argv)
    except OSError as e:
        print(f"rollback helper: cannot run {args[1]}: {e.strerror}", file=sys.stderr, flush=True)
        print(f"{RESTORE_DONE}exit 127 (could not start)", flush=True)
        return 127
    word = f"exit {rc}" if rc >= 0 else f"signal {-rc}"
    print(f"{RESTORE_DONE}{word}", flush=True)
    return rc if rc >= 0 else 128 - rc


def op_restore(args: list[str]) -> int:
    usage = "restore-offline checkpoint NAME | archive BASENAME | into-check BASENAME"
    _arity(args, 2, usage)
    mode, value = args
    if mode == "checkpoint":
        source = ["--checkpoint", _match(_NAME, value, "checkpoint name"), "--accept-loss"]
    elif mode in ("archive", "into-check"):
        _match(_BASENAME, value, "archive name")
        source = []
    else:
        raise RefusedError(f"usage: {usage}", 2)
    cfg = load_config()
    if mode == "into-check":
        cmd = [TOOLS["python3"], "-I", os.path.abspath(__file__), "_into-worker", value]
    else:
        if mode == "archive":
            source = ["--archive", f"{cfg['archive_dir']}/{value}", "--accept-loss"]
        cmd = [cfg["bin"], "storage", "restore-offline", "--db", cfg["db"], *source]
    return _transient(cfg, cmd)


def free_bytes(path: str) -> int:
    return shutil.disk_usage(path).free


def _needed(archive: str) -> tuple[int, str]:
    """Bytes the extracted store needs, and how that was worked out."""
    try:
        with tarfile.open(archive, "r:*") as tf:
            total = sum(m.size for m in tf if m.isfile())
        return int(total * 1.1) + (64 << 20), "the archive's members plus 10% and 64 MiB"
    except (tarfile.TarError, OSError, EOFError):
        return os.path.getsize(archive) * 4 + (
            64 << 20
        ), "four times the archive's size (not a readable tar)"


def _clean_scratch(into: str) -> None:
    for p in (into, f"{into}.state"):
        if os.path.lexists(p):
            if os.path.isdir(p) and not os.path.islink(p):
                shutil.rmtree(p)
            else:
                os.unlink(p)
    for suffix in (".storage-levels", ".storage-format"):
        if os.path.lexists(into + suffix):
            os.unlink(into + suffix)


def op_into_worker(args: list[str]) -> int:
    """into-check's body, run as the service user inside the transient unit."""
    _arity(args, 1, "_into-worker BASENAME")
    base = _match(_BASENAME, args[0], "archive name")
    cfg = load_config()
    archive = f"{cfg['archive_dir']}/{base}"
    into = into_path(cfg)
    scratch = os.path.dirname(into)
    os.makedirs(scratch, mode=0o700, exist_ok=True)
    _clean_scratch(into)
    try:
        if not os.path.isfile(archive):
            raise RefusedError(f"no archive {archive}")
        need, how = _needed(archive)
        have = free_bytes(scratch)
        if have < need:
            raise RefusedError(
                f"{scratch} has {have} bytes free and the scratch copy needs about {need} ({how}); free space "
                "there first"
            )
        argv = [
            cfg["bin"],
            "storage",
            "restore-offline",
            "--db",
            cfg["db"],
            "--archive",
            archive,
            "--into",
            into,
        ]
        rc = subprocess.run(argv, check=False).returncode
        if rc != 0:
            raise RefusedError(
                f"`storage restore-offline --archive {archive} --into {into}` exited {rc}"
            )
        with open(f"{into}.storage-levels", encoding="utf-8") as f:
            levels = f.read()
        print("LEVELS-BEGIN")
        print(levels.rstrip("\n"))
        print("LEVELS-END")
        return 0
    finally:
        _clean_scratch(into)


def _fixed_dropin(name: str, text: str | None, what: str) -> None:
    """Write the fixed drop-in `name` (text) or remove it (None), then
    daemon-reload."""
    cfg = load_config()
    d = f"{SYSTEMD_DIR}/{cfg['unit']}.d"
    path = f"{d}/{name}"
    if text is not None:
        os.makedirs(d, mode=0o755, exist_ok=True)
        _write_replace(path, text.encode(), 0o644)
        print(f"installed {path} ({what})")
    else:
        try:
            os.unlink(path)
            print(f"removed {path} ({what})")
        except FileNotFoundError:
            print(f"{path} is absent ({what})")
    _daemon_reload()


def _daemon_reload() -> None:
    rc = subprocess.run([TOOLS["systemctl"], "daemon-reload"], check=False).returncode
    if rc != 0:
        raise RefusedError(f"systemctl daemon-reload exited {rc}")


def _sync_dir(path: str) -> None:
    """fsync the directory holding path, so a rename into it survives a crash.
    The rename has already happened, so a failure is a warning, not a refusal."""
    d = os.path.dirname(path) or "."
    try:
        dfd = os.open(d, os.O_RDONLY | os.O_DIRECTORY)
        try:
            os.fsync(dfd)
        finally:
            os.close(dfd)
    except OSError as e:
        print(f"warning: {path} is in place but {d} could not be synced ({e})", file=sys.stderr)


def _copy_hashed(src: str, dst: str) -> str:
    """Copy the regular file src to dst (new, 0o700 until the mode is set, fsynced), opened
    without following a symlink; the sha256 of the bytes that were copied.
    The caller compares it to the hash it expected, so a file swapped between
    its own hash and this copy is caught on the copy itself."""
    sfd = os.open(src, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    try:
        st = os.fstat(sfd)
        if not stat.S_ISREG(st.st_mode):
            raise OSError(f"{src} is not a regular file")
        with contextlib.suppress(FileNotFoundError):
            os.unlink(dst)
        dfd = os.open(dst, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o700)
        try:
            h = hashlib.sha256()
            while True:
                chunk = os.read(sfd, 1 << 20)
                if not chunk:
                    break
                h.update(chunk)
                view = memoryview(chunk)
                while view:
                    view = view[os.write(dfd, view) :]
            os.fchmod(dfd, stat.S_IMODE(st.st_mode))
            os.fsync(dfd)
            return h.hexdigest()
        finally:
            os.close(dfd)
    finally:
        os.close(sfd)


@contextlib.contextmanager
def _helper_lock(cfg: dict[str, str]):
    """One install, rollback or swap at a time, across every verb that changes
    the binary, .prev, the unit, local.conf or the generations. The lock is a
    file in the generation directory (root-only), held for the verb's life
    and released by the kernel if the helper dies. A second caller is refused
    rather than queued: a deploy and a rollback running together is an
    operator error to look at, not to wait out."""
    _ensure_env_dir(cfg)
    path = f"{_env_dir(cfg)}/lock"
    try:
        fd = os.open(path, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
    except OSError as e:
        raise RefusedError(
            f"cannot open the helper lock {path} ({e.strerror}); nothing changed"
        ) from e
    try:
        try:
            fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except OSError as e:
            raise RefusedError(
                "another install or rollback is running (the helper lock is held); nothing changed. "
                "Wait for it to finish, then run this again"
            ) from e
        yield
    finally:
        os.close(fd)


def _write_replace(path: str, data: bytes, mode: int) -> None:
    """Write data to path atomically: a temporary file beside it, fsync, rename."""
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
        os.replace(tmp, path)
    except BaseException:
        with contextlib.suppress(OSError):
            os.unlink(tmp)
        raise
    _sync_dir(path)


def op_unit_type(args: list[str]) -> int:
    _arity(args, 1, "unit-type simple|notify")
    kind = args[0]
    if kind not in ("simple", "notify"):
        raise RefusedError("usage: unit-type simple|notify", 2)
    _fixed_dropin(DROPIN, DROPIN_TEXT if kind == "simple" else None, f"Type={kind}")
    return 0


def op_pprof(args: list[str]) -> int:
    _arity(args, 1, "pprof on|off")
    if args[0] not in ("on", "off"):
        raise RefusedError("usage: pprof on|off", 2)
    on = args[0] == "on"
    _fixed_dropin(PPROF_DROPIN, PPROF_TEXT if on else None, f"pprof {args[0]}")
    return 0


# systemd's own whitespace inside a line is " \t". Python's str.strip(),
# split() and splitlines() also take \v, \f, \x1c-\x1f, \x85, U+2028 and
# more for whitespace or line breaks, which systemd does not. The rendered
# local.conf holds only lines this helper writes, so a differential in how
# the staged file is read cannot reach systemd; still, a deployed line must
# be printable ASCII or tab, lines split on \n alone, and only " \t" is
# stripped, so what is checked is exactly what is written.
_WS = " \t"
_DIRECTIVE_CHARS = re.compile(r"[\t\x20-\x7e]*")
# One assignment per Environment= line: KEY=value with no space, quote or
# backslash in the value, or the whole assignment in double or single
# quotes with no matching quote or backslash inside. No backslash anywhere:
# no escapes, no continuation.
_ENV_KEY = r"[A-Za-z_][A-Za-z0-9_]*"
_ENV_ASSIGNMENT = re.compile(
    rf"{_ENV_KEY}=[^\s\"'\\]*|\"{_ENV_KEY}=[^\"\\]*\"|'{_ENV_KEY}=[^'\\]*'"
)


# Variables that make the dynamic loader, libc, an interpreter or the Go
# runtime load or run something else, or weaken its checks: refused in
# local.conf even though every Environment= line reaches only the service
# user (defence in depth behind _check_rules). Compared upper-cased.
# GODEBUG is refused too: it can switch off Go's TLS and x509 checks, and no
# deployed local.conf uses it.
_ENV_DENIED = frozenset(
    {
        "GCONV_PATH",
        "GETCONF_DIR",
        "HOSTALIASES",
        "LOCALDOMAIN",
        "RES_OPTIONS",
        "NLSPATH",
        "NODE_OPTIONS",
        "GODEBUG",
        "TZDIR",
        "GLIBC_TUNABLES",
        "BASH_ENV",
        "SSL_CERT_FILE",
        "SSL_CERT_DIR",
        "LOCPATH",
    }
)
_ENV_DENIED_PREFIXES = ("LD_", "MALLOC_", "PYTHON", "PERL5")


def _env_key_denied(assignment: str) -> bool:
    key = assignment.lstrip("\"'").partition("=")[0].upper()
    return key in _ENV_DENIED or key.startswith(_ENV_DENIED_PREFIXES)


def render_local_conf(text: str, source: str) -> str:
    """The local.conf drop-in to install, rendered from the staged file:
    `[Service]` and its `Environment=` lines (logical lines, joined as
    systemd joins them), each re-written as is after it matches
    _ENV_ASSIGNMENT. Comment and blank lines are dropped; anything
    else refuses (by line number: a refusal never prints the file). Settings
    other than Environment= (ExecStart=, WorkingDirectory=, MemoryMax=, ...)
    decide what runs and as whom, so they live in host.conf, which the owner
    installs as root and deploy never touches."""
    if "\r" in text or "\0" in text:
        raise RefusedError("carriage returns and NUL bytes are refused")
    lines: list[str] = []
    section = False
    # Logical lines, as systemd would read the staged file (_logical_lines):
    # comments and blank lines are dropped there, and a value continued by
    # a trailing backslash is checked, and rendered, as the one line it is.
    for n, joined in _logical_lines(text, source):
        s = joined.strip(_WS)
        if s == "[Service]":
            section = True
            continue
        if s.startswith("Environment=") and section:
            value = s[len("Environment=") :]
            if _ENV_ASSIGNMENT.fullmatch(value):
                if _env_key_denied(value):
                    raise RefusedError(
                        f"line {n}: a loader or runtime-injection variable (LD_*, MALLOC_*, PYTHON*, "
                        "PERL5*, GCONV_PATH, GLIBC_TUNABLES, GODEBUG, NODE_OPTIONS, ...) is refused"
                    )
                lines.append(f"Environment={value}\n")
                continue
            raise RefusedError(
                f"line {n}: an Environment= line must hold one KEY=value assignment, unquoted "
                "with no spaces, or wholly in double or single quotes, with no backslash"
            )
        raise RefusedError(
            f"line {n}: only [Service] and its Environment= lines are deployed in local.conf; "
            "put any other setting in host.conf, which the owner installs as root "
            "(docs/system/deploy-and-gpu-ops.md)"
        )
    return (
        f"# Rendered by rollback_helper.py (install-staged local-conf) from {source}:\n"
        "# only its Environment= lines. Edit that file and deploy again.\n"
        "[Service]\n" + "".join(lines)
    )


HOST_CONF = "host.conf"
# The system unit directories systemd reads drop-ins from besides
# SYSTEMD_DIR (systemd.unit(5), "Unit File Load Path"). /lib/systemd/system
# is the same directory as /usr/lib/systemd/system on a merged-/usr host.
UNIT_DIRS = (
    "/etc/systemd/system.control",
    "/run/systemd/system.control",
    "/run/systemd/transient",
    "/run/systemd/generator.early",
    "/etc/systemd/system.attached",
    "/run/systemd/system",
    "/run/systemd/system.attached",
    "/run/systemd/generator",
    "/usr/local/lib/systemd/system",
    "/usr/lib/systemd/system",
    "/lib/systemd/system",
    "/run/systemd/generator.late",
)
# Where the unit's main file (its fragment) may live besides SYSTEMD_DIR:
# the unit directories the owner or a package writes. A copy in a generator,
# transient, .control or .attached directory was made by something else and
# refuses.
FRAGMENT_DIRS = (
    "/run/systemd/system",
    "/usr/local/lib/systemd/system",
    "/usr/lib/systemd/system",
    "/lib/systemd/system",
)
# Settings no drop-in may carry. Every Environment= line local.conf deploys
# reaches the processes the unit runs; that is safe only while those are the
# service user's own ExecStart (whose code the deploy user already controls
# through the binary). A second command, a `+`/`!` prefix,
# PermissionsStartOnly=, another User=/Group= or ambient capabilities would
# hand those variables (LD_PRELOAD=, ...) to something with more privilege.
_HOST_REFUSED = ("PermissionsStartOnly", "AmbientCapabilities")
_WHY_RULES = (
    "every drop-in of the unit may run only the service user's ExecStart, because every "
    "Environment= line local.conf deploys reaches what it runs"
)


def _read_root_conf(path: str) -> str:
    """A unit file or drop-in systemd reads as root: no symlink, a regular
    file owned by root and not writable by group or others, at most
    UNIT_LIMIT bytes, UTF-8 with no CR or NUL. Raises FileNotFoundError when
    it does not exist; any other problem refuses."""
    try:
        fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    except FileNotFoundError:
        raise
    except OSError as e:
        raise RefusedError(
            f"cannot open {path} ({e.strerror}; a symlink is refused); nothing installed"
        ) from e
    try:
        st = os.fstat(fd)
        if not stat.S_ISREG(st.st_mode) or not _owner_ok(st) or st.st_size > UNIT_LIMIT:
            raise RefusedError(
                f"{path} must be a regular file of at most {UNIT_LIMIT} bytes, owned by root and "
                "not writable by group or others; nothing installed"
            )
        data = _read_all(fd, UNIT_LIMIT + 1)
    finally:
        os.close(fd)
    try:
        text = data.decode("utf-8", "strict")
    except UnicodeDecodeError as e:
        raise RefusedError(f"{path} is not UTF-8; nothing installed") from e
    if "\r" in text or "\0" in text:
        raise RefusedError(f"{path}: carriage returns and NUL bytes are refused; nothing installed")
    return text


def _logical_lines(text: str, path: str) -> list[tuple[int, str]]:
    """The directive lines systemd reads, joined the way its parser joins
    them (src/shared/conf-parser.c): leading whitespace is skipped; a line
    continues only when it ends in an ODD number of backslashes, the last
    of which becomes a space; comment lines inside a continuation are
    skipped; a continuation still open at the end of the file is a line.
    Returns (first physical line number, joined text). Every physical line
    that is not a comment must be printable ASCII and tabs."""
    out: list[tuple[int, str]] = []
    cont: str | None = None
    start = 0
    for n, raw in enumerate(text.split("\n"), 1):
        line = raw.lstrip(_WS)
        if line[:1] in ("#", ";"):
            continue  # a comment, inside a continuation or not
        if cont is None and not line:
            continue
        if not _DIRECTIVE_CHARS.fullmatch(raw):
            raise RefusedError(
                f"{path} line {n}: only printable ASCII and tabs are allowed outside comments; "
                "nothing installed"
            )
        backslashes = len(line) - len(line.rstrip("\\"))
        if backslashes % 2 == 1:
            if cont is None:
                cont, start = "", n
            cont += line[:-1] + " "
            continue
        if cont is None:
            out.append((n, line))
        else:
            out.append((start, cont + line))
            cont = None
    if cont is not None:
        out.append((start, cont))
    return out


# A word systemd reads as a command separator in ExecStart= (`;`), and the
# spellings that are close enough that a parser change could make them one:
# `\;` and a quoted `;` are refused too.
_SEMICOLON_WORD = re.compile(r"""\\?;|";"|';'""")


def _check_rules(text: str, cfg: dict[str, str], path: str) -> None:
    """Refuse a unit file or drop-in that runs anything but the service
    user's ExecStart: another Exec*= line, an ExecStart= whose command does
    not start with an absolute path after only the @, - and : prefixes (so
    no + ! | or anything unknown), an ExecStart= with a `;` word (a second
    command), Type=oneshot (which runs each of them), PermissionsStartOnly=,
    AmbientCapabilities=, or a User=/Group= other than the config's. Lines
    are checked as systemd joins them (_logical_lines). A refusal names the
    line, never its text."""
    want = {"User": cfg["service_user"], "Group": cfg["service_group"]}
    for n, line in _logical_lines(text, path):
        key, sep, value = line.partition("=")
        key, value = key.strip(_WS), value.strip(_WS + "\n\r")
        if not sep:
            continue
        problem = ""
        if key.startswith("Exec") and key != "ExecStart":
            problem = "runs a command other than ExecStart="
        elif key == "ExecStart" and value and not value.lstrip("@-:").startswith("/"):
            problem = (
                "runs ExecStart= with a prefix other than @, - or : (+ and ! run it with "
                "privileges) or without an absolute path"
            )
        elif key == "ExecStart" and any(_SEMICOLON_WORD.fullmatch(w) for w in value.split()):
            problem = (
                "puts a `;` word in ExecStart=, which starts a second command whose prefix "
                "the check never sees"
            )
        elif key == "Type" and value == "oneshot":
            problem = "sets Type=oneshot, which runs every command an ExecStart= line holds"
        elif key in _HOST_REFUSED:
            problem = f"sets {key}="
        elif key in want and value != want[key]:
            problem = f"sets {key}= to something other than service_{key.lower()} in {CONFIG}"
        if problem:
            raise RefusedError(f"{path} line {n} {problem}; {_WHY_RULES}; nothing installed")


def _check_host_conf(cfg: dict[str, str]) -> None:
    """local.conf carries only Environment= lines, so the ExecStart=
    override and the host's other settings live in host.conf beside it,
    which the owner writes as root. Without it the service would restart on
    the base unit's ExecStart (no TLS, the wrong store), so install-staged
    unit and local-conf both refuse, before they copy anything, unless
    host.conf is a root-owned regular file, not writable by group or
    others, that runs nothing but the service user's ExecStart."""
    path = f"{SYSTEMD_DIR}/{cfg['unit']}.d/{HOST_CONF}"
    try:
        text = _read_root_conf(path)
    except FileNotFoundError as e:
        raise RefusedError(
            f"{path} is missing: it holds the ExecStart= override and the host's other settings, "
            "which local.conf no longer carries; without it the service would restart on the base "
            "unit's ExecStart. The owner writes it as root (docs/system/deploy-and-gpu-ops.md, "
            "host.conf); nothing installed"
        ) from e
    _check_rules(text, cfg, path)


def _dropin_dir_names(unit: str) -> list[str]:
    """The drop-in directory names systemd applies to unit: <unit>.d, each
    dash prefix (`foo-bar-baz.service` gives foo-bar-.service.d and
    foo-.service.d) and the type-wide service.d (systemd.unit(5))."""
    base = unit[: -len(".service")]
    parts = base.split("-")
    names = [f"{unit}.d"]
    names += [f"{'-'.join(parts[:i])}-.service.d" for i in range(len(parts) - 1, 0, -1)]
    return [*names, "service.d"]


def _unit_names(cfg: dict[str, str]) -> list[str]:
    """The unit's name and its aliases: what `systemctl show -p Names`
    reports (parsed strictly) and every symlink in a unit directory that
    points at a file named after the unit (an alias not loaded yet)."""
    unit = cfg["unit"]
    names = {unit}
    p = subprocess.run(  # noqa: S603 (fixed argv)
        [TOOLS["systemctl"], "show", "-p", "Names", "--value", unit],
        capture_output=True,
        text=True,
        check=False,
    )
    if p.returncode == 0:
        for tok in p.stdout.strip("\n").split(" "):
            if not tok:
                continue
            if not _UNIT.fullmatch(tok):
                raise RefusedError(
                    f"systemctl show -p Names for {unit} gave a name the helper cannot parse; "
                    "nothing installed"
                )
            names.add(tok)
    for root in (SYSTEMD_DIR, *UNIT_DIRS):
        try:
            entries = os.listdir(root)
        except (FileNotFoundError, NotADirectoryError):
            continue
        for e in entries:
            path = f"{root}/{e}"
            if (
                _UNIT.fullmatch(e)
                and os.path.islink(path)
                and os.path.basename(os.readlink(path)) == unit
            ):
                names.add(e)
    return sorted(names)


def _unit_dropins(cfg: dict[str, str]) -> list[str]:
    """Every drop-in systemd applies to the unit: the ones it reports
    loaded (`systemctl show -p DropInPaths`, parsed strictly) together with
    a scan of every unit directory, since one added after the last
    daemon-reload is not reported yet and both installs reload. A drop-in
    directory must be a real directory owned by root and not writable by
    group or others."""
    p = subprocess.run(  # noqa: S603 (fixed argv)
        [TOOLS["systemctl"], "show", "-p", "DropInPaths", "--value", cfg["unit"]],
        capture_output=True,
        text=True,
        check=False,
    )
    found: set[str] = set()
    if p.returncode == 0:
        for tok in p.stdout.split(" "):
            tok = tok.strip("\n")
            if not tok:
                continue
            if not _PATH.fullmatch(tok) or not tok.endswith(".conf") or "/../" in tok:
                raise RefusedError(
                    f"systemctl show -p DropInPaths for {cfg['unit']} gave a path the helper cannot "
                    "parse; nothing installed"
                )
            found.add(tok)
    dirs = {os.path.dirname(f) for f in found}
    dir_names = sorted({d for n in _unit_names(cfg) for d in _dropin_dir_names(n)})
    for root in (SYSTEMD_DIR, *UNIT_DIRS):
        for name in dir_names:
            d = f"{root}/{name}"
            try:
                dst = os.lstat(d)
            except FileNotFoundError:
                continue
            if not stat.S_ISDIR(dst.st_mode):
                raise RefusedError(f"{d} is not a directory; nothing installed")
            dirs.add(d)
            found.update(f"{d}/{e}" for e in os.listdir(d) if e.endswith(".conf"))
    for d in sorted(dirs):
        dst = os.lstat(d)
        if not stat.S_ISDIR(dst.st_mode) or not _owner_ok(dst):
            raise RefusedError(
                f"{d} must be a directory owned by root and not writable by group or others; "
                "nothing installed"
            )
    return sorted(found)


def _check_unit_fragment(cfg: dict[str, str], installing_unit: bool) -> None:
    """The unit's main file must pass the same rules as its drop-ins and set
    the config's User=/Group= (a unit without User= runs as root).

    The candidates are what `systemctl show -p FragmentPath -p SourcePath`
    reports (parsed strictly) and every copy of the unit file in the unit
    directories (an /etc copy shadows /usr/lib, but each is checked). A
    generated unit (SourcePath set), a FragmentPath outside SYSTEMD_DIR and
    FRAGMENT_DIRS, or a copy in any other directory refuses. With none at
    all it refuses, except for install-staged unit, which is about to write
    the checked template to SYSTEMD_DIR: it also skips the SYSTEMD_DIR copy
    it replaces, so a bad /etc copy never blocks the command that fixes it.
    Every other copy (a shadowed /usr/lib one) must still pass."""
    unit = cfg["unit"]
    allowed = {SYSTEMD_DIR, *FRAGMENT_DIRS}
    p = subprocess.run(  # noqa: S603 (fixed argv)
        [TOOLS["systemctl"], "show", "-p", "FragmentPath", "-p", "SourcePath", unit],
        capture_output=True,
        text=True,
        check=False,
    )
    found: set[str] = set()
    if p.returncode == 0:
        props: dict[str, str] = {}
        for line in p.stdout.split("\n"):
            if not line:
                continue
            key, sep, value = line.partition("=")
            if not sep or key not in ("FragmentPath", "SourcePath") or key in props:
                raise RefusedError(
                    f"systemctl show -p FragmentPath -p SourcePath for {unit} gave an answer the helper "
                    "cannot parse; nothing installed"
                )
            if value and not _PATH.fullmatch(value):
                raise RefusedError(
                    f"systemctl show gave a {key} for {unit} the helper cannot parse; nothing installed"
                )
            props[key] = value
        if props.get("SourcePath"):
            raise RefusedError(
                f"{unit} is generated from another file (SourcePath is set): only a unit file the owner "
                "installed is accepted; nothing installed"
            )
        frag = props.get("FragmentPath", "")
        if frag:
            if os.path.dirname(frag) not in allowed or os.path.basename(frag) != unit:
                raise RefusedError(
                    f"systemctl reports {unit}'s FragmentPath outside the unit directories "
                    f"({', '.join(sorted(allowed))}); nothing installed"
                )
            found.add(frag)
    for root in (SYSTEMD_DIR, *UNIT_DIRS):
        path = f"{root}/{unit}"
        if os.path.lexists(path):
            if root not in allowed:
                raise RefusedError(
                    f"{path} is a copy of the unit in a directory systemd fills itself (a generator, "
                    "transient or attached unit); nothing installed"
                )
            found.add(path)
    etc = f"{SYSTEMD_DIR}/{unit}"
    if not found and not installing_unit:
        raise RefusedError(
            f"no unit file for {unit} was found ({etc} or the other unit directories): install the "
            "unit first (install-staged unit); nothing installed"
        )
    for path in sorted(found):
        if installing_unit and path == etc:
            continue  # the copy install-staged unit replaces with the checked template
        try:
            text = _read_root_conf(path)
        except FileNotFoundError as e:
            raise RefusedError(f"{path}, {unit}'s unit file, is missing; nothing installed") from e
        try:
            _check_template(text, cfg, path)
        except RefusedError as e:
            raise RefusedError(f"{path}: {e}; nothing installed") from e
        _check_rules(text, cfg, path)


def _check_unit_dropins(cfg: dict[str, str]) -> None:
    """Apply _check_rules to every drop-in systemd applies to the unit,
    host.conf and the helper's own fixed files included (they pass)."""
    for path in _unit_dropins(cfg):
        try:
            text = _read_root_conf(path)
        except FileNotFoundError as e:
            raise RefusedError(f"{path} vanished while it was checked; nothing installed") from e
        _check_rules(text, cfg, path)


def _check_template(text: str, cfg: dict[str, str], path: str) -> None:
    """The owner's unit template, and every copy of the unit file, must name
    this config's binary, user and group: a template from another install
    would start the wrong thing, and a unit without User= runs as root.
    Read as systemd reads it: logical lines (_logical_lines), so a User=
    folded into the line before it by a trailing backslash is not a User=;
    the last assignment wins, and an empty one resets the setting."""
    service = False
    found: dict[str, str | None] = {"User": None, "Group": None, "ExecStart": None}
    for _, line in _logical_lines(text, path):
        s = line.strip(_WS)
        if s.startswith("["):
            service = s == "[Service]"
            continue
        key, sep, value = s.partition("=")
        key, value = key.strip(_WS), value.strip(_WS)
        if service and sep and key in found:
            found[key] = value or None
    want = {"User": cfg["service_user"], "Group": cfg["service_group"]}
    for key, who in want.items():
        if found[key] != who:
            raise RefusedError(
                f"the unit template's {key}= is not {who} (service_{key.lower()} in {CONFIG})"
            )
    exe = found["ExecStart"] or ""
    if exe != cfg["bin"] and not exe.startswith(cfg["bin"] + " "):
        raise RefusedError(
            f"the unit template's ExecStart= does not run {cfg['bin']} (bin in {CONFIG})"
        )


def _read_all(fd: int, limit: int) -> bytes:
    """Read fd to its end or to limit bytes: one read(2) may return less
    than asked (a network-mounted home), and a short read must never install
    a truncated file."""
    chunks: list[bytes] = []
    total = 0
    while total < limit:
        chunk = os.read(fd, limit - total)
        if not chunk:
            break
        chunks.append(chunk)
        total += len(chunk)
    return b"".join(chunks)


def _deploy_uid() -> int:
    """The user who ran sudo (SUDO_UID, which sudo sets and the caller cannot
    override), or this process's user when run without sudo."""
    v = os.environ.get("SUDO_UID", "")
    return int(v) if v.isdigit() else os.getuid()


def _open_no_symlinks(path: str) -> int:
    """Open path for reading with no symlink in ANY component: O_NOFOLLOW
    alone guards only the last one, so each directory is opened in turn,
    relative to the one before, with O_NOFOLLOW."""
    parts = [p for p in path.split("/") if p not in ("", ".")]
    if not path.startswith("/") or not parts or ".." in parts:
        raise RefusedError(f"{path} is not an absolute path")
    fd = os.open("/", os.O_RDONLY | os.O_DIRECTORY)
    try:
        for part in parts[:-1]:
            nxt = os.open(part, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=fd)
            os.close(fd)
            fd = nxt
        return os.open(parts[-1], os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK, dir_fd=fd)
    finally:
        os.close(fd)


def _read_staged(path: str, limit: int | None) -> tuple[int, os.stat_result]:
    """Open a staged file as root. It is reached with no symlink in any
    component, and must be a regular file (of at most limit bytes) owned by
    the deploy user with no other hard link: a hard link to a root-only file
    such as /etc/shadow opens through O_NOFOLLOW, but has the root file's
    owner and a second link. Returns the open fd and its stat."""
    try:
        fd = _open_no_symlinks(path)
    except OSError as e:
        raise RefusedError(
            f"cannot open {path}: {e.strerror} (a symlink in any component of the path is refused)"
        ) from e
    st = os.fstat(fd)
    problem = ""
    uid = _deploy_uid()
    if not stat.S_ISREG(st.st_mode) or (limit is not None and st.st_size > limit):
        problem = "is not a regular file" + (f" of at most {limit} bytes" if limit else "")
    elif st.st_nlink != 1:
        problem = f"has {st.st_nlink} hard links; a staged file must have exactly one"
    elif st.st_uid != uid:
        problem = f"is owned by uid {st.st_uid}, not by the deploy user (uid {uid}) who must own it"
    if problem:
        os.close(fd)
        raise RefusedError(f"{path} {problem}; nothing installed")
    return fd, st


def _read_template(cfg: dict[str, str]) -> bytes:
    """LIBDIR/UNIT_TEMPLATE, which the owner installed with the helpers:
    no symlink, a regular file owned by root and not writable by group or
    others, like CONFIG."""
    path = f"{LIBDIR}/{UNIT_TEMPLATE}"
    try:
        fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    except OSError as e:
        raise RefusedError(
            f"cannot read the unit template {path}: {e.strerror}. Install it with the helpers "
            "(docs/system/deploy-and-gpu-ops.md)."
        ) from e
    try:
        st = os.fstat(fd)
        if not stat.S_ISREG(st.st_mode) or st.st_size > UNIT_LIMIT or not _owner_ok(st):
            raise RefusedError(
                f"{path} must be a regular file owned by root, not writable by group or others"
            )
        data = _read_all(fd, UNIT_LIMIT + 1)
    finally:
        os.close(fd)
    try:
        text = data.decode("utf-8", "strict")
    except UnicodeDecodeError as e:
        raise RefusedError(f"{path} is not UTF-8") from e
    _check_template(text, cfg, path)
    _check_rules(text, cfg, path)
    return data


# --- the service environment is part of a rollback ---------------------------
#
# <bin>.envgen/ keeps one GENERATION per binary: the unit file and the local.conf
# drop-in that binary ran under, written when a deploy replaced it, as
# <sha256 of that binary>.json. Root-only (directory 0700, files 0600): local.conf
# carries secrets, and nothing prints it. A rollback restores the generation of the
# binary it puts back, found by that hash and never by position, so a hotfix or a
# same-build redeploy between the flip and its rollback cannot pair a binary with
# an environment it never ran under.

ENV_VERSION = 2
ENV_LIMIT = 2 * UNIT_LIMIT + 4096
_HEX64 = re.compile(r"[0-9a-f]{64}")
ENV_PARTS = ("unit", "local_conf")
# The newest generations always kept; every one kept because the deploy that
# replaced its binary changed what the store or the environment needs (see
# _pin_reason): the pre-flip binary's, so the flip can be undone after later
# hotfixes; and the newest GEN_KEEP_PINNED kept because the facts to tell were
# missing.
GEN_KEEP = 3
GEN_KEEP_PINNED = 3
PIN_BOUNDARY = "boundary"
PIN_UNKNOWN = "unknown"
_VERSION_TAG = re.compile(r"[A-Za-z0-9][A-Za-z0-9._+-]{0,63}")
_LEVEL_OWNER = re.compile(r"[a-z][a-z0-9_.-]{0,63}")
_LEVEL_MAX = 64


def _env_dir(cfg: dict[str, str]) -> str:
    return f"{cfg['bin']}.envgen"


def _gen_path(cfg: dict[str, str], sha: str) -> str:
    return f"{_env_dir(cfg)}/{sha}.json"


def _pending_path(cfg: dict[str, str]) -> str:
    """The capture a deploy is still making."""
    return f"{_env_dir(cfg)}/pending.json"


def _current_path(cfg: dict[str, str]) -> str:
    """What the recipe said about the binary installed now (advisory)."""
    return f"{_env_dir(cfg)}/current.json"


def _ensure_env_dir(cfg: dict[str, str]) -> None:
    d = _env_dir(cfg)
    with contextlib.suppress(FileExistsError):
        os.mkdir(d, 0o700)
    _check_env_dir(d)


def _check_env_dir(d: str) -> bool:
    """True when the directory exists and is trustworthy; False when absent."""
    try:
        st = os.lstat(d)
    except FileNotFoundError:
        return False
    if not stat.S_ISDIR(st.st_mode) or not _owner_ok(st):
        raise RefusedError(f"{d} is not a directory owned by root and closed to group and others")
    return True


def _live_env_path(cfg: dict[str, str], part: str) -> str:
    if part == "unit":
        return f"{SYSTEMD_DIR}/{cfg['unit']}"
    return f"{SYSTEMD_DIR}/{cfg['unit']}.d/local.conf"


def _sha256_file(path: str) -> str:
    """sha256 of a regular file, opened without following a symlink; ""
    when it does not exist. Anything else refuses."""
    try:
        fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    except FileNotFoundError:
        return ""
    except OSError as e:
        raise RefusedError(f"cannot open {path} ({e.strerror}; a symlink is refused)") from e
    try:
        if not stat.S_ISREG(os.fstat(fd).st_mode):
            raise RefusedError(f"{path} is not a regular file")
        h = hashlib.sha256()
        while True:
            chunk = os.read(fd, 1 << 20)
            if not chunk:
                return h.hexdigest()
            h.update(chunk)
    finally:
        os.close(fd)


def _capture_env(cfg: dict[str, str], part: str) -> dict:
    """The live file as a snapshot entry. A file that cannot be read under
    _read_root_conf's rules is recorded as unreadable, never skipped: it must
    not block the install that replaces it, and a rollback must not pretend
    it knows what was there."""
    try:
        return {"present": True, "text": _read_root_conf(_live_env_path(cfg, part))}
    except FileNotFoundError:
        return {"present": False}
    except RefusedError:
        return {"unreadable": True}


def _read_env_json(path: str, root_owned: bool) -> dict | None:
    """The JSON object at path, or None when it is absent. A file that is
    not a small regular file (owned by root unless root_owned is False), or
    does not parse, raises RefusedError."""
    try:
        fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    except FileNotFoundError:
        return None
    except OSError as e:
        raise RefusedError(f"cannot open {path} ({e.strerror}; a symlink is refused)") from e
    try:
        st = os.fstat(fd)
        if not stat.S_ISREG(st.st_mode) or st.st_size > ENV_LIMIT or not _owner_ok(st):
            raise RefusedError(
                f"{path} must be a regular file of at most {ENV_LIMIT} bytes, owned by root and "
                "not writable by group or others"
            )
        data = _read_all(fd, ENV_LIMIT + 1)
    finally:
        os.close(fd)
    try:
        doc = json.loads(data.decode("utf-8", "strict"))
    except (ValueError, UnicodeDecodeError) as e:
        raise RefusedError(f"{path} does not parse as JSON") from e
    if not isinstance(doc, dict):
        raise RefusedError(f"{path} is not a JSON object")
    return doc


def _write_env_json(path: str, doc: dict) -> None:
    _write_replace(path, json.dumps(doc, sort_keys=True).encode(), 0o600)


def _forget_env(*paths: str) -> None:
    for p in paths:
        with contextlib.suppress(FileNotFoundError):
            os.unlink(p)


def _parse_levels(token: str) -> dict[str, int] | None:
    """`-` unknown, `.` every owner at 0, else owner=level,owner=level."""
    if token == "-":
        return None
    if token == ".":
        return {}
    out: dict[str, int] = {}
    for part in token.split(","):
        owner, eq, level = part.partition("=")
        if not eq or not _LEVEL_OWNER.fullmatch(owner) or not re.fullmatch(r"[0-9]{1,6}", level):
            raise RefusedError(f"bad levels {token!r}: use - or . or owner=level,owner=level", 2)
        out[owner] = int(level)
    if len(out) > _LEVEL_MAX:
        raise RefusedError("too many owners in the levels", 2)
    return out


def _fmt_levels(levels: dict[str, int] | None) -> str:
    if levels is None:
        return "unrecorded"
    return ",".join(f"{k}={v}" for k, v in sorted(levels.items())) or "."


def _clean_meta(raw: object) -> dict:
    """Recipe-supplied or stored facts about a binary, kept only as far as
    they are well formed. Display and the pin decision only: a generation is
    never chosen by them."""
    out: dict = {"version": None, "levels": None, "enforced": None}
    if not isinstance(raw, dict):
        return out
    v = raw.get("version")
    if isinstance(v, str) and _VERSION_TAG.fullmatch(v):
        out["version"] = v
    lv = raw.get("levels")
    if (
        isinstance(lv, dict)
        and len(lv) <= _LEVEL_MAX
        and all(
            isinstance(k, str)
            and _LEVEL_OWNER.fullmatch(k)
            and isinstance(n, int)
            and not isinstance(n, bool)
            and 0 <= n <= 999999
            for k, n in lv.items()
        )
    ):
        out["levels"] = dict(lv)
    if isinstance(raw.get("enforced"), bool):
        out["enforced"] = raw["enforced"]
    return out


def _parse_meta(args: list[str]) -> dict | None:
    """`install binary [SHA256 VERSION LEVELS ENFORCED]`: what the deploy
    recipe measured about the staged binary, as the deploy user (the helper
    never runs a binary as root). None when the recipe said nothing."""
    if not args:
        return None
    if len(args) != 4:
        raise RefusedError("usage: install binary [SHA256 VERSION|- LEVELS|-|. ENFORCED(0|1|-)]", 2)
    sha, version, levels, enforced = args
    if not _HEX64.fullmatch(sha):
        raise RefusedError("usage: the first argument of `install binary` is a sha256", 2)
    if version != "-" and not _VERSION_TAG.fullmatch(version):
        raise RefusedError(f"bad version {version!r}", 2)
    if enforced not in ("0", "1", "-"):
        raise RefusedError("ENFORCED is 0, 1 or -", 2)
    return {
        "bin_sha256": sha,
        "version": None if version == "-" else version,
        "levels": _parse_levels(levels),
        "enforced": None if enforced == "-" else enforced == "1",
    }


def _pin_reason(replaced: dict | None, incoming: dict | None) -> str:
    """Why this deploy pins the generation of the binary it replaces:
    "boundary" when both sides' facts are known and the deploy raises a store
    level or changes `enforced` (that binary is the last one that could run
    under the previous levels or names); "unknown" when a fact needed to tell
    is missing (the cost of a wrong yes is one more kept generation); "" for a
    routine deploy. A known boundary is never evicted; unknown pins are
    bounded, so a run of deploys that measured nothing cannot grow the
    directory, and cannot push a boundary out either."""
    if replaced is not None and incoming is not None:
        r, i = replaced["levels"], incoming["levels"]
        if (
            r is not None
            and i is not None
            and any(i.get(o, 0) > r.get(o, 0) for o in set(r) | set(i))
        ):
            return PIN_BOUNDARY
        re, ie = replaced["enforced"], incoming["enforced"]
        if re is not None and ie is not None and re != ie:
            return PIN_BOUNDARY
    if replaced is None or incoming is None:
        return PIN_UNKNOWN
    if (
        replaced["levels"] is None
        or incoming["levels"] is None
        or replaced["enforced"] is None
        or incoming["enforced"] is None
    ):
        return PIN_UNKNOWN
    return ""


def _env_capture(cfg: dict[str, str], part: str) -> None:
    """Before install unit or local-conf replaces its file: keep that file as
    this deploy's capture. The first capture wins, so a deploy rerun after a
    half-done one still keeps the environment the previous BINARY ran under,
    not the half-new one. A capture made under another binary than the one
    installed now is stale (a rollback or a hand swap came between) and is
    started again."""
    pending_path = _pending_path(cfg)
    _ensure_env_dir(cfg)
    try:
        cur = _sha256_file(cfg["bin"])
        pend = _read_env_json(pending_path, True)
    except RefusedError:
        pend, cur = None, _sha256_file(cfg["bin"])
    if pend is not None and (pend.get("version") != ENV_VERSION or pend.get("bin_sha256") != cur):
        pend = None
    if pend is None:
        pend = {"version": ENV_VERSION, "bin_sha256": cur}
    if part in pend:
        return
    pend[part] = _capture_env(cfg, part)
    try:
        _write_env_json(pending_path, pend)
    except OSError as e:
        raise RefusedError(
            f"keeping the {part.replace('_', '-')} being replaced for a rollback failed ({e}); nothing installed"
        ) from e


def _env_promote(cfg: dict[str, str], had_binary: bool, incoming: dict | None = None) -> None:
    """In install binary, once <bin>.prev holds the replaced binary: commit
    the deploy's capture as that binary's generation. A part the deploy did
    not capture was not changed by it, so it is taken as it stands. A capture
    the binary changed under (stale) is dropped, and with it the binary's
    generation: a rollback then refuses instead of restoring a guess. A deploy
    with no binary to keep has nothing to roll back to. The capture itself is
    deleted by install binary once the new binary is in place, not here: if
    that last step fails and the deploy is rerun, the capture is what still
    holds the environment the replaced binary ran under."""
    pending_path = _pending_path(cfg)
    b = cfg["bin"]
    if not had_binary:
        _forget_env(pending_path)
        return
    prev_sha = _sha256_file(f"{b}.prev")
    gen_path = _gen_path(cfg, prev_sha)
    try:
        pend = _read_env_json(pending_path, True)
    except RefusedError:
        pend = {}
    if pend is not None and (
        pend.get("version") != ENV_VERSION or pend.get("bin_sha256") != prev_sha
    ):
        _forget_env(gen_path, pending_path)
        print(
            f"warning: the environment capture for {b}.prev is stale, so no environment is kept for "
            "it: a rollback to it refuses until the next deploy",
            file=sys.stderr,
        )
        return
    pend = pend or {}
    _ensure_env_dir(cfg)
    docs, _ = _scan_generations(cfg)
    replaced = None
    try:
        cur = _read_env_json(_current_path(cfg), True)
    except RefusedError:
        cur = None  # advisory: an unreadable record is "unrecorded"
    if cur is not None and cur.get("bin_sha256") == prev_sha:
        replaced = _clean_meta(cur)
    reason = _pin_reason(replaced, incoming)
    doc = {
        "version": ENV_VERSION,
        "bin_sha256": prev_sha,
        "seq": max((d["seq"] for d in docs), default=0) + 1,
        "binary": replaced or _clean_meta(None),
        "pinned": bool(reason),
        "pin_reason": reason,
    }
    for part in ENV_PARTS:
        doc[part] = pend.get(part) or _capture_env(cfg, part)
    _write_env_json(gen_path, doc)
    _evict(cfg, prev_sha)


def _scan_generations(cfg: dict[str, str]) -> tuple[list[dict], int]:
    """(the generations, newest first; how many files could not be read).
    A damaged file is counted, never trusted and never deleted."""
    d = _env_dir(cfg)
    try:
        if not _check_env_dir(d):
            return [], 0
        names = os.listdir(d)
    except (OSError, RefusedError):
        return [], 1
    docs: list[dict] = []
    damaged = 0
    for name in names:
        stem, dot, ext = name.partition(".")
        if ext != "json" or not _HEX64.fullmatch(stem):
            continue
        try:
            doc = _read_env_json(f"{d}/{name}", True)
        except (OSError, RefusedError):
            damaged += 1
            continue
        if (
            doc is None
            or doc.get("version") != ENV_VERSION
            or doc.get("bin_sha256") != stem
            or not isinstance(doc.get("seq"), int)
            or isinstance(doc.get("seq"), bool)
        ):
            damaged += 1
            continue
        docs.append(doc)
    docs.sort(key=lambda x: x["seq"], reverse=True)
    return docs, damaged


def _evict(cfg: dict[str, str], keep_sha: str) -> None:
    """Drop generations beyond the newest GEN_KEEP, except every generation
    pinned for a known boundary, the newest GEN_KEEP_PINNED pinned for unknown
    facts (a pin without a recorded reason counts as unknown), and keep_sha.
    Nothing is dropped while any generation file is unreadable (it may be one
    that must stay)."""
    docs, damaged = _scan_generations(cfg)
    if damaged:
        print(
            f"warning: {damaged} generation file(s) under {_env_dir(cfg)} are unreadable; none evicted",
            file=sys.stderr,
        )
        return
    keep = {d["bin_sha256"] for d in docs[:GEN_KEEP]}
    keep |= {d["bin_sha256"] for d in docs if d.get("pin_reason") == PIN_BOUNDARY}
    unknown = [d for d in docs if d.get("pinned") is True and d.get("pin_reason") != PIN_BOUNDARY]
    keep |= {d["bin_sha256"] for d in unknown[:GEN_KEEP_PINNED]}
    keep.add(keep_sha)
    for d in docs:
        if d["bin_sha256"] not in keep:
            try:
                _forget_env(_gen_path(cfg, d["bin_sha256"]))
            except OSError as e:
                print(f"warning: could not evict a generation ({e})", file=sys.stderr)


def _gen_desc(doc: dict) -> str:
    b = _clean_meta(doc.get("binary"))
    return (
        f"version={b['version'] or 'unrecorded'} levels={_fmt_levels(b['levels'])} "
        f"pinned={(doc.get('pin_reason') or PIN_UNKNOWN) if doc.get('pinned') is True else 0}"
    )


def _kept_list(cfg: dict[str, str]) -> str:
    docs, _ = _scan_generations(cfg)
    if not docs:
        return "none are kept"
    return "kept: " + "; ".join(f"{d['bin_sha256']} ({_gen_desc(d)})" for d in docs)


def _read_generation(cfg: dict[str, str], gen: str, named: bool) -> dict:
    """The generation for the binary with sha256 gen, validated; every
    refusal says what to do. named: the operator named it (ROLLBACK_ENV)."""
    d = _env_dir(cfg)
    path = _gen_path(cfg, gen)
    doc = _read_env_json(path, True) if _check_env_dir(d) else None
    if doc is None:
        what = (
            "no kept environment with that sha256"
            if named
            else (f"no kept environment for the binary with sha256 {gen[:12]}...")
        )
        raise RefusedError(
            f"{what}: the unit and local.conf it ran under were not kept, so a rollback to it would "
            "restore the binary alone. A deploy through install-staged keeps them when it replaces a "
            f"binary; {_kept_list(cfg)}. If one of those is the environment this binary needs "
            "(a release built elsewhere never has the sha256 of the build that was deployed), name it: "
            "ROLLBACK_ENV=<sha256> (make rollback-check lists them)"
        )
    if (
        doc.get("version") != ENV_VERSION
        or doc.get("bin_sha256") != gen
        or not isinstance(doc.get("seq"), int)
    ):
        raise RefusedError(f"{path} is malformed (version, bin_sha256 or seq)")
    for part in ENV_PARTS:
        entry = doc.get(part)
        if not isinstance(entry, dict):
            raise RefusedError(f"{path} is malformed ({part})")
        if entry.get("unreadable") is True:
            raise RefusedError(
                f"{path}: the {part.replace('_', '-')} that was replaced could not be read when it was kept "
                "(not a root-owned regular file), so it cannot be restored"
            )
        if entry.get("present") is True and isinstance(entry.get("text"), str):
            continue
        if entry.get("present") is False and part == "local_conf":
            continue
        if entry.get("present") is False:
            raise RefusedError(
                f"{path}: no unit file existed before this deploy installed one, and a rollback does not "
                "remove the unit"
            )
        raise RefusedError(f"{path} is malformed ({part})")
    return doc


def _check_restorable(cfg: dict[str, str], doc: dict) -> str | None:
    """The rules install unit and install local-conf apply, applied to what
    would be put back. Returns the local.conf to write: the kept drop-in
    re-rendered through render_local_conf, so a drop-in an older tool or a
    person wrote (not this helper) is held to the grammar and the denied
    loader variables too, or None when there was no drop-in."""
    _check_host_conf(cfg)
    _check_unit_dropins(cfg)
    _check_unit_fragment(cfg, installing_unit=True)
    path = _gen_path(cfg, doc["bin_sha256"])
    text = doc["unit"]["text"]
    try:
        _check_template(text, cfg, path)
    except RefusedError as e:
        raise RefusedError(f"the kept unit: {e}; nothing changed") from e
    _check_rules(text, cfg, f"{path} (unit)")
    if not doc["local_conf"].get("present"):
        return None
    try:
        return render_local_conf(doc["local_conf"]["text"], "the kept local.conf")
    except RefusedError as e:
        raise RefusedError(f"the kept local.conf: {e}; nothing changed") from e


def _env_summary(doc: dict) -> str:
    lc = doc["local_conf"]
    lines = (
        sum(1 for ln in lc["text"].splitlines() if ln.lstrip().startswith("Environment="))
        if lc.get("present")
        else 0
    )
    return (
        f"unit=present local_conf={'present' if lc.get('present') else 'absent'} "
        f"environment_lines={lines}"
    )


def _rollback_list(cfg: dict[str, str]) -> int:
    """Read-only: every kept generation (sha256, recorded version and levels,
    pinned, what the files hold: no content), and which one .prev matches."""
    docs, damaged = _scan_generations(cfg)
    print(
        f"generations={len(docs)} unreadable={damaged} keep_newest={GEN_KEEP} keep_unknown_pinned={GEN_KEEP_PINNED}"
    )
    for d in docs:
        lc = d["local_conf"]
        print(
            f"generation sha256={d['bin_sha256']} seq={d['seq']} {_gen_desc(d)} "
            f"unit={'present' if d['unit'].get('present') else 'absent'} "
            f"local_conf={'present' if lc.get('present') else 'absent'}"
        )
    have = _sha256_file(f"{cfg['bin']}.prev")
    cur = _sha256_file(cfg["bin"])
    print(
        f"prev sha256={have or 'none'} generation={have if have and any(d['bin_sha256'] == have for d in docs) else 'none'}"
    )
    print(f"current sha256={cur or 'none'}")
    return 0


def _rollback_env(cfg: dict[str, str], rest: list[str]) -> int:
    """install rollback list | check prev|SHA256 [GEN] | apply SHA256 [GEN]."""
    usage = (
        "install rollback list | install rollback check prev|SHA256 [GENERATION-SHA256] | "
        "install rollback apply SHA256 [GENERATION-SHA256]"
    )
    verb = rest[0] if rest else ""
    gen_arg = rest[2] if len(rest) == 3 else ""
    if verb == "list" and len(rest) == 1:
        return _rollback_list(cfg)
    ok_gen = not gen_arg or bool(_HEX64.fullmatch(gen_arg))
    if (
        verb == "check"
        and len(rest) in (2, 3)
        and ok_gen
        and (rest[1] == "prev" or _HEX64.fullmatch(rest[1]))
    ):
        apply = False
    elif verb == "apply" and len(rest) in (2, 3) and ok_gen and _HEX64.fullmatch(rest[1]):
        apply = True
    else:
        raise RefusedError(f"usage: {usage}", 2)
    if apply:
        # Read, checked and changed under one lock: a deploy cannot land
        # between the sha256 of .prev and the copy of it.
        with _helper_lock(cfg):
            return _rollback_env_run(cfg, rest, gen_arg, apply)
    return _rollback_env_run(cfg, rest, gen_arg, apply)


def _rollback_env_run(cfg: dict[str, str], rest: list[str], gen_arg: str, apply: bool) -> int:
    b = cfg["bin"]
    prev = f"{b}.prev"
    have = _sha256_file(prev)
    if not have and (apply or rest[1] == "prev"):
        raise RefusedError(f"no {prev}: nothing to roll back to; nothing changed")
    # The binary whose environment is restored: .prev as it is (apply holds
    # it to the hash the recipe checked), or, before RELEASE= installs one,
    # the staged file the recipe hashed.
    binsha = rest[1] if rest[1] != "prev" else have
    gen = gen_arg or binsha
    doc = _read_generation(cfg, gen, named=bool(gen_arg))
    if not apply:
        _check_restorable(cfg, doc)
        print(
            f"env snapshot ok: current_prev_sha256={binsha} generation={gen} "
            f"{_gen_desc(doc)} {_env_summary(doc)}"
        )
        return 0
    if have != rest[1]:
        raise RefusedError(
            f"{prev} is not the file the rollback checked (sha256 {have[:12]}..., expected {rest[1][:12]}...): "
            "a deploy or an install landed in between. Nothing changed; run the rollback again"
        )
    conf_text = _check_restorable(cfg, doc)
    unit_dest = _live_env_path(cfg, "unit")
    conf_dest = _live_env_path(cfg, "local_conf")
    kept, new = f"{b}.rolled-back", f"{b}.new"
    # Stage everything first, kept-binary copy included: the renames below
    # are the only steps that change a live path, so a refusal before them is
    # "nothing changed" in fact.
    staged: list[tuple[str, str]] = []
    cleanup: list[str] = [new]

    def unstage() -> None:
        for path in cleanup:
            with contextlib.suppress(OSError):
                os.unlink(path)

    try:
        copied = _copy_hashed(prev, new)
        if copied != rest[1]:
            raise RefusedError(
                f"{prev} changed while it was being copied (sha256 {copied[:12]}..., expected "
                f"{rest[1][:12]}...); nothing changed. Run the rollback again"
            )
        staged.append((new, b))
        for text, dest in (
            (doc["unit"]["text"], unit_dest),
            (conf_text, conf_dest),
        ):
            if text is None:
                continue
            tmp = f"{dest}.tmp"
            cleanup.append(tmp)
            with contextlib.suppress(FileNotFoundError):
                os.unlink(tmp)
            os.makedirs(os.path.dirname(dest), mode=0o755, exist_ok=True)
            fd = os.open(tmp, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o644)
            with os.fdopen(fd, "wb") as f:
                f.write(text.encode())
                f.flush()
                os.fchmod(f.fileno(), 0o644)
                os.fsync(f.fileno())
            staged.append((tmp, dest))
        # Keep what is running as .rolled-back, unless it already is the
        # binary being restored: a rerun after the binary rename must not
        # overwrite the kept rolled-back binary with the restored one.
        if _sha256_file(b) != rest[1]:
            shutil.copy2(b, f"{kept}.tmp")
            cleanup.append(f"{kept}.tmp")
            os.replace(f"{kept}.tmp", kept)
            _sync_dir(kept)
    except RefusedError:
        unstage()
        raise
    except OSError as e:
        unstage()
        raise RefusedError(f"staging the rollback failed ({e}); nothing changed") from e
    try:
        # Environment first, binary last: a failure before the last rename
        # leaves the old binary running under the restored environment's
        # files, which `apply` run again finishes. A missing local.conf is
        # removed with the environment, before the binary is replaced.
        for tmp, dest in sorted(staged, key=lambda t: t[1] == b):
            if dest == b and not doc["local_conf"].get("present"):
                with contextlib.suppress(FileNotFoundError):
                    os.unlink(conf_dest)
            os.replace(tmp, dest)
        for _, dest in staged:
            _sync_dir(dest)
        if not doc["local_conf"].get("present") and os.path.isdir(os.path.dirname(conf_dest)):
            _sync_dir(conf_dest)
        _forget_env(_pending_path(cfg))
    except OSError as e:
        raise RefusedError(
            f"the rollback was interrupted ({e}): run `install rollback apply` again to finish it"
        ) from e
    print(
        f"restored {prev} as {b} (the replaced binary is {kept}), the unit and the local.conf drop-in "
        f"({_env_summary(doc)})"
    )
    try:
        _daemon_reload()
    except RefusedError as e:
        raise RefusedError(
            f"{e}; the binary, the unit and local.conf are already restored: run make rollback again "
            "(it finds nothing left to replace and reloads systemd)"
        ) from e
    return 0


def op_install(args: list[str]) -> int:
    usage = "install binary [SHA256 VERSION LEVELS ENFORCED]|prev|unit|local-conf|rollback ..."
    if not args or args[0] not in ("binary", "prev", "unit", "local-conf", "rollback"):
        raise RefusedError(f"usage: {usage}", 2)
    what = args[0]
    meta = None
    if what == "binary":
        meta = _parse_meta(args[1:])
    elif what != "rollback":
        _arity(args, 1, usage)
    cfg = load_config()
    if what == "rollback":
        return _rollback_env(cfg, args[1:])
    with _helper_lock(cfg):
        return _install(cfg, what, meta)


def _install(cfg: dict[str, str], what: str, meta: dict | None) -> int:
    if what in ("unit", "local-conf"):
        # Before anything is read or copied: without a good host.conf the
        # base unit's ExecStart would run (no TLS, the wrong store), so
        # neither the unit nor local.conf is installed.
        _check_host_conf(cfg)
        _check_unit_dropins(cfg)
        _check_unit_fragment(cfg, installing_unit=what == "unit")
    if what == "unit":
        data = _read_template(cfg)
        os.makedirs(SYSTEMD_DIR, mode=0o755, exist_ok=True)
        dest = f"{SYSTEMD_DIR}/{cfg['unit']}"
        _env_capture(cfg, "unit")
        _write_replace(dest, data, 0o644)
        print(f"installed the unit template {LIBDIR}/{UNIT_TEMPLATE} as {dest}")
        _daemon_reload()
        return 0
    key = "stage_bin" if what in ("binary", "prev") else "stage_local_conf"
    src = cfg.get(key, "")
    if not src:
        raise RefusedError(f"{key} is not set in {CONFIG}: nothing to install from")
    if what == "binary":
        return _install_binary(cfg, src, meta)
    if what == "prev":
        return _install_prev(cfg["bin"], src)
    fd, _ = _read_staged(src, UNIT_LIMIT)
    try:
        data = _read_all(fd, UNIT_LIMIT + 1)
    finally:
        os.close(fd)
    if len(data) > UNIT_LIMIT:
        raise RefusedError(f"{src} is larger than {UNIT_LIMIT} bytes")
    try:
        text = data.decode("utf-8", "strict")
    except UnicodeDecodeError as e:
        raise RefusedError(f"{src} is not UTF-8") from e
    try:
        rendered = render_local_conf(text, src)
    except RefusedError as e:
        raise RefusedError(f"{src} refused, nothing installed: {e}") from e
    d = f"{SYSTEMD_DIR}/{cfg['unit']}.d"
    os.makedirs(d, mode=0o755, exist_ok=True)
    dest = f"{d}/local.conf"
    _env_capture(cfg, "local_conf")
    _write_replace(dest, rendered.encode("ascii"), 0o644)
    print(f"rendered {dest} from the Environment= lines of {src}")
    _daemon_reload()
    return 0


def _install_binary(cfg: dict[str, str], src: str, meta: dict | None = None) -> int:
    """Install the staged binary as <bin>. The order keeps every failure
    harmless: the new bytes are written to <bin>.new first (a full disk fails
    here, before .prev or the environment is touched); then, only if they
    differ from the installed binary, the installed one is kept as <bin>.prev
    with its environment generation; the rename of <bin>.new is last. A
    redeploy of the same build does not rotate: .prev and the generations
    stay what they were, so a hotfix of the same bytes cannot push the
    pre-flip binary out."""
    b = cfg["bin"]
    prev, new = f"{b}.prev", f"{b}.new"
    fd, _ = _read_staged(src, None)
    try:
        sha_new = _write_new(fd, new, src, b)
        try:
            had = os.path.lexists(b)
            cur = _sha256_file(b) if had else ""
            if had and cur == sha_new:
                os.unlink(new)
                _forget_env(_pending_path(cfg))
                _record_current(cfg, sha_new, meta)
                print(
                    f"{src} is the binary already installed as {b}: nothing replaced, {prev} and the "
                    "kept environments are as they were"
                )
                return 0
            if had:
                try:
                    shutil.copy2(b, f"{prev}.tmp")
                    os.replace(f"{prev}.tmp", prev)
                except OSError as e:
                    raise RefusedError(
                        f"keeping {b} as {prev} failed ({e}); {b} is unchanged"
                    ) from e
            try:
                _env_promote(
                    cfg,
                    had,
                    meta if meta is not None and meta["bin_sha256"] == sha_new else None,
                )
            except (OSError, RefusedError) as e:
                # .prev is now the current binary: a generation kept for it by
                # an earlier deploy is not the environment it last ran under.
                # The capture stays (see _env_promote).
                with contextlib.suppress(OSError, RefusedError):
                    _forget_env(_gen_path(cfg, _sha256_file(prev)))
                raise RefusedError(
                    f"keeping the environment of {prev} failed ({e}); {b} is unchanged, "
                    "and a rollback to it refuses until a deploy succeeds"
                ) from e
            try:
                os.replace(new, b)
            except OSError as e:
                raise RefusedError(f"installing {src} as {b} failed ({e}); {b} is unchanged") from e
            _sync_dir(b)
        except BaseException:
            with contextlib.suppress(OSError):
                os.unlink(new)
            raise
        # The new binary is in place: this deploy's capture has done its job.
        try:
            _forget_env(_pending_path(cfg))
        except OSError as e:
            print(f"warning: could not remove the finished capture ({e})", file=sys.stderr)
        _record_current(cfg, sha_new, meta)
    finally:
        os.close(fd)
    print(f"installed {src} as {b}; the replaced binary is {prev}")
    return 0


def _write_new(fd: int, new: str, src: str, b: str) -> str:
    """Copy the staged file to <b>.new; the sha256 of the bytes written."""
    h = hashlib.sha256()
    try:
        with contextlib.suppress(FileNotFoundError):
            os.unlink(new)
        out = os.open(new, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o755)
        with os.fdopen(out, "wb") as f, os.fdopen(os.dup(fd), "rb") as s:
            while chunk := s.read(1 << 20):
                h.update(chunk)
                f.write(chunk)
            f.flush()
            os.fchmod(f.fileno(), 0o755)
            os.fsync(f.fileno())
    except OSError as e:
        with contextlib.suppress(OSError):
            os.unlink(new)
        raise RefusedError(f"installing {src} as {b} failed ({e}); {b} is unchanged") from e
    return h.hexdigest()


def _record_current(cfg: dict[str, str], sha: str, meta: dict | None) -> None:
    """Remember what the recipe measured about the binary now installed, so
    the deploy that replaces it can say what that generation served. Advisory
    and best effort: a failure costs a label, never the deploy. A recipe's
    facts about another file than the one installed are not recorded."""
    doc: dict = {"version": ENV_VERSION, "bin_sha256": sha}
    if meta is not None and meta["bin_sha256"] != sha:
        print(
            "warning: the recipe measured another file than the one installed (the staged file "
            "changed in between): its version and levels are not recorded",
            file=sys.stderr,
        )
    elif meta is not None:
        doc.update(version=meta["version"], levels=meta["levels"], enforced=meta["enforced"])
    try:
        _ensure_env_dir(cfg)
        _write_env_json(_current_path(cfg), doc)
    except (OSError, RefusedError) as e:
        print(f"warning: could not record the installed binary's facts ({e})", file=sys.stderr)


def _install_prev(b: str, src: str) -> int:
    """Install src as <b>.prev and nothing else: <b> is not read or changed."""
    prev, tmp = f"{b}.prev", f"{b}.prev.tmp"
    fd, _ = _read_staged(src, None)
    try:
        try:
            with contextlib.suppress(FileNotFoundError):
                os.unlink(tmp)
            out = os.open(tmp, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o755)
            with os.fdopen(out, "wb") as f, os.fdopen(os.dup(fd), "rb") as s:
                shutil.copyfileobj(s, f, 1 << 20)
                f.flush()
                os.fchmod(f.fileno(), 0o755)
                os.fsync(f.fileno())
            os.replace(tmp, prev)
        except OSError as e:
            with contextlib.suppress(OSError):
                os.unlink(tmp)
            raise RefusedError(
                f"installing {src} as {prev} failed ({e}); {prev} is unchanged"
            ) from e
    finally:
        os.close(fd)
    # The rename is durable only once the directory is. It has happened, so a
    # failure here is a warning, not a refusal: .prev is replaced.
    try:
        dfd = os.open(os.path.dirname(prev), os.O_RDONLY | os.O_DIRECTORY)
        try:
            os.fsync(dfd)
        finally:
            os.close(dfd)
    except OSError as e:
        print(
            f"warning: {prev} is installed but its directory could not be synced ({e})",
            file=sys.stderr,
        )
    print(f"installed {src} as {prev}")
    return 0


def op_swap_prev(args: list[str]) -> int:
    _arity(args, 0, "swap-prev (no arguments)")
    cfg = load_config()
    with _helper_lock(cfg):
        return _swap_prev(cfg["bin"])


def _swap_prev(b: str) -> int:
    prev, kept, new = f"{b}.prev", f"{b}.rolled-back", f"{b}.new"
    try:
        pst = os.lstat(prev)
    except FileNotFoundError as e:
        raise RefusedError(f"no {prev}: nothing to roll back to; {b} is unchanged") from e
    if not stat.S_ISREG(pst.st_mode):
        raise RefusedError(f"{prev} is not a regular file; {b} is unchanged")
    try:
        shutil.copy2(b, f"{kept}.tmp")
        os.replace(f"{kept}.tmp", kept)
    except OSError as e:
        raise RefusedError(f"keeping {b} as {kept} failed ({e}); {b} is unchanged") from e
    try:
        shutil.copy2(prev, new)
        with open(new, "rb") as f:
            os.fsync(f.fileno())
        os.replace(new, b)
    except OSError as e:
        with contextlib.suppress(OSError):
            os.unlink(new)
        raise RefusedError(
            f"installing {prev} as {b} failed ({e}); {b} is unchanged (still the current binary)"
        ) from e
    print(f"installed {prev} as {b}; the replaced binary is {kept}")
    return 0


OPS = {
    "probe": op_probe,
    "record": op_record,
    "restore": op_restore,
    "unit-type": op_unit_type,
    "swap-prev": op_swap_prev,
    "install": op_install,
    "pprof": op_pprof,
    "_into-worker": op_into_worker,
    "_report": op_report,
}


def main(argv: list[str]) -> int:
    if not argv or argv[0] not in OPS:
        print(f"usage: rollback_helper.py {'|'.join(OPS)} ...", file=sys.stderr)
        return 2
    try:
        return OPS[argv[0]](argv[1:])
    except RefusedError as e:
        print(f"rollback helper: {e}", file=sys.stderr)
        return e.code


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
