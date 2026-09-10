#!/usr/bin/env python3
# file: scripts/finish_credential_migration.py
# version: 2.0.0
# guid: 5c8b2e14-9a37-4d06-b8f1-2e74a95c30d8
# last-edited: 2026-09-09

"""Finish the 2026-09-09 credential relocation on a deployed host.

PR #3171 pinned the settings encryption key, ``.bootstrap-token`` and
``.readonly-key`` to a constant directory (``/var/lib/audiobook-organizer``)
instead of ``filepath.Dir(database_path)``. The code change landed; the
filesystem tasks did not, because they need root:

1. Delete stale ``.bootstrap-token`` files. Token files are NEVER cleaned up on
   restart -- only overwritten in whichever directory is in use at the time. So
   a file at a previously-used path sits there indefinitely and ``sudo cat``
   still returns a well-formed value that expired ten minutes after it was
   written. Following the runbook against one of those yields a plausible token
   and a bare ``401 invalid bootstrap token``.
2. Move ``.encryption_key`` to the new directory, so the server stops depending
   on its legacy-location fallback.
3. Verify the read-only key landed where it should.

WHY THIS IS A SCRIPT AND NOT THREE COMMANDS -- the ordering trap
================================================================
Task 2 is destructive if done before the new binary is deployed.

A pre-#3171 binary looks for the key ONLY at ``filepath.Dir(database_path)``.
Move the key out from under it and on the next restart it finds nothing,
generates a fresh key, and ``config/persistence.go`` then re-encrypts the four
secrets it can recover from the config file and calls ``DeleteSetting`` on every
other one. The process exits 0 and looks healthy. That is how secrets were lost
the first time.

THE SAFE ORDER IS: DEPLOY FIRST, MOVE SECOND -- and there is no rush
-------------------------------------------------------------------
Deploying #3171 while the key is still at the old path is SAFE and does not
need this script to run first. ``InitEncryption`` probes its ``legacyDirs``
and *uses* the key it finds there, logging "settings encryption key found at
its OLD location". So the deploy is not gated on the move; the move is gated
on the deploy. Do not sequence these backwards out of anxiety.

WHAT UNLOCKS THE MOVE
---------------------
Not "is #3171 merged" -- a green branch that was never deployed must not
unlock this. Two signals, both read from the host:

* the ``ABK_STATE_DIR`` string is present in the deployed executable. This is
  a cheap pre-check only: it proves the string is linked in, NOT that the
  running process uses the new directory.
* the CURRENTLY RUNNING invocation wrote its ``.bootstrap-token`` into the new
  directory. That is behavioural proof that the process resolving paths right
  now is post-#3171. Since deploying involves a restart anyway, requiring this
  costs nothing and is far stronger than inspecting bytes on disk.

The destination is then READ from that same observation rather than asserted.
This script hardcoding the destination would make it a FOURTH independent
derivation of the credential directory, in a change whose entire point is that
three such derivations diverged. ``EnsureSecureStateDir`` has a fallback branch;
if the running process disagrees with the constant, that is a refusal, not
something to paper over.

WHAT THIS SCRIPT WILL NOT DO
----------------------------
* It never restarts the service. A restart resumes the interrupted library
  scan, which is its own hazard and not this script's call to make. Nothing
  here requires one: the running process already holds the key in memory.
* It never deletes the source key. The move RENAMES it aside, because it is the
  one file in this system that cannot be regenerated and a leftover copy is
  inert once the new path works. Removing it is a separate ``--remove-legacy-key``
  run, gated on positive proof that the app has since read the new location.
* It never treats an unreadable path as an absent one. ``Path.exists()`` returns
  False on EACCES, and the app-data directory is 0700 ``audiobook:audiobook``,
  so a non-root run is BLIND to everything in it. Every filesystem question
  here is tri-state: present, absent, or unknown.

Usage (on the server)::

    sudo python3 scripts/finish_credential_migration.py            # dry run
    sudo python3 scripts/finish_credential_migration.py --apply
    sudo python3 scripts/finish_credential_migration.py --remove-legacy-key --apply

Exit codes: 0 success/clean, 1 refused (a precondition failed), 2 usage/environment error.
"""

