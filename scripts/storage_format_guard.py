#!/usr/bin/env python3
# file: scripts/storage_format_guard.py
# version: 2.0.0
# guid: 35696d63-9d8e-4fc3-b6a8-9c9770cd798c
# last-edited: 2026-11-09

"""The store guard of `make rollback` and `make rollback-restore`.

05-storage §5S.3.7 "The binary-swap guard", §5S.3.12 row `make
rollback-restore`, 09 §9.2.1 "Rollback"; R1-27. The name is kept from HEAD's
storage-format guard, which this replaces.

Three subcommands, each reading the deploy host over `ssh` (BatchMode, so a
password prompt fails at once) and deciding before anything is changed:

  rollback  `make rollback`'s check before it swaps `<bin>.prev` in. It
            refuses while a swap is in flight, and when any owner's store
            level is above what `.prev` serves, naming `make
            rollback-restore`: a binary swap cannot go back past a level step.
  inflight  the in-flight probe alone (`make rollback-restore` runs it again
            after it stops the service).
  stopped   refuse unless the unit reads inactive or failed: `make
            rollback-restore` holds the service down by checking, after the
            stop and again after the restore, that nothing started it (the
            unit cannot be masked while its file is in /etc/systemd/system;
            the restore's own swap lock is the exclusion).
  restore   `make rollback-restore`'s check before it stops the service: the
            in-flight probe, then the source's levels (the checkpoint record's
            `from_vector`, or with --archive the levels of the archived store,
            read from a scratch copy the current binary extracts) must EQUAL
            `.prev --print-storage-levels`, else it refuses naming the release
            to install. On success it prints, on stdout and alone, the remote
            command that restores the source; everything else goes to stderr.

What is read, and from where:

- the in-flight probe, one literal command shared by both recipes:
  `sudo -n /usr/bin/find <state_dir> -maxdepth 3 -path
  '<state_dir>/restore/*/inflight.json' -print`. Any output is a swap in
  flight: refuse, no override ("boot the current build once to finish it").
  A pending intent (`restore/pending.json`) does not match and passes: the
  older build rejects it at its own boot. A non-zero exit, or stderr starting
  with `sudo:`, refuses naming the sudoers line.
- the store's levels from `<db>.storage-levels` (lines `<owner> <level>`),
  read with `sudo -n cat`. Only when that file does not exist (a store no
  core build has opened) the guard falls back to HEAD's projection sidecar
  `<db>.storage-format` and its old rules, ROLLBACK_IGNORE_FORMAT=1 included
  (it covers only an unreadable projection sidecar, never levels, never the
  probe).
- what `.prev` serves from `<bin>.prev --print-storage-levels`, which prints
  one JSON object, owner to level. An R0 binary rejects the flag ("unknown
  flag: --print-storage-levels"); the guard then asks
  `--print-storage-format`, and format 1, or a build so old it rejects that
  flag too, serves every owner at level 0. Any other answer (a crash, an ssh
  failure, nothing) is not guessed: it refuses.
- an owner absent from a vector is at level 0 (05-storage §5S.3.7: absent
  stamp = 0).
- the checkpoint: `<db>.migration-checkpoint` (JSON, `name` names the latest
  attempt's checkpoint) and the record
  `<checkpoint_dir>/.mediacore-checkpoints/<name>.json`, read as the service
  user (the record is the service user's, mode 0600).

Exit 0: proceed. Exit 1: refuse; nothing was changed. Exit 2: bad arguments.

Standard library only. Tests: scripts/test_storage_format_guard.py (the
decisions) and scripts/tests/test_make_rollback.py (the recipes over a fake
host).
"""

from __future__ import annotations

import argparse
import json
import os
import re
import subprocess
import sys
from dataclasses import dataclass
from typing import Callable

