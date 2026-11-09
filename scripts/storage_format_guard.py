#!/usr/bin/env python3
# file: scripts/storage_format_guard.py
# version: 2.2.0
# guid: 35696d63-9d8e-4fc3-b6a8-9c9770cd798c
# last-edited: 2026-11-09

"""The store guard of `make rollback` and `make rollback-restore`.

05-storage §5S.3.7 "The binary-swap guard", §5S.3.12 row `make
rollback-restore`, 09 §9.2.1 "Rollback"; R1-27. The name is kept from HEAD's
storage-format guard, which this replaces.

This module decides; scripts/deploy_rollback.py runs the recipes and loads
it from ROLLBACK_GUARD (so the recipe tests can run a mutated copy). Every
remote command either recipe runs is in `commands()`, and `sudoers()`
renders the deploy user's sudoers drop-in from the same table, so the two
cannot drift (scripts/tests/test_make_rollback.py checks every sudo command
the recipes run against it, and the doc's block against its output).

Privileged steps go through the root-owned helpers in HELPER_DIR
(deploy/rollback-helpers/), each granted with no argument list and each
validating its own arguments; plain `sudo -n` lines remain only for
commands with fixed arguments (the sidecar reads, systemctl stop, start and
restart of the one unit). The helpers take their paths from the host's
HOST_CONFIG, which the recipes read first (it is world-readable and holds no
secret) and compare with DEPLOY_BIN, DEPLOY_DB and DEPLOY_UNIT.

The decisions:

- `check_inflight`: the in-flight probe (the inflight-probe helper runs
  `find <state_dir> -maxdepth 3 -path '<state_dir>/restore/*/inflight.json'
  -print`). Any output is a swap in flight: refuse, no override ("boot the
  current build once to finish it"). A pending intent
  (`restore/pending.json`) does not match and passes: the older build
  rejects it at its own boot. A non-zero exit, or stderr starting with
  `sudo:`, refuses.
- `decide_rollback`: the store's levels from `<db>.storage-levels` (lines
  `<owner> <level>`), against what `.prev` serves (`--print-storage-levels`,
  one JSON object; an R0 binary rejects the flag and is asked
  `--print-storage-format`: format 1, or no such flag either, serves every
  owner at 0; any other answer is not guessed and refuses). An owner absent
  from a vector is at level 0. Refuses when any store level is above
  `.prev`'s, naming `make rollback-restore`. Only a store no core build has
  opened (no `.storage-levels` AND no `<db>.migration-checkpoint`) falls
  back to HEAD's projection sidecar `<db>.storage-format` and its old rules,
  ROLLBACK_IGNORE_FORMAT=1 included (it covers only an unreadable projection
  sidecar, never levels, never the probe).
- `decide_restore`: the probe, then the source: the latest checkpoint
  (`<db>.migration-checkpoint` names it; the checkpoint-record helper prints
  its record, which must be restorable as core's Checkpoint.Restorable()
  says and carry `from_vector.levels`), or with ROLLBACK_ARCHIVE an archive
  in the helpers' archive_dir, whose levels the restore-offline helper reads
  from a scratch copy (`into-check`). The source's levels must EQUAL
  `.prev --print-storage-levels`, else it refuses naming the release to
  install.
- `check_stopped`: the unit must read inactive or failed; an unreadable unit
  is a state unknown, and refuses too.

Text from the host is printed with its control characters removed.

Exit status of the CLI: 0 printed; 2 bad arguments. The one subcommand,
`sudoers`, prints the drop-in for the given values.

Standard library only. Tests: scripts/test_storage_format_guard.py (the
decisions) and scripts/tests/test_make_rollback.py (the recipes over a fake
host).
"""

from __future__ import annotations

import argparse
import json
import os
import re
import shlex
import subprocess
import sys
from dataclasses import dataclass
from typing import Callable

