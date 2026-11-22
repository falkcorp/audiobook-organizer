#!/usr/bin/env python3
# file: scripts/export_offhost.py
# version: 0.1.0
# guid: 4b8d2f6e-9c31-4a57-b0e4-6f1a7d3c8e92
# last-edited: 2026-11-22

"""`make export-keyring` and `make export-install-secrets`: bring files that only
the service user can read off the deploy host, through the root-owned rollback
helpers, with no raw `sudo` and no step typed by hand.

R2 runbook (docs/system/r2-cutover-runbook.md) Step 7 (the keyring) and Step 1
(the install's `.encryption_key` and `config.yaml`). The Makefile passes every
value as an environment variable of the recipe, never as text in a shell line:

    AORG_OH_WHAT   keyring | install-secrets
    AORG_OH_HOST   DEPLOY_HOST          AORG_OH_BIN   DEPLOY_BIN
    AORG_OH_DB     DEPLOY_DB            AORG_OH_UNIT  DEPLOY_UNIT
    AORG_OH_GUARD  ROLLBACK_GUARD       AORG_OH_DIR   OFFHOST_DIR (default
                   ~/aorg-offhost/<UTC stamp>, created mode 0700)

What it does, in order, stopping at the first failure:

  1. Reads the helpers' rollback.conf on the host and refuses unless it names
     DEPLOY_BIN, DEPLOY_DB and DEPLOY_UNIT, and sets export_dir (the guard's
     load_host_config, as every other recipe does).
  2. Runs the one helper (`sudo -n .../export-keyring` or
     `.../export-install-secrets`, no arguments, the line the sudoers drop-in
     grants with ""). The helper runs the service-user work in a transient unit
     and leaves each file in export_dir, mode 0600, owned by the deploy user,
     under a fixed timestamped name, and prints `EXPORTED <name> <bytes>
     <sha256>` for each (the hash of what it wrote, computed by root).
  3. Copies each file back with scp to <local dir>/<name>.part and checks that its
     size and sha256 are the helper's. A mismatch deletes the local part, keeps
     the remote copies and says where they are: `secrets export-keyring`
     records the export before the copy happens, so health stops warning even
     when the copy is bad.
  4. Only when every file matches: renames them into place (0600) and deletes
     the remote copies. That is a plain `rm` as the deploy user, who owns them
     in a directory only it can enter; no helper, and no sudoers line, is needed
     for it.

Names the host prints are held to a fixed grammar before they reach scp or
`rm`. Host text is printed with control characters removed. Exit status: 0 done;
1 refused or failed. Standard library only. Tests:
scripts/tests/test_export_offhost.py.
"""

from __future__ import annotations

import hashlib
import os
import re
import shlex
import stat
import subprocess
import sys
import time
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from deploy_rollback import load_guard  # noqa: E402