DOCS = "docs/system/deploy-and-gpu-ops.md (Rollback flow, sudoers)"
# Host paths go into shell commands and sudoers lines unquoted: allow only
# characters that need no quoting.
_SAFE_PATH = re.compile(r"^/[A-Za-z0-9._/@+-]+$")
_SAFE_ENV = re.compile(r"^[A-Z_][A-Z0-9_]*=[A-Za-z0-9._/@+:-]*$")
_SAFE_USER = re.compile(r"^[a-z_][a-z0-9_-]*$")
# 05-storage §5S.3.12 "Where": the checkpoint name grammar.
_CHECKPOINT_NAME = re.compile(r"^\d{8}T\d{6}Z-[0-9a-f]{8}$")
_OLD_LEVELS_FLAG = re.compile(r"unknown flag: --print-storage-levels(\s|$)")
_OLD_FORMAT_FLAG = re.compile(r"unknown flag: --print-storage-format(\s|$)")
INTO_NAME = "rollback-restore-levels"


@dataclass
class Result:
    """One ssh command's outcome."""

    stdout: str = ""
    stderr: str = ""
    rc: int = 0

    @property
    def missing(self) -> bool:
        return self.rc != 0 and "No such file or directory" in self.stderr

    @property
    def sudo_refused(self) -> bool:
        return self.stderr.lstrip().startswith("sudo:")


Getter = Callable[[str], Result]


@dataclass
class Opts:
    host: str
    db: str
    bin: str
    state_dir: str
    unit: str = "audiobook-organizer.service"
    user: str = "audiobook"
    checkpoint_dir: str = ""
    archive: str = ""
    offline_env: str = ""

    @property
    def cp_area(self) -> str:
        return (self.checkpoint_dir or os.path.dirname(self.db)).rstrip(
            "/"
        ) + "/.mediacore-checkpoints"

    @property
    def into(self) -> str:
        return f"{self.cp_area}/scratch/{INTO_NAME}"

    def offline(self, args: str) -> str:
        env = f" {self.offline_env}" if self.offline_env else ""
        return f"sudo -n -u {self.user} /usr/bin/env{env} {self.bin} storage restore-offline --db {self.db} {args}"


def commands(o: Opts, record_name: str = "") -> dict[str, str]:
    """Every remote command the guard runs, by key."""
    sd = o.state_dir.rstrip("/")
    into = o.into
    return {
        "probe": f"sudo -n /usr/bin/find {sd} -maxdepth 3 -path '{sd}/restore/*/inflight.json' -print",
        "levels": f"sudo -n cat {o.db}.storage-levels",
        "format": f"sudo -n cat {o.db}.storage-format",
        "prev_levels": f"{o.bin}.prev --print-storage-levels",
        "prev_format": f"{o.bin}.prev --print-storage-format",
        "current_format": f"{o.bin} --print-storage-format",
        "pointer": f"sudo -n cat {o.db}.migration-checkpoint",
        "record": f"sudo -n -u {o.user} cat {o.cp_area}/{record_name}.json",
        "into_clean": f"sudo -n -u {o.user} rm -rf {into} {into}.state {into}.storage-levels {into}.storage-format",
        "into": o.offline(f"--archive {o.archive} --into {into}"),
        "into_levels": f"sudo -n -u {o.user} cat {into}.storage-levels",
        "unit": f"systemctl show -p ActiveState,SubState {o.unit}",
    }


def _cause(r: Result) -> str:
    lines = [ln.strip() for ln in r.stderr.splitlines() if ln.strip()]
    parts = [f"exit {r.rc}"]
    if r.stdout.strip():
        parts.append(f"stdout {r.stdout.strip()[:200]!r}")
    if lines:
        parts.append("stderr: " + " | ".join(lines[-3:]))
    return "; ".join(parts)


def fmt(levels: dict[str, int]) -> str:
    return (
        "{" + ", ".join(f"{k}: {v}" for k, v in sorted(levels.items())) + "}"
        if levels
        else "{} (every owner at 0)"
    )