from __future__ import annotations

import argparse
import enum
import errno
import hashlib
import json
import os
import pwd
import re
import shutil
import subprocess
import sys
import time
from dataclasses import dataclass, field
from pathlib import Path

SERVICE = "audiobook-organizer.service"
BINARY = Path("/usr/local/bin/audiobook-organizer")
NEW_STATE_DIR = Path("/var/lib/audiobook-organizer")
TOKEN_NAME = ".bootstrap-token"
KEY_NAME = ".encryption_key"
READONLY_NAME = ".readonly-key"

# The string a post-#3171 binary contains: the env var name from
# internal/config/state_dir.go. It cannot appear in an older build.
CAPABILITY_MARKER = b"ABK_STATE_DIR"

# Journal needles. Both ASCII on purpose -- the real log line for the legacy
# key contains an em dash, and shipping a non-ASCII regex through ssh and
# journalctl's PCRE is an encoding problem nobody needs. `expected_at=` is a
# structured field of that one message.
TOKEN_NEEDLE = "token_file="
LEGACY_KEY_NEEDLE = "expected_at="

MIGRATED_PREFIX = KEY_NAME + ".migrated-"


class Refused(Exception):
    """A precondition failed. Nothing was changed."""


class Presence(enum.Enum):
    PRESENT = "present"
    ABSENT = "absent"
    UNKNOWN = "unknown"


@dataclass
class Probe:
    """A tri-state answer to "is this file there?".

    UNKNOWN exists because ``Path.exists()`` cannot distinguish "no such file"
    from "I am not allowed to look", and this script decides what to do with
    credentials. Reading EACCES as "absent" would let a non-root run conclude
    the encryption key does not exist -- and the note it printed about that
    would be a claim about a directory it never saw.
    """

    presence: Presence
    path: Path
    reason: str = ""

    @property
    def present(self) -> bool:
        return self.presence is Presence.PRESENT

    @property
    def absent(self) -> bool:
        return self.presence is Presence.ABSENT

    @property
    def unknown(self) -> bool:
        return self.presence is Presence.UNKNOWN

    def describe(self) -> str:
        if self.present:
            return f"{self.path}: present"
        if self.absent:
            return f"{self.path}: absent"
        return f"{self.path}: UNKNOWN ({self.reason})"


def probe(path: Path) -> Probe:
    """Tri-state stat. ``lstat`` so a dangling symlink reads as present."""
    try:
        os.lstat(path)
    except FileNotFoundError:
        return Probe(Presence.ABSENT, path)
    except NotADirectoryError:
        # A path component is a file. Genuinely nothing can be here.
        return Probe(Presence.ABSENT, path, "a parent component is not a directory")
    except PermissionError as exc:
        return Probe(
            Presence.UNKNOWN,
            path,
            f"permission denied ({os.strerror(exc.errno or errno.EACCES)}) -- "
            f"re-run as root",
        )
    except OSError as exc:
        return Probe(Presence.UNKNOWN, path, f"stat failed: {exc}")
    return Probe(Presence.PRESENT, path)


@dataclass
class Journal:
    """What the CURRENTLY RUNNING invocation said about its own paths."""

    invocation: str | None = None
    token_file: Path | None = None
    token_expires: str | None = None
    legacy_key_line: str | None = None
    problem: str | None = None


@dataclass
class Plan:
    db_path: Path | None = None
    journal: Journal = field(default_factory=Journal)
    binary_has_marker: bool = False
    observed_state_dir: Path | None = None

    live_token: Probe | None = None
    stale_tokens: list[Probe] = field(default_factory=list)

    key_src: Probe | None = None
    key_dst: Probe | None = None
    migrated_keys: list[Path] = field(default_factory=list)

    # Why the key move is not available. Empty means it is.
    key_blockers: list[str] = field(default_factory=list)
    notes: list[str] = field(default_factory=list)
    left_alone: list[str] = field(default_factory=list)