DOCS = "docs/system/deploy-and-gpu-ops.md (Rollback helpers, sudoers)"
HELPER_DIR = "/usr/local/libexec/audiobook-organizer/rollback"
HOST_CONFIG = "/etc/audiobook-organizer/rollback.conf"
TRANSIENT_UNIT = "audiobook-organizer-rollback-restore"
SSH_OPTS = [
    "-o",
    "BatchMode=yes",
    "-o",
    "ConnectTimeout=10",
    "-o",
    "ServerAliveInterval=15",
    "-o",
    "ServerAliveCountMax=4",
]
# Host paths go into shell commands and sudoers lines unquoted: allow only
# characters that need no quoting.
_SAFE_PATH = re.compile(r"^/[A-Za-z0-9._/@+-]+$")
_SAFE_USER = re.compile(r"^[a-z_][a-z0-9_-]*$")
# 05-storage §5S.3.12 "Where": the checkpoint name grammar (fullmatch: `$`
# would let a trailing newline through).
_CHECKPOINT_NAME = re.compile(r"[0-9]{8}T[0-9]{6}Z-[0-9a-f]{8}")
_ARCHIVE_NAME = re.compile(r"[A-Za-z0-9_][A-Za-z0-9._-]{0,199}")
_CONTROL = re.compile(r"[\x00-\x08\x0b-\x1f\x7f-\x9f]")
# core/storage/checkpoints.go Restorable(): the statuses whose copy is still
# the store the record describes.
_RESTORABLE = ("open", "served", "completed")
_OLD_LEVELS_FLAG = re.compile(r"unknown flag: --print-storage-levels(\s|$)")
_OLD_FORMAT_FLAG = re.compile(r"unknown flag: --print-storage-format(\s|$)")


def clean(text: str) -> str:
    """Host text with control characters removed (newlines and tabs kept)."""
    return _CONTROL.sub("", text or "")


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
    unit: str = "audiobook-organizer.service"
    # From the host's HOST_CONFIG (load_host_config):
    state_dir: str = ""
    user: str = "audiobook"
    checkpoint_dir: str = ""
    archive_dir: str = ""
    # ROLLBACK_ARCHIVE: an archive's file name inside archive_dir.
    archive: str = ""

    @property
    def cp_area(self) -> str:
        return (self.checkpoint_dir or os.path.dirname(self.db)).rstrip(
            "/"
        ) + "/.mediacore-checkpoints"

    @property
    def transient_log(self) -> str:
        return "/run/audiobook-organizer-rollback/restore.log"


def commands(o: Opts, name: str = "") -> dict[str, str]:
    """Every remote command the rollback recipes run, by key, plus the
    helpers Makefile.local.example's deploy, deploy-debug and pprof-off run
    (the install_* and pprof_* keys; their recipes spell them out, and
    test_make_rollback.py checks those against sudoers()). `name` is the
    checkpoint name for the record and restore keys."""
    h = HELPER_DIR
    return {
        "host_config": f"cat {HOST_CONFIG}",
        "prev_exists": f"test -f {o.bin}.prev",
        "probe": f"sudo -n {h}/inflight-probe",
        "levels": f"sudo -n /usr/bin/cat {o.db}.storage-levels",
        "format": f"sudo -n /usr/bin/cat {o.db}.storage-format",
        "pointer": f"sudo -n /usr/bin/cat {o.db}.migration-checkpoint",
        "prev_levels": f"{o.bin}.prev --print-storage-levels",
        "prev_format": f"{o.bin}.prev --print-storage-format",
        "prev_caps": f"{o.bin}.prev --print-capabilities",
        "current_format": f"{o.bin} --print-storage-format",
        "record": f"sudo -n -u {o.user} {h}/checkpoint-record {name}",
        "into": f"sudo -n {h}/restore-offline into-check {o.archive}",
        "restore_checkpoint": f"sudo -n {h}/restore-offline checkpoint {name}",
        "restore_archive": f"sudo -n {h}/restore-offline archive {o.archive}",
        "unit": f"systemctl show -p ActiveState,SubState {o.unit}",
        "invocation": f"systemctl show -p InvocationID --value {o.unit}",
        "stop": f"sudo -n /usr/bin/systemctl stop {o.unit}",
        "start": f"sudo -n /usr/bin/systemctl start {o.unit}",
        "restart": f"sudo -n /usr/bin/systemctl restart {o.unit}",
        "unit_type_simple": f"sudo -n {h}/unit-type simple",
        "unit_type_notify": f"sudo -n {h}/unit-type notify",
        "swap": f"sudo -n {h}/swap-prev",
        "install_binary": f"sudo -n {h}/install-staged binary",
        "install_unit": f"sudo -n {h}/install-staged unit",
        "install_local_conf": f"sudo -n {h}/install-staged local-conf",
        "pprof_on": f"sudo -n {h}/pprof on",
        "pprof_off": f"sudo -n {h}/pprof off",
    }