def parse_sidecar(text: str) -> tuple[dict[str, int] | None, str]:
    """Parse `<owner> <level>` lines (core/internal/pebble ReadLevels)."""
    out: dict[str, int] = {}
    for n, line in enumerate(text.splitlines(), 1):
        line = line.strip()
        if not line:
            continue
        owner, sep, value = line.partition(" ")
        value = value.strip()
        if not sep or not owner or not value.isascii() or not value.isdigit():
            return None, f"line {n} {line!r} is not `<owner> <level>`"
        if owner in out:
            return None, f"owner {owner!r} is listed twice"
        out[owner] = int(value)
    return out, ""


def parse_vector(text: str) -> dict[str, int] | None:
    """Parse --print-storage-levels' JSON object (core/app/fastexit.go)."""
    try:
        doc = json.loads(text)
    except ValueError:
        return None
    if not isinstance(doc, dict):
        return None
    out = {}
    for k, v in doc.items():
        if not isinstance(k, str) or isinstance(v, bool) or not isinstance(v, int) or v < 0:
            return None
        out[k] = v
    return out


def _int(text: str) -> int | None:
    t = (text or "").strip()
    return int(t) if t.isascii() and t.isdigit() and int(t) >= 1 else None


def differs(a: dict[str, int], b: dict[str, int]) -> list[str]:
    return sorted(o for o in set(a) | set(b) if a.get(o, 0) != b.get(o, 0))


def above(store: dict[str, int], prev: dict[str, int]) -> list[str]:
    return sorted(o for o in store if store[o] > prev.get(o, 0))


def check_inflight(o: Opts, get: Getter) -> list[str]:
    """Return refusal lines, or [] when no swap is in flight."""
    cmd = commands(o)["probe"]
    r = get("probe")
    if r.rc != 0 or r.sudo_refused:
        return [
            f"REFUSING: the in-flight probe failed ({_cause(r)}), so it cannot tell whether a restore or reset "
            "swap is in flight. Nothing was changed.",
            f"It runs `{cmd}` on {o.host}; add that exact line to the deploy user's sudoers drop-in "
            f"({DOCS}), and check that {o.state_dir} is the service's state directory (DEPLOY_STATE_DIR).",
        ]
    found = [ln.strip() for ln in r.stdout.splitlines() if ln.strip()]
    if found:
        return [
            f"REFUSING: a restore or reset swap is in flight ({', '.join(found)}). Nothing was changed, and "
            "nothing overrides this.",
            f"Boot the current build once to finish it (sudo systemctl start {o.unit}), then decide again.",
        ]
    return []


def check_stopped(o: Opts, get: Getter) -> list[str]:
    """Return refusal lines unless the unit reads inactive or failed (the
    rollback-restore hold: after the stop, and again after the restore)."""
    r = get("unit")
    props = dict(ln.partition("=")[::2] for ln in r.stdout.splitlines() if "=" in ln)
    state = props.get("ActiveState", "").strip()
    if r.rc == 0 and state in ("inactive", "failed"):
        return []
    seen = (
        f"ActiveState={state} SubState={props.get('SubState', '').strip()}"
        if r.rc == 0
        else _cause(r)
    )
    return [
        f"REFUSING: {o.unit} did not stay stopped ({seen}). Something started it during the rollback; "
        "the recipe stopped here and swapped no binary.",
        f"Find what started it (systemctl status {o.unit}), stop it, read any restore output above, and run "
        "make rollback-restore again.",
    ]


def prev_vector(o: Opts, get: Getter) -> tuple[dict[str, int] | None, str]:
    """Return (the levels .prev serves, how it was read) or (None, why not)."""
    r = get("prev_levels")
    if r.rc == 0:
        v = parse_vector(r.stdout)
        if v is not None:
            return v, "it printed them"
        return (
            None,
            f"`{o.bin}.prev --print-storage-levels` printed no JSON level vector ({_cause(r)})",
        )
    if not _OLD_LEVELS_FLAG.search(r.stderr):
        return None, f"`{o.bin}.prev --print-storage-levels` failed ({_cause(r)})"
    f = get("prev_format")
    n = _int(f.stdout) if f.rc == 0 else None
    if n == 1 or (f.rc != 0 and _OLD_FORMAT_FLAG.search(f.stderr)):
        return (
            {},
            "it predates --print-storage-levels and reads storage format 1, so every owner is at 0",
        )
    if n is not None:
        return (
            None,
            f"it predates --print-storage-levels and reads storage format {n}, which maps to no level vector",
        )
    return (
        None,
        f"it predates --print-storage-levels and `--print-storage-format` failed too ({_cause(f)})",
    )