def run(cmd: list[str], *, check: bool = True) -> tuple[int, str, str]:
    """Run a command and decode its output leniently.

    Deliberately NOT ``text=True``. That decodes as strict UTF-8, and this
    host's journal contains at least one invalid continuation byte -- a book
    title in some scanner log line, most likely. Strict decoding turns that
    into a ``UnicodeDecodeError`` raised from inside ``communicate()``, which
    kills a read-only inspection script for a reason that has nothing to do
    with what it was inspecting. Every pattern matched here is ASCII, so
    replacing undecodable bytes cannot change any answer.
    """
    proc = subprocess.run(cmd, capture_output=True)
    out = proc.stdout.decode("utf-8", errors="replace")
    err = proc.stderr.decode("utf-8", errors="replace")
    if check and proc.returncode != 0:
        raise Refused(f"command failed ({proc.returncode}): {' '.join(cmd)}\n{err.strip()}")
    return proc.returncode, out, err


def unit_property(name: str) -> str:
    _, out, _ = run(["systemctl", "show", SERVICE, "-p", name, "--value"], check=False)
    return out.strip()


def service_user() -> str:
    """The user the unit runs as -- the owner every file here must keep."""
    return unit_property("User") or "audiobook"


def database_path() -> Path | None:
    """DATABASE_PATH as the RUNNING service sees it.

    Read from the merged unit rather than from any .env or repo file: drop-ins
    override the base unit, and the last Environment= wins. Guessing from a
    checkout is how you end up reasoning about a host you are not on.
    """
    out = unit_property("Environment")
    for tok in re.findall(r"DATABASE_PATH=(\S+)", out):
        return Path(tok.strip("'\""))
    return None


def process_start_time() -> float | None:
    """When the running main process started, as a unix timestamp.

    ``/proc/<pid>`` is created at process creation and its mtime is that
    moment, which is both simpler and less brittle than parsing systemd's
    human-formatted ``ExecMainStartTimestamp``.
    """
    pid = unit_property("MainPID")
    if not pid.isdigit() or int(pid) == 0:
        return None
    try:
        return os.stat(f"/proc/{pid}").st_mtime
    except OSError:
        return None


def read_journal() -> Journal:
    """Read the two lines that matter, scoped to the running invocation.

    Scoping by ``_SYSTEMD_INVOCATION_ID`` is not just an optimisation (0.3s
    against 2m27s for a 90-day window on this host, because otherwise
    journalctl streams every retained line for the unit to this process). It is
    also more CORRECT: the live token is by definition the one the running
    process wrote, and the last ``token_file=`` line in a wide window can
    easily belong to an earlier invocation.

    ``--grep`` filters inside journalctl rather than here, so only the handful
    of matching lines cross the pipe.
    """
    j = Journal()
    j.invocation = unit_property("InvocationID") or None
    if not j.invocation:
        j.problem = (
            f"{SERVICE} reports no InvocationID -- it is not running, so there is no "
            "live token and nothing can be proven about which binary resolves paths"
        )
        return j

    rc, out, err = run(
        [
            "journalctl",
            f"_SYSTEMD_INVOCATION_ID={j.invocation}",
            "--no-pager",
            "-o",
            "cat",
            f"--grep={TOKEN_NEEDLE}|{LEGACY_KEY_NEEDLE}",
        ],
        check=False,
    )
    # journalctl exits 1 for "no entries matched", which is a legitimate empty
    # result, not a failure. Anything else is the tool itself complaining, and
    # must NOT be read as "the log says nothing".
    if rc not in (0, 1):
        j.problem = f"journalctl failed (rc={rc}): {err.strip() or 'no stderr'}"
        return j

    for line in out.splitlines():
        if TOKEN_NEEDLE in line:
            m = re.search(r"token_file=(\S+)", line)
            if m:
                j.token_file = Path(m.group(1).strip('"'))
            exp = re.search(r"expires_at=(\S+)", line)
            j.token_expires = exp.group(1).strip('"') if exp else None
        elif LEGACY_KEY_NEEDLE in line:
            j.legacy_key_line = line.strip()
    return j