# What each target runs, and what the helper must print for it.
WHAT = {
    "keyring": ("export_keyring", ("keyring",), ()),
    "install-secrets": ("export_install_secrets", ("encryption-key",), ("config",)),
}
_STAMP = r"[0-9]{8}T[0-9]{6}Z"
# The file names the helper creates (rollback_helper.EXPORT_NAMES).
_NAMES = {
    "keyring": re.compile(rf"aorg-keyring-{_STAMP}"),
    "encryption-key": re.compile(rf"aorg-encryption-key-{_STAMP}"),
    "config": re.compile(rf"aorg-config-{_STAMP}\.yaml"),
}
_EXPORTED = re.compile(r"EXPORTED (\S+) ([0-9]{1,8}) ([0-9a-f]{64})")
_ABSENT = re.compile(r"ABSENT config")
SCP_OPTS = ["-q", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10"]
STEP_TIMEOUT = 300


def fail(msg: str) -> int:
    print(f"export_offhost: {msg}", file=sys.stderr)
    return 1


def env(name: str) -> str:
    return os.environ.get(name, "")


def local_dir(raw: str) -> Path:
    """The local directory: OFFHOST_DIR, else ~/aorg-offhost/<UTC stamp>. Made
    mode 0700 when absent; one that exists must be a real directory of this
    user with no group or other access (reused for a second target)."""
    path = (
        Path(raw)
        if raw
        else Path.home() / "aorg-offhost" / time.strftime("%Y%m%dT%H%M%SZ", time.gmtime())
    )
    if os.path.lexists(path):
        st = os.lstat(path)
        if not stat.S_ISDIR(st.st_mode) or st.st_uid != os.getuid() or st.st_mode & 0o077:
            raise SystemExit(
                f"export_offhost: {path} exists and is not a directory of yours with mode 0700 "
                "(no symlink, no group or other access); nothing was copied"
            )
        return path
    path.parent.mkdir(parents=True, exist_ok=True)
    path.mkdir(mode=0o700)
    os.chmod(path, 0o700)
    return path


def parse(
    out: str, want: tuple[str, ...], optional: tuple[str, ...]
) -> dict[str, tuple[str, int, str]] | str:
    """kind -> (name, bytes, sha256) from the helper's lines, or a refusal.
    Exactly the wanted kinds, each once; an optional kind (config.yaml) may be
    ABSENT instead."""
    found: dict[str, tuple[str, int, str]] = {}
    absent: set[str] = set()
    for line in out.splitlines():
        if not line.strip():
            continue
        if _ABSENT.fullmatch(line):
            absent.add("config")
            continue
        m = _EXPORTED.fullmatch(line)
        kind = next((k for k, rx in _NAMES.items() if m and rx.fullmatch(m.group(1))), None)
        if m is None or kind is None or kind in found:
            return "the helper printed a line this recipe does not recognise"
        found[kind] = (m.group(1), int(m.group(2)), m.group(3))
    for kind in want:
        if kind not in found:
            return f"the helper did not report the {kind}"
    for kind in optional:
        if (kind in found) == (kind in absent):
            return f"the helper reported the {kind} neither once nor as absent"
    if set(found) - set(want) - set(optional):
        return "the helper reported a file this recipe did not ask for"
    return found


def sha256_of(path: Path) -> tuple[int, str]:
    h = hashlib.sha256()
    n = 0
    with open(path, "rb") as f:
        while chunk := f.read(1 << 20):
            h.update(chunk)
            n += len(chunk)
    return n, h.hexdigest()


def main() -> int:
    what = env("AORG_OH_WHAT")
    if what not in WHAT:
        return fail(f"AORG_OH_WHAT {what!r} is not keyring or install-secrets")
    guard = load_guard(env("AORG_OH_GUARD"))
    o = guard.Opts(
        host=env("AORG_OH_HOST"),
        db=env("AORG_OH_DB"),
        bin=env("AORG_OH_BIN"),
        unit=env("AORG_OH_UNIT") or "audiobook-organizer.service",
    )
    bad = guard._validate(o)
    if bad:
        return fail(f"{bad}. Nothing was changed.")
    refusal = guard.load_host_config(o, guard.SSH(o))
    if refusal:
        print("\n".join(refusal), file=sys.stderr)
        return 1
    if not o.export_dir:
        return fail(
            f"{guard.HOST_CONFIG} on {o.host} has no export_dir. Set it to a directory the deploy "
            "user owns (created as that user with `mkdir -m 700`) and reinstall the helpers "
            "(docs/system/deploy-and-gpu-ops.md, Rollback helpers). Nothing was changed."
        )
    try:
        dest = local_dir(env("AORG_OH_DIR"))
    except OSError as e:
        return fail(f"cannot make the local directory ({e.strerror}); nothing was copied")
    key, want, optional = WHAT[what]
    r = guard.run_remote(o.host, guard.commands(o)[key], timeout=STEP_TIMEOUT)
    if r.stderr.strip():
        print(
            "the host said:\n  " + guard.clean(r.stderr).strip().replace("\n", "\n  "),
            file=sys.stderr,
        )
    if r.rc != 0:
        return fail(f"the export helper exited {r.rc}; nothing was copied")
    files = parse(guard.clean(r.stdout), want, optional)
    if isinstance(files, str):
        return fail(f"{files}; nothing was copied. Look in {o.export_dir} on {o.host}.")
    remote = {k: f"{o.export_dir}/{name}" for k, (name, _, _) in files.items()}
    where = ", ".join(remote.values())
    parts: dict[str, Path] = {}
    for k, (name, size, sha) in files.items():
        if os.path.lexists(dest / name) or os.path.lexists(dest / f"{name}.part"):
            return fail(f"{dest / name} already exists; nothing was copied. On {o.host}: {where}")
        part = dest / f"{name}.part"
        p = subprocess.run(  # noqa: S603 (argv, no shell; the name matched a fixed grammar)
            ["scp", *SCP_OPTS, "--", f"{o.host}:{remote[k]}", str(part)],
            check=False,
            timeout=STEP_TIMEOUT,
        )
        parts[k] = part
        got = (-1, "")
        if p.returncode == 0 and part.is_file() and not part.is_symlink():
            got = sha256_of(part)
        if got != (size, sha):
            for q in parts.values():
                q.unlink(missing_ok=True)
            return fail(
                f"the copy of {name} is not the file the helper wrote (scp exit {p.returncode}, "
                f"{got[0]} bytes, wanted {size}). The remote copies were KEPT: {where} on {o.host}. "
                "Copy them by hand, or run this target again after removing them"
            )
    for k, (name, _, _) in files.items():
        os.chmod(parts[k], 0o600)
        os.rename(parts[k], dest / name)
    rm = "rm -f -- " + " ".join(shlex.quote(p) for p in remote.values())
    d = guard.run_remote(o.host, rm, timeout=STEP_TIMEOUT)
    for name, size, sha in files.values():
        print(f"{dest / name}  {size} bytes  sha256 {sha}")
    if d.rc != 0:
        return fail(
            f"the copies are here and match, but deleting the remote ones failed (exit {d.rc}); "
            f"delete them by hand: ssh {o.host} {shlex.quote(rm)}"
        )
    print(
        "The remote copies are deleted. These files hold secrets: whoever has them can decrypt "
        "the install's stored secrets and encrypted backups. Keep them off this machine's "
        "shared places, with the install's archive."
    )
    return 0


if __name__ == "__main__":
    sys.exit(main())