def _current_format(get: Getter) -> tuple[int | None, str]:
    r = get("current_format")
    n = _int(r.stdout) if r.rc == 0 else None
    if n is not None:
        return n, f"it printed {n}"
    if _OLD_FORMAT_FLAG.search(r.stderr):
        return 1, "it predates --print-storage-format, so it reads format 1"
    return None, _cause(r)


def _legacy(o: Opts, get: Getter, prev: dict[str, int], ignore: bool) -> tuple[int, list[str]]:
    """The rule for a store no core build has opened (no .storage-levels):
    HEAD's projection sidecar against the format .prev reads."""
    prev_format = 1 if all(v == 0 for v in prev.values()) else 2
    r = get("format")
    store = _int(r.stdout) if r.rc == 0 else None
    sidecar = f"{o.db}.storage-format"
    if store is None:
        cause = f"cause: {_cause(r)}"
        if not ignore:
            return 1, [
                f"REFUSING: {o.db}.storage-levels does not exist and the storage format in {sidecar} cannot be "
                f"read ({cause}). Set ROLLBACK_IGNORE_FORMAT=1 only if you have verified the store is at format 1.",
                f"If the cause is sudo asking for a password, add the sudoers lines in {DOCS}.",
            ]
        current, why = _current_format(get)
        if current is None:
            return 1, [
                f"REFUSING: cannot read {sidecar} ({cause}), and ROLLBACK_IGNORE_FORMAT=1 needs the current "
                f"binary's format to vouch for the store, but `{o.bin} --print-storage-format` gave no answer "
                f"({why}). Nothing was swapped."
            ]
        if current > prev_format:
            return 1, [
                f"REFUSING: cannot read {sidecar} ({cause}), and the current binary {o.bin} reads storage format "
                f"{current}, above the {prev_format} the previous binary reads. ROLLBACK_IGNORE_FORMAT=1 cannot "
                "vouch for a store a newer binary may already have converted; nothing was swapped."
            ]
        return 0, [
            f"WARNING: cannot read {sidecar} ({cause}); ROLLBACK_IGNORE_FORMAT=1 is set and the current binary "
            f"reads no format above the previous binary's ({current} <= {prev_format}; {why}), so the rollback "
            "proceeds as if the store were at format 1."
        ]
    if store > prev_format:
        lines = [
            f"REFUSING: the previous binary reads storage format {prev_format}, the store is at {store} "
            f"({sidecar}). A binary swap cannot go back past a format change; nothing was swapped.",
            "Roll back with `make rollback-restore`, which restores the pre-upgrade checkpoint (or "
            "ROLLBACK_ARCHIVE=<archive on the host>) and then installs .prev.",
        ]
        if ignore:
            lines.append("ROLLBACK_IGNORE_FORMAT=1 does not override a format change.")
        return 1, lines
    return 0, [
        f"store guard: store format {store} ({sidecar}), previous binary reads {prev_format}; swap allowed."
    ]