def sudoers(o: Opts, deploy_user: str) -> str:
    """The deploy user's sudoers drop-in for deploy, deploy-debug, pprof-off
    and both rollbacks, rendered from commands(): one line
    per command with fixed arguments (sudo matches them exactly), each helper
    that takes arguments granted bare (in sudoers that admits any arguments:
    the helper validates them), each helper that takes none granted with ""
    (none admitted). No `*` anywhere: in sudoers a `*` in arguments also
    matches spaces and `/`, which made the old find line a root shell."""
    c = commands(o, "NAME")
    root: list[str] = []
    as_user: list[str] = []
    for key in (
        "levels",
        "format",
        "pointer",
        "stop",
        "start",
        "restart",
        "probe",
        "swap",
        "restore_checkpoint",
        "unit_type_simple",
        "install_unit",
        "pprof_on",
        "record",
    ):
        argv = shlex.split(c[key])
        user = "root"
        argv = argv[2:]  # sudo -n
        if argv[0] == "-u":
            user, argv = argv[1], argv[2:]
        if argv[0].startswith(HELPER_DIR + "/"):
            spec = (
                argv[0]
                if key
                in ("restore_checkpoint", "unit_type_simple", "install_unit", "pprof_on", "record")
                else f'{argv[0]} ""'
            )
        else:
            spec = " ".join(argv)
        (root if user == "root" else as_user).append(spec)

    def block(runas: str, specs: list[str]) -> str:
        return f"{deploy_user} ALL=({runas}) NOPASSWD: " + ", \\\n    ".join(specs)

    return block("root", root) + "\n" + block(o.user, as_user) + "\n"


def parse_host_config(text: str) -> tuple[dict[str, str] | None, str]:
    """Parse HOST_CONFIG's `key = value` lines (deploy/rollback-helpers)."""
    cfg: dict[str, str] = {}
    for n, raw in enumerate(text.splitlines(), 1):
        line = raw.strip()
        if not line or line.startswith("#"):
            continue
        key, sep, value = line.partition("=")
        if not sep:
            return None, f"line {n} is not `key = value`"
        cfg[key.strip()] = value.strip()
    for key in ("bin", "db", "state_dir", "unit", "service_user", "archive_dir"):
        if not cfg.get(key):
            return None, f"{key} is not set"
    return cfg, ""