def binary_has_marker(binary: Path) -> bool:
    """Does the DEPLOYED artifact contain the new-state-dir marker?

    A byte search of the executable, deliberately: the question is what is on
    this host, and ``git log`` cannot answer it. This is a PRE-CHECK only --
    a linked-in string does not prove the running process uses the new
    directory. See the module docstring.
    """
    p = probe(binary)
    if p.unknown:
        raise Refused(f"cannot read the deployed binary -- {p.describe()}")
    if p.absent:
        raise Refused(f"binary not found at {binary}; pass --binary if it lives elsewhere")
    with binary.open("rb") as fh:
        # Read in chunks with an overlap so the marker cannot straddle a boundary.
        overlap = len(CAPABILITY_MARKER)
        prev = b""
        while chunk := fh.read(4 << 20):
            if CAPABILITY_MARKER in prev + chunk:
                return True
            prev = chunk[-overlap:]
    return False


def sha256(path: Path) -> str:
    h = hashlib.sha256()
    with path.open("rb") as fh:
        while chunk := fh.read(1 << 20):
            h.update(chunk)
    return h.hexdigest()


def candidate_dirs(db_path: Path | None) -> list[Path]:
    """Every directory a credential could be sitting in, new location first."""
    dirs = [NEW_STATE_DIR]
    if db_path is not None:
        dirs.append(db_path.parent)
    seen: set[str] = set()
    out: list[Path] = []
    for d in dirs:
        key = str(d)
        if key not in seen:
            seen.add(key)
            out.append(d)
    return out


def find_migrated_keys(legacy_dir: Path) -> list[Path]:
    """Previously renamed-aside keys, so a second run can see its own work."""
    try:
        return sorted(p for p in legacy_dir.iterdir() if p.name.startswith(MIGRATED_PREFIX))
    except OSError:
        return []


