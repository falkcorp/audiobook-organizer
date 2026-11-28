#!/usr/bin/env python3
# file: scripts/cutover_blockers_offhost.py
# version: 0.1.0
# guid: 8c14e7a2-5d09-4b63-a7f1-0e92c3b6d845
# last-edited: 2026-11-22

"""`make cutover-blockers-list` and `make cutover-blockers-apply`: the agents'
Mac-side way to run the root-owned cutover-blockers helpers, with no raw `sudo`
and no step typed by hand. R2 runbook (docs/system/r2-cutover-runbook.md) Step 0,
Path B. The owner's way is the matching script on the server (deploy/owner/).

The service must already be stopped on DEPLOY_HOST: the helpers refuse
otherwise and never stop or start it. The Makefile passes every value as an
environment variable of the recipe, never as text in a shell line:

    AORG_OH_WHAT     list | apply
    AORG_OH_HOST     DEPLOY_HOST          AORG_OH_BIN   DEPLOY_BIN
    AORG_OH_DB       DEPLOY_DB            AORG_OH_UNIT  DEPLOY_UNIT
    AORG_OH_GUARD    ROLLBACK_GUARD       AORG_OH_DIR   OFFHOST_DIR (default
                     ~/aorg-offhost/<UTC stamp>, created mode 0700)
    AORG_OH_REQUEST  apply only: the local request file (REQUEST)

list: runs `sudo -n .../cutover-blockers-list` (no arguments), then copies the
printed list (`aorg-cutover-blockers-<stamp>.txt`) and, when something blocks,
the rows with their values (`.json`) to the local directory, checks each file's
size and sha256 against what root reported, deletes the remote copies, and
prints the list.

apply: checks the local request (a regular file of yours, not a symlink, no
group or other access, at most 16 MiB, and valid by the helper's own parser),
refuses if the host already has a request, copies it to export_dir under the
fixed name `aorg-cutover-blockers-request.json`, sets it to mode 0600 and checks
its size and sha256 there, runs `sudo -n .../cutover-blockers-apply` (no
arguments), copies the result file back, checks it, deletes the remote copy and
prints it. The helper deletes the request itself after a successful apply. A
failed apply cannot say nothing changed: the request stays on the host, the
message says so, and the result file (when one was written) is still fetched.
The exception is a refusal before the helper started anything: it prints one
`NOT-RUN` line and exits 4, nothing ran and nothing changed, and this recipe
removes the request it placed (only if the file there is still the one it
placed, by inode) so a corrected request can be sent.

Names the host prints are held to a fixed grammar before they reach scp or
`rm`; host text is printed with control characters removed. Exit status: 0 done;
1 refused or failed. Standard library only. Tests:
scripts/tests/test_cutover_blockers_offhost.py (its Targets class runs the
recipes against the FakeHost of scripts/tests/test_make_rollback.py).
"""

from __future__ import annotations

import hashlib
import importlib.util
import os
import re
import shlex
import stat
import subprocess
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from deploy_rollback import load_guard  # noqa: E402
from export_offhost import SCP_OPTS, env, local_dir, sha256_of  # noqa: E402

HELPER_SRC = (
    Path(__file__).resolve().parents[1] / "deploy" / "rollback-helpers" / "rollback_helper.py"
)
REQUEST_NAME = "aorg-cutover-blockers-request.json"
_STAMP = r"[0-9]{8}T[0-9]{6}Z"
# The file names the helper creates (rollback_helper.BLOCKERS_NAMES).
_NAMES = {
    "list": re.compile(rf"aorg-cutover-blockers-{_STAMP}\.txt"),
    "rows": re.compile(rf"aorg-cutover-blockers-{_STAMP}\.json"),
    "result": re.compile(rf"aorg-cutover-blockers-result-{_STAMP}\.txt"),
}
_EXPORTED = re.compile(r"EXPORTED (\S+) ([0-9]{1,8}) ([0-9a-f]{64})")
_REMOVED = f"REQUEST-REMOVED {REQUEST_NAME}"
# A list may take as long as the helper lets the unit run (600 s) plus its stop.
OP_TIMEOUT = 720
STEP_TIMEOUT = 300
REQUEST_LIMIT = 16 << 20
# What each target asks the helper for: the guard's command key, the parts it
# must print (in order), and the ones it may report ABSENT.
WHAT = {
    "list": ("cutover_blockers_list", ("list",), ("rows",)),
    "apply": ("cutover_blockers_apply", ("result",), ()),
}