def load_host_config(o: Opts, get: Getter) -> list[str]:
    """Read HOST_CONFIG, fill o's host-side values, and return refusal lines
    when it is missing or names another binary, store or unit."""
    r = get("host_config")
    if r.rc != 0:
        return [
            f"REFUSING: cannot read the rollback helpers' config {HOST_CONFIG} on {o.host} ({_cause(r)}). "
            "Nothing was changed.",
            f"Install the rollback helpers and their config first ({DOCS}).",
        ]
    cfg, err = parse_host_config(clean(r.stdout))
    if cfg is None:
        return [f"REFUSING: {HOST_CONFIG} on {o.host} does not parse ({err}). Nothing was changed."]
    differ = [
        f"{label}={mine} but {HOST_CONFIG} has {key} = {cfg[key]}"
        for label, mine, key in (
            ("DEPLOY_BIN", o.bin, "bin"),
            ("DEPLOY_DB", o.db, "db"),
            ("DEPLOY_UNIT", o.unit, "unit"),
        )
        if mine != cfg[key]
    ]
    if differ:
        return [
            "REFUSING: the rollback helpers act on the paths in their config, and they differ from this make "
            f"run's: {'; '.join(differ)}. Nothing was changed.",
            "Fix whichever one is wrong, so the guard checks the store and binary the helpers change.",
        ]
    o.state_dir, o.user = cfg["state_dir"], cfg["service_user"]
    o.checkpoint_dir, o.archive_dir = cfg.get("checkpoint_dir", ""), cfg["archive_dir"]
    bad = _validate(o)
    if bad:
        return [f"REFUSING: {HOST_CONFIG}: {bad}. Nothing was changed."]
    return []


def _cause(r: Result) -> str:
    lines = [ln.strip() for ln in clean(r.stderr).splitlines() if ln.strip()]
    parts = [f"exit {r.rc}"]
    out = clean(r.stdout).strip()
    if out:
        parts.append(f"stdout {out[:200]!r}")
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
            f"It runs `{cmd}` on {o.host}: install the rollback helpers and their sudoers line ({DOCS}), and "
            f"check that state_dir in {HOST_CONFIG} ({o.state_dir}) is the service's state directory.",
        ]
    found = [ln.strip() for ln in clean(r.stdout).splitlines() if ln.strip()]
    if found:
        return [
            f"REFUSING: a restore or reset swap is in flight ({', '.join(found)}). Nothing was changed, and "
            "nothing overrides this.",
            f"Boot the current build once to finish it (sudo systemctl start {o.unit}), then decide again.",
        ]
    return []


def unit_state(o: Opts, get: Getter) -> tuple[str | None, str]:
    """(ActiveState, a description) or (None, why it could not be read)."""
    r = get("unit")
    props = dict(ln.partition("=")[::2] for ln in clean(r.stdout).splitlines() if "=" in ln)
    state = props.get("ActiveState", "").strip()
    if r.rc != 0 or not state:
        return None, _cause(r)
    return state, f"ActiveState={state} SubState={props.get('SubState', '').strip()}"