def decide_rollback(o: Opts, get: Getter, ignore: bool) -> tuple[int, list[str]]:
    refusal = check_inflight(o, get)
    if refusal:
        return 1, refusal
    prev, how = prev_vector(o, get)
    if prev is None:
        return 1, [
            f"REFUSING: cannot tell which store levels {o.bin}.prev serves: {how}. Nothing was swapped."
        ]
    r = get("levels")
    sidecar = f"{o.db}.storage-levels"
    if r.missing:
        return _legacy(o, get, prev, ignore)
    if r.rc != 0 or r.sudo_refused:
        return 1, [
            f"REFUSING: cannot read the store's levels from {sidecar} ({_cause(r)}). Nothing was swapped; "
            "ROLLBACK_IGNORE_FORMAT does not cover levels.",
            f"If the cause is sudo asking for a password, add `cat {sidecar}` to the sudoers lines in {DOCS}.",
        ]
    store, err = parse_sidecar(r.stdout)
    if store is None:
        return 1, [f"REFUSING: {sidecar} does not parse ({err}). Nothing was swapped."]
    up = above(store, prev)
    if up:
        return 1, [
            f"REFUSING: the store is at levels {fmt(store)} ({sidecar}) and {o.bin}.prev serves {fmt(prev)}: "
            f"{', '.join(up)} above what it can read. A binary swap cannot go back past a level step; "
            "nothing was swapped.",
            "Roll back with `make rollback-restore`: it restores the pre-upgrade checkpoint (or "
            "ROLLBACK_ARCHIVE=<archive on the host>) and then installs .prev.",
        ]
    return 0, [
        f"store guard: store levels {fmt(store)}, {o.bin}.prev serves {fmt(prev)} ({how}); swap allowed."
    ]


@dataclass
class Source:
    levels: dict[str, int]
    describe: str
    release: str
    taken_at: str
    args: str


def _checkpoint_source(o: Opts, get: Getter) -> tuple[Source | None, list[str]]:
    pointer_path = f"{o.db}.migration-checkpoint"
    r = get("pointer")
    if r.missing:
        return None, [
            f"REFUSING: no checkpoint is recorded beside the store ({pointer_path} does not exist; a checkpoint "
            "is deleted 7 days after it stops being needed). Nothing was changed.",
            "Restore an archive instead: make rollback-restore ROLLBACK_ARCHIVE=<archive file on the host>.",
        ]
    if r.rc != 0 or r.sudo_refused:
        return None, [
            f"REFUSING: cannot read {pointer_path} ({_cause(r)}). Nothing was changed.",
            f"If the cause is sudo asking for a password, add `cat {pointer_path}` to the sudoers lines in {DOCS}.",
        ]
    try:
        pointer = json.loads(r.stdout)
        name = pointer["name"]
    except (ValueError, TypeError, KeyError):
        return None, [
            f"REFUSING: {pointer_path} is not a checkpoint pointer ({r.stdout.strip()[:200]!r}); a store last "
            "opened by a pre-core build records no checkpoint. Nothing was changed.",
            "Restore an archive instead: make rollback-restore ROLLBACK_ARCHIVE=<archive file on the host>.",
        ]
    if not isinstance(name, str) or not _CHECKPOINT_NAME.match(name):
        return None, [
            f"REFUSING: {pointer_path} names {name!r}, not a checkpoint name. Nothing was changed."
        ]
    rec_path = f"{o.cp_area}/{name}.json"
    rr = get("record:" + name)
    if rr.rc != 0 or rr.sudo_refused:
        return None, [
            f"REFUSING: cannot read the checkpoint record {rec_path} ({_cause(rr)}). Nothing was changed.",
            f"If the checkpoint directory is not the store's parent, set DEPLOY_CHECKPOINT_DIR to "
            f"core.storage.checkpoint_dir; if sudo asked for a password, add the record line in {DOCS}.",
        ]
    try:
        rec = json.loads(rr.stdout)
        levels = parse_vector(json.dumps(rec["from_vector"].get("levels") or {}))
    except (ValueError, TypeError, KeyError, AttributeError):
        levels = None
        rec = {}
    if levels is None:
        return None, [
            f"REFUSING: {rec_path} holds no readable from_vector levels. Nothing was changed."
        ]
    status = rec.get("status", "")
    if status in ("consumed", "restore_left"):
        return None, [
            f"REFUSING: checkpoint {name} is {status}: its copy is not the store the record describes. "
            "Nothing was changed.",
            "Restore an archive instead: make rollback-restore ROLLBACK_ARCHIVE=<archive file on the host>.",
        ]
    release = rec.get("from_release") or ""
    return Source(
        levels=levels,
        describe=f"checkpoint {name} (from_vector in {rec_path})",
        release=f"release {release} (the checkpoint's from_release)"
        if release
        else "the release you upgraded from (the checkpoint records no from_release)",
        taken_at=str(rec.get("taken_at") or "unknown"),
        args=f"--checkpoint {name}",
    ), []