def build_plan(binary: Path) -> Plan:
    plan = Plan()
    plan.db_path = database_path()
    plan.notes.append(f"DATABASE_PATH (from the running unit) = {plan.db_path}")

    if os.geteuid() != 0:
        plan.notes.append(
            "NOT running as root. The app-data directory is 0700 audiobook:audiobook, "
            "so every path inside it will read as UNKNOWN rather than absent. Re-run "
            f"with: sudo python3 {sys.argv[0]} " + " ".join(sys.argv[1:])
        )

    plan.binary_has_marker = binary_has_marker(binary)
    plan.notes.append(
        f"deployed binary contains {CAPABILITY_MARKER.decode()}: "
        f"{'yes' if plan.binary_has_marker else 'NO (pre-#3171)'}  [pre-check only]"
    )

    plan.journal = read_journal()
    j = plan.journal
    if j.problem:
        plan.notes.append(f"journal unusable: {j.problem}")
    elif j.token_file is None:
        plan.notes.append(
            f"invocation {j.invocation} logged no {TOKEN_NEEDLE} line -- cannot observe "
            "which directory the running process resolves to"
        )
    else:
        plan.observed_state_dir = j.token_file.parent
        plan.notes.append(
            f"RUNNING process writes its token to {j.token_file}"
            + (f" (that value expired {j.token_expires})" if j.token_expires else "")
        )
        plan.notes.append(f"=> observed state dir = {plan.observed_state_dir}")
    if j.legacy_key_line:
        plan.notes.append(
            "the running process reported the encryption key at its OLD location, which "
            "is exactly the state this script exists to resolve:\n      " + j.legacy_key_line
        )

    # ---- tokens -------------------------------------------------------
    for d in candidate_dirs(plan.db_path):
        p = probe(d / TOKEN_NAME)
        if p.absent:
            continue
        if p.unknown:
            plan.left_alone.append(f"{p.describe()} -- cannot even confirm it exists")
            continue
        if j.token_file is not None and str(p.path) == str(j.token_file):
            plan.live_token = p
            plan.left_alone.append(
                f"{p.path} -- this is the LIVE token path. Its value expired "
                f"{j.token_expires or 'about ten minutes after startup'} and is rewritten "
                "on every restart, so it is stale-but-owned, not garbage. Remove it by "
                "hand if you want it gone before then."
            )
            continue
        if j.token_file is None:
            plan.left_alone.append(
                f"{p.path} -- present, but with no observed live token there is nothing "
                "to compare it against. Never guessing about a credential."
            )
            continue
        plan.stale_tokens.append(p)

    # ---- the encryption key -------------------------------------------
    if plan.db_path is None:
        plan.key_blockers.append("DATABASE_PATH is unknown, so the legacy directory is too")
        return plan

    legacy_dir = plan.db_path.parent
    plan.migrated_keys = find_migrated_keys(legacy_dir)
    src, dst = probe(legacy_dir / KEY_NAME), probe(NEW_STATE_DIR / KEY_NAME)
    plan.key_src, plan.key_dst = src, dst
    plan.notes.append(f"legacy key  {src.describe()}")
    plan.notes.append(f"new key     {dst.describe()}")
    for m in plan.migrated_keys:
        plan.notes.append(f"renamed aside by an earlier run: {m}")

    if str(legacy_dir) == str(NEW_STATE_DIR):
        plan.key_blockers.append("legacy and new directory are the same path; nothing to move")
        return plan

    # Destination must be READ from the running process, never asserted.
    if plan.observed_state_dir is None:
        plan.key_blockers.append(
            "cannot observe the running process's state dir (see above), so the "
            "destination is unproven. Refusing to guess -- that guess is the original bug."
        )
    elif str(plan.observed_state_dir) != str(NEW_STATE_DIR):
        plan.key_blockers.append(
            f"the running process resolves its state dir to {plan.observed_state_dir}, not "
            f"{NEW_STATE_DIR}. Either it predates #3171, or EnsureSecureStateDir took its "
            "fallback branch. Deploy/restart, then re-run; do not move the key toward a "
            "directory this process does not read."
        )
    elif not plan.binary_has_marker:
        plan.key_blockers.append(
            f"the running process looks post-#3171 but {binary} does not contain "
            f"{CAPABILITY_MARKER.decode()} -- the deployed file is not the one running. "
            "A restart would load the older binary and regenerate the key. Redeploy first."
        )

    if src.unknown or dst.unknown:
        plan.key_blockers.append(
            "cannot see one of the key paths, so neither its presence nor its absence is "
            "established. Re-run as root."
        )
    elif src.absent and dst.present:
        plan.key_blockers.append(f"already migrated: the key is at {dst.path}")
    elif src.absent and dst.absent:
        plan.key_blockers.append(
            f"no key at {src.path} or {dst.path}. If this host has encrypted settings, "
            "#3171's guard will refuse to start it; if it is a fresh install, one is "
            "generated on first run."
        )
    elif src.present and dst.present:
        same = sha256(src.path) == sha256(dst.path)
        plan.key_blockers.append(
            f"a key exists at BOTH {src.path} and {dst.path} -- "
            + (
                "identical, so the legacy one is a leftover copy; re-run with "
                "--remove-legacy-key to retire it safely"
                if same
                else "THEY DIFFER. Do not touch either. Work out which one decrypts the "
                "live settings before doing anything at all."
            )
        )
    return plan