def check_stopped(o: Opts, get: Getter) -> list[str]:
    """Return refusal lines unless the unit reads inactive or failed (the
    rollback-restore hold: after the stop, and again after the restore)."""
    state, seen = unit_state(o, get)
    if state in ("inactive", "failed"):
        return []
    if state is None:
        return [
            f"REFUSING: cannot read {o.unit}'s state ({seen}), so whether it stayed stopped is unknown; "
            "the recipe stopped here."
        ]
    return [
        f"REFUSING: {o.unit} did not stay stopped ({seen}): something started it; the recipe stopped here."
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
        pointer = get("pointer")
        if not pointer.missing:
            return 1, [
                f"REFUSING: {sidecar} does not exist, but the store has a checkpoint pointer "
                f"({o.db}.migration-checkpoint{': ' + _cause(pointer) if pointer.rc else ''}), so a core build "
                "opened and upgraded it: the levels file is missing, not a store from before core. Nothing was "
                "swapped, and the projection rule does not apply.",
                "Find out what removed the levels file before rolling back; `make rollback-restore` restores the "
                "pre-upgrade checkpoint if the store must go back.",
            ]
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
    restore_cmd: str


def _checkpoint_source(o: Opts, get: Getter) -> tuple[Source | None, list[str]]:
    pointer_path = f"{o.db}.migration-checkpoint"
    r = get("pointer")
    if r.missing:
        return None, [
            f"REFUSING: no checkpoint is recorded beside the store ({pointer_path} does not exist; a checkpoint "
            "is deleted 7 days after it stops being needed). Nothing was changed.",
            f"Restore an archive instead: make rollback-restore ROLLBACK_ARCHIVE=<file name in {o.archive_dir}>.",
        ]
    if r.rc != 0 or r.sudo_refused:
        return None, [
            f"REFUSING: cannot read {pointer_path} ({_cause(r)}). Nothing was changed.",
            f"If the cause is sudo asking for a password, add `/usr/bin/cat {pointer_path}` from {DOCS}.",
        ]
    try:
        pointer = json.loads(r.stdout)
        name = pointer["name"]
    except (ValueError, TypeError, KeyError):
        return None, [
            f"REFUSING: {pointer_path} is not a checkpoint pointer ({clean(r.stdout).strip()[:200]!r}); a store "
            "last opened by a pre-core build records no checkpoint. Nothing was changed.",
            f"Restore an archive instead: make rollback-restore ROLLBACK_ARCHIVE=<file name in {o.archive_dir}>.",
        ]
    if not isinstance(name, str) or not _CHECKPOINT_NAME.fullmatch(name):
        return None, [
            f"REFUSING: {pointer_path} names {clean(repr(name))[:200]}, not a checkpoint name. Nothing was changed."
        ]
    rec_path = f"{o.cp_area}/{name}.json"
    rr = get("record:" + name)
    if rr.rc != 0 or rr.sudo_refused:
        return None, [
            f"REFUSING: cannot read the checkpoint record {rec_path} ({_cause(rr)}). Nothing was changed.",
            f"If the checkpoint directory is not the store's parent, set checkpoint_dir in {HOST_CONFIG} to "
            f"core.storage.checkpoint_dir; if sudo asked for a password, add the checkpoint-record line ({DOCS}).",
        ]
    try:
        rec = json.loads(rr.stdout)
    except ValueError:
        rec = None
    fv = rec.get("from_vector") if isinstance(rec, dict) else None
    raw_levels = fv.get("levels") if isinstance(fv, dict) else None
    levels = parse_vector(json.dumps(raw_levels)) if isinstance(raw_levels, dict) else None
    if levels is None:
        return None, [
            f"REFUSING: {rec_path} holds no readable from_vector levels (core always writes them), so which "
            "levels the checkpoint is at is unknown. Nothing was changed."
        ]
    status = rec.get("status", "")
    released = status in ("open", "served") and rec.get("delete_after") is not None
    if status not in _RESTORABLE or released:
        why = f"{status} and released (delete_after is set)" if released else repr(status)
        return None, [
            f"REFUSING: checkpoint {name} is {why}: its copy is not the store the record describes, and "
            "storage restore-offline would refuse it (core's Checkpoint.Restorable). Nothing was changed.",
            f"Restore an archive instead: make rollback-restore ROLLBACK_ARCHIVE=<file name in {o.archive_dir}>.",
        ]
    release = clean(str(rec.get("from_release") or ""))
    return Source(
        levels=levels,
        describe=f"checkpoint {name} (from_vector in {rec_path})",
        release=f"release {release} (the checkpoint's from_release)"
        if release
        else "the release you upgraded from (the checkpoint records no from_release)",
        taken_at=clean(str(rec.get("taken_at") or "unknown")),
        restore_cmd=commands(o, name)["restore_checkpoint"],
    ), []


def _archive_source(o: Opts, get: Getter) -> tuple[Source | None, list[str]]:
    path = f"{o.archive_dir}/{o.archive}"
    x = get("into")
    out = clean(x.stdout)
    if x.rc != 0 or "LEVELS-BEGIN\n" not in out or "\nLEVELS-END" not in out:
        return None, [
            f"REFUSING: could not read the levels of the archived store {path} from a scratch copy "
            f"({_cause(x)}). Nothing was changed.",
            f"Check that the archive exists and is readable by {o.user}, and that the helpers are installed ({DOCS}).",
        ]
    text = out.split("LEVELS-BEGIN\n", 1)[1].split("\nLEVELS-END", 1)[0]
    levels, err = parse_sidecar(text)
    if levels is None:
        return None, [
            f"REFUSING: the archived store's .storage-levels does not parse ({err}). Nothing was changed."
        ]
    return Source(
        levels=levels,
        describe=f"archive {path} (levels read from its own store)",
        release="the release that was running when the archive was taken (this archive records no version)",
        taken_at="the archive's date",
        restore_cmd=commands(o)["restore_archive"],
    ), []


def decide_restore(o: Opts, get: Getter) -> tuple[int, list[str], str]:
    """Return (exit, lines, the remote restore command or "")."""
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
        src.restore_cmd,
    )