def fail(msg: str) -> int:
    print(f"cutover_blockers_offhost: {msg}", file=sys.stderr)
    return 1


def load_helper():
    """The helper module, for its request parser: the host's helper checks the
    same rules again, so this only fails early and with the same words."""
    spec = importlib.util.spec_from_file_location("rollback_helper_for_offhost", HELPER_SRC)
    assert spec is not None and spec.loader is not None
    mod = importlib.util.module_from_spec(spec)
    sys.modules[spec.name] = mod
    spec.loader.exec_module(mod)
    return mod


def parse(out: str, want: tuple[str, ...], optional: tuple[str, ...], removed: bool):
    """part -> (name, bytes, sha256) from the helper's stdout, or a refusal
    string. Exactly the wanted parts, each once and in order; an optional part
    may be `ABSENT <part>` instead; for apply, one REQUEST-REMOVED line is
    allowed (and required when removed is True)."""
    found: dict[str, tuple[str, int, str]] = {}
    absent: set[str] = set()
    saw_removed = False
    for line in out.splitlines():
        if not line.strip():
            continue
        if line == _REMOVED:
            if saw_removed or "result" not in want:
                return "the helper printed a line this recipe does not recognise"
            saw_removed = True
            continue
        m = re.fullmatch(r"ABSENT (\w+)", line)
        if m:
            if m.group(1) not in optional or m.group(1) in absent:
                return "the helper reported a part as absent that this recipe requires"
            absent.add(m.group(1))
            continue
        m = _EXPORTED.fullmatch(line)
        part = next((k for k, rx in _NAMES.items() if m and rx.fullmatch(m.group(1))), None)
        if m is None or part is None or part in found:
            return "the helper printed a line this recipe does not recognise"
        found[part] = (m.group(1), int(m.group(2)), m.group(3))
    for part in want:
        if part not in found:
            return f"the helper did not report the {part}"
    for part in optional:
        if (part in found) == (part in absent):
            return f"the helper reported the {part} neither once nor as absent"
    if set(found) - set(want) - set(optional):
        return "the helper reported a file this recipe did not ask for"
    if removed and not saw_removed:
        return "the helper did not report removing the request"
    return found


def check_request(raw: str) -> tuple[Path, bytes] | str:
    """The local request file as bytes, or why it is refused."""
    if not raw:
        return "REQUEST is not set: pass REQUEST=<local request file>"
    path = Path(raw)
    try:
        st = os.lstat(path)
    except OSError as e:
        return f"cannot read {path}: {e.strerror}"
    if not stat.S_ISREG(st.st_mode):
        return f"{path} is not a regular file (a symlink is refused)"
    if st.st_uid != os.getuid() or st.st_mode & 0o077:
        return (
            f"{path} must belong to you with no group or other access (chmod 600): it holds a "
            "value, and it travels as it is"
        )
    if st.st_size == 0 or st.st_size > REQUEST_LIMIT:
        return f"{path} is {st.st_size} bytes; it must be 1 to {REQUEST_LIMIT}"
    data = path.read_bytes()
    try:
        load_helper()._blockers_parse_request(data)
    except Exception as e:  # noqa: BLE001 (RefusedError's text names no value)
        return f"{path} is not a valid request: {e}"
    return path, data