def move_key(src: Path, dst: Path, owner: str, *, apply: bool) -> None:
    """Copy, verify, install, then RENAME the source aside. Never a bare move.

    ``/var/lib`` and the app-data dataset are different filesystems, so a
    rename would fall back to copy+unlink anyway -- doing it explicitly lets
    the digest be checked before anything about the source changes.

    The source is renamed, not deleted. Post-#3171 ``InitEncryption`` reads the
    new directory first and returns on success, never consulting the legacy
    dirs, so a leftover file there is inert -- and it is a free rollback for the
    one file in this system that cannot be regenerated. Nothing here has yet
    proven the app can READ from the new location; that only becomes provable
    after the next restart, which this script refuses to perform.
    """
    before = sha256(src)
    aside = src.with_name(MIGRATED_PREFIX + time.strftime("%Y%m%d%H%M%S"))
    print(f"    src sha256 = {before}")
    if not apply:
        print(f"    would copy   {src} -> {dst}  (0600, {owner}), verify")
        print(f"    would rename {src} -> {aside}  (kept as rollback, NOT deleted)")
        return

    try:
        ent = pwd.getpwnam(owner)
    except KeyError:
        raise Refused(
            f"service user {owner!r} does not exist; refusing to leave a key with wrong ownership"
        )

    dst.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    tmp = dst.with_name(dst.name + ".migrating")
    shutil.copyfile(src, tmp)
    if sha256(tmp) != before:
        tmp.unlink(missing_ok=True)
        raise Refused("copy digest mismatch; source left untouched")

    os.chmod(tmp, 0o600)
    os.chown(tmp, ent.pw_uid, ent.pw_gid)
    os.replace(tmp, dst)
    if sha256(dst) != before:
        raise Refused(f"post-move digest mismatch at {dst}; source NOT touched")
    print(f"    installed and verified -> {dst}")

    src.rename(aside)
    print(f"    renamed source aside   -> {aside}")
    print("    (kept on purpose: it is inert once the new path works, and it is the only")
    print("     rollback for a file that cannot be regenerated. Retire it later with")
    print("     --remove-legacy-key, which checks the app has since read the new path.)")


def legacy_removal_blockers(plan: Plan, migrated: Path) -> list[str]:
    """Why a renamed-aside key may not be deleted yet.

    The proof this looks for is structural rather than a log line. If the
    service is UP, started AFTER the rename, with the legacy name gone and a
    key at the new location, then it must have read the key from the new
    location -- because #3171's ``guardAgainstKeyRegeneration`` refuses to
    start at all when encrypted settings exist and no key is found. A running
    service is therefore the evidence.
    """
    out: list[str] = []
    if plan.observed_state_dir is None or str(plan.observed_state_dir) != str(NEW_STATE_DIR):
        out.append("the running process is not observably using the new state dir")
    if plan.key_dst is None or not plan.key_dst.present:
        out.append(f"no key confirmed at {NEW_STATE_DIR / KEY_NAME}")
    if plan.journal.legacy_key_line:
        out.append(
            "this invocation logged the OLD-location warning, so it is still depending "
            "on the legacy path"
        )
    started = process_start_time()
    if started is None:
        out.append("cannot determine when the running process started")
    else:
        try:
            renamed = migrated.stat().st_mtime
        except OSError as exc:
            out.append(f"cannot stat {migrated}: {exc}")
        else:
            if started <= renamed:
                out.append(
                    "the running process started BEFORE the rename, so it has not yet "
                    "demonstrated a startup that reads the new location. Wait for the "
                    "next restart (this script will not cause one)."
                )
    return out


def report_readonly_key() -> None:
    p = probe(NEW_STATE_DIR / READONLY_NAME)
    if p.present:
        print(f"  read-only key present at {p.path}")
    elif p.unknown:
        print(f"  read-only key: {p.describe()}")
    else:
        print(
            f"  no {READONLY_NAME} at {p.path} -- expected until the service next restarts "
            "with the new binary, or if write_startup_readonly_key is off. Not an error."
        )


def emit_json(plan: Plan) -> None:
    def d(pr: Probe | None) -> dict | None:
        return None if pr is None else {"path": str(pr.path), "presence": pr.presence.value, "reason": pr.reason}

    print(
        json.dumps(
            {
                "db_path": str(plan.db_path) if plan.db_path else None,
                "invocation": plan.journal.invocation,
                "binary_has_marker": plan.binary_has_marker,
                "observed_state_dir": str(plan.observed_state_dir) if plan.observed_state_dir else None,
                "live_token": d(plan.live_token),
                "stale_tokens": [str(p.path) for p in plan.stale_tokens],
                "key_src": d(plan.key_src),
                "key_dst": d(plan.key_dst),
                "migrated_keys": [str(p) for p in plan.migrated_keys],
                "key_blockers": plan.key_blockers,
                "left_alone": plan.left_alone,
            },
            indent=2,
        )
    )


