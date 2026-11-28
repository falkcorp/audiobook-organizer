#!/usr/bin/env python3
# file: scripts/service_control_offhost.py
# version: 0.1.0
# guid: ad7aec98-2722-4d65-bcd3-5917d710bc0b
# last-edited: 2026-11-28

"""`make service-stop` and `make service-start`: stop or start ONLY the
audiobook-organizer service on DEPLOY_HOST, with a check before and a wait
after. R2 runbook (docs/system/r2-cutover-runbook.md) Step 0 stops the service
before the cutover-blockers helpers run and starts it again afterwards. The
owner's way is deploy/owner/service-stop.sh and service-start.sh.

It runs the two commands the deploy user's sudoers drop-in already grants
(`sudo -n /usr/bin/systemctl stop|start <unit>`, the guard's `stop` and `start`
keys); it adds no sudoers line. The Makefile passes every value as an
environment variable of the recipe:

    AORG_OH_WHAT   stop | start
    AORG_OH_HOST   DEPLOY_HOST      AORG_OH_UNIT   DEPLOY_UNIT
    AORG_OH_GUARD  ROLLBACK_GUARD   AORG_OH_DRY    1: look and say, change nothing
    AORG_OH_WAIT_SEC, AORG_OH_POLL_SEC   (optional) how long to wait for the
                   state, and how often to look (defaults 120 and 2)

stop: already inactive -> nothing to do. Otherwise stop, then wait until the
unit is `inactive`. start: already active -> nothing to do. Otherwise start,
then wait until it is `active`; `failed` ends the wait at once. Exit status:
0 in the wanted state; 1 refused, failed or timed out. Standard library only.
Tests: scripts/tests/test_service_control.py.
"""

from __future__ import annotations

import re
import sys
import time
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from deploy_rollback import load_guard  # noqa: E402
from export_offhost import env  # noqa: E402

# What each target needs: the guard's command key and the state it waits for.
WHAT = {"stop": ("stop", "inactive"), "start": ("start", "active")}
SHOW_TIMEOUT = 60
CHANGE_TIMEOUT = 300


def fail(msg: str) -> int:
    print(f"service_control_offhost: {msg}", file=sys.stderr)
    return 1


def seconds(name: str, default: float) -> float:
    raw = env(name)
    try:
        value = float(raw) if raw else default
    except ValueError:
        value = -1.0
    return value if 0 <= value <= 3600 else -1.0


def state_of(guard, o) -> str:
    """The unit's ActiveState, or "unreadable"."""
    r = guard.run_remote(o.host, guard.commands(o)["unit"], timeout=SHOW_TIMEOUT)
    m = re.search(r"^ActiveState=([a-z-]+)$", guard.clean(r.stdout), re.M)
    return m.group(1) if r.rc == 0 and m else "unreadable"


def main() -> int:
    what = env("AORG_OH_WHAT")
    if what not in WHAT:
        return fail(f"AORG_OH_WHAT {what!r} is not stop or start")
    key, wanted = WHAT[what]
    wait, poll = seconds("AORG_OH_WAIT_SEC", 120), seconds("AORG_OH_POLL_SEC", 2)
    if wait < 0 or poll < 0:
        return fail("AORG_OH_WAIT_SEC and AORG_OH_POLL_SEC must be numbers from 0 to 3600")
    guard = load_guard(env("AORG_OH_GUARD"))
    o = guard.Opts(
        host=env("AORG_OH_HOST"),
        db="",
        bin="",
        unit=env("AORG_OH_UNIT") or "audiobook-organizer.service",
    )
    bad = guard._validate(o)
    if bad:
        return fail(f"{bad}. Nothing was changed.")
    cmd = guard.commands(o)[key]
    state = state_of(guard, o)
    print(f"{o.unit} on {o.host} is {state}")
    if state == "unreadable":
        return fail(f"cannot read the state of {o.unit} on {o.host}; nothing was changed")
    if state == wanted:
        print(f"already {wanted}: nothing to do")
        return 0
    if env("AORG_OH_DRY") == "1":
        print(
            f"dry run only: would run `{cmd}` on {o.host} and wait for {wanted} (nothing was run)"
        )
        return 0
    r = guard.run_remote(o.host, cmd, timeout=CHANGE_TIMEOUT)
    if r.stderr.strip():
        print(
            "the host said:\n  " + guard.clean(r.stderr).strip().replace("\n", "\n  "),
            file=sys.stderr,
        )
    if r.rc != 0:
        return fail(
            f"`systemctl {what}` exited {r.rc} on {o.host}; the unit is now {state_of(guard, o)}"
        )
    deadline = time.monotonic() + wait
    while True:
        state = state_of(guard, o)
        if state == wanted:
            print(f"{o.unit} is {wanted}")
            return 0
        if state == "failed" and what == "start":
            return fail(
                f"{o.unit} is failed; read its journal on {o.host} (journalctl -u {o.unit})"
            )
        if time.monotonic() >= deadline:
            return fail(f"{o.unit} is still {state} after {wait:g} seconds; wanted {wanted}")
        time.sleep(poll)


if __name__ == "__main__":
    sys.exit(main())