def _archive_source(o: Opts, get: Getter) -> tuple[Source | None, list[str]]:
    clean = get("into_clean")
    if clean.rc != 0:
        return None, [
            f"REFUSING: cannot clear the scratch copy {o.into} ({_cause(clean)}). Nothing was changed.",
            f"Add the scratch `rm` line to the sudoers drop-in ({DOCS}).",
        ]
    x = get("into")
    if x.rc != 0:
        return None, [
            f"REFUSING: the current binary could not copy {o.archive} into the scratch area to read its levels "
            f"({_cause(x)}). Nothing was changed.",
            f"Check that the archive exists and is readable by {o.user}, and the sudoers line in {DOCS}.",
        ]
    r = get("into_levels")
    if r.rc != 0:
        return None, [
            f"REFUSING: cannot read the archived store's levels from {o.into}.storage-levels ({_cause(r)})."
        ]
    levels, err = parse_sidecar(r.stdout)
    if levels is None:
        return None, [
            f"REFUSING: {o.into}.storage-levels does not parse ({err}). Nothing was changed."
        ]
    return Source(
        levels=levels,
        describe=f"archive {o.archive} (levels read from its own store)",
        release="the release that was running when the archive was taken (this archive records no version)",
        taken_at="the archive's date",
        args=f"--archive {o.archive}",
    ), []


def decide_restore(o: Opts, get: Getter) -> tuple[int, list[str], str]:
    """Return (exit, stderr lines, the remote restore command or "")."""
    refusal = check_inflight(o, get)
    if refusal:
        return 1, refusal, ""
    src, lines = (_archive_source if o.archive else _checkpoint_source)(o, get)
    if src is None:
        return 1, lines, ""
    prev, how = prev_vector(o, get)
    if prev is None:
        return (
            1,
            [
                f"REFUSING: cannot tell which store levels {o.bin}.prev serves: {how}. Nothing was changed."
            ],
            "",
        )
    diff = differs(src.levels, prev)
    if diff:
        return (
            1,
            [
                f"REFUSING: the {src.describe} is at levels {fmt(src.levels)}, and {o.bin}.prev serves {fmt(prev)} "
                f"({', '.join(diff)} differ). That binary would refuse the restored store or migrate it again "
                "(DATA-9). Nothing was changed.",
                f"Install {src.release} as {o.bin}.prev, then run make rollback-restore again. "
                "(`make rollback-restore RELEASE=<tag>` fetches it once F8's updater fetch-release ships.)",
            ],
            "",
        )
    cmd = o.offline(f"{src.args} --accept-loss")
    return (
        0,
        [
            f"rollback-restore: {src.describe} matches {o.bin}.prev ({fmt(prev)}; {how}).",
            f"Restoring it discards every write made since it was taken ({src.taken_at}): imports, edits, and the "
            "listening progress the phone apps synced.",
            "WARNING: every credential change made since then is undone too. A key revoked, a user disabled or a "
            "password changed after that point is live again on the older release: repeat those revocations by "
            "hand once the rollback is up.",
        ],
        cmd,
    )