def main() -> int:
    ap = argparse.ArgumentParser(
        description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter
    )
    mode = ap.add_mutually_exclusive_group()
    mode.add_argument("--apply", action="store_true", help="make changes (default is a dry run)")
    mode.add_argument(
        "--check-only", action="store_true", help="report state and exit (the default behaviour)"
    )
    ap.add_argument("--binary", type=Path, default=BINARY, help=f"deployed executable (default {BINARY})")
    ap.add_argument(
        "--remove-legacy-key",
        action="store_true",
        help="delete a previously renamed-aside key, if the app has since read the new location",
    )
    ap.add_argument(
        "--allow-key-move-without-proof",
        action="store_true",
        help="DANGEROUS: move the key even though the running process was not observed using "
        "the new directory. If it predates #3171 it will generate a new key on next restart "
        "and DELETE every secret it cannot recover from the config file.",
    )
    ap.add_argument("--json", action="store_true", help="emit the observed state as JSON as well")
    args = ap.parse_args()

    if not sys.platform.startswith("linux"):
        print(f"this script targets the Linux service host; running on {sys.platform}", file=sys.stderr)
        return 2
    if args.apply and os.geteuid() != 0:
        print(
            "--apply needs root: the key lives in a 0700 directory and must be chowned to "
            f"the service user.\n  sudo python3 {' '.join(sys.argv)}",
            file=sys.stderr,
        )
        return 2

    try:
        plan = build_plan(args.binary)
    except Refused as exc:
        print(f"REFUSED: {exc}", file=sys.stderr)
        return 1

    print("=== observed state ===")
    for n in plan.notes:
        print(f"  {n}")
    report_readonly_key()

    if args.json:
        emit_json(plan)

    print("\n=== 1. stale bootstrap tokens ===")
    if not plan.stale_tokens:
        print("  none identified as stale")
    for p in plan.stale_tokens:
        if not args.apply:
            print(f"  would remove {p.path}")
        else:
            p.path.unlink()
            print(f"  removed {p.path}")

    print("\n=== 2. encryption key ===")
    rc = 0
    if args.remove_legacy_key:
        if not plan.migrated_keys:
            print("  nothing renamed aside to remove")
        for m in plan.migrated_keys:
            blockers = legacy_removal_blockers(plan, m)
            if blockers:
                print(f"  REFUSED: keeping {m}")
                for b in blockers:
                    print(f"    - {b}")
                rc = 1
            elif not args.apply:
                print(f"  would remove {m} (all proofs satisfied)")
            else:
                m.unlink()
                print(f"  removed {m}")
    elif plan.key_blockers:
        movable = plan.key_src is not None and plan.key_src.present
        override = args.allow_key_move_without_proof and movable
        for b in plan.key_blockers:
            print(f"  {'WARNING (overridden)' if override else 'REFUSED'}: {b}")
        if override:
            print("  proceeding without proof because you asked for it")
            assert plan.key_src is not None and plan.key_dst is not None
            try:
                move_key(plan.key_src.path, plan.key_dst.path, service_user(), apply=args.apply)
            except Refused as exc:
                print(f"  REFUSED: {exc}", file=sys.stderr)
                return 1
        elif args.apply:
            rc = 1
    else:
        assert plan.key_src is not None and plan.key_dst is not None
        try:
            move_key(plan.key_src.path, plan.key_dst.path, service_user(), apply=args.apply)
        except Refused as exc:
            print(f"  REFUSED: {exc}", file=sys.stderr)
            return 1

    if plan.left_alone:
        print("\n=== left alone, on purpose ===")
        for item in plan.left_alone:
            print(f"  {item}")

    print("\n=== done ===")
    if not args.apply:
        print("  dry run -- nothing changed. Re-run with --apply.")
    else:
        print("  The service was NOT restarted, on purpose: a restart resumes the interrupted")
        print("  library scan. The running process already holds the key in memory, and the")
        print("  next restart for any other reason picks it up from the new location.")
    return rc


if __name__ == "__main__":
    try:
        sys.exit(main())
    except KeyboardInterrupt:
        print("interrupted", file=sys.stderr)
        sys.exit(130)