def pull(guard, host: str, export_dir: str, dest: Path, files: dict) -> tuple[int, str]:
    """Copy each file to dest/<name>.part, check its size and sha256, rename it
    into place (0600), and delete the remote copies. (0, "") on success, else
    (1, why); on a bad copy the remote files are KEPT and the message says
    where."""
    remote = {k: f"{export_dir}/{name}" for k, (name, _, _) in files.items()}
    where = ", ".join(remote.values())
    rm_cmd = "rm -f -- " + " ".join(shlex.quote(p) for p in remote.values())
    parts: dict[str, Path] = {}
    for k, (name, size, sha) in files.items():
        if os.path.lexists(dest / name) or os.path.lexists(dest / f"{name}.part"):
            return 1, f"{dest / name} already exists; nothing was copied. On {host}: {where}"
        part = dest / f"{name}.part"
        p = subprocess.run(  # noqa: S603 (argv, no shell; the name matched a fixed grammar)
            ["scp", *SCP_OPTS, "--", f"{host}:{remote[k]}", str(part)],
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
            return 1, (
                f"the copy of {name} is not the file the helper wrote (scp exit {p.returncode}, "
                f"{got[0]} bytes, wanted {size}). The remote copies were KEPT: {where} on {host}. "
                f"Copy them by hand, or remove them with `ssh {host} {shlex.quote(rm_cmd)}` and "
                "run this target again"
            )
    for k, (name, _, _) in files.items():
        os.chmod(parts[k], 0o600)
        os.rename(parts[k], dest / name)
    d = guard.run_remote(host, rm_cmd, timeout=STEP_TIMEOUT)
    if d.rc != 0:
        return 1, (
            f"the copies are here and match, but deleting the remote ones failed (exit {d.rc}); "
            f"delete them by hand: ssh {host} {shlex.quote(rm_cmd)}"
        )
    return 0, ""


def upload_request(
    guard, host: str, export_dir: str, path: Path, data: bytes
) -> tuple[str, str]:
    """Put the request on the host under the fixed name, mode 0600, and check it
    there. ("", inode of the placed file) on success, else (why, ""). An
    existing remote request is never replaced."""
    remote = f"{export_dir}/{REQUEST_NAME}"
    q = shlex.quote(remote)
    r = guard.run_remote(host, f"test -e {q} -o -L {q} && echo PRESENT || echo ABSENT", timeout=60)
    state = guard.clean(r.stdout).strip()
    if r.rc != 0 or state not in ("PRESENT", "ABSENT"):
        return (
            f"cannot tell whether {remote} exists on {host} (ssh exit {r.rc}); nothing was copied"
        ), ""
    if state == "PRESENT":
        return (
            f"{host} already has a request at {remote}: an earlier apply may have failed, or "
            "another is waiting. Nothing was copied or run. Look at it, then remove it with "
            f"`ssh {host} {shlex.quote('rm -f -- ' + remote)}` and run this target again"
        ), ""
    p = subprocess.run(  # noqa: S603 (argv, no shell; the remote path is built from export_dir)
        ["scp", *SCP_OPTS, "--", str(path), f"{host}:{remote}"],
        check=False,
        timeout=STEP_TIMEOUT,
    )
    cleanup = f"remove it with `ssh {host} {shlex.quote('rm -f -- ' + remote)}`"
    if p.returncode != 0:
        return (
            f"scp of the request failed (exit {p.returncode}); if a partial file is there, {cleanup}",
            "",
        )
    sha = hashlib.sha256(data).hexdigest()
    cmd = f"chmod -- 600 {q} && stat -c '%a %s %u %i' -- {q} && sha256sum -- {q}"
    r = guard.run_remote(host, cmd, timeout=60)
    lines = guard.clean(r.stdout).strip().splitlines()
    ok = (
        r.rc == 0
        and len(lines) == 2
        and re.fullmatch(r"600 [0-9]+ [0-9]+ [0-9]+", lines[0]) is not None
        and int(lines[0].split()[1]) == len(data)
        and lines[1].split()[0] == sha
    )
    if not ok:
        return (
            f"the request on {host} is not the file that was sent (mode, size or sha256 differ; "
            f"ssh exit {r.rc}). Nothing was run. {cleanup}"
        ), ""
    return "", lines[0].split()[3]


def take_back_request(guard, host: str, export_dir: str, inode: str) -> str:
    """Remove the request this run placed, but only if the file at the fixed
    name is still that file (same inode, not a symlink). Says what happened."""
    remote = f"{export_dir}/{REQUEST_NAME}"
    q = shlex.quote(remote)
    cmd = (
        f'[ ! -L {q} ] && [ "$(stat -c %i -- {q})" = {int(inode)} ] && rm -f -- {q} '
        "&& echo REMOVED || echo KEPT"
    )
    r = guard.run_remote(host, cmd, timeout=60)
    if r.rc == 0 and guard.clean(r.stdout).strip() == "REMOVED":
        return f"the request this run sent was removed from {host}"
    return (
        f"the request at {remote} on {host} was not removed (it is not the file this run sent, "
        f"or ssh failed); look at it and remove it with `ssh {host} "
        f"{shlex.quote('rm -f -- ' + remote)}` before the next run"
    )


def main() -> int:
    what = env("AORG_OH_WHAT")
    if what not in WHAT:
        return fail(f"AORG_OH_WHAT {what!r} is not list or apply")
    request: tuple[Path, bytes] | None = None
    if what == "apply":
        checked = check_request(env("AORG_OH_REQUEST"))
        if isinstance(checked, str):
            return fail(f"{checked}; nothing was sent")
        request = checked
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
    placed = ""
    if request is not None:
        why, placed = upload_request(guard, o.host, o.export_dir, *request)
        if why:
            return fail(why)
    r = guard.run_remote(o.host, guard.commands(o)[key], timeout=OP_TIMEOUT)
    if r.stderr.strip():
        print(
            "the host said:\n  " + guard.clean(r.stderr).strip().replace("\n", "\n  "),
            file=sys.stderr,
        )
    stdout = guard.clean(r.stdout)
    if (
        what == "apply"
        and r.rc == load_helper().BLOCKERS_NOT_RUN_EXIT
        and stdout.strip() == load_helper().BLOCKERS_NOT_RUN
    ):
        return fail(
            "NOT RUN: the helper refused before it started (its message is above). Nothing "
            f"changed; {take_back_request(guard, o.host, o.export_dir, placed)}"
        )
    files = parse(stdout, want, optional, removed=what == "apply" and r.rc == 0)
    if r.rc != 0 and isinstance(files, str):
        files = {}
    if what == "apply" and r.rc != 0:
        msg = (
            f"the apply helper exited {r.rc}. The change MAY have been applied: run "
            f"`make cutover-blockers-list` to see what is left. The request is still on {o.host} "
            f"at {o.export_dir}/{REQUEST_NAME} unless the host said otherwise above"
        )
    elif r.rc != 0:
        return fail(f"the list helper exited {r.rc}; nothing was copied")
    else:
        msg = ""
    if isinstance(files, str):
        return fail(f"{files}; nothing was copied. Look in {o.export_dir} on {o.host}.")
    code, why = pull(guard, o.host, o.export_dir, dest, files) if files else (0, "")
    if code:
        return fail((msg + ". Also: " if msg else "") + why)
    for part, (name, size, sha) in files.items():
        print(f"{dest / name}  {size} bytes  sha256 {sha}")
        if part in ("list", "result"):
            print("-" * 60)
            print(guard.clean((dest / name).read_text(encoding="utf-8", errors="replace")).rstrip())
            print("-" * 60)
    if msg:
        return fail(msg)
    if what == "list":
        print(
            "The .json (when present) holds each blocking row's VALUE, which can be a credential "
            "hash: keep it off shared places and delete it once the rows are fixed."
        )
    else:
        print(
            "Applied. The request was removed on the host. Run `make cutover-blockers-list` to see "
            "what is left; delete your local request file, it holds the value."
        )
    return 0


if __name__ == "__main__":
    sys.exit(main())