class SSH:
    """Runs the guard's commands on the host, each once, and caches them."""

    def __init__(self, o: Opts):
        self.o = o
        self.cache: dict[str, Result] = {}

    def __call__(self, key: str) -> Result:
        if key not in self.cache:
            name = key.split(":", 1)[1] if key.startswith("record:") else ""
            cmd = commands(self.o, name)["record" if name else key]
            try:
                p = subprocess.run(
                    ["ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10", self.o.host, cmd],
                    capture_output=True,
                    text=True,
                    timeout=600,
                    check=False,
                )
                self.cache[key] = Result(p.stdout, p.stderr, p.returncode)
            except subprocess.TimeoutExpired:
                self.cache[key] = Result("", "ssh timed out", 255)
        return self.cache[key]


def _validate(o: Opts) -> str:
    for label, p in (("--db", o.db), ("--bin", o.bin), ("--state-dir", o.state_dir)):
        if not _SAFE_PATH.match(p):
            return f"{label} {p!r} must be an absolute path of letters, digits and ._/@+-"
    for label, p in (("--checkpoint-dir", o.checkpoint_dir), ("--archive", o.archive)):
        if p and not _SAFE_PATH.match(p):
            return f"{label} {p!r} must be an absolute path of letters, digits and ._/@+-"
    if not _SAFE_USER.match(o.user):
        return f"--service-user {o.user!r} is not a user name"
    if not re.match(r"^[A-Za-z0-9@._:-]+$", o.unit):
        return f"--unit {o.unit!r} is not a unit name"
    for tok in o.offline_env.split():
        if not _SAFE_ENV.match(tok):
            return f"--offline-env token {tok!r} is not NAME=value"
    if not o.host or o.host.startswith("-"):
        return "--host is required"
    return ""


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    sub = parser.add_subparsers(dest="mode", required=True)
    for mode in ("rollback", "inflight", "restore", "stopped"):
        p = sub.add_parser(mode)
        p.add_argument("--host", required=True)
        p.add_argument("--state-dir", required=True)
        p.add_argument("--unit", default="audiobook-organizer.service")
        probe_only = mode in ("inflight", "stopped")
        p.add_argument("--db", default="/unused" if probe_only else None, required=not probe_only)
        p.add_argument("--bin", default="/unused" if probe_only else None, required=not probe_only)
        if mode == "restore":
            p.add_argument("--service-user", default="audiobook")
            p.add_argument("--checkpoint-dir", default="")
            p.add_argument("--archive", default="")
            p.add_argument("--offline-env", default="")
    a = parser.parse_args(argv)
    o = Opts(
        host=a.host.strip(),
        db=a.db.strip(),
        bin=a.bin.strip(),
        state_dir=a.state_dir.strip(),
        unit=a.unit.strip(),
    )
    if a.mode == "restore":
        o.user, o.checkpoint_dir = a.service_user.strip(), a.checkpoint_dir.strip()
        o.archive, o.offline_env = a.archive.strip(), a.offline_env.strip()
    bad = _validate(o)
    if bad:
        print(f"storage_format_guard: {bad}", file=sys.stderr)
        return 2
    get = SSH(o)
    if a.mode in ("inflight", "stopped"):
        lines = (check_inflight if a.mode == "inflight" else check_stopped)(o, get)
        for line in lines:
            print(line, file=sys.stderr)
        return 1 if lines else 0
    if a.mode == "rollback":
        ignore = os.environ.get("ROLLBACK_IGNORE_FORMAT", "").strip() == "1"
        code, lines = decide_rollback(o, get, ignore)
        for line in lines:
            print(line, file=sys.stdout if code == 0 else sys.stderr)
        return code
    try:
        code, lines, cmd = decide_restore(o, get)
    finally:
        if o.archive and "into" in get.cache:
            # The scratch copy holds the store's secrets: never leave it.
            done = SSH(o)("into_clean")
            if done.rc != 0:
                print(
                    f"WARNING: could not delete the scratch copy {o.into} ({_cause(done)}); the checkpoint "
                    "sweeper deletes it 7 days after its last change.",
                    file=sys.stderr,
                )
    for line in lines:
        print(line, file=sys.stderr)
    if code == 0:
        print(cmd)
    return code


if __name__ == "__main__":
    sys.exit(main())
