#!/usr/bin/python3 -I
# file: deploy/rollback-helpers/rollback_helper.py
# version: 1.0.0
# guid: 1d8f47fb-8f03-4fc9-af45-4b16e6ef75aa
# last-edited: 2026-11-09

"""Root-owned helpers for `make rollback` and `make rollback-restore` (R1-27).

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
  swap-prev          `swap-prev`, no arguments, as root. Keeps <bin> as
                     <bin>.rolled-back and installs <bin>.prev as <bin>
                     atomically (copy to <bin>.new, then rename), which also
                     avoids ETXTBSY on the running executable.

Exit 0 done; 1 refused or failed (the message says which step); 2 bad
arguments. Standard library only. Tests: scripts/tests/test_rollback_helper.py.
Install: docs/system/deploy-and-gpu-ops.md ("Rollback helpers").
"""

from __future__ import annotations

import contextlib
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
INTO_NAME = "rollback-restore-levels"
DROPIN = "zz-rollback-type-simple.conf"
DROPIN_TEXT = "[Service]\nType=simple\n"
RECORD_LIMIT = 1 << 20
# stub file name -> subcommand. Each stub is `exec /usr/bin/python3 -I
# LIBDIR/rollback_helper.py <subcommand> "$@"`.
STUBS = {
    "inflight-probe": "probe",
    "checkpoint-record": "record",
    "restore-offline": "restore",
    "unit-type": "unit-type",
    "swap-prev": "swap-prev",
}

_NAME = re.compile(r"[0-9]{8}T[0-9]{6}Z-[0-9a-f]{8}")
_BASENAME = re.compile(r"[A-Za-z0-9_][A-Za-z0-9._-]{0,199}")
_PATH = re.compile(r"/[A-Za-z0-9._/@+-]+")
_USER = re.compile(r"[a-z_][a-z0-9_-]{0,31}")
_UNIT = re.compile(r"[A-Za-z0-9@._:-]+\.service")
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
        text = os.read(fd, 65537).decode("utf-8", "strict")
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
    for key in ("bin", "db", "state_dir", "archive_dir", "checkpoint_dir"):
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
        data = os.read(fd, RECORD_LIMIT + 1)
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


def _transient(cfg: dict[str, str], cmd: list[str]) -> int:
    """Run cmd as the service user in the transient unit, relaying its log."""
    os.makedirs(RUN_DIR, mode=0o700, exist_ok=True)
    log = f"{RUN_DIR}/restore.log"
    fd = os.open(log, os.O_WRONLY | os.O_CREAT | os.O_TRUNC | os.O_NOFOLLOW, 0o600)
    os.close(fd)
    argv = [
        TOOLS["systemd-run"],
        "--wait",
        "--collect",
        "--quiet",
        f"--unit={TRANSIENT_UNIT}",
        "--service-type=exec",
        f"--uid={cfg['service_user']}",
        f"--property=StandardOutput=append:{log}",
        f"--property=StandardError=append:{log}",
    ]
    argv += [f"--setenv={tok}" for tok in cfg.get("offline_env", "").split()]
    argv += ["--", *cmd]
    print(
        f"rollback helper: running as {cfg['service_user']} in {TRANSIENT_UNIT}.service; if this connection drops "
        f"it keeps running: `systemctl status {TRANSIENT_UNIT}`, output in {log}.",
        file=sys.stderr,
    )
    proc = subprocess.Popen(argv)  # noqa: S603 (fixed argv built above)
    pos = 0
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


def op_unit_type(args: list[str]) -> int:
    _arity(args, 1, "unit-type simple|notify")
    kind = args[0]
    if kind not in ("simple", "notify"):
        raise RefusedError("usage: unit-type simple|notify", 2)
    cfg = load_config()
    d = f"{SYSTEMD_DIR}/{cfg['unit']}.d"
    path = f"{d}/{DROPIN}"
    if kind == "simple":
        os.makedirs(d, mode=0o755, exist_ok=True)
        tmp = f"{path}.tmp"
        with open(tmp, "w", encoding="utf-8") as f:
            f.write(DROPIN_TEXT)
        os.chmod(tmp, 0o644)
        os.replace(tmp, path)
        print(f"installed {path} (Type=simple)")
    else:
        try:
            os.unlink(path)
            print(f"removed {path} (Type=notify)")
        except FileNotFoundError:
            print(f"{path} is absent (Type=notify)")
    rc = subprocess.run([TOOLS["systemctl"], "daemon-reload"], check=False).returncode
    if rc != 0:
        raise RefusedError(f"systemctl daemon-reload exited {rc}")
    return 0


def op_swap_prev(args: list[str]) -> int:
    _arity(args, 0, "swap-prev (no arguments)")
    b = load_config()["bin"]
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
    "_into-worker": op_into_worker,
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