class SSH:
    """Runs the guard's reads on the host, each once, and caches them."""

    def __init__(self, o: Opts):
        self.o = o
        self.cache: dict[str, Result] = {}

    def __call__(self, key: str) -> Result:
        if key not in self.cache:
            name = key.split(":", 1)[1] if key.startswith("record:") else ""
            cmd = commands(self.o, name)["record" if name else key]
            # into-check extracts a whole archive; everything else is a read.
            self.cache[key] = run_remote(self.o.host, cmd, timeout=None if key == "into" else 120)
        return self.cache[key]


def run_remote(host: str, cmd: str, timeout: float | None = 120, stream: bool = False) -> Result:
    """Run cmd on host over ssh. stream: pass its output through as it comes
    (a long restore) instead of capturing it."""
    argv = ["ssh", *SSH_OPTS, host, cmd]
    try:
        if stream:
            p = subprocess.run(argv, timeout=timeout, check=False)
            return Result("", "", p.returncode)
        p = subprocess.run(argv, capture_output=True, text=True, timeout=timeout, check=False)
        return Result(p.stdout, p.stderr, p.returncode)
    except subprocess.TimeoutExpired:
        return Result("", f"ssh did not finish within {timeout}s", 255)


def _validate(o: Opts) -> str:
    for label, p in (
        ("bin", o.bin),
        ("db", o.db),
        ("state_dir", o.state_dir),
        ("archive_dir", o.archive_dir),
    ):
        if p and (not _SAFE_PATH.match(p) or ".." in p.split("/")):
            return f"{label} {p!r} must be an absolute path of letters, digits and ._/@+-"
    if o.checkpoint_dir and not _SAFE_PATH.match(o.checkpoint_dir):
        return f"checkpoint_dir {o.checkpoint_dir!r} must be an absolute path of letters, digits and ._/@+-"
    if o.archive and (not _ARCHIVE_NAME.fullmatch(o.archive) or ".." in o.archive):
        return f"ROLLBACK_ARCHIVE {o.archive!r} must be a file name in archive_dir (letters, digits and ._-, no /)"
    if not _SAFE_USER.match(o.user) or o.user == "root":
        return f"service_user {o.user!r} is not the service's user"
    if not re.fullmatch(r"[A-Za-z0-9@._:-]+\.service", o.unit):
        return f"unit {o.unit!r} is not a .service unit name"
    if not o.host or o.host.startswith("-") or not re.fullmatch(r"[A-Za-z0-9@._:-]+", o.host):
        return f"host {o.host!r} is not a host name"
    return ""


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    sub = parser.add_subparsers(dest="mode", required=True)
    p = sub.add_parser("sudoers", help="print the deploy user's sudoers drop-in")
    p.add_argument("--deploy-user", required=True)
    p.add_argument("--db", required=True)
    p.add_argument("--bin", default="/usr/local/bin/audiobook-organizer")
    p.add_argument("--unit", default="audiobook-organizer.service")
    p.add_argument("--service-user", default="audiobook")
    a = parser.parse_args(argv)
    o = Opts(host="localhost", db=a.db, bin=a.bin, unit=a.unit, user=a.service_user)
    bad = _validate(o) or (
        "" if _SAFE_USER.match(a.deploy_user) else f"--deploy-user {a.deploy_user!r}"
    )
    if bad:
        print(f"storage_format_guard: {bad}", file=sys.stderr)
        return 2
    sys.stdout.write(sudoers(o, a.deploy_user))
    return 0


if __name__ == "__main__":
    sys.exit(main())
