#!/usr/bin/python3 -I
# file: deploy/rollback-helpers/rollback_helper.py
# version: 1.3.0
# guid: 1d8f47fb-8f03-4fc9-af45-4b16e6ef75aa
# last-edited: 2026-11-09

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
  install-staged     `install binary`, `install unit` or `install local-conf`,
                     as root: the deploy. binary installs stage_bin as <bin>
                     atomically (copy to <bin>.new, fsync, rename), keeping
                     the replaced one as <bin>.prev. systemd reads the unit
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
  pprof              `pprof on` or `pprof off`, as root. on writes the fixed
                     drop-in `[Service]\\nEnvironment=ABK_PPROF_ADDR=localhost:6060\\n`
                     to /etc/systemd/system/<unit>.d/pprof.conf; off removes
                     it. Then `systemctl daemon-reload`.
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


def render_local_conf(text: str, source: str) -> str:
    """The local.conf drop-in to install, rendered from the staged file:
    `[Service]` and its `Environment=` lines, each re-written as is after it
    matches _ENV_ASSIGNMENT. Comment and blank lines are dropped; anything
    else refuses (by line number: a refusal never prints the file). Settings
    other than Environment= (ExecStart=, WorkingDirectory=, MemoryMax=, ...)
    decide what runs and as whom, so they live in host.conf, which the owner
    installs as root and deploy never touches."""
    if "\r" in text or "\0" in text:
        raise RefusedError("carriage returns and NUL bytes are refused")
    lines: list[str] = []
    section = False
    for n, raw in enumerate(text.split("\n"), 1):
        s = raw.strip(_WS)
        if not s or s[0] in "#;":
            continue  # dropped: nothing in it reaches systemd
        if not _DIRECTIVE_CHARS.fullmatch(raw):
            raise RefusedError(
                f"line {n}: only printable ASCII and tabs are allowed outside comments"
            )
        if s == "[Service]":
            section = True
            continue
        if s.startswith("Environment=") and section:
            value = s[len("Environment=") :]
            if _ENV_ASSIGNMENT.fullmatch(value):
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


def _check_template(text: str, cfg: dict[str, str]) -> None:
    """The owner's unit template must name this config's binary, user and
    group: a template from another install would start the wrong thing."""
    service = False
    found = {"User": None, "Group": None, "ExecStart": None}
    for line in text.split("\n"):
        s = line.strip(_WS)
        if s.startswith("["):
            service = s == "[Service]"
            continue
        key, sep, value = s.partition("=")
        if service and sep and key in found and value:
            found[key] = value
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
        _check_template(data.decode("utf-8", "strict"), cfg)
    except UnicodeDecodeError as e:
        raise RefusedError(f"{path} is not UTF-8") from e
    return data


def op_install(args: list[str]) -> int:
    _arity(args, 1, "install binary|unit|local-conf")
    what = args[0]
    if what not in ("binary", "unit", "local-conf"):
        raise RefusedError("usage: install binary|unit|local-conf", 2)
    cfg = load_config()
    if what == "unit":
        data = _read_template(cfg)
        os.makedirs(SYSTEMD_DIR, mode=0o755, exist_ok=True)
        dest = f"{SYSTEMD_DIR}/{cfg['unit']}"
        _write_replace(dest, data, 0o644)
        print(f"installed the unit template {LIBDIR}/{UNIT_TEMPLATE} as {dest}")
        _daemon_reload()
        return 0
    key = "stage_bin" if what == "binary" else "stage_local_conf"
    src = cfg.get(key, "")
    if not src:
        raise RefusedError(f"{key} is not set in {CONFIG}: nothing to install from")
    if what == "binary":
        return _install_binary(cfg["bin"], src)
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
    _write_replace(dest, rendered.encode("ascii"), 0o644)
    print(f"rendered {dest} from the Environment= lines of {src}")
    _daemon_reload()
    return 0


def _install_binary(b: str, src: str) -> int:
    prev, new = f"{b}.prev", f"{b}.new"
    fd, _ = _read_staged(src, None)
    try:
        if os.path.lexists(b):
            try:
                shutil.copy2(b, f"{prev}.tmp")
                os.replace(f"{prev}.tmp", prev)
            except OSError as e:
                raise RefusedError(f"keeping {b} as {prev} failed ({e}); {b} is unchanged") from e
        try:
            with contextlib.suppress(FileNotFoundError):
                os.unlink(new)
            out = os.open(new, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o755)
            with os.fdopen(out, "wb") as f, os.fdopen(os.dup(fd), "rb") as s:
                shutil.copyfileobj(s, f, 1 << 20)
                f.flush()
                os.fchmod(f.fileno(), 0o755)
                os.fsync(f.fileno())
            os.replace(new, b)
        except OSError as e:
            with contextlib.suppress(OSError):
                os.unlink(new)
            raise RefusedError(f"installing {src} as {b} failed ({e}); {b} is unchanged") from e
    finally:
        os.close(fd)
    print(f"installed {src} as {b}; the replaced binary is {prev}")
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
    "install": op_install,
    "pprof": op_pprof,
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
